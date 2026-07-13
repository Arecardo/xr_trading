//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/database/migrations"
	"xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/database/readiness"
	"xr-trading/market-info-service/internal/observability"
)

func TestDB007PoolAndReadinessAgainstPostgres(t *testing.T) {
	t.Parallel()

	databaseURL := integrationDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := postgres.OpenPool(ctx, postgres.Config{
		DatabaseURL:       databaseURL,
		MaxConns:          2,
		MinConns:          0,
		MaxConnLifetime:   time.Minute,
		HealthCheckPeriod: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}

	checker, err := readiness.New(pool, migrations.LatestVersion)
	if err != nil {
		t.Fatalf("readiness.New() error = %v", err)
	}
	if err := checker.Check(ctx); err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	incompatibleChecker, err := readiness.New(pool, migrations.LatestVersion+1)
	if err != nil {
		t.Fatalf("readiness.New(incompatible) error = %v", err)
	}
	if err := incompatibleChecker.Check(ctx); !errors.Is(err, observability.ErrMigrationIncompatible) {
		t.Fatalf("Check() error = %v, want migration incompatible", err)
	}

	pool.Close()
	if err := checker.Check(ctx); !errors.Is(err, observability.ErrDatabaseUnavailable) {
		t.Fatalf("Check() after Close error = %v, want database unavailable", err)
	}
}

func TestDB007OpenPoolFailsFastWhenPostgresIsUnavailable(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := postgres.OpenPool(ctx, postgres.Config{
		DatabaseURL:       "postgres://postgres:postgres@127.0.0.1:1/xr_trading?sslmode=disable",
		MaxConns:          1,
		MinConns:          0,
		MaxConnLifetime:   time.Minute,
		HealthCheckPeriod: time.Second,
	})
	if err == nil {
		t.Fatal("OpenPool() error = nil, want error")
	}
}

func TestDB007RealReadinessHandlerStatusCodes(t *testing.T) {
	t.Parallel()

	databaseURL := integrationDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := postgres.OpenPool(ctx, postgres.Config{
		DatabaseURL:       databaseURL,
		MaxConns:          2,
		MinConns:          0,
		MaxConnLifetime:   time.Minute,
		HealthCheckPeriod: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	defer pool.Close()

	readyChecker, err := readiness.New(pool, migrations.LatestVersion)
	if err != nil {
		t.Fatalf("readiness.New() error = %v", err)
	}
	assertReadyStatus(t, readyChecker, http.StatusOK)

	incompatibleChecker, err := readiness.New(pool, migrations.LatestVersion+1)
	if err != nil {
		t.Fatalf("readiness.New(incompatible) error = %v", err)
	}
	assertReadyStatus(t, incompatibleChecker, http.StatusServiceUnavailable)
}

func assertReadyStatus(t *testing.T, checker observability.ReadinessChecker, wantStatus int) {
	t.Helper()

	handler, err := observability.NewHealthHandler(checker, time.Second)
	if err != nil {
		t.Fatalf("NewHealthHandler() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.Readiness(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != wantStatus {
		t.Fatalf("readyz status = %d, want %d, body = %s", response.Code, wantStatus, response.Body.String())
	}
}

func integrationDatabaseURL(t *testing.T) string {
	t.Helper()

	if databaseURL := os.Getenv("MARKET_INFO_TEST_DATABASE_URL"); databaseURL != "" {
		return databaseURL
	}
	if databaseURL := os.Getenv("MARKET_INFO_DATABASE_URL"); databaseURL != "" {
		return databaseURL
	}
	t.Skip("MARKET_INFO_DATABASE_URL or MARKET_INFO_TEST_DATABASE_URL is required")
	return ""
}
