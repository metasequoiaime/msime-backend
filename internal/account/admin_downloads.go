package account

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Downloads page (unit U6): the download event list and the grouped summary. The telemetry endpoint itself is in admin_telemetry.go.

// downloadsList serves GET /api/downloads.
var downloadsList = adminList{
	query: `SELECT id,platform,version,artifact,channel,created_at FROM admin_events WHERE kind='download'`,
	filters: []listFilter{
		{param: "platform", field: "platform", max: 32},
		{param: "version", field: "version", max: 64},
		{param: "artifact", field: "artifact", max: 64},
		{param: "channel", field: "channel", max: 32},
	},
}

const (
	// downloadsMirrorChannel is the telemetry channel key of the domestic website mirror, the numerator of the mirror share.
	downloadsMirrorChannel = "cn-mirror"
	// downloadsSourceTelemetry rows count download events reported through POST /v1/telemetry/events.
	downloadsSourceTelemetry = "telemetry"
	// downloadsSourceGitHub rows count GitHub Release asset downloads, as differences between adjacent daily snapshots.
	downloadsSourceGitHub = "github_release"
	// downloadsRowLimit caps each source's groups; telemetry dimensions are client text, so the number of groups is not otherwise bounded.
	downloadsRowLimit = 500
)

// downloadsRow is one group of the downloads summary: today's and the last seven days' downloads of one platform, version, artifact and channel.
type downloadsRow struct {
	Source   string  `json:"source"`
	Platform string  `json:"platform"`
	Version  string  `json:"version"`
	Artifact *string `json:"artifact"`
	Channel  *string `json:"channel"`
	Repo     string  `json:"repo,omitempty"`
	Tag      string  `json:"tag,omitempty"`
	Today    int64   `json:"today"`
	Week     int64   `json:"week"`
}

// downloadsTotals sums every download of the week, including groups beyond the row cap; the GitHub fields are the GitHub Release part of the sums and MirrorWeek is the domestic mirror's part of Week.
type downloadsTotals struct {
	Today       int64 `json:"today"`
	Week        int64 `json:"week"`
	GitHubToday int64 `json:"github_today"`
	GitHubWeek  int64 `json:"github_week"`
	MirrorWeek  int64 `json:"mirror_week"`
}

// downloadsSummary is the body of GET /api/downloads/summary. Days are UTC calendar days, like the release snapshots; the week is the seven days ending with Day. MirrorShare is null while no download event in the week names a channel, because a zero share would then be a guess.
type downloadsSummary struct {
	Day             string          `json:"day"`
	Rows            []downloadsRow  `json:"rows"`
	Totals          downloadsTotals `json:"totals"`
	MirrorShare     *float64        `json:"mirror_share"`
	ChannelReported bool            `json:"channel_reported"`
	SnapshotDay     *string         `json:"snapshot_day"`
	Truncated       bool            `json:"truncated"`
}

// adminDownloadsSummary serves GET /api/downloads/summary.
func (a *Service) adminDownloadsSummary(w http.ResponseWriter, r *http.Request, _ string) {
	summary, err := a.downloadsSummary(r.Context(), time.Now())
	if err != nil {
		a.error(w, err)
		return
	}
	write(w, 200, summary)
}

// downloadsSummary groups the download telemetry of the UTC week ending on now's day and adds the GitHub Release channel from the asset snapshots.
func (a *Service) downloadsSummary(ctx context.Context, now time.Time) (downloadsSummary, error) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekStart := today.AddDate(0, 0, -6)
	summary := downloadsSummary{Day: today.Format(time.DateOnly), Rows: []downloadsRow{}}
	// One snapshot for every query, so the rows, the totals and the snapshot day agree with each other.
	tx, err := a.store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return summary, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `SELECT platform,version,artifact,channel,count(*) FILTER (WHERE created_at>=$1),count(*)
 FROM admin_events WHERE kind='download' AND created_at>=$2 AND created_at<$3
 GROUP BY platform,version,artifact,channel
 ORDER BY count(*) DESC,platform,version,artifact NULLS LAST,channel NULLS LAST LIMIT $4`, today, weekStart, today.AddDate(0, 0, 1), downloadsRowLimit+1)
	if err != nil {
		return summary, err
	}
	for rows.Next() {
		row := downloadsRow{Source: downloadsSourceTelemetry}
		if err = rows.Scan(&row.Platform, &row.Version, &row.Artifact, &row.Channel, &row.Today, &row.Week); err != nil {
			rows.Close()
			return summary, err
		}
		summary.Rows = append(summary.Rows, row)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return summary, err
	}
	if len(summary.Rows) > downloadsRowLimit {
		summary.Rows, summary.Truncated = summary.Rows[:downloadsRowLimit], true
	}
	// Totals and the channel flag aggregate every event of the week, not only the capped groups.
	var telemetryToday, telemetryWeek int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE created_at>=$1),count(*),count(*) FILTER (WHERE channel=$4),COALESCE(bool_or(channel IS NOT NULL),false)
 FROM admin_events WHERE kind='download' AND created_at>=$2 AND created_at<$3`, today, weekStart, today.AddDate(0, 0, 1), downloadsMirrorChannel).Scan(&telemetryToday, &telemetryWeek, &summary.Totals.MirrorWeek, &summary.ChannelReported); err != nil {
		return summary, err
	}

	github, err := githubReleaseDownloads(ctx, tx, weekStart, today, &summary)
	if err != nil {
		return summary, err
	}
	summary.Rows = append(summary.Rows, github...)
	if err = tx.QueryRow(ctx, `SELECT to_char(max(day),'YYYY-MM-DD') FROM release_asset_snapshots`).Scan(&summary.SnapshotDay); err != nil {
		return summary, err
	}
	summary.Totals.Today = telemetryToday + summary.Totals.GitHubToday
	summary.Totals.Week = telemetryWeek + summary.Totals.GitHubWeek
	if summary.ChannelReported && summary.Totals.Week > 0 {
		share := float64(summary.Totals.MirrorWeek) / float64(summary.Totals.Week)
		summary.MirrorShare = &share
	}
	sort.SliceStable(summary.Rows, func(i, j int) bool {
		if summary.Rows[i].Week != summary.Rows[j].Week {
			return summary.Rows[i].Week > summary.Rows[j].Week
		}
		return summary.Rows[i].Today > summary.Rows[j].Today
	})
	return summary, nil
}

// githubReleaseDownloads turns the asset snapshots into per-asset downloads: each snapshot day counts the difference to the asset's previous snapshot, which may be older than the window, and an asset's first snapshot counts nothing because its downloads cannot be dated. Assets without downloads in the window and release metadata files are left out. It adds every asset to the summary's GitHub totals, including assets beyond the row cap, which set Truncated.
func githubReleaseDownloads(ctx context.Context, tx pgx.Tx, weekStart, today time.Time, summary *downloadsSummary) ([]downloadsRow, error) {
	rows, err := tx.Query(ctx, `WITH base AS (
 SELECT DISTINCT ON (repo,tag,asset) repo,tag,asset,day,download_count FROM release_asset_snapshots WHERE day<$1 ORDER BY repo,tag,asset,day DESC
), joined AS (
 SELECT * FROM base UNION ALL SELECT repo,tag,asset,day,download_count FROM release_asset_snapshots WHERE day>=$1 AND day<=$2
), deltas AS (
 SELECT repo,tag,asset,day,GREATEST(download_count-lag(download_count) OVER (PARTITION BY repo,tag,asset ORDER BY day),0) AS delta FROM joined
)
SELECT repo,tag,asset,COALESCE(sum(delta) FILTER (WHERE day=$2),0),sum(delta)
 FROM deltas WHERE day>=$1 GROUP BY repo,tag,asset HAVING sum(delta)>0
 ORDER BY sum(delta) DESC,repo,tag,asset`, weekStart, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channel := "github"
	result := []downloadsRow{}
	for rows.Next() {
		row := downloadsRow{Source: downloadsSourceGitHub, Channel: &channel}
		var asset string
		if err = rows.Scan(&row.Repo, &row.Tag, &asset, &row.Today, &row.Week); err != nil {
			return nil, err
		}
		if releaseMetadataAsset(asset) {
			continue
		}
		summary.Totals.GitHubToday += row.Today
		summary.Totals.GitHubWeek += row.Week
		if len(result) == downloadsRowLimit {
			summary.Truncated = true
			continue
		}
		row.Artifact = &asset
		row.Platform, row.Version = releaseTagPlatform(row.Tag, asset)
		result = append(result, row)
	}
	return result, rows.Err()
}

// releaseTag splits the configured tag convention "<platform>-v<version>", for example windows-v0.5.4.
var releaseTag = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)-(v?[0-9][0-9A-Za-z.+_-]*)$`)

// releaseTagPlatform derives the platform and version of a release asset: from the tag when it follows the platform prefix convention, otherwise the whole tag is the version and the installer extension names the platform. An unknown platform is empty.
func releaseTagPlatform(tag, asset string) (string, string) {
	if m := releaseTag.FindStringSubmatch(tag); m != nil {
		return strings.ToLower(m[1]), m[2]
	}
	name := strings.ToLower(asset)
	for _, rule := range []struct{ platform, suffixes string }{
		{"windows", ".exe .msi .msix .appx"},
		{"macos", ".dmg .pkg"},
		{"linux", ".deb .rpm .appimage .flatpak .snap"},
		{"android", ".apk .aab"},
		{"ios", ".ipa"},
		{"harmony", ".hap .app"},
	} {
		for _, suffix := range strings.Fields(rule.suffixes) {
			if strings.HasSuffix(name, suffix) {
				return rule.platform, tag
			}
		}
	}
	return "", tag
}

// releaseMetadataAsset reports checksum, signature and updater manifest files, whose downloads are update checks or verification rather than installs.
func releaseMetadataAsset(asset string) bool {
	name := strings.ToLower(asset)
	for _, suffix := range []string{".sha256", ".sha512", ".sha256sum", ".sig", ".asc", ".minisig", ".txt", ".yml", ".yaml", ".json", ".blockmap", ".zsync"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
