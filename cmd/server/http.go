package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/api/s3compat"
	dav "github.com/aero-vault/aero-vault/internal/api/webdav"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/mcp"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
	"github.com/aero-vault/aero-vault/internal/webui"
	"log/slog"
)

func readyzHandler(repo repository.Repository, store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := repo.Ping(req.Context()); err != nil {
			http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if _, err := store.Stat(req.Context(), "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "storage: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

func buildDispatcher(r *chi.Mux, davH http.Handler, cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if davH != nil && cfg.WebDAV.Prefix != "" {
			p := req.URL.Path
			if p == cfg.WebDAV.Prefix || strings.HasPrefix(p, cfg.WebDAV.Prefix+"/") {
				davH.ServeHTTP(w, req)
				return
			}
		}
		r.ServeHTTP(w, req)
	})
}

func buildRouter(svc *service.FileService, repo repository.Repository, store storage.Storage, search *ai.Search, chat *ai.Chat, agent *ai.Agent, bus *events.Bus, authReg *auth.Registry, promHandler http.Handler, cfg *config.Config, aiTimeout time.Duration, aiRL *middleware.RateLimiter, logger *slog.Logger, corsProvider middleware.BucketCORSProvider) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r.Get("/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"aero-vault","version":"0.1.0"}` + "\n"))
	})
	r.Get("/readyz", readyzHandler(repo, store))
	if promHandler != nil {
		r.Method(http.MethodGet, "/metrics", promHandler)
	}
	r.Get("/openapi.json", rest.OpenAPISpecHandler())
	r.Get("/docs", rest.SwaggerUIHandler())
	r.Mount("/v1", rest.NewRouter(svc, repo, search, chat, agent, bus, authReg, logger, cfg.Reconcile.IdempotencyHashBody, aiRL, aiTimeout, cfg.AI.DegradedMode,
		func(h *rest.Handler) { h.WithCORSProvider(corsProvider) }))
	if cfg.S3Compat.Prefix != "" {
		r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(svc, logger))
	}
	mcpServer := mcp.NewServer(svc, repo, search, "default", logger)
	if chat != nil {
		mcpServer.WithChat(chat)
	}
	r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))
	if cfg.WebUI.Enabled {
		r.Mount("/ui", webui.Handler())
	}
	var davH http.Handler
	if cfg.WebDAV.Prefix != "" {
		davH = dav.Handler(cfg.WebDAV.Prefix, svc, logger)
	}
	return buildDispatcher(r, davH, cfg)
}

func applyMiddleware(handler http.Handler, authReg *auth.Registry, rl *middleware.RateLimiter, cfg *config.Config, logger *slog.Logger, concurrencyMW func(http.Handler) http.Handler, corsProvider middleware.BucketCORSProvider) http.Handler {
	chain := []struct {
		name string
		mw   func(http.Handler) http.Handler
	}{
		{"access_log", middleware.AccessLog(logger)},
		{"concurrency", concurrencyMW},
		{"recoverer", middleware.Recoverer(logger)},
		{"otel", telemetry.HTTPMiddleware("aero-vault")},
		{"rate_limit", rl.Middleware()},
		{"tenant", middleware.Tenant},
		{"auth", authReg.Middleware()},
		{"max_body", middleware.MaxBodySize(int64(cfg.App.MaxBodySize))},
		{"secure_headers", middleware.SecureHeaders()},
		{"cors", middleware.CORS(middleware.CORSConfig{
			AllowedOrigins: cfg.CORS.AllowedOrigins,
			AllowedHeaders: cfg.CORS.AllowedHeaders,
			AllowedMethods: cfg.CORS.AllowedMethods,
			ExposeHeaders:  append([]string{"ETag", "Idempotency-Replayed", "Retry-After", "X-Request-ID", "X-Version-Id"}, cfg.CORS.ExposeHeaders...),
		})},
		{"cors_bucket", middleware.BucketCORS(corsProvider)},
		{"request_id", middleware.RequestID},
	}
	for _, link := range chain {
		handler = telemetry.WithMiddlewareTiming(link.name, link.mw)(handler)
	}
	return handler
}

func runServer(ctx context.Context, handler http.Handler, cfg *config.Config, logger *slog.Logger, bus *events.Bus, shutdownOtel func(context.Context) error) error {
	srv := &http.Server{
		Addr:              cfg.App.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      time.Duration(cfg.App.WriteTimeoutSec) * time.Second,
		IdleTimeout:       time.Duration(cfg.App.IdleTimeoutSec) * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.App.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		if err != nil {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	bus.Close()
	_ = shutdownOtel(shutdownCtx)
	return nil
}
