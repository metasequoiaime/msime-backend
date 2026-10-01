package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/metasequoiaime/MSIME-Backend/internal/engine"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Cancel one database statement at its driver boundary. Earlier statements run
// against PostgreSQL, so this verifies rollback after partial transaction work.
// The cancellation belongs only to that statement, leaving rollback possible.
type statementCancellation struct {
	mu         sync.Mutex
	at, seen   int
	statements []string
}

func (f *statementCancellation) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen++
	f.statements = append(f.statements, data.SQL)
	if f.at > 0 && f.seen == f.at {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	return ctx
}
func (*statementCancellation) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestUserDataTransactionsRollbackAtEveryDatabaseStatement(t *testing.T) {
	testUserDataTransactions(t, false)
}
func TestNativeUserDataTransactionsRollbackAtEveryDatabaseStatement(t *testing.T) {
	if os.Getenv("MSIME_ENGINE_TEST_BINARY") == "" || os.Getenv("MSIME_ENGINE_TEST_RESOURCES") == "" {
		t.Skip("需要真实 Engine 与发布词库")
	}
	testUserDataTransactions(t, true)
}
func testUserDataTransactions(t *testing.T, native bool) {
	cfg := engine.Config{Binary: os.Getenv("MSIME_ENGINE_TEST_BINARY"), Resources: os.Getenv("MSIME_ENGINE_TEST_RESOURCES")}

	db := testStore(t)
	if _, err := db.pool.Exec(t.Context(), `TRUNCATE admin_members,admin_sessions,admin_audit,admin_login_flows`); err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		user           Tokens
		entry          DictionaryEntry
		clipboard      string
		skin, resource string
		revision       int64
		// foreignCandidate is another account's candidate skin the fixture user has downloaded, so rating it succeeds.
		foreignCandidate string
	}
	seed := func(t *testing.T) fixture {
		t.Helper()
		user := complete(t, db, Identity{"email", randomToken() + "@example.test"})
		uid := user.User.ID
		change, err := db.EditDictionary(t.Context(), uid, "quick", "", 0, &DictionaryEntry{Code: "hi", Word: "hello", Weight: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SetClipboardEnabled(t.Context(), uid, true); err != nil {
			t.Fatal(err)
		}
		item, err := db.AddClipboard(t.Context(), uid, "original")
		if err != nil {
			t.Fatal(err)
		}
		id := randomToken()[:8] + "-1234-1234-1234-" + randomToken()[:12]
		if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_skins(id,owner_id,name,description,design) VALUES($1,$2,'skin','',$3)`, id, uid, communityFixture); err != nil {
			t.Fatal(err)
		}
		if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_resources(id,owner_id,kind,name,description,content) VALUES($1,$2,'reply','reply','','{"prompt":"hello"}')`, id, uid); err != nil {
			t.Fatal(err)
		}
		insertCandidateSkin(t, db, id, uid, "candidate")
		author := complete(t, db, Identity{"email", randomToken() + "@example.test"})
		foreign := randomToken()[:8] + "-1234-1234-1234-" + randomToken()[:12]
		insertCandidateSkin(t, db, foreign, author.User.ID, "foreign candidate")
		if _, err := db.pool.Exec(t.Context(), `INSERT INTO community_candidate_skin_downloads(skin_id,user_id) VALUES($1,$2)`, foreign, uid); err != nil {
			t.Fatal(err)
		}
		revision := int64(1)
		if native {
			for i, word := range []string{"测试首词", "测试次词"} {
				if _, err := db.EditDictionary(t.Context(), uid, "wubi", "", 0, &DictionaryEntry{Code: "abcd", Word: word, Weight: int64(300 - i*100)}); err != nil {
					t.Fatal(err)
				}
				revision++
			}
			if _, err := db.pool.Exec(t.Context(), `UPDATE community_resources SET kind='dictionary',content='{"entries":[{"kind":"quick","code":"new","word":"new phrase","weight":1}]}' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
		return fixture{user, *change.Replacement, item.ID, id, id, revision, foreign}
	}
	snapshot := func(t *testing.T, uid string) string {
		t.Helper()
		var all strings.Builder
		for _, table := range []string{"auth_users", "user_dictionary_state", "user_dictionary_entries", "user_dictionary_changes", "user_dictionary_overlay", "user_candidate_positions", "user_candidate_selections", "user_clipboard_settings", "user_clipboard", "user_preferences", "auth_sessions", "community_skins", "community_resources", "community_skin_downloads", "community_resource_saves", "community_candidate_skins", "community_candidate_skin_files", "community_candidate_skin_downloads", "community_candidate_skin_ratings", "admin_members", "admin_sessions", "admin_audit"} {
			var raw string
			column := "user_id"
			if table == "auth_users" {
				column = "id"
			}
			if table == "community_skins" || table == "community_resources" || table == "community_candidate_skins" {
				column = "owner_id"
			}
			where := ` WHERE ` + column + `=$1`
			if table == "community_candidate_skin_files" {
				where = ` WHERE skin_id IN (SELECT id FROM community_candidate_skins WHERE owner_id=$1)`
			}
			args := []any{uid}
			if strings.HasPrefix(table, "admin_") {
				where = ""
				args = nil
			}
			if err := db.pool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(row ORDER BY row::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(x) AS row FROM `+table+` x`+where+`) s`, args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			all.WriteString(table + raw)
		}
		return all.String()
	}
	operations := map[string]func(*Store, fixture) error{
		"dictionary insert": func(s *Store, f fixture) error {
			_, e := s.EditDictionary(t.Context(), f.user.User.ID, "quick", "", 0, &DictionaryEntry{Code: "bye", Word: "goodbye", Weight: 2})
			return e
		},
		"dictionary update": func(s *Store, f fixture) error {
			next := f.entry
			next.Word = "changed"
			_, e := s.EditDictionary(t.Context(), f.user.User.ID, "quick", f.entry.ID, f.entry.Revision, &next)
			return e
		},
		"dictionary delete": func(s *Store, f fixture) error {
			_, e := s.EditDictionary(t.Context(), f.user.User.ID, "quick", f.entry.ID, f.entry.Revision, nil)
			return e
		},
		"dictionary import": func(s *Store, f fixture) error {
			_, e := s.ImportDictionary(t.Context(), f.user.User.ID, "quick", []DictionaryEntry{{Code: "a", Word: "one", Weight: 1}, {Code: "b", Word: "two", Weight: 1}})
			return e
		},
		"position insert": func(s *Store, f fixture) error {
			_, e := s.SetCandidatePosition(t.Context(), f.user.User.ID, 1, CandidatePosition{Context: "hi", Code: "hi", Word: "hello", Position: 1})
			return e
		},
		"position delete": func(s *Store, f fixture) error {
			_, e := s.SetCandidatePosition(t.Context(), f.user.User.ID, 1, CandidatePosition{Context: "hi", Code: "hi", Word: "hello"})
			return e
		},
		"clipboard add":     func(s *Store, f fixture) error { _, e := s.AddClipboard(t.Context(), f.user.User.ID, "new"); return e },
		"clipboard delete":  func(s *Store, f fixture) error { return s.DeleteClipboard(t.Context(), f.user.User.ID, f.clipboard) },
		"clipboard disable": func(s *Store, f fixture) error { return s.SetClipboardEnabled(t.Context(), f.user.User.ID, false) },
		"preferences": func(s *Store, f fixture) error {
			_, e := s.PutPreferences(t.Context(), f.user.User.ID, 0, map[string]json.RawMessage{"keyboard.haptics": json.RawMessage(`true`)})
			return e
		},
		"refresh": func(s *Store, f fixture) error { _, e := s.Refresh(t.Context(), f.user.RefreshToken); return e },
	}

	candidateManifest, candidateFiles := candidateFixture(t, "shared")
	// Exercise handlers directly so the IP rate-limit write does not mask faults
	// in the endpoint's own transaction. Authentication still uses PostgreSQL.
	for name, spec := range map[string]struct {
		method  string
		handler func(*Service, http.ResponseWriter, *http.Request)
		body    func(fixture) string
		status  int
	}{
		"publish skin": {"POST", (*Service).communityPublish, func(f fixture) string {
			return `{"id":"` + randomToken()[:8] + `-1234-1234-1234-` + randomToken()[:12] + `","name":"new","design":` + communityFixture + `}`
		}, 201},
		"publish resource": {"POST", (*Service).resourcePublish, func(f fixture) string {
			return `{"id":"` + randomToken()[:8] + `-1234-1234-1234-` + randomToken()[:12] + `","kind":"reply","name":"new","content":{"prompt":"new"},"revision":0}`
		}, 201},
		"update resource": {"POST", (*Service).resourcePublish, func(f fixture) string {
			return `{"id":"` + f.resource + `","kind":"reply","name":"changed","content":{"prompt":"changed"},"revision":1}`
		}, 200},
		"publish candidate skin": {"POST", (*Service).communityCandidatePublish, func(f fixture) string {
			return candidatePublishBody(t, randomToken()[:8]+"-1234-1234-1234-"+randomToken()[:12], "new", candidateManifest, candidateFiles)
		}, 201},
		"download candidate skin": {"POST", (*Service).communityCandidateDownload, func(f fixture) string { return `` }, 200},
		"rate candidate skin":     {"PUT", (*Service).communityCandidateRate, func(f fixture) string { return `{"stars":4}` }, 200},
		"delete candidate skin":   {"DELETE", (*Service).communityCandidateDelete, func(f fixture) string { return `` }, 200},
		"download skin":           {"POST", (*Service).communityDownload, func(f fixture) string { return `{}` }, 200},
		"delete skin":             {"DELETE", (*Service).communityDelete, func(f fixture) string { return `` }, 200},
		"delete resource":         {"DELETE", (*Service).resourceDelete, func(f fixture) string { return `` }, 200},
		"save resource":           {"PUT", (*Service).resourceSave, func(f fixture) string { return `{"saved":true}` }, 200},
		"unsave resource":         {"PUT", (*Service).resourceSave, func(f fixture) string { return `{"saved":false}` }, 200},
		"profile read":            {"GET", (*Service).me, func(f fixture) string { return `` }, 200},
		"preferences read":        {"GET", (*Service).preferences, func(f fixture) string { return `` }, 200},
		"positions read":          {"GET", (*Service).candidatePositions, func(f fixture) string { return `` }, 200},
		"dictionary read":         {"GET", (*Service).dictionary, func(f fixture) string { return `` }, 200},
		"changes read":            {"GET", (*Service).dictionaryChanges, func(f fixture) string { return `` }, 200},
		"clipboard read":          {"GET", (*Service).clipboard, func(f fixture) string { return `` }, 200},
		"profile update":          {"PATCH", (*Service).update, func(f fixture) string { return `{"display_name":"changed"}` }, 204},
		"logout":                  {"POST", (*Service).logout, func(f fixture) string { return `{}` }, 204},
		"positions update":        {"PUT", (*Service).candidatePositions, func(f fixture) string { return `{"revision":1,"context":"hi","code":"hi","word":"hello","position":1}` }, 200},
		"clipboard append":        {"POST", (*Service).clipboard, func(f fixture) string { return `{"text":"new text"}` }, 200},
		"clipboard clear":         {"DELETE", (*Service).clipboard, func(f fixture) string { return `` }, 204},
	} {
		operations[name] = func(s *Store, f fixture) error {
			r := jsonRequest(spec.method, "/test", spec.body(f), f.user.AccessToken)
			r.SetPathValue("id", f.resource)
			r.SetPathValue("kind", "quick")
			if name == "clipboard clear" {
				r.SetPathValue("id", "")
			}
			if name == "rate candidate skin" {
				r.SetPathValue("id", f.foreignCandidate)
			}
			w := httptest.NewRecorder()
			spec.handler(&Service{store: s}, w, r)
			if w.Code != spec.status {
				return fmt.Errorf("HTTP %d: %s", w.Code, w.Body.String())
			}
			return nil
		}
	}

	operations["admin revoke sessions"] = func(s *Store, f fixture) error {
		w := httptest.NewRecorder()
		(&Service{store: s}).adminAction(w, adminJSONRequest("POST", "/api/actions", `{"action":"revoke_sessions","id":"`+f.user.User.ID+`"}`))
		if w.Code != 200 {
			return fmt.Errorf("HTTP %d: %s", w.Code, w.Body.String())
		}
		return nil
	}
	operations["admin add member"] = func(s *Store, f fixture) error {
		w := httptest.NewRecorder()
		(&Service{store: s}).AdminMembersHTTP(w, jsonRequest("POST", "/api/admins", `{"action":"add","email":"`+f.user.User.ID+`@example.test"}`, ""), nil)
		if w.Code != 200 {
			return fmt.Errorf("HTTP %d: %s", w.Code, w.Body.String())
		}
		return nil
	}
	if native {
		operations = map[string]func(*Store, fixture) error{
			"managed edit": func(s *Store, f fixture) error {
				next := f.entry
				next.Word = "changed"
				_, err := s.EditManagedDictionary(t.Context(), f.user.User.ID, f.revision, f.entry, &next, cfg)
				return err
			},
			"managed delete": func(s *Store, f fixture) error {
				_, err := s.EditManagedDictionary(t.Context(), f.user.User.ID, f.revision, f.entry, nil, cfg)
				return err
			},
			"rank": func(s *Store, f fixture) error {
				_, err := s.RankCandidate(t.Context(), f.user.User.ID, f.revision, cfg, map[string]any{"operation": "candidates", "scheme": "wubi", "text": "abcd", "limit": 20}, RankingAction{Code: "abcd", Word: "测试次词", ForceTop: true, Mode: "pin", LinearStep: 1, TriggerCount: 1})
				return err
			},
			"restore": func(s *Store, f fixture) error {
				w := httptest.NewRecorder()
				(&Service{store: db}).dictionarySnapshot(w, jsonRequest("GET", "/v1/users/me/dictionary/snapshot", "", f.user.AccessToken))
				if w.Code != 200 {
					return fmt.Errorf("export HTTP %d", w.Code)
				}
				raw := w.Body.Bytes()
				_, err := s.RestoreDictionarySnapshot(t.Context(), f.user.User.ID, f.revision, bytes.NewReader(raw), cfg)
				return err
			},
			"apply resource": func(s *Store, f fixture) error {
				r := jsonRequest("POST", "/test", fmt.Sprintf(`{"resource_revision":1,"dictionary_revision":%d}`, f.revision), f.user.AccessToken)
				r.SetPathValue("id", f.resource)
				w := httptest.NewRecorder()
				(&Service{store: s, engine: cfg}).resourceApply(w, r)
				if w.Code != 200 {
					return fmt.Errorf("HTTP %d: %s", w.Code, w.Body.String())
				}
				return nil
			},
		}
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			trace := &statementCancellation{}
			cfg := db.pool.Config()
			cfg.ConnConfig.Tracer = trace
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			store := &Store{pool: pool}
			f := seed(t)
			if err := operation(store, f); err != nil {
				t.Fatal("baseline", err)
			}
			trace.mu.Lock()
			statements := append([]string(nil), trace.statements...)
			trace.mu.Unlock()
			if len(statements) < 1 {
				t.Fatal("transaction was not traced")
			}
			for i, sql := range statements {
				t.Run(fmt.Sprintf("statement_%02d", i+1), func(t *testing.T) {
					f := seed(t)
					before := snapshot(t, f.user.User.ID)
					trace.mu.Lock()
					trace.at = i + 1
					trace.seen = 0
					trace.statements = nil
					trace.mu.Unlock()
					err := operation(store, f)
					trace.mu.Lock()
					reached := trace.seen >= trace.at
					trace.at = 0
					trace.mu.Unlock()
					if !reached || err == nil {
						t.Fatalf("cancellation not handled for %s: %v", sql, err)
					}
					if after := snapshot(t, f.user.User.ID); after != before {
						t.Fatalf("partial mutation persisted after cancellation of %s", sql)
					}
				})
			}
		})
	}
}
