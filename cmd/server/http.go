package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/api/s3compat"
	dav "github.com/aero-vault/aero-vault/internal/api/webdav"
	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/mcp"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/webui"
	"log/slog"
)

type readinessChecker interface {
	Ready(context.Context) error
}

// degradedChecker is the additive readiness-degradation surface: a checker
// that also reports a degraded condition (with the recorded backlog age)
// lets /readyz answer 200 with a degraded marker instead of 503 — the D1
// "degrade, never evict" contract. billing.Runtime (no Degraded) does not
// implement it and contributes false/0 through the group.
type degradedChecker interface {
	Degraded() bool
	BacklogAge() time.Duration
}

// readyzProbeTimeout bounds the /readyz probes independently of
// STORAGE_READ_TIMEOUT (30s default) and REQUEST_TIMEOUT_SECONDS (120s
// default): a wedged object store or database must not hold the readiness
// endpoint for tens of seconds per probe, defeating LB/orchestrator
// failover. The same 2s budget bounds repo.Ping (H2), the storage probe,
// and the extra readiness group (billing/audit-governance store queries).
// Worst-case degraded-path latency = ping 2s + storage 2s + audit probes 2s
// = 6s < the helm readinessProbe timeoutSeconds: 10 (deployment.yaml).
const readyzProbeTimeout = 2 * time.Second

type readinessGroup []readinessChecker

func (g readinessGroup) Ready(ctx context.Context) error {
	for _, checker := range g {
		if err := checker.Ready(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Degraded reports OR over the members that implement degradedChecker
// (false when none do — billing contributes false, an empty group false).
func (g readinessGroup) Degraded() bool {
	for _, checker := range g {
		if dc, ok := checker.(degradedChecker); ok && dc.Degraded() {
			return true
		}
	}
	return false
}

// BacklogAge reports the max backlog age over the members that implement
// degradedChecker (0 when none do).
func (g readinessGroup) BacklogAge() time.Duration {
	var max time.Duration
	for _, checker := range g {
		if dc, ok := checker.(degradedChecker); ok {
			if age := dc.BacklogAge(); age > max {
				max = age
			}
		}
	}
	return max
}

func readyzHandler(
	repo repository.Repository, store storage.Storage, extra readinessChecker,
) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// H2: bound repo.Ping the same way as the storage probe — a wedged
		// database must not hold /readyz beyond the probe budget either.
		pingCtx, pingCancel := context.WithTimeout(req.Context(), readyzProbeTimeout)
		defer pingCancel()
		if err := repo.Ping(pingCtx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		probeCtx, cancel := context.WithTimeout(req.Context(), readyzProbeTimeout)
		defer cancel()
		if _, err := store.Stat(probeCtx, "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
			return
		}
		if extra != nil {
			if err := extra.Ready(probeCtx); err != nil {
				http.Error(w, "runtime dependency unavailable", http.StatusServiceUnavailable)
				return
			}
			// D1: a degraded extra (lag > maxLag, or a store probe timeout —
			// age unknown → 0) is still 200 with the marker body; the
			// healthy path below stays byte-identical. Written via a literal
			// template, not json.Marshal, to keep the healthy pin trivial.
			if dc, ok := extra.(degradedChecker); ok && dc.Degraded() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, `{"ok":true,"degraded":true,"backlog_age_seconds":%d}`, int64(dc.BacklogAge().Seconds()))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
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

// mcpScopeGate enforces the audit-governance scope on the HTTP /mcp mount
// before MCP dispatch (REQ-1/REQ-2): every principal reaching MCP tools must
// hold write AND audit:event:write, or admin (Key.Has implies); audit-only
// keys are rejected by the ring's checkScope (missing scope: write) earlier.
// Registry disabled → pass-through (I5 baseline TestFullServer_MCP). Single
// wiring point shared with cmd/server/governance_mcp_scope_e2e_test.go —
// mount and test cannot drift.
func mcpScopeGate(authReg *auth.Registry) func(http.Handler) http.Handler {
	return authReg.Require(auth.Scope(auditgovernance.RequiredScope))
}

func buildRouter(svc *service.FileService, repo repository.Repository, store storage.Storage, search *ai.Search, chat *ai.Chat, agent *ai.Agent, bus *events.Bus, authReg *auth.Registry, accessManager *access.Manager, oidc *auth.OIDCHandler, promHandler http.Handler, cfg *config.Config, aiTimeout time.Duration, aiRL, adminRL *middleware.RateLimiter, logger *slog.Logger, corsProvider middleware.BucketCORSProvider, extraReady readinessChecker) http.Handler {
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
	r.Get("/readyz", readyzHandler(repo, store, extraReady))
	if promHandler != nil {
		r.Method(http.MethodGet, "/metrics", promHandler)
	}
	r.Get("/openapi.json", rest.OpenAPISpecHandler())
	r.Get("/docs", rest.SwaggerUIHandler())
	if oidc != nil {
		r.Get("/auth/oidc/login", oidc.Login)
		r.Get("/auth/oidc/callback", oidc.Callback)
		r.Get("/auth/oidc/logout", oidc.Logout)
	}
	r.Mount("/v1", rest.NewRouter(svc, repo, search, chat, agent, bus, authReg, logger, cfg.Reconcile.IdempotencyHashBody, aiRL, adminRL, aiTimeout, cfg.AI.DegradedMode,
		func(h *rest.Handler) {
			h.WithCORSProvider(corsProvider)
			if accessManager != nil {
				h.WithAccessManager(accessManager, cfg.Access.PublicBaseURL)
			}
		}))
	if accessManager != nil {
		publicAccess := rest.NewPublicAccessHandler(svc, accessManager, logger)
		r.Get("/share/{token}", publicAccess.Share)
		r.Head("/share/{token}", publicAccess.Share)
		r.Get("/public/assets/*", publicAccess.Asset)
		r.Head("/public/assets/*", publicAccess.Asset)
	}
	if cfg.S3Compat.Prefix != "" {
		r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(svc, logger, accessManager))
	}
	mcpServer := mcp.NewServer(svc, repo, search, "default", logger)
	if chat != nil {
		mcpServer.WithChat(chat)
	}
	r.Method(http.MethodPost, "/mcp", mcpScopeGate(authReg)(mcp.HTTPHandler(mcpServer)))
	if cfg.WebUI.Enabled {
		r.Get("/", redirectWebUI)
		r.Get("/favicon.ico", webui.Favicon)
		r.Mount("/ui", webui.Handler())
	}
	var davH http.Handler
	if cfg.WebDAV.Prefix != "" {
		davH = dav.Handler(cfg.WebDAV.Prefix, svc, logger)
	}
	return buildDispatcher(r, davH, cfg)
}

func redirectWebUI(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusFound)
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
		if cfg.App.TLSEnabled {
			logger.Info("listening (TLS)", "addr", cfg.App.Addr)
			if err := srv.ListenAndServeTLS(cfg.App.TLSCertFile, cfg.App.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		} else {
			logger.Info("listening", "addr", cfg.App.Addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
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
