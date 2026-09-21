package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Jobs are short-lived drafts, not saved skins. The current single-instance
// deployment retains at most eight bounded image responses for ten minutes.
const skinJobTTL = 10 * time.Minute
const maxSkinJobs = 8

type skinArtworkJob struct {
	owner   string
	expires time.Time
	cancel  context.CancelFunc
	state   string
	artwork json.RawMessage
	// 失败的原因。此前只记 state="failed",而 generateSkinArtwork 已经算出过具体是哪一种
	// (invalid_skin_artwork / upstream_failure / upstream_timeout),那一步把它丢了 —— 客户端
	// 只知道「失败」,于是自己编了一个 502,而服务端日志里什么也没有。谁都查不下去。
	reason string
}

func (s *Server) expireSkinJobs(now time.Time) {
	for id, job := range s.skinJobs {
		if !now.Before(job.expires) {
			job.cancel()
			delete(s.skinJobs, id)
		}
	}
}

func (s *Server) createSkinArtworkJob(w http.ResponseWriter, r *http.Request) {
	if !enabled(w, s.config.Images) {
		return
	}
	var input struct {
		Prompt string `json:"prompt"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if !utf8.ValidString(input.Prompt) || utf8.RuneCountInString(input.Prompt) < 1 || utf8.RuneCountInString(input.Prompt) > 1200 || strings.ContainsRune(input.Prompt, 0) {
		fail(w, 400, "invalid_skin_prompt")
		return
	}
	owner, _ := r.Context().Value(skinJobOwnerKey{}).(string)
	if owner == "" {
		fail(w, 401, "unauthorized")
		return
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		fail(w, 503, "job_unavailable")
		return
	}
	id := hex.EncodeToString(nonce[:])
	s.mu.Lock()
	s.expireSkinJobs(time.Now())
	count := 0
	for _, job := range s.skinJobs {
		if job.owner == owner {
			count++
		}
	}
	if s.closed || len(s.skinJobs) >= min(maxSkinJobs, s.config.MaxConcurrent) || count >= 3 || s.skinOwners[owner] >= 3 || s.skinActive >= min(maxSkinJobs, s.config.MaxConcurrent) {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		fail(w, 503, "skin_jobs_busy")
		return
	}
	if s.skinJobs == nil {
		s.skinJobs = make(map[string]*skinArtworkJob)
	}
	ctx, cancel := context.WithTimeout(s.lifetime, 180*time.Second)
	job := &skinArtworkJob{owner: owner, expires: time.Now().Add(skinJobTTL), cancel: cancel, state: "running"}
	s.skinJobs[id] = job
	if s.skinOwners == nil {
		s.skinOwners = make(map[string]int)
	}
	s.skinOwners[owner]++
	s.skinActive++
	s.skinWorkers.Add(1)
	s.mu.Unlock()
	body, _ := json.Marshal(input)
	go func() {
		defer s.skinWorkers.Done()
		defer cancel()
		// Reuse the same fixed upstream, prompt rules and image validation as the
		// synchronous compatibility endpoint; never retain the user's bearer token.
		request, _ := http.NewRequestWithContext(ctx, "POST", "/v1/skins/generate", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := &skinJobResponse{header: make(http.Header)}
		s.generateSkinArtwork(response, request)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.skinActive--
		s.skinOwners[owner]--
		if s.skinOwners[owner] == 0 {
			delete(s.skinOwners, owner)
		}
		if s.skinJobs[id] != job {
			return
		}
		if response.status == http.StatusOK && ctx.Err() == nil {
			job.state = "succeeded"
			job.artwork = append(json.RawMessage(nil), response.body.Bytes()...)
		} else {
			job.state = "failed"
			job.reason = skinFailureReason(response, ctx.Err())
			slog.Error("skin artwork job failed", "job", id, "status", response.status, "reason", job.reason)
		}
	}()
	w.Header().Set("Location", "/v1/skins/jobs/"+id)
	respond(w, http.StatusAccepted, map[string]any{"id": id, "state": "running", "expires_at": job.expires.UTC().Format(time.RFC3339)})
}

func (s *Server) getSkinArtworkJob(w http.ResponseWriter, r *http.Request) {
	owner, _ := r.Context().Value(skinJobOwnerKey{}).(string)
	s.mu.Lock()
	s.expireSkinJobs(time.Now())
	job := s.skinJobs[r.PathValue("job")]
	if job == nil || owner == "" || job.owner != owner {
		s.mu.Unlock()
		fail(w, 404, "skin_job_not_found")
		return
	}
	state, artwork, reason := job.state, job.artwork, job.reason
	s.mu.Unlock()
	if state == "running" {
		w.Header().Set("Retry-After", "5")
	}
	payload := map[string]any{"id": r.PathValue("job"), "state": state, "artwork": artwork}
	if reason != "" {
		payload["reason"] = reason
	}
	respond(w, 200, payload)
}

func (s *Server) deleteSkinArtworkJob(w http.ResponseWriter, r *http.Request) {
	owner, _ := r.Context().Value(skinJobOwnerKey{}).(string)
	s.mu.Lock()
	job := s.skinJobs[r.PathValue("job")]
	if job == nil || owner == "" || job.owner != owner {
		s.mu.Unlock()
		fail(w, 404, "skin_job_not_found")
		return
	}
	job.cancel()
	delete(s.skinJobs, r.PathValue("job"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

type skinJobResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *skinJobResponse) Header() http.Header            { return w.header }
func (w *skinJobResponse) WriteHeader(status int)         { w.status = status }
func (w *skinJobResponse) Write(data []byte) (int, error) { return w.body.Write(data) }

// 把同步端点写出的错误体还原成一个可上报的原因码。
//
// generateSkinArtwork 通过 fail()/upstreamError() 写的是 {"error":{"code":...}},这里只取 code:
// message 和 code 目前是同一个字符串,而 code 是稳定的、可以被客户端和日志一起认的那一个。
func skinFailureReason(response *skinJobResponse, ctxErr error) string {
	if ctxErr != nil {
		return "cancelled"
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(response.body.Bytes(), &body) == nil && body.Error.Code != "" {
		return body.Error.Code
	}
	if response.status != 0 {
		return "upstream_status_" + strconv.Itoa(response.status)
	}
	return "unknown"
}
