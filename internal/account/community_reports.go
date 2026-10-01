package account

import "net/http"

// CommunityReport serves POST /v1/community/reports (unit U2): a signed-in user reports a community item. The route is registered in the server package with the same per-address limit and timeout as the other account routes.
func (a *Service) CommunityReport(w http.ResponseWriter, r *http.Request) {
	if a == nil {
		writeError(w, 503, "user_auth_disabled")
		return
	}
	notImplemented(w)
}
