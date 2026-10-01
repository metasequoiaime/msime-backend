package account

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// adminOverview serves GET /api/overview?days=7|30 (default 30): totals plus a daily series ending today in UTC.
func (a *Service) adminOverview(w http.ResponseWriter, r *http.Request, _ string) {
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
   'plugins',(SELECT count(*) FROM community_plugins),
   'plugin_downloads',(SELECT count(*) FROM community_plugin_downloads),
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
}
