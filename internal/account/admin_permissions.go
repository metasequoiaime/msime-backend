package account

import "net/http"

// Permissions and audit log page (unit U12).

// auditList serves GET /api/audit.
var auditList = adminList{
	query: `SELECT id,actor,action,target,created_at FROM admin_audit`,
	filters: []listFilter{
		{param: "action", field: "action", max: 64},
		{param: "actor", field: "actor", max: 200, contains: true},
	},
}

// adminPermissions serves GET /api/permissions.
func (a *Service) adminPermissions(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}

// adminPermissionsAction serves POST /api/permissions {action: grant|revoke, role, permission}.
func (a *Service) adminPermissionsAction(w http.ResponseWriter, r *http.Request, _ string) {
	notImplemented(w)
}
