package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAdminHTTPValidationAndAuditAtomicity(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `TRUNCATE admin_events,admin_audit`); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.AdminHTTP(w, r.WithContext(adminTestContext(r.Context(), "test-admin")))
	})
	for _, body := range []string{`{`, `{} {}`, `{"action":"resolve_crash","id":"missing","extra":true}`, `{"action":"unknown","id":"missing"}`, `{"action":"resolve_crash","id":""}`, `{"action":"revoke_session","id":"missing"}`} {
		apiRequest(t, handler, "POST", "/api/actions", body, "", 400)
	}
	for _, action := range []string{"delete_skin", "delete_candidate_skin", "delete_plugin", "delete_dictionary", "delete_reply", "resolve_crash", "reopen_crash"} {
		apiRequest(t, handler, "POST", "/api/actions", `{"action":"`+action+`","id":"missing"}`, "", 404)
	}
	apiRequest(t, handler, "POST", "/api/actions", `{"action":"revoke_sessions","id":"missing"}`, "", 404)
	for _, path := range []string{"users", "downloads", "crashes", "skins", "candidate-skins", "plugins", "dictionaries", "replies", "audit"} {
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
	for _, body := range []string{`{`, valid + `{}`, strings.Replace(valid, `"download"`, `"other"`, 1), strings.Replace(valid, `"ios"`, `""`, 1), strings.Replace(valid, `"version":"1"`, `"version":"1","stack":"forbidden"`, 1), strings.Replace(valid, `"download"`, `"crash"`, 1), strings.Replace(valid, `"version":"1"`, `"version":"`+strings.Repeat("x", 65)+`"`, 1), strings.Replace(valid, `"version":"1"`, `"version":"1","unknown":"x"`, 1), strings.Replace(valid, `"version":"1"`, `"version":"1","channel":"GitHub"`, 1), strings.Replace(valid, `"version":"1"`, `"version":"1","artifact":"`+strings.Repeat("x", 65)+`"`, 1), strings.Replace(valid, `"version":"1"`, `"version":"1","install_id":"short"`, 1), strings.Replace(valid, `"download"`, `"active"`, 1)} {
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

// Windows 的崩溃文本以 CRLF 换行，堆栈也可能超长：校验前先统一换行，所以崩溃能被接收并与 LF 的报告归入同一分组；超长堆栈在行边界处截断，而不是被拒绝。
func TestTelemetryCrashTextNormalization(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	if _, err := db.pool.Exec(t.Context(), `TRUNCATE admin_events,admin_crash_groups`); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(a.Telemetry)
	crash := func(id, message, stack string) string {
		body, err := json.Marshal(map[string]string{"id": id, "kind": "crash", "platform": "windows", "version": "1.0", "message": message, "stack": stack, "install_id": "install-crlf-000001"})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	apiRequest(t, handler, "POST", TelemetryPath, crash("crlf-crash-event-0001", "Access violation\r\nat 0x0", "msime.dll+0x1234\r\nkernel32.dll+0x10\rntdll.dll+0x20"), "", 202)
	apiRequest(t, handler, "POST", TelemetryPath, crash("crlf-crash-event-0002", "Access violation\nat 0x0", "msime.dll+0x1234\nkernel32.dll+0x10\nntdll.dll+0x20"), "", 202)
	var message, stack string
	if err := db.pool.QueryRow(t.Context(), `SELECT message,stack FROM admin_events WHERE id='crlf-crash-event-0001'`).Scan(&message, &stack); err != nil || message != "Access violation\nat 0x0" || stack != "msime.dll+0x1234\nkernel32.dll+0x10\nntdll.dll+0x20" {
		t.Fatalf("CRLF not normalized: %q %q %v", message, stack, err)
	}
	var signatures, groups int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(DISTINCT signature),(SELECT count(*) FROM admin_crash_groups) FROM admin_events WHERE kind='crash'`).Scan(&signatures, &groups); err != nil || signatures != 1 || groups != 1 {
		t.Fatal("CRLF and LF reports of one crash should share a group", signatures, groups, err)
	}

	// 全是 ASCII，所以 20000 个标量的堆栈仍然放得进 32 KiB 的请求体。
	line := strings.Repeat("f", 99) + "\n"
	long := strings.Repeat(line, 200)
	apiRequest(t, handler, "POST", TelemetryPath, crash("long-stack-event-0001", "boom", long), "", 202)
	if err := db.pool.QueryRow(t.Context(), `SELECT stack FROM admin_events WHERE id='long-stack-event-0001'`).Scan(&stack); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(stack); n > telemetryStackLimit || n != 160*100-1 || strings.HasSuffix(stack, "\n") || !strings.HasSuffix(stack, "\n"+strings.Repeat("f", 99)) {
		t.Fatalf("stack not cut at a line boundary: %d scalars", n)
	}
	// message 的上限仍然是硬性规则，非 crash 事件也仍然不能带文本。
	apiRequest(t, handler, "POST", TelemetryPath, crash("long-message-event-01", strings.Repeat("m", telemetryMessageLimit+1), "frame"), "", 400)
	apiRequest(t, handler, "POST", TelemetryPath, `{"id":"crlf-session-event-01","kind":"session","platform":"windows","version":"1","message":"\r\n"}`, "", 400)
}

func TestTruncateStack(t *testing.T) {
	for _, tc := range []struct {
		stack string
		limit int
		want  string
	}{
		{"a\nb\nc", 5, "a\nb\nc"},
		{"a\nb\nc", 4, "a\nb"},
		{"ab\ncd", 3, "ab"},
		{"abcdef", 3, "abc"},
		{"\nabcdef", 3, "\nab"},
		{"界界\n界界", 4, "界界"},
	} {
		if got := truncateStack(tc.stack, tc.limit); got != tc.want {
			t.Errorf("truncateStack(%q, %d) = %q, want %q", tc.stack, tc.limit, got, tc.want)
		}
	}
}

// 公开路由不需要凭据，带了也忽略；按配置的客户端地址头计入独立的按地址额度，并限制每个地址每天的 crash 事件数。
func TestTelemetryAnonymousRouteLimits(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db, clientIPHeader: "CF-Connecting-IP"}
	if _, err := db.pool.Exec(t.Context(), `TRUNCATE admin_events,admin_crash_groups`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `DELETE FROM auth_rates WHERE key LIKE 'telemetry-%' OR key LIKE 'ip:%'`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+TelemetryPath, Route(a, "POST "+TelemetryPath, (*Service).Telemetry))
	send := func(client, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", TelemetryPath, strings.NewReader(body))
		r.RemoteAddr = "10.0.0.1:443" // 所有请求都经过同一个代理
		r.Header.Set("CF-Connecting-IP", client)
		r.Header.Set("Content-Type", "application/json")
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	event := func(i int, kind string) string {
		extra := `,"install_id":"install-anon-000001"`
		if kind == "crash" {
			extra += `,"message":"boom ` + strconv.Itoa(i) + `"`
		}
		return `{"id":"anonymous-event-` + strconv.Itoa(1000+i) + `","kind":"` + kind + `","platform":"win","version":"1"` + extra + `}`
	}
	if w := send("198.51.100.10", "", event(0, "active")); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	// 令牌无论是否有效都被忽略，不做校验。
	if w := send("198.51.100.10", "Bearer not-a-real-session", event(1, "session")); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("198.51.100.10", "", event(1, "session")); w.Code != 202 {
		t.Fatal("a retried event must be accepted again", w.Code)
	}
	var count int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM admin_events`).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate id stored twice", count, err)
	}

	// crash 事件：每个地址每天 20 次，之后返回 429 并带较长的 Retry-After；其他地址不受影响。
	for i := 0; i < telemetryCrashDailyLimit; i++ {
		if w := send("198.51.100.20", "", event(100+i, "crash")); w.Code != 202 {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	w := send("198.51.100.20", "", event(200, "crash"))
	if w.Code != 429 || w.Header().Get("Retry-After") != "3600" {
		t.Fatal("crash limit", w.Code, w.Header().Get("Retry-After"))
	}
	if w = send("198.51.100.21", "", event(201, "crash")); w.Code != 202 {
		t.Fatal("crash limit leaked across addresses", w.Code)
	}

	// 每分钟额度：每个地址 60 次，按客户端地址而不是代理地址计。
	if _, err := db.pool.Exec(t.Context(), `DELETE FROM auth_rates WHERE key LIKE 'telemetry-ip:%'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < telemetryRateLimit; i++ {
		if w = send("2001:db8:1:2::1", "", event(300+i, "session")); w.Code != 202 {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	// 同一个 /64，所以是同一份额度。
	if w = send("2001:db8:1:2::ffff", "", event(400, "session")); w.Code != 429 || w.Header().Get("Retry-After") != "60" {
		t.Fatal("per-minute limit", w.Code, w.Header().Get("Retry-After"))
	}
	if w = send("2001:db8:1:3::1", "", event(401, "session")); w.Code != 202 {
		t.Fatal("another /64 shares the bucket", w.Code)
	}
	// 遥测额度与登录和社区接口每分钟 120 次的额度分开计。
	var shared int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_rates WHERE key LIKE 'ip:%'`).Scan(&shared); err != nil || shared != 0 {
		t.Fatal("telemetry charged the account bucket", shared, err)
	}
}
