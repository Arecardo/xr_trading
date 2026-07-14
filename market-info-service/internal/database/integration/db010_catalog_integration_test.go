//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"xr-trading/market-info-service/internal/application"
	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
)

func TestDB010CatalogAndProviderRepositoryAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, assetCode, instrumentCode := createCoreFixture(t, ctx, admin)
	t.Cleanup(func() { deleteCatalogFixture(t, context.Background(), admin, assetID, instrumentID) })

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
	repository, err := repositorypostgres.NewCatalogRepository(pool)
	if err != nil {
		t.Fatalf("NewCatalogRepository() error = %v", err)
	}

	asset, err := repository.FindAssetByCode(ctx, assetCode)
	if err != nil {
		t.Fatalf("FindAssetByCode() error = %v", err)
	}
	if asset.ID != assetID || asset.CanonicalSymbol != "BTC" {
		t.Fatalf("FindAssetByCode() = %#v", asset)
	}
	instrument, err := repository.FindInstrumentByCode(ctx, instrumentCode)
	if err != nil {
		t.Fatalf("FindInstrumentByCode() error = %v", err)
	}
	if instrument.ID != instrumentID || instrument.AssetID != assetID || instrument.QuoteCurrency != "USDT" {
		t.Fatalf("FindInstrumentByCode() = %#v", instrument)
	}
	if _, err := repository.FindAssetByCode(ctx, "asset.missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("FindAssetByCode(missing) error = %v, want not found", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	providerID := newIntegrationID(t)
	provider := domain.Provider{
		ID:           providerID,
		Code:         integrationCode(t, "bybit-db010-"+providerID.String()),
		Name:         "Bybit integration fixture",
		ProviderType: "EXCHANGE",
		Status:       "active",
		Metadata:     json.RawMessage(`{"environment":"test"}`),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repository.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	loadedProvider, err := repository.FindProviderByCode(ctx, provider.Code.String())
	if err != nil {
		t.Fatalf("FindProviderByCode() error = %v", err)
	}
	if loadedProvider.ID != providerID || loadedProvider.ProviderType != "EXCHANGE" {
		t.Fatalf("FindProviderByCode() = %#v", loadedProvider)
	}

	defaultMapping := domain.ProviderInstrument{
		ID:             newIntegrationID(t),
		Code:           integrationCode(t, "provider.bybit.spot.db010-"+providerID.String()),
		ProviderID:     providerID,
		InstrumentID:   instrumentID,
		ExternalSymbol: "BTCUSDT",
		ProviderMarket: "spot",
		Capabilities:   domain.ProviderCapabilities{Quote: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day}},
		Priority:       10,
		IsDefault:      true,
		Enabled:        true,
		Metadata:       json.RawMessage(`{}`),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := repository.CreateProviderInstrument(ctx, defaultMapping); err != nil {
		t.Fatalf("CreateProviderInstrument(default) error = %v", err)
	}
	secondMapping := defaultMapping
	secondMapping.ID = newIntegrationID(t)
	secondMapping.Code = integrationCode(t, "provider.bybit.spot.db010-secondary-"+providerID.String())
	secondMapping.ExternalSymbol = "BTCUSDT-SECONDARY"
	secondMapping.Priority = 20
	if err := repository.CreateProviderInstrument(ctx, secondMapping); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateProviderInstrument(second default) error = %v, want conflict", err)
	}
	secondMapping.IsDefault = false
	if err := repository.CreateProviderInstrument(ctx, secondMapping); err != nil {
		t.Fatalf("CreateProviderInstrument(second) error = %v", err)
	}

	mappings, err := repository.ListActiveProviderInstruments(ctx, instrumentID)
	if err != nil {
		t.Fatalf("ListActiveProviderInstruments() error = %v", err)
	}
	if len(mappings) != 2 || !mappings[0].IsDefault || mappings[0].ID != defaultMapping.ID || mappings[1].ID != secondMapping.ID {
		t.Fatalf("ListActiveProviderInstruments() = %#v", mappings)
	}
	optionsService, err := application.NewInstrumentOptionsService(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewInstrumentOptionsService() error = %v", err)
	}
	options, err := optionsService.List(ctx, application.InstrumentOptionsInput{AssetCode: assetCode, Limit: 10})
	if err != nil {
		t.Fatalf("InstrumentOptionsService.List() error = %v", err)
	}
	if len(options.Items) != 1 || options.Items[0].ID != instrumentID || len(options.Items[0].Providers) != 1 || options.Items[0].Providers[0].Code != provider.Code || !options.Items[0].Providers[0].IsDefault || len(options.Items[0].Providers[0].SupportedIntervals) != 2 {
		t.Fatalf("InstrumentOptionsService.List() = %#v", options)
	}
	if _, err := repository.ListActiveProviderInstruments(ctx, domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListActiveProviderInstruments(zero) error = %v, want invalid data", err)
	}
}

func openAdminConnection(t *testing.T, ctx context.Context) *pgx.Conn {
	t.Helper()
	url := os.Getenv("MARKET_INFO_TEST_ADMIN_DATABASE_URL")
	if url == "" {
		t.Skip("MARKET_INFO_TEST_ADMIN_DATABASE_URL is required for core catalog fixtures")
	}
	connection, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect test admin database: %v", err)
	}
	return connection
}

func createCoreFixture(t *testing.T, ctx context.Context, admin *pgx.Conn) (domain.ID, domain.ID, string, string) {
	t.Helper()
	assetID := newIntegrationID(t)
	instrumentID := newIntegrationID(t)
	assetCode := "asset.crypto.db010-" + assetID.String()
	instrumentCode := "instrument.bybit.spot.db010-" + instrumentID.String()
	symbol := "DB010-" + instrumentID.String()[:8]
	_, err := admin.Exec(ctx, `
INSERT INTO core.assets (id, code, asset_type, canonical_symbol, name)
VALUES ($1, $2, 'CRYPTO', 'BTC', 'DB010 Bitcoin')`, assetID.UUID(), assetCode)
	if err != nil {
		t.Fatalf("insert core asset fixture: %v", err)
	}
	_, err = admin.Exec(ctx, `
INSERT INTO core.instruments (
    id, code, asset_id, venue, instrument_type, symbol, quote_currency, market_timezone
) VALUES (
    $1, $2, $3, 'BYBIT', 'SPOT', $4, 'USDT', 'UTC'
)`, instrumentID.UUID(), instrumentCode, assetID.UUID(), symbol)
	if err != nil {
		t.Fatalf("insert core instrument fixture: %v", err)
	}
	return assetID, instrumentID, assetCode, instrumentCode
}

func deleteCatalogFixture(t *testing.T, ctx context.Context, admin *pgx.Conn, assetID, instrumentID domain.ID) {
	t.Helper()
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.provider_instruments WHERE instrument_id = $1", instrumentID.UUID()); err != nil {
		t.Errorf("delete provider instrument fixtures: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM market_data.providers WHERE code LIKE 'bybit-db010-%'"); err != nil {
		t.Errorf("delete provider fixture: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM core.instruments WHERE id = $1", instrumentID.UUID()); err != nil {
		t.Errorf("delete core instrument fixture: %v", err)
	}
	if _, err := admin.Exec(ctx, "DELETE FROM core.assets WHERE id = $1", assetID.UUID()); err != nil {
		t.Errorf("delete core asset fixture: %v", err)
	}
}

func newIntegrationID(t *testing.T) domain.ID {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	return id
}

func integrationCode(t *testing.T, value string) domain.Code {
	t.Helper()
	code, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return code
}
