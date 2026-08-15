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
	"xr-trading/market-info-service/internal/database/migrations"
	"xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/markettime"
	"xr-trading/market-info-service/internal/server"
)

func TestRunServeWiresDependencies(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db := &stubPool{version: migrations.LatestVersion}
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
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "market_info_readiness_status 1") || !strings.Contains(response.Body.String(), "market_info_operational_snapshot_success 0") {
			t.Fatalf("metrics route is not wired: status=%d body=%s", response.Code, response.Body.String())
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
		return &stubPool{version: migrations.LatestVersion}, nil
	}, server.New)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunWorkerAndAllModes(t *testing.T) {
	t.Parallel()

	for _, mode := range []config.RuntimeMode{config.RuntimeModeWorker, config.RuntimeModeAll} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := &stubPool{claimCalled: make(chan struct{}, 1)}
			serverCalls := 0
			result := make(chan error, 1)
			go func() {
				result <- run(ctx, []string{string(mode)}, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) {
					return db, nil
				}, func(cfg server.Config, handler http.Handler) (*server.Server, error) {
					serverCalls++
					return server.New(cfg, handler)
				})
			}()
			select {
			case <-db.claimCalled:
			case <-time.After(time.Second):
				t.Fatal("worker did not attempt to claim a task")
			}
			cancel()
			if err := <-result; err != nil {
				t.Fatalf("run(%s) error = %v", mode, err)
			}
			wantServerCalls := 0
			if mode == config.RuntimeModeAll {
				wantServerCalls = 1
			}
			if serverCalls != wantServerCalls || !db.closed {
				t.Fatalf("server calls=%d want=%d closed=%v", serverCalls, wantServerCalls, db.closed)
			}
		})
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
		{"missing dependency", nil, nil, nil, nil, "configuration loader and database opener"},
		{"unsupported mode", []string{"other"}, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) {
			return &stubPool{version: migrations.LatestVersion}, nil
		}, server.New, "unsupported mode"},
		{"too many modes", []string{"serve", "worker"}, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) {
			return &stubPool{version: migrations.LatestVersion}, nil
		}, server.New, "at most one"},
		{"config error", nil, func(config.RuntimeMode) (config.Config, error) { return config.Config{}, errors.New("bad env") }, func(context.Context, postgres.Config) (pooledDB, error) {
			return &stubPool{version: migrations.LatestVersion}, nil
		}, server.New, "load configuration"},
		{"open error", nil, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) { return nil, errors.New("db down") }, server.New, "open database pool"},
		{"server error", nil, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) {
			return &stubPool{version: migrations.LatestVersion}, nil
		}, func(server.Config, http.Handler) (*server.Server, error) { return nil, errors.New("bad server") }, "create HTTP server"},
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
	if err := run(nil, nil, validRuntimeConfig, func(context.Context, postgres.Config) (pooledDB, error) {
		return &stubPool{}, nil
	}, server.New); err == nil || !strings.Contains(err.Error(), "runtime context") {
		t.Fatalf("run(nil context) error = %v", err)
	}
}

func TestBuildAdapterRegistry(t *testing.T) {
	t.Parallel()

	calendar, err := markettime.NewNYSECalendar()
	if err != nil {
		t.Fatalf("NewNYSECalendar() error = %v", err)
	}
	registry, closeAdapters, err := buildAdapterRegistry(context.Background(), config.Config{
		EnabledProviders: []string{"bybit", "coingecko"},
	}, calendar)
	if err != nil {
		t.Fatalf("buildAdapterRegistry() error = %v", err)
	}
	adapters := registry.List()
	if len(adapters) != 2 || adapters[0].ProviderCode().String() != "bybit" || adapters[1].ProviderCode().String() != "coingecko" {
		t.Fatalf("registered adapters = %#v", adapters)
	}
	if err := closeAdapters(); err != nil {
		t.Fatalf("close adapters error = %v", err)
	}

	if _, _, err := buildAdapterRegistry(context.Background(), config.Config{
		EnabledProviders: []string{"bybit"},
		BybitBaseURL:     "not-a-url",
	}, calendar); err == nil || !strings.Contains(err.Error(), "create Bybit adapter") {
		t.Fatalf("buildAdapterRegistry(invalid URL) error = %v", err)
	}
	if _, _, err := buildAdapterRegistry(context.Background(), config.Config{
		EnabledProviders: []string{"coingecko"},
		CoinGeckoBaseURL: "not-a-url",
	}, calendar); err == nil || !strings.Contains(err.Error(), "create CoinGecko adapter") {
		t.Fatalf("buildAdapterRegistry(invalid CoinGecko URL) error = %v", err)
	}
	if _, _, err := buildAdapterRegistry(context.Background(), config.Config{
		EnabledProviders: []string{"unsupported"},
	}, calendar); err == nil || !strings.Contains(err.Error(), "unsupported configured provider") {
		t.Fatalf("buildAdapterRegistry(unsupported) error = %v", err)
	}
}

func validRuntimeConfig(config.RuntimeMode) (config.Config, error) {
	return config.Config{
		HTTPAddress:             "127.0.0.1:0",
		ReadTimeout:             time.Millisecond,
		WriteTimeout:            time.Millisecond,
		IdleTimeout:             time.Millisecond,
		ShutdownTimeout:         time.Millisecond,
		ReadinessTimeout:        time.Millisecond,
		DatabaseURL:             "postgres://user:pass@localhost:5432/db",
		DBMaxConns:              8,
		DBMinConns:              1,
		DBMaxConnLife:           time.Minute,
		DBHealthPeriod:          time.Second,
		AdminBearerToken:        "test-admin-token",
		AdminSubject:            "test-admin",
		EnabledProviders:        []string{"bybit"},
		WorkerID:                "test-worker",
		WorkerConcurrency:       1,
		WorkerLeaseDuration:     time.Minute,
		WorkerPollInterval:      time.Millisecond,
		WorkerClaimErrorBackoff: time.Millisecond,
		IngestionBarsPerPage:    100,
		IngestionMaximumPages:   2,
		SchedulerInterval:       time.Millisecond,
	}, nil
}

type stubPool struct {
	version     int64
	closed      bool
	claimCalled chan struct{}
}

func (s *stubPool) Ping(context.Context) error {
	return nil
}

func (s *stubPool) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if strings.Contains(query, "core.assets") || strings.Contains(query, "core.instruments") {
		return stubErrorRow{err: pgx.ErrNoRows}
	}
	if strings.Contains(query, "count(*) FILTER (WHERE status = 'pending')") {
		return stubMetricsRow{}
	}
	if strings.Contains(query, "WITH candidate AS") {
		if s.claimCalled != nil {
			select {
			case s.claimCalled <- struct{}{}:
			default:
			}
		}
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

type stubMetricsRow struct{}

func (stubMetricsRow) Scan(destinations ...any) error {
	for index := 0; index < 6; index++ {
		*destinations[index].(*int64) = 0
	}
	*destinations[6].(**time.Time) = nil
	return nil
}
