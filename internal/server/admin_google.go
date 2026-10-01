package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	msimebackend "github.com/metasequoiaime/MSIME-Backend"
	"github.com/metasequoiaime/MSIME-Backend/internal/account"
	"golang.org/x/oauth2"
)

const adminCallbackPath = "/api/auth/google/callback"

type AdminGoogleConfig struct {
	ClientID         string   `json:"client_id"`
	SecretEnv        string   `json:"secret_env"`
	RedirectURI      string   `json:"redirect_uri"`
	AllowedEmails    []string `json:"allowed_emails"`
	AllowedEmailsEnv string   `json:"allowed_emails_env"`
	secret           string
}

func (c *AdminGoogleConfig) validate(host string) error {
	if c.ClientID == "" {
		return nil
	}
	if c.SecretEnv == "" {
		c.SecretEnv = "MSIME_ADMIN_GOOGLE_SECRET"
	}
	c.secret = os.Getenv(c.SecretEnv)
	if c.RedirectURI == "" {
		c.RedirectURI = "https://" + host + adminCallbackPath
	}
	u, err := url.Parse(c.RedirectURI)
	if err != nil || u.Hostname() != host || u.User != nil || u.Path != adminCallbackPath || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && host == "admin.localhost")) {
		return errors.New("invalid admin Google redirect_uri")
	}
	if strings.TrimSpace(c.ClientID) != c.ClientID || len(c.ClientID) > 255 || c.secret == "" || strings.ContainsAny(c.secret, "\r\n") {
		return errors.New("admin Google credentials missing or invalid")
	}
	if c.AllowedEmailsEnv != "" {
		value := strings.TrimSpace(os.Getenv(c.AllowedEmailsEnv))
		if value == "" {
			return errors.New("admin Google allowed emails environment variable missing")
		}
		c.AllowedEmails = strings.Split(value, ",")
		for i := range c.AllowedEmails {
			c.AllowedEmails[i] = strings.TrimSpace(c.AllowedEmails[i])
		}
	}
	if len(c.AllowedEmails) == 0 || len(c.AllowedEmails) > 100 {
		return errors.New("admin Google requires 1..100 allowed_emails")
	}
	seen := map[string]bool{}
	for i, email := range c.AllowedEmails {
		email = strings.ToLower(email)
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email || seen[email] {
			return errors.New("admin Google allowed_emails must be unique email addresses")
		}
		c.AllowedEmails[i] = email
		seen[email] = true
	}
	return nil
}
func (c AdminGoogleConfig) allows(email string) bool {
	for _, allowed := range c.AllowedEmails {
		if strings.EqualFold(email, allowed) {
			return true
		}
	}
	return false
}

type adminAuthStore interface {
	SaveAdminFlow(context.Context, string, account.AdminLoginFlow) error
	ConsumeAdminFlow(context.Context, string) (account.AdminLoginFlow, error)
	CreateAdminSession(context.Context, account.AdminIdentity) (string, error)
	AdminSession(context.Context, string) (account.AdminIdentity, error)
	DeleteAdminSession(context.Context, string) error
	AdminEmailAllowed(context.Context, string) (bool, error)
	// AdminMemberRole resolves an enabled member's role and permissions; see account.Service.AdminMemberRole.
	AdminMemberRole(context.Context, string) (string, []string, error)
	// AdminTokenIdentity resolves a personal access token (account.AdminTokenPrefix) to its identity.
	AdminTokenIdentity(context.Context, string) (account.AdminIdentity, error)
}

// adminGoogleScopes asks for the profile scope only to show the admin's Google name on the console; the avatar is never loaded because the console's CSP allows images from itself only.
var adminGoogleScopes = []string{oidc.ScopeOpenID, "email", "profile"}

type adminGoogleAuth struct {
	oauth    oauth2.Config
	verifier account.Verifier
}

func (s *Server) initAdminGoogle() {
	if s.accounts != nil {
		s.adminStore = s.accounts
	}
	c := s.config.Admin.Google
	if !s.config.Admin.Enabled || c.ClientID == "" {
		return
	}
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(s.lifetime, s.client), "https://www.googleapis.com/oauth2/v3/certs")
	s.adminGoogle = &adminGoogleAuth{
		oauth:    oauth2.Config{ClientID: c.ClientID, ClientSecret: c.secret, RedirectURL: c.RedirectURI, Scopes: adminGoogleScopes, Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams}},
		verifier: oidc.NewVerifier("https://accounts.google.com", keys, &oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{"RS256"}}),
	}
}
func adminRandom() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}
func (s *Server) adminCookieName(flow bool) string {
	name := "__Host-msime_admin"
	if s.config.Admin.Host == "admin.localhost" && strings.HasPrefix(s.config.Admin.Google.RedirectURI, "http://") {
		name = "msime_admin_local"
	}
	if flow {
		name += "_flow"
	}
	return name
}
func (s *Server) adminCookie(w http.ResponseWriter, value string, flow bool, maxAge int) {
	name := s.adminCookieName(flow)
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(name, "__Host-"), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (s *Server) adminBearer(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	supplied := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
	expected := sha256.Sum256([]byte(s.config.Admin.token))
	return s.config.Admin.token != "" && strings.HasPrefix(auth, "Bearer ") && subtle.ConstantTimeCompare(supplied[:], expected[:]) == 1
}

// adminPersonalToken returns the personal access token the request carries, if any.
func adminPersonalToken(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token, ok && strings.HasPrefix(token, account.AdminTokenPrefix)
}

func (s *Server) adminIdentity(r *http.Request) (string, string, error) {
	if s.adminBearer(r) {
		return "legacy-token", "", nil
	}
	// A personal access token stands for its holder, whose membership is checked as for a session.
	if token, ok := adminPersonalToken(r); ok {
		if s.adminStore == nil {
			return "", "", account.ErrInvalid
		}
		identity, err := s.adminStore.AdminTokenIdentity(r.Context(), token)
		if err != nil {
			return "", "", err
		}
		allowed, err := s.adminEmailAllowed(r.Context(), identity.Email)
		if err != nil {
			return "", "", err
		}
		if !allowed {
			return "", "", account.ErrInvalid
		}
		return "pat:" + strings.ToLower(identity.Email), identity.Email, nil
	}
	if s.adminGoogle == nil {
		return "", "", account.ErrInvalid
	}
	cookie, err := r.Cookie(s.adminCookieName(false))
	if err != nil {
		return "", "", account.ErrInvalid
	}
	identity, err := s.adminStore.AdminSession(r.Context(), cookie.Value)
	if err != nil {
		return "", "", err
	}
	allowed, err := s.adminEmailAllowed(r.Context(), identity.Email)
	if err != nil {
		return "", "", err
	}
	if !allowed {
		return "", "", account.ErrInvalid
	}
	return "google:" + identity.Subject + ":" + identity.Email, identity.Email, nil
}
func (s *Server) adminMutationOrigin(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" || s.adminBearer(r) {
		return true
	}
	// Bearer tokens are never sent by the browser on its own, so they need no cross-site protection.
	if _, ok := adminPersonalToken(r); ok {
		return true
	}
	// Cookie authentication requires an explicit same-origin browser request.
	if s.adminGoogle == nil && r.Header.Get("Origin") != "" {
		return true
	}
	origin := r.Header.Get("Origin")
	expected, err := url.Parse(s.config.Admin.Google.RedirectURI)
	if origin == "" || err != nil || expected.Host == "" || origin != expected.Scheme+"://"+expected.Host {
		fail(w, 403, "origin_required")
		return false
	}
	return true
}
func (s *Server) adminAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, account.ErrInvalid) {
		fail(w, 401, "unauthorized")
	} else {
		fail(w, 503, "admin_auth_unavailable")
	}
}
func (s *Server) adminAuthRoute(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/auth/") {
		return false
	}
	peer, _, peerErr := net.SplitHostPort(r.RemoteAddr)
	if peerErr != nil {
		peer = r.RemoteAddr
	}
	if !s.allow(Client{ID: "admin-auth:" + peer, RequestsPerMinute: 120}, time.Now()) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limit_exceeded")
		return true
	}
	switch r.URL.Path {
	case "/api/auth/session":
		if r.Method != "GET" {
			fail(w, 405, "method_not_allowed")
			return true
		}
		_, email, err := s.adminIdentity(r)
		if err != nil && !errors.Is(err, account.ErrInvalid) {
			s.adminAuthError(w, err)
			return true
		}
		respond(w, 200, map[string]any{"version": msimebackend.Version(), "authenticated": err == nil, "email": email, "google_enabled": s.adminGoogle != nil, "token_enabled": s.config.Admin.token != "", "can_manage_admins": err == nil && s.config.Admin.Google.allows(email)})
	case "/api/auth/logout":
		if r.Method != "POST" {
			fail(w, 405, "method_not_allowed")
			return true
		}
		if !s.adminMutationOrigin(w, r) {
			return true
		}
		if cookie, err := r.Cookie(s.adminCookieName(false)); err == nil && s.adminStore != nil {
			if err = s.adminStore.DeleteAdminSession(r.Context(), cookie.Value); err != nil {
				s.adminAuthError(w, err)
				return true
			}
		}
		s.adminCookie(w, "", false, -1)
		s.adminCookie(w, "", true, -1)
		respond(w, 200, map[string]bool{"ok": true})
	case "/api/auth/google/start":
		if r.Method != "GET" {
			fail(w, 405, "method_not_allowed")
			return true
		}
		if s.adminGoogle == nil {
			fail(w, 404, "google_disabled")
			return true
		}
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			peer = r.RemoteAddr
		}
		if !s.allow(Client{ID: "admin-login:" + peer, RequestsPerMinute: 10}, time.Now()) {
			fail(w, 429, "rate_limit_exceeded")
			return true
		}
		state, nonce, verifier := adminRandom(), adminRandom(), oauth2.GenerateVerifier()
		if err := s.adminStore.SaveAdminFlow(r.Context(), state, account.AdminLoginFlow{Nonce: nonce, Verifier: verifier}); err != nil {
			s.adminAuthError(w, err)
			return true
		}
		s.adminCookie(w, state, true, 600)
		http.Redirect(w, r, s.adminGoogle.oauth.AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce), oauth2.SetAuthURLParam("prompt", "select_account"), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
	case adminCallbackPath:
		if r.Method != "GET" {
			fail(w, 405, "method_not_allowed")
			return true
		}
		if s.adminGoogle == nil {
			fail(w, 404, "google_disabled")
			return true
		}
		s.adminGoogleCallback(w, r)
	default:
		fail(w, 404, "not_found")
	}
	return true
}
func (s *Server) adminGoogleCallback(w http.ResponseWriter, r *http.Request) {
	denied := func() {
		s.adminCookie(w, "", true, -1)
		http.Redirect(w, r, "/?login_error=google_denied", http.StatusSeeOther)
	}
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(s.adminCookieName(true))
	if err != nil || len(state) != 64 || subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		denied()
		return
	}
	flow, err := s.adminStore.ConsumeAdminFlow(r.Context(), state)
	if err != nil {
		if errors.Is(err, account.ErrInvalid) {
			denied()
		} else {
			s.adminAuthError(w, err)
		}
		return
	}
	code := r.URL.Query().Get("code")
	if r.URL.Query().Get("error") != "" || code == "" || len(code) > 4096 {
		denied()
		return
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, s.client)
	tokens, err := s.adminGoogle.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		denied()
		return
	}
	raw, ok := tokens.Extra("id_token").(string)
	if !ok || len(raw) > 16384 {
		denied()
		return
	}
	token, err := s.adminGoogle.verifier.Verify(r.Context(), raw)
	if err != nil || token.Subject == "" || len(token.Subject) > 255 || token.IssuedAt.IsZero() || token.IssuedAt.After(time.Now().Add(30*time.Second)) || subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(flow.Nonce)) != 1 {
		denied()
		return
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
	}
	if token.Claims(&claims) != nil || !claims.Verified {
		denied()
		return
	}
	allowed, err := s.adminEmailAllowed(r.Context(), claims.Email)
	if err != nil {
		s.adminAuthError(w, err)
		return
	}
	if !allowed {
		denied()
		return
	}
	value, err := s.adminStore.CreateAdminSession(r.Context(), account.AdminIdentity{Subject: token.Subject, Email: strings.ToLower(claims.Email), Name: claims.Name, UserAgent: r.UserAgent()})
	if err != nil {
		s.adminAuthError(w, err)
		return
	}
	if old, err := r.Cookie(s.adminCookieName(false)); err == nil {
		if err = s.adminStore.DeleteAdminSession(r.Context(), old.Value); err != nil {
			_ = s.adminStore.DeleteAdminSession(r.Context(), value)
			s.adminAuthError(w, err)
			return
		}
	}
	s.adminCookie(w, "", true, -1)
	s.adminCookie(w, value, false, 8*60*60)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminEmailAllowed(ctx context.Context, email string) (bool, error) {
	if s.config.Admin.Google.allows(email) {
		return true, nil
	}
	return s.adminStore.AdminEmailAllowed(ctx, strings.ToLower(email))
}
