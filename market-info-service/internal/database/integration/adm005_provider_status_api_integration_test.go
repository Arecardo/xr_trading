//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"xr-trading/market-info-service/internal/api/httpapi"
	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/auth"
	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
	"xr-trading/market-info-service/internal/scheduler"
)

func TestADM005ProviderStatusHTTPAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, _ := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	providerCode := integrationCode(t, "bybit-adm005-"+providerID.String())
	t.Cleanup(func() { cleanupADM005Fixture(t, context.Background(), admin, providerID, instrumentID, assetID) })

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{DatabaseURL: integrationDatabaseURL(t), MaxConns: 3, MinConns: 0, MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second})
	if err != nil {
		t.Fatalf("OpenPool() error=%v", err)
	}
	defer pool.Close()
	catalog, _ := repositorypostgres.NewCatalogRepository(pool)
	subscriptions, _ := repositorypostgres.NewSubscriptionRepository(pool)
	ingestionRepository, _ := repositorypostgres.NewIngestionRepository(pool)
	checkedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	provider := domain.Provider{ID: providerID, Code: providerCode, Name: "ADM005 Bybit", ProviderType: domain.ProviderTypeExchange, Status: domain.ProviderStatusActive, CreatedAt: checkedAt, UpdatedAt: checkedAt}
	mapping := domain.ProviderInstrument{
		ID: newIntegrationID(t), Code: integrationCode(t, "provider.bybit.adm005-"+providerID.String()), ProviderID: providerID,
		InstrumentID: instrumentID, ExternalSymbol: "ADM005BTCUSDT", ProviderMarket: "spot",
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}},
		Enabled:      true, CreatedAt: checkedAt, UpdatedAt: checkedAt,
	}
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err := catalog.CreateProviderInstrument(ctx, mapping); err != nil {
		t.Fatal(err)
	}
	subscription := domain.CollectionSubscription{ID: newIntegrationID(t), ProviderInstrumentID: mapping.ID, Interval: "1h", Enabled: true, Priority: 1, CloseDelaySeconds: 120, CreatedAt: checkedAt, UpdatedAt: checkedAt}
	if err := subscriptions.CreateSubscription(ctx, subscription); err != nil {
		t.Fatal(err)
	}
	latest, err := scheduler.CalculateLatestContinuousWindow(domain.BarInterval1Hour, scheduler.WindowTriggerClose, checkedAt, 2*time.Minute)
	if err != nil || latest == nil {
		t.Fatalf("latest continuous window=(%#v,%v)", latest, err)
	}
	lastSuccess := checkedAt.Add(-time.Minute)
	if err := ingestionRepository.UpsertCheckpoint(ctx, domain.IngestionCheckpoint{
		SubscriptionID: subscription.ID, LastSuccessOpenTime: &latest.RangeStart, LastClosedOpenTime: &latest.RangeStart,
		LastAttemptAt: &lastSuccess, LastSuccessAt: &lastSuccess, UpdatedAt: lastSuccess,
	}); err != nil {
		t.Fatalf("UpsertCheckpoint() error=%v", err)
	}

	calendar, _ := markettime.NewNYSECalendar()
	service, err := application.NewProviderStatusService(ingestionRepository, func() time.Time { return checkedAt }, calendar)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := application.NewPrincipal("adm005@example.com", application.ActorTypeUser, application.PermissionOperationsRead)
	authenticator, _ := auth.NewStaticBearerAuthenticator([]auth.StaticCredential{{Token: "adm005-secret", Principal: principal}})
	mux := http.NewServeMux()
	if err := httpapi.RegisterProviderStatusRoutes(mux, service, authenticator); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.WithRequestID(mux)

	healthy := adm005Get(t, handler, "req_"+newIntegrationID(t).String(), http.StatusOK)
	if !strings.Contains(healthy.Body.String(), `"provider_code":"`+providerCode.String()+`"`) ||
		!strings.Contains(healthy.Body.String(), `"health_status":"healthy"`) ||
		!strings.Contains(healthy.Body.String(), `"market":"crypto_spot"`) ||
		!strings.Contains(healthy.Body.String(), `"freshness_status":"fresh"`) ||
		!strings.Contains(healthy.Body.String(), `"data_delay_seconds":0`) {
		t.Fatalf("healthy response=%s", healthy.Body.String())
	}

	staleOpen := latest.RangeStart.Add(-4 * time.Hour)
	if _, err := admin.Exec(ctx, `UPDATE market_data.ingestion_checkpoints
SET last_success_open_time = $1, last_closed_open_time = $1, consecutive_failures = 3, updated_at = $2
WHERE subscription_id = $3`, staleOpen, checkedAt, subscription.ID.UUID()); err != nil {
		t.Fatal(err)
	}
	unhealthy := adm005Get(t, handler, "req_"+newIntegrationID(t).String(), http.StatusOK)
	if !strings.Contains(unhealthy.Body.String(), `"provider_code":"`+providerCode.String()+`"`) || !strings.Contains(unhealthy.Body.String(), `"health_status":"unhealthy"`) || !strings.Contains(unhealthy.Body.String(), `"consecutive_failures":3`) || !strings.Contains(unhealthy.Body.String(), `"delayed_subscriptions":1`) {
		t.Fatalf("unhealthy response=%s", unhealthy.Body.String())
	}

	if _, err := admin.Exec(ctx, `UPDATE market_data.providers SET status = 'disabled', updated_at = $1 WHERE id = $2`, checkedAt, providerID.UUID()); err != nil {
		t.Fatal(err)
	}
	disabled := adm005Get(t, handler, "req_"+newIntegrationID(t).String(), http.StatusOK)
	providerPosition := strings.Index(disabled.Body.String(), `"provider_code":"`+providerCode.String()+`"`)
	if providerPosition < 0 || !strings.Contains(disabled.Body.String()[providerPosition:], `"configured_status":"disabled","health_status":"unknown"`) {
		t.Fatalf("disabled response=%s", disabled.Body.String())
	}

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/market-info/v1/providers/status", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d", unauthenticated.Code)
	}
}

func adm005Get(t *testing.T, handler http.Handler, requestID string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/market-info/v1/providers/status", nil)
	request.Header.Set("Authorization", "Bearer adm005-secret")
	request.Header.Set(httpapi.RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus || response.Header().Get(httpapi.RequestIDHeader) != requestID {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	return response
}

func cleanupADM005Fixture(t *testing.T, ctx context.Context, admin *pgx.Conn, providerID, instrumentID, assetID domain.ID) {
	t.Helper()
	_, _ = admin.Exec(ctx, `DELETE FROM market_data.ingestion_checkpoints WHERE subscription_id IN (
    SELECT subscriptions.id FROM market_data.collection_subscriptions AS subscriptions
    JOIN market_data.provider_instruments AS mappings ON mappings.id = subscriptions.provider_instrument_id
    WHERE mappings.provider_id = $1
)`, providerID.UUID())
	deleteDB011Fixture(t, ctx, admin, providerID, instrumentID, assetID)
}
