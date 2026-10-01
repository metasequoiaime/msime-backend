package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// The histogram estimates percentiles inside the bucket that holds them and survives the database's JSON form.
func TestLatencyHistogram(t *testing.T) {
	var c metricCounts
	if _, ok := c.latency.percentile(0.95); ok {
		t.Fatal("an empty histogram has a percentile")
	}
	for range 19 {
		c.observe(80*time.Millisecond, false, 0)
	}
	c.observe(4*time.Second, true, 10)
	if c.calls != 20 || c.errors != 1 || c.usage != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if p95, _ := c.latency.percentile(0.95); p95 < 50 || p95 > 100 {
		t.Fatalf("p95 = %d, want inside the 50..100ms bucket", p95)
	}
	if p100, _ := c.latency.percentile(1); p100 <= 3000 || p100 > 5000 {
		t.Fatalf("max = %d, want inside the 3000..5000ms bucket", p100)
	}
	c.observe(10*time.Minute, false, 2.5)
	if c.usage != 2.5 {
		t.Fatalf("usage of a successful call = %v", c.usage)
	}
	if p, _ := c.latency.percentile(1); p != 120000 {
		t.Fatalf("overflow percentile = %d", p)
	}
	encoded := c.latency.histogramJSON()
	if encoded["100"] != 19 || encoded["5000"] != 1 || encoded[overflowBucket] != 1 || len(encoded) != 3 {
		t.Fatalf("histogram JSON = %v", encoded)
	}
	if histogramFromJSON(encoded) != c.latency {
		t.Fatal("histogram JSON does not round-trip")
	}
	// A bound the code no longer uses lands in the next bucket up rather than being lost.
	if h := histogramFromJSON(map[string]int64{"60": 2, "bogus": 1, "10": -1}); h[2] != 2 || h[len(latencyBoundsMS)] != 1 {
		t.Fatalf("foreign keys = %v", h)
	}
}

// Hourly buckets are taken for flushing, restored when the flush fails (dropping stale ones), and the minute ring answers the probe's window.
func TestServiceMetricsBuckets(t *testing.T) {
	var m serviceMetrics
	now := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	m.record("chat", now.Add(-7*time.Minute), time.Second, false, 0)
	m.record("chat", now.Add(-2*time.Minute), time.Second, true, 0)
	m.record("chat", now, 2*time.Second, false, 0)
	m.record("translation", now, time.Millisecond, false, 12)
	if w := m.recent("chat", now, 5); w.calls != 2 || w.errors != 1 {
		t.Fatalf("five-minute window = %+v", w)
	}
	if w := m.recent("missing", now, 5); w.calls != 0 {
		t.Fatalf("unknown service window = %+v", w)
	}
	if pending := m.pending(); len(pending) != 2 {
		t.Fatalf("pending = %+v", pending)
	}
	rows := m.take()
	if len(rows) != 2 || len(m.take()) != 0 {
		t.Fatalf("take = %+v", rows)
	}
	for _, row := range rows {
		if !row.Hour.Equal(now.Truncate(time.Hour)) || (row.Service == "chat" && (row.Calls != 3 || row.Errors != 1)) || (row.Service == "translation" && row.Usage != 12) {
			t.Fatalf("row = %+v", row)
		}
	}
	stale := account.ServiceMetric{Service: "cloud", Hour: now.Add(-8 * 24 * time.Hour), Calls: 5}
	m.restore(append(rows, stale), now)
	m.record("chat", now, time.Second, false, 0)
	restored := map[string]int64{}
	for _, row := range m.take() {
		restored[row.Service] += row.Calls
	}
	if restored["chat"] != 4 || restored["translation"] != 1 || restored["cloud"] != 0 {
		t.Fatalf("restored = %v", restored)
	}
	// The ring forgets minutes that have rotated out.
	if w := m.recent("chat", now.Add(20*time.Minute), 5); w.calls != 0 {
		t.Fatalf("window after 20 minutes = %+v", w)
	}
}

func TestJudgeService(t *testing.T) {
	window := func(calls, errors int, latency time.Duration) metricCounts {
		var c metricCounts
		for i := range calls {
			c.observe(latency, i < errors, 0)
		}
		return c
	}
	for _, tc := range []struct {
		name string
		w    metricCounts
		want string
	}{
		{"no traffic", metricCounts{}, stateIdle},
		{"healthy", window(50, 1, 100*time.Millisecond), stateOK},
		{"one failure of two", window(2, 1, 100*time.Millisecond), stateOK},
		{"error rate", window(20, 2, 100*time.Millisecond), stateDegraded},
		{"slow", window(5, 0, 4*time.Second), stateDegraded},
		{"slow but too few calls", window(2, 0, 4*time.Second), stateOK},
		{"all failing", window(3, 3, 100*time.Millisecond), stateDown},
	} {
		if got := judgeService(tc.w, 3000); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestQuotaUnits(t *testing.T) {
	month := metricCounts{calls: 10, usage: 7200}
	if meteredUnits(meterSeconds, month) != 2 || meteredUnits(meterChars, month) != 7200 || meteredUnits(meterCalls, month) != 10 {
		t.Fatal("metered units")
	}
	for _, tc := range []struct {
		unit, meter string
		want        float64
	}{
		{"calls", meterSeconds, 10}, {"hours", meterSeconds, 2}, {"chars", meterChars, 7200}, {"chars", meterCalls, 0}, {"hours", meterCalls, 0}, {"cny", meterCalls, 3.5},
	} {
		if got := quotaUsed(tc.unit, tc.meter, month, 3.5); got != tc.want {
			t.Errorf("%s/%s = %v, want %v", tc.unit, tc.meter, got, tc.want)
		}
	}
	if s := wavSeconds(testWAV()); math.Abs(s-4.0/32000) > 1e-12 {
		t.Fatalf("wav seconds = %v", s)
	}
	if wavSeconds([]byte("RIFF")) != 0 {
		t.Fatal("a truncated WAV has a duration")
	}
	if formatLatency(310) != "310ms" || formatLatency(1840) != "1.8s" {
		t.Fatal("latency format")
	}
}

// Calls made for a tagged request are recorded with their outcome and metered usage, never their content; nothing is recorded while the console is disabled.
func TestUpstreamCallsAreMetered(t *testing.T) {
	failing, rejecting := false, false
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if failing {
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/" && r.Method == "GET" {
			if rejecting {
				_, _ = io.WriteString(w, `["FAILED_TO_PARSE_REQUEST_BODY"]`)
				return
			}
			_, _ = io.WriteString(w, `["SUCCESS",[["q",["你好"]]]]`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "messages") {
			if rejecting {
				_, _ = io.WriteString(w, `{"error":{"message":"quota exceeded"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"测试"}}]}`)
			return
		}
		if rejecting {
			_, _ = io.WriteString(w, `{"code":500,"data":""}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":200,"data":"test"}`)
	})
	if w := call(s, "POST", "/v1/translate", `{"text":"测试一下","source_lang":"auto","target_lang":"en"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if rows := s.metrics.pending(); len(rows) != 0 {
		t.Fatalf("recorded while the console is disabled: %+v", rows)
	}
	s.config.Admin.Enabled = true
	if w := call(s, "POST", "/v1/translate", `{"text":"测试一下","source_lang":"auto","target_lang":"en"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(s, "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"secret prompt"}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(s, "GET", "/v1/cloud/candidates?text=nihao", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	failing = true
	if w := call(s, "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"secret prompt"}]}`); w.Code != 502 {
		t.Fatal(w.Code, w.Body)
	}
	// An upstream that answers 200 with an error body failed too, and a failed translation meters no characters.
	failing, rejecting = false, true
	if w := call(s, "POST", "/v1/translate", `{"text":"测试一下","source_lang":"auto","target_lang":"en"}`); w.Code != 502 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(s, "GET", "/v1/cloud/candidates?text=nihao", ""); w.Code != 502 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(s, "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"secret prompt"}]}`); w.Code != 502 {
		t.Fatal(w.Code, w.Body)
	}
	// A request refused before any upstream call is not a call.
	if w := call(s, "POST", "/v1/translate", `{"text":"x","source_lang":"??","target_lang":"en"}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	got := map[string]account.ServiceMetric{}
	for _, row := range s.metrics.pending() {
		got[row.Service] = row
	}
	if got["translation"].Calls != 2 || got["translation"].Usage != 4 || got["translation"].Errors != 1 {
		t.Fatalf("translation = %+v", got["translation"])
	}
	if got["chat"].Calls != 3 || got["chat"].Errors != 2 || got["chat"].Usage != 0 {
		t.Fatalf("chat = %+v", got["chat"])
	}
	if got["cloud"].Calls != 2 || got["cloud"].Errors != 1 || len(got) != 3 {
		t.Fatalf("services = %+v", got)
	}
	encoded, _ := json.Marshal(s.metrics.pending())
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "测试") {
		t.Fatalf("metrics hold content: %s", encoded)
	}
	// A call the client abandoned says nothing about the upstream.
	s.observe("chat", time.Now(), context.Canceled, 0)
	s.observe("chat", time.Now(), errors.Join(errors.New("dial"), context.Canceled), 0)
	if w := s.metrics.recent("chat", time.Now(), 5); w.calls != 3 {
		t.Fatalf("cancelled calls were recorded: %+v", w)
	}
}

// Without admin.services the pages list the configured upstreams under default names; with it, exactly the configured services.
func TestMonitoredServices(t *testing.T) {
	s := fixture(t, nil)
	var keys []string
	for _, svc := range s.monitoredServices() {
		keys = append(keys, svc.Key)
		// Transcribing a whole recording is judged by a longer threshold than the 3s default.
		wantSlow := defaultServiceSlowMS
		if svc.Key == "transcription" {
			wantSlow = 10000
		}
		if svc.Name == "" || svc.Provider != "127.0.0.1" || svc.SlowMS != wantSlow {
			t.Fatalf("derived service = %+v", svc)
		}
	}
	if strings.Join(keys, ",") != "cloud,chat,translation,transcription" {
		t.Fatalf("derived keys = %v", keys)
	}
	s.config.Translation.Provider = "tencent"
	if svc := s.monitoredServices()[2]; svc.Provider != "腾讯 TMT" {
		t.Fatalf("translation provider = %+v", svc)
	}
	s.config.Admin.Services = []AdminServiceConfig{{Key: "chat", Name: "AI 联想", Provider: "账号通道", Quota: AdminServiceQuota{Limit: 2000, Unit: "cny", UnitPrice: 0.05}, SlowMS: 2000}}
	services := s.monitoredServices()
	if len(services) != 1 || services[0] != (monitoredService{Key: "chat", Name: "AI 联想", Provider: "账号通道", SlowMS: 2000, QuotaLimit: 2000, QuotaUnit: "cny", UnitPrice: 0.05}) {
		t.Fatalf("configured services = %+v", services)
	}
}

// /api/cloud needs view_cloud_usage even though it is a read, and both pages are GET only.
func TestStatusAndCloudAccess(t *testing.T) {
	s, _ := adminRBACFixture(t)
	reviewer := strings.Repeat("r", 64)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, adminRequest(s, "GET", "/api/cloud", reviewer, ""))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "permission_denied") {
		t.Fatalf("reviewer cloud: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/cloud", "/api/status"} {
		w = httptest.NewRecorder()
		r := adminRequest(s, "POST", path, "", strings.Repeat("a", 40))
		s.ServeHTTP(w, r)
		if w.Code != 405 {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body)
		}
	}
	if got := s.adminHealth(context.Background()); got != stateUnknown {
		t.Fatalf("health before any probe = %s", got)
	}
}

func monitoringServer(t *testing.T, upstream http.HandlerFunc) *Server {
	t.Helper()
	disposableSchema(t)
	t.Setenv("TEST_AUTH_PEPPER", strings.Repeat("p", 64))
	t.Setenv("TEST_CLIENT_TOKEN", testToken)
	t.Setenv("TEST_ADMIN_TOKEN", strings.Repeat("q", 48))
	t.Setenv("TEST_UPSTREAM_TOKEN", "provider-secret")
	srv := httptest.NewTLSServer(upstream)
	t.Cleanup(srv.Close)
	s, err := New(Config{
		Auth:    account.Config{Enabled: true, DatabaseEnv: "MSIME_TEST_DATABASE_URL", PepperEnv: "TEST_AUTH_PEPPER"},
		Admin:   AdminConfig{Enabled: true, Host: "admin.example.com", TokenEnv: "TEST_ADMIN_TOKEN", Services: []AdminServiceConfig{{Key: "chat", Name: "AI 联想", Provider: "账号通道", Quota: AdminServiceQuota{Limit: 100, Unit: "calls", UnitPrice: 0.5}, SlowMS: 2000}, {Key: "translation", Name: "在线翻译", Provider: "腾讯 TMT"}}},
		Clients: []Client{{ID: "device", TokenEnv: "TEST_CLIENT_TOKEN", RequestsPerMinute: 120}},
		Chat:    Endpoint{URL: srv.URL, TokenEnv: "TEST_UPSTREAM_TOKEN", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.client = srv.Client()
	t.Cleanup(func() {
		s.Close()
		s.CloseAccounts()
	})
	// statusProbeJob runs its first probe at startup; wait for it so the test's probes do not interleave with it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if checked, _ := s.statusStates(); !checked.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the startup probe did not run")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s
}

func adminGet(t *testing.T, s *Server, path string, out any) {
	t.Helper()
	r := httptest.NewRequest("GET", "https://admin.example.com"+path, nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("q", 48))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

type statusResponse struct {
	CheckedAt *time.Time `json:"checked_at"`
	State     string     `json:"state"`
	Services  []struct {
		Key       string   `json:"key"`
		Name      string   `json:"name"`
		Desc      string   `json:"desc"`
		State     string   `json:"state"`
		P95MS     *int     `json:"p95_ms"`
		Uptime60d *float64 `json:"uptime_60d"`
		Days      []struct {
			Day    string   `json:"day"`
			State  string   `json:"state"`
			Uptime *float64 `json:"uptime"`
		} `json:"days"`
	} `json:"services"`
	Incidents []struct {
		ID          int64  `json:"id"`
		Service     string `json:"service"`
		ServiceName string `json:"service_name"`
		Title       string `json:"title"`
		State       string `json:"state"`
		Auto        bool   `json:"auto"`
	} `json:"incidents"`
}

// End to end against PostgreSQL: slow upstream calls degrade the service, three bad probes open one automatic incident, the pages report the minute rollup and the month's quota, and five good probes resolve the incident.
func TestStatusProbeAndPages(t *testing.T) {
	s := monitoringServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"测试"}}]}`)
	})
	ctx := context.Background()
	// One real proxied call, then slow calls recorded directly: the window is degraded by P95.
	if w := call(s, "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	now := time.Now()
	for range 4 {
		s.metrics.record("chat", now, 4*time.Second, false, 0)
	}
	for range 3 {
		s.statusTick(ctx, now)
	}
	if got := s.adminHealth(ctx); got != stateDegraded {
		t.Fatalf("health = %s", got)
	}
	var status statusResponse
	adminGet(t, s, "/api/status", &status)
	if status.CheckedAt == nil || status.State != stateDegraded || len(status.Services) != 3 {
		t.Fatalf("status = %+v", status)
	}
	db, chat, translation := status.Services[0], status.Services[1], status.Services[2]
	if db.Key != databaseService || db.State != stateOK || db.Desc != "PostgreSQL" || db.P95MS == nil {
		t.Fatalf("database = %+v", db)
	}
	if chat.Key != "chat" || chat.Name != "AI 联想" || chat.Desc != "账号通道" || chat.State != stateDegraded || chat.P95MS == nil || *chat.P95MS <= 2000 || len(chat.Days) != statusDays {
		t.Fatalf("chat = %+v", chat)
	}
	todayStrip := chat.Days[statusDays-1]
	if todayStrip.Day != time.Now().UTC().Format(time.DateOnly) || todayStrip.State != stateDegraded || todayStrip.Uptime == nil || *todayStrip.Uptime != 1 || chat.Uptime60d == nil || chat.Days[0].State != "none" {
		t.Fatalf("chat strip today = %+v, uptime %v", todayStrip, chat.Uptime60d)
	}
	if translation.State != stateIdle || translation.P95MS != nil {
		t.Fatalf("idle translation = %+v", translation)
	}
	if len(status.Incidents) != 1 || status.Incidents[0].Title != "AI 联想响应变慢" || status.Incidents[0].ServiceName != "AI 联想" || status.Incidents[0].State != "open" || !status.Incidents[0].Auto {
		t.Fatalf("incidents = %+v", status.Incidents)
	}

	var cloud struct {
		Services []struct {
			Key       string   `json:"key"`
			State     string   `json:"state"`
			Calls     int64    `json:"calls_24h"`
			ErrorRate *float64 `json:"error_rate"`
			P95MS     *int     `json:"p95_ms"`
			Hourly    []struct {
				Hour  time.Time `json:"hour"`
				Calls int64     `json:"calls"`
			} `json:"hourly"`
			Month struct {
				Calls int64  `json:"calls"`
				Meter string `json:"meter"`
			} `json:"month"`
			Quota *struct {
				Limit float64 `json:"limit"`
				Unit  string  `json:"unit"`
				Used  float64 `json:"used"`
				Pct   float64 `json:"pct"`
			} `json:"quota"`
			CostCNY *float64 `json:"cost_cny"`
		} `json:"services"`
	}
	// One more call stays in memory until the next flush; the page includes it.
	s.metrics.record("chat", time.Now(), time.Second, true, 0)
	adminGet(t, s, "/api/cloud", &cloud)
	if len(cloud.Services) != 2 {
		t.Fatalf("cloud = %+v", cloud)
	}
	c := cloud.Services[0]
	if c.Key != "chat" || c.State != stateDegraded || c.Calls != 6 || c.ErrorRate == nil || math.Abs(*c.ErrorRate-1.0/6) > 1e-9 || c.P95MS == nil || len(c.Hourly) != cloudHours || c.Hourly[cloudHours-1].Calls != 6 {
		t.Fatalf("chat card = %+v", c)
	}
	if c.Month.Calls != 6 || c.Month.Meter != meterCalls || c.Quota == nil || c.Quota.Used != 6 || c.Quota.Pct != 6 || c.Quota.Unit != "calls" || c.CostCNY == nil || *c.CostCNY != 3 {
		t.Fatalf("chat month = %+v quota %+v cost %v", c.Month, c.Quota, c.CostCNY)
	}
	if tr := cloud.Services[1]; tr.Calls != 0 || tr.ErrorRate != nil || tr.Quota != nil || tr.CostCNY != nil || tr.Month.Meter != meterChars {
		t.Fatalf("idle translation card = %+v", tr)
	}

	// Recovery: the window is empty from ten minutes on, and the fifth good probe resolves the incident.
	later := now.Add(10 * time.Minute)
	for i := range incidentResolveAfter {
		s.statusTick(ctx, later)
		incidents, err := s.accounts.Incidents(ctx, 5)
		if err != nil {
			t.Fatal(err)
		}
		if resolved := incidents[0].State == "resolved"; resolved != (i == incidentResolveAfter-1) {
			t.Fatalf("after %d good probes the incident is %s", i+1, incidents[0].State)
		}
	}
	if got := s.adminHealth(ctx); got != stateOK {
		t.Fatalf("health after recovery = %s", got)
	}
}

// Tencent answers its errors with HTTP 200: the call counts as failed and meters no characters, while a good batch meters only the characters it sent.
func TestTencentCallsAreMetered(t *testing.T) {
	reply := `{"Response":{"Error":{"Code":"FailedOperation.NoFreeAmount","Message":"quota"}}}`
	s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, reply)
	})
	s.config.Admin.Enabled = true
	s.config.Translation.Provider = "tencent"
	s.config.Translation.secretID = "test-id"
	s.config.Translation.Region = "ap-guangzhou"
	if w := call(s, "POST", "/v1/translate", `{"text":"测试","source_lang":"auto","target_lang":"en"}`); w.Code != 502 {
		t.Fatal(w.Code, w.Body)
	}
	reply = `{"Response":{"TargetTextList":["test"]}}`
	if w := call(s, "POST", "/v1/translate", `{"text":"测试","source_lang":"auto","target_lang":"en"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	rows := s.metrics.pending()
	if len(rows) != 1 || rows[0].Service != "translation" || rows[0].Calls != 2 || rows[0].Errors != 1 || rows[0].Usage != 2 {
		t.Fatalf("translation = %+v", rows)
	}
}

// A streaming session is failed only when the upstream side broke: a client that drops its connection without a close frame is not an upstream failure.
func TestStreamingSessionsAreMetered(t *testing.T) {
	for _, tc := range []struct {
		name         string
		upstreamDrop bool
	}{{"client drops", false}, {"upstream drops", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer c.CloseNow()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				kind, data, err := c.Read(ctx)
				if err != nil || tc.upstreamDrop {
					return
				}
				_ = c.Write(ctx, kind, data)
				_, _, _ = c.Read(ctx)
			})
			s.config.Admin.Enabled = true
			s.config.Streaming = StreamingEndpoint{URL: strings.Replace(s.config.Cloud.URL, "https:", "wss:", 1), token: "provider-secret", ResourceID: "synthetic-resource", MaxSeconds: 5}
			live := httptest.NewTLSServer(s)
			t.Cleanup(func() { s.Close(); live.Close() })
			c := dialStream(t, live)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := c.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
				t.Fatal(err)
			}
			if tc.upstreamDrop {
				if _, _, err := c.Read(ctx); err == nil {
					t.Fatal("stream stayed open after the upstream dropped")
				}
			} else {
				if _, _, err := c.Read(ctx); err != nil {
					t.Fatal(err)
				}
				_ = c.CloseNow()
			}
			deadline := time.Now().Add(3 * time.Second)
			var window metricCounts
			for time.Now().Before(deadline) {
				if window = s.metrics.recent("streaming", time.Now(), 5); window.calls > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			wantErrors := int64(0)
			if tc.upstreamDrop {
				wantErrors = 1
			}
			if window.calls != 1 || window.errors != wantErrors {
				t.Fatalf("streaming = %+v", window)
			}
		})
	}
}
