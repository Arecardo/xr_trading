//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
	"xr-trading/market-info-service/internal/ingestion/ports"
	"xr-trading/market-info-service/internal/providers"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
)

type ing002Adapter struct{ providerCode domain.Code }

func (adapter *ing002Adapter) ProviderCode() domain.Code { return adapter.providerCode }

func (adapter *ing002Adapter) Capabilities(context.Context) (ports.ProviderCapabilities, error) {
	return ports.ProviderCapabilities{ProviderCode: adapter.providerCode, Markets: []ports.ProviderMarketCapability{{
		ProviderMarket: "spot", AssetTypes: []domain.AssetType{domain.AssetTypeCrypto}, InstrumentTypes: []domain.InstrumentType{domain.InstrumentTypeSpot},
		SupportsQuote: true, SupportsBars: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}, MaxBatchSize: 10, MaxBarsPerRequest: 100,
	}}}, nil
}

func (*ing002Adapter) FetchLatestQuotes(context.Context, []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	return nil, errors.New("not implemented")
}

func (*ing002Adapter) FetchBars(_ context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	openTime := request.StartTime
	closeTime, err := domain.NewUTCInstant(openTime.Time().Add(time.Hour))
	if err != nil {
		return ports.FetchBarsResult{}, err
	}
	receivedAt, err := domain.NewUTCInstant(request.EndTime.Time().Add(time.Second))
	if err != nil {
		return ports.FetchBarsResult{}, err
	}
	bar := ports.ProviderBar{
		ProviderInstrumentID: request.Instrument.ProviderInstrumentID, InstrumentID: request.Instrument.InstrumentID,
		AssetID: request.Instrument.AssetID, ProviderCode: request.Instrument.ProviderCode, Interval: request.Interval,
		OpenTime: openTime, CloseTime: closeTime, Open: domain.DecimalFromExact(decimal.NewFromInt(100)),
		High: domain.DecimalFromExact(decimal.NewFromInt(110)), Low: domain.DecimalFromExact(decimal.NewFromInt(90)),
		Close: domain.DecimalFromExact(decimal.NewFromInt(105)), IsClosed: true, ReceivedAt: receivedAt,
		RawPayload: json.RawMessage(`{"fixture":"ing002"}`),
	}
	return ports.FetchBarsResult{Bars: []ports.ProviderBar{bar}}, nil
}

func TestING002TaskExecutesAndCommitsAtomicallyAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, _ := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	now := time.Now().UTC().Truncate(time.Second)
	provider := domain.Provider{
		ID: providerID, Code: integrationCode(t, "bybit-ing002-"+providerID.String()), Name: "ING002 Bybit",
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
	mapping := db012Mapping(t, providerID, instrumentID, now, "ing002")
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
	run, task := db013RunTask(t, subscription.ID, now, "ing002")
	if err := store.CreateRunWithTask(ctx, run, task); err != nil {
		t.Fatalf("CreateRunWithTask() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupING002Fixture(t, context.Background(), admin, run.ID, subscription.ID, providerID, mapping.ID, instrumentID, assetID)
	})
	claim, err := store.ClaimNextTask(ctx, "worker-ing002", now, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimNextTask() = (%#v, %v)", claim, err)
	}
	registry, err := providers.NewRegistry(ctx, &ing002Adapter{providerCode: provider.Code})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	service, err := ingestion.NewService(
		ingestion.Config{BarsPerPage: 100, MaximumPages: 10}, store, registry,
		ingestion.NewStructuralBarQualityValidator(), func() time.Time { return now.Add(time.Second) },
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.ExecuteTask(ctx, *claim); err != nil {
		t.Fatalf("ExecuteTask() error = %v", err)
	}

	var status string
	var attemptCount int
	var lockedBy *string
	if err := admin.QueryRow(ctx, "SELECT status, attempt_count, locked_by FROM market_data.ingestion_tasks WHERE id = $1", task.ID.UUID()).Scan(&status, &attemptCount, &lockedBy); err != nil {
		t.Fatalf("query completed task: %v", err)
	}
	if status != "success" || attemptCount != 1 || lockedBy != nil {
		t.Fatalf("completed task status=%s attempt=%d lockedBy=%v", status, attemptCount, lockedBy)
	}
	var barCount, revision int
	var quality string
	if err := admin.QueryRow(ctx, "SELECT count(*), max(revision), max(quality_status) FROM market_data.market_bars WHERE provider_instrument_id = $1", mapping.ID.UUID()).Scan(&barCount, &revision, &quality); err != nil {
		t.Fatalf("query committed bars: %v", err)
	}
	if barCount != 1 || revision != 1 || quality != "valid" {
		t.Fatalf("committed bars count=%d revision=%d quality=%s", barCount, revision, quality)
	}
	var lastSuccessOpen, lastAttempt time.Time
	var consecutiveFailures int
	if err := admin.QueryRow(ctx, "SELECT last_success_open_time, last_attempt_at, consecutive_failures FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", subscription.ID.UUID()).Scan(&lastSuccessOpen, &lastAttempt, &consecutiveFailures); err != nil {
		t.Fatalf("query checkpoint: %v", err)
	}
	if !lastSuccessOpen.Equal(task.RangeStart) || !lastAttempt.Equal(now.Add(time.Second)) || consecutiveFailures != 0 {
		t.Fatalf("checkpoint lastOpen=%s lastAttempt=%s failures=%d", lastSuccessOpen, lastAttempt, consecutiveFailures)
	}
}

func cleanupING002Fixture(t *testing.T, ctx context.Context, admin interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, runID, subscriptionID, providerID, mappingID, instrumentID, assetID domain.ID) {
	t.Helper()
	statements := []struct {
		query string
		arg   any
	}{
		{"DELETE FROM market_data.data_quality_issues WHERE provider_instrument_id = $1", mappingID.UUID()},
		{"DELETE FROM market_data.market_bars WHERE provider_instrument_id = $1", mappingID.UUID()},
		{"DELETE FROM market_data.ingestion_checkpoints WHERE subscription_id = $1", subscriptionID.UUID()},
		{"DELETE FROM market_data.ingestion_tasks WHERE run_id = $1", runID.UUID()},
		{"DELETE FROM market_data.ingestion_runs WHERE id = $1", runID.UUID()},
		{"DELETE FROM market_data.collection_subscriptions WHERE id = $1", subscriptionID.UUID()},
		{"DELETE FROM market_data.provider_instruments WHERE id = $1", mappingID.UUID()},
		{"DELETE FROM market_data.providers WHERE id = $1", providerID.UUID()},
		{"DELETE FROM core.instruments WHERE id = $1", instrumentID.UUID()},
		{"DELETE FROM core.assets WHERE id = $1", assetID.UUID()},
	}
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement.query, statement.arg); err != nil {
			t.Errorf("cleanup ING002 fixture: %v", err)
		}
	}
}
