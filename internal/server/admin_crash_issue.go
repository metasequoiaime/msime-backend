package server

import (
	"context"
	"net/http"
)

// Crash group to GitHub issue (unit U9).

// adminCrashIssue serves POST /api/crash-groups/{signature}/issue: opens an issue in the platform's repository and marks the group known.
func (s *Server) adminCrashIssue(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}

// crashSpikeJob checks every hour until ctx ends for crash groups whose last 7 days rose more than 20% over the 7 days before, and notifies the console of each once.
func (s *Server) crashSpikeJob(ctx context.Context) {}
