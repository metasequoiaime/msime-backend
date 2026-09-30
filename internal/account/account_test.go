package account

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("MSIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("设置 MSIME_TEST_DATABASE_URL 运行 PostgreSQL 集成测试")
	}
	if !strings.Contains(dsn, "msime_auth_test") {
		t.Fatal("仅允许使用名为 msime_auth_test 的一次性数据库")
	}
	s, e := Open(context.Background(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	if e = s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	// 只允许指向一次性测试数据库，CI 使用专用 Postgres service。
	if _, e = s.pool.Exec(context.Background(), "TRUNCATE auth_users,auth_challenges,auth_sessions,auth_identities,auth_used_refresh,auth_rates CASCADE"); e != nil {
		t.Fatal(e)
	}
	return s
}
func complete(t *testing.T, s *Store, identity Identity) Tokens {
	t.Helper()
	ctx := context.Background()
	id := randomToken()
	c := Challenge{IDHash: hash(id), Provider: identity.Provider, Subject: identity.Subject}
	if e := s.PutChallenge(ctx, c); e != nil {
		t.Fatal(e)
	}
	v, e := s.Complete(ctx, c, identity)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestSessionRotationReplayLogoutAndDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first := complete(t, s, Identity{"email", "one@example.test"})
	p, e := s.Authenticate(ctx, first.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	next, e := s.Refresh(ctx, first.RefreshToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, first.AccessToken); !errors.Is(e, ErrInvalid) {
		t.Fatal("旧 access 令牌应失效", e)
	}
	if _, e = s.Authenticate(ctx, next.AccessToken); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Refresh(ctx, first.RefreshToken); !errors.Is(e, ErrInvalid) {
		t.Fatal("刷新重放应失败", e)
	}
	if _, e = s.Authenticate(ctx, next.AccessToken); !errors.Is(e, ErrInvalid) {
		t.Fatal("重放后应撤销会话族", e)
	}
	another := complete(t, s, Identity{"email", "one@example.test"})
	if another.User.ID != p.UserID {
		t.Fatal("重复登录不应重复建用户")
	}
	p, e = s.Authenticate(ctx, another.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Logout(ctx, p, true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Refresh(ctx, another.RefreshToken); !errors.Is(e, ErrInvalid) {
		t.Fatal("退出后不能刷新", e)
	}
	again := complete(t, s, Identity{"email", "one@example.test"})
	if e = s.DeleteUser(ctx, p.UserID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, again.AccessToken); !errors.Is(e, ErrInvalid) {
		t.Fatal("注销后访问应失败", e)
	}
	var n int
	if e = s.pool.QueryRow(ctx, "SELECT count(*) FROM auth_identities").Scan(&n); e != nil || n != 0 {
		t.Fatal("注销应清理身份", n, e)
	}
}
func TestChallengeSingleUseAttemptsExpiryAndLinkConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id := randomToken()
	c := Challenge{IDHash: hash(id), Provider: "google"}
	if e := s.PutChallenge(ctx, c); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 5; i++ {
		if _, e := s.Attempt(ctx, id); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Attempt(ctx, id); !errors.Is(e, ErrInvalid) {
		t.Fatal("超过五次应锁定", e)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Complete(ctx, c, Identity{"google", "subject"}); e == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("挑战只能消费一次", successes.Load())
	}
	expiredID := randomToken()
	expired := Challenge{IDHash: hash(expiredID), Provider: "email"}
	s.PutChallenge(ctx, expired)
	s.pool.Exec(ctx, "UPDATE auth_challenges SET expires_at=now()-interval '1 second' WHERE id_hash=$1", expired.IDHash)
	if _, e := s.Attempt(ctx, expiredID); !errors.Is(e, ErrInvalid) {
		t.Fatal("过期挑战", e)
	}
	one := complete(t, s, Identity{"email", "one@example.test"})
	two := complete(t, s, Identity{"google", "two"})
	link := Challenge{IDHash: hash(randomToken()), Provider: "google", LinkUser: one.User.ID}
	s.PutChallenge(ctx, link)
	if _, e := s.Complete(ctx, link, Identity{"google", "two"}); !errors.Is(e, ErrConflict) {
		t.Fatal("不能夺取其他用户身份", e)
	}
	if one.User.ID == two.User.ID {
		t.Fatal("独立身份不应自动合并")
	}
	link.IDHash = hash(randomToken())
	s.PutChallenge(ctx, link)
	bound, e := s.Complete(ctx, link, Identity{"google", "new"})
	if e != nil || bound.User.ID != one.User.ID {
		t.Fatal("显式绑定失败", e)
	}
}
func TestRateIsSharedAndBounded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var n atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Rate(ctx, "target", 5, time.Hour) == nil {
				n.Add(1)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 5 {
		t.Fatal(n.Load())
	}
}
func TestOIDCSignatureAudienceIssuerNonceAndExpiry(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if e != nil {
		t.Fatal(e)
	}
	a := &Service{verifiers: map[string]Verifier{"google": oidc.NewVerifier("https://accounts.google.com", &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: "client"})}}
	for _, test := range []struct {
		name, issuer, aud, nonce, sub string
		expiry                        int64
		valid                         bool
	}{
		{"合法", "https://accounts.google.com", "client", "nonce", "subject", time.Now().Add(time.Minute).Unix(), true},
		{"错误 audience", "https://accounts.google.com", "attacker", "nonce", "subject", time.Now().Add(time.Minute).Unix(), false},
		{"错误 issuer", "https://attacker.invalid", "client", "nonce", "subject", time.Now().Add(time.Minute).Unix(), false},
		{"错误 nonce", "https://accounts.google.com", "client", "other", "subject", time.Now().Add(time.Minute).Unix(), false},
		{"过期", "https://accounts.google.com", "client", "nonce", "subject", time.Now().Add(-time.Hour).Unix(), false},
		{"空 subject", "https://accounts.google.com", "client", "nonce", "", time.Now().Add(time.Minute).Unix(), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{"iss": test.issuer, "aud": test.aud, "nonce": test.nonce, "sub": test.sub, "exp": test.expiry, "iat": time.Now().Unix()})
			signed, _ := signer.Sign(b)
			token, _ := signed.CompactSerialize()
			_, _, e := a.identity(context.Background(), Challenge{Provider: "google", Nonce: "nonce"}, token)
			if (e == nil) != test.valid {
				t.Fatal(e)
			}
		})
	}
	if _, _, e = a.identity(context.Background(), Challenge{Provider: "google", Nonce: "nonce"}, "eyJhbGciOiJub25lIn0.e30."); e == nil {
		t.Fatal("不能接受无签名令牌")
	}
}

type captureSender struct {
	code string
	fail bool
}

func (s *captureSender) Send(_ context.Context, _, _, code string) error {
	s.code = code
	if s.fail {
		return errors.New("不可用")
	}
	return nil
}
func TestEmailHTTPFlowAndFailure(t *testing.T) {
	s := testStore(t)
	t.Setenv("AUTH_TEST_PEPPER", strings.Repeat("p", 32))
	sender := &captureSender{}
	a := &Service{store: s, config: Config{PepperEnv: "AUTH_TEST_PEPPER", Email: MailConfig{From: "login@example.test"}}, sender: sender}
	mux := http.NewServeMux()
	Mount(mux, a)
	request := func(path string, v any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(v)
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request("/v1/auth/challenges", map[string]string{"provider": "email", "target": "one@example.test"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), sender.code) {
		t.Fatal("响应不得泄露验证码")
	}
	var c struct {
		ID string `json:"challenge_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &c)
	if w = request("/v1/auth/login", map[string]string{"challenge_id": c.ID, "credential": "abcdef"}); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = request("/v1/auth/login", map[string]string{"challenge_id": c.ID, "credential": sender.code})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("/v1/auth/login", map[string]string{"challenge_id": c.ID, "credential": sender.code}); w.Code != 401 {
		t.Fatal("OTP 重放应失败", w.Code)
	}
	sender.fail = true
	w = request("/v1/auth/challenges", map[string]string{"provider": "email", "target": "two@example.test"})
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	var n int
	s.pool.QueryRow(context.Background(), "SELECT count(*) FROM auth_challenges WHERE subject='two@example.test'").Scan(&n)
	if n != 0 {
		t.Fatal("失败发送应删除挑战")
	}
}
