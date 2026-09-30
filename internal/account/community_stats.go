package account

import (
	"net/http"
	"time"
)

// CommunityStats is the public aggregate of community content. It deliberately excludes account counts and install telemetry, which only the admin overview exposes because they are easy to misread out of context.
type CommunityStats struct {
	Skins         int64     `json:"skins"`
	SkinDownloads int64     `json:"skin_downloads"`
	Dictionaries  int64     `json:"dictionaries"`
	Replies       int64     `json:"replies"`
	ResourceSaves int64     `json:"resource_saves"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// The counters mirror the community subset of the admin overview query so both surfaces report the same numbers.
const communityStatsQuery = `SELECT
 (SELECT count(*) FROM community_skins),
 (SELECT count(*) FROM community_skin_downloads),
 (SELECT count(*) FROM community_resources WHERE kind='dictionary'),
 (SELECT count(*) FROM community_resources WHERE kind='reply'),
 (SELECT count(*) FROM community_resource_saves),
 now()`

func (a *Service) communityStats(w http.ResponseWriter, r *http.Request) {
	var s CommunityStats
	if err := a.store.pool.QueryRow(r.Context(), communityStatsQuery).Scan(&s.Skins, &s.SkinDownloads, &s.Dictionaries, &s.Replies, &s.ResourceSaves, &s.GeneratedAt); err != nil {
		a.error(w, err)
		return
	}
	s.GeneratedAt = s.GeneratedAt.UTC()
	write(w, 200, s)
}
