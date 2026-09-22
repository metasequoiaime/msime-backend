package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	msimebackend "github.com/metasequoiaime/MSIME-Backend"
	adminweb "github.com/metasequoiaime/MSIME-Backend/admin-web"
	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

type AdminConfig struct {
	Enabled  bool              `json:"enabled"`
	Host     string            `json:"host"`
	TokenEnv string            `json:"token_env"`
	Google   AdminGoogleConfig `json:"google"`
	token    string
}

func (c *AdminConfig) validate(authEnabled bool, clients []Client) error {
	if !c.Enabled {
		return nil
	}
	if c.Host == "" {
		c.Host = "admin.msime.app"
	}
	if c.TokenEnv == "" {
		c.TokenEnv = "MSIME_ADMIN_TOKEN"
	}
	c.token = os.Getenv(c.TokenEnv)
	if !authEnabled {
		return errors.New("admin requires auth.enabled and PostgreSQL")
	}
	if len(c.Host) > 253 || strings.ContainsAny(c.Host, "/:?#@ \\\r\n\t") || !strings.Contains(c.Host, ".") {
		return errors.New("invalid admin host: use a hostname without port")
	}
	if err := c.Google.validate(c.Host); err != nil {
		return err
	}
	if (c.token == "" && c.Google.ClientID == "") || (c.token != "" && (len(c.token) < 32 || strings.ContainsAny(c.token, " \r\n\t"))) {
		return errors.New("admin token requires at least 32 non-whitespace bytes")
	}
	for _, client := range clients {
		if c.token != "" && c.token == os.Getenv(client.TokenEnv) {
			return errors.New("admin token must differ from client tokens")
		}
	}
	return nil
}

var adminAssets = adminweb.Handler()

func (s *Server) serveAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.config.Admin.Enabled {
		return false
	}
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	if !strings.EqualFold(host, s.config.Admin.Host) {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if origin := r.Header.Get("Origin"); origin != "" && origin != "https://"+r.Host && !(r.TLS == nil && origin == "http://"+r.Host) {
		fail(w, 403, "origin_denied")
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		if s.adminAuthRoute(w, r) {
			return true
		}
		actor, email, err := s.adminIdentity(r)
		if err != nil {
			s.adminAuthError(w, err)
			return true
		}
		if !s.adminMutationOrigin(w, r) {
			return true
		}
		if !s.allow(Client{ID: "admin", RequestsPerMinute: 120}, time.Now()) {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "rate_limit_exceeded")
			return true
		}
		if r.URL.Path == "/api/admins" {
			if !s.config.Admin.Google.allows(email) {
				fail(w, 403, "owner_required")
				return true
			}
			s.accounts.AdminMembersHTTP(w, r.WithContext(account.WithAdminActor(ctx, actor)), s.config.Admin.Google.AllowedEmails)
			return true
		}
		if r.URL.Path == "/api/system" {
			if r.Method != "GET" {
				fail(w, 405, "method_not_allowed")
				return true
			}
			respond(w, 200, map[string]any{
				"version": msimebackend.Version(), "server_time": time.Now().UTC(),
				"auth": s.config.Auth.Enabled, "engine": s.config.Engine.Binary != "", "engine_resources": s.config.Engine.Resources != "",
				"services": map[string]bool{"cloud": s.config.Cloud.URL != "", "chat": s.config.Chat.URL != "", "translation": s.config.Translation.URL != "", "transcription": s.config.Transcription.URL != "", "streaming_transcription": s.config.Streaming.URL != ""},
			})
			return true
		}
		s.accounts.AdminHTTP(w, r.WithContext(account.WithAdminActor(ctx, actor)))
		return true
	}
	if !adminweb.IsPath(r.URL.Path) {
		http.NotFound(w, r)
		return true
	}
	adminAssets.ServeHTTP(w, r)
	return true
}
