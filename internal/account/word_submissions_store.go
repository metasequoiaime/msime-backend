package account

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"unicode/utf8"
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

// wordSubmissionKinds are the kinds word_submissions accepts.
var wordSubmissionKinds = []string{"words", "english", "translations"}

// RecordWordSubmission stores a submission after GitHub accepted it. Entries must be a non-empty JSON array; ID and Created are assigned by the database.
func (a *Service) RecordWordSubmission(ctx context.Context, s WordSubmission) error {
	var entries []json.RawMessage
	switch {
	case s.PRNumber <= 0:
		return errors.New("word submission: invalid pull request number")
	case !slices.Contains(wordSubmissionKinds, s.Kind):
		return errors.New("word submission: invalid kind")
	case json.Unmarshal(s.Entries, &entries) != nil || len(entries) == 0:
		return errors.New("word submission: entries must be a non-empty JSON array")
	case !utf8.ValidString(s.Note) || utf8.RuneCountInString(s.Note) > 1000:
		return errors.New("word submission: invalid note")
	}
	_, err := a.store.pool.Exec(ctx, `INSERT INTO word_submissions(pr_number,kind,entries,note) VALUES($1,$2,$3,$4)`, s.PRNumber, s.Kind, string(s.Entries), s.Note)
	return err
}

// WordSubmissions lists the submissions recorded for the given pull requests, oldest first. No numbers is an empty list.
func (a *Service) WordSubmissions(ctx context.Context, prNumbers []int) ([]WordSubmission, error) {
	out := []WordSubmission{}
	if len(prNumbers) == 0 {
		return out, nil
	}
	numbers := make([]int32, 0, len(prNumbers))
	for _, n := range prNumbers {
		if n > 0 && n <= 1<<31-1 {
			numbers = append(numbers, int32(n))
		}
	}
	rows, err := a.store.pool.Query(ctx, `SELECT id,pr_number,kind,entries,note,created_at FROM word_submissions WHERE pr_number=ANY($1) ORDER BY created_at,id`, numbers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s WordSubmission
		var entries []byte
		if err = rows.Scan(&s.ID, &s.PRNumber, &s.Kind, &entries, &s.Note, &s.Created); err != nil {
			return nil, err
		}
		s.Entries = json.RawMessage(entries)
		out = append(out, s)
	}
	return out, rows.Err()
}
