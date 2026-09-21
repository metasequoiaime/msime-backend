package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/metasequoiaime/MSIME-Backend/internal/account"
	"github.com/metasequoiaime/MSIME-Backend/internal/contract"
	"github.com/metasequoiaime/MSIME-Backend/internal/skins"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type skinJobOwnerKey struct{}

type bucket struct {
	tokens  float64
	updated time.Time
}
type Server struct {
	skinActive int
	skinOwners map[string]int

	skinJobs    map[string]*skinArtworkJob
	skinWorkers sync.WaitGroup

	adminStore  adminAuthStore
	adminGoogle *adminGoogleAuth
	accounts    *account.Service
	lifetime    context.Context
	stop        context.CancelFunc
	streams     sync.WaitGroup
	closed      bool
	config      Config
	client      *http.Client
	slots       chan struct{}
	mu          sync.Mutex
	buckets     map[string]bucket
	handler     http.Handler
}

func New(c Config) (*Server, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	s := &Server{config: c, slots: make(chan struct{}, c.MaxConcurrent), buckets: map[string]bucket{}, client: &http.Client{Timeout: time.Duration(c.TimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.lifetime, s.stop = context.WithCancel(context.Background())
	// 30 秒:启动时可能要顺带补迁移,空库要建二十多张表。独立的 -migrate-users 入口本来就按这个额度
	// 算,两边保持一致。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	s.accounts, err = account.New(ctx, c.Auth)
	if err != nil {
		s.stop()
		return nil, err
	}
	if c.Admin.Enabled {
		if err = s.accounts.AdminReady(ctx); err != nil {
			s.accounts.Close()
			s.stop()
			return nil, errors.New("admin database migration failed: " + err.Error())
		}
	}
	s.initAdminGoogle()
	s.accounts.ConfigureEngine(c.Engine)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/skins/generate", s.generateSkinArtwork)
	mux.HandleFunc("POST /v1/skins/jobs", s.createSkinArtworkJob)
	mux.HandleFunc("GET /v1/skins/jobs/{job}", s.getSkinArtworkJob)
	mux.HandleFunc("DELETE /v1/skins/jobs/{job}", s.deleteSkinArtworkJob)
	mux.HandleFunc("GET /v1/skins", s.skinCatalog)
	mux.HandleFunc("GET /v1/skins/{id}", s.skinDetails)
	mux.HandleFunc("GET /v1/skins/{id}/resources/{resource...}", s.skinResource)
	mux.HandleFunc("GET /v1/skins/source", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, skins.Source) })
	mux.HandleFunc("GET /v1/skins/license", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(skins.License())
	})
	account.Mount(mux, s.accounts)
	mux.HandleFunc("POST /v1/telemetry/events", s.accounts.Telemetry)
	mux.HandleFunc("POST /v1/input/{operation}", s.inputQuery)
	mux.HandleFunc("GET /v1/input/capabilities", s.inputCapabilities)
	mux.HandleFunc("GET /v1/catalog/{kind}", s.inputCatalog)
	mux.HandleFunc("GET "+contract.StreamingTranscriptionPath, s.streamTranscription)
	mux.HandleFunc("GET "+contract.HealthPath, func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET "+contract.CapabilitiesPath, s.capabilities)
	mux.HandleFunc("POST "+contract.ChatPath, s.chat)
	mux.HandleFunc("GET /v1/models", s.chatModels)
	mux.HandleFunc("POST "+contract.TranslationPath, s.translate)
	mux.HandleFunc("POST "+contract.TranscriptionPath, s.transcribe)
	mux.HandleFunc("GET "+contract.CloudPath, s.cloud)
	s.handler = s.middleware(mux)
	return s, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.serveAdmin(w, r) {
		return
	}
	if serveDocumentation(w, r, s.config.DocsEnabled) {
		return
	}
	s.handler.ServeHTTP(w, r)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Add("Vary", "Origin")
			allowed := origin == "https://"+r.Host || (r.TLS == nil && origin == "http://"+r.Host)
			for _, o := range s.config.AllowedOrigins {
				if origin == o {
					allowed = true
				}
			}
			if !allowed {
				fail(w, 403, "origin_denied")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if r.Method == "OPTIONS" {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.WriteHeader(204)
				return
			}
		}
		if r.URL.Path == contract.HealthPath || account.IsPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		var principal *Client
		supplied := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
		for i := range s.config.Clients {
			c := &s.config.Clients[i]
			expected := sha256.Sum256([]byte(c.token))
			if subtle.ConstantTimeCompare(supplied[:], expected[:]) == 1 && strings.HasPrefix(auth, "Bearer ") {
				principal = c
			}
		}
		if principal == nil && s.accounts != nil && strings.HasPrefix(auth, "Bearer ") {
			authCtx, authCancel := context.WithTimeout(r.Context(), 5*time.Second)
			p, err := s.accounts.Authenticate(authCtx, strings.TrimPrefix(auth, "Bearer "))
			authCancel()
			if err == nil {
				principal = &Client{ID: "user:" + p.UserID, RequestsPerMinute: 120}
			} else if !errors.Is(err, account.ErrInvalid) {
				fail(w, 503, "auth_unavailable")
				return
			}
		}
		if principal == nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "unauthorized")
			return
		}
		if !s.allow(*principal, time.Now()) {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "rate_limit_exceeded")
			return
		}
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			w.Header().Set("Retry-After", "1")
			fail(w, 503, "server_busy")
			return
		}
		timeout := time.Duration(s.config.TimeoutSeconds) * time.Second
		if r.Method == "POST" && r.URL.Path == "/v1/skins/generate" {
			timeout = 180 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		ctx = context.WithValue(ctx, skinJobOwnerKey{}, principal.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (s *Server) allow(c Client, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buckets) > 10000 {
		for id, b := range s.buckets {
			if now.Sub(b.updated) > 10*time.Minute {
				delete(s.buckets, id)
			}
		}
		if _, exists := s.buckets[c.ID]; !exists && len(s.buckets) > 10000 {
			return false
		}
	}
	b, ok := s.buckets[c.ID]
	if !ok {
		b = bucket{float64(c.RequestsPerMinute), now}
	}
	b.tokens = min(float64(c.RequestsPerMinute), b.tokens+now.Sub(b.updated).Seconds()*float64(c.RequestsPerMinute)/60)
	b.updated = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	s.buckets[c.ID] = b
	return allowed
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := strings.Split(r.Header.Get("Content-Type"), ";")[0]; ct != "application/json" {
		fail(w, 415, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, contract.JsonBodyBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "invalid_json")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		fail(w, 400, "invalid_json")
		return false
	}
	return true
}
func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, map[string]any{"api_version": contract.APIVersion, "cloud": s.config.Cloud.URL != "", "chat": s.config.Chat.URL != "", "translation": s.config.Translation.URL != "", "transcription": s.config.Transcription.URL != "", "streaming_transcription": s.config.Streaming.URL != ""})
}
func (s *Server) upstream(r *http.Request, e Endpoint, method, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), method, e.URL, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	return s.doUpstream(req)
}

func (s *Server) doUpstream(req *http.Request) ([]byte, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("upstream rejected request")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, contract.UpstreamResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > contract.UpstreamResponseBytes || !json.Valid(b) {
		return nil, errors.New("invalid upstream response")
	}
	return b, nil
}
// 每一条 502/504 都要留下痕迹。
//
// 这里此前只写响应、不记日志,而 502 对客户端来说只是「上游失败」四个字。一次真实排查为此翻遍了
// 隧道、网关、上游、令牌和额度 —— 服务端明明握着原因(是传输错误,还是响应通过了但没过校验),
// 却一个字都没留下。cause 为空恰恰是最需要说明的那一种:请求成功了,是我们自己拒绝了响应。
func upstreamError(w http.ResponseWriter, r *http.Request, cause error) {
	var networkError net.Error
	timeout := errors.Is(cause, context.DeadlineExceeded) || (errors.As(cause, &networkError) && networkError.Timeout())
	reason := "response rejected by validation"
	if cause != nil {
		reason = cause.Error()
	}
	if r.Context().Err() != nil || timeout {
		slog.Warn("upstream timed out", "path", r.URL.Path, "reason", reason)
		fail(w, 504, "upstream_timeout")
	} else {
		slog.Error("upstream failed", "path", r.URL.Path, "reason", reason)
		fail(w, 502, "upstream_failure")
	}
}
func (s *Server) proxyJSON(w http.ResponseWriter, r *http.Request, e Endpoint, v any, validate func([]byte) bool) {
	b, err := json.Marshal(v)
	if err != nil {
		fail(w, 400, "invalid_request")
		return
	}
	result, err := s.upstream(r, e, "POST", "application/json", bytes.NewReader(b))
	if err != nil || !validate(result) {
		upstreamError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(result)
}
func enabled(w http.ResponseWriter, e Endpoint) bool {
	if e.URL == "" {
		fail(w, 503, "feature_disabled")
		return false
	}
	return true
}
func bounded(v string, n int) bool { return strings.TrimSpace(v) != "" && len(v) <= n }
func intQuery(r *http.Request, name string, defaultValue, maximum int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return defaultValue, true
	}
	i, err := strconv.Atoi(v)
	return i, err == nil && i > 0 && i <= maximum
}

// CloseAccounts 应在 HTTP 请求排空后调用，释放用户数据库资源。
func (s *Server) CloseAccounts() { s.accounts.Close() }
