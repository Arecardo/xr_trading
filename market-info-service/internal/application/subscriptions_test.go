package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
)

func TestSubscriptionServiceListsStablePage(t *testing.T) {
	service, stub, fixture := subscriptionServiceFixture(t)
	second := fixture.record
	second.Subscription.ID = domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897109"))
	stub.records = []SubscriptionRecord{fixture.record, second}
	enabled := true
	page, err := service.List(context.Background(), SubscriptionListInput{ProviderCode: "bybit", InstrumentCode: fixture.record.InstrumentCode.String(), Interval: "1h", Enabled: &enabled, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextAfterID == nil || *page.NextAfterID != fixture.record.Subscription.ID {
		t.Fatalf("List() = (%#v, %v)", page, err)
	}
	if stub.listFilter.Limit != 2 || stub.listFilter.ProviderCode != "bybit" || stub.listFilter.Enabled == nil || !*stub.listFilter.Enabled {
		t.Fatalf("list filter = %#v", stub.listFilter)
	}
}

func TestSubscriptionServiceCreatesAuditedSubscription(t *testing.T) {
	service, stub, fixture := subscriptionServiceFixture(t)
	ctx := subscriptionAuditContext(t)
	revision := 300
	record, err := service.Create(ctx, CreateSubscriptionInput{
		ProviderCode: "bybit", InstrumentCode: fixture.source.InstrumentCode.String(), Interval: "1h",
		Enabled: true, Priority: 100, CloseDelaySeconds: 120, RevisionDelaySeconds: &revision, Reason: "collect hourly bars",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if stub.created.ID != fixture.newID || record.Subscription.ID != fixture.newID || stub.sourceProvider != "bybit" || !stub.sourceAt.Equal(fixture.now) {
		t.Fatalf("created=%#v record=%#v source=(%s,%s)", stub.created, record, stub.sourceProvider, stub.sourceInstrument)
	}
	if !strings.Contains(string(stub.created.Metadata), `"action":"create"`) || !strings.Contains(string(stub.created.Metadata), `"request_id":"req_subscriptions_test"`) || !strings.Contains(string(stub.created.Metadata), `"reason":"collect hourly bars"`) {
		t.Fatalf("audit metadata = %s", stub.created.Metadata)
	}
	if stub.created.RevisionDelaySeconds == nil || *stub.created.RevisionDelaySeconds != revision || record.ProviderInstrumentCode != fixture.source.ProviderInstrumentCode {
		t.Fatalf("created settings/source = %#v / %#v", stub.created, record)
	}
}

func TestSubscriptionServiceCreateRejectsConflictCapabilityAndSourceState(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*subscriptionServiceStub, *subscriptionFixture)
		wantCode ErrorCode
	}{
		{"duplicate", func(stub *subscriptionServiceStub, _ *subscriptionFixture) { stub.createErr = domain.ErrConflict }, ErrorCodeSubscriptionAlreadyExists},
		{"unsupported interval", func(_ *subscriptionServiceStub, fixture *subscriptionFixture) {
			fixture.source.Capabilities.Intervals = []domain.BarInterval{domain.BarInterval1Day}
		}, ErrorCodeUnsupportedInterval},
		{"disabled source", func(_ *subscriptionServiceStub, fixture *subscriptionFixture) { fixture.source.Enabled = false }, ErrorCodeConflict},
		{"ambiguous source", func(stub *subscriptionServiceStub, fixture *subscriptionFixture) {
			stub.sources = []SubscriptionSource{fixture.source, fixture.source}
		}, ErrorCodeInvalidArgument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, stub, fixture := subscriptionServiceFixture(t)
			test.mutate(stub, &fixture)
			if len(stub.sources) != 2 {
				stub.sources = []SubscriptionSource{fixture.source}
			}
			_, err := service.Create(subscriptionAuditContext(t), CreateSubscriptionInput{ProviderCode: "bybit", InstrumentCode: fixture.source.InstrumentCode.String(), Interval: "1h", Enabled: true, Priority: 1, CloseDelaySeconds: 0, Reason: "test"})
			var appErr *Error
			if !errors.As(err, &appErr) || appErr.Code != test.wantCode {
				t.Fatalf("Create() error = %#v, want %s", err, test.wantCode)
			}
		})
	}
}

func TestSubscriptionServiceUpdatesOnlyMutableSettingsAndAudit(t *testing.T) {
	service, stub, fixture := subscriptionServiceFixture(t)
	enabled := false
	priority := 7
	revision := 600
	record, err := service.Update(subscriptionAuditContext(t), UpdateSubscriptionInput{
		ID: fixture.record.Subscription.ID, Enabled: &enabled, Priority: &priority,
		RevisionDelaySeconds: &revision, RevisionDelaySecondsSet: true, Reason: "slow the revision pass",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if stub.updatedID != fixture.record.Subscription.ID || stub.settings.Enabled || stub.settings.Priority != 7 || stub.settings.CloseDelaySeconds != fixture.record.Subscription.CloseDelaySeconds || stub.settings.RevisionDelaySeconds == nil || *stub.settings.RevisionDelaySeconds != revision {
		t.Fatalf("updated settings = %#v", stub.settings)
	}
	if stub.audit.Action != "update" || stub.audit.Reason != "slow the revision pass" || stub.audit.RequestedBy != "admin@example.com" || !record.Subscription.UpdatedAt.Equal(fixture.now) {
		t.Fatalf("audit/record = %#v / %#v", stub.audit, record)
	}
	if record.Subscription.ProviderInstrumentID != fixture.record.Subscription.ProviderInstrumentID || record.Subscription.Interval != "1h" {
		t.Fatal("immutable subscription identity changed")
	}
}

func TestSubscriptionServiceUpdateClearsRevisionAndMapsMissing(t *testing.T) {
	service, stub, fixture := subscriptionServiceFixture(t)
	result, err := service.Update(subscriptionAuditContext(t), UpdateSubscriptionInput{ID: fixture.record.Subscription.ID, RevisionDelaySecondsSet: true, Reason: "disable revision pass"})
	if err != nil || result.Subscription.RevisionDelaySeconds != nil || stub.settings.RevisionDelaySeconds != nil {
		t.Fatalf("Update(clear) = (%#v, %v), settings=%#v", result, err, stub.settings)
	}
	stub.getErr = domain.ErrNotFound
	_, err = service.Update(subscriptionAuditContext(t), UpdateSubscriptionInput{ID: fixture.record.Subscription.ID, RevisionDelaySecondsSet: true, Reason: "missing"})
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != ErrorCodeSubscriptionNotFound {
		t.Fatalf("Update(missing) error = %#v", err)
	}
}

func TestSubscriptionServiceValidatesInputsAndDependencies(t *testing.T) {
	if _, err := NewSubscriptionService(nil, nil, nil, nil); err == nil {
		t.Fatal("NewSubscriptionService(nil) error = nil")
	}
	service, _, fixture := subscriptionServiceFixture(t)
	for _, input := range []SubscriptionListInput{
		{ProviderCode: "BAD", Limit: 1}, {InstrumentCode: "asset.crypto.btc", Limit: 1}, {Interval: "5m", Limit: 1}, {Limit: 0}, {Limit: 101}, {AfterID: &domain.ID{}, Limit: 1},
	} {
		if _, err := service.List(context.Background(), input); err == nil {
			t.Fatalf("List(%#v) error = nil", input)
		}
	}
	invalidCreates := []CreateSubscriptionInput{
		{}, {ProviderCode: "BAD", InstrumentCode: "bad", Interval: "5m", Priority: -1, CloseDelaySeconds: -1, Reason: " bad"},
		{ProviderCode: "bybit", InstrumentCode: fixture.source.InstrumentCode.String(), Interval: "1h", Priority: 32768, Reason: strings.Repeat("r", 513)},
		{ProviderCode: "bybit", InstrumentCode: fixture.source.InstrumentCode.String(), Interval: "1h", CloseDelaySeconds: int(int64(1) << 32), Reason: "too large"},
	}
	for _, input := range invalidCreates {
		if _, err := service.Create(subscriptionAuditContext(t), input); err == nil {
			t.Fatalf("Create(%#v) error = nil", input)
		}
	}
	if _, err := service.Create(context.Background(), CreateSubscriptionInput{ProviderCode: "bybit", InstrumentCode: fixture.source.InstrumentCode.String(), Interval: "1h", Reason: "no audit"}); err == nil {
		t.Fatal("Create(no audit) error = nil")
	}
	if _, err := service.Update(subscriptionAuditContext(t), UpdateSubscriptionInput{ID: fixture.record.Subscription.ID, Reason: "empty patch"}); err == nil {
		t.Fatal("Update(empty patch) error = nil")
	}
	negative := -1
	if _, err := service.Update(subscriptionAuditContext(t), UpdateSubscriptionInput{ID: fixture.record.Subscription.ID, Priority: &negative, Reason: "bad"}); err == nil {
		t.Fatal("Update(negative) error = nil")
	}
}

func TestSubscriptionServiceClassifiesReaderAndInvalidProjectionFailures(t *testing.T) {
	service, stub, fixture := subscriptionServiceFixture(t)
	stub.listErr = domain.ErrDatabaseUnavailable
	if _, err := service.List(context.Background(), SubscriptionListInput{Limit: 1}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("List(database unavailable) error = %v", err)
	}
	stub.listErr = nil
	bad := fixture.record
	bad.ProviderSymbol = ""
	stub.records = []SubscriptionRecord{bad}
	if _, err := service.List(context.Background(), SubscriptionListInput{Limit: 1}); err == nil {
		t.Fatal("List(invalid projection) error = nil")
	}
	stub.records = nil
	stub.sourceErr = domain.ErrRetryable
	if _, err := service.Create(subscriptionAuditContext(t), CreateSubscriptionInput{ProviderCode: "bybit", InstrumentCode: fixture.source.InstrumentCode.String(), Interval: "1h", Reason: "retry"}); !errors.Is(err, domain.ErrRetryable) {
		t.Fatalf("Create(retryable) error = %v", err)
	}
}

type subscriptionServiceStub struct {
	records          []SubscriptionRecord
	sources          []SubscriptionSource
	record           SubscriptionRecord
	listFilter       SubscriptionReadFilter
	created          domain.CollectionSubscription
	settings         domain.SubscriptionSettings
	audit            domain.SubscriptionAuditEntry
	updatedID        domain.ID
	sourceProvider   string
	sourceInstrument string
	sourceAt         time.Time
	listErr          error
	sourceErr        error
	getErr           error
	createErr        error
	updateErr        error
}

func (stub *subscriptionServiceStub) CreateSubscription(_ context.Context, subscription domain.CollectionSubscription) error {
	stub.created = subscription
	return stub.createErr
}
func (stub *subscriptionServiceStub) GetSubscription(context.Context, domain.ID) (domain.CollectionSubscription, error) {
	return stub.record.Subscription, stub.getErr
}
func (stub *subscriptionServiceStub) ListSubscriptions(context.Context, domain.SubscriptionFilter) (domain.SubscriptionPage, error) {
	return domain.SubscriptionPage{}, nil
}
func (stub *subscriptionServiceStub) UpdateSubscriptionSettings(_ context.Context, id domain.ID, settings domain.SubscriptionSettings, audit domain.SubscriptionAuditEntry) error {
	stub.updatedID, stub.settings, stub.audit = id, settings, audit
	return stub.updateErr
}
func (stub *subscriptionServiceStub) ListSubscriptionRecords(_ context.Context, filter SubscriptionReadFilter) ([]SubscriptionRecord, error) {
	stub.listFilter = filter
	return stub.records, stub.listErr
}
func (stub *subscriptionServiceStub) FindSubscriptionSources(_ context.Context, provider, instrument string, effectiveAt time.Time) ([]SubscriptionSource, error) {
	stub.sourceProvider, stub.sourceInstrument, stub.sourceAt = provider, instrument, effectiveAt
	return stub.sources, stub.sourceErr
}
func (stub *subscriptionServiceStub) GetSubscriptionRecord(context.Context, domain.ID) (SubscriptionRecord, error) {
	return stub.record, stub.getErr
}

type subscriptionFixture struct {
	now    time.Time
	newID  domain.ID
	source SubscriptionSource
	record SubscriptionRecord
}

func subscriptionServiceFixture(t *testing.T) (*SubscriptionService, *subscriptionServiceStub, subscriptionFixture) {
	t.Helper()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	providerID := subscriptionID(t, "019f1452-90f7-7992-a87a-ca2727897101")
	instrumentID := subscriptionID(t, "019f1452-90f7-7992-a87a-ca2727897102")
	mappingID := subscriptionID(t, "019f1452-90f7-7992-a87a-ca2727897103")
	subscriptionIDValue := subscriptionID(t, "019f1452-90f7-7992-a87a-ca2727897104")
	newID := subscriptionID(t, "019f1452-90f7-7992-a87a-ca2727897105")
	providerCode := subscriptionCode(t, "bybit")
	instrumentCode := subscriptionCode(t, "instrument.bybit.spot.btc-usdt")
	mappingCode := subscriptionCode(t, "provider.bybit.spot.btcusdt")
	revision := 300
	source := SubscriptionSource{
		ProviderID: providerID, ProviderCode: providerCode, ProviderStatus: domain.ProviderStatusActive,
		InstrumentID: instrumentID, InstrumentCode: instrumentCode, InstrumentStatus: domain.InstrumentStatusActive,
		ProviderInstrumentID: mappingID, ProviderInstrumentCode: mappingCode, ProviderSymbol: "BTCUSDT",
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day}}, Enabled: true,
	}
	record := SubscriptionRecord{Subscription: domain.CollectionSubscription{
		ID: subscriptionIDValue, ProviderInstrumentID: mappingID, Interval: "1h", Enabled: true,
		Priority: 100, CloseDelaySeconds: 120, RevisionDelaySeconds: &revision,
		Metadata: []byte(`{}`), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}, ProviderCode: providerCode, InstrumentCode: instrumentCode, ProviderInstrumentCode: mappingCode, ProviderSymbol: "BTCUSDT"}
	stub := &subscriptionServiceStub{sources: []SubscriptionSource{source}, sourceProvider: providerCode.String(), record: record}
	service, err := NewSubscriptionService(stub, stub, func() time.Time { return now }, func() (domain.ID, error) { return newID, nil })
	if err != nil {
		t.Fatalf("NewSubscriptionService() error = %v", err)
	}
	return service, stub, subscriptionFixture{now: now, newID: newID, source: source, record: record}
}

func subscriptionAuditContext(t *testing.T) context.Context {
	t.Helper()
	principal, err := NewPrincipal("admin@example.com", ActorTypeUser, PermissionSubscriptionsManage, PermissionOperationsRead)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := NewAuditContext(principal, "req_subscriptions_test")
	if err != nil {
		t.Fatal(err)
	}
	return WithAuditContext(WithPrincipal(context.Background(), principal), audit)
}

func subscriptionID(t *testing.T, value string) domain.ID {
	t.Helper()
	return domain.IDFromUUID(uuid.MustParse(value))
}

func subscriptionCode(t *testing.T, value string) domain.Code {
	t.Helper()
	code, err := domain.ParseCode(value)
	if err != nil {
		t.Fatal(err)
	}
	return code
}
