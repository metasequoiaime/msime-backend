package account

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Personal access tokens (unit U12).

// AdminTokenPrefix starts every personal access token, so the server can tell one from the legacy admin token.
const AdminTokenPrefix = "msime_pat_"

// adminTokenLength is the length of a token: the prefix and the 64 hex characters of randomToken.
const adminTokenLength = len(AdminTokenPrefix) + 64

// adminTokenLifetime is how long a regenerated token stays valid; it matches the admin_tokens.expires_at default.
const adminTokenLifetime = 30 * 24 * time.Hour

// AdminTokenIdentity resolves an unexpired personal access token to its admin identity; an unknown or expired token is ErrInvalid. The server re-checks the email's admin membership on every request.
func (a *Service) AdminTokenIdentity(ctx context.Context, token string) (AdminIdentity, error) {
	if len(token) != adminTokenLength || token[:len(AdminTokenPrefix)] != AdminTokenPrefix {
		return AdminIdentity{}, ErrInvalid
	}
	identity := AdminIdentity{Subject: "pat"}
	err := a.store.pool.QueryRow(ctx, `SELECT email FROM admin_tokens WHERE hash=$1 AND expires_at>now()`, hash(token)).Scan(&identity.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminIdentity{}, ErrInvalid
	}
	if err != nil {
		return AdminIdentity{}, err
	}
	return identity, nil
}

// adminTokenInfo is what the personal page may show about a token: never the token or its hash.
type adminTokenInfo struct {
	Last4     string    `json:"last4"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// issueAdminToken replaces every token of email with a new one inside tx and returns the full token, which is never stored or shown again.
func issueAdminToken(ctx context.Context, tx pgx.Tx, email string) (string, adminTokenInfo, error) {
	token := AdminTokenPrefix + randomToken()
	info := adminTokenInfo{Last4: token[len(token)-4:]}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_tokens WHERE email=$1`, email); err != nil {
		return "", info, err
	}
	err := tx.QueryRow(ctx, `INSERT INTO admin_tokens(hash,email,last4,expires_at) VALUES($1,$2,$3,now()+make_interval(secs=>$4)) RETURNING created_at,expires_at`, hash(token), email, info.Last4, adminTokenLifetime.Seconds()).Scan(&info.CreatedAt, &info.ExpiresAt)
	return token, info, err
}
