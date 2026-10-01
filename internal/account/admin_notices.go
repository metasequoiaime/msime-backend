package account

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Notices (unit U8): drafts and publishing in the console, the public feed, and the broadcaster that sends a published notice to external channels.

// Notice is one announcement as broadcasters see it.
type Notice struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Targets  []string `json:"targets"`
	Channels []string `json:"channels"`
}

// NoticeBroadcaster delivers a notice being published to the external channels it lists, such as Telegram. A failure fails the publication.
type NoticeBroadcaster interface {
	BroadcastNotice(ctx context.Context, n Notice) error
}

// ConfigureNoticeBroadcaster is called once during server construction, before serving requests; nil means no external channel is configured.
func (a *Service) ConfigureNoticeBroadcaster(b NoticeBroadcaster) {
	if a != nil {
		a.broadcaster = b
	}
}

// adminNotices serves GET /api/notices.
func (a *Service) adminNotices(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}

// PublicNotices serves GET /v1/notices?platform=&channel=, the unauthenticated feed of live notices that apps and the website show.
func (a *Service) PublicNotices(w http.ResponseWriter, r *http.Request) {
	if a == nil {
		writeError(w, 503, "user_auth_disabled")
		return
	}
	notImplemented(w)
}

// actionSaveNoticeDraft creates (no id) or updates (id) a draft from value.
func actionSaveNoticeDraft(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionPublishNotice publishes the draft id, or a new notice from value, and broadcasts it.
func actionPublishNotice(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionArchiveNotice takes the notice id off the public feed.
func actionArchiveNotice(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}
