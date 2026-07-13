//go:build integration

package integration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"

	"github.com/jackc/pgx/v5"
)

func TestDB014QualityIssueRepositoryAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, _ := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	t.Cleanup(func() { deleteDB014Fixture(t, context.Background(), admin, providerID, instrumentID, assetID) })

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{DatabaseURL: integrationDatabaseURL(t), MaxConns: 5, MinConns: 0, MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	defer pool.Close()
	catalog, _ := repositorypostgres.NewCatalogRepository(pool)
	issues, err := repositorypostgres.NewDataQualityIssueRepository(pool)
	if err != nil {
		t.Fatalf("NewDataQualityIssueRepository() error = %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	provider := domain.Provider{ID: providerID, Code: integrationCode(t, "bybit-db014-"+providerID.String()), Name: "DB014 Bybit", ProviderType: "EXCHANGE", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	mapping := db012Mapping(t, providerID, instrumentID, now, "db014")
	if err := catalog.CreateProviderInstrument(ctx, mapping); err != nil {
		t.Fatalf("CreateProviderInstrument() error = %v", err)
	}

	base := db014Issue(t, instrumentID, now, "missing_quote")
	created := make(chan bool, 2)
	errs := make(chan error, 2)
	var writers sync.WaitGroup
	for range 2 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			wasCreated, err := issues.OpenIssue(ctx, base)
			created <- wasCreated
			errs <- err
		}()
	}
	writers.Wait()
	close(created)
	close(errs)
	createdCount := 0
	for err := range errs {
		if err != nil {
			t.Fatalf("OpenIssue(concurrent) error = %v", err)
		}
	}
	for value := range created {
		if value {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent created count = %d, want 1", createdCount)
	}
	if err := issues.AcknowledgeIssue(ctx, base.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("AcknowledgeIssue() error = %v", err)
	}
	if opened, err := issues.OpenIssue(ctx, base); err != nil || opened {
		t.Fatalf("OpenIssue(acknowledged duplicate) = (%t, %v)", opened, err)
	}
	if err := issues.ResolveIssue(ctx, base.ID, "backfilled", now.Add(2*time.Second)); err != nil {
		t.Fatalf("ResolveIssue() error = %v", err)
	}
	reopened := base
	reopened.ID = newIntegrationID(t)
	if opened, err := issues.OpenIssue(ctx, reopened); err != nil || !opened {
		t.Fatalf("OpenIssue(reopened) = (%t, %v)", opened, err)
	}

	partial := db014Issue(t, instrumentID, now, "provider_gap")
	partial.ProviderInstrumentID = &mapping.ID
	if opened, err := issues.OpenIssue(ctx, partial); err != nil || !opened {
		t.Fatalf("OpenIssue(partial NULL) = (%t, %v)", opened, err)
	}
	interval := "1h"
	openTime := now.Truncate(time.Hour)
	full := db014Issue(t, instrumentID, now, "bar_invalid")
	full.ProviderInstrumentID, full.Interval, full.OpenTime = &mapping.ID, &interval, &openTime
	if opened, err := issues.OpenIssue(ctx, full); err != nil || !opened {
		t.Fatalf("OpenIssue(full dimensions) = (%t, %v)", opened, err)
	}
	if err := issues.IgnoreIssue(ctx, partial.ID, "known provider delay", now.Add(3*time.Second)); err != nil {
		t.Fatalf("IgnoreIssue() error = %v", err)
	}
	var status string
	if err := admin.QueryRow(ctx, "SELECT status FROM market_data.data_quality_issues WHERE id = $1", partial.ID.UUID()).Scan(&status); err != nil || status != "ignored" {
		t.Fatalf("partial issue status = (%q, %v)", status, err)
	}
}

func db014Issue(t *testing.T, instrumentID domain.ID, now time.Time, ruleCode string) domain.DataQualityIssue {
	t.Helper()
	return domain.DataQualityIssue{ID: newIntegrationID(t), InstrumentID: instrumentID, RuleCode: ruleCode, Severity: "error", Summary: "DB014 fixture", DetectedAt: now, CreatedAt: now, UpdatedAt: now}
}

func deleteDB014Fixture(t *testing.T, ctx context.Context, admin *pgx.Conn, providerID, instrumentID, assetID domain.ID) {
	t.Helper()
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.data_quality_issues WHERE instrument_id = $1", instrumentID.UUID()); err != nil {
		t.Errorf("delete quality issue fixtures: %v", err)
	}
	deleteDB011Fixture(t, ctx, admin, providerID, instrumentID, assetID)
}
