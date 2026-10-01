package account

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Users page (unit U5): the user list, session revocation, bans and user statistics.

// usersList serves GET /api/users.
var usersList = adminList{
	query: `SELECT u.id,u.display_name,u.created_at,(SELECT count(*) FROM auth_sessions s WHERE s.user_id=u.id AND NOT revoked AND expires_at>now()) AS sessions FROM auth_users u`,
}

// actionRevokeSession revokes one session (id) of one user (user_id).
func actionRevokeSession(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	if err := requireActionID(v); err != nil {
		return actionResult{}, err
	}
	if !resourceText(v.UserID, 1, 128, false) {
		return actionResult{}, actionFail(400, "invalid_user_id")
	}
	tag, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked=true WHERE id=$1 AND user_id=$2`, v.ID, v.UserID)
	if err != nil {
		return actionResult{}, err
	}
	if tag.RowsAffected() == 0 {
		return actionResult{}, actionFail(404, "not_found")
	}
	return actionResult{Affected: tag.RowsAffected()}, nil
}

// actionRevokeSessions revokes every session of the user id.
func actionRevokeSessions(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	if err := requireActionID(v); err != nil {
		return actionResult{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked=true WHERE user_id=$1`, v.ID)
	if err != nil {
		return actionResult{}, err
	}
	if tag.RowsAffected() == 0 {
		// Revoking an existing user's already-empty session set is idempotent, but a missing user must not create a misleading audit record.
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_users WHERE id=$1)`, v.ID).Scan(&exists); err != nil {
			return actionResult{}, err
		}
		if !exists {
			return actionResult{}, actionFail(404, "not_found")
		}
	}
	return actionResult{Affected: tag.RowsAffected()}, nil
}

// actionBanUser bans the user id with reason: in one transaction it sets auth_users.banned_*, revokes every session and removes the user's community content with moderation_reason 'owner_banned'.
func actionBanUser(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionUnbanUser lifts the ban on the user id and restores only the content the ban removed.
func actionUnbanUser(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// adminUserStats serves GET /api/users/stats.
func (a *Service) adminUserStats(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}
