package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

//go:embed dist/*
var webDistFS embed.FS

// registerWebConsole mounts the React SPA web console on the router.
// It serves static assets with proper MIME types, and falls back to index.html
// for client-side routes (e.g. /dashboard, /devices, /login, /audit).
// Any path beginning with /api/, /healthz, or /readyz is left to API handlers.
func registerWebConsole(r *chi.Mux) {
	subFS, err := fs.Sub(webDistFS, "dist")
	if err != nil {
		log.Warn().Err(err).Msg("failed to create sub-filesystem for web console")
		return
	}

	fileServer := http.FileServer(http.FS(subFS))

	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		trimmed := strings.TrimPrefix(req.URL.Path, "/")
		if trimmed == "" {
			trimmed = "index.html"
		}

		// If requested file exists in embedded assets, serve it directly
		f, err := subFS.Open(trimmed)
		if err == nil {
			_ = f.Close()
			if strings.HasPrefix(trimmed, "assets/") {
				// Vite hashed static bundles are immutable; cache for 1 year
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if trimmed == "index.html" {
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				w.Header().Set("Pragma", "no-cache")
				w.Header().Set("Expires", "0")
			}
			fileServer.ServeHTTP(w, req)
			return
		}

		// Fallback to index.html for SPA client routes
		if !strings.HasPrefix(req.URL.Path, "/api") && req.URL.Path != "/healthz" && req.URL.Path != "/readyz" {
			indexData, err := fs.ReadFile(subFS, "index.html")
			if err != nil {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexData)
			return
		}

		http.NotFound(w, req)
	})

	log.Info().Msg("web console mounted (embedded single-binary SPA)")
}
