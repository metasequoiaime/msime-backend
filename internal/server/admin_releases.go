package server

import (
	"context"
	"net/http"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// Release management (unit U7): admin.github.platforms and their GitHub releases.

// adminReleases serves every path under /api/releases: GET /api/releases, GET /api/releases/{platform}, POST /api/releases/{platform}/trigger, POST /api/releases/{platform}/{tag}/notes and /withdraw.
func (s *Server) adminReleases(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}

// searchReleases matches q against the cached releases for the global search; it never calls GitHub.
func (s *Server) searchReleases(q string) []account.AdminSearchHit {
	return nil
}

// releaseSnapshotJob records the release asset download counts once a day until ctx ends.
func (s *Server) releaseSnapshotJob(ctx context.Context) {}
