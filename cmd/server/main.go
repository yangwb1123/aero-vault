package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/cli"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/mcp"
	"github.com/aero-vault/aero-vault/internal/service"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp":
			if err := runMCP(); err != nil {
				fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
				os.Exit(1)
			}
			return
		case "cli":
			os.Exit(cli.Run(os.Args[2:]))
		}
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.App.LogLevel}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	p, err := prepareServerStartup(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() { stop(); p.close() }()
	oidc, err := buildOIDCHandler(cfg)
	if err != nil {
		return fmt.Errorf("configure OIDC: %w", err)
	}
	if err := p.initialize(ctx); err != nil {
		return err
	}
	if err := p.start(ctx); err != nil {
		return err
	}
	return servePreparedHTTP(ctx, p, oidc)
}

// HTTP and stdio share the same raw-reader capture, write wrappers, consumers
// and preflight. Nothing here starts a runtime or a protected background loop.
func prepareServerStartup(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*spaceStartup, error) {
	infra, err := prepareInfrastructure(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	a, err := prepareSpaceAssembly(cfg, infra.raw, infra.store, infra.bus, infra.readers, logger)
	if err != nil {
		infra.bus.Close()
		_ = infra.raw.Close()
		return nil, err
	}
	p, err := prepareSpaceStartup(cfg, logger, infra, a)
	if err != nil {
		a.close()
		infra.bus.Close()
		_ = infra.raw.Close()
		return nil, err
	}
	return p, nil
}

func runMCP() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.App.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	p, err := prepareServerStartup(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() { stop(); p.close() }()
	if err := p.initialize(ctx); err != nil {
		return err
	}
	if err := p.start(ctx); err != nil {
		return err
	}
	return servePreparedMCP(access.SystemContext(ctx, "default"), p)
}

func servePreparedMCP(ctx context.Context, p *spaceStartup) error {
	if err := p.requireStarted(); err != nil {
		return err
	}
	srv := mcp.NewServer(p.assembly.svc, p.assembly.repo, p.search, "default", p.logger)
	if p.chat != nil {
		srv.WithChat(p.chat)
	}
	p.logger.Info("mcp stdio server starting")
	return mcp.ServeStdio(ctx, srv, os.Stdin, os.Stdout)
}

// Compile-time interface satisfaction checks.
var _ service.ChunkCleaner = (*ai.Indexer)(nil)
