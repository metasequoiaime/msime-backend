package server

import (
	"strings"

	"github.com/metasequoiaime/MSIME-Backend/internal/account"
)

// AdminRouteList is every route the admin site serves under /api/, read from the tables serveAdmin and the account package dispatch with, so a tool listing them cannot fall behind. Method "*" marks a route whose handler takes more than one method; docs/admin.md says which. The sign-in routes under /api/auth/ are left out: the command line drives them itself.
func AdminRouteList() []account.AdminRoute {
	routes := []account.AdminRoute{{Method: "GET", Path: "/api/system"}, {Method: "GET", Path: "/api/admins"}, {Method: "POST", Path: "/api/admins"}}
	for _, route := range adminServerRoutes {
		path := strings.ReplaceAll(route.pattern, "{}", "{id}")
		if strings.HasSuffix(path, "/") {
			path += "{rest...}"
		}
		routes = append(routes, account.AdminRoute{Method: "*", Path: path})
	}
	// 日志流不在 adminServerRoutes 表里（它绕过请求超时，由 serveAdmin 单独分发），在这里补上。
	routes = append(routes, account.AdminRoute{Method: "GET", Path: adminLogsStreamPath})
	return append(routes, account.AdminRouteList()...)
}
