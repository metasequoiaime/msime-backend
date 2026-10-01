package account

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Sensitive word list (unit U4): the matcher community uploads and dictionary submissions consult, the console list and its actions.

// Sensitive word levels: a block hit rejects the text, a review hit lets it through flagged for a moderator.
const (
	SensitiveBlock  = "block"
	SensitiveReview = "review"
)

// sensitiveRefresh is how long the matcher serves its cached word list, and how long hit counts wait in memory before they are written, so every replica sees console edits within this interval.
const sensitiveRefresh = 30 * time.Second

// sensitiveCategories are the categories admin_sensitive_words accepts.
var sensitiveCategories = []string{"ad", "vulgar", "abuse", "illegal", "custom"}

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

// sensitiveWord is one cached entry: a plain word in its folded form, or a compiled regex.
type sensitiveWord struct {
	hit   SensitiveHit
	plain string
	re    *regexp.Regexp
}

// sensitiveHitKey counts hits per word and UTC day.
type sensitiveHitKey struct {
	word int64
	day  string
}

// sensitiveWords is the matcher state kept on the Service: the cached word list and the hit counts not yet written. The zero value is ready to use; the first Match loads the list.
type sensitiveWords struct {
	// reload serialises list loads and hit flushes, so concurrent Match calls never query the database twice for the same refresh.
	reload sync.Mutex
	// mu guards the fields below; it is never held across a database call.
	mu      sync.Mutex
	words   []sensitiveWord
	loaded  time.Time
	pending map[sensitiveHitKey]int64
	flushed time.Time
}

// invalidate makes the next Match reload the list, so a console edit applies at once on this replica; other replicas pick it up within sensitiveRefresh.
func (s *sensitiveWords) invalidate() {
	s.mu.Lock()
	s.loaded = time.Time{}
	s.mu.Unlock()
}

// list returns the cached words, reloading them when the cache is older than sensitiveRefresh. A failed reload keeps serving the previous list and is only an error before the first successful load.
func (s *sensitiveWords) list(ctx context.Context, store *Store, now time.Time) ([]sensitiveWord, error) {
	fresh := func() ([]sensitiveWord, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.words, !s.loaded.IsZero() && now.Sub(s.loaded) < sensitiveRefresh
	}
	if words, ok := fresh(); ok {
		return words, nil
	}
	s.reload.Lock()
	defer s.reload.Unlock()
	words, ok := fresh()
	if ok {
		return words, nil
	}
	loaded, err := loadSensitiveWords(ctx, store)
	if err != nil {
		if words != nil {
			slog.Warn("sensitive words: reload failed, serving the cached list", "reason", err.Error())
			return words, nil
		}
		return nil, err
	}
	s.mu.Lock()
	s.words, s.loaded = loaded, now
	s.mu.Unlock()
	return loaded, nil
}

// loadSensitiveWords reads and compiles the whole list. A stored regex that no longer compiles (it was validated on insert) is skipped with a warning rather than disabling the list.
func loadSensitiveWords(ctx context.Context, store *Store) ([]sensitiveWord, error) {
	rows, err := store.pool.Query(ctx, `SELECT id,pattern,is_regex,category,level FROM admin_sensitive_words ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	words := []sensitiveWord{}
	for rows.Next() {
		var w sensitiveWord
		var isRegex bool
		if err = rows.Scan(&w.hit.WordID, &w.hit.Pattern, &isRegex, &w.hit.Category, &w.hit.Level); err != nil {
			return nil, err
		}
		if isRegex {
			if w.re, err = compileSensitiveRegex(w.hit.Pattern); err != nil {
				slog.Warn("sensitive words: stored regex skipped", "id", w.hit.WordID, "reason", err.Error())
				continue
			}
		} else if w.plain = foldSensitive(w.hit.Pattern, true); w.plain == "" {
			continue
		}
		words = append(words, w)
	}
	return words, rows.Err()
}

// compileSensitiveRegex compiles a pattern as a case-insensitive RE2 expression. A regex that matches the empty string would flag every text, so it is rejected.
func compileSensitiveRegex(pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, err
	}
	if re.MatchString("") {
		return nil, errors.New("pattern matches empty text")
	}
	return re, nil
}

// foldSensitive normalises text for matching: full-width ASCII becomes half-width, letters lowercase, and with dropSpace every white space character is removed so spacing cannot split a plain word.
func foldSensitive(text string, dropSpace bool) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '　':
			r = ' '
		case r >= '！' && r <= '～':
			r -= 0xFEE0
		}
		if dropSpace && unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// record adds one hit per matched word and writes the counts once the oldest unwritten ones are sensitiveRefresh old. A failed write keeps the counts for the next attempt.
func (s *sensitiveWords) record(ctx context.Context, store *Store, hits []SensitiveHit, now time.Time) {
	day := now.UTC().Format(time.DateOnly)
	s.mu.Lock()
	if s.pending == nil {
		s.pending = map[sensitiveHitKey]int64{}
	}
	if s.flushed.IsZero() {
		s.flushed = now
	}
	for _, h := range hits {
		s.pending[sensitiveHitKey{h.WordID, day}]++
	}
	due := now.Sub(s.flushed) >= sensitiveRefresh
	s.mu.Unlock()
	if due {
		if err := s.flush(ctx, store, now); err != nil {
			slog.Warn("sensitive words: hit counts not written yet", "reason", err.Error())
		}
	}
}

// flush writes the pending hit counts in one statement. Counts for words deleted meanwhile are dropped; on failure the counts are merged back.
func (s *sensitiveWords) flush(ctx context.Context, store *Store, now time.Time) error {
	s.reload.Lock()
	defer s.reload.Unlock()
	s.mu.Lock()
	pending := s.pending
	s.pending, s.flushed = nil, now
	s.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	ids, days, counts := make([]int64, 0, len(pending)), make([]string, 0, len(pending)), make([]int64, 0, len(pending))
	for key, n := range pending {
		ids, days, counts = append(ids, key.word), append(days, key.day), append(counts, n)
	}
	_, err := store.pool.Exec(ctx, `INSERT INTO admin_sensitive_hits(word_id,day,count)
SELECT h.word_id,h.day::date,h.count FROM unnest($1::bigint[],$2::text[],$3::bigint[]) AS h(word_id,day,count)
JOIN admin_sensitive_words w ON w.id=h.word_id
ON CONFLICT (word_id,day) DO UPDATE SET count=admin_sensitive_hits.count+EXCLUDED.count`, ids, days, counts)
	if err != nil {
		s.mu.Lock()
		if s.pending == nil {
			s.pending = map[sensitiveHitKey]int64{}
		}
		for key, n := range pending {
			s.pending[key] += n
		}
		s.mu.Unlock()
	}
	return err
}

// sensitiveMatcher is the SensitiveMatcher handed out by Sensitive; it reaches the database and the cached state through the Service.
type sensitiveMatcher struct{ a *Service }

// Match reports every word text hits, block hits first, at most once per word, and counts each hit. Plain words match a case-insensitive substring of the text with white space removed; regexes match case-insensitively against the text with white space kept. Full-width ASCII is folded to half-width for both.
func (m sensitiveMatcher) Match(ctx context.Context, text string) ([]SensitiveHit, error) {
	now := time.Now()
	words, err := m.a.sensitive.list(ctx, m.a.store, now)
	if err != nil {
		return nil, err
	}
	if len(words) == 0 || text == "" {
		return nil, nil
	}
	compact, spaced := foldSensitive(text, true), foldSensitive(text, false)
	var hits []SensitiveHit
	for _, w := range words {
		if (w.re != nil && w.re.MatchString(spaced)) || (w.re == nil && strings.Contains(compact, w.plain)) {
			hits = append(hits, w.hit)
		}
	}
	if len(hits) == 0 {
		return nil, nil
	}
	slices.SortStableFunc(hits, func(x, y SensitiveHit) int {
		return sensitiveLevelRank(x.Level) - sensitiveLevelRank(y.Level)
	})
	m.a.sensitive.record(ctx, m.a.store, hits, now)
	return hits, nil
}

func sensitiveLevelRank(level string) int {
	if level == SensitiveBlock {
		return 0
	}
	return 1
}

// Sensitive returns the shared matcher.
func (a *Service) Sensitive() SensitiveMatcher { return sensitiveMatcher{a} }

// adminSensitiveWords serves GET /api/sensitive-words: the whole list, newest first, with each word's hits over the last seven UTC days (today included) and the largest of those counts for the console's bars.
func (a *Service) adminSensitiveWords(w http.ResponseWriter, r *http.Request, _ string) {
	ctx := r.Context()
	now := time.Now()
	// Pending counts from this replica are written first so the console shows them.
	if err := a.sensitive.flush(ctx, a.store, now); err != nil {
		slog.Warn("sensitive words: hit counts not written yet", "reason", err.Error())
	}
	var result json.RawMessage
	err := a.store.pool.QueryRow(ctx, `WITH hits AS (
 SELECT word_id,sum(count)::bigint AS n FROM admin_sensitive_hits WHERE day>$1::date-7 GROUP BY word_id
), items AS (
 SELECT w.id,w.pattern,w.is_regex,w.category,w.level,w.created_by,w.created_at,COALESCE(h.n,0) AS hits_7d
 FROM admin_sensitive_words w LEFT JOIN hits h ON h.word_id=w.id
)
SELECT json_build_object('items',COALESCE((SELECT json_agg(i ORDER BY created_at DESC,id DESC) FROM items i),'[]'::json),
 'max_hits',COALESCE((SELECT max(hits_7d) FROM items),0))`, now.UTC().Format(time.DateOnly)).Scan(&result)
	if err != nil {
		a.error(w, err)
		return
	}
	write(w, 200, result)
}

// sensitiveWordInput is the value of add_sensitive_word. IsRegex is optional: when it is omitted, a pattern written as /.../ is a regex and the slashes are dropped.
type sensitiveWordInput struct {
	Pattern  string `json:"pattern"`
	IsRegex  *bool  `json:"is_regex"`
	Category string `json:"category"`
	Level    string `json:"level"`
}

// decodeSensitiveValue decodes an action value strictly: a missing value, unknown fields and trailing data are 400 invalid_value.
func decodeSensitiveValue(raw json.RawMessage, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || d.Decode(out) != nil || d.More() {
		return actionFail(400, "invalid_value")
	}
	return nil
}

// sensitiveCreator is what created_by records: the admin's email, or the actor for the legacy token.
func sensitiveCreator(ctx context.Context) string {
	if access, ok := AdminAccessFrom(ctx); ok && access.Email != "" {
		return access.Email
	}
	return adminActor(ctx)
}

// actionAddSensitiveWord adds value {pattern, is_regex?, category, level}.
func actionAddSensitiveWord(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	var in sensitiveWordInput
	if err := decodeSensitiveValue(v.Value, &in); err != nil {
		return actionResult{}, err
	}
	pattern := strings.TrimSpace(in.Pattern)
	isRegex := false
	if in.IsRegex != nil {
		isRegex = *in.IsRegex
	} else if len(pattern) > 2 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
		isRegex, pattern = true, pattern[1:len(pattern)-1]
	}
	if !utf8.ValidString(pattern) || !resourceText(pattern, 1, 200, false) {
		return actionResult{}, actionFail(400, "invalid_pattern")
	}
	if isRegex {
		if _, err := compileSensitiveRegex(pattern); err != nil {
			return actionResult{}, actionFail(400, "invalid_pattern")
		}
	} else if foldSensitive(pattern, true) == "" {
		return actionResult{}, actionFail(400, "invalid_pattern")
	}
	if !slices.Contains(sensitiveCategories, in.Category) {
		return actionResult{}, actionFail(400, "invalid_category")
	}
	if in.Level != SensitiveBlock && in.Level != SensitiveReview {
		return actionResult{}, actionFail(400, "invalid_level")
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO admin_sensitive_words(pattern,is_regex,category,level,created_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT (pattern) DO NOTHING RETURNING id`, pattern, isRegex, in.Category, in.Level, sensitiveCreator(ctx)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return actionResult{}, actionFail(409, "exists")
	}
	if err != nil {
		return actionResult{}, err
	}
	a.sensitive.invalidate()
	return actionResult{
		Affected: 1,
		Target:   pattern,
		Detail:   map[string]any{"id": id, "pattern": pattern, "is_regex": isRegex, "category": in.Category, "level": in.Level},
		Extra:    map[string]any{"id": id},
	}, nil
}

// sensitiveWordIDs returns the action's word ids (ids, or the single id when ids is empty) as positive integers.
func sensitiveWordIDs(v actionRequest) ([]int64, error) {
	raw := v.IDs
	if len(raw) == 0 && v.ID != "" {
		raw = []string{v.ID}
	}
	if len(raw) == 0 {
		return nil, actionFail(400, "invalid_ids")
	}
	ids := make([]int64, 0, len(raw))
	for _, s := range raw {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			return nil, actionFail(400, "invalid_ids")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// sensitiveRowsResult turns the patterns a batch changed into the action result; a batch that matched nothing is 404 not_found. The audit target is the pattern for one word, or the count for several, and the detail lists every pattern.
func sensitiveRowsResult(rows pgx.Rows, detail map[string]any) (actionResult, error) {
	patterns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return actionResult{}, err
	}
	if len(patterns) == 0 {
		return actionResult{}, actionFail(404, "not_found")
	}
	slices.Sort(patterns)
	detail["count"], detail["patterns"] = len(patterns), patterns
	target := patterns[0]
	if len(patterns) > 1 {
		target = strconv.Itoa(len(patterns)) + " words"
	}
	return actionResult{Affected: int64(len(patterns)), Target: target, Detail: detail}, nil
}

// actionSetSensitiveWordLevel sets the level in value ("block" or "review") on the words ids.
func actionSetSensitiveWordLevel(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	ids, err := sensitiveWordIDs(v)
	if err != nil {
		return actionResult{}, err
	}
	var level string
	if err = decodeSensitiveValue(v.Value, &level); err != nil || (level != SensitiveBlock && level != SensitiveReview) {
		return actionResult{}, actionFail(400, "invalid_level")
	}
	rows, err := tx.Query(ctx, `UPDATE admin_sensitive_words SET level=$2 WHERE id=ANY($1) RETURNING pattern`, ids, level)
	if err != nil {
		return actionResult{}, err
	}
	result, err := sensitiveRowsResult(rows, map[string]any{"level": level})
	if err == nil {
		a.sensitive.invalidate()
	}
	return result, err
}

// actionDeleteSensitiveWord deletes the words ids; their hit counts go with them.
func actionDeleteSensitiveWord(a *Service, ctx context.Context, tx pgx.Tx, v actionRequest) (actionResult, error) {
	ids, err := sensitiveWordIDs(v)
	if err != nil {
		return actionResult{}, err
	}
	rows, err := tx.Query(ctx, `DELETE FROM admin_sensitive_words WHERE id=ANY($1) RETURNING pattern`, ids)
	if err != nil {
		return actionResult{}, err
	}
	result, err := sensitiveRowsResult(rows, map[string]any{})
	if err == nil {
		a.sensitive.invalidate()
	}
	return result, err
}
