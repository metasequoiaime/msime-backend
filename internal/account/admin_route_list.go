package account

import "strings"

// AdminRoute is one admin API route as this package dispatches it, for tools that list the admin API. Path is under /api/ with {id} for a path parameter; Filters names the query parameters a list accepts besides page and q.
type AdminRoute struct {
	Method  string
	Path    string
	Filters []string
}

// AdminRouteList is every route AdminHTTP serves: the fixed handlers, adminRoutes and the adminLists, read from the tables it dispatches with.
func AdminRouteList() []AdminRoute {
	routes := []AdminRoute{{Method: "GET", Path: "/api/site-settings"}, {Method: "POST", Path: "/api/site-settings"}, {Method: "POST", Path: "/api/actions"}}
	for _, route := range adminRoutes {
		routes = append(routes, AdminRoute{Method: route.method, Path: "/api/" + strings.ReplaceAll(route.pattern, "{}", "{id}")})
	}
	for name, list := range adminLists {
		route := AdminRoute{Method: "GET", Path: "/api/" + name}
		for _, filter := range list.filters {
			route.Filters = append(route.Filters, filter.param)
		}
		routes = append(routes, route)
	}
	return routes
}
