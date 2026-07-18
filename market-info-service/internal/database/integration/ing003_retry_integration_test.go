//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
	"xr-trading/market-info-service/internal/ingestion/ports"
	"xr-trading/market-info-service/internal/providers"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
)

type ing003Adapter struct {
	providerCode domain.Code
	failure      error
}

func (adapter *ing003Adapter) ProviderCode() domain.Code { return adapter.providerCode }
func (adapter *ing003Adapter) Capabilities(context.Context) (ports.ProviderCapabilities, error) {
	return ports.ProviderCapabilities{ProviderCode: adapter.providerCode, Markets: []ports.ProviderMarketCapability{{
		ProviderMarket: "spot", AssetTypes: []domain.AssetType{domain.AssetTypeCrypto}, InstrumentTypes: []domain.InstrumentType{domain.InstrumentTypeSpot},
		SupportsQuote: true, SupportsBars: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}, MaxBatchSize: 10, MaxBarsPerRequest: 100,
	}}}, nil
}
func (*ing003Adapter) FetchLatestQuotes(context.Context, []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	return nil, errors.New("not implemented")
}
func (adapter *ing003Adapter) FetchBars(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	return ports.FetchBarsResult{}, adapter.failure
}

func TestING003RetryWaitThenNonRetryableFailureAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, _ := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	now := time.Now().UTC().Truncate(time.Second)
	provider := domain.Provider{
		ID: providerID, Code: integrationCode(t, "bybit-ing003-"+providerID.String()), Name: "ING003 Bybit",
		ProviderType: domain.ProviderTypeExchange, Status: domain.ProviderStatusActive, CreatedAt: now, UpdatedAt: now,
	}

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{DatabaseURL: integrationDatabaseURL(t), MaxConns: 5, MinConns: 0, MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	defer pool.Close()
	catalog, _ := repositorypostgres.NewCatalogRepository(pool)
	subscriptions, _ := repositorypostgres.NewSubscriptionRepository(pool)
	store, _ := repositorypostgres.NewIngestionRepository(pool)
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	mapping := db012Mapping(t, providerID, instrumentID, now, "ing003")
	if err := catalog.CreateProviderInstrument(ctx, mapping); err != nil {
		t.Fatalf("CreateProviderInstrument() error = %v", err)
	}
	subscription := domain.CollectionSubscription{
		ID: newIntegrationID(t), ProviderInstrumentID: mapping.ID, Interval: "1h", Enabled: true,
		Priority: 1, CloseDelaySeconds: 0, CreatedAt: now, UpdatedAt: now,
	}
	if err := subscriptions.CreateSubscription(ctx, subscription); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	run, task := db013RunTask(t, subscription.ID, now, "ing003")
	// ClaimNextTask intentionally has no test-only scope. Make this fixture the
	// oldest eligible task so a developer database containing unrelated pending
	// rows cannot be mutated by this integration test.
	fixtureOrder := time.Unix(0, 0).UTC()
	run.CreatedAt = fixtureOrder
	task.CreatedAt = fixtureOrder
	task.UpdatedAt = fixtureOrder
	if err := store.CreateRunWithTask(ctx, run, task); err != nil {
		t.Fatalf("CreateRunWithTask() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupING002Fixture(t, context.Background(), admin, run.ID, subscription.ID, providerID, mapping.ID, instrumentID, assetID)
	})

	networkFailure, _ := ports.NewProviderError(provider.Code, ports.ProviderErrorNetwork, "provider network request failed", nil, errors.New("secret socket detail"))
	adapter := &ing003Adapter{providerCode: provider.Code, failure: networkFailure}
	registry, err := providers.NewRegistry(ctx, adapter)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	claim, err := store.ClaimNextTask(ctx, "worker-ing003", now, time.Minute)
	if err != nil || claim == nil || claim.Task.ID != task.ID {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
	firstFinishedAt := now.Add(time.Second)
	service, _ := ingestion.NewService(ingestion.Config{BarsPerPage: 100, MaximumPages: 10}, store, registry, ingestion.NewStructuralBarQualityValidator(), func() time.Time { return firstFinishedAt })
	executionErr := service.ExecuteTask(ctx, *claim)
	if !errors.Is(executionErr, networkFailure) || strings.Contains(executionErr.Error(), "commit ingestion failure") || strings.Contains(executionErr.Error(), "transition ingestion failure") {
		t.Fatalf("ExecuteTask(network) error = %v", executionErr)
	}

	var status string
	var errorCode, errorMessage *string
	var attemptCount, consecutiveFailures int
	var nextAttemptAt *time.Time
	var lastAttemptAt time.Time
	var lockedBy, finishedAt *time.Time
	var errorDetails []byte
	if err := admin.QueryRow(ctx, `SELECT status, attempt_count, next_attempt_at, locked_until, finished_at,
error_code, error_message, error_details FROM market_data.ingestion_tasks WHERE id = $1`, task.ID.UUID()).Scan(
		&status, &attemptCount, &nextAttemptAt, &lockedBy, &finishedAt, &errorCode, &errorMessage, &errorDetails,
	); err != nil {
		t.Fatalf("query retry task: %v", err)
	}
	if status != "retry_wait" || attemptCount != 1 || nextAttemptAt == nil || !nextAttemptAt.Equal(firstFinishedAt.Add(time.Minute)) || lockedBy != nil || finishedAt != nil || errorCode == nil || *errorCode != "network" || errorMessage == nil || *errorMessage != "provider network request failed" || string(errorDetails) != `{"provider_code": "`+provider.Code.String()+`"}` {
		t.Fatalf("retry task status=%s attempt=%d next=%v lock=%v finish=%v code=%v message=%v details=%s", status, attemptCount, nextAttemptAt, lockedBy, finishedAt, errorCode, errorMessage, errorDetails)
	}
	if err := admin.QueryRow(ctx, "SELECT last_attempt_at, consecutive_failures FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", subscription.ID.UUID()).Scan(&lastAttemptAt, &consecutiveFailures); err != nil {
		t.Fatalf("query failure checkpoint: %v", err)
	}
	if !lastAttemptAt.Equal(firstFinishedAt) || consecutiveFailures != 1 {
		t.Fatalf("checkpoint lastAttempt=%v failures=%d", lastAttemptAt, consecutiveFailures)
	}

	secondClaimedAt := *nextAttemptAt
	claim, err = store.ClaimNextTask(ctx, "worker-ing003", secondClaimedAt, time.Minute)
	if err != nil || claim == nil || claim.Task.ID != task.ID || claim.Task.AttemptCount != 2 {
		t.Fatalf("ClaimNextTask(retry) = (%#v, %v)", claim, err)
	}
	unauthorized, _ := ports.NewProviderError(provider.Code, ports.ProviderErrorUnauthorized, "provider authorization was rejected", nil, errors.New("expired secret"))
	adapter.failure = unauthorized
	secondFinishedAt := secondClaimedAt.Add(time.Second)
	service, _ = ingestion.NewService(ingestion.Config{BarsPerPage: 100, MaximumPages: 10}, store, registry, ingestion.NewStructuralBarQualityValidator(), func() time.Time { return secondFinishedAt })
	if err := service.ExecuteTask(ctx, *claim); !errors.Is(err, unauthorized) {
		t.Fatalf("ExecuteTask(unauthorized) error = %v", err)
	}
	var terminalFinishedAt time.Time
	if err := admin.QueryRow(ctx, `SELECT status, attempt_count, finished_at, error_code
FROM market_data.ingestion_tasks WHERE id = $1`, task.ID.UUID()).Scan(&status, &attemptCount, &terminalFinishedAt, &errorCode); err != nil {
		t.Fatalf("query failed task: %v", err)
	}
	if status != "failed" || attemptCount != 2 || !terminalFinishedAt.Equal(secondFinishedAt) || errorCode == nil || *errorCode != "unauthorized" {
		t.Fatalf("failed task status=%s attempt=%d finish=%v code=%v", status, attemptCount, terminalFinishedAt, errorCode)
	}
	if err := admin.QueryRow(ctx, "SELECT last_attempt_at, consecutive_failures FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", subscription.ID.UUID()).Scan(&lastAttemptAt, &consecutiveFailures); err != nil {
		t.Fatalf("query terminal checkpoint: %v", err)
	}
	if !lastAttemptAt.Equal(secondFinishedAt) || consecutiveFailures != 2 {
		t.Fatalf("terminal checkpoint lastAttempt=%v failures=%d", lastAttemptAt, consecutiveFailures)
	}
	var barCount int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM market_data.market_bars WHERE provider_instrument_id = $1", mapping.ID.UUID()).Scan(&barCount); err != nil || barCount != 0 {
		t.Fatalf("failed task bars=%d error=%v", barCount, err)
	}
}
