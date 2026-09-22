package account

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// AdminReady makes an enabled admin fail startup if its migration is missing.
func (a *Service) AdminReady(ctx context.Context) error {
	_, err := a.store.pool.Exec(ctx, `SELECT email FROM admin_members WHERE false; SELECT id FROM admin_events WHERE false; SELECT actor FROM admin_audit WHERE false; SELECT state_hash FROM admin_login_flows WHERE false; SELECT token_hash FROM admin_sessions WHERE false`)
	return err
}

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

// AdminHTTP must only be called after the independent admin authentication gate.
func (a *Service) AdminHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	if r.Method == "POST" && path == "actions" {
		a.adminAction(w, r)
		return
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return
	}
	if strings.HasPrefix(path, "users/") {
		a.adminUser(w, r, strings.TrimPrefix(path, "users/"))
		return
	}
	for _, section := range []string{"skins", "dictionaries", "replies"} {
		if id, ok := strings.CutPrefix(path, section+"/"); ok {
			a.adminContent(w, r, section, id)
			return
		}
	}
	if path == "overview" {
		days := 30
		if raw := r.URL.Query().Get("days"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || (parsed != 7 && parsed != 30) {
				writeError(w, 400, "invalid_days")
				return
			}
			days = parsed
		}
		var result json.RawMessage
		err := a.store.pool.QueryRow(r.Context(), `SELECT json_build_object(
   'users',(SELECT count(*) FROM auth_users),
   'new_users_30d',(SELECT count(*) FROM auth_users WHERE created_at>=now()-interval '30 days'),
   'session_users',(SELECT count(DISTINCT user_id) FROM auth_sessions WHERE NOT revoked AND expires_at>now()),
   'downloads',(SELECT count(*) FROM admin_events WHERE kind='download'),
   'crashes',(SELECT count(*) FROM admin_events WHERE kind='crash'),
   'open_crashes',(SELECT count(*) FROM admin_events WHERE kind='crash' AND NOT resolved),
   'skins',(SELECT count(*) FROM community_skins),
   'skin_downloads',(SELECT count(*) FROM community_skin_downloads),
   'dictionaries',(SELECT count(*) FROM community_resources WHERE kind='dictionary'),
   'replies',(SELECT count(*) FROM community_resources WHERE kind='reply'),
   'resource_saves',(SELECT count(*) FROM community_resource_saves),
   'daily',(SELECT json_agg(x ORDER BY day) FROM (
     SELECT to_char(d AT TIME ZONE 'UTC','YYYY-MM-DD') AS day,
     (SELECT count(*) FROM auth_users WHERE created_at>=d AND created_at<d+interval '1 day') AS users,
     (SELECT count(*) FROM admin_events WHERE kind='download' AND created_at>=d AND created_at<d+interval '1 day') AS downloads,
     (SELECT count(*) FROM admin_events WHERE kind='crash' AND created_at>=d AND created_at<d+interval '1 day') AS crashes
	     FROM generate_series(date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - ($1::int - 1) * interval '1 day',date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',interval '1 day') d
   ) x), 'range_days',$1)`, days).Scan(&result)
		if err != nil {
			a.error(w, err)
			return
		}
		write(w, 200, result)
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 10000 {
			writeError(w, 400, "invalid_page")
			return
		}
	}
	search := r.URL.Query().Get("q")
	if len(search) > 200 {
		writeError(w, 400, "invalid_search")
		return
	}
	// Every SQL fragment is selected from this fixed allowlist, never from request text.
	queries := map[string]string{
		"users":        `SELECT u.id,u.display_name,u.created_at,(SELECT count(*) FROM auth_sessions s WHERE s.user_id=u.id AND NOT revoked AND expires_at>now()) AS sessions FROM auth_users u`,
		"skins":        `SELECT s.id,s.name,s.description,s.owner_id,s.created_at,(SELECT count(*) FROM community_skin_downloads WHERE skin_id=s.id) AS downloads FROM community_skins s`,
		"dictionaries": `SELECT id,name,description,owner_id,revision,created_at,updated_at,jsonb_array_length(content->'entries') AS entries,(SELECT count(*) FROM community_resource_saves WHERE resource_id=community_resources.id) AS saves FROM community_resources WHERE kind='dictionary'`,
		"replies":      `SELECT id,name,description,owner_id,revision,created_at,updated_at,content->>'prompt' AS prompt FROM community_resources WHERE kind='reply'`,
		"downloads":    `SELECT id,platform,version,created_at FROM admin_events WHERE kind='download'`,
		"crashes":      `SELECT id,platform,version,message,stack,resolved,created_at FROM admin_events WHERE kind='crash'`,
		"audit":        `SELECT id,actor,action,target,created_at FROM admin_audit`,
	}
	query, ok := queries[path]
	if !ok {
		writeError(w, 404, "not_found")
		return
	}
	platform, version, status := r.URL.Query().Get("platform"), r.URL.Query().Get("version"), r.URL.Query().Get("status")
	action, actor := r.URL.Query().Get("action"), r.URL.Query().Get("actor")
	if len(platform) > 32 || len(version) > 64 || len(action) > 64 || len(actor) > 200 || (status != "" && status != "open" && status != "resolved") || (status != "" && path != "crashes") || ((platform != "" || version != "") && path != "crashes" && path != "downloads") || ((action != "" || actor != "") && path != "audit") {
		writeError(w, 400, "invalid_filter")
		return
	}
	var result json.RawMessage
	// Count and page share one statement snapshot, including pages beyond the last row.
	err := a.store.pool.QueryRow(r.Context(), `WITH filtered AS MATERIALIZED (
 SELECT to_jsonb(x) AS item, created_at, id FROM (`+query+`) x
 WHERE ($1='' OR to_jsonb(x)::text ILIKE '%'||$1||'%')
 AND ($3='' OR to_jsonb(x)->>'platform'=$3)
 AND ($4='' OR to_jsonb(x)->>'version'=$4)
 AND ($5='' OR to_jsonb(x)->>'resolved'=CASE WHEN $5='resolved' THEN 'true' ELSE 'false' END)
 AND ($6='' OR to_jsonb(x)->>'action'=$6)
 AND ($7='' OR to_jsonb(x)->>'actor' ILIKE '%'||$7||'%')
), selected AS (SELECT * FROM filtered ORDER BY created_at DESC,id DESC LIMIT 50 OFFSET $2)
SELECT json_build_object('items', COALESCE((SELECT json_agg(item ORDER BY created_at DESC,id DESC) FROM selected),'[]'::json),
 'page',$8::int,'total',(SELECT count(*) FROM filtered),'has_more',(SELECT count(*) FROM filtered)>$2+50)`, search, (page-1)*50, platform, version, status, action, actor, page).Scan(&result)
	if err != nil {
		a.error(w, err)
		return
	}
	write(w, 200, result)
}

func (a *Service) adminAction(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Action string `json:"action"`
		ID     string `json:"id"`
		UserID string `json:"user_id"`
	}
	if !read(w, r, &v) {
		return
	}
	if !resourceText(v.ID, 1, 128, false) {
		writeError(w, 400, "invalid_id")
		return
	}
	queries := map[string]string{
		"revoke_session":    `UPDATE auth_sessions SET revoked=true WHERE id=$1 AND user_id=$2`,
		"revoke_sessions":   `UPDATE auth_sessions SET revoked=true WHERE user_id=$1`,
		"delete_skin":       `DELETE FROM community_skins WHERE id=$1`,
		"delete_dictionary": `DELETE FROM community_resources WHERE id=$1 AND kind='dictionary'`,
		"delete_reply":      `DELETE FROM community_resources WHERE id=$1 AND kind='reply'`,
		"resolve_crash":     `UPDATE admin_events SET resolved=true WHERE id=$1 AND kind='crash'`,
		"reopen_crash":      `UPDATE admin_events SET resolved=false WHERE id=$1 AND kind='crash'`,
	}
	query, ok := queries[v.Action]
	if !ok {
		writeError(w, 400, "invalid_action")
		return
	}
	tx, err := a.store.pool.Begin(r.Context())
	if err != nil {
		a.error(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	args := []any{v.ID}
	if v.Action == "revoke_session" {
		if !resourceText(v.UserID, 1, 128, false) {
			writeError(w, 400, "invalid_user_id")
			return
		}
		args = append(args, v.UserID)
	}
	tag, err := tx.Exec(r.Context(), query, args...)
	if err != nil {
		a.error(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		if v.Action != "revoke_sessions" {
			writeError(w, 404, "not_found")
			return
		}
		// Revoking an existing user's already-empty session set is idempotent,
		// but a missing user must not create a misleading audit record.
		var exists bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_users WHERE id=$1)`, v.ID).Scan(&exists); err != nil {
			a.error(w, err)
			return
		}
		if !exists {
			writeError(w, 404, "not_found")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO admin_audit(action,target,actor) VALUES($1,$2,$3)`, v.Action, v.ID, adminActor(r.Context())); err != nil {
		a.error(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.error(w, err)
		return
	}
	write(w, 200, map[string]any{"ok": true, "affected": tag.RowsAffected()})
}
