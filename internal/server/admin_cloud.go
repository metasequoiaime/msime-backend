package server

import "net/http"

// Cloud usage (unit U10).

// adminCloud serves GET /api/cloud; it requires view_cloud_usage.
func (s *Server) adminCloud(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}
