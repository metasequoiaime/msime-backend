package account

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminHTTPValidationAndAuditAtomicity(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `TRUNCATE admin_events,admin_audit`); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.AdminHTTP(w, r.WithContext(WithAdminActor(r.Context(), "test-admin")))
	})
	for _, body := range []string{`{`, `{} {}`, `{"action":"resolve_crash","id":"missing","extra":true}`, `{"action":"unknown","id":"missing"}`, `{"action":"resolve_crash","id":""}`, `{"action":"revoke_session","id":"missing"}`} {
		apiRequest(t, handler, "POST", "/api/actions", body, "", 400)
	}
	for _, action := range []string{"delete_skin", "delete_dictionary", "delete_reply", "resolve_crash", "reopen_crash"} {
		apiRequest(t, handler, "POST", "/api/actions", `{"action":"`+action+`","id":"missing"}`, "", 404)
	}
	apiRequest(t, handler, "POST", "/api/actions", `{"action":"revoke_sessions","id":"missing"}`, "", 404)
	for _, path := range []string{"users", "downloads", "crashes", "skins", "dictionaries", "replies", "audit"} {
		apiRequest(t, handler, "GET", "/api/"+path+"?page=10001", "", "", 400)
		apiRequest(t, handler, "GET", "/api/"+path+"?q="+strings.Repeat("a", 201), "", "", 400)
		apiRequest(t, handler, "POST", "/api/"+path, `{}`, "", 405)
	}
	apiRequest(t, handler, "GET", "/api/overview?days=14", "", "", 400)
	apiRequest(t, handler, "GET", "/api/unknown", "", "", 404)
	var count int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed action audited as success", count, err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO admin_events(id,kind,platform,version,message) VALUES('crash-contract','crash','ios','1','failure'),('download-contract','download','ios','1','')`); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"resolve_crash", "reopen_crash"} {
		apiRequest(t, handler, "POST", "/api/actions", `{"action":"`+action+`","id":"download-contract"}`, "", 404)
		apiRequest(t, handler, "POST", "/api/actions", `{"action":"`+action+`","id":"crash-contract"}`, "", 200)
		var resolved bool
		if err := db.pool.QueryRow(ctx, `SELECT resolved FROM admin_events WHERE id='crash-contract'`).Scan(&resolved); err != nil || resolved != (action == "resolve_crash") {
			t.Fatal("wrong crash state", resolved, err)
		}
	}
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE target='crash-contract' AND actor='test-admin'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("missing action audit", count, err)
	}
}

func TestTelemetryHTTPValidationAndIdempotency(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	if _, err := db.pool.Exec(t.Context(), `TRUNCATE admin_events`); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(a.Telemetry)
	valid := `{"id":"telemetry-contract-01","kind":"download","platform":"ios","version":"1"}`
	apiRequest(t, handler, "POST", "/v1/telemetry/events", valid, "", 202)
	apiRequest(t, handler, "POST", "/v1/telemetry/events", strings.Replace(valid, `"ios"`, `"windows"`, 1), "", 202)
	for _, body := range []string{`{`, valid + `{}`, strings.Replace(valid, `"download"`, `"other"`, 1), strings.Replace(valid, `"ios"`, `""`, 1), strings.Replace(valid, `"version":"1"`, `"version":"1","stack":"forbidden"`, 1), strings.Replace(valid, `"download"`, `"crash"`, 1), strings.Replace(valid, `"version":"1"`, `"version":"`+strings.Repeat("x", 65)+`"`, 1)} {
		apiRequest(t, handler, "POST", "/v1/telemetry/events", body, "", 400)
	}
	var count int
	var platform string
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*),min(platform) FROM admin_events`).Scan(&count, &platform); err != nil || count != 1 || platform != "ios" {
		t.Fatal("idempotency or invalid write", count, platform, err)
	}
	r := httptest.NewRequest("POST", "/v1/telemetry/events", strings.NewReader(valid))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal("missing media type accepted", w.Code)
	}
}
