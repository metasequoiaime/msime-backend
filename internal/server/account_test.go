package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/jackc/pgx/v5"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

func TestUserSessionAuthorizesAPIAndDeviceCannotManageUsers(t *testing.T) {
	dsn := os.Getenv("MSIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要独立测试 PostgreSQL")
	}
	if !strings.Contains(dsn, "msime_auth_test") {
		t.Fatal("只能使用 msime_auth_test 测试数据库")
	}
	ctx := context.Background()
	// go test runs packages concurrently. Isolate this integration test from the
	// account package, which truncates its disposable schema between cases.
	admin, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close(ctx)
	schema := "server_test_" + time.Now().Format("20060102150405000000000")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
	parsed, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	params := parsed.Query()
	params.Set("search_path", schema)
	parsed.RawQuery = params.Encode()
	dsn = parsed.String()
	t.Setenv("MSIME_TEST_DATABASE_URL", dsn)
	db, e := account.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("b", 32) + time.Now().Format("20060102150405.000000000")
	sum := sha256.Sum256([]byte(id))
	challenge := account.Challenge{IDHash: hex.EncodeToString(sum[:]), Provider: "email", Subject: id + "@example.com"}
	if e = db.PutChallenge(ctx, challenge); e != nil {
		t.Fatal(e)
	}
	tokens, e := db.Complete(ctx, challenge, account.Identity{Provider: "email", Subject: challenge.Subject})
	if e != nil {
		t.Fatal(e)
	}
	defer db.DeleteUser(ctx, tokens.User.ID)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	s, e := New(Config{Auth: account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"}, Clients: []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 120}}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.CloseAccounts()
	defer s.Close()
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{"/v1/capabilities", tokens.AccessToken, 200},
		{"/v1/users/me", tokens.AccessToken, 200},
		{"/v1/capabilities", testToken, 200},
		{"/v1/users/me", testToken, 401},
		{"/v1/capabilities", tokens.RefreshToken, 401},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.status)
		}
	}
	// A ban written straight into the database leaves the session alive; the bearer middleware tells the client the account is banned instead of asking it to sign in again.
	bannedSum := sha256.Sum256([]byte(id + "banned"))
	bannedChallenge := account.Challenge{IDHash: hex.EncodeToString(bannedSum[:]), Provider: "email", Subject: "banned-" + id + "@example.com"}
	if e = db.PutChallenge(ctx, bannedChallenge); e != nil {
		t.Fatal(e)
	}
	banned, e := db.Complete(ctx, bannedChallenge, account.Identity{Provider: "email", Subject: bannedChallenge.Subject})
	if e != nil {
		t.Fatal(e)
	}
	defer db.DeleteUser(ctx, banned.User.ID)
	if _, e = admin.Exec(ctx, "UPDATE "+quoted+".auth_users SET banned_at=now(),ban_reason='manual' WHERE id=$1", banned.User.ID); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/v1/capabilities", "/v1/users/me"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+banned.AccessToken)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "account_banned") {
			t.Fatalf("%s for a banned account: %d %s", path, w.Code, w.Body.String())
		}
	}
	p, e := db.Authenticate(ctx, tokens.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Logout(ctx, p, false); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "/v1/capabilities", nil)
	r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("已退出会话仍可调用 API")
	}
}

// 遥测不带凭据穿过整个服务端：中间件让它绕过 Bearer 认证，官网的跨域请求和预检都能得到响应，按地址的额度按代理报告的地址计。
func TestAnonymousTelemetryThroughServer(t *testing.T) {
	disposableSchema(t)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	s, err := New(Config{
		Auth:           account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
		AllowedOrigins: []string{"https://msime.app"},
		ClientIPHeader: "CF-Connecting-IP",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.CloseAccounts()
	send := func(method, origin, client, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://api.example.com"+account.TelemetryPath, strings.NewReader(body))
		r.RemoteAddr = "10.0.0.2:443"
		r.Header.Set("CF-Connecting-IP", client)
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	event := func(i int) string {
		return `{"id":"server-telemetry-` + strconv.Itoa(10000+i) + `","kind":"download","platform":"windows","version":"0.9.3","artifact":"msime-setup.exe","channel":"cn-mirror"}`
	}
	if w := send("OPTIONS", "https://msime.app", "198.51.100.40", "", ""); w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://msime.app" {
		t.Fatal("preflight", w.Code, w.Header())
	}
	if w := send("POST", "https://msime.app", "198.51.100.40", "", event(0)); w.Code != 202 || w.Header().Get("Access-Control-Allow-Origin") != "https://msime.app" {
		t.Fatal("website report", w.Code, w.Body.String())
	}
	if w := send("POST", "", "198.51.100.40", "Bearer stale-or-forged", event(1)); w.Code != 202 {
		t.Fatal("a token must be ignored, not checked", w.Code, w.Body.String())
	}
	if w := send("POST", "https://evil.example", "198.51.100.40", "", event(2)); w.Code != 403 {
		t.Fatal("unlisted origin", w.Code)
	}
	for i := 3; i <= 60; i++ {
		if w := send("POST", "", "198.51.100.40", "", event(i)); w.Code != 202 {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	if w := send("POST", "", "198.51.100.40", "", event(61)); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("per-address limit", w.Code)
	}
	if w := send("POST", "", "198.51.100.41", "", event(62)); w.Code != 202 {
		t.Fatal("another client behind the same proxy was limited", w.Code)
	}
}
