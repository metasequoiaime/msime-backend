package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestValidMirrorURL(t *testing.T) {
	for _, raw := range []string{"", "https://wwbn.lanzouq.com/iAbc123", "https://example.com", "https://example.com:8443/a?pwd=x#y", "https://" + strings.Repeat("a", maxMirrorURLBytes-len("https://"))} {
		if !validMirrorURL(raw) {
			t.Errorf("rejected %q", raw)
		}
	}
	for _, raw := range []string{"http://example.com/a", "HTTPS://example.com", "ftp://example.com", "//example.com/a", "example.com/a", "https://", "https:///path", "https://:443/a", "https://user:pass@example.com/a", "https://user@example.com", "https://example.com/a b", "https://example.com/\x00", "https://example.com/　", "javascript:alert(1)", "https:example.com", "https://" + strings.Repeat("a", maxMirrorURLBytes-len("https://")+1), "https://example.com/\xff"} {
		if validMirrorURL(raw) {
			t.Errorf("accepted %q", raw)
		}
	}
}

// The website builds against these exact keys, so pin the wire shape without a database.
func TestSiteDownloadMirrorsWireShape(t *testing.T) {
	for value, want := range map[any][]string{SiteDownloadMirrors{}: {"lanzou_url", "updated_at"}, SiteSettings{}: {"lanzou_url", "updated_at", "updated_by"}} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err = json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(fields))
		for key, v := range fields {
			if v != "" {
				t.Fatalf("zero value must be an empty string: %s", raw)
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, want) {
			t.Fatal(keys)
		}
	}
}

func TestSiteDownloadMirrorAdminEditAndPublicRead(t *testing.T) {
	db := testStore(t)
	ctx := t.Context()
	if _, err := db.pool.Exec(ctx, `TRUNCATE site_settings,admin_audit`); err != nil {
		t.Fatal(err)
	}
	a := &Service{store: db}
	mux := http.NewServeMux()
	Mount(mux, a)
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.AdminHTTP(w, r.WithContext(WithAdminActor(r.Context(), "google:sub admin@example.test")))
	})
	public := func() SiteDownloadMirrors {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/site/download-mirrors", nil))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=60" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
		var v SiteDownloadMirrors
		d := json.NewDecoder(w.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	settings := func(w *httptest.ResponseRecorder) SiteSettings {
		t.Helper()
		var v SiteSettings
		d := json.NewDecoder(w.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	audits := func() int {
		t.Helper()
		var n int
		if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if got := public(); got != (SiteDownloadMirrors{}) {
		t.Fatal("unset link must be empty strings", got)
	}
	if got := settings(apiRequest(t, admin, "GET", "/api/site-settings", "", "", 200)); got != (SiteSettings{}) {
		t.Fatal("unset admin settings", got)
	}
	for _, body := range []string{`{`, `{}`, `{"lanzou_url":null}`, `{"lanzou_url":"http://example.com"}`, `{"lanzou_url":"https://u:p@example.com/a"}`, `{"lanzou_url":"https://example.com","extra":1}`, `{"lanzou_url":"https://` + strings.Repeat("a", maxMirrorURLBytes) + `"}`} {
		apiRequest(t, admin, "POST", "/api/site-settings", body, "", 400)
	}
	apiRequest(t, admin, "PUT", "/api/site-settings", `{"lanzou_url":""}`, "", 405)
	apiRequest(t, admin, "DELETE", "/api/site-settings", "", "", 405)
	if n := audits(); n != 0 {
		t.Fatal("rejected writes audited", n)
	}

	before := time.Now().Add(-time.Minute)
	link := "https://wwbn.lanzouq.com/iAbc123"
	saved := settings(apiRequest(t, admin, "POST", "/api/site-settings", `{"lanzou_url":"  `+link+`  "}`, "", 200))
	at, err := time.Parse(time.RFC3339, saved.UpdatedAt)
	if saved.LanzouURL != link || saved.UpdatedBy != "google:sub admin@example.test" || err != nil || at.Before(before) {
		t.Fatal("saved settings", saved, err)
	}
	if got := settings(apiRequest(t, admin, "GET", "/api/site-settings", "", "", 200)); got != saved {
		t.Fatal("admin read", got, saved)
	}
	if got := public(); got != (SiteDownloadMirrors{LanzouURL: link, UpdatedAt: saved.UpdatedAt}) {
		t.Fatal("public read", got)
	}

	cleared := settings(apiRequest(t, admin, "POST", "/api/site-settings", `{"lanzou_url":""}`, "", 200))
	if cleared.LanzouURL != "" || cleared.UpdatedAt == "" || cleared.UpdatedBy != "google:sub admin@example.test" {
		t.Fatal("cleared admin settings keep the last editor", cleared)
	}
	if got := public(); got != (SiteDownloadMirrors{}) {
		t.Fatal("cleared link must be empty strings", got)
	}
	rows, err := db.pool.Query(ctx, `SELECT action,target,actor FROM admin_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var trail []string
	for rows.Next() {
		var action, target, actor string
		if err = rows.Scan(&action, &target, &actor); err != nil {
			t.Fatal(err)
		}
		trail = append(trail, action+" "+target+" "+actor)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"set_lanzou_url " + link + " google:sub admin@example.test", "clear_lanzou_url lanzou_url google:sub admin@example.test"}; !reflect.DeepEqual(trail, want) {
		t.Fatal("audit trail", trail)
	}

	// A failing audit insert must roll the setting change back with it.
	if _, err = db.pool.Exec(ctx, `ALTER TABLE admin_audit ADD CONSTRAINT site_settings_test_reject CHECK(action<>'set_lanzou_url') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.pool.Exec(context.Background(), `ALTER TABLE admin_audit DROP CONSTRAINT IF EXISTS site_settings_test_reject`)
	})
	apiRequest(t, admin, "POST", "/api/site-settings", `{"lanzou_url":"`+link+`"}`, "", 503)
	if got := public(); got != (SiteDownloadMirrors{}) {
		t.Fatal("setting changed without its audit record", got)
	}
}
