package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

const (
	wordsTestOrigin = "https://msime.app"
	wordsTestRepo   = "/repos/metasequoiaime/msime-dictionary"
	wordsTestToken  = "ghs_installation_token"
)

var (
	wordsTestKeyOnce sync.Once
	wordsTestKey     *rsa.PrivateKey
)

func wordsKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	wordsTestKeyOnce.Do(func() {
		var err error
		if wordsTestKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return wordsTestKey
}

// fakeWordsUpstream stands in for both Cloudflare siteverify and the GitHub REST API. A status override of -1 drops the connection without a response, which is how a write with an unknown outcome looks to the server.
type fakeWordsUpstream struct {
	t               *testing.T
	mu              sync.Mutex
	server          *httptest.Server
	turnstile       map[string]any
	turnstileStatus int
	pulls           []map[string]any
	content         string
	sha             string
	status          map[string]int
	calls           map[string]int
	bodies          map[string]map[string]any
	query           map[string]url.Values
	tokens          int
}

func newFakeWordsUpstream(t *testing.T) *fakeWordsUpstream {
	f := &fakeWordsUpstream{t: t, turnstile: map[string]any{"success": true, "action": "words", "hostname": "msime.app"}, turnstileStatus: 200,
		content: "未来可期\twei'lai'ke'qi\t1\n", sha: "sha-0", status: map[string]int{}, calls: map[string]int{}, bodies: map[string]map[string]any{}, query: map[string]url.Values{}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeWordsUpstream) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.calls[key]++
	f.query[key] = r.URL.Query()
	raw, _ := io.ReadAll(r.Body)
	if key == "POST /siteverify" {
		form, _ := url.ParseQuery(string(raw))
		if form.Get("secret") != "turnstile-secret" || form.Get("response") != "turnstile-token" {
			f.t.Errorf("siteverify form: %v", form)
		}
		w.WriteHeader(f.turnstileStatus)
		_ = json.NewEncoder(w).Encode(f.turnstile)
		return
	}
	var body map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Errorf("%s: invalid JSON body", key)
		}
		f.bodies[key] = body
	}
	if r.Header.Get("X-GitHub-Api-Version") == "" || r.Header.Get("User-Agent") == "" || r.Header.Get("Accept") != "application/vnd.github+json" {
		f.t.Errorf("%s: missing GitHub headers", key)
	}
	if status, ok := f.status[key]; ok {
		if status < 0 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"override"}`))
		return
	}
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if key == "POST /app/installations/77/access_tokens" {
		f.tokens++
		assertion := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parsed, err := jwt.ParseSigned(assertion, []jose.SignatureAlgorithm{jose.RS256})
		var claims jwt.Claims
		if err != nil || parsed.Claims(&wordsTestKey.PublicKey, &claims) != nil || claims.Issuer != "42" || claims.Expiry.Time().Sub(claims.IssuedAt.Time()) > 10*time.Minute {
			f.t.Errorf("app JWT rejected: %v %+v", err, claims)
		}
		reply(201, map[string]any{"token": wordsTestToken, "expires_at": "2026-09-30T13:34:56Z"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+wordsTestToken {
		f.t.Errorf("%s: installation token missing", key)
	}
	switch key {
	case "GET " + wordsTestRepo + "/pulls":
		reply(200, f.pulls)
	case "GET " + wordsTestRepo + "/git/ref/heads/main":
		reply(200, map[string]any{"object": map[string]string{"sha": "base-commit"}})
	case "GET " + wordsTestRepo + "/contents/custom/words.txt":
		encoded := base64.StdEncoding.EncodeToString([]byte(f.content))
		var wrapped []string
		for len(encoded) > 60 {
			wrapped, encoded = append(wrapped, encoded[:60]), encoded[60:]
		}
		reply(200, map[string]any{"type": "file", "encoding": "base64", "sha": f.sha, "content": strings.Join(append(wrapped, encoded), "\n") + "\n"})
	case "POST " + wordsTestRepo + "/git/refs":
		reply(201, map[string]any{"ref": body["ref"]})
	case "PUT " + wordsTestRepo + "/contents/custom/words.txt":
		if body["sha"] != f.sha {
			reply(409, map[string]string{"message": "sha mismatch"})
			return
		}
		decoded, _ := base64.StdEncoding.DecodeString(body["content"].(string))
		f.content, f.sha = string(decoded), f.sha+"+"
		reply(200, map[string]any{"content": map[string]string{"sha": f.sha}})
	case "POST " + wordsTestRepo + "/pulls":
		f.pulls = append(f.pulls, map[string]any{"number": 12, "head": map[string]any{"ref": body["head"], "repo": map[string]string{"full_name": "metasequoiaime/msime-dictionary"}}, "base": map[string]string{"ref": "main"}})
		reply(201, map[string]any{"number": 12, "html_url": "https://github.com/metasequoiaime/msime-dictionary/pull/12"})
	default:
		f.t.Errorf("unexpected GitHub call %s", key)
		w.WriteHeader(404)
	}
}

func (f *fakeWordsUpstream) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

type fakeLimiter struct {
	mu    sync.Mutex
	err   error
	calls []string
}

func (l *fakeLimiter) RateLimit(_ context.Context, scope, subject string, limit int, window time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, scope+"|"+subject)
	return l.err
}

func wordsKeyPEM(t *testing.T, pkcs8 bool) string {
	t.Helper()
	if pkcs8 {
		der, err := x509.MarshalPKCS8PrivateKey(wordsKey(t))
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(wordsKey(t))}))
}

func wordsConfig(t *testing.T, upstream string) WordSubmissionsConfig {
	t.Helper()
	t.Setenv("TEST_TURNSTILE_SECRET", "turnstile-secret")
	t.Setenv("TEST_WORDS_APP_KEY", wordsKeyPEM(t, false))
	return WordSubmissionsConfig{
		Turnstile: TurnstileConfig{SiteKey: "0x4AAAAAAA-site-key", SecretEnv: "TEST_TURNSTILE_SECRET", SiteverifyURL: upstream + "/siteverify"},
		GitHub:    WordsGitHubConfig{AppID: 42, InstallationID: 77, PrivateKeyEnv: "TEST_WORDS_APP_KEY", Repository: "metasequoiaime/msime-dictionary", APIURL: upstream},
	}
}

func wordsFixture(t *testing.T) (*Server, *fakeWordsUpstream, *fakeLimiter) {
	t.Helper()
	f := newFakeWordsUpstream(t)
	s := fixture(t, nil)
	s.config.AllowedOrigins = []string{wordsTestOrigin}
	c := wordsConfig(t, f.server.URL)
	if err := c.validate(true, s.config.AllowedOrigins); err != nil {
		t.Fatal(err)
	}
	limiter := &fakeLimiter{}
	s.words = newWordSubmitter(c, s.config.AllowedOrigins, limiter)
	s.words.client = f.server.Client()
	s.words.now = func() time.Time { return time.Date(2026, 9, 30, 12, 34, 56, 0, time.UTC) }
	return s, f, limiter
}

func postWords(s *Server, body string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", wordSubmissionsPath, strings.NewReader(body))
	r.Header.Set("Origin", wordsTestOrigin)
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "198.51.100.7:4567"
	for _, m := range mutate {
		m(r)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

const validWords = `{"entries":[{"word":"扛把子","pinyin":"kang'ba'zi"},{"word":"二〇二六","pinyin":"er'ling'er'liu"}],"note":"网络流行语 @octocat fixes #3 https://example.com","token":"turnstile-token"}`

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid JSON %q", w.Body.String())
	}
	return v
}

func TestWordSubmissionSettings(t *testing.T) {
	s := fixture(t, nil)
	r := httptest.NewRequest("GET", wordSubmissionsPath, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"enabled":false,"site_key":""}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = postWords(s, validWords, func(r *http.Request) { r.Header.Del("Origin") })
	if w.Code != 503 || decodeBody(t, w)["code"] != "word_submissions_disabled" {
		t.Fatal("disabled submissions accepted", w.Code, w.Body.String())
	}
	s, _, _ = wordsFixture(t)
	r = httptest.NewRequest("GET", wordSubmissionsPath, nil)
	r.Header.Set("Origin", wordsTestOrigin)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"enabled":true,"site_key":"0x4AAAAAAA-site-key"}` || w.Header().Get("Access-Control-Allow-Origin") != wordsTestOrigin {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	// Preflight from the website is answered by the shared CORS middleware.
	r = httptest.NewRequest("OPTIONS", wordSubmissionsPath, nil)
	r.Header.Set("Origin", wordsTestOrigin)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Fatal("preflight", w.Code)
	}
}

func TestWordSubmissionValidation(t *testing.T) {
	s, f, limiter := wordsFixture(t)
	// Each case comes from its own address so the per-minute gate in front of Turnstile does not interfere.
	requests := 0
	postWords := func(s *Server, body string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
		requests++
		address := func(r *http.Request) { r.RemoteAddr = "192.0.2." + strconv.Itoa(requests) + ":1" }
		return postWords(s, body, append([]func(*http.Request){address}, mutate...)...)
	}
	entry := func(word, pinyin string) string {
		raw, _ := json.Marshal(map[string]any{"entries": []map[string]string{{"word": word, "pinyin": pinyin}}, "note": "", "token": "turnstile-token"})
		return string(raw)
	}
	for _, c := range []struct{ word, pinyin, code string }{
		{"", "ni", "word_required"},
		{"abc", "a'b'c", "invalid_word"},
		{"你 好", "ni'hao", "invalid_word"},
		{"你好。", "ni'hao", "invalid_word"},
		{"⺀", "ni", "invalid_word"},
		{"\x00", "ni", "invalid_word"},
		{string([]byte{0xff}), "ni", "invalid_word"},
		{strings.Repeat("好", 17), strings.TrimSuffix(strings.Repeat("hao'", 17), "'"), "word_too_long"},
		{"你好", "", "pinyin_required"},
		{"你好", "Ni'hao", "invalid_pinyin"},
		{"你好", "ni''hao", "invalid_pinyin"},
		{"你好", "ni hao", "invalid_pinyin"},
		{"你好", "nǐ'hǎo", "invalid_pinyin"},
		{"略", "lue", "invalid_syllable"},
		{"你好", "ni'hoa", "invalid_syllable"},
		{"你好", "ni", "syllable_count_mismatch"},
		{"你好", "nihao", "invalid_syllable"},
	} {
		w := postWords(s, entry(c.word, c.pinyin))
		body := decodeBody(t, w)
		rejected, _ := body["rejected"].([]any)
		if w.Code != 400 || len(rejected) != 1 || rejected[0].(map[string]any)["code"] != c.code || rejected[0].(map[string]any)["index"] != 0.0 || rejected[0].(map[string]any)["reason"] == "" || body["error"] == "" {
			t.Errorf("%q %q: %d %s", c.word, c.pinyin, w.Code, w.Body.String())
		}
	}
	// The dictionary spellings the website normalises to are accepted: v for ü, lve/nve.
	if rejected := validateWordEntries([]wordSubmissionEntry{{"绿", "lv"}, {"略", "lve"}, {"虐", "nve"}, {"一回事儿", "yi'hui'shi'er"}, {strings.Repeat("好", 16), strings.TrimSuffix(strings.Repeat("hao'", 16), "'")}}); len(rejected) != 0 {
		t.Fatal(rejected)
	}
	w := postWords(s, `{"entries":[{"word":"你好","pinyin":"ni'hao"},{"word":"你好","pinyin":"ni'hao"},{"word":"你好","pinyin":"ni'hao"}],"note":"","token":"turnstile-token"}`)
	if w.Code != 400 || strings.Count(w.Body.String(), "duplicate_entry") != 2 {
		t.Fatal("duplicates", w.Code, w.Body.String())
	}
	many := make([]map[string]string, 21)
	for i := range many {
		many[i] = map[string]string{"word": "你", "pinyin": "ni"}
	}
	raw, _ := json.Marshal(map[string]any{"entries": many, "token": "turnstile-token"})
	for body, code := range map[string]string{
		string(raw): "invalid_entry_count",
		`{"entries":[],"token":"turnstile-token"}`: "invalid_entry_count",
		`{"token":"turnstile-token"}`:              "invalid_entry_count",
		`{"entries":[{"word":"你","pinyin":"ni"}],"note":"` + strings.Repeat("长", 501) + `","token":"t"}`: "invalid_note",
		`{"entries":[{"word":"你","pinyin":"ni"}],"note":"\xff","token":"t"}`:                             "invalid_json",
		`{"entries":[{"word":"你","pinyin":"ni"}],"note":""}`:                                             "token_required",
		`{"entries":[{"word":"你","pinyin":"ni"}],"token":"` + strings.Repeat("t", 2049) + `"}`:           "token_required",
		`{"entries":[{"word":"你","pinyin":"ni","weight":1}],"token":"t"}`:                                "invalid_json",
		`{"entries":[{"word":"你","pinyin":"ni"}],"token":"t"} {}`:                                        "invalid_json",
		`not json`: "invalid_json",
	} {
		w := postWords(s, body)
		if w.Code != 400 || decodeBody(t, w)["code"] != code {
			t.Errorf("%.60s: %d %s", body, w.Code, w.Body.String())
		}
	}
	// Exactly 500 characters and the maximum entry count are accepted by validation.
	if _, ok := sanitizeWordNote(strings.Repeat("长", 500)); !ok {
		t.Fatal("500-character note rejected")
	}
	w = postWords(s, `{"entries":[{"word":"你","pinyin":"ni"}],"note":"`+strings.Repeat("x", 17000)+`","token":"t"}`)
	if w.Code != 413 {
		t.Fatal("oversized body", w.Code)
	}
	w = postWords(s, validWords, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") })
	if w.Code != 415 {
		t.Fatal("content type", w.Code)
	}
	// Only the configured website may submit; requests without an Origin (scripts, native clients) are refused.
	w = postWords(s, validWords, func(r *http.Request) { r.Header.Del("Origin") })
	if w.Code != 403 || decodeBody(t, w)["code"] != "origin_required" {
		t.Fatal("origin", w.Code, w.Body.String())
	}
	w = postWords(s, validWords, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if w.Code != 403 {
		t.Fatal("foreign origin", w.Code)
	}
	if f.count("POST /siteverify") != 0 || len(limiter.calls) != 0 || f.count("POST /app/installations/77/access_tokens") != 0 {
		t.Fatal("invalid requests reached Turnstile, the rate limit or GitHub")
	}
}

func TestWordNoteSanitising(t *testing.T) {
	note, ok := sanitizeWordNote("  line one\nline\ttwo  @octocat #3 GH-12 gh-7 see https://evil.example ​‮ ")
	if !ok {
		t.Fatal("rejected")
	}
	want := "line one line two @​octocat #​3 GH​-12 gh​-7 see https:​//evil.example"
	if note != want {
		t.Fatalf("%q", note)
	}
	if note, ok = sanitizeWordNote(""); !ok || note != "" {
		t.Fatal("empty note")
	}
	message := wordsCommitMessage([]wordSubmissionEntry{{"扛把子", "kang'ba'zi"}}, "")
	if !strings.HasPrefix(message, "feat(words): add 1 community-submitted word\n\n- 扛把子 kang'ba'zi\n") || strings.Contains(message, "Note:") {
		t.Fatalf("%q", message)
	}
	if got := appendWordLines("a\tb\t1", []wordSubmissionEntry{{"你", "ni"}}); got != "a\tb\t1\n你\tni\t5000\n" {
		t.Fatalf("%q", got)
	}
	if got := appendWordLines("", []wordSubmissionEntry{{"你", "ni"}}); got != "你\tni\t5000\n" {
		t.Fatalf("%q", got)
	}
}

func TestWordSubmissionTurnstile(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		result map[string]any
		code   int
	}{
		"failed":        {200, map[string]any{"success": false, "error-codes": []string{"invalid-input-response"}}, 403},
		"wrong action":  {200, map[string]any{"success": true, "action": "feedback", "hostname": "msime.app"}, 403},
		"wrong host":    {200, map[string]any{"success": true, "action": "words", "hostname": "evil.example"}, 403},
		"server error":  {500, map[string]any{}, 503},
		"invalid reply": {200, nil, 503},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, limiter := wordsFixture(t)
			f.turnstileStatus, f.turnstile = c.status, c.result
			if c.result == nil {
				f.turnstile = map[string]any{"success": "yes"}
			}
			w := postWords(s, validWords)
			if w.Code != c.code || f.count("POST /siteverify") != 1 {
				t.Fatal(w.Code, w.Body.String())
			}
			if len(limiter.calls) != 0 || f.count("POST /app/installations/77/access_tokens") != 0 {
				t.Fatal("unverified request went further")
			}
		})
	}
	s, f, _ := wordsFixture(t)
	f.server.Close()
	if w := postWords(s, validWords); w.Code != 503 || decodeBody(t, w)["code"] != "verification_unavailable" {
		t.Fatal("unreachable siteverify", w.Code, w.Body.String())
	}
}

func TestWordSubmissionRateLimit(t *testing.T) {
	s, f, limiter := wordsFixture(t)
	limiter.err = account.ErrLimited
	w := postWords(s, validWords)
	if w.Code != 429 || w.Header().Get("Retry-After") != "600" || decodeBody(t, w)["code"] != "rate_limit_exceeded" {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(limiter.calls) != 1 || limiter.calls[0] != "word-submissions-10m|198.51.100.7" {
		t.Fatal("the short window must be checked first and stop the daily count", limiter.calls)
	}
	if f.count("POST /app/installations/77/access_tokens") != 0 {
		t.Fatal("rate-limited request reached GitHub")
	}
	limiter.err = errors.New("database down")
	if w = postWords(s, validWords); w.Code != 503 || decodeBody(t, w)["code"] != "rate_limit_unavailable" {
		t.Fatal("limiter failure", w.Code, w.Body.String())
	}
	// The in-memory gate in front of Turnstile allows ten requests a minute per address.
	s, f, _ = wordsFixture(t)
	codes := map[int]int{}
	for range 12 {
		codes[postWords(s, `{}`, func(r *http.Request) { r.RemoteAddr = "203.0.113.9:1" }).Code]++
	}
	if codes[400] != 10 || codes[429] != 2 || f.count("POST /siteverify") != 0 {
		t.Fatal(codes)
	}
}

func TestWordSubmissionClientAddress(t *testing.T) {
	ws := &wordSubmitter{}
	request := func(remote string, headers map[string][]string) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = remote
		for k, v := range headers {
			r.Header[http.CanonicalHeaderKey(k)] = v
		}
		return r
	}
	spoofed := map[string][]string{"X-Forwarded-For": {"192.0.2.1"}}
	if got := ws.clientAddress(request("198.51.100.7:1", spoofed)); got != "198.51.100.7" {
		t.Fatal("forwarding headers must be ignored unless configured", got)
	}
	if got := ws.clientAddress(request("[2001:db8:1:2:3:4:5:6]:1", nil)); got != "2001:db8:1:2::/64" {
		t.Fatal(got)
	}
	if got := ws.clientAddress(request("[::ffff:198.51.100.7]:1", nil)); got != "198.51.100.7" {
		t.Fatal(got)
	}
	if got := ws.clientAddress(request("not-an-address", nil)); got != "not-an-address" {
		t.Fatal(got)
	}
	ws.config.ClientIPHeader = "X-Forwarded-For"
	if got := ws.clientAddress(request("10.0.0.1:1", map[string][]string{"X-Forwarded-For": {"192.0.2.1, 192.0.2.2", "192.0.2.3, 203.0.113.5"}})); got != "203.0.113.5" {
		t.Fatal("the proxy-appended last entry wins", got)
	}
	if got := ws.clientAddress(request("10.0.0.1:1", map[string][]string{"X-Forwarded-For": {"garbage"}})); got != "10.0.0.1" {
		t.Fatal(got)
	}
	ws.config.ClientIPHeader = "CF-Connecting-IP"
	if got := ws.clientAddress(request("10.0.0.1:1", map[string][]string{"Cf-Connecting-Ip": {"192.0.2.44"}})); got != "192.0.2.44" {
		t.Fatal(got)
	}
}

func TestWordSubmissionCreatesBranchAndPullRequest(t *testing.T) {
	s, f, limiter := wordsFixture(t)
	w := postWords(s, validWords)
	if w.Code != 201 || strings.TrimSpace(w.Body.String()) != `{"pull_request_url":"https://github.com/metasequoiaime/msime-dictionary/pull/12"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Join(limiter.calls, ",") != "word-submissions-10m|198.51.100.7,word-submissions-day|198.51.100.7" {
		t.Fatal(limiter.calls)
	}
	tokenRequest := f.bodies["POST /app/installations/77/access_tokens"]
	if raw, _ := json.Marshal(tokenRequest); string(raw) != `{"permissions":{"contents":"write","pull_requests":"write"},"repositories":["msime-dictionary"]}` {
		t.Fatal("installation token must be scoped to the repository and two permissions", string(raw))
	}
	pullsQuery := f.query["GET "+wordsTestRepo+"/pulls"]
	if pullsQuery.Get("state") != "open" || pullsQuery.Get("base") != "main" {
		t.Fatal(pullsQuery)
	}
	if ref := f.bodies["POST "+wordsTestRepo+"/git/refs"]; ref["ref"] != "refs/heads/community-words/20260930-123456" || ref["sha"] != "base-commit" {
		t.Fatal(ref)
	}
	if f.query["GET "+wordsTestRepo+"/contents/custom/words.txt"].Get("ref") != "base-commit" {
		t.Fatal("words.txt must be read at the commit the new branch starts from")
	}
	put := f.bodies["PUT "+wordsTestRepo+"/contents/custom/words.txt"]
	if put["sha"] != "sha-0" || put["branch"] != "community-words/20260930-123456" {
		t.Fatal(put)
	}
	if f.content != "未来可期\twei'lai'ke'qi\t1\n扛把子\tkang'ba'zi\t5000\n二〇二六\ter'ling'er'liu\t5000\n" {
		t.Fatalf("%q", f.content)
	}
	message := put["message"].(string)
	if !strings.HasPrefix(message, "feat(words): add 2 community-submitted words\n\n- 扛把子 kang'ba'zi\n- 二〇二六 er'ling'er'liu\n") || !strings.Contains(message, "Note: 网络流行语 @​octocat fixes #​3 https:​//example.com") {
		t.Fatalf("%q", message)
	}
	pr := f.bodies["POST "+wordsTestRepo+"/pulls"]
	body, _ := pr["body"].(string)
	if pr["head"] != "community-words/20260930-123456" || pr["base"] != "main" || pr["title"] != wordsPullRequestTitle || !strings.Contains(body, "reviewed by maintainers") || !strings.Contains(body, "msime-dictionary CI") || !strings.Contains(body, "dict-v*") || !strings.Contains(body, "resources/dictionary-sources.lock.json") || !strings.Contains(body, "custom/words.txt") {
		t.Fatal(pr)
	}
	if strings.Contains(body, "扛把子") || strings.Contains(body, "octocat") {
		t.Fatal("submitter data must not reach the pull request body")
	}

	// The second submission appends to the pull request just opened and reuses the cached installation token.
	w = postWords(s, `{"entries":[{"word":"堪堪","pinyin":"kan'kan"}],"token":"turnstile-token"}`)
	if w.Code != 201 || !strings.HasSuffix(strings.TrimSpace(w.Body.String()), `/pull/12"}`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.tokens != 1 || f.count("POST "+wordsTestRepo+"/git/refs") != 1 || f.count("POST "+wordsTestRepo+"/pulls") != 1 {
		t.Fatal("append must not create a branch, a pull request or a new token", f.calls)
	}
	if !strings.HasSuffix(f.content, "堪堪\tkan'kan\t5000\n") || f.bodies["PUT "+wordsTestRepo+"/contents/custom/words.txt"]["sha"] != "sha-0+" {
		t.Fatal(f.content)
	}
}

func TestWordSubmissionAppendsToOpenPullRequest(t *testing.T) {
	s, f, _ := wordsFixture(t)
	head := func(ref, repo string) map[string]any {
		if repo == "" {
			return map[string]any{"ref": ref, "repo": nil}
		}
		return map[string]any{"ref": ref, "repo": map[string]string{"full_name": repo}}
	}
	f.pulls = []map[string]any{
		{"number": 3, "head": head("feat/other", "metasequoiaime/msime-dictionary"), "base": map[string]string{"ref": "main"}},
		{"number": 4, "head": head("community-words/20260101-000000", "someone/msime-dictionary"), "base": map[string]string{"ref": "main"}},
		{"number": 5, "head": head("community-words/20260102-000000", ""), "base": map[string]string{"ref": "main"}},
		{"number": 9, "head": head("community-words/20260915-080000", "metasequoiaime/msime-dictionary"), "base": map[string]string{"ref": "main"}},
	}
	w := postWords(s, `{"entries":[{"word":"堪堪","pinyin":"kan'kan"}],"token":"turnstile-token"}`)
	if w.Code != 201 || strings.TrimSpace(w.Body.String()) != `{"pull_request_url":"https://github.com/metasequoiaime/msime-dictionary/pull/9"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.query["GET "+wordsTestRepo+"/contents/custom/words.txt"].Get("ref") != "community-words/20260915-080000" || f.bodies["PUT "+wordsTestRepo+"/contents/custom/words.txt"]["branch"] != "community-words/20260915-080000" {
		t.Fatal("must append on the open rolling branch")
	}
	if f.count("GET "+wordsTestRepo+"/git/ref/heads/main") != 0 || f.count("POST "+wordsTestRepo+"/git/refs") != 0 || f.count("POST "+wordsTestRepo+"/pulls") != 0 {
		t.Fatal(f.calls)
	}
}

func TestWordSubmissionConflictIsNotRetried(t *testing.T) {
	s, f, _ := wordsFixture(t)
	f.status["PUT "+wordsTestRepo+"/contents/custom/words.txt"] = 409
	w := postWords(s, validWords)
	if w.Code != 409 || decodeBody(t, w)["code"] != "concurrent_update" || f.count("PUT "+wordsTestRepo+"/contents/custom/words.txt") != 1 || f.count("POST "+wordsTestRepo+"/pulls") != 0 {
		t.Fatal(w.Code, w.Body.String(), f.calls)
	}
	s, f, _ = wordsFixture(t)
	f.status["POST "+wordsTestRepo+"/git/refs"] = 422
	if w = postWords(s, validWords); w.Code != 409 || f.count("PUT "+wordsTestRepo+"/contents/custom/words.txt") != 0 {
		t.Fatal("same-second branch collision", w.Code, w.Body.String())
	}
}

func TestWordSubmissionUncertainOutcome(t *testing.T) {
	for name, override := range map[string]map[string]int{
		"commit 5xx":          {"PUT " + wordsTestRepo + "/contents/custom/words.txt": 502},
		"commit dropped":      {"PUT " + wordsTestRepo + "/contents/custom/words.txt": -1},
		"pull request 5xx":    {"POST " + wordsTestRepo + "/pulls": 500},
		"pull request 422":    {"POST " + wordsTestRepo + "/pulls": 422},
		"pull request broken": {"POST " + wordsTestRepo + "/pulls": 201},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := wordsFixture(t)
			for k, v := range override {
				f.status[k] = v
			}
			w := postWords(s, validWords)
			body := decodeBody(t, w)
			if w.Code != 502 || body["uncertain"] != true || body["code"] != "outcome_unknown" || body["pulls_url"] != "https://github.com/metasequoiaime/msime-dictionary/pulls" {
				t.Fatal(w.Code, w.Body.String())
			}
			if f.count("PUT "+wordsTestRepo+"/contents/custom/words.txt") != 1 || f.count("POST "+wordsTestRepo+"/pulls") > 1 {
				t.Fatal("writes must never be retried", f.calls)
			}
		})
	}
}

func TestWordSubmissionGitHubFailuresBeforeWriting(t *testing.T) {
	for name, c := range map[string]struct {
		override map[string]int
		code     string
	}{
		"token 5xx":        {map[string]int{"POST /app/installations/77/access_tokens": 503}, "github_unavailable"},
		"token dropped":    {map[string]int{"POST /app/installations/77/access_tokens": -1}, "github_unavailable"},
		"token malformed":  {map[string]int{"POST /app/installations/77/access_tokens": 201}, "github_unavailable"},
		"token rejected":   {map[string]int{"POST /app/installations/77/access_tokens": 401}, "word_submissions_misconfigured"},
		"list pulls":       {map[string]int{"GET " + wordsTestRepo + "/pulls": 500}, "github_unavailable"},
		"list malformed":   {map[string]int{"GET " + wordsTestRepo + "/pulls": 200}, "github_unavailable"},
		"base branch":      {map[string]int{"GET " + wordsTestRepo + "/git/ref/heads/main": 404}, "github_unavailable"},
		"base branch sha":  {map[string]int{"GET " + wordsTestRepo + "/git/ref/heads/main": 200}, "github_unavailable"},
		"read file":        {map[string]int{"GET " + wordsTestRepo + "/contents/custom/words.txt": 500}, "github_unavailable"},
		"read file format": {map[string]int{"GET " + wordsTestRepo + "/contents/custom/words.txt": 200}, "github_unavailable"},
		"create branch":    {map[string]int{"POST " + wordsTestRepo + "/git/refs": 500}, "github_unavailable"},
		"commit forbidden": {map[string]int{"PUT " + wordsTestRepo + "/contents/custom/words.txt": 403}, "github_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := wordsFixture(t)
			for k, v := range c.override {
				f.status[k] = v
			}
			w := postWords(s, validWords)
			if w.Code != 503 || decodeBody(t, w)["code"] != c.code || decodeBody(t, w)["uncertain"] != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			if f.count("POST "+wordsTestRepo+"/pulls") != 0 {
				t.Fatal(f.calls)
			}
		})
	}
	// Entries already in words.txt are reported per row before anything is written.
	s, f, _ := wordsFixture(t)
	w := postWords(s, `{"entries":[{"word":"堪堪","pinyin":"kan'kan"},{"word":"未来可期","pinyin":"wei'lai'ke'qi"}],"token":"turnstile-token"}`)
	body := decodeBody(t, w)
	rejected, _ := body["rejected"].([]any)
	if w.Code != 400 || len(rejected) != 1 || rejected[0].(map[string]any)["index"] != 1.0 || rejected[0].(map[string]any)["code"] != "already_listed" {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.count("POST "+wordsTestRepo+"/git/refs") != 0 || f.count("PUT "+wordsTestRepo+"/contents/custom/words.txt") != 0 {
		t.Fatal("a rejected submission must not write", f.calls)
	}
	// Non-base64 content is refused rather than rewritten.
	if _, ok := (githubFile{Type: "file", Encoding: "base64", SHA: "x", Content: "!!"}).text(); ok {
		t.Fatal("invalid base64 accepted")
	}
}

func TestWordSubmissionsConfigValidation(t *testing.T) {
	t.Setenv("TEST_TURNSTILE_SECRET", "turnstile-secret")
	t.Setenv("TEST_WORDS_APP_KEY", wordsKeyPEM(t, false))
	origins := []string{wordsTestOrigin}
	valid := func() WordSubmissionsConfig {
		return WordSubmissionsConfig{
			Turnstile: TurnstileConfig{SiteKey: "site-key", SecretEnv: "TEST_TURNSTILE_SECRET"},
			GitHub:    WordsGitHubConfig{AppID: 1, InstallationID: 2, PrivateKeyEnv: "TEST_WORDS_APP_KEY", Repository: "metasequoiaime/msime-dictionary"},
		}
	}
	c := valid()
	if err := c.validate(true, origins); err != nil || c.GitHub.Branch != "main" || c.GitHub.APIURL != defaultGitHubAPIURL || c.Turnstile.SiteverifyURL != defaultTurnstileURL || c.GitHub.key == nil {
		t.Fatal(err, c)
	}
	disabled := WordSubmissionsConfig{GitHub: WordsGitHubConfig{AppID: -1}}
	if err := disabled.validate(false, nil); err != nil {
		t.Fatal("an empty site key must leave the feature off without checking the rest", err)
	}
	for name, mutate := range map[string]func(*WordSubmissionsConfig){
		"header":      func(c *WordSubmissionsConfig) { c.ClientIPHeader = "X Forwarded" },
		"site key":    func(c *WordSubmissionsConfig) { c.Turnstile.SiteKey = "has space" },
		"secret":      func(c *WordSubmissionsConfig) { c.Turnstile.SecretEnv = "TEST_MISSING_SECRET" },
		"verify url":  func(c *WordSubmissionsConfig) { c.Turnstile.SiteverifyURL = "http://challenges.example/siteverify" },
		"app id":      func(c *WordSubmissionsConfig) { c.GitHub.AppID = 0 },
		"install id":  func(c *WordSubmissionsConfig) { c.GitHub.InstallationID = 0 },
		"repository":  func(c *WordSubmissionsConfig) { c.GitHub.Repository = "msime-dictionary" },
		"branch":      func(c *WordSubmissionsConfig) { c.GitHub.Branch = "main..x" },
		"own prefix":  func(c *WordSubmissionsConfig) { c.GitHub.Branch = "community-words/x" },
		"api url":     func(c *WordSubmissionsConfig) { c.GitHub.APIURL = "https://api.github.com/?x=1" },
		"key missing": func(c *WordSubmissionsConfig) { c.GitHub.PrivateKeyEnv = "TEST_MISSING_KEY" },
	} {
		c := valid()
		mutate(&c)
		if err := c.validate(true, origins); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c = valid()
	if c.validate(false, origins) == nil || c.validate(true, nil) == nil {
		t.Fatal("word submissions need the user database and an allowed website origin")
	}
	t.Setenv("TEST_WORDS_APP_KEY", strings.ReplaceAll(wordsKeyPEM(t, true), "\n", `\n`))
	c = valid()
	if err := c.validate(true, origins); err != nil {
		t.Fatal("PKCS#8 key stored with literal \\n rejected", err)
	}
	for name, raw := range map[string]string{
		"garbage": "not a key",
		"der":     string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")})),
	} {
		if _, err := parseGitHubAppKey(raw); err == nil {
			t.Errorf("%s key accepted", name)
		}
	}
	ecKey, err := x509.MarshalPKCS8PrivateKey(mustECKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parseGitHubAppKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecKey}))); err == nil {
		t.Fatal("non-RSA key accepted")
	}
	// A partial configuration must stop the server from starting rather than silently disabling the form.
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	bad := valid()
	bad.GitHub.AppID = 0
	if _, err = New(Config{Clients: []Client{{ID: "test", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 1}}, AllowedOrigins: origins, WordSubmissions: bad}); err == nil {
		t.Fatal("server started with a partial word_submissions configuration")
	}
}

func mustECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// The per-address limit lives in PostgreSQL so every replica shares it. Three submissions pass in ten minutes, the fourth is refused before GitHub is touched, another address is unaffected, and the database holds only a digest of the address.
func TestWordSubmissionRateLimitInPostgreSQL(t *testing.T) {
	admin, schema := disposableSchema(t)
	f := newFakeWordsUpstream(t)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	s, err := New(Config{
		Auth:            account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
		Clients:         []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 100}},
		AllowedOrigins:  []string{wordsTestOrigin},
		WordSubmissions: wordsConfig(t, f.server.URL),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.CloseAccounts()
	if s.words == nil {
		t.Fatal("word submissions not enabled")
	}
	s.words.client = f.server.Client()
	for i, word := range []string{"堪堪", "判空", "双码", "狂写"} {
		pinyin := map[string]string{"堪堪": "kan'kan", "判空": "pan'kong", "双码": "shuang'ma", "狂写": "kuang'xie"}[word]
		w := postWords(s, `{"entries":[{"word":"`+word+`","pinyin":"`+pinyin+`"}],"token":"turnstile-token"}`, func(r *http.Request) { r.RemoteAddr = "198.51.100.8:1" })
		if i < 3 && w.Code != 201 {
			t.Fatal(i, w.Code, w.Body.String())
		}
		if i == 3 && (w.Code != 429 || w.Header().Get("Retry-After") != "600") {
			t.Fatal("fourth submission in ten minutes", w.Code, w.Body.String())
		}
	}
	if got := f.count("PUT " + wordsTestRepo + "/contents/custom/words.txt"); got != 3 {
		t.Fatal("rate-limited submission reached GitHub", got)
	}
	w := postWords(s, `{"entries":[{"word":"狂写","pinyin":"kuang'xie"}],"token":"turnstile-token"}`, func(r *http.Request) { r.RemoteAddr = "198.51.100.9:1" })
	if w.Code != 201 {
		t.Fatal("another address was limited", w.Code, w.Body.String())
	}
	var rows, leaked int
	if err = admin.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE key LIKE 'word-submissions-%'), count(*) FILTER (WHERE key LIKE '%198.51.100%') FROM `+pgx.Identifier{schema, "auth_rates"}.Sanitize()).Scan(&rows, &leaked); err != nil {
		t.Fatal(err)
	}
	if rows != 4 || leaked != 0 {
		t.Fatal("expected two windows for two addresses, stored as digests", rows, leaked)
	}
}
