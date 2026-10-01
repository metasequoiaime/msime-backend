package server

import (
	"context"
	"net/http"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// Issue triage (unit U3): the issues of admin.github.issue_repos.

// adminIssues serves every path under /api/issues: GET /api/issues, GET /api/issues/{owner}/{repo}/{n} and POST /api/issues/actions.
func (s *Server) adminIssues(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}

// searchIssues matches q against the cached issues for the global search; it never calls GitHub.
func (s *Server) searchIssues(q string) []account.AdminSearchHit {
	return nil
}

// pendingIssues counts open, untriaged issues for the shell badge.
func (s *Server) pendingIssues(ctx context.Context) (int, error) {
	return 0, nil
}
