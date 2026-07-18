package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/scheduler"
)

type providerStatusHTTPStub struct {
	items []application.ProviderStatus
	err   error
	calls int
}

func (stub *providerStatusHTTPStub) List(context.Context) ([]application.ProviderStatus, error) {
	stub.calls++
	return stub.items, stub.err
}

func TestProviderStatusRouteReturnsOperationalProjection(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	lastSuccess, nextOpen := now.Add(-time.Minute), now.Add(time.Hour)
	providerCode, _ := domain.ParseCode("longbridge")
	service := &providerStatusHTTPStub{items: []application.ProviderStatus{{
		ProviderID: providerStatusHTTPID("019f1452-90f7-7992-a87a-ca2727898601"), ProviderCode: providerCode,
		DisplayName: "Longbridge", ProviderType: domain.ProviderTypeBroker, ConfiguredStatus: domain.ProviderStatusActive,
		HealthStatus: application.ProviderHealthHealthy, LastSuccessAt: &lastSuccess, CheckedAt: now,
		Scopes: []application.ProviderScopeStatus{{
			Market: "us_equity", SessionType: "regular", Interval: domain.BarInterval1Hour,
			MarketState: "closed", HealthStatus: application.ProviderHealthHealthy,
			FreshnessStatus: scheduler.FreshnessStatusNotApplicable, ActiveSubscriptions: 2, NextMarketOpenAt: &nextOpen,
		}},
	}}}
	handler := registeredProviderStatusHandler(t, service, application.PermissionOperationsRead)
	request := httptest.NewRequest(http.MethodGet, providerStatusPath, nil)
	request.Header.Set(authorizationHeader, "Bearer admin-token")
	request.Header.Set(RequestIDHeader, testRequestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.calls != 1 || response.Header().Get(RequestIDHeader) != testRequestID {
		t.Fatalf("response=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
	}
	for _, expected := range []string{`"provider_code":"longbridge"`, `"provider_type":"broker"`, `"health_status":"healthy"`, `"market_state":"closed"`, `"freshness_status":"not_applicable"`, `"data_delay_seconds":null`, `"next_market_open_at":"2026-07-19T13:00:00Z"`} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("response missing %s: %s", expected, response.Body.String())
		}
	}
}

func TestProviderStatusRouteEnforcesReadPermissionAndNoQuery(t *testing.T) {
	service := &providerStatusHTTPStub{}
	writeOnly := registeredProviderStatusHandler(t, service, application.PermissionIngestionManage)
	request := httptest.NewRequest(http.MethodGet, providerStatusPath, nil)
	response := httptest.NewRecorder()
	writeOnly.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, providerStatusPath, nil)
	request.Header.Set(authorizationHeader, "Bearer admin-token")
	response = httptest.NewRecorder()
	writeOnly.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("forbidden=%d calls=%d", response.Code, service.calls)
	}

	read := registeredProviderStatusHandler(t, service, application.PermissionOperationsRead)
	request = httptest.NewRequest(http.MethodGet, providerStatusPath+"?probe=true", nil)
	request.Header.Set(authorizationHeader, "Bearer admin-token")
	response = httptest.NewRecorder()
	read.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.calls != 0 {
		t.Fatalf("query=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
	}
	if err := RegisterProviderStatusRoutes(nil, nil, nil); err == nil {
		t.Fatal("RegisterProviderStatusRoutes(nil) error=nil")
	}
}

func TestProviderStatusRouteMapsServiceAndInvalidResultErrors(t *testing.T) {
	for _, test := range []struct {
		service    *providerStatusHTTPStub
		wantStatus int
		wantCode   string
	}{
		{&providerStatusHTTPStub{err: application.NewError(application.ErrorCodeDatabaseUnavailable, "database unavailable", true, nil)}, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE"},
		{&providerStatusHTTPStub{items: []application.ProviderStatus{{}}}, http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		handler := registeredProviderStatusHandler(t, test.service, application.PermissionOperationsRead)
		request := httptest.NewRequest(http.MethodGet, providerStatusPath, nil)
		request.Header.Set(authorizationHeader, "Bearer admin-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
			t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func registeredProviderStatusHandler(t *testing.T, service *providerStatusHTTPStub, permissions ...application.Permission) http.Handler {
	t.Helper()
	principal, err := application.NewPrincipal("admin@example.com", application.ActorTypeUser, permissions...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterProviderStatusRoutes(mux, service, &stubAuthenticator{principal: principal}); err != nil {
		t.Fatal(err)
	}
	return WithRequestID(mux)
}

func providerStatusHTTPID(value string) domain.ID { return domain.IDFromUUID(uuid.MustParse(value)) }
