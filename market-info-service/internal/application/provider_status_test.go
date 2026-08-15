package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
	"xr-trading/market-info-service/internal/scheduler"
)

type providerStatusReaderStub struct {
	sources []ProviderStatusSource
	err     error
	at      time.Time
}

func (stub *providerStatusReaderStub) ListProviderStatusSources(_ context.Context, at time.Time) ([]ProviderStatusSource, error) {
	stub.at = at
	return stub.sources, stub.err
}

func TestProviderStatusServiceProjectsContinuousAndClosedUSScopes(t *testing.T) {
	checkedAt := providerStatusTime(t, "2026-07-05T12:00:00Z") // Sunday.
	cryptoExpectedOpen := providerStatusTime(t, "2026-07-05T10:00:00Z")
	priorUSBar := providerStatusTime(t, "2026-07-02T19:30:00Z")
	lastSuccess := checkedAt.Add(-time.Hour)
	lastFailure := checkedAt.Add(-2 * time.Hour)
	reader := &providerStatusReaderStub{sources: []ProviderStatusSource{
		providerStatusSource("bybit", domain.ProviderStatusActive, ProviderSubscriptionObservation{
			SubscriptionID: providerStatusID("019f1452-90f7-7992-a87a-ca2727898401"), ProviderMarket: "spot",
			AssetType: domain.AssetTypeCrypto, Interval: domain.BarInterval1Hour, CloseDelaySeconds: 120,
			LastClosedOpenTime: &cryptoExpectedOpen, LastSuccessAt: &lastSuccess,
		}),
		providerStatusSource("longbridge", domain.ProviderStatusActive, ProviderSubscriptionObservation{
			SubscriptionID: providerStatusID("019f1452-90f7-7992-a87a-ca2727898402"), ProviderMarket: "us",
			AssetType: domain.AssetTypeStock, Interval: domain.BarInterval1Hour, CloseDelaySeconds: 120,
			LastClosedOpenTime: &priorUSBar, LastSuccessAt: &lastSuccess, LastFailureAt: &lastFailure,
		}),
		providerStatusSource("disabled-source", domain.ProviderStatusDisabled),
	}}
	calendar, _ := markettime.NewNYSECalendar()
	service, err := NewProviderStatusService(reader, func() time.Time { return checkedAt.In(time.FixedZone("east", 8*60*60)) }, calendar)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := service.List(context.Background())
	if err != nil || len(statuses) != 3 || !reader.at.Equal(checkedAt) || reader.at.Location() != time.UTC {
		t.Fatalf("List() = (%#v, %v), at=%v", statuses, err, reader.at)
	}
	bybit := statuses[0]
	if bybit.ProviderCode.String() != "bybit" || bybit.HealthStatus != ProviderHealthHealthy || len(bybit.Scopes) != 1 ||
		bybit.Scopes[0].Market != "crypto_spot" || bybit.Scopes[0].SessionType != "continuous" ||
		bybit.Scopes[0].FreshnessStatus != scheduler.FreshnessStatusFresh || bybit.Scopes[0].DataDelaySeconds == nil || *bybit.Scopes[0].DataDelaySeconds != 0 {
		t.Fatalf("bybit = %#v", bybit)
	}
	disabled := statuses[1]
	if disabled.ProviderCode.String() != "disabled-source" || disabled.HealthStatus != ProviderHealthUnknown || len(disabled.Scopes) != 0 {
		t.Fatalf("disabled = %#v", disabled)
	}
	longbridge := statuses[2]
	if longbridge.HealthStatus != ProviderHealthHealthy || longbridge.LastFailureAt == nil || len(longbridge.Scopes) != 1 ||
		longbridge.Scopes[0].MarketState != "closed" || longbridge.Scopes[0].FreshnessStatus != scheduler.FreshnessStatusNotApplicable ||
		longbridge.Scopes[0].DataDelaySeconds != nil || longbridge.Scopes[0].NextMarketOpenAt == nil {
		t.Fatalf("longbridge = %#v", longbridge)
	}
}

func TestProviderStatusServiceAggregatesDelayFailuresAndConfiguredDegraded(t *testing.T) {
	checkedAt := providerStatusTime(t, "2026-07-06T14:40:00Z")
	delayed := providerStatusTime(t, "2026-07-06T09:00:00Z")
	fresh := providerStatusTime(t, "2026-07-06T13:00:00Z")
	success := checkedAt.Add(-time.Minute)
	source := providerStatusSource("bybit", domain.ProviderStatusDegraded,
		ProviderSubscriptionObservation{SubscriptionID: providerStatusID("019f1452-90f7-7992-a87a-ca2727898411"), ProviderMarket: "spot", AssetType: domain.AssetTypeCrypto, Interval: domain.BarInterval1Hour, CloseDelaySeconds: 120, LastClosedOpenTime: &delayed, LastSuccessAt: &success},
		ProviderSubscriptionObservation{SubscriptionID: providerStatusID("019f1452-90f7-7992-a87a-ca2727898412"), ProviderMarket: "spot", AssetType: domain.AssetTypeCrypto, Interval: domain.BarInterval1Hour, CloseDelaySeconds: 120, LastClosedOpenTime: &fresh, LastSuccessAt: &success, ConsecutiveFailures: 3},
	)
	calendar, _ := markettime.NewNYSECalendar()
	service, _ := NewProviderStatusService(&providerStatusReaderStub{sources: []ProviderStatusSource{source}}, func() time.Time { return checkedAt }, calendar)
	statuses, err := service.List(context.Background())
	if err != nil || len(statuses) != 1 {
		t.Fatalf("List() = (%#v, %v)", statuses, err)
	}
	status := statuses[0]
	if status.HealthStatus != ProviderHealthUnhealthy || status.ConsecutiveFailures != 3 || len(status.Scopes) != 1 ||
		status.Scopes[0].HealthStatus != ProviderHealthUnhealthy || status.Scopes[0].DelayedSubscriptions != 1 ||
		status.Scopes[0].DataDelaySeconds == nil || *status.Scopes[0].DataDelaySeconds != 4*3600 {
		t.Fatalf("status = %#v", status)
	}
}

func TestProviderStatusServiceMarksCalendarRangeAsUnhealthy(t *testing.T) {
	checkedAt := providerStatusTime(t, "2029-07-06T14:40:00Z")
	source := providerStatusSource("longbridge", domain.ProviderStatusActive, ProviderSubscriptionObservation{
		SubscriptionID: providerStatusID("019f1452-90f7-7992-a87a-ca2727898421"), ProviderMarket: "us",
		AssetType: domain.AssetTypeETF, Interval: domain.BarInterval1Hour, CloseDelaySeconds: 120,
	})
	calendar, _ := markettime.NewNYSECalendar()
	service, _ := NewProviderStatusService(&providerStatusReaderStub{sources: []ProviderStatusSource{source}}, func() time.Time { return checkedAt }, calendar)
	statuses, err := service.List(context.Background())
	if err != nil || statuses[0].HealthStatus != ProviderHealthUnhealthy || statuses[0].Scopes[0].MarketState != "unknown" || statuses[0].Scopes[0].FreshnessStatus != scheduler.FreshnessStatusUnknown {
		t.Fatalf("List(calendar out of range) = (%#v, %v)", statuses, err)
	}
}

func TestProviderStatusServiceProjectsFXAsContinuousScope(t *testing.T) {
	checkedAt := providerStatusTime(t, "2026-07-06T14:40:00Z")
	fresh := providerStatusTime(t, "2026-07-06T14:00:00Z")
	source := providerStatusSource("coingecko", domain.ProviderStatusActive, ProviderSubscriptionObservation{
		SubscriptionID: providerStatusID("019f1452-90f7-7992-a87a-ca2727898461"), ProviderMarket: "fx",
		AssetType: domain.AssetTypeFX, Interval: domain.BarInterval1Day, CloseDelaySeconds: 120,
		LastClosedOpenTime: &fresh, LastSuccessAt: &fresh,
	})
	calendar, _ := markettime.NewNYSECalendar()
	// This exercises the exact regression risk documented on providerScopeFor:
	// an unrecognized AssetType previously made List() fail entirely (for
	// every provider, not only the offending one), so a live CoinGecko FX
	// subscription must not do that.
	service, err := NewProviderStatusService(&providerStatusReaderStub{sources: []ProviderStatusSource{source}}, func() time.Time { return checkedAt }, calendar)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := service.List(context.Background())
	if err != nil || len(statuses) != 1 {
		t.Fatalf("List() = (%#v, %v)", statuses, err)
	}
	status := statuses[0]
	if status.ProviderCode.String() != "coingecko" || len(status.Scopes) != 1 ||
		status.Scopes[0].Market != "fx_fx" || status.Scopes[0].SessionType != "continuous" {
		t.Fatalf("coingecko status = %#v", status)
	}
}

func TestProviderStatusServiceValidatesAndMapsReaderErrors(t *testing.T) {
	calendar, _ := markettime.NewNYSECalendar()
	if _, err := NewProviderStatusService(nil, nil, nil); err == nil {
		t.Fatal("NewProviderStatusService(nil) error = nil")
	}
	service, _ := NewProviderStatusService(&providerStatusReaderStub{}, time.Now, calendar)
	if _, err := service.List(nil); err == nil {
		t.Fatal("List(nil) error = nil")
	}
	for _, test := range []struct {
		source ProviderStatusSource
		want   error
	}{
		{ProviderStatusSource{}, domain.ErrInvalidData},
		{providerStatusSource("bybit", domain.ProviderStatusActive, ProviderSubscriptionObservation{}), domain.ErrInvalidData},
	} {
		service, _ := NewProviderStatusService(&providerStatusReaderStub{sources: []ProviderStatusSource{test.source}}, time.Now, calendar)
		if _, err := service.List(context.Background()); err == nil {
			t.Fatalf("List(%#v) error=nil", test.source)
		}
	}
	for _, value := range []error{domain.ErrDatabaseUnavailable, domain.ErrRetryable, errors.New("bad row")} {
		service, _ := NewProviderStatusService(&providerStatusReaderStub{err: value}, time.Now, calendar)
		if _, err := service.List(context.Background()); err == nil {
			t.Fatalf("List(reader=%v) error=nil", value)
		}
	}
}

func providerStatusSource(code string, status domain.ProviderStatus, observations ...ProviderSubscriptionObservation) ProviderStatusSource {
	parsedCode, _ := domain.ParseCode(code)
	return ProviderStatusSource{
		ProviderID: providerStatusID(map[string]string{
			"bybit": "019f1452-90f7-7992-a87a-ca2727898451", "longbridge": "019f1452-90f7-7992-a87a-ca2727898452",
			"disabled-source": "019f1452-90f7-7992-a87a-ca2727898453", "coingecko": "019f1452-90f7-7992-a87a-ca2727898454",
		}[code]),
		ProviderCode: parsedCode, DisplayName: code, ProviderType: domain.ProviderTypeExchange,
		ConfiguredStatus: status, Subscriptions: observations,
	}
}
func providerStatusID(value string) domain.ID { return domain.IDFromUUID(uuid.MustParse(value)) }
func providerStatusTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
