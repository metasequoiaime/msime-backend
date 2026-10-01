package account

import (
	"context"
	"encoding/json"
	"time"
)

// Website dictionary submissions (unit U4 writes them, unit U1 reads them).

// WordSubmission is one website submission appended to a rolling dictionary pull request.
type WordSubmission struct {
	ID       int64           `json:"id"`
	PRNumber int             `json:"pr_number"`
	Kind     string          `json:"kind"` // words, english or translations
	Entries  json.RawMessage `json:"entries"`
	Note     string          `json:"note"`
	Created  time.Time       `json:"created_at"`
}

// RecordWordSubmission stores a submission after GitHub accepted it.
func (a *Service) RecordWordSubmission(ctx context.Context, s WordSubmission) error {
	return errNotImplemented
}

// WordSubmissions lists the submissions recorded for the given pull requests, oldest first.
func (a *Service) WordSubmissions(ctx context.Context, prNumbers []int) ([]WordSubmission, error) {
	return nil, errNotImplemented
}
