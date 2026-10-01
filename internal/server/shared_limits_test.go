package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// replicaPair starts two servers on one disposable schema, standing in for two replicas behind a round-robin balancer.
func replicaPair(t *testing.T, words func() WordSubmissionsConfig) [2]*Server {
	t.Helper()
	disposableSchema(t)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	var pair [2]*Server
	for i := range pair {
		c := Config{
			Auth:           account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
			Clients:        []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 100}},
			AllowedOrigins: []string{wordsTestOrigin},
		}
		if words != nil {
			c.WordSubmissions = words()
		}
		s, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(); s.CloseAccounts() })
		pair[i] = s
	}
	return pair
}

// The login start budget (ten a minute per address) is one budget across replicas: requests alternating between two servers stop at the eleventh, on either server, while another address keeps its own.
func TestAdminLoginLimitSharedAcrossReplicas(t *testing.T) {
	pair := replicaPair(t, nil)
	for _, s := range pair {
		s.config.Admin = AdminConfig{Enabled: true, Host: "admin.msime.app", Google: AdminGoogleConfig{ClientID: "test", RedirectURI: "https://admin.msime.app" + adminCallbackPath}}
		s.initAdminGoogle()
		s.adminStore = &adminMemoryStore{flows: map[string]account.AdminLoginFlow{}, sessions: map[string]account.AdminIdentity{}}
	}
	start := func(s *Server, peer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://admin.msime.app/api/auth/google/start", nil)
		r.RemoteAddr = peer + ":4567"
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	for i := range 10 {
		if w := start(pair[i%2], "198.51.100.20"); w.Code != 302 {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	for _, s := range pair {
		if w := start(s, "198.51.100.20"); w.Code != 429 || w.Header().Get("Retry-After") != "60" || !strings.Contains(w.Body.String(), "rate_limit_exceeded") {
			t.Fatal("the other replica did not see the spent budget", w.Code, w.Body.String())
		}
	}
	if w := start(pair[1], "198.51.100.21"); w.Code != 302 {
		t.Fatal("another address was limited", w.Code)
	}
}

// Two replicas that both find no open pull request must not open two. The fake GitHub holds the first replica's pull request listing until the second replica lists too (or a second passes): without the cross-replica lock both listings come back empty and each replica creates its own community-words branch (on GitHub that ends in two pull requests; the fake keeps one blob for every branch, so there the loser fails with a conflict instead); with it the second replica waits, sees the first one's pull request and appends to it.
func TestWordSubmissionsOnePullRequestAcrossReplicas(t *testing.T) {
	f := newFakeWordsUpstream(t)
	pair := replicaPair(t, func() WordSubmissionsConfig { return wordsConfig(t, f.server.URL) })
	for i, s := range pair {
		if s.words == nil || s.words.locks == nil {
			t.Fatal("word submissions without the shared lock")
		}
		s.words.client = f.server.Client()
		// Different seconds give different branch names, which is how the replicas raced past GitHub's own checks.
		second := time.Date(2026, 9, 30, 12, 34, 56+i, 0, time.UTC)
		s.words.now = func() time.Time { return second }
	}
	var listings atomic.Int32
	both := make(chan struct{})
	f.before = func(key string) {
		if key != "GET "+wordsTestRepo+"/pulls" {
			return
		}
		if listings.Add(1) == 2 {
			close(both)
			return
		}
		select {
		case <-both:
		case <-time.After(time.Second):
		}
	}
	bodies := []string{
		`{"entries":[{"word":"扛把子","pinyin":"kang'ba'zi"}],"token":"turnstile-token"}`,
		`{"entries":[{"word":"判空","pinyin":"pan'kong"}],"token":"turnstile-token"}`,
	}
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 2)
	for i, s := range pair {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = postWords(s, bodies[i], func(r *http.Request) { r.RemoteAddr = "198.51.100.3" + string(rune('0'+i)) + ":1" })
		}()
	}
	wg.Wait()
	for i, w := range results {
		if w.Code != 201 || decodeBody(t, w)["pull_request_url"] != "https://github.com/metasequoiaime/msime-dictionary/pull/12" {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	if pulls, branches := f.count("POST "+wordsTestRepo+"/pulls"), f.count("POST "+wordsTestRepo+"/git/refs"); pulls != 1 || branches != 1 {
		t.Fatal("replicas opened more than one rolling pull request", pulls, branches)
	}
	f.mu.Lock()
	content := f.content
	f.mu.Unlock()
	if !strings.Contains(content, "扛把子\tkang'ba'zi") || !strings.Contains(content, "判空\tpan'kong") {
		t.Fatal("both submissions must be on the one branch", content)
	}
}

type fakeLocker struct{ err error }

func (l fakeLocker) WaitLock(context.Context, string, time.Duration) (func(), error) {
	return func() {}, l.err
}

// A submission that cannot get the cross-replica lock in time, or whose database cannot take it, touches nothing on GitHub and answers 503 server_busy with Retry-After, which the visitor may simply retry.
func TestWordSubmissionLockUnavailable(t *testing.T) {
	for _, cause := range []error{account.ErrLockBusy, errors.New("database down")} {
		s, f, _ := wordsFixture(t)
		s.words.locks = fakeLocker{cause}
		w := postWords(s, validWords)
		if w.Code != 503 || w.Header().Get("Retry-After") != "30" || decodeBody(t, w)["code"] != "server_busy" || strings.Contains(w.Body.String(), "database down") {
			t.Fatal(cause, w.Code, w.Body.String())
		}
		if f.count("POST /app/installations/77/access_tokens") != 0 || f.count("GET "+wordsTestRepo+"/pulls") != 0 {
			t.Fatal("GitHub was called without the lock", cause)
		}
	}
}
