package account

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
)

func permissionsRequest(a *Service, access AdminAccess, method, body string) *httptest.ResponseRecorder {
	r := jsonRequest(method, "/api/permissions", body, "")
	r = r.WithContext(WithAdminAccess(r.Context(), access))
	w := httptest.NewRecorder()
	a.AdminHTTP(w, r)
	return w
}

type permissionsResponse struct {
	Roles       []adminRole             `json:"roles"`
	Permissions []string                `json:"permissions"`
	Matrix      map[string][]string     `json:"matrix"`
	Members     []adminPermissionMember `json:"members"`
}

func TestAdminPermissionsMatrix(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	a := &Service{store: db, admin: AdminSettings{Owners: []string{"owner@example.test"}}}
	truncateAdminPersonal(t, db)
	// The matrix is shared by every test; put back whatever this test changes.
	t.Cleanup(func() {
		_, _ = db.pool.Exec(context.Background(), `DELETE FROM admin_role_permissions WHERE role='readonly' AND permission='ban_users'`)
		_, _ = db.pool.Exec(context.Background(), `INSERT INTO admin_role_permissions(role,permission) VALUES('reviewer','triage_issues') ON CONFLICT DO NOTHING`)
	})
	if _, err := db.pool.Exec(ctx, `INSERT INTO admin_members(email,role,enabled) VALUES('owner@example.test','readonly',true),('helper@example.test','reviewer',true),('off@example.test','operator',false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateAdminSession(ctx, AdminIdentity{Subject: "h", Email: "helper@example.test"}); err != nil {
		t.Fatal(err)
	}
	owner := adminTestContext(ctx, "google:o:owner@example.test")
	ownerAccess, _ := AdminAccessFrom(owner)
	reader := AdminAccess{Actor: "pat:helper@example.test", Email: "helper@example.test", Role: "reviewer", Permissions: []string{PermReviewDictPR}}

	w := permissionsRequest(a, reader, "GET", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got permissionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, role := range got.Roles {
		keys = append(keys, role.Key)
	}
	if !slices.Equal(keys[:4], []string{"maintainer", "reviewer", "operator", "readonly"}) || got.Roles[0].Name != "维护者" || !slices.Equal(got.Permissions, AllAdminPermissions()) {
		t.Fatalf("%+v", got)
	}
	if !slices.Equal(got.Matrix["maintainer"], AllAdminPermissions()) || !slices.Equal(got.Matrix["reviewer"], []string{PermReviewDictPR, PermReviewCommunity, PermTriageIssues}) || !slices.Equal(got.Matrix["readonly"], []string{PermViewCloudUsage}) {
		t.Fatalf("matrix %+v", got.Matrix)
	}
	// The owner is listed once, as a maintainer, even with a stale member row of another role.
	if len(got.Members) != 3 || got.Members[0].Email != "owner@example.test" || !got.Members[0].Owner || got.Members[0].Role != RoleMaintainer {
		t.Fatalf("members %+v", got.Members)
	}
	if m := got.Members[1]; m.Email != "helper@example.test" || m.Role != "reviewer" || !m.Enabled || m.Sessions != 1 || m.LastSeenAt == nil || m.Owner {
		t.Fatalf("member %+v", m)
	}
	if m := got.Members[2]; m.Email != "off@example.test" || m.Enabled || m.Sessions != 0 || m.LastSeenAt != nil {
		t.Fatalf("member %+v", m)
	}

	audits := func() int {
		t.Helper()
		var n int
		if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit WHERE action LIKE 'permission\_%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Writing requires manage_permissions, which the legacy token lacks too.
	legacy := AdminAccess{Actor: "legacy-token", Role: RoleMaintainer, Permissions: []string{PermReviewDictPR, PermBanUsers}}
	for _, access := range []AdminAccess{reader, legacy} {
		if w = permissionsRequest(a, access, "POST", `{"action":"grant","role":"readonly","permission":"ban_users"}`); w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for body, want := range map[string]int{
		`{"action":"revoke","role":"maintainer","permission":"manage_permissions"}`: 409,
		`{"action":"grant","role":"nobody","permission":"ban_users"}`:               404,
		`{"action":"grant","role":"readonly","permission":"fly"}`:                   400,
		`{"action":"toggle","role":"readonly","permission":"ban_users"}`:            400,
		`{"action":"grant","role":"Readonly","permission":"ban_users"}`:             400,
		`{"action":"grant","role":"readonly","permission":"ban_users","x":1}`:       400,
	} {
		if w = permissionsRequest(a, ownerAccess, "POST", body); w.Code != want {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	if audits() != 0 {
		t.Fatal("refused change audited")
	}
	for _, step := range []struct {
		body     string
		affected float64
	}{
		{`{"action":"grant","role":"readonly","permission":"ban_users"}`, 1},
		{`{"action":"grant","role":"readonly","permission":"ban_users"}`, 0},
		{`{"action":"revoke","role":"reviewer","permission":"triage_issues"}`, 1},
		{`{"action":"revoke","role":"reviewer","permission":"triage_issues"}`, 0},
	} {
		w = permissionsRequest(a, ownerAccess, "POST", step.body)
		var v map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		if w.Code != 200 || v["affected"] != step.affected {
			t.Fatal(step.body, w.Code, w.Body.String())
		}
	}
	if audits() != 2 {
		t.Fatal("expected one audit row per change", audits())
	}
	var detail map[string]string
	if err := db.pool.QueryRow(ctx, `SELECT detail FROM admin_audit WHERE action='permission_revoke'`).Scan(&detail); err != nil || detail["role"] != "reviewer" || detail["permission"] != PermTriageIssues || detail["role_name"] != "审核志愿者" {
		t.Fatal(detail, err)
	}
	// The change applies to the member's next request.
	if _, perms, err := a.AdminMemberRole(ctx, "helper@example.test"); err != nil || slices.Contains(perms, PermTriageIssues) {
		t.Fatal(perms, err)
	}

	// The audit list carries detail for the console's phrasing.
	r := jsonRequest("GET", "/api/audit?action=permission_grant", "", "")
	r = r.WithContext(owner)
	w = httptest.NewRecorder()
	a.AdminHTTP(w, r)
	var list struct {
		Items []struct {
			Actor  string            `json:"actor"`
			Detail map[string]string `json:"detail"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Items) != 1 || list.Items[0].Detail["permission"] != PermBanUsers || list.Items[0].Actor != "google:o:owner@example.test" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAdminMemberSetRole(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	a := &Service{store: db}
	truncateAdminPersonal(t, db)
	call := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := jsonRequest("POST", "/api/admins", body, "")
		r = r.WithContext(WithAdminActor(r.Context(), "google:o:owner@example.test"))
		w := httptest.NewRecorder()
		a.AdminMembersHTTP(w, r, []string{"owner@example.test"})
		return w
	}
	for body, want := range map[string]int{
		`{"action":"add","email":"member@example.test","role":"ghost"}`:          400,
		`{"action":"add","email":"member@example.test","role":"reviewer"}`:       200,
		`{"action":"set_role","email":"member@example.test"}`:                    400,
		`{"action":"disable","email":"member@example.test","role":"x"}`:          400,
		`{"action":"set_role","email":"owner@example.test","role":"readonly"}`:   403,
		`{"action":"set_role","email":"missing@example.test","role":"readonly"}`: 404,
	} {
		if w := call(body); w.Code != want {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	if role, _, err := a.AdminMemberRole(ctx, "member@example.test"); err != nil || role != "reviewer" {
		t.Fatal(role, err)
	}
	// Adding clears any older credentials, so the member signs in after the add.
	session, err := a.CreateAdminSession(ctx, AdminIdentity{Subject: "m", Email: "member@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if w := call(`{"action":"set_role","email":"member@example.test","role":"operator"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(`{"action":"set_role","email":"member@example.test","role":"operator"}`); w.Code != 200 || !json.Valid(w.Body.Bytes()) {
		t.Fatal(w.Code)
	}
	role, perms, err := a.AdminMemberRole(ctx, "member@example.test")
	if err != nil || role != "operator" || !slices.Contains(perms, PermBanUsers) {
		t.Fatal(role, perms, err)
	}
	// A role change keeps the member signed in.
	if _, err = a.AdminSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	var n int
	var detail map[string]string
	if err = db.pool.QueryRow(ctx, `SELECT count(*),max(detail::text)::jsonb FROM admin_audit WHERE action='admin_set_role'`).Scan(&n, &detail); err != nil || n != 1 || detail["from"] != "reviewer" || detail["to"] != "operator" {
		t.Fatal(n, detail, err)
	}
	w := httptest.NewRecorder()
	a.AdminMembersHTTP(w, jsonRequest("GET", "/api/admins", "", ""), []string{"owner@example.test"})
	var list struct {
		Items []struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"items"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Role != "operator" {
		t.Fatal(w.Body.String(), err)
	}
}
