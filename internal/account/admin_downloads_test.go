package account

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The telemetry endpoint accepts the device activity kinds and the optional download dimensions, stores omitted dimensions as NULL and keeps the first protocol working.
func TestTelemetryDeviceActivityAndDownloadDimensions(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `TRUNCATE admin_events`); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(a.Telemetry)
	install := "install-0123456789abcdef"
	for _, body := range []string{
		`{"id":"dimension-download-01","kind":"download","platform":"windows","version":"v0.5.4","artifact":"x64 安装包","channel":"cn-mirror","install_id":"` + install + `"}`,
		`{"id":"dimension-download-02","kind":"download","platform":"windows","version":"v0.5.4","artifact":"","channel":""}`,
		`{"id":"dimension-active-0001","kind":"active","platform":"macos","version":"0.50.0","install_id":"` + install + `"}`,
		`{"id":"dimension-session-001","kind":"session","platform":"macos","version":"0.50.0"}`,
		`{"id":"dimension-session-002","kind":"session_crash","platform":"macos","version":"0.50.0","install_id":"` + install + `"}`,
		`{"id":"dimension-crash-00001","kind":"crash","platform":"macos","version":"0.50.0","message":"  boom\nsecond line","stack":"frame","channel":"github"}`,
	} {
		apiRequest(t, handler, "POST", "/v1/telemetry/events", body, "", 202)
	}
	for _, body := range []string{
		`{"id":"dimension-invalid-01","kind":"active","platform":"macos","version":"1"}`,
		`{"id":"dimension-invalid-02","kind":"session","platform":"macos","version":"1","message":"no"}`,
		`{"id":"dimension-invalid-03","kind":"session_crash","platform":"macos","version":"1","stack":"no"}`,
		`{"id":"dimension-invalid-04","kind":"download","platform":"macos","version":"1","channel":"国内镜像"}`,
		`{"id":"dimension-invalid-05","kind":"download","platform":"macos","version":"1","channel":"-mirror"}`,
		`{"id":"dimension-invalid-06","kind":"download","platform":"macos","version":"1","artifact":"a\nb"}`,
		`{"id":"dimension-invalid-07","kind":"download","platform":"macos","version":"1","artifact":"   "}`,
		`{"id":"dimension-invalid-08","kind":"active","platform":"macos","version":"1","install_id":"` + strings.Repeat("a", 65) + `"}`,
		`{"id":"dimension-invalid-09","kind":"active","platform":"macos","version":"1","install_id":"user@example.test-123"}`,
	} {
		apiRequest(t, handler, "POST", "/v1/telemetry/events", body, "", 400)
	}
	type stored struct {
		kind                         string
		artifact, channel, installID *string
	}
	got := map[string]stored{}
	rows, err := db.pool.Query(ctx, `SELECT id,kind,artifact,channel,install_id FROM admin_events`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var s stored
		if err := rows.Scan(&id, &s.kind, &s.artifact, &s.channel, &s.installID); err != nil {
			t.Fatal(err)
		}
		got[id] = s
	}
	rows.Close()
	if len(got) != 6 {
		t.Fatal("invalid events stored or valid events lost", got)
	}
	full := got["dimension-download-01"]
	if full.artifact == nil || *full.artifact != "x64 安装包" || full.channel == nil || *full.channel != "cn-mirror" || full.installID == nil || *full.installID != install {
		t.Fatal("download dimensions not stored", full)
	}
	if empty := got["dimension-download-02"]; empty.artifact != nil || empty.channel != nil || empty.installID != nil {
		t.Fatal("omitted dimensions not NULL", empty)
	}
	if got["dimension-active-0001"].kind != "active" || got["dimension-session-002"].kind != "session_crash" {
		t.Fatal("activity kinds not stored", got)
	}
	// The crash group is written by the crash unit's signature function; whatever it returns is what the event carries.
	var signature *string
	if err := db.pool.QueryRow(ctx, `SELECT signature FROM admin_events WHERE id='dimension-crash-00001'`).Scan(&signature); err != nil {
		t.Fatal(err)
	}
	want := crashSignature("macos", "  boom\nsecond line", "frame")
	if (want == "") != (signature == nil) || (signature != nil && *signature != want) {
		t.Fatal("crash signature not stored", signature, want)
	}
	if want != "" {
		var title string
		if err := db.pool.QueryRow(ctx, `SELECT title FROM admin_crash_groups WHERE signature=$1`, want).Scan(&title); err != nil || title != "boom" {
			t.Fatal("crash group not upserted in the event transaction", title, err)
		}
	}
}

func TestCrashGroupTitle(t *testing.T) {
	for in, want := range map[string]string{
		"boom":                              "boom",
		"  first line  \r\n second":         "first line",
		"\n\nnull pointer\nat main":         "null pointer",
		strings.Repeat("崩", 250):            strings.Repeat("崩", 200),
		strings.Repeat("a", 199) + "  tail": strings.Repeat("a", 199),
	} {
		if got := crashGroupTitle(in); got != want {
			t.Errorf("crashGroupTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReleaseTagPlatform(t *testing.T) {
	for _, tc := range []struct{ tag, asset, platform, version string }{
		{"windows-v0.5.4", "msime-windows-x64-setup.exe", "windows", "v0.5.4"},
		{"macos-v0.50.0", "MSIME-0.50.0.dmg", "macos", "v0.50.0"},
		{"Linux-0.1.0", "msime_0.1.0_amd64.deb", "linux", "0.1.0"},
		{"v1.2.3", "msime-1.2.3.AppImage", "linux", "v1.2.3"},
		{"v1.2.3", "msime-android-1.2.3.apk", "android", "v1.2.3"},
		{"nightly", "source.tar.gz", "", "nightly"},
		{"HarmonyOS-v1.0.0", "msime.hap", "harmony", "v1.0.0"},
		{"msime-v1.2.3", "msime-1.2.3-setup.exe", "windows", "msime-v1.2.3"},
		{"release-2026", "notes.pdf", "", "release-2026"},
	} {
		platform, version := releaseTagPlatform(tc.tag, tc.asset)
		if platform != tc.platform || version != tc.version {
			t.Errorf("releaseTagPlatform(%q,%q) = %q,%q", tc.tag, tc.asset, platform, version)
		}
	}
	for asset, metadata := range map[string]bool{"SHA256SUMS.txt": true, "latest.yml": true, "msime.exe.sig": true, "msime.exe.blockmap": true, "msime.exe": false, "msime.dmg": false} {
		if releaseMetadataAsset(asset) != metadata {
			t.Errorf("releaseMetadataAsset(%q) != %v", asset, metadata)
		}
	}
}

// The summary groups the week's download telemetry, adds GitHub Release downloads as differences between adjacent snapshots, and computes the domestic mirror share over both.
func TestDownloadsSummary(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `TRUNCATE admin_events,release_asset_snapshots`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	empty, err := a.downloadsSummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Day != "2026-09-30" || len(empty.Rows) != 0 || empty.Totals != (downloadsTotals{}) || empty.MirrorShare != nil || empty.ChannelReported || empty.SnapshotDay != nil || empty.Truncated {
		t.Fatal("empty summary", empty)
	}
	seed := func(id, kind, platform, version string, artifact, channel any, at time.Time) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, `INSERT INTO admin_events(id,kind,platform,version,artifact,channel,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, kind, platform, version, artifact, channel, at); err != nil {
			t.Fatal(err)
		}
	}
	today := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	// Mirror x64: 2 today, 1 three days ago; one older than the week and one tomorrow are outside the window.
	seed("summary-mirror-today-1", "download", "windows", "v0.5.4", "x64 安装包", "cn-mirror", today.Add(time.Hour))
	seed("summary-mirror-today-2", "download", "windows", "v0.5.4", "x64 安装包", "cn-mirror", today.Add(14*time.Hour))
	seed("summary-mirror-earlier", "download", "windows", "v0.5.4", "x64 安装包", "cn-mirror", today.AddDate(0, 0, -6))
	seed("summary-mirror-too-old", "download", "windows", "v0.5.4", "x64 安装包", "cn-mirror", today.AddDate(0, 0, -7).Add(23*time.Hour))
	seed("summary-mirror-tomorrow", "download", "windows", "v0.5.4", "x64 安装包", "cn-mirror", today.AddDate(0, 0, 1))
	// A legacy download without dimensions forms its own group.
	seed("summary-legacy-download", "download", "android", "0.1.0", nil, nil, today.AddDate(0, 0, -2))
	// Other kinds never count as downloads.
	seed("summary-active-event-01", "active", "windows", "v0.5.4", nil, nil, today)

	// GitHub snapshots: x64 has a baseline before the window, a counter reset is clamped at zero, metadata files and assets without new downloads are left out, and an asset's first snapshot counts nothing.
	snap := func(repo, tag, asset string, day time.Time, count int64) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, `INSERT INTO release_asset_snapshots(repo,tag,asset,day,download_count) VALUES($1,$2,$3,$4,$5)`, repo, tag, asset, day, count); err != nil {
			t.Fatal(err)
		}
	}
	repo := "metasequoiaime/msime-windows"
	snap(repo, "windows-v0.5.4", "msime-windows-x64-setup.exe", today.AddDate(0, 0, -10), 100)
	snap(repo, "windows-v0.5.4", "msime-windows-x64-setup.exe", today.AddDate(0, 0, -3), 130)
	snap(repo, "windows-v0.5.4", "msime-windows-x64-setup.exe", today.AddDate(0, 0, -1), 150)
	snap(repo, "windows-v0.5.4", "msime-windows-x64-setup.exe", today, 157)
	snap(repo, "windows-v0.5.4", "msime-windows-arm64-setup.exe", today.AddDate(0, 0, -2), 50)
	snap(repo, "windows-v0.5.4", "msime-windows-arm64-setup.exe", today.AddDate(0, 0, -1), 40)
	snap(repo, "windows-v0.5.4", "msime-windows-arm64-setup.exe", today, 45)
	snap(repo, "windows-v0.5.4", "latest.yml", today.AddDate(0, 0, -1), 1000)
	snap(repo, "windows-v0.5.4", "latest.yml", today, 5000)
	snap(repo, "windows-v0.5.3", "msime-windows-x64-setup.exe", today.AddDate(0, 0, -1), 900)
	snap(repo, "windows-v0.5.3", "msime-windows-x64-setup.exe", today, 900)
	snap("metasequoiaime/msime", "macos-v0.50.0", "MSIME-0.50.0.dmg", today, 1086)

	summary, err := a.downloadsSummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	var got []string
	for _, r := range summary.Rows {
		got = append(got, strings.Join([]string{r.Source, r.Platform, r.Version, str(r.Artifact), str(r.Channel), r.Repo, r.Tag}, "|")+"|"+strconv.FormatInt(r.Today, 10)+"/"+strconv.FormatInt(r.Week, 10))
	}
	want := []string{
		"github_release|windows|v0.5.4|msime-windows-x64-setup.exe|github|metasequoiaime/msime-windows|windows-v0.5.4|7/57",
		"github_release|windows|v0.5.4|msime-windows-arm64-setup.exe|github|metasequoiaime/msime-windows|windows-v0.5.4|5/5",
		"telemetry|windows|v0.5.4|x64 安装包|cn-mirror|||2/3",
		"telemetry|android|0.1.0|<nil>|<nil>|||0/1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if summary.Totals != (downloadsTotals{Today: 14, Week: 66, GitHubToday: 12, GitHubWeek: 62, MirrorWeek: 3}) {
		t.Fatal("totals", summary.Totals)
	}
	if !summary.ChannelReported || summary.MirrorShare == nil || math.Abs(*summary.MirrorShare-3.0/66) > 1e-9 {
		t.Fatal("mirror share", summary.ChannelReported, summary.MirrorShare)
	}
	if summary.SnapshotDay == nil || *summary.SnapshotDay != "2026-09-30" || summary.Truncated {
		t.Fatal("snapshot day or truncation", summary.SnapshotDay, summary.Truncated)
	}

	// Without any reported channel the share is unknown, not zero.
	if _, err := db.pool.Exec(ctx, `UPDATE admin_events SET channel=NULL`); err != nil {
		t.Fatal(err)
	}
	if summary, err = a.downloadsSummary(ctx, now); err != nil || summary.ChannelReported || summary.MirrorShare != nil || summary.Totals.MirrorWeek != 0 {
		t.Fatal("unreported channel share", summary.ChannelReported, summary.MirrorShare, err)
	}
}

// The summary route answers every console role, and the event list exposes and filters by the new dimensions.
func TestDownloadsSummaryRouteAndList(t *testing.T) {
	db := testStore(t)
	a := &Service{store: db}
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `TRUNCATE admin_events,release_asset_snapshots`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO admin_events(id,kind,platform,version,artifact,channel) VALUES('route-download-0001','download','windows','v0.5.4','x64 安装包','cn-mirror'),('route-download-0002','download','windows','v0.5.4',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	readonly := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.AdminHTTP(w, r.WithContext(WithAdminAccess(r.Context(), AdminAccess{Actor: "pat:viewer@example.test", Email: "viewer@example.test", Role: "readonly", Permissions: []string{PermViewCloudUsage}})))
	})
	w := apiRequest(t, readonly, "GET", "/api/downloads/summary", "", "", 200)
	var summary struct {
		Day    string `json:"day"`
		Rows   []map[string]any
		Totals map[string]int64 `json:"totals"`
		Share  *float64         `json:"mirror_share"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Day != time.Now().UTC().Format(time.DateOnly) || len(summary.Rows) != 2 || summary.Totals["week"] != 2 || summary.Share == nil || *summary.Share != 0.5 {
		t.Fatal("summary over HTTP", w.Body.String())
	}
	apiRequest(t, readonly, "POST", "/api/downloads/summary", `{}`, "", 405)

	w = apiRequest(t, readonly, "GET", "/api/downloads?channel=cn-mirror", "", "", 200)
	var list struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Total != 1 || list.Items[0]["artifact"] != "x64 安装包" || list.Items[0]["channel"] != "cn-mirror" {
		t.Fatal("channel filter", w.Body.String(), err)
	}
	w = apiRequest(t, readonly, "GET", "/api/downloads?artifact="+strings.ReplaceAll("x64 安装包", " ", "%20"), "", "", 200)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Total != 1 {
		t.Fatal("artifact filter", w.Body.String(), err)
	}
	apiRequest(t, readonly, "GET", "/api/downloads?channel="+strings.Repeat("x", 33), "", "", 400)
	apiRequest(t, readonly, "GET", "/api/users?channel=cn-mirror", "", "", 400)
}
