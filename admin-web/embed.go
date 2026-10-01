// Package adminweb embeds the Vite production build in the Go service.
package adminweb

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed dist
var assets embed.FS

var pagePaths = map[string]bool{
	"/admins": true, "/": true, "/users": true, "/downloads": true, "/crashes": true,
	"/skins": true, "/dictionaries": true, "/replies": true, "/audit": true, "/site-settings": true,
}

func IsPath(path string) bool { return pagePaths[path] || strings.HasPrefix(path, "/assets/") }

func Handler() http.Handler {
	root, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if pagePaths[r.URL.Path] {
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		stat, err := fs.Stat(root, path)
		if !IsPath(r.URL.Path) || err != nil || stat.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
