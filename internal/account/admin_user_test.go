package account

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminUserDetailsAndSessionRevocation(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	ctx := context.Background()
	first := complete(t, db, Identity{"email", "private-user@example.test"})
	second := complete(t, db, Identity{"email", "private-user@example.test"})
	other := complete(t, db, Identity{"email", "other@example.test"})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(adminTestContext(r.Context(), "google:owner"))
		w := httptest.NewRecorder()
		a.AdminHTTP(w, r)
		return w
	}
	detail := call("GET", "/api/users/"+first.User.ID, "")
	var data struct {
		ID        string
		Providers []string
		Active    int `json:"active_sessions"`
		Sessions  []struct{ ID, Status string }
	}
	if detail.Code != 200 || json.Unmarshal(detail.Body.Bytes(), &data) != nil || data.ID != first.User.ID || data.Active != 2 || len(data.Sessions) != 2 || len(data.Providers) != 1 || data.Providers[0] != "email" {
		t.Fatal(detail.Code, detail.Body.String())
	}
	for _, secret := range []string{"private-user@example.test", first.AccessToken, first.RefreshToken, "access_hash", "refresh_hash", "subject"} {
		if strings.Contains(detail.Body.String(), secret) {
			t.Fatal("private data exposed", secret)
		}
	}
	session := data.Sessions[0].ID
	action := func(userID string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"action": "revoke_session", "id": session, "user_id": userID})
		return call("POST", "/api/actions", string(body))
	}
	if w := action(other.User.ID); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := db.Authenticate(ctx, first.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Authenticate(ctx, second.AccessToken); err != nil {
		t.Fatal(err)
	}
	if w := action(first.User.ID); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	valid := 0
	for _, token := range []string{first.AccessToken, second.AccessToken} {
		if _, err := db.Authenticate(ctx, token); err == nil {
			valid++
		} else if err != ErrInvalid {
			t.Fatal(err)
		}
	}
	if valid != 1 {
		t.Fatal("expected exactly one valid session", valid)
	}
	if _, err := db.Authenticate(ctx, other.AccessToken); err != nil {
		t.Fatal("other user affected", err)
	}
	var audits int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE action='revoke_session' AND target=$1 AND actor='google:owner'`, session).Scan(&audits); err != nil || audits != 1 {
		t.Fatal(audits, err)
	}
	if w := call("GET", "/api/users/missing", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := call("GET", "/api/users/bad/path", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/api/users/"+first.User.ID, ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
