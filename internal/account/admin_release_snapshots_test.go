package account

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// Snapshots upsert by (repo, tag, asset, UTC day): a later count for the same day replaces the earlier one, another day adds a row, and an invalid batch writes nothing.
func TestRecordReleaseAssetSnapshots(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `TRUNCATE release_asset_snapshots`); err != nil {
		t.Fatal(err)
	}
	a := &Service{store: db}
	if err := a.RecordReleaseAssetSnapshots(ctx, nil); err != nil {
		t.Fatal(err)
	}
	tokyo := time.FixedZone("JST", 9*3600)
	// 2026-10-02 01:00 in Tokyo is still 2026-10-01 in UTC.
	day := time.Date(2026, 10, 2, 1, 0, 0, 0, tokyo)
	snap := func(asset string, day time.Time, count int64) ReleaseAssetSnapshot {
		return ReleaseAssetSnapshot{Repo: "metasequoiaime/msime-windows", Tag: "windows-v0.5.4", Asset: asset, Day: day, DownloadCount: count}
	}
	if err := a.RecordReleaseAssetSnapshots(ctx, []ReleaseAssetSnapshot{snap("x64.exe", day, 10), snap("arm64.exe", day, 3), snap("x64.exe", day, 12)}); err != nil {
		t.Fatal(err)
	}
	if err := a.RecordReleaseAssetSnapshots(ctx, []ReleaseAssetSnapshot{snap("x64.exe", day.Add(2*time.Hour), 15), snap("x64.exe", day.Add(24*time.Hour), 20)}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]ReleaseAssetSnapshot{
		{snap("ok.exe", day, 1), snap("", day, 1)},
		{snap("neg.exe", day, -1)},
		{snap("noday.exe", time.Time{}, 1)},
	} {
		if err := a.RecordReleaseAssetSnapshots(ctx, bad); !errors.Is(err, errInvalidAssetSnapshot) {
			t.Fatal(bad, err)
		}
	}
	rows, err := db.pool.Query(ctx, `SELECT asset, to_char(day,'YYYY-MM-DD'), download_count FROM release_asset_snapshots ORDER BY asset, day`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var asset, d string
		var n int64
		if err = rows.Scan(&asset, &d, &n); err != nil {
			t.Fatal(err)
		}
		got = append(got, asset+"@"+d+"="+strconv.FormatInt(n, 10))
	}
	want := []string{"arm64.exe@2026-10-01=3", "x64.exe@2026-10-01=15", "x64.exe@2026-10-02=20"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatal(got)
		}
	}
}
