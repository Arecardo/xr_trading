//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
)

func TestDB011SubscriptionRepositoryAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, _ := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	t.Cleanup(func() { deleteDB011Fixture(t, context.Background(), admin, providerID, instrumentID, assetID) })

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{
		DatabaseURL:       integrationDatabaseURL(t),
		MaxConns:          2,
		MinConns:          0,
		MaxConnLifetime:   time.Minute,
		HealthCheckPeriod: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	defer pool.Close()
	catalog, err := repositorypostgres.NewCatalogRepository(pool)
	if err != nil {
		t.Fatalf("NewCatalogRepository() error = %v", err)
	}
	subscriptions, err := repositorypostgres.NewSubscriptionRepository(pool)
	if err != nil {
		t.Fatalf("NewSubscriptionRepository() error = %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	provider := domain.Provider{
		ID: providerID, Code: integrationCode(t, "bybit-db011-"+providerID.String()), Name: "DB011 Bybit",
		ProviderType: "EXCHANGE", Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	firstMapping := domain.ProviderInstrument{
		ID: newIntegrationID(t), Code: integrationCode(t, "provider.bybit.db011-"+providerID.String()), ProviderID: providerID,
		InstrumentID: instrumentID, ExternalSymbol: "DB011BTCUSDT", ProviderMarket: "spot",
		Capabilities: domain.ProviderCapabilities{Intervals: []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day}}, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	secondMapping := firstMapping
	secondMapping.ID = newIntegrationID(t)
	secondMapping.Code = integrationCode(t, "provider.bybit.db011-secondary-"+providerID.String())
	secondMapping.ExternalSymbol = "DB011BTCUSDTSECOND"
	if err := catalog.CreateProviderInstrument(ctx, firstMapping); err != nil {
		t.Fatalf("CreateProviderInstrument(first) error = %v", err)
	}
	if err := catalog.CreateProviderInstrument(ctx, secondMapping); err != nil {
		t.Fatalf("CreateProviderInstrument(second) error = %v", err)
	}

	first := domain.CollectionSubscription{
		ID: newIntegrationID(t), ProviderInstrumentID: firstMapping.ID, Interval: "1h", Enabled: true,
		Priority: 10, CloseDelaySeconds: 120, CreatedAt: now, UpdatedAt: now,
	}
	if err := subscriptions.CreateSubscription(ctx, first); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	if err := subscriptions.CreateSubscription(ctx, first); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateSubscription(duplicate) error = %v, want conflict", err)
	}
	second := domain.CollectionSubscription{
		ID: newIntegrationID(t), ProviderInstrumentID: secondMapping.ID, Interval: "1d", Enabled: true,
		Priority: 20, CloseDelaySeconds: 300, CreatedAt: now, UpdatedAt: now,
	}
	if err := subscriptions.CreateSubscription(ctx, second); err != nil {
		t.Fatalf("CreateSubscription(second) error = %v", err)
	}

	settings := domain.SubscriptionSettings{Enabled: false, Priority: 3, CloseDelaySeconds: 60}
	if err := subscriptions.UpdateSubscriptionSettings(ctx, first.ID, settings, now.Add(time.Second)); err != nil {
		t.Fatalf("UpdateSubscriptionSettings() error = %v", err)
	}
	loaded, err := subscriptions.GetSubscription(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetSubscription() error = %v", err)
	}
	if loaded.ProviderInstrumentID != firstMapping.ID || loaded.Interval != "1h" || loaded.Enabled || loaded.Priority != 3 || loaded.CloseDelaySeconds != 60 {
		t.Fatalf("GetSubscription() = %#v", loaded)
	}

	page, err := subscriptions.ListSubscriptions(ctx, domain.SubscriptionFilter{ProviderCode: provider.Code.String(), Limit: 1})
	if err != nil {
		t.Fatalf("ListSubscriptions() error = %v", err)
	}
	if len(page.Items) != 1 || page.NextAfterID == nil {
		t.Fatalf("ListSubscriptions() = %#v, want one item and cursor", page)
	}
	secondPage, err := subscriptions.ListSubscriptions(ctx, domain.SubscriptionFilter{ProviderCode: provider.Code.String(), AfterID: page.NextAfterID, Limit: 1})
	if err != nil || len(secondPage.Items) != 1 || secondPage.NextAfterID != nil {
		t.Fatalf("ListSubscriptions(next) = (%#v, %v)", secondPage, err)
	}
	if _, err := subscriptions.ListSubscriptions(ctx, domain.SubscriptionFilter{InstrumentCode: "missing", Limit: 1}); err != nil {
		t.Fatalf("ListSubscriptions(empty) error = %v", err)
	}
}

func deleteDB011Fixture(t *testing.T, ctx context.Context, admin *pgx.Conn, providerID, instrumentID, assetID domain.ID) {
	t.Helper()
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.collection_subscriptions WHERE provider_instrument_id IN (SELECT id FROM market_data.provider_instruments WHERE instrument_id = $1)", instrumentID.UUID()); err != nil {
		t.Errorf("delete subscription fixtures: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.provider_instruments WHERE instrument_id = $1", instrumentID.UUID()); err != nil {
		t.Errorf("delete provider instrument fixtures: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.providers WHERE id = $1", providerID.UUID()); err != nil {
		t.Errorf("delete provider fixture: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM core.instruments WHERE id = $1", instrumentID.UUID()); err != nil {
		t.Errorf("delete core instrument fixture: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM core.assets WHERE id = $1", assetID.UUID()); err != nil {
		t.Errorf("delete core asset fixture: %v", err)
	}
}
