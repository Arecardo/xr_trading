package scheduler

import (
	"errors"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
)

func TestUSFreshnessStopsDelayWheneverMarketIsClosed(t *testing.T) {
	calendar := testNYSECalendar(t)
	stale := mustTime(t, "2026-07-01T19:30:00Z")
	tests := []struct {
		name       string
		observedAt string
		wantNext   string
	}{
		{"overnight", "2026-07-02T21:00:00Z", "2026-07-06T13:30:00Z"},
		{"independence holiday", "2026-07-03T15:00:00Z", "2026-07-06T13:30:00Z"},
		{"weekend", "2026-07-04T15:00:00Z", "2026-07-06T13:30:00Z"},
		{"before open", "2026-07-06T13:29:59Z", "2026-07-06T13:30:00Z"},
		{"exact regular close", "2026-07-06T20:00:00Z", "2026-07-07T13:30:00Z"},
		{"exact early close", "2026-11-27T18:00:00Z", "2026-11-30T14:30:00Z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := CalculateUSFreshness(calendar, USFreshnessInput{
				ObservedAt: mustTime(t, test.observedAt), Interval: domain.BarInterval1Hour,
				CloseDelay: 2 * time.Minute, LastClosedOpenTime: &stale,
			})
			if err != nil {
				t.Fatalf("CalculateUSFreshness() error = %v", err)
			}
			if result.MarketState != MarketStateClosed || result.FreshnessStatus != FreshnessStatusNotApplicable ||
				result.DataDelaySeconds != nil || result.ExpectedOpenTime != nil || result.ExpectedCloseTime != nil ||
				result.NextMarketOpenAt == nil || *result.NextMarketOpenAt != mustTime(t, test.wantNext) {
				t.Fatalf("CalculateUSFreshness() = %#v", result)
			}
		})
	}
}

func TestUSHourlyFreshnessUsesDelayBoundaryAndTradingTime(t *testing.T) {
	calendar := testNYSECalendar(t)
	previousFinal := mustTime(t, "2026-07-02T19:30:00Z")
	tests := []struct {
		name       string
		observedAt string
		lastOpen   *time.Time
		wantStatus FreshnessStatus
		wantDelay  *int64
		wantOpen   string
	}{
		{"before first bar delay uses prior session", "2026-07-06T14:31:59.999999999Z", &previousFinal, FreshnessStatusFresh, int64Pointer(0), "2026-07-02T19:30:00Z"},
		{"exact delay boundary expects first current bar", "2026-07-06T14:32:00Z", &previousFinal, FreshnessStatusDelayed, int64Pointer(3600), "2026-07-06T13:30:00Z"},
		{"holiday and weekend do not add delay", "2026-07-06T14:40:00Z", &previousFinal, FreshnessStatusDelayed, int64Pointer(3600), "2026-07-06T13:30:00Z"},
		{"caught up", "2026-07-06T14:40:00Z", timePointer(mustTime(t, "2026-07-06T13:30:00Z")), FreshnessStatusFresh, int64Pointer(0), "2026-07-06T13:30:00Z"},
		{"no successful bar", "2026-07-06T14:40:00Z", nil, FreshnessStatusUnknown, nil, "2026-07-06T13:30:00Z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := CalculateUSFreshness(calendar, USFreshnessInput{
				ObservedAt: mustTime(t, test.observedAt), Interval: domain.BarInterval1Hour,
				CloseDelay: 2 * time.Minute, LastClosedOpenTime: test.lastOpen,
			})
			if err != nil {
				t.Fatalf("CalculateUSFreshness() error = %v", err)
			}
			if result.MarketState != MarketStateOpen || result.FreshnessStatus != test.wantStatus || result.NextMarketOpenAt != nil ||
				result.ExpectedOpenTime == nil || *result.ExpectedOpenTime != mustTime(t, test.wantOpen) || !equalInt64Pointers(result.DataDelaySeconds, test.wantDelay) {
				t.Fatalf("CalculateUSFreshness() = %#v", result)
			}
		})
	}
}

func TestUSDailyFreshnessCountsOnlyTradingSessions(t *testing.T) {
	calendar := testNYSECalendar(t)
	lastOpen := mustTime(t, "2026-07-01T00:00:00Z")
	result, err := CalculateUSFreshness(calendar, USFreshnessInput{
		ObservedAt: mustTime(t, "2026-07-06T15:00:00Z"), Interval: domain.BarInterval1Day,
		CloseDelay: 2 * time.Minute, LastClosedOpenTime: &lastOpen,
	})
	if err != nil {
		t.Fatalf("CalculateUSFreshness() error = %v", err)
	}
	if result.MarketState != MarketStateOpen || result.FreshnessStatus != FreshnessStatusDelayed ||
		result.ExpectedOpenTime == nil || *result.ExpectedOpenTime != mustTime(t, "2026-07-02T00:00:00Z") ||
		result.DataDelaySeconds == nil || *result.DataDelaySeconds != int64((6*time.Hour+30*time.Minute)/time.Second) {
		t.Fatalf("CalculateUSFreshness() = %#v", result)
	}
}

func TestUSFreshnessHandlesSupportedCalendarStartAndInvalidData(t *testing.T) {
	calendar := testNYSECalendar(t)
	result, err := CalculateUSFreshness(calendar, USFreshnessInput{
		ObservedAt: mustTime(t, "2026-01-02T14:45:00Z"), Interval: domain.BarInterval1Hour, CloseDelay: 2 * time.Minute,
	})
	if err != nil || result.MarketState != MarketStateOpen || result.FreshnessStatus != FreshnessStatusUnknown || result.ExpectedOpenTime != nil {
		t.Fatalf("CalculateUSFreshness(calendar start) = (%#v, %v)", result, err)
	}

	valid := USFreshnessInput{ObservedAt: mustTime(t, "2026-07-06T14:40:00Z"), Interval: domain.BarInterval1Hour, CloseDelay: 2 * time.Minute}
	invalidOpen := mustTime(t, "2026-07-06T13:31:00Z")
	futureOpen := mustTime(t, "2026-07-06T19:30:00Z")
	tests := []struct {
		name     string
		calendar markettime.TradingCalendar
		mutate   func(*USFreshnessInput)
		want     error
	}{
		{"nil calendar", nil, func(*USFreshnessInput) {}, nil},
		{"zero observed", calendar, func(value *USFreshnessInput) { value.ObservedAt = time.Time{} }, domain.ErrInvalidData},
		{"negative delay", calendar, func(value *USFreshnessInput) { value.CloseDelay = -time.Second }, domain.ErrInvalidData},
		{"unsupported interval", calendar, func(value *USFreshnessInput) { value.Interval = "5m" }, domain.ErrInvalidData},
		{"unaligned stored bar", calendar, func(value *USFreshnessInput) { value.LastClosedOpenTime = &invalidOpen }, domain.ErrInvalidData},
		{"stored bar not closed", calendar, func(value *USFreshnessInput) { value.LastClosedOpenTime = &futureOpen }, domain.ErrInvalidData},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, err := CalculateUSFreshness(test.calendar, input)
			if err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("CalculateUSFreshness() error = %v, want %v", err, test.want)
			}
		})
	}
}

func testNYSECalendar(t *testing.T) *markettime.NYSECalendar {
	t.Helper()
	calendar, err := markettime.NewNYSECalendar()
	if err != nil {
		t.Fatalf("NewNYSECalendar() error = %v", err)
	}
	return calendar
}

func int64Pointer(value int64) *int64 { return &value }

func timePointer(value time.Time) *time.Time { return &value }

func equalInt64Pointers(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}
