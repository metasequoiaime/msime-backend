package server

import (
	"context"
	"net/http"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// Dictionary pull request review (unit U1): the community-words/ pull requests on admin.github.dictionary_repo.

// adminDictPRs serves every path under /api/dict-prs: GET /api/dict-prs, GET /api/dict-prs/{n}, POST /api/dict-prs/{n}/approve, /reject and /trim.
func (s *Server) adminDictPRs(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}

// searchDictPRs matches q against the cached dictionary pull requests for the global search; it never calls GitHub.
func (s *Server) searchDictPRs(q string) []account.AdminSearchHit {
	return nil
}

// pendingDictPRs counts open dictionary pull requests for the shell badge.
func (s *Server) pendingDictPRs(ctx context.Context) (int, error) {
	return 0, nil
}
