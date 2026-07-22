package observability

import (
	"testing"
	"time"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/scheduler"
)

func TestEvaluateOperationalAlertsUsesFixedThresholdsAndSkipsClosedMarket(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	oldest := observedAt.Add(-TaskBacklogAgeThreshold)
	hourlyThreshold := int64(DataDelayIntervalMultiplier * time.Hour / time.Second)
	dailyThreshold := int64(DataDelayIntervalMultiplier * 24 * time.Hour / time.Second)
	provider := metricProvider("longbridge", application.ProviderHealthUnhealthy)
	provider.ConsecutiveFailures = ProviderFailureAlertThreshold
	provider.Scopes = []application.ProviderScopeStatus{
		{Market: "crypto_spot", SessionType: "continuous", Interval: domain.BarInterval1Hour, MarketState: "open", FreshnessStatus: scheduler.FreshnessStatusDelayed, DataDelaySeconds: &hourlyThreshold},
		{Market: "us_equity", SessionType: "regular", Interval: domain.BarInterval1Day, MarketState: "closed", FreshnessStatus: scheduler.FreshnessStatusNotApplicable, DataDelaySeconds: &dailyThreshold},
	}
	snapshot := OperationalSnapshot{
		ObservedAt: observedAt,
		Tasks:      TaskMetricsSnapshot{Counts: map[string]int64{"pending": 1}, OldestBacklogCreatedAt: &oldest},
		Providers:  []application.ProviderStatus{provider},
	}
	alerts, err := EvaluateOperationalAlerts(false, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 4 {
		t.Fatalf("alerts = %#v", alerts)
	}
	wanted := map[AlertCode]bool{
		AlertReadyFailed: false, AlertProviderFailures: false,
		AlertProviderDataDelay: false, AlertIngestionTaskBacklog: false,
	}
	for _, alert := range alerts {
		wanted[alert.Code] = true
		if alert.Code == AlertProviderDataDelay && (alert.Market != "crypto_spot" || alert.Interval != domain.BarInterval1Hour || alert.Value != float64(hourlyThreshold)) {
			t.Fatalf("data delay alert = %#v", alert)
		}
	}
	for code, seen := range wanted {
		if !seen {
			t.Fatalf("alert %s missing from %#v", code, alerts)
		}
	}
}

func TestEvaluateOperationalAlertsDoesNotFireBelowThresholds(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	oldest := observedAt.Add(-TaskBacklogAgeThreshold + time.Second)
	delay := int64(DataDelayIntervalMultiplier*time.Hour/time.Second) - 1
	provider := metricProvider("bybit", application.ProviderHealthDegraded)
	provider.ConsecutiveFailures = ProviderFailureAlertThreshold - 1
	provider.Scopes = []application.ProviderScopeStatus{{
		Market: "crypto_spot", SessionType: "continuous", Interval: domain.BarInterval1Hour,
		MarketState: "open", FreshnessStatus: scheduler.FreshnessStatusDelayed, DataDelaySeconds: &delay,
	}}
	snapshot := OperationalSnapshot{
		ObservedAt: observedAt,
		Tasks:      TaskMetricsSnapshot{Counts: map[string]int64{"pending": TaskBacklogCountThreshold - 1}, OldestBacklogCreatedAt: &oldest},
		Providers:  []application.ProviderStatus{provider},
	}
	alerts, err := EvaluateOperationalAlerts(true, snapshot)
	if err != nil || len(alerts) != 0 {
		t.Fatalf("EvaluateOperationalAlerts() = (%#v, %v)", alerts, err)
	}
	if _, err := EvaluateOperationalAlerts(true, OperationalSnapshot{}); err == nil {
		t.Fatal("EvaluateOperationalAlerts(zero) error = nil")
	}
}
