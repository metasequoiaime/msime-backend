package account

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

const (
	testDesktopClient = "desktop-client"
	testWebClient     = "web-client"
	testLoopback      = "http://127.0.0.1:53682/callback"
)

// googleFixture signs ID tokens and serves a fake Google token endpoint so the exchange runs end to end without the network.
type googleFixture struct {
	t        *testing.T
	signer   jose.Signer
	verifier Verifier
	server   *httptest.Server
	key      []byte
	// Per-exchange behaviour and the last form the token endpoint received.
	status    int
	claims    map[string]any
	refresh   string
	noIDToken bool
	form      url.Values
}

func newGoogleFixture(t *testing.T) *googleFixture {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: rsaKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&rsaKey.PublicKey}}
	f := &googleFixture{t: t, signer: signer, status: 200, key: bytes.Repeat([]byte{7}, 32)}
	// Same shape as makeVerifiers: one verifier per configured audience.
	f.verifier = audienceVerifiers{
		oidc.NewVerifier("https://accounts.google.com", keys, &oidc.Config{ClientID: testWebClient, SupportedSigningAlgs: []string{"RS256"}}),
		oidc.NewVerifier("https://accounts.google.com", keys, &oidc.Config{ClientID: testDesktopClient, SupportedSigningAlgs: []string{"RS256"}}),
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f.form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		if f.status != 200 {
			w.WriteHeader(f.status)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		body := map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 3599, "scope": "openid email profile"}
		if !f.noIDToken {
			body["id_token"] = f.sign(f.claims)
		}
		if f.refresh != "" {
			body["refresh_token"] = f.refresh
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *googleFixture) sign(claims map[string]any) string {
	f.t.Helper()
	b, _ := json.Marshal(claims)
	signed, err := f.signer.Sign(b)
	if err != nil {
		f.t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

func googleClaims(aud, subject, nonce, name string) map[string]any {
	return map[string]any{"iss": "https://accounts.google.com", "aud": aud, "sub": subject, "nonce": nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "email": subject + "@example.test", "email_verified": true, "name": name, "picture": "https://example.test/" + subject + ".png"}
}

func (f *googleFixture) service(db *Store) *Service {
	return &Service{store: db, client: f.server.Client(), tokenKey: f.key, googleTokenURL: f.server.URL, verifiers: map[string]Verifier{"google": f.verifier}, config: Config{
		PepperEnv: "GOOGLE_TEST_PEPPER",
		Google:    GoogleConfig{ClientIDs: []string{testWebClient, testDesktopClient}, Desktop: GoogleDesktopConfig{ClientID: testDesktopClient, SecretEnv: "GOOGLE_TEST_DESKTOP_SECRET"}},
	}}
}

func openSealed(t *testing.T, key, sealed []byte, identity Identity) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) < gcm.NonceSize() {
		t.Fatal("sealed token shorter than nonce")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(identity.Provider+":"+identity.Subject))
	if err != nil {
		t.Fatal("sealed token does not open with provider:subject AAD", err)
	}
	return string(plain)
}

func TestGoogleLoopbackTarget(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1:1024/callback", "http://127.0.0.1:53682/callback", "http://127.0.0.1:65535/callback", "http://[::1]:8080/callback"} {
		if !loopbackTarget(target) {
			t.Error("loopback target rejected", target)
		}
	}
	for _, target := range []string{
		"", "http://127.0.0.1/callback", "http://127.0.0.1:/callback", "http://127.0.0.1:80/callback", "http://127.0.0.1:1023/callback", "http://127.0.0.1:65536/callback", "http://127.0.0.1:099999/callback", "http://127.0.0.1:08080/callback", "http://127.0.0.1:+8080/callback", "http://127.0.0.1:-1/callback",
		"https://127.0.0.1:8080/callback", "HTTP://127.0.0.1:8080/callback", "http://localhost:8080/callback", "http://127.0.0.2:8080/callback", "http://::1:8080/callback", "http://[::1]/callback", "http://attacker.test:8080/callback",
		"http://user@127.0.0.1:8080/callback", "http://127.0.0.1:8080@attacker.test/callback", "http://127.0.0.1:8080/callback?x=1", "http://127.0.0.1:8080/callback?", "http://127.0.0.1:8080/callback#", "http://127.0.0.1:8080/callback#x",
		"http://127.0.0.1:8080/", "http://127.0.0.1:8080/callback/", "http://127.0.0.1:8080/other/callback", "http://127.0.0.1:8080/CALLBACK", " http://127.0.0.1:8080/callback", "http://127.0.0.1:8080/callback ",
	} {
		if loopbackTarget(target) {
			t.Error("non-loopback target accepted", target)
		}
	}
}

func TestGoogleDesktopConfigurationValidation(t *testing.T) {
	t.Setenv("CONFIG_DB", "postgres://local/test")
	t.Setenv("CONFIG_PEPPER", strings.Repeat("p", 32))
	t.Setenv("CONFIG_DESKTOP_SECRET", "secret")
	t.Setenv("CONFIG_TOKEN_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	t.Setenv("CONFIG_SHORT_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16)))
	t.Setenv("CONFIG_URL_KEY", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)))
	base := Config{Enabled: true, DatabaseEnv: "CONFIG_DB", PepperEnv: "CONFIG_PEPPER", TokenKeyEnv: "CONFIG_TOKEN_KEY",
		Google: GoogleConfig{ClientIDs: []string{testWebClient, testDesktopClient}, Desktop: GoogleDesktopConfig{ClientID: testDesktopClient, SecretEnv: "CONFIG_DESKTOP_SECRET"}}}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"secret env missing":         func(c *Config) { c.Google.Desktop.SecretEnv = "MISSING_DESKTOP_SECRET" },
		"secret env unnamed":         func(c *Config) { c.Google.Desktop.SecretEnv = "" },
		"desktop not an audience":    func(c *Config) { c.Google.ClientIDs = []string{testWebClient} },
		"token key env missing":      func(c *Config) { c.TokenKeyEnv = "MISSING_TOKEN_KEY" },
		"token key env unnamed":      func(c *Config) { c.TokenKeyEnv = "" },
		"token key too short":        func(c *Config) { c.TokenKeyEnv = "CONFIG_SHORT_KEY" },
		"token key not std base64":   func(c *Config) { c.TokenKeyEnv = "CONFIG_URL_KEY" },
		"token key is the plaintext": func(c *Config) { c.TokenKeyEnv = "CONFIG_PEPPER" },
	} {
		c := base
		change(&c)
		if c.Validate() == nil {
			t.Error("invalid Google desktop configuration accepted:", name)
		}
	}
	// Without a desktop client neither the secret nor the token key is required, so existing deployments keep validating.
	c := base
	c.Google.Desktop = GoogleDesktopConfig{}
	c.TokenKeyEnv = ""
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGoogleServerExchangeIdentity(t *testing.T) {
	t.Setenv("GOOGLE_TEST_DESKTOP_SECRET", "desktop-secret")
	f := newGoogleFixture(t)
	a := f.service(nil)
	c := Challenge{Provider: "google", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), RedirectURI: testLoopback}
	f.claims = googleClaims(testDesktopClient, "person", "nonce", "Person")
	f.refresh = "refresh-secret"
	identity, grant, err := a.identity(t.Context(), c, "auth-code")
	if err != nil || identity != (Identity{"google", "person"}) || grant == nil {
		t.Fatal(identity, grant, err)
	}
	for key, want := range map[string]string{"grant_type": "authorization_code", "code": "auth-code", "redirect_uri": testLoopback, "client_id": testDesktopClient, "client_secret": "desktop-secret", "code_verifier": c.CodeVerifier} {
		if got := f.form.Get(key); got != want {
			t.Errorf("token request %s = %q, want %q", key, got, want)
		}
	}
	if p := grant.Profile; p == nil || *p != (providerProfile{Email: "person@example.test", EmailVerified: true, Name: "Person", Picture: "https://example.test/person.png"}) {
		t.Fatal("profile", p)
	}
	if grant.Scope != "openid email profile" || bytes.Contains(grant.SealedRefresh, []byte("refresh-secret")) || openSealed(t, f.key, grant.SealedRefresh, identity) != "refresh-secret" {
		t.Fatal("refresh token not sealed")
	}
	// The AAD binds the ciphertext to its identity.
	block, _ := aes.NewCipher(f.key)
	gcm, _ := cipher.NewGCM(block)
	if _, err := gcm.Open(nil, grant.SealedRefresh[:gcm.NonceSize()], grant.SealedRefresh[gcm.NonceSize():], []byte("google:other")); err == nil {
		t.Fatal("sealed token opened for another identity")
	}
	f.refresh = ""
	if _, grant, err = a.identity(t.Context(), c, "auth-code"); err != nil || grant.SealedRefresh != nil {
		t.Fatal("absent refresh token must not produce a sealed value", grant, err)
	}
	for name, change := range map[string]func(){
		"nonce mismatch":      func() { f.claims = googleClaims(testDesktopClient, "person", "other", "Person") },
		"foreign audience":    func() { f.claims = googleClaims("attacker-client", "person", "nonce", "Person") },
		"missing id_token":    func() { f.noIDToken = true },
		"token endpoint 400":  func() { f.status = 400 },
		"token endpoint 500":  func() { f.status = 500 },
		"expired id token":    func() { f.claims["exp"] = time.Now().Add(-time.Hour).Unix() },
		"empty subject claim": func() { f.claims["sub"] = "" },
	} {
		f.status, f.noIDToken, f.claims = 200, false, googleClaims(testDesktopClient, "person", "nonce", "Person")
		change()
		if _, _, err := a.identity(t.Context(), c, "auth-code"); err != ErrInvalid {
			t.Error(name, "accepted or wrong error", err)
		}
	}
	// A transport failure is reported as an invalid credential and never echoes upstream detail.
	a.googleTokenURL = "http://127.0.0.1:1/token"
	if _, _, err := a.identity(t.Context(), c, "auth-code"); err != ErrInvalid {
		t.Fatal(err)
	}
}

func TestGoogleExchangeAndIDTokenLoginsStoreProfileAndSealedRefreshToken(t *testing.T) {
	db := testStore(t)
	t.Setenv("GOOGLE_TEST_PEPPER", strings.Repeat("p", 32))
	t.Setenv("GOOGLE_TEST_DESKTOP_SECRET", "desktop-secret")
	f := newGoogleFixture(t)
	a := f.service(db)
	mux := http.NewServeMux()
	Mount(mux, a)
	type challenge struct {
		ID    string `json:"challenge_id"`
		Nonce string `json:"nonce"`
		URL   string `json:"authorization_url"`
	}
	begin := func(target string) challenge {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"provider": "google", "target": target, "purpose": "login"})
		w := apiRequest(t, mux, "POST", "/v1/auth/challenges", string(raw), "", 201)
		var c challenge
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || len(c.ID) != 64 || len(c.Nonce) != 64 {
			t.Fatal(w.Body.String(), err)
		}
		return c
	}
	login := func(id, credential string, status int) Tokens {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"challenge_id": id, "credential": credential})
		w := apiRequest(t, mux, "POST", "/v1/auth/login", string(raw), "", status)
		var out Tokens
		if status == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.AccessToken == "" {
				t.Fatal(w.Body.String(), err)
			}
		}
		return out
	}
	stored := func(subject string) (email string, verified bool, name, picture string, updated bool, sealed []byte) {
		t.Helper()
		if err := db.pool.QueryRow(t.Context(), `SELECT i.email,i.email_verified,i.name,i.picture,i.updated_at IS NOT NULL,COALESCE(p.refresh_token,''::bytea)
 FROM auth_identities i LEFT JOIN auth_provider_tokens p USING(provider,subject) WHERE i.provider='google' AND i.subject=$1`, subject).Scan(&email, &verified, &name, &picture, &updated, &sealed); err != nil {
			t.Fatal(err)
		}
		return
	}

	for _, target := range []string{"http://localhost:53682/callback", "http://127.0.0.1:80/callback", "https://127.0.0.1:53682/callback", "http://127.0.0.1:53682/callback?x=1"} {
		raw, _ := json.Marshal(map[string]string{"provider": "google", "target": target})
		w := apiRequest(t, mux, "POST", "/v1/auth/challenges", string(raw), "", 400)
		if !strings.Contains(w.Body.String(), "invalid_target") {
			t.Fatal(w.Body.String())
		}
	}

	c := begin(testLoopback)
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != googleAuthEndpoint {
		t.Fatal(c.URL, err)
	}
	q := u.Query()
	var verifier, redirect string
	if err = db.pool.QueryRow(t.Context(), "SELECT code_verifier,redirect_uri FROM auth_challenges WHERE id_hash=$1", hash(c.ID)).Scan(&verifier, &redirect); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	for key, want := range map[string]string{"client_id": testDesktopClient, "redirect_uri": testLoopback, "response_type": "code", "scope": "openid email profile", "nonce": c.Nonce, "code_challenge": base64.RawURLEncoding.EncodeToString(sum[:]), "code_challenge_method": "S256", "access_type": "offline", "prompt": "consent"} {
		if got := q.Get(key); got != want {
			t.Errorf("authorization_url %s = %q, want %q", key, got, want)
		}
	}
	if len(verifier) < 43 || len(verifier) > 128 || redirect != testLoopback || len(q.Get("state")) != 64 || q.Get("state") == c.ID || strings.Contains(c.URL, verifier) || strings.Contains(c.URL, "desktop-secret") {
		t.Fatal("PKCE verifier, redirect or state not bound correctly", len(verifier), redirect, q.Get("state"))
	}

	f.claims, f.refresh = googleClaims(testDesktopClient, "desktop-person", c.Nonce, "First Name"), "refresh-one"
	first := login(c.ID, "code-one", 200)
	if f.form.Get("code_verifier") != verifier || f.form.Get("redirect_uri") != testLoopback {
		t.Fatal("exchange did not use the stored PKCE verifier and redirect URI")
	}
	login(c.ID, "code-one", 401)
	email, verified, name, picture, updated, sealed := stored("desktop-person")
	identity := Identity{"google", "desktop-person"}
	if email != "desktop-person@example.test" || !verified || name != "First Name" || picture != "https://example.test/desktop-person.png" || !updated {
		t.Fatal("profile not stored", email, verified, name, picture, updated)
	}
	if bytes.Contains(sealed, []byte("refresh-one")) || openSealed(t, f.key, sealed, identity) != "refresh-one" {
		t.Fatal("refresh token not encrypted at rest")
	}

	// A later consent without refresh_token keeps the stored token but still refreshes the profile.
	c = begin("http://[::1]:61000/callback")
	f.claims, f.refresh = googleClaims(testDesktopClient, "desktop-person", c.Nonce, "Renamed"), ""
	again := login(c.ID, "code-two", 200)
	if again.User.ID != first.User.ID {
		t.Fatal("same Google subject created a second user")
	}
	_, _, name, _, _, sealed = stored("desktop-person")
	if name != "Renamed" || openSealed(t, f.key, sealed, identity) != "refresh-one" {
		t.Fatal("stored refresh token lost or profile not refreshed", name)
	}
	c = begin(testLoopback)
	f.claims, f.refresh = googleClaims(testDesktopClient, "desktop-person", c.Nonce, "Renamed"), "refresh-two"
	login(c.ID, "code-three", 200)
	if _, _, _, _, _, sealed = stored("desktop-person"); openSealed(t, f.key, sealed, identity) != "refresh-two" {
		t.Fatal("new refresh token not stored")
	}

	// Nonce mismatch and a failed exchange are ordinary invalid credentials.
	c = begin(testLoopback)
	f.claims = googleClaims(testDesktopClient, "desktop-person", "other-nonce", "Renamed")
	login(c.ID, "code-four", 401)
	c = begin(testLoopback)
	f.status = 400
	login(c.ID, "code-five", 401)
	f.status = 200

	// The ID token flow (no target) is unchanged apart from also upserting the profile; it has no refresh token.
	c = begin("")
	if c.URL != "" {
		t.Fatal("ID token flow must not return an authorization_url", c.URL)
	}
	login(c.ID, f.sign(googleClaims(testWebClient, "android-person", c.Nonce, "Android")), 200)
	email, verified, name, _, updated, sealed = stored("android-person")
	if email != "android-person@example.test" || !verified || name != "Android" || !updated || len(sealed) != 0 {
		t.Fatal("ID token login profile", email, verified, name, updated, len(sealed))
	}

	// Other providers leave the profile columns empty.
	other := complete(t, db, Identity{"email", "plain@example.test"})
	var otherEmail string
	var otherUpdated bool
	if err = db.pool.QueryRow(t.Context(), "SELECT email,updated_at IS NOT NULL FROM auth_identities WHERE provider='email' AND subject='plain@example.test'").Scan(&otherEmail, &otherUpdated); err != nil || otherEmail != "" || otherUpdated {
		t.Fatal("non-Google identity gained profile data", otherEmail, otherUpdated, err)
	}

	// Deleting the account removes the stored provider token through the identity cascade.
	if err = db.DeleteUser(t.Context(), first.User.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_provider_tokens WHERE subject='desktop-person'").Scan(&n); err != nil || n != 0 {
		t.Fatal("provider token survived account deletion", n, err)
	}
	apiRequest(t, mux, "GET", "/v1/users/me", "", other.AccessToken, 200)

	// Without a desktop client the exchange flow is disabled while the ID token flow keeps working.
	a.config.Google.Desktop = GoogleDesktopConfig{}
	w := apiRequest(t, mux, "POST", "/v1/auth/challenges", `{"provider":"google","target":"`+testLoopback+`"}`, "", 503)
	if !strings.Contains(w.Body.String(), "provider_disabled") {
		t.Fatal(w.Body.String())
	}
	begin("")
}

func TestProviderTokenSealingUsesFreshNonces(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	identity := Identity{"google", "subject"}
	one, err := sealProviderToken(key, identity, "token")
	if err != nil {
		t.Fatal(err)
	}
	two, err := sealProviderToken(key, identity, "token")
	if err != nil || bytes.Equal(one, two) || openSealed(t, key, one, identity) != "token" || openSealed(t, key, two, identity) != "token" {
		t.Fatal("sealing must be randomized and reversible", err)
	}
	if _, err = sealProviderToken(nil, identity, "token"); err == nil {
		t.Fatal("missing key must fail instead of storing plaintext")
	}
}
