package account

import "net/http"

// Personal page (unit U12).

// adminMe serves GET /api/me.
func (a *Service) adminMe(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}

// adminMeAction serves POST /api/me {action: set_pref|revoke_session|regenerate_token, ...}.
func (a *Service) adminMeAction(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}
