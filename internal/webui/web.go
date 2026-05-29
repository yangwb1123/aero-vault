// Package webui serves a single-page HTML console at /ui. It depends on
// nothing — pure HTML + vanilla JS calling the existing REST API. Tenant is
// chosen via a text input; the page persists it in localStorage.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFS embed.FS

// Handler returns an http.Handler that serves the static UI under /ui/* and
// redirects /ui → /ui/.
func Handler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// strip the prefix /ui before delegating to the file server
		if r.URL.Path == "/ui" || r.URL.Path == "/ui/" {
			http.ServeFileFS(w, r, sub, "index.html")
			return
		}
		http.StripPrefix("/ui", files).ServeHTTP(w, r)
	})
}
