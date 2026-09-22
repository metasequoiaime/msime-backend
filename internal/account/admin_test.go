package account

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminDataAndActions(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `TRUNCATE admin_events,admin_audit`); err != nil {
		t.Fatal(err)
	}
	a := &Service{store: db}
	if err := a.AdminReady(ctx); err != nil {
		t.Fatal(err)
	}
	user := complete(t, db, Identity{"email", "admin-data@example.test"})
	call := func(method, path, body string, telemetry bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(WithAdminActor(r.Context(), "google:test:admin@example.test"))
		w := httptest.NewRecorder()
		if telemetry {
			a.Telemetry(w, r)
		} else {
			a.AdminHTTP(w, r)
		}
		return w
	}
	for i := 0; i < 2; i++ {
		if w := call("POST", "/v1/telemetry/events", `{"id":"test-download-0001","kind":"download","platform":"windows","version":"1.0"}`, true); w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call("POST", "/v1/telemetry/events", `{"id":"test-crash-000001","kind":"crash","platform":"windows","version":"1.0","message":"Oops","stack":"<script>alert(1)</script>"}`, true); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if w := call("POST", "/v1/telemetry/events", `{"id":"test-crash-000002","kind":"crash","platform":"windows","version":"1.0"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/v1/telemetry/events", `{"id":"test-download-0002","kind":"download","platform":"windows","version":"1.0","message":"not permitted"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	overview := call("GET", "/api/overview", "", false)
	var summary struct {
		Users, Downloads, Crashes int
		Daily                     []any
	}
	if overview.Code != 200 || json.Unmarshal(overview.Body.Bytes(), &summary) != nil || summary.Users != 1 || summary.Downloads != 1 || summary.Crashes != 1 || len(summary.Daily) != 30 {
		t.Fatal(overview.Code, overview.Body.String())
	}
	short := call("GET", "/api/overview?days=7", "", false)
	var shortSummary struct {
		RangeDays int   `json:"range_days"`
		Daily     []any `json:"daily"`
	}
	if short.Code != 200 || json.Unmarshal(short.Body.Bytes(), &shortSummary) != nil || shortSummary.RangeDays != 7 || len(shortSummary.Daily) != 7 {
		t.Fatal(short.Code, short.Body.String())
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO community_skins(id,owner_id,name,design) VALUES('skin-test',$1,'Skin','{}');`, user.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO community_resources(id,owner_id,kind,name,content) VALUES('dict-test',$1,'dictionary','Dict','{"entries":[]}'),('reply-test',$1,'reply','Reply','{"prompt":"Hello"}')`, user.User.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"users", "skins", "dictionaries", "replies", "downloads", "crashes", "audit"} {
		if w := call("GET", "/api/"+path, "", false); w.Code != 200 || !strings.Contains(w.Body.String(), `"items"`) {
			t.Fatal(path, w.Body.String())
		}
	}
	for _, path := range []string{"users?page=0", "users?page=oops", "users?page=10001"} {
		if w := call("GET", "/api/"+path, "", false); w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	if w := call("GET", "/api/users?q=%27%3B%20DROP%20TABLE%20auth_users%3B--", "", false); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, action := range []struct{ action, id string }{{"resolve_crash", "test-crash-000001"}, {"reopen_crash", "test-crash-000001"}, {"revoke_sessions", user.User.ID}, {"delete_skin", "skin-test"}, {"delete_dictionary", "dict-test"}, {"delete_reply", "reply-test"}} {
		body, _ := json.Marshal(map[string]string{"action": action.action, "id": action.id})
		w := call("POST", "/api/actions", string(body), false)
		if w.Code != 200 {
			t.Fatal(action, w.Body.String())
		}
	}
	if _, err := db.Authenticate(ctx, user.AccessToken); err != ErrInvalid {
		t.Fatal("session not revoked", err)
	}
	var audits int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE actor='google:test:admin@example.test'`).Scan(&audits); err != nil || audits != 6 {
		t.Fatal(audits, err)
	}
	if w := call("POST", "/api/actions", `{"action":"delete_skin","id":"missing"}`, false); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/actions", `{"action":"DROP TABLE","id":"missing"}`, false); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO admin_events(id,kind,platform,version) SELECT 'bulk-'||i,'download','test','1' FROM generate_series(1,51) i`); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Total int   `json:"total"`
		Items []any `json:"items"`
		More  bool  `json:"has_more"`
	}
	w := call("GET", "/api/downloads?q=bulk-", "", false)
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 50 || list.Total != 51 || !list.More {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/api/downloads?q=bulk-&page=2", "", false)
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Total != 51 || list.More {
		t.Fatal(w.Body.String())
	}
}

func TestAdminListFilters(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `TRUNCATE admin_events; INSERT INTO admin_events(id,kind,platform,version,message,resolved) VALUES ('a','crash','ios','1','failure',false),('b','crash','ios','2','failure',true),('c','crash','windows','1','failure',false),('d','download','ios','1','',false)`); err != nil {
		t.Fatal(err)
	}
	a := &Service{store: db}
	for _, tc := range []struct {
		query        string
		total, count int
	}{
		{"crashes", 3, 3}, {"crashes?status=open", 2, 2}, {"crashes?status=resolved", 1, 1},
		{"crashes?platform=ios&version=1&status=open", 1, 1}, {"crashes?platform=ios&version=1&status=resolved", 0, 0},
		{"downloads?platform=ios&version=1", 1, 1}, {"downloads?platform=ios&version=1&page=2", 1, 0},
		{"crashes?q=failure&platform=windows", 1, 1}, {"crashes?platform=io", 0, 0}, {"crashes?version=%27%3B--", 0, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.AdminHTTP(w, httptest.NewRequest("GET", "/api/"+tc.query, nil))
			var got struct {
				Total int
				Items []json.RawMessage
				More  bool `json:"has_more"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Total != tc.total || len(got.Items) != tc.count || got.More {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, query := range []string{"crashes?status=bad", "users?status=open", "audit?platform=ios", "downloads?status=open", "crashes?platform=" + strings.Repeat("a", 33), "downloads?version=" + strings.Repeat("b", 65)} {
		w := httptest.NewRecorder()
		a.AdminHTTP(w, httptest.NewRequest("GET", "/api/"+query, nil))
		if w.Code != 400 {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	a.AdminHTTP(w, httptest.NewRequest("GET", "/api/audit?action=resolve_crash", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
