package account

import (
	"net/http"
	"strings"
)

// Telemetry is mounted behind the server's client/session authentication and quota.
func (a *Service) Telemetry(w http.ResponseWriter, r *http.Request) {
	if a == nil {
		writeError(w, 503, "user_auth_disabled")
		return
	}
	var v struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Platform string `json:"platform"`
		Version  string `json:"version"`
		Message  string `json:"message"`
		Stack    string `json:"stack"`
	}
	if !readSized(w, r, &v, 32768) {
		return
	}
	if !resourceText(v.ID, 16, 128, false) || (v.Kind != "download" && v.Kind != "crash") || !resourceText(v.Platform, 1, 32, false) || !resourceText(v.Version, 1, 64, false) || !resourceText(v.Message, 0, 1000, true) || !resourceText(v.Stack, 0, 16000, true) || (v.Kind == "crash" && strings.TrimSpace(v.Message) == "") || (v.Kind == "download" && (v.Message != "" || v.Stack != "")) {
		writeError(w, 400, "invalid_event")
		return
	}
	_, err := a.store.pool.Exec(r.Context(), `INSERT INTO admin_events(id,kind,platform,version,message,stack) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO NOTHING`, v.ID, v.Kind, v.Platform, v.Version, v.Message, v.Stack)
	if err != nil {
		a.error(w, err)
		return
	}
	write(w, 202, map[string]bool{"accepted": true})
}
