package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	msimebackend "github.com/metasequoiaime/MSIME-Backend"
	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

func TestInterleaveSearchHits(t *testing.T) {
	hit := func(id string) account.AdminSearchHit { return account.AdminSearchHit{ID: id} }
	ids := func(hits []account.AdminSearchHit) string {
		out := []string{}
		for _, h := range hits {
			out = append(out, h.ID)
		}
		return strings.Join(out, ",")
	}
	db := []account.AdminSearchHit{hit("u1"), hit("u2"), hit("u3"), hit("u4"), hit("u5"), hit("u6"), hit("u7"), hit("u8")}
	if got := ids(interleaveSearchHits(8, db, []account.AdminSearchHit{hit("pr1")}, nil, []account.AdminSearchHit{hit("r1"), hit("r2")})); got != "u1,pr1,r1,u2,r2,u3,u4,u5" {
		t.Fatal(got)
	}
	if got := interleaveSearchHits(8); got == nil || len(got) != 0 {
		t.Fatalf("no sources %#v", got)
	}
}

// Method and query validation happen before any database access.
func TestAdminShellAndSearchValidation(t *testing.T) {
	s, _ := adminRBACFixture(t)
	legacy := strings.Repeat("a", 40)
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"POST", "/api/shell", 405, "method_not_allowed"},
		{"POST", "/api/search?q=x", 405, "method_not_allowed"},
		{"GET", "/api/search?q=" + strings.Repeat("x", 101), 400, "invalid_query"},
		{"GET", "/api/search?q=%00", 400, "invalid_query"},
		{"GET", "/api/search?q=%20%20", 200, `{"items":[]}`},
		{"GET", "/api/search", 200, `{"items":[]}`},
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, adminRequest(s, tc.method, tc.path, "", legacy))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestAdminShellAndSearchWithDatabase(t *testing.T) {
	admin, schema := disposableSchema(t)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	t.Setenv("TEST_ADMIN_TOKEN", strings.Repeat("q", 48))
	s, err := New(Config{
		Auth:    account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
		Admin:   AdminConfig{Enabled: true, Host: "admin.example.com", TokenEnv: "TEST_ADMIN_TOKEN", Environment: "测试环境", Logs: AdminLogsConfig{LokiURL: "http://loki.example.test:3100"}},
		Clients: []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 120}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.CloseAccounts()
	ctx := context.Background()
	if _, err := admin.Exec(ctx, "SET search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO admin_crash_groups(signature,platform,version,title) VALUES('0123456789abcdef','ios','1.0','LayoutCandidates overflow')`); err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.NotifyNow(ctx, account.Notification{Kind: account.NotifyIncident, TargetPage: "status"}); err != nil {
		t.Fatal(err)
	}
	get := func(path string, out any) {
		t.Helper()
		r := httptest.NewRequest("GET", "https://admin.example.com"+path, nil)
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("q", 48))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), out) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	var shell struct {
		Version     string `json:"version"`
		Environment string `json:"environment"`
		Me          struct {
			Email       string   `json:"email"`
			Name        string   `json:"name"`
			Role        string   `json:"role"`
			Permissions []string `json:"permissions"`
		} `json:"me"`
		Pending             map[string]int  `json:"pending"`
		UnreadNotifications int             `json:"unread_notifications"`
		Status              string          `json:"status"`
		Features            map[string]bool `json:"features"`
	}
	get("/api/shell", &shell)
	// 配置了 admin.logs 时外壳报告服务日志已启用，前端据此显示该页。
	if len(shell.Features) != 1 || !shell.Features["logs"] {
		t.Fatalf("shell features %+v", shell.Features)
	}
	// The legacy token has no email: no name and no notifications, every permission but manage_permissions.
	if shell.Version != msimebackend.Version() || shell.Environment != "测试环境" || shell.Me.Email != "" || shell.Me.Role != account.RoleMaintainer || len(shell.Me.Permissions) != len(account.AllAdminPermissions())-1 || slices.Contains(shell.Me.Permissions, account.PermManagePermissions) {
		t.Fatalf("shell %+v", shell)
	}
	if len(shell.Pending) != 3 || shell.Pending["dict_prs"] != 0 || shell.Pending["community"] != 0 || shell.Pending["issues"] != 0 || shell.UnreadNotifications != 0 {
		t.Fatalf("shell counts %+v", shell)
	}
	// No status probe has run yet, and the database answered, so the backend reports ok.
	if shell.Status != "ok" {
		t.Fatalf("status %q", shell.Status)
	}
	var search struct {
		Items []account.AdminSearchHit `json:"items"`
	}
	get("/api/search?q=layoutcandidates", &search)
	if len(search.Items) != 1 || search.Items[0] != (account.AdminSearchHit{Kind: "crash_group", ID: "0123456789abcdef", Title: "LayoutCandidates overflow", Where: "崩溃上报", Target: "crash"}) {
		t.Fatalf("search %+v", search.Items)
	}
}
