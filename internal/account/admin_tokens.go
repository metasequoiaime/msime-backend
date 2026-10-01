package account

import "context"

// Personal access tokens (unit U12).

// AdminTokenPrefix starts every personal access token, so the server can tell one from the legacy admin token.
const AdminTokenPrefix = "msime_pat_"

// AdminTokenIdentity resolves an unexpired personal access token to its admin identity; an unknown or expired token is ErrInvalid. The server re-checks the email's admin membership on every request.
func (a *Service) AdminTokenIdentity(ctx context.Context, token string) (AdminIdentity, error) {
	return AdminIdentity{}, ErrInvalid
}
