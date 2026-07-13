package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"xr-trading/market-info-service/internal/config"
	"xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/server"
)

func TestRunServeWiresDependencies(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db := &stubPool{version: 4}
	var openedConfig postgres.Config
	var serverConfig server.Config
	err := run(ctx, []string{"serve"}, validRuntimeConfig, func(_ context.Context, cfg postgres.Config) (pooledDB, error) {
		openedConfig = cfg
		return db, nil
	}, func(cfg server.Config, handler http.Handler) (*server.Server, error) {
		serverConfig = cfg
		if handler == nil {
			t.Fatal("handler is nil")
		}
		return server.New(cfg, handler)
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !db.closed {
		t.Fatal("database pool was not closed")
	}
	if openedConfig.DatabaseURL == "" || openedConfig.MaxConns != 8 || openedConfig.MinConns != 1 {
		t.Fatalf("database config not mapped: %+v", openedConfig)
	}
	if serverConfig.Address != "127.0.0.1:0" || serverConfig.ShutdownTimeout != time.Millisecond {
		t.Fatalf("server config not mapped: %+v", serverConfig)
	}
}

func TestRunDefaultsToServeMode(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := run(ctx, nil, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) {
		return &stubPool{version: 4}, nil
	}, server.New)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		load      loadConfig
		open      openPool
		create    newServer
		wantError string
	}{
		{"missing dependency", nil, nil, nil, nil, "serve dependencies are required"},
		{"unsupported mode", []string{"worker"}, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) { return &stubPool{version: 4}, nil }, server.New, "unsupported mode"},
		{"config error", nil, func() (config.Config, error) { return config.Config{}, errors.New("bad env") }, func(context.Context, postgres.Config) (pooledDB, error) { return &stubPool{version: 4}, nil }, server.New, "load configuration"},
		{"open error", nil, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) { return nil, errors.New("db down") }, server.New, "open database pool"},
		{"server error", nil, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) { return &stubPool{version: 4}, nil }, func(server.Config, http.Handler) (*server.Server, error) { return nil, errors.New("bad server") }, "create HTTP server"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := run(context.Background(), tt.args, tt.load, tt.open, tt.create)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("run() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func validRuntimeConfig() (config.Config, error) {
	return config.Config{
		HTTPAddress:      "127.0.0.1:0",
		ReadTimeout:      time.Millisecond,
		WriteTimeout:     time.Millisecond,
		IdleTimeout:      time.Millisecond,
		ShutdownTimeout:  time.Millisecond,
		ReadinessTimeout: time.Millisecond,
		DatabaseURL:      "postgres://user:pass@localhost:5432/db",
		DBMaxConns:       8,
		DBMinConns:       1,
		DBMaxConnLife:    time.Minute,
		DBHealthPeriod:   time.Second,
	}, nil
}

type stubPool struct {
	version int64
	closed  bool
}

func (s *stubPool) Ping(context.Context) error {
	return nil
}

func (s *stubPool) QueryRow(context.Context, string, ...any) pgx.Row {
	return stubRow{version: s.version}
}

func (s *stubPool) Close() {
	s.closed = true
}

type stubRow struct {
	version int64
}

func (s stubRow) Scan(dest ...any) error {
	*(dest[0].(*int64)) = s.version
	return nil
}
