// Package webui serves the Iris UI application at /ui and /ui/app. The
// dependency-free console remains available under /ui/legacy for rollback.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFS embed.FS

// Favicon serves the public browser icon from the embedded UI assets.
func Favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	http.ServeFileFS(w, r, staticFS, "static/favicon.svg")
}

// Handler returns an http.Handler that serves the static UI under /ui/*.
func Handler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui" || r.URL.Path == "/ui/" {
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFileFS(w, r, sub, "app/index.html")
			return
		}
		if r.URL.Path == "/ui/app" || r.URL.Path == "/ui/app/" {
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFileFS(w, r, sub, "app/index.html")
			return
		}
		if r.URL.Path == "/ui/legacy" || r.URL.Path == "/ui/legacy/" {
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFileFS(w, r, sub, "index.html")
			return
		}
		if r.URL.Path == "/ui/app/runtime-config.js" {
			w.Header().Set("Cache-Control", "no-store")
		}
		http.StripPrefix("/ui", files).ServeHTTP(w, r)
	})
}
