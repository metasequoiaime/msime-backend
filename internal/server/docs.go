package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed swagger/*
var documentation embed.FS

// OpenAPI returns the published API description, the same document /openapi.json serves.
func OpenAPI() []byte {
	data, err := documentation.ReadFile("swagger/openapi.json")
	if err != nil {
		panic(err)
	}
	return data
}

var documentationHandler = func() http.Handler {
	files, err := fs.Sub(documentation, "swagger")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/swagger/", http.FileServer(http.FS(files)))
}()

func serveDocumentation(w http.ResponseWriter, r *http.Request, enabled bool) bool {
	if r.URL.Path != "/swagger" && r.URL.Path != "/openapi.json" && !strings.HasPrefix(r.URL.Path, "/swagger/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !enabled {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'")
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return true
	}
	if r.URL.Path == "/swagger" {
		http.Redirect(w, r, "/swagger/", http.StatusTemporaryRedirect)
		return true
	}
	if r.URL.Path == "/openapi.json" {
		data, _ := documentation.ReadFile("swagger/openapi.json")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != "HEAD" {
			_, _ = w.Write(data)
		}
		return true
	}
	documentationHandler.ServeHTTP(w, r)
	return true
}
