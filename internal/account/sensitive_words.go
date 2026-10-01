package account

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Sensitive word list (unit U4): the matcher community uploads and dictionary submissions consult, the console list and its actions.

// Sensitive word levels: a block hit rejects the text, a review hit lets it through flagged for a moderator.
const (
	SensitiveBlock  = "block"
	SensitiveReview = "review"
)

// SensitiveHit is one configured word a text matched.
type SensitiveHit struct {
	WordID   int64  `json:"word_id"`
	Pattern  string `json:"pattern"`
	Category string `json:"category"`
	Level    string `json:"level"`
}

// SensitiveMatcher reports which sensitive words a text hits. Implementations keep the word list in memory and record hit counts themselves, so callers only act on the levels.
type SensitiveMatcher interface {
	Match(ctx context.Context, text string) ([]SensitiveHit, error)
}

// sensitiveWords is the matcher state kept on the Service (the cached word list and pending hit counts).
type sensitiveWords struct{}

// sensitiveMatcher is the SensitiveMatcher handed out by Sensitive; it reaches the database and the cached state through the Service.
type sensitiveMatcher struct{ a *Service }

// Match reports no hits until the word list is implemented.
func (m sensitiveMatcher) Match(ctx context.Context, text string) ([]SensitiveHit, error) {
	return nil, nil
}

// Sensitive returns the shared matcher.
func (a *Service) Sensitive() SensitiveMatcher { return sensitiveMatcher{a} }

// adminSensitiveWords serves GET /api/sensitive-words.
func (a *Service) adminSensitiveWords(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}

// actionAddSensitiveWord adds value {pattern, category, level}.
func actionAddSensitiveWord(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionSetSensitiveWordLevel sets the level in value on the words ids.
func actionSetSensitiveWordLevel(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}

// actionDeleteSensitiveWord deletes the words ids.
func actionDeleteSensitiveWord(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	return actionResult{}, errNotImplemented
}
