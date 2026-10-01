package account

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"
)

// GitHub Release download snapshots (unit U7 writes them; unit U6 reads them for the downloads summary).

// ReleaseAssetSnapshot is one asset's cumulative GitHub download_count on one UTC day.
type ReleaseAssetSnapshot struct {
	Repo          string    `json:"repo"`
	Tag           string    `json:"tag"`
	Asset         string    `json:"asset"`
	Day           time.Time `json:"day"`
	DownloadCount int64     `json:"download_count"`
}

// errInvalidAssetSnapshot rejects a snapshot that could not have come from GitHub; the whole batch is refused so a bad caller never records half a day.
var errInvalidAssetSnapshot = errors.New("invalid release asset snapshot")

type snapshotKey struct {
	repo, tag, asset string
	day              time.Time
}

// RecordReleaseAssetSnapshots upserts the day's snapshots.
func (a *Service) RecordReleaseAssetSnapshots(ctx context.Context, snapshots []ReleaseAssetSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	// One statement cannot update the same row twice, so a later duplicate of a key replaces the earlier one before the rows reach PostgreSQL.
	index := map[snapshotKey]int{}
	var repos, tags, assets []string
	var days []time.Time
	var counts []int64
	for _, s := range snapshots {
		if !snapshotText(s.Repo) || !snapshotText(s.Tag) || !snapshotText(s.Asset) || s.DownloadCount < 0 || s.Day.IsZero() {
			return errInvalidAssetSnapshot
		}
		day := s.Day.UTC().Truncate(24 * time.Hour)
		key := snapshotKey{s.Repo, s.Tag, s.Asset, day}
		if i, ok := index[key]; ok {
			counts[i] = s.DownloadCount
			continue
		}
		index[key] = len(repos)
		repos = append(repos, s.Repo)
		tags = append(tags, s.Tag)
		assets = append(assets, s.Asset)
		days = append(days, day)
		counts = append(counts, s.DownloadCount)
	}
	_, err := a.store.pool.Exec(ctx, `INSERT INTO release_asset_snapshots(repo,tag,asset,day,download_count)
SELECT * FROM unnest($1::text[],$2::text[],$3::text[],$4::date[],$5::bigint[])
ON CONFLICT (repo,tag,asset,day) DO UPDATE SET download_count=EXCLUDED.download_count`, repos, tags, assets, days, counts)
	return err
}

func snapshotText(s string) bool {
	return s != "" && len(s) <= 512 && utf8.ValidString(s)
}
