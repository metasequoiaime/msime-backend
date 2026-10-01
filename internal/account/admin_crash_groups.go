package account

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Crash page (unit U9): the crash event list, crash groups, their signatures and status.

// crashesList serves GET /api/crashes.
var crashesList = adminList{
	query: `SELECT id,platform,version,message,stack,resolved,created_at FROM admin_events WHERE kind='crash'`,
	filters: []listFilter{
		{param: "platform", field: "platform", max: 32},
		{param: "version", field: "version", max: 64},
		{param: "status", field: "resolved", max: 8, values: map[string]string{"open": "false", "resolved": "true"}},
	},
}

// setCrashResolved marks the crash event id resolved or open.
func setCrashResolved(resolved bool) adminActionFunc {
	return func(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
		if err := requireActionID(v); err != nil {
			return actionResult{}, err
		}
		tag, err := tx.Exec(ctx, `UPDATE admin_events SET resolved=$2 WHERE id=$1 AND kind='crash'`, v.ID, resolved)
		if err != nil {
			return actionResult{}, err
		}
		if tag.RowsAffected() == 0 {
			return actionResult{}, actionFail(404, "not_found")
		}
		return actionResult{Affected: tag.RowsAffected()}, nil
	}
}

var (
	actionResolveCrash = setCrashResolved(true)
	actionReopenCrash  = setCrashResolved(false)
)

// actionCrashGroupStatus sets the status in value (open, known or fixed) on the crash group id.
func actionCrashGroupStatus(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// adminCrashGroups serves GET /api/crash-groups?platform=.
func (a *Service) adminCrashGroups(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}

// adminCrashGroup serves GET /api/crash-groups/{signature}.
func (a *Service) adminCrashGroup(w http.ResponseWriter, r *http.Request, signature string) {
	notImplemented(w)
}

// crashSignature groups a crash: the first 16 hex digits of sha256 over the normalized message and the first non-system stack frame. Empty means the crash is not grouped.
func crashSignature(message, stack string) string {
	return ""
}

// upsertCrashGroup records a crash with a non-empty signature in its group, inside the telemetry insert's transaction.
func (a *Service) upsertCrashGroup(ctx context.Context, tx pgx.Tx, signature, platform, version, title string) error {
	return nil
}

// backfillCrashSignatures computes the signature of stored crashes that have none, with crashSignature, and creates their groups. AdminReady runs it at every startup, so it must be cheap when nothing is left to fill.
func (a *Service) backfillCrashSignatures(ctx context.Context) error {
	return nil
}
