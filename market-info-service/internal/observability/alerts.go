package observability

import (
	"errors"
	"sort"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/scheduler"
)

const (
	ProviderFailureAlertThreshold = 3
	TaskBacklogCountThreshold     = 100
	TaskBacklogAgeThreshold       = time.Hour
	DataDelayIntervalMultiplier   = 3
)

type AlertCode string

const (
	AlertReadyFailed          AlertCode = "ready_failed"
	AlertProviderFailures     AlertCode = "provider_consecutive_failures"
	AlertProviderDataDelay    AlertCode = "provider_data_delay"
	AlertIngestionTaskBacklog AlertCode = "ingestion_task_backlog"
)

// OperationalAlert is a bounded projection used to verify the thresholds
// mirrored by deploy/prometheus/market-info-alerts.yml.
type OperationalAlert struct {
	Code      AlertCode
	Severity  string
	Provider  string
	Market    string
	Interval  domain.BarInterval
	Value     float64
	Threshold float64
}

// EvaluateOperationalAlerts applies first-phase thresholds at a fixed time.
// Prometheus owns the configured `for` duration; this function validates the
// instantaneous predicate and market-session exclusions.
func EvaluateOperationalAlerts(ready bool, snapshot OperationalSnapshot) ([]OperationalAlert, error) {
	if snapshot.ObservedAt.IsZero() {
		return nil, errors.New("alert snapshot observed time is required")
	}
	if err := validateOperationalSnapshot(snapshot.Tasks, snapshot.Providers); err != nil {
		return nil, err
	}
	alerts := make([]OperationalAlert, 0)
	if !ready {
		alerts = append(alerts, OperationalAlert{Code: AlertReadyFailed, Severity: "critical", Value: 0, Threshold: 1})
	}
	backlogCount := snapshot.Tasks.Counts["pending"] + snapshot.Tasks.Counts["retry_wait"]
	backlogAge := time.Duration(0)
	if snapshot.Tasks.OldestBacklogCreatedAt != nil && snapshot.ObservedAt.After(*snapshot.Tasks.OldestBacklogCreatedAt) {
		backlogAge = snapshot.ObservedAt.Sub(*snapshot.Tasks.OldestBacklogCreatedAt)
	}
	if backlogCount >= TaskBacklogCountThreshold || backlogAge >= TaskBacklogAgeThreshold {
		value, threshold := float64(backlogCount), float64(TaskBacklogCountThreshold)
		if backlogAge >= TaskBacklogAgeThreshold {
			value, threshold = backlogAge.Seconds(), TaskBacklogAgeThreshold.Seconds()
		}
		alerts = append(alerts, OperationalAlert{Code: AlertIngestionTaskBacklog, Severity: "warning", Value: value, Threshold: threshold})
	}
	for _, provider := range snapshot.Providers {
		providerCode := provider.ProviderCode.String()
		if provider.ConsecutiveFailures >= ProviderFailureAlertThreshold {
			alerts = append(alerts, OperationalAlert{
				Code: AlertProviderFailures, Severity: "critical", Provider: providerCode,
				Value: float64(provider.ConsecutiveFailures), Threshold: ProviderFailureAlertThreshold,
			})
		}
		for _, scope := range provider.Scopes {
			if scope.MarketState == "closed" || scope.FreshnessStatus == scheduler.FreshnessStatusNotApplicable || scope.DataDelaySeconds == nil {
				continue
			}
			threshold, err := alertDelayThreshold(scope.Interval)
			if err != nil {
				return nil, err
			}
			if *scope.DataDelaySeconds >= threshold {
				alerts = append(alerts, OperationalAlert{
					Code: AlertProviderDataDelay, Severity: "warning", Provider: providerCode,
					Market: scope.Market, Interval: scope.Interval,
					Value: float64(*scope.DataDelaySeconds), Threshold: float64(threshold),
				})
			}
		}
	}
	sort.Slice(alerts, func(left, right int) bool {
		if alerts[left].Code != alerts[right].Code {
			return alerts[left].Code < alerts[right].Code
		}
		if alerts[left].Provider != alerts[right].Provider {
			return alerts[left].Provider < alerts[right].Provider
		}
		if alerts[left].Market != alerts[right].Market {
			return alerts[left].Market < alerts[right].Market
		}
		return alerts[left].Interval < alerts[right].Interval
	})
	return alerts, nil
}

func alertDelayThreshold(interval domain.BarInterval) (int64, error) {
	switch interval {
	case domain.BarInterval1Hour:
		return int64(DataDelayIntervalMultiplier * time.Hour / time.Second), nil
	case domain.BarInterval1Day:
		return int64(DataDelayIntervalMultiplier * 24 * time.Hour / time.Second), nil
	default:
		return 0, errors.New("unsupported alert interval")
	}
}
