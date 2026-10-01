package server

import "net/http"

// Console shell and global search (unit U11).

// adminShell serves GET /api/shell: everything the console frame needs in one request, polled once a minute.
func (s *Server) adminShell(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}

// adminSearch serves GET /api/search?q=: at most 8 hits from the database (account.AdminSearch) and from the cached GitHub pull requests, issues and releases (searchDictPRs, searchIssues, searchReleases).
func (s *Server) adminSearch(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}
