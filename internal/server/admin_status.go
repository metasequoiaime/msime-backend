package server

import (
	"context"
	"net/http"
)

// System status (unit U10).

// adminStatus serves GET /api/status.
func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}

// adminHealth is the overall state for the shell: ok, degraded or down, or unknown while no probe has run.
func (s *Server) adminHealth(ctx context.Context) string {
	return "unknown"
}

// statusProbeJob probes the database and the recent upstream metrics every minute until ctx ends, rolling the results into admin_service_daily and opening or resolving automatic incidents.
func (s *Server) statusProbeJob(ctx context.Context) {}
