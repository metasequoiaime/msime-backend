package account

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

type overviewBody struct {
	Downloads        int   `json:"downloads"`
	Downloads30d     int   `json:"downloads_30d"`
	DownloadsPrev30d int   `json:"downloads_prev_30d"`
	RangeDays        int   `json:"range_days"`
	Daily            []any `json:"daily"`
	Telemetry        struct {
		Active   bool `json:"active"`
		Sessions bool `json:"sessions"`
	} `json:"telemetry"`
	ActiveDevicesDaily []struct {
		Day      string `json:"day"`
		Windows  int    `json:"windows"`
		MacLinux int    `json:"mac_linux"`
		Mobile   int    `json:"mobile"`
	} `json:"active_devices_daily"`
	ActiveDevices7d     int               `json:"active_devices_7d"`
	ActiveDevicesPrev7d int               `json:"active_devices_prev_7d"`
	PlatformActive7d    map[string]int    `json:"platform_active_7d"`
	CrashFreeRate       *float64          `json:"crash_free_rate"`
	CrashFreeRatePrev   *float64          `json:"crash_free_rate_prev"`
	Pending             map[string]int    `json:"pending"`
	ServicesConfigured  bool              `json:"services_configured"`
	Services            []overviewService `json:"services"`
}

type overviewService struct {
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	Provider  string   `json:"provider"`
	State     string   `json:"state"`
	Uptime60d *float64 `json:"uptime_60d"`
	P95MS     *int     `json:"p95_ms"`
}

func overviewTestService(t *testing.T) *Service {
	t.Helper()
	db := testStore(t)
	if _, err := db.pool.Exec(context.Background(), `TRUNCATE admin_events,admin_crash_groups,admin_service_daily,admin_incidents CASCADE`); err != nil {
		t.Fatal(err)
	}
	return &Service{store: db}
}

func getOverview(t *testing.T, a *Service, query string) overviewBody {
	t.Helper()
	w := httptest.NewRecorder()
	a.AdminHTTP(w, adminJSONRequest("GET", "/api/overview"+query, ""))
	var body overviewBody
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	return body
}

func TestOverviewWithoutClientTelemetry(t *testing.T) {
	a := overviewTestService(t)
	body := getOverview(t, a, "?days=7")
	if body.Telemetry.Active || body.Telemetry.Sessions || body.CrashFreeRate != nil || body.ActiveDevices7d != 0 || len(body.PlatformActive7d) != 0 {
		t.Fatalf("telemetry without events %+v", body)
	}
	if len(body.ActiveDevicesDaily) != 7 || len(body.Daily) != 7 || body.RangeDays != 7 {
		t.Fatalf("series length %+v", body)
	}
	for _, day := range body.ActiveDevicesDaily {
		if day.Windows+day.MacLinux+day.Mobile != 0 || len(day.Day) != 10 {
			t.Fatalf("empty day %+v", day)
		}
	}
	if body.ServicesConfigured || len(body.Services) != 0 || body.Pending["community"] != 0 || body.Pending["crash_groups"] != 0 {
		t.Fatalf("services/pending %+v", body)
	}
}

func TestOverviewTelemetryAndServices(t *testing.T) {
	a := overviewTestService(t)
	ctx := context.Background()
	a.ConfigureAdmin(AdminSettings{Services: []AdminService{{Key: "cloud", Name: "云候选代理", Provider: "腾讯云"}, {Key: "chat", Name: "AI 联想", Provider: "OpenAI"}, {Key: "translation", Name: "翻译"}}})
	if _, err := a.store.pool.Exec(ctx, `INSERT INTO admin_events(id,kind,platform,version,install_id,created_at) VALUES
 ('a1','active','Windows','1','device-windows-0001',now()),
 ('a2','active','windows','1','device-windows-0001',now()-interval '1 hour'),
 ('a3','active','windows','1','device-windows-0002',now()),
 ('a4','active','macos','1','device-macos-00001',now()),
 ('a5','active','linux','1','device-linux-00001',now()),
 ('a6','active','ios','1','device-ios-0000001',now()),
 ('a7','active','harmonyos','1','device-harmony-001',now()-interval '2 days'),
 ('a8','active','android','1','device-android-001',now()-interval '10 days'),
 ('s1','session','ios','1',NULL,now()),('s2','session','ios','1',NULL,now()),('s3','session','ios','1',NULL,now()),('s4','session_crash','ios','1',NULL,now()),
 ('s5','session','ios','1',NULL,now()-interval '8 days'),
 ('d1','download','windows','1',NULL,now()),('d2','download','windows','1',NULL,now()-interval '40 days'),('d3','download','windows','1',NULL,now()-interval '45 days');
INSERT INTO admin_crash_groups(signature,platform,version,title,first_seen) VALUES('0123456789abcdef','ios','1','new',now()),('0123456789abcde0','ios','1','old',now()-interval '30 days');
INSERT INTO admin_crash_groups(signature,platform,version,title,status) VALUES('0123456789abcde1','ios','1','known','known');
INSERT INTO admin_service_daily(service,day,ok_minutes,total_minutes,degraded,p95_ms) VALUES
 ('cloud',current_date,600,600,false,42),('cloud',current_date-1,1380,1440,false,40),
 ('chat',current_date,500,600,true,1800),
 ('stale',current_date,10,10,false,1);
INSERT INTO admin_incidents(service,title) VALUES('translation','翻译超时')`); err != nil {
		t.Fatal(err)
	}
	body := getOverview(t, a, "")
	if !body.Telemetry.Active || !body.Telemetry.Sessions || body.RangeDays != 30 || len(body.ActiveDevicesDaily) != 30 {
		t.Fatalf("flags %+v", body)
	}
	today := body.ActiveDevicesDaily[29]
	if today.Windows != 2 || today.MacLinux != 2 || today.Mobile != 1 {
		t.Fatalf("today %+v", today)
	}
	if body.ActiveDevices7d != 6 || body.ActiveDevicesPrev7d != 1 {
		t.Fatalf("7d %d prev %d", body.ActiveDevices7d, body.ActiveDevicesPrev7d)
	}
	wantPlatforms := map[string]int{"windows": 2, "macos": 1, "linux": 1, "ios": 1, "harmonyos": 1}
	if len(body.PlatformActive7d) != len(wantPlatforms) {
		t.Fatalf("platforms %+v", body.PlatformActive7d)
	}
	for platform, n := range wantPlatforms {
		if body.PlatformActive7d[platform] != n {
			t.Fatalf("platforms %+v", body.PlatformActive7d)
		}
	}
	if body.CrashFreeRate == nil || *body.CrashFreeRate != 0.75 || body.CrashFreeRatePrev == nil || *body.CrashFreeRatePrev != 1 {
		t.Fatalf("crash free %v %v", body.CrashFreeRate, body.CrashFreeRatePrev)
	}
	if body.Downloads != 3 || body.Downloads30d != 1 || body.DownloadsPrev30d != 2 {
		t.Fatalf("downloads %+v", body)
	}
	if body.Pending["crash_groups"] != 1 {
		t.Fatalf("pending %+v", body.Pending)
	}
	if !body.ServicesConfigured || len(body.Services) != 3 {
		t.Fatalf("services %+v", body.Services)
	}
	cloud, chat, translation := body.Services[0], body.Services[1], body.Services[2]
	if cloud.Key != "cloud" || cloud.Name != "云候选代理" || cloud.Provider != "腾讯云" || cloud.State != "ok" || cloud.P95MS == nil || *cloud.P95MS != 42 || cloud.Uptime60d == nil || *cloud.Uptime60d != 97.06 {
		t.Fatalf("cloud %+v", cloud)
	}
	if chat.State != "degraded" || translation.State != "degraded" || translation.Uptime60d != nil || translation.P95MS != nil {
		t.Fatalf("chat %+v translation %+v", chat, translation)
	}
	// Without configured services the overview lists the services the status probe recorded.
	a.ConfigureAdmin(AdminSettings{})
	derived := getOverview(t, a, "?days=7")
	if derived.ServicesConfigured || len(derived.Services) != 3 || derived.Services[0].Key != "chat" || derived.Services[2].Key != "stale" || derived.Services[2].Name != "stale" {
		t.Fatalf("derived %+v", derived.Services)
	}
}
