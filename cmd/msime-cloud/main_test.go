package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCloud answers the sign-in, refresh and a few other routes the way the real service does, with synthetic tokens.
type fakeCloud struct {
	mu        sync.Mutex
	access    string
	refresh   string
	issued    int
	refreshes int
	seen      []string
}

func (f *fakeCloud) issue() map[string]any {
	f.issued++
	f.access, f.refresh = "access-"+string(rune('0'+f.issued)), "refresh-"+string(rune('0'+f.issued))
	return map[string]any{"access_token": f.access, "refresh_token": f.refresh, "token_type": "Bearer", "expires_in": 900, "user": map[string]string{"id": "u1", "display_name": "合成用户"}}
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization"))
	reply := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(value)
	}
	var body map[string]any
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /v1/auth/challenges":
		reply(200, map[string]any{"challenge_id": "c1", "expires_in": 300})
	case "POST /v1/auth/login":
		if body["challenge_id"] != "c1" || body["credential"] != "123456" {
			reply(401, map[string]any{"error": map[string]string{"code": "invalid_credential"}})
			return
		}
		reply(200, f.issue())
	case "POST /v1/auth/refresh":
		f.refreshes++
		if body["refresh_token"] != f.refresh {
			// A replayed refresh token revokes the session.
			f.access, f.refresh = "", ""
			reply(401, map[string]any{"error": map[string]string{"code": "unauthorized"}})
			return
		}
		reply(200, f.issue())
	case "GET /v1/users/me", "POST /v1/auth/logout":
		if f.access == "" || r.Header.Get("Authorization") != "Bearer "+f.access {
			reply(401, map[string]any{"error": map[string]string{"code": "unauthorized"}})
			return
		}
		reply(200, map[string]any{"id": "u1"})
	case "GET /v1/skins/source":
		w.Header().Set("Content-Type", "application/zip")
		w.Write([]byte("PK\x03\x04synthetic"))
	case "POST /v1/community/plugins":
		file, header, err := r.FormFile("file")
		if err != nil {
			reply(400, map[string]any{"error": map[string]string{"code": "missing_file"}})
			return
		}
		data, _ := io.ReadAll(file)
		reply(201, map[string]any{"name": header.Filename, "size": len(data), "kind": r.FormValue("kind")})
	case "GET /api/overview":
		if r.Header.Get("Authorization") != "Bearer admin-key" {
			reply(401, map[string]any{"error": map[string]string{"code": "unauthorized"}})
			return
		}
		reply(200, map[string]any{"users": 3, "range_days": r.URL.Query().Get("days")})
	default:
		reply(404, map[string]any{"error": map[string]string{"code": "not_found"}})
	}
}

type harness struct {
	t     *testing.T
	cloud *fakeCloud
	url   string
	dir   string
	env   map[string]string
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	cloud := &fakeCloud{}
	server := httptest.NewServer(cloud)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	return &harness{t: t, cloud: cloud, url: server.URL, dir: dir, now: time.Now(), env: map[string]string{
		"MSIME_CLOUD_URL":        server.URL,
		"MSIME_ADMIN_URL":        server.URL,
		"MSIME_CLOUD_CONFIG_DIR": dir,
	}}
}

func (h *harness) run(stdin string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	c := cli{env: func(name string) string { return h.env[name] }, stdin: strings.NewReader(stdin), stdout: &stdout, stderr: &stderr, client: http.DefaultClient, now: func() time.Time { return h.now }}
	return c.run(args), stdout.String(), stderr.String()
}

func (h *harness) signIn() {
	h.t.Helper()
	if code, out, _ := h.run("", "login", "start", "--email", "someone@example.com"); code != 0 || !strings.Contains(out, `"challenge_id": "c1"`) {
		h.t.Fatalf("login start: %d %s", code, out)
	}
	code, out, errText := h.run("", "login", "finish", "--challenge", "c1", "--code", "123456")
	if code != 0 {
		h.t.Fatalf("login finish: %d %s %s", code, out, errText)
	}
	if strings.Contains(out, "access-") || strings.Contains(out, "refresh-") {
		h.t.Fatalf("tokens printed: %s", out)
	}
}

func TestSignInKeepsTheSessionPrivatelyAndCallsWithIt(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	info, err := os.Stat(filepath.Join(h.dir, "credentials.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %v %v", info, err)
	}
	if code, out, _ := h.run("", "whoami"); code != 0 || !strings.Contains(out, `"id": "u1"`) {
		t.Fatalf("whoami: %d %s", code, out)
	}
	last := h.cloud.seen[len(h.cloud.seen)-1]
	if last != "GET /v1/users/me auth=Bearer access-1" {
		t.Fatalf("whoami sent %q", last)
	}
	// The sign-in itself is anonymous.
	for _, line := range h.cloud.seen[:2] {
		if !strings.HasSuffix(line, "auth=") {
			t.Fatalf("anonymous route got credentials: %s", line)
		}
	}
}

func TestAnExpiredSessionIsRefreshedOnceBeforeTheCall(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	h.now = h.now.Add(20 * time.Minute)
	if code, _, errText := h.run("", "call", "GET", "/v1/users/me"); code != 0 {
		t.Fatalf("call: %d %s", code, errText)
	}
	if h.cloud.refreshes != 1 || h.cloud.seen[len(h.cloud.seen)-1] != "GET /v1/users/me auth=Bearer access-2" {
		t.Fatalf("refreshes %d, seen %v", h.cloud.refreshes, h.cloud.seen)
	}
}

func TestARejectedAccessTokenIsRefreshedAndRetried(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	// The server ended the access token early, as after a password change elsewhere.
	h.cloud.access = "elsewhere"
	h.cloud.refresh = "refresh-1"
	if code, _, errText := h.run("", "whoami"); code != 0 {
		t.Fatalf("whoami: %d %s", code, errText)
	}
	if h.cloud.refreshes != 1 {
		t.Fatalf("refreshes %d", h.cloud.refreshes)
	}
}

func TestARefreshAlreadyDoneByAnotherCommandIsNotReplayed(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	c := cli{env: func(name string) string { return h.env[name] }, stdout: io.Discard, stderr: io.Discard, client: http.DefaultClient, now: time.Now}
	stale, _, _ := c.store().get(c.server())
	if _, err := c.refresh(c.server(), stale); err != nil {
		t.Fatal(err)
	}
	// A second command that read the session before the first refreshed it.
	latest, err := c.refresh(c.server(), stale)
	if err != nil || latest.AccessToken != "access-2" || h.cloud.refreshes != 1 {
		t.Fatalf("latest %+v err %v refreshes %d", latest, err, h.cloud.refreshes)
	}
}

func TestAnEndedSessionIsForgotten(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	h.cloud.access, h.cloud.refresh = "", ""
	code, _, errText := h.run("", "whoami")
	if code != 1 || !strings.Contains(errText, "sign in again") {
		t.Fatalf("whoami: %d %s", code, errText)
	}
	data, _ := os.ReadFile(filepath.Join(h.dir, "credentials.json"))
	if strings.Contains(string(data), "access-") {
		t.Fatalf("session kept: %s", data)
	}
}

func TestLogoutEndsAndForgetsTheSession(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	if code, _, errText := h.run("", "logout"); code != 0 {
		t.Fatalf("logout: %d %s", code, errText)
	}
	if code, _, _ := h.run("", "logout"); code != 1 {
		t.Fatal("a second logout should find no session")
	}
}

func TestAnErrorResponseIsPrintedAndFails(t *testing.T) {
	h := newHarness(t)
	code, out, errText := h.run("", "call", "GET", "/v1/users/me")
	if code != 1 || !strings.Contains(out, "unauthorized") || !strings.Contains(errText, "not signed in") {
		t.Fatalf("call: %d %s %s", code, out, errText)
	}
}

func TestABinaryResponseNeedsAFile(t *testing.T) {
	h := newHarness(t)
	h.env["MSIME_CLOUD_TOKEN"] = "device-token"
	if code, _, errText := h.run("", "call", "GET", "/v1/skins/source"); code != 1 || !strings.Contains(errText, "-o") {
		t.Fatalf("call: %d %s", code, errText)
	}
	target := filepath.Join(t.TempDir(), "source.zip")
	code, out, _ := h.run("", "call", "GET", "/v1/skins/source", "-o", target)
	data, _ := os.ReadFile(target)
	if code != 0 || !strings.Contains(out, `"saved"`) || !bytes.HasPrefix(data, []byte("PK")) {
		t.Fatalf("call -o: %d %s %q", code, out, data)
	}
}

func TestFormFieldsUploadAFile(t *testing.T) {
	h := newHarness(t)
	h.env["MSIME_CLOUD_TOKEN"] = "device-token"
	pack := filepath.Join(t.TempDir(), "pack.zip")
	os.WriteFile(pack, []byte("synthetic pack"), 0o600)
	code, out, errText := h.run("", "call", "POST", "/v1/community/plugins", "-F", "file=@"+pack, "-F", "kind=effect")
	if code != 0 || !strings.Contains(out, `"name": "pack.zip"`) || !strings.Contains(out, `"kind": "effect"`) {
		t.Fatalf("upload: %d %s %s", code, out, errText)
	}
	if code, _, _ := h.run("{}", "call", "POST", "/v1/community/plugins", "-", "-F", "kind=effect"); code != 2 {
		t.Fatal("a JSON body and form fields together should be refused")
	}
}

func TestAdminPathsUseTheAdminKey(t *testing.T) {
	h := newHarness(t)
	if code, _, errText := h.run("", "call", "GET", "/api/overview"); code != 1 || !strings.Contains(errText, "MSIME_ADMIN_TOKEN") {
		t.Fatalf("without key: %d %s", code, errText)
	}
	h.env["MSIME_ADMIN_TOKEN"] = "admin-key"
	code, out, errText := h.run("", "call", "GET", "/api/overview", "-q", "days=7")
	if code != 0 || !strings.Contains(out, `"range_days": "7"`) {
		t.Fatalf("with key: %d %s %s", code, out, errText)
	}
}

func TestRoutesAndDescribeCoverTheAPIAndTheAdminSite(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("", "routes")
	if code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"/v1/users/me", "/v1/auth/login", "/api/actions"} {
		if !strings.Contains(out, want) {
			t.Fatalf("routes lacks %s", want)
		}
	}
	if _, out, _ = h.run("", "routes", "/api/"); strings.Contains(out, "/v1/") {
		t.Fatal("the filter let /v1 through")
	}
	var op operation
	_, out, _ = h.run("", "describe", "get", "/v1/skins/source")
	if err := json.Unmarshal([]byte(out), &op); err != nil || op.Path != "/v1/skins/source" {
		t.Fatalf("a literal segment should beat {id}: %s", out)
	}
	_, out, _ = h.run("", "describe", "GET", "/v1/users/me/dictionaries/pinyin?q=x")
	if err := json.Unmarshal([]byte(out), &op); err != nil || op.Path != "/v1/users/me/dictionaries/{kind}" || op.Auth != authUser {
		t.Fatalf("concrete path: %s", out)
	}
	_, out, _ = h.run("", "describe", "POST", "/v1/community/plugins/p1/download")
	if err := json.Unmarshal([]byte(out), &op); err != nil || op.Path != "/v1/community/plugins/{id}/download" {
		t.Fatalf("parameter in the middle: %s", out)
	}
	if code, _, _ := h.run("", "describe", "GET", "/v1/nothing"); code != 2 {
		t.Fatal("an unknown operation should be a usage error")
	}
}

func TestBadCommandLinesAreUsageErrors(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{},
		{"nonsense"},
		{"call", "GET"},
		{"call", "GET", "v1/users/me"},
		{"call", "POST", "/v1/auth/logout", "{not json"},
		{"call", "GET", "/v1/users/me", "-q"},
		{"login", "start"},
		{"login", "finish", "--challenge", "c1"},
	} {
		if code, _, _ := h.run("", args...); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
}

func TestAWrongCodeKeepsNoSession(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("", "login", "finish", "--challenge", "c1", "--code", "000000")
	if code != 1 || !strings.Contains(out, "invalid_credential") {
		t.Fatalf("finish: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("a failed sign-in wrote credentials")
	}
	if code, out, _ := h.run("", "login", "start", "--phone", "+8613800138000"); code != 0 || !strings.Contains(out, "c1") {
		t.Fatalf("phone start: %d %s", code, out)
	}
}

func TestAStaleLockIsTakenOverAndALiveOneWaitedFor(t *testing.T) {
	s := store{dir: t.TempDir()}
	lock := filepath.Join(s.dir, "credentials.lock")
	os.WriteFile(lock, nil, 0o600)
	old := time.Now().Add(-2 * lockStale)
	os.Chtimes(lock, old, old)
	unlock, err := s.lock()
	if err != nil {
		t.Fatalf("a stale lock should be taken over: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		unlock()
		close(released)
	}()
	second, err := s.lock()
	if err != nil {
		t.Fatalf("a released lock should be taken: %v", err)
	}
	<-released
	second()
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("the lock file was left behind")
	}
}

func TestAnUnreadableCredentialsFileIsReported(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.dir, "credentials.json"), []byte("{broken"), 0o600)
	if code, _, errText := h.run("", "whoami"); code != 1 || !strings.Contains(errText, "sign in again") {
		t.Fatalf("whoami: %d %s", code, errText)
	}
	if err := (store{}).put("x", nil); err != errNoConfigDir {
		t.Fatalf("a store without a directory: %v", err)
	}
	if _, err := (store{}).lock(); err != errNoConfigDir {
		t.Fatalf("a store without a directory: %v", err)
	}
}

func TestDefaultsPointAtProduction(t *testing.T) {
	c := cli{env: func(string) string { return "" }}
	if c.server() != defaultServer || c.adminServer() != defaultAdminServer {
		t.Fatal(c.server(), c.adminServer())
	}
	if dir := c.store().dir; !strings.HasSuffix(dir, "msime-cloud") {
		t.Fatalf("store dir %q", dir)
	}
	h := newHarness(t)
	if code, out, _ := h.run("", "help"); code != 0 || !strings.Contains(out, "usage: msime-cloud") {
		t.Fatal("help")
	}
	for _, args := range [][]string{{"routes", "a", "b"}, {"describe", "GET"}, {"whoami", "x"}, {"logout", "now"}, {"login"}, {"login", "again"}} {
		if code, _, _ := h.run("", args...); code != 2 {
			t.Fatalf("%v should be a usage error", args)
		}
	}
}

func TestLogoutAllAndAFailedRefreshAreReported(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	if code, _, errText := h.run("", "logout", "--all"); code != 0 {
		t.Fatalf("logout --all: %d %s", code, errText)
	}
	h.signIn()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":{"code":"upstream_error"}}`))
	}))
	defer broken.Close()
	c := cli{env: func(name string) string { return h.env[name] }, stdout: io.Discard, stderr: io.Discard, client: http.DefaultClient, now: time.Now}
	current, _, _ := c.store().get(c.server())
	sessions := c.store()
	sessions.put(broken.URL, &current)
	if _, err := c.refresh(broken.URL, current); err == nil || !strings.Contains(err.Error(), "upstream_error") {
		t.Fatalf("refresh: %v", err)
	}
	if kept, ok, _ := sessions.get(broken.URL); !ok || kept.RefreshToken != current.RefreshToken {
		t.Fatal("a refresh the server failed should keep the session for a retry")
	}
}
