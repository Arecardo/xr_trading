package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/api/httpapi"
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
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != http.StatusOK || !httpapi.ValidRequestID(response.Header().Get(httpapi.RequestIDHeader)) {
			t.Fatalf("health response is missing request ID: status=%d header=%q", response.Code, response.Header().Get(httpapi.RequestIDHeader))
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/market-info/v1/instruments?asset_code=asset.crypto.missing", nil))
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"ASSET_NOT_FOUND"`) {
			t.Fatalf("instrument options route is not wired: status=%d body=%s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/market-info/v1/quotes/latest?asset_code=asset.crypto.missing", nil))
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"ASSET_NOT_FOUND"`) {
			t.Fatalf("latest quotes route is not wired: status=%d body=%s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/market-info/v1/bars?instrument_code=instrument.crypto.missing&provider=bybit&interval=1h", nil))
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"INSTRUMENT_NOT_FOUND"`) {
			t.Fatalf("bars route is not wired: status=%d body=%s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/market-info/v1/collection-subscriptions", nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("subscription route is not protected: status=%d body=%s", response.Code, response.Body.String())
		}
		request := httptest.NewRequest(http.MethodPost, "/api/market-info/v1/collection-subscriptions", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer test-admin-token")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("subscription route is not wired: status=%d body=%s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/market-info/v1/ingestion-runs/backfill", strings.NewReader(`{}`)))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("backfill route is not protected: status=%d body=%s", response.Code, response.Body.String())
		}
		request = httptest.NewRequest(http.MethodPost, "/api/market-info/v1/ingestion-runs/backfill", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer test-admin-token")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("backfill route is not wired: status=%d body=%s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/market-info/v1/ingestion-runs", nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("ingestion query route is not protected: status=%d body=%s", response.Code, response.Body.String())
		}
		request = httptest.NewRequest(http.MethodGet, "/api/market-info/v1/ingestion-tasks?status=partial", nil)
		request.Header.Set("Authorization", "Bearer test-admin-token")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("ingestion query route is not wired: status=%d body=%s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/market-info/v1/ingestion-tasks/019f1452-90f7-7992-a87a-ca2727898301/retry", strings.NewReader(`{"reason":"retry"}`)))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("ingestion command route is not protected: status=%d body=%s", response.Code, response.Body.String())
		}
		request = httptest.NewRequest(http.MethodPost, "/api/market-info/v1/ingestion-tasks/019f1452-90f7-7992-a87a-ca2727898301/cancel", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer test-admin-token")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("ingestion command route is not wired: status=%d body=%s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/market-info/v1/providers/status", nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("provider status route is not protected: status=%d body=%s", response.Code, response.Body.String())
		}
		request = httptest.NewRequest(http.MethodGet, "/api/market-info/v1/providers/status?probe=true", nil)
		request.Header.Set("Authorization", "Bearer test-admin-token")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("provider status route is not wired: status=%d body=%s", response.Code, response.Body.String())
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
		AdminBearerToken: "test-admin-token",
		AdminSubject:     "test-admin",
	}, nil
}

type stubPool struct {
	version int64
	closed  bool
}

func (s *stubPool) Ping(context.Context) error {
	return nil
}

func (s *stubPool) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if strings.Contains(query, "core.assets") || strings.Contains(query, "core.instruments") {
		return stubErrorRow{err: pgx.ErrNoRows}
	}
	return stubRow{version: s.version}
}

func (s *stubPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("query is not expected")
}

func (s *stubPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("exec is not expected")
}

func (s *stubPool) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("begin is not expected")
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

type stubErrorRow struct {
	err error
}

func (s stubErrorRow) Scan(...any) error {
	return s.err
}
