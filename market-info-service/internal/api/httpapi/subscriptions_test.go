package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

func TestSubscriptionRoutesListWithAuthenticationAndScopedCursor(t *testing.T) {
	service, record := subscriptionHTTPFixture(t)
	service.page = application.SubscriptionPage{Items: []application.SubscriptionRecord{record}, NextAfterID: &record.Subscription.ID}
	handler := registeredSubscriptionHandler(t, service, application.PermissionOperationsRead)
	request := httptest.NewRequest(http.MethodGet, collectionSubscriptionsPath+"?provider=bybit&instrument_code=instrument.bybit.spot.btc-usdt&interval=1h&enabled=false&limit=1", nil)
	request.Header.Set(authorizationHeader, "Bearer read-token")
	request.Header.Set(RequestIDHeader, testRequestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.listInput.Limit != 1 || service.listInput.Enabled == nil || *service.listInput.Enabled || response.Header().Get(RequestIDHeader) != testRequestID {
		t.Fatalf("response=%d body=%s input=%#v", response.Code, response.Body.String(), service.listInput)
	}
	var body struct {
		Items []struct {
			SubscriptionID string `json:"subscription_id"`
			Provider       string `json:"provider"`
			CreatedAt      string `json:"created_at"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || body.Items[0].Provider != "bybit" || !strings.HasSuffix(body.Items[0].CreatedAt, "Z") || body.NextCursor == nil {
		t.Fatalf("list body = %#v, err=%v", body, err)
	}
	second := httptest.NewRequest(http.MethodGet, collectionSubscriptionsPath+"?provider=bybit&instrument_code=instrument.bybit.spot.btc-usdt&interval=1h&enabled=false&limit=1&cursor="+*body.NextCursor, nil)
	second.Header.Set(authorizationHeader, "Bearer read-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, second)
	if response.Code != http.StatusOK || service.listInput.AfterID == nil || *service.listInput.AfterID != record.Subscription.ID {
		t.Fatalf("cursor response=%d body=%s input=%#v", response.Code, response.Body.String(), service.listInput)
	}
}

func TestSubscriptionRoutesCreateAndPatchPreserveAuditContext(t *testing.T) {
	service, record := subscriptionHTTPFixture(t)
	service.record = record
	handler := registeredSubscriptionHandler(t, service, application.PermissionOperationsRead, application.PermissionSubscriptionsManage)
	createBody := `{"provider":"bybit","instrument_code":"instrument.bybit.spot.btc-usdt","interval":"1h","enabled":true,"priority":100,"close_delay_seconds":120,"revision_delay_seconds":null,"reason":"enable hourly collection"}`
	request := httptest.NewRequest(http.MethodPost, collectionSubscriptionsPath, strings.NewReader(createBody))
	request.Header.Set(authorizationHeader, "Bearer admin-token")
	request.Header.Set(RequestIDHeader, testRequestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || service.createInput.Reason != "enable hourly collection" || service.createAudit.RequestID() != testRequestID || service.createAudit.RequestedBy() != "admin@example.com" {
		t.Fatalf("create response=%d body=%s input=%#v audit=%#v", response.Code, response.Body.String(), service.createInput, service.createAudit)
	}
	patchBody := `{"enabled":false,"revision_delay_seconds":null,"reason":"pause collection"}`
	request = httptest.NewRequest(http.MethodPatch, collectionSubscriptionsPath+"/"+record.Subscription.ID.String(), strings.NewReader(patchBody))
	request.Header.Set(authorizationHeader, "Bearer admin-token")
	request.Header.Set(RequestIDHeader, testRequestID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.updateInput.Enabled == nil || *service.updateInput.Enabled || !service.updateInput.RevisionDelaySecondsSet || service.updateInput.RevisionDelaySeconds != nil || service.updateInput.ID != record.Subscription.ID || service.updateAudit.RequestID() != testRequestID {
		t.Fatalf("patch response=%d body=%s input=%#v audit=%#v", response.Code, response.Body.String(), service.updateInput, service.updateAudit)
	}
}

func TestSubscriptionRoutesEnforcePermissions(t *testing.T) {
	service, _ := subscriptionHTTPFixture(t)
	readOnly := registeredSubscriptionHandler(t, service, application.PermissionOperationsRead)
	request := httptest.NewRequest(http.MethodPost, collectionSubscriptionsPath, strings.NewReader(`{}`))
	request.Header.Set(authorizationHeader, "Bearer read-token")
	response := httptest.NewRecorder()
	readOnly.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || service.createCalls != 0 {
		t.Fatalf("read-only POST response=%d calls=%d", response.Code, service.createCalls)
	}
	response = httptest.NewRecorder()
	readOnly.ServeHTTP(response, httptest.NewRequest(http.MethodGet, collectionSubscriptionsPath, nil))
	if response.Code != http.StatusUnauthorized || service.listCalls != 0 {
		t.Fatalf("unauthenticated GET response=%d calls=%d", response.Code, service.listCalls)
	}
}

func TestSubscriptionRoutesRejectInvalidProtocolInput(t *testing.T) {
	service, record := subscriptionHTTPFixture(t)
	handler := registeredSubscriptionHandler(t, service, application.PermissionOperationsRead, application.PermissionSubscriptionsManage)
	badCursor, _ := EncodeCursor(collectionSubscriptionsCursorScope, "other", "", "", "", record.Subscription.ID.String())
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, collectionSubscriptionsPath + "?enabled=TRUE", ""},
		{http.MethodGet, collectionSubscriptionsPath + "?unknown=x", ""},
		{http.MethodGet, collectionSubscriptionsPath + "?provider=bybit&cursor=" + badCursor, ""},
		{http.MethodPatch, collectionSubscriptionsPath + "/not-a-uuid", `{}`},
		{http.MethodPatch, collectionSubscriptionsPath + "/" + record.Subscription.ID.String(), `{"provider":"bybit"}`},
		{http.MethodPost, collectionSubscriptionsPath, `{"provider":`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set(authorizationHeader, "Bearer admin-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s %s response=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func TestSubscriptionRoutesMapServiceErrorsAndRequireDependencies(t *testing.T) {
	if err := RegisterSubscriptionRoutes(nil, nil, nil); err == nil {
		t.Fatal("RegisterSubscriptionRoutes(nil) error = nil")
	}
	service, _ := subscriptionHTTPFixture(t)
	service.listErr = domain.ErrDatabaseUnavailable
	handler := registeredSubscriptionHandler(t, service, application.PermissionOperationsRead)
	request := httptest.NewRequest(http.MethodGet, collectionSubscriptionsPath, nil)
	request.Header.Set(authorizationHeader, "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("service error response=%d body=%s", response.Code, response.Body.String())
	}
}

type subscriptionHTTPServiceStub struct {
	page        application.SubscriptionPage
	record      application.SubscriptionRecord
	listInput   application.SubscriptionListInput
	createInput application.CreateSubscriptionInput
	updateInput application.UpdateSubscriptionInput
	createAudit application.AuditContext
	updateAudit application.AuditContext
	listErr     error
	createErr   error
	updateErr   error
	listCalls   int
	createCalls int
	updateCalls int
}

func (stub *subscriptionHTTPServiceStub) List(_ context.Context, input application.SubscriptionListInput) (application.SubscriptionPage, error) {
	stub.listCalls++
	stub.listInput = input
	return stub.page, stub.listErr
}
func (stub *subscriptionHTTPServiceStub) Create(ctx context.Context, input application.CreateSubscriptionInput) (application.SubscriptionRecord, error) {
	stub.createCalls++
	stub.createInput = input
	stub.createAudit, _ = application.AuditContextFromContext(ctx)
	return stub.record, stub.createErr
}
func (stub *subscriptionHTTPServiceStub) Update(ctx context.Context, input application.UpdateSubscriptionInput) (application.SubscriptionRecord, error) {
	stub.updateCalls++
	stub.updateInput = input
	stub.updateAudit, _ = application.AuditContextFromContext(ctx)
	return stub.record, stub.updateErr
}

func registeredSubscriptionHandler(t *testing.T, service *subscriptionHTTPServiceStub, permissions ...application.Permission) http.Handler {
	t.Helper()
	principal, err := application.NewPrincipal("admin@example.com", application.ActorTypeUser, permissions...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterSubscriptionRoutes(mux, service, &stubAuthenticator{principal: principal}); err != nil {
		t.Fatal(err)
	}
	return WithRequestID(mux)
}

func subscriptionHTTPFixture(t *testing.T) (*subscriptionHTTPServiceStub, application.SubscriptionRecord) {
	t.Helper()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	id := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897301"))
	mappingID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897302"))
	providerCode, _ := domain.ParseCode("bybit")
	instrumentCode, _ := domain.ParseCode("instrument.bybit.spot.btc-usdt")
	mappingCode, _ := domain.ParseCode("provider.bybit.spot.btcusdt")
	record := application.SubscriptionRecord{Subscription: domain.CollectionSubscription{
		ID: id, ProviderInstrumentID: mappingID, Interval: "1h", Enabled: true, Priority: 100,
		CloseDelaySeconds: 120, Metadata: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}, ProviderCode: providerCode, InstrumentCode: instrumentCode, ProviderInstrumentCode: mappingCode, ProviderSymbol: "BTCUSDT"}
	return &subscriptionHTTPServiceStub{record: record}, record
}
