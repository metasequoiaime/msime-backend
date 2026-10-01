package account

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Console notifications (unit U11). Other units only call Notify or NotifyNow.

// Notification kinds.
const (
	NotifyReport     = "report"      // a community report was filed
	NotifyDictPR     = "dict_pr"     // a new dictionary pull request was found
	NotifyCrashSpike = "crash_spike" // a crash group rose more than 20% week over week
	NotifyIncident   = "incident"    // an incident opened
	NotifyRelease    = "release"     // a release changed state
	NotifyIssue      = "issue"       // a new GitHub issue
)

// Notification is one console notification. TargetPage is the console page key (for example "community") and TargetID the item to open there.
type Notification struct {
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	TargetPage string `json:"target_page"`
	TargetID   string `json:"target_id"`
}

// Notify records n inside tx, so it exists exactly when the write that caused it commits. It records nothing until notifications are implemented.
func (a *Service) Notify(ctx context.Context, tx pgx.Tx, n Notification) error {
	return nil
}

// NotifyNow records n on its own, for causes outside the database such as a GitHub poll. It records nothing until notifications are implemented.
func (a *Service) NotifyNow(ctx context.Context, n Notification) error {
	return nil
}

// UnreadNotifications counts the notifications email has not read, for the console shell.
func (a *Service) UnreadNotifications(ctx context.Context, email string) (int, error) {
	return 0, errNotImplemented
}

// adminNotifications serves GET /api/notifications?limit=20.
func (a *Service) adminNotifications(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}

// adminNotificationsRead serves POST /api/notifications/read {ids?|all:true}.
func (a *Service) adminNotificationsRead(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}
