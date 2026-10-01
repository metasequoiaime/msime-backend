package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The admin handler dispatches by path in code rather than through the mux, so this reads its source: every list it can query, every action it can run and every content section must be in adminRoutes, and adminRoutes must name nothing else.
func TestAdminRoutesFollowTheAdminHandler(t *testing.T) {
	source, err := os.ReadFile("../../internal/account/admin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	// keys returns the keys of the map[string]string literal whose entries include first.
	keys := func(first string) []string {
		t.Helper()
		for _, block := range strings.Split(text, "queries := map[string]string{")[1:] {
			block, _, _ = strings.Cut(block, "\n\t}\n")
			if !strings.Contains(block, `"`+first+`":`) {
				continue
			}
			var names []string
			for _, m := range regexp.MustCompile(`(?m)^\s*"([a-z_-]+)":`).FindAllStringSubmatch(block, -1) {
				names = append(names, m[1])
			}
			sort.Strings(names)
			return names
		}
		t.Fatalf("admin.go no longer has a query map holding %q", first)
		return nil
	}
	sectionsBlock, _, _ := strings.Cut(strings.SplitN(text, `for _, section := range []string{`, 2)[1], "}")
	var sections []string
	for _, m := range regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(sectionsBlock, -1) {
		sections = append(sections, m[1])
	}

	documented := map[string]bool{}
	var actions []string
	for _, route := range adminRoutes {
		documented[route.method+" "+route.path] = true
		if route.path == "/api/actions" {
			_, values, _ := strings.Cut(route.notes, `"action": `)
			values, _, _ = strings.Cut(values, `, "id"`)
			for _, m := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(values, -1) {
				actions = append(actions, m[1])
			}
		}
	}
	sort.Strings(actions)

	if want := keys("revoke_session"); strings.Join(want, ",") != strings.Join(actions, ",") {
		t.Errorf("actions: handler %v, documented %v", want, actions)
	}
	expected := map[string]bool{
		"GET /api/system": true, "GET /api/overview": true, "GET /api/users/{id}": true,
		"POST /api/actions": true, "GET /api/site-settings": true, "POST /api/site-settings": true,
		"GET /api/admins": true, "POST /api/admins": true,
	}
	for _, list := range keys("users") {
		expected["GET /api/"+list] = true
	}
	for _, section := range sections {
		expected["GET /api/"+section+"/{id}"] = true
	}
	for route := range expected {
		if !documented[route] {
			t.Errorf("adminRoutes lacks %s", route)
		}
	}
	for route := range documented {
		if !expected[route] {
			t.Errorf("adminRoutes names %s, which the handler does not serve", route)
		}
	}
}
