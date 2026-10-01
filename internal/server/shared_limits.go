package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// limitShared counts one request against a per-minute budget for scope and subject. With a database the budget lives in auth_rates, a fixed one-minute window shared by every replica (the subject is stored only as a digest); without one it is this process's token bucket under the ID scope+":"+subject. It returns account.ErrLimited when the budget is spent and any other error when the database could not count, which callers answer with 503 rather than letting the request through.
func (s *Server) limitShared(ctx context.Context, scope, subject string, perMinute int) error {
	if s.limits == nil {
		if !s.allow(Client{ID: scope + ":" + subject, RequestsPerMinute: perMinute}, time.Now()) {
			return account.ErrLimited
		}
		return nil
	}
	return s.limits.RateLimit(ctx, scope, subject, perMinute, time.Minute)
}

// adminLimit applies limitShared to a console request and answers it when it must stop: 429 rate_limit_exceeded with Retry-After 60 once the budget is spent, 503 admin_auth_unavailable (the console's code for a database it cannot reach) with Retry-After 30 when the count failed.
func (s *Server) adminLimit(ctx context.Context, w http.ResponseWriter, scope, subject string, perMinute int) bool {
	err := s.limitShared(ctx, scope, subject, perMinute)
	switch {
	case err == nil:
		return true
	case errors.Is(err, account.ErrLimited):
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limit_exceeded")
	default:
		slog.Error("admin: rate limit unavailable", "scope", scope, "reason", err.Error())
		w.Header().Set("Retry-After", "30")
		fail(w, 503, "admin_auth_unavailable")
	}
	return false
}
