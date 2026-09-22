package account

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/metasequoiaime/MSIME-Backend/internal/engine"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Service struct {
	engine    engine.Config
	store     *Store
	config    Config
	client    *http.Client
	verifiers map[string]Verifier
	sender    Sender
	cancel    context.CancelFunc
	done      chan struct{}
	once      sync.Once
}

func New(ctx context.Context, c Config) (*Service, error) {
	if !c.Enabled {
		return nil, nil
	}
	if e := c.Validate(); e != nil {
		return nil, e
	}
	db, e := Open(ctx, os.Getenv(c.DatabaseEnv))
	if e != nil {
		return nil, e
	}
	if e = db.Ready(ctx); e != nil {
		// 缺表就地补上,不要求运维记得先跑一次 -migrate-users。这样做是安全的:所有 DDL 都是
		// CREATE ... IF NOT EXISTS / ADD COLUMN IF NOT EXISTS,纯增量、可重复执行,而 Migrate 自己在
		// 事务里拿 advisory lock,多副本同时启动会串行,后到的跑成空操作。
		//
		// 已经迁移过的库走不到这里,所以运行时角色被收走 DDL 权限的部署不会因为这段而尝试建表;
		// 真缺表又没权限时,下面的错误会把原因说清楚,再由有权限的角色跑 -migrate-users。
		if migrated := db.MigrateAs(ctx, c.MigrationRole); migrated != nil {
			db.Close()
			// 生产建议的做法是运行角色只拿 DML 权限、由有 DDL 权限的账号迁移(见 docs/user-auth.md),
			// 那种部署下这里必然失败。所以错误里要把「用另一个账号跑 -migrate-users」说出来,不能只
			// 甩一句 permission denied。
			return nil, errors.New("用户数据库缺少必需的表，自动迁移失败：" + migrated.Error() +
				"（运行角色无 DDL 权限时，请把 migration_role 配成库的属主角色并把它授予运行角色，" +
				"或用有 DDL 权限的账号执行 -migrate-users）")
		}
		if e = db.Ready(ctx); e != nil {
			db.Close()
			return nil, errors.New("用户数据库迁移后仍缺少必需的表：" + e.Error())
		}
	}
	lifetime, cancel := context.WithCancel(context.Background())
	a := &Service{store: db, config: c, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cancel: cancel, done: make(chan struct{})}
	a.verifiers = makeVerifiers(lifetime, c, a.client)
	a.sender = delivery{config: c}
	go func() {
		defer close(a.done)
		timer := time.NewTicker(time.Hour)
		defer timer.Stop()
		for {
			select {
			case <-lifetime.Done():
				return
			case <-timer.C:
				cleanup, cancel := context.WithTimeout(lifetime, 30*time.Second)
				a.store.Prune(cleanup)
				cancel()
			}
		}
	}()
	return a, nil
}

// ConfigureEngine is called once during server construction, before serving requests.
func (a *Service) ConfigureEngine(c engine.Config) {
	if a != nil {
		a.engine = c
	}
}
func (a *Service) Close() {
	if a == nil {
		return
	}
	a.once.Do(func() { a.cancel(); <-a.done; a.store.Close() })
}
func (a *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if a == nil {
		return Principal{}, ErrInvalid
	}
	return a.store.Authenticate(ctx, token)
}
func IsPath(path string) bool {
	return strings.HasPrefix(path, "/v1/community/") || strings.HasPrefix(path, "/v1/auth/") || path == "/v1/users/me" || strings.HasPrefix(path, "/v1/users/me/")
}
func Mount(mux *http.ServeMux, a *Service) {
	for pattern, method := range map[string]func(*Service, http.ResponseWriter, *http.Request){
		"POST /v1/community/resources/{id}/apply": (*Service).resourceApply,
		"GET /v1/community/resources":             (*Service).resourceList,
		"POST /v1/community/resources":            (*Service).resourcePublish,
		"GET /v1/community/resources/{id}":        (*Service).resourceDetail,
		"DELETE /v1/community/resources/{id}":     (*Service).resourceDelete,
		"PUT /v1/community/resources/{id}/save":   (*Service).resourceSave,
		"PUT /v1/community/resources/{id}/rating": (*Service).resourceRate,
		"GET /v1/community/skins":                 (*Service).communityList,
		"POST /v1/community/skins":                (*Service).communityPublish,
		"GET /v1/community/skins/{id}":            (*Service).communityDetail,
		"DELETE /v1/community/skins/{id}":         (*Service).communityDelete,
		"POST /v1/community/skins/{id}/download":  (*Service).communityDownload,
		"PUT /v1/community/skins/{id}/rating":     (*Service).communityRate,

		"DELETE /v1/users/me/dictionary/candidates":         (*Service).candidateDelete,
		"PUT /v1/users/me/dictionary/snapshot":              (*Service).restoreDictionarySnapshot,
		"GET /v1/users/me/dictionary/snapshot":              (*Service).dictionarySnapshot,
		"POST /v1/users/me/dictionary/ranking":              (*Service).candidateRanking,
		"GET /v1/users/me/dictionary/positions":             (*Service).candidatePositions,
		"PUT /v1/users/me/dictionary/positions":             (*Service).candidatePositions,
		"DELETE /v1/users/me/dictionary/positions":          (*Service).candidatePositions,
		"POST /v1/users/me/dictionary/candidates":           (*Service).dictionaryQuery,
		"POST /v1/users/me/dictionaries/{kind}/edit":        (*Service).dictionaryManage,
		"GET /v1/users/me/dictionaries/{kind}/catalog":      (*Service).dictionaryCatalog,
		"GET /v1/users/me/dictionaries/{kind}":              (*Service).dictionary,
		"POST /v1/users/me/dictionaries/{kind}":             (*Service).dictionary,
		"PUT /v1/users/me/dictionaries/{kind}/{id}":         (*Service).dictionary,
		"DELETE /v1/users/me/dictionaries/{kind}/{id}":      (*Service).dictionary,
		"POST /v1/users/me/dictionaries/{kind}/import-hans": (*Service).dictionaryImportHans,
		"POST /v1/users/me/dictionaries/{kind}/import":      (*Service).dictionaryImport,
		"GET /v1/users/me/dictionaries/{kind}/export":       (*Service).dictionaryExport,
		"GET /v1/users/me/dictionary/changes":               (*Service).dictionaryChanges,
		"GET /v1/users/me/clipboard":                        (*Service).clipboard,
		"POST /v1/users/me/clipboard":                       (*Service).clipboard,
		"DELETE /v1/users/me/clipboard":                     (*Service).clipboard,
		"DELETE /v1/users/me/clipboard/{id}":                (*Service).clipboard,
		"PUT /v1/users/me/clipboard/settings":               (*Service).clipboardSettings,
		"GET /v1/users/me/preferences":                      (*Service).preferences,
		"PUT /v1/users/me/preferences":                      (*Service).preferences,
		"GET /v1/users/me/preferences/schema":               (*Service).preferencesSchema,
		"GET /v1/auth/providers":                            (*Service).providers,
		"POST /v1/auth/challenges":                          (*Service).begin,
		"POST /v1/auth/login":                               (*Service).login,
		"POST /v1/auth/refresh":                             (*Service).refresh,
		"POST /v1/auth/logout":                              (*Service).logout,
		"GET /v1/users/me":                                  (*Service).me,
		"PATCH /v1/users/me":                                (*Service).update,
		"DELETE /v1/users/me":                               (*Service).delete,
	} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if a == nil {
				writeError(w, 503, "user_auth_disabled")
				return
			}
			timeout := 15 * time.Second
			if pattern == "PUT /v1/users/me/dictionary/snapshot" {
				timeout = snapshotRestoreTimeout
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			r = r.WithContext(ctx)
			host, _, e := net.SplitHostPort(r.RemoteAddr)
			if e != nil {
				host = r.RemoteAddr
			}
			// 默认只信任 TCP 对端，不使用可伪造的转发头。代理配置见部署文档。
			if e = a.store.Rate(ctx, "ip:"+hash(host), 120, time.Minute); e != nil {
				a.error(w, e)
				return
			}
			method(a, w, r)
		})
	}
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}
func (a *Service) error(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrInvalid):
		writeError(w, 401, "invalid_credentials")
	case errors.Is(e, ErrLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "rate_limit_exceeded")
	case errors.Is(e, ErrConflict):
		writeError(w, 409, "identity_already_linked")
	default:
		writeError(w, 503, "auth_unavailable")
	}
}
func read(w http.ResponseWriter, r *http.Request, v any) bool {
	return readSized(w, r, v, 16384)
}
func readSized(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		writeError(w, 415, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		writeError(w, 400, "invalid_json")
		return false
	}
	return true
}
func (a *Service) principal(w http.ResponseWriter, r *http.Request, fresh bool) (Principal, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		writeError(w, 401, "user_session_required")
		return Principal{}, false
	}
	p, e := a.Authenticate(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if e != nil {
		a.error(w, e)
		return p, false
	}
	if fresh && time.Since(p.CreatedAt) > 10*time.Minute {
		writeError(w, 403, "recent_login_required")
		return p, false
	}
	return p, true
}
func (a *Service) enabled(provider string) bool {
	switch provider {
	case "google":
		return len(a.config.Google.ClientIDs) > 0
	case "apple":
		return len(a.config.Apple.ClientIDs) > 0
	case "wechat":
		return a.config.Wechat.AppID != ""
	case "email":
		return a.config.Email.From != ""
	case "phone":
		return a.config.SMS.TemplateCode != ""
	case "anonymous":
		return a.config.Anonymous.Enabled
	default:
		return false
	}
}
func (a *Service) providers(w http.ResponseWriter, r *http.Request) {
	m := map[string]bool{}
	for _, p := range []string{"apple", "google", "wechat", "phone", "email", "anonymous"} {
		m[p] = a.enabled(p)
	}
	write(w, 200, map[string]any{"providers": m})
}
func (a *Service) codeHash(challenge, code string) string {
	mac := hmac.New(sha256.New, []byte(os.Getenv(a.config.PepperEnv)))
	mac.Write([]byte(challenge + ":" + code))
	return hex.EncodeToString(mac.Sum(nil))
}
func normalize(provider, target string) (string, error) {
	target = strings.TrimSpace(target)
	if provider == "email" {
		v, e := mail.ParseAddress(target)
		if e != nil || v.Address != target || len(target) > 254 || strings.ContainsAny(target, "\r\n") {
			return "", ErrInvalid
		}
		return strings.ToLower(target), nil
	}
	if provider == "phone" && regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`).MatchString(target) {
		return target, nil
	}
	return "", ErrInvalid
}
func (a *Service) begin(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Provider string `json:"provider"`
		Target   string `json:"target"`
		Purpose  string `json:"purpose"`
	}
	if !read(w, r, &v) {
		return
	}
	if !a.enabled(v.Provider) {
		writeError(w, 503, "provider_disabled")
		return
	}
	if v.Purpose != "" && v.Purpose != "login" && v.Purpose != "link" {
		writeError(w, 400, "invalid_purpose")
		return
	}
	c := Challenge{Provider: v.Provider}
	id := randomToken()
	c.IDHash = hash(id)
	if v.Purpose == "link" {
		p, ok := a.principal(w, r, true)
		if !ok {
			return
		}
		c.LinkUser = p.UserID
	}
	response := map[string]any{"challenge_id": id, "expires_in": 300}
	if v.Provider == "anonymous" {
		// 开户没有门槛,拦在这一层:每个地址每天有限额,否则一段脚本就能把账号和 AI 额度刷穿。
		limit := a.config.Anonymous.DailyPerAddress
		if limit <= 0 {
			limit = 5
		}
		host, hostErr := "", error(nil)
		if host, _, hostErr = net.SplitHostPort(r.RemoteAddr); hostErr != nil {
			host = r.RemoteAddr
		}
		// 和上面的通用限流一样只信任 TCP 对端,转发头可伪造。
		if e := a.store.Rate(r.Context(), "anonymous:"+hash(host), limit, 24*time.Hour); e != nil {
			a.error(w, e)
			return
		}
		subject := strings.TrimSpace(v.Target)
		if len(subject) < 8 || len(subject) > 64 {
			writeError(w, 400, "invalid_target")
			return
		}
		for _, r := range subject {
			if !(r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z')) {
				writeError(w, 400, "invalid_target")
				return
			}
		}
		c.Subject = subject
	}
	var code string
	if v.Provider == "email" || v.Provider == "phone" {
		target, e := normalize(v.Provider, v.Target)
		if e != nil {
			writeError(w, 400, "invalid_target")
			return
		}
		c.Subject = target
		for _, lim := range []struct {
			key    string
			n      int
			window time.Duration
		}{{"target-minute", 1, time.Minute}, {"target-hour", 5, time.Hour}, {"target-day", 10, 24 * time.Hour}} {
			if e = a.store.Rate(r.Context(), lim.key+":"+a.codeHash(v.Provider, target), lim.n, lim.window); e != nil {
				a.error(w, e)
				return
			}
		}
		if e = a.store.Rate(r.Context(), "delivery-global", 500, 24*time.Hour); e != nil {
			a.error(w, e)
			return
		}
		n, e := rand.Int(rand.Reader, big.NewInt(1000000))
		if e != nil {
			a.error(w, e)
			return
		}
		code = n.String()
		code = strings.Repeat("0", 6-len(code)) + code
		c.CodeHash = a.codeHash(id, code)
	} else {
		c.Nonce = randomToken()
		response["nonce"] = c.Nonce
		if v.Provider == "wechat" {
			values := url.Values{"appid": {a.config.Wechat.AppID}, "redirect_uri": {a.config.Wechat.RedirectURI}, "response_type": {"code"}, "scope": {"snsapi_login"}, "state": {id}}
			response["authorization_url"] = "https://open.weixin.qq.com/connect/qrconnect?" + values.Encode() + "#wechat_redirect"
		}
	}
	if e := a.store.PutChallenge(r.Context(), c); e != nil {
		a.error(w, e)
		return
	}
	if code != "" {
		if e := a.sender.Send(r.Context(), v.Provider, c.Subject, code); e != nil {
			a.store.DropChallenge(r.Context(), c.IDHash)
			a.error(w, e)
			return
		}
	}
	write(w, 201, response)
}
func (a *Service) login(w http.ResponseWriter, r *http.Request) {
	var v struct {
		ChallengeID string `json:"challenge_id"`
		Credential  string `json:"credential"`
	}
	if !read(w, r, &v) {
		return
	}
	if len(v.ChallengeID) != 64 || v.Credential == "" || len(v.Credential) > 12000 {
		a.error(w, ErrInvalid)
		return
	}
	c, e := a.store.Attempt(r.Context(), v.ChallengeID)
	if e != nil {
		a.error(w, e)
		return
	}
	if c.LinkUser != "" {
		p, ok := a.principal(w, r, true)
		if !ok {
			return
		}
		if p.UserID != c.LinkUser {
			a.error(w, ErrInvalid)
			return
		}
	}
	var identity Identity
	if c.Provider == "email" || c.Provider == "phone" {
		if len(v.Credential) != 6 || subtle.ConstantTimeCompare([]byte(c.CodeHash), []byte(a.codeHash(v.ChallengeID, v.Credential))) != 1 {
			a.error(w, ErrInvalid)
			return
		}
		identity = Identity{c.Provider, c.Subject}
	} else if c.Provider == "anonymous" {
		// 口令只存 HMAC,和邮箱验证码同一套 pepper。首次登录即建号,之后同一对凭据取回同一个账号。
		if len(v.Credential) < 32 || len(v.Credential) > 256 {
			a.error(w, ErrInvalid)
			return
		}
		identity = Identity{c.Provider, c.Subject + ":" + a.codeHash("anonymous", v.Credential)}
	} else {
		identity, e = a.identity(r.Context(), c, v.Credential)
		if e != nil {
			a.error(w, e)
			return
		}
	}
	t, e := a.store.Complete(r.Context(), c, identity)
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 200, t)
}
func (a *Service) refresh(w http.ResponseWriter, r *http.Request) {
	var v struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !read(w, r, &v) {
		return
	}
	t, e := a.store.Refresh(r.Context(), v.RefreshToken)
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 200, t)
}
func (a *Service) logout(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	var v struct {
		All bool `json:"all"`
	}
	if !read(w, r, &v) {
		return
	}
	if e := a.store.Logout(r.Context(), p, v.All); e != nil {
		a.error(w, e)
		return
	}
	w.WriteHeader(204)
}
func (a *Service) me(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	u, ids, e := a.store.Me(r.Context(), p.UserID)
	if e != nil {
		a.error(w, e)
		return
	}
	write(w, 200, map[string]any{"user": u, "identities": ids})
}
func (a *Service) update(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, false)
	if !ok {
		return
	}
	var v struct {
		Name string `json:"display_name"`
	}
	if !read(w, r, &v) {
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if !utf8.ValidString(v.Name) || utf8.RuneCountInString(v.Name) > 64 {
		writeError(w, 400, "invalid_display_name")
		return
	}
	if e := a.store.UpdateName(r.Context(), p.UserID, v.Name); e != nil {
		a.error(w, e)
		return
	}
	w.WriteHeader(204)
}
func (a *Service) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r, true)
	if !ok {
		return
	}
	if e := a.store.DeleteUser(r.Context(), p.UserID); e != nil {
		a.error(w, e)
		return
	}
	w.WriteHeader(204)
}
