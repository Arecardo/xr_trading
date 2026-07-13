package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"xr-trading/market-info-service/internal/config"
	"xr-trading/market-info-service/internal/database/migrations"
	"xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/database/readiness"
	"xr-trading/market-info-service/internal/observability"
	"xr-trading/market-info-service/internal/server"
)

type pooledDB interface {
	readiness.DB
	Close()
}

type openPool func(context.Context, postgres.Config) (pooledDB, error)
type loadConfig func() (config.Config, error)
type newServer func(server.Config, http.Handler) (*server.Server, error)

func main() {
	os.Exit(entrypoint())
}

func entrypoint() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], config.Load, openPostgresPool, server.New); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, load loadConfig, open openPool, createServer newServer) error {
	if load == nil || open == nil || createServer == nil {
		return errors.New("serve dependencies are required")
	}
	mode := "serve"
	if len(args) > 0 {
		mode = args[0]
	}
	if mode != "serve" {
		return fmt.Errorf("unsupported mode %q", mode)
	}

	cfg, err := load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	pool, err := open(ctx, postgres.Config{
		DatabaseURL:       cfg.DatabaseURL,
		MaxConns:          cfg.DBMaxConns,
		MinConns:          cfg.DBMinConns,
		MaxConnLifetime:   cfg.DBMaxConnLife,
		HealthCheckPeriod: cfg.DBHealthPeriod,
	})
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}
	defer pool.Close()

	checker, err := readiness.New(pool, migrations.LatestVersion)
	if err != nil {
		return fmt.Errorf("create readiness checker: %w", err)
	}
	health, err := observability.NewHealthHandler(checker, cfg.ReadinessTimeout)
	if err != nil {
		return fmt.Errorf("create health handler: %w", err)
	}
	mux := http.NewServeMux()
	health.Register(mux)

	httpServer, err := createServer(server.Config{
		Address:         cfg.HTTPAddress,
		ReadTimeout:     cfg.ReadTimeout,
		WriteTimeout:    cfg.WriteTimeout,
		IdleTimeout:     cfg.IdleTimeout,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}, mux)
	if err != nil {
		return fmt.Errorf("create HTTP server: %w", err)
	}
	if err := httpServer.Run(ctx); err != nil {
		return fmt.Errorf("run HTTP server: %w", err)
	}
	return nil
}

func openPostgresPool(ctx context.Context, cfg postgres.Config) (pooledDB, error) {
	return postgres.OpenPool(ctx, cfg)
}
