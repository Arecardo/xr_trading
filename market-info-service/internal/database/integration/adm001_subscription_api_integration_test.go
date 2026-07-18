//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/api/httpapi"
	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/auth"
	poolpostgres "xr-trading/market-info-service/internal/database/postgres"
	"xr-trading/market-info-service/internal/domain"
	repositorypostgres "xr-trading/market-info-service/internal/repository/postgres"
)

func TestADM001SubscriptionManagementHTTPAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin := openAdminConnection(t, ctx)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	assetID, instrumentID, _, instrumentCode := createCoreFixture(t, ctx, admin)
	providerID := newIntegrationID(t)
	t.Cleanup(func() { deleteDB011Fixture(t, context.Background(), admin, providerID, instrumentID, assetID) })

	pool, err := poolpostgres.OpenPool(ctx, poolpostgres.Config{
		DatabaseURL: integrationDatabaseURL(t), MaxConns: 3, MinConns: 0,
		MaxConnLifetime: time.Minute, HealthCheckPeriod: time.Second,
	})
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	defer pool.Close()
	catalog, _ := repositorypostgres.NewCatalogRepository(pool)
	subscriptions, _ := repositorypostgres.NewSubscriptionRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	provider := domain.Provider{
		ID: providerID, Code: integrationCode(t, "bybit-adm001-"+providerID.String()), Name: "ADM001 Bybit",
		ProviderType: domain.ProviderTypeExchange, Status: domain.ProviderStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	mapping := domain.ProviderInstrument{
		ID: newIntegrationID(t), Code: integrationCode(t, "provider.bybit.adm001-"+providerID.String()),
		ProviderID: provider.ID, InstrumentID: instrumentID, ExternalSymbol: "ADM001BTCUSDT", ProviderMarket: "spot",
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}},
		Enabled:      true, CreatedAt: now, UpdatedAt: now,
	}
	if err := catalog.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if err := catalog.CreateProviderInstrument(ctx, mapping); err != nil {
		t.Fatalf("CreateProviderInstrument() error = %v", err)
	}

	service, err := application.NewSubscriptionService(subscriptions, subscriptions, func() time.Time { return now }, domain.NewID)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := application.NewPrincipal("adm001@example.com", application.ActorTypeUser, application.PermissionOperationsRead, application.PermissionSubscriptionsManage)
	authenticator, _ := auth.NewStaticBearerAuthenticator([]auth.StaticCredential{{Token: "adm001-secret", Principal: principal}})
	mux := http.NewServeMux()
	if err := httpapi.RegisterSubscriptionRoutes(mux, service, authenticator); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.WithRequestID(mux)

	createJSON := `{"provider":"` + provider.Code.String() + `","instrument_code":"` + instrumentCode + `","interval":"1h","enabled":true,"priority":100,"close_delay_seconds":120,"revision_delay_seconds":300,"reason":"start hourly collection"}`
	created := adm001Serve(t, handler, http.MethodPost, "/api/market-info/v1/collection-subscriptions", createJSON, "req_019f1452-90f7-7992-a87a-ca2727897401", http.StatusCreated)
	var createResponse struct {
		Subscription struct {
			SubscriptionID         string `json:"subscription_id"`
			Provider               string `json:"provider"`
			InstrumentCode         string `json:"instrument_code"`
			ProviderInstrumentID   string `json:"provider_instrument_id"`
			ProviderInstrumentCode string `json:"provider_instrument_code"`
			Enabled                bool   `json:"enabled"`
		} `json:"subscription"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createResponse); err != nil {
		t.Fatal(err)
	}
	if createResponse.Subscription.Provider != provider.Code.String() || createResponse.Subscription.InstrumentCode != instrumentCode || createResponse.Subscription.ProviderInstrumentID != mapping.ID.String() || createResponse.Subscription.ProviderInstrumentCode != mapping.Code.String() || !createResponse.Subscription.Enabled {
		t.Fatalf("create response = %#v", createResponse)
	}

	duplicate := adm001Serve(t, handler, http.MethodPost, "/api/market-info/v1/collection-subscriptions", createJSON, "req_019f1452-90f7-7992-a87a-ca2727897402", http.StatusConflict)
	if !strings.Contains(duplicate.Body.String(), `"code":"SUBSCRIPTION_ALREADY_EXISTS"`) {
		t.Fatalf("duplicate response = %s", duplicate.Body.String())
	}
	unsupportedJSON := strings.Replace(createJSON, `"interval":"1h"`, `"interval":"1d"`, 1)
	unsupported := adm001Serve(t, handler, http.MethodPost, "/api/market-info/v1/collection-subscriptions", unsupportedJSON, "req_019f1452-90f7-7992-a87a-ca2727897403", http.StatusBadRequest)
	if !strings.Contains(unsupported.Body.String(), `"code":"UNSUPPORTED_INTERVAL"`) {
		t.Fatalf("unsupported response = %s", unsupported.Body.String())
	}

	query := url.Values{"provider": {provider.Code.String()}, "instrument_code": {instrumentCode}, "interval": {"1h"}, "enabled": {"true"}, "limit": {"1"}}
	listed := adm001Serve(t, handler, http.MethodGet, "/api/market-info/v1/collection-subscriptions?"+query.Encode(), "", "req_019f1452-90f7-7992-a87a-ca2727897404", http.StatusOK)
	if !strings.Contains(listed.Body.String(), createResponse.Subscription.SubscriptionID) || !strings.Contains(listed.Body.String(), mapping.Code.String()) {
		t.Fatalf("list response = %s", listed.Body.String())
	}

	patchJSON := `{"enabled":false,"priority":7,"revision_delay_seconds":null,"reason":"pause noisy source"}`
	patched := adm001Serve(t, handler, http.MethodPatch, "/api/market-info/v1/collection-subscriptions/"+createResponse.Subscription.SubscriptionID, patchJSON, "req_019f1452-90f7-7992-a87a-ca2727897405", http.StatusOK)
	if !strings.Contains(patched.Body.String(), `"enabled":false`) || !strings.Contains(patched.Body.String(), `"priority":7`) || !strings.Contains(patched.Body.String(), `"revision_delay_seconds":null`) {
		t.Fatalf("patch response = %s", patched.Body.String())
	}

	var providerInstrumentID string
	var interval string
	var enabled bool
	var auditLog []byte
	if err := admin.QueryRow(ctx, `SELECT provider_instrument_id::text, interval, enabled, metadata -> 'audit_log'
FROM market_data.collection_subscriptions WHERE id = $1`, createResponse.Subscription.SubscriptionID).Scan(&providerInstrumentID, &interval, &enabled, &auditLog); err != nil {
		t.Fatalf("read subscription audit: %v", err)
	}
	var entries []domain.SubscriptionAuditEntry
	if err := json.Unmarshal(auditLog, &entries); err != nil || len(entries) != 2 {
		t.Fatalf("audit log = %s, entries=%#v, err=%v", auditLog, entries, err)
	}
	if providerInstrumentID != mapping.ID.String() || interval != "1h" || enabled || entries[0].Action != "create" || entries[0].RequestID != "req_019f1452-90f7-7992-a87a-ca2727897401" || entries[1].Action != "update" || entries[1].Reason != "pause noisy source" {
		t.Fatalf("persisted identity/settings/audit = (%s, %s, %t, %#v)", providerInstrumentID, interval, enabled, entries)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/market-info/v1/collection-subscriptions", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated response = %d %s", response.Code, response.Body.String())
	}
}

func adm001Serve(t *testing.T, handler http.Handler, method, path, body, requestID string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer adm001-secret")
	request.Header.Set(httpapi.RequestIDHeader, requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus || response.Header().Get(httpapi.RequestIDHeader) != requestID {
		t.Fatalf("%s %s response=%d headers=%v body=%s", method, path, response.Code, response.Header(), response.Body.String())
	}
	return response
}
