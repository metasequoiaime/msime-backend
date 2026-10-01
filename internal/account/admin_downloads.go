package account

import "net/http"

// Downloads page (unit U6): the download event list and the grouped summary. The telemetry endpoint itself is in admin_telemetry.go.

// downloadsList serves GET /api/downloads.
var downloadsList = adminList{
	query: `SELECT id,platform,version,created_at FROM admin_events WHERE kind='download'`,
	filters: []listFilter{
		{param: "platform", field: "platform", max: 32},
		{param: "version", field: "version", max: 64},
	},
}

// adminDownloadsSummary serves GET /api/downloads/summary.
func (a *Service) adminDownloadsSummary(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}
