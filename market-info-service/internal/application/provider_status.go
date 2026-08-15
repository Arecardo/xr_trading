package application

import (
	"context"
	"errors"
	"sort"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
	"xr-trading/market-info-service/internal/scheduler"
)

const providerUnhealthyFailureThreshold = 3

type ProviderHealthStatus string

const (
	ProviderHealthHealthy   ProviderHealthStatus = "healthy"
	ProviderHealthDegraded  ProviderHealthStatus = "degraded"
	ProviderHealthUnhealthy ProviderHealthStatus = "unhealthy"
	ProviderHealthUnknown   ProviderHealthStatus = "unknown"
)

// ProviderStatusSource is one provider directory row with its current
// collection subscriptions and persisted observations.
type ProviderStatusSource struct {
	ProviderID       domain.ID
	ProviderCode     domain.Code
	DisplayName      string
	ProviderType     domain.ProviderType
	ConfiguredStatus domain.ProviderStatus
	Subscriptions    []ProviderSubscriptionObservation
}

type ProviderSubscriptionObservation struct {
	SubscriptionID      domain.ID
	ProviderMarket      string
	AssetType           domain.AssetType
	Interval            domain.BarInterval
	CloseDelaySeconds   int
	LastClosedOpenTime  *time.Time
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	ConsecutiveFailures int
}

type ProviderStatusReader interface {
	ListProviderStatusSources(context.Context, time.Time) ([]ProviderStatusSource, error)
}

type ProviderStatus struct {
	ProviderID          domain.ID
	ProviderCode        domain.Code
	DisplayName         string
	ProviderType        domain.ProviderType
	ConfiguredStatus    domain.ProviderStatus
	HealthStatus        ProviderHealthStatus
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	ConsecutiveFailures int
	CheckedAt           time.Time
	Scopes              []ProviderScopeStatus
}

type ProviderScopeStatus struct {
	Market               string
	SessionType          string
	Interval             domain.BarInterval
	MarketState          string
	HealthStatus         ProviderHealthStatus
	FreshnessStatus      scheduler.FreshnessStatus
	DataDelaySeconds     *int64
	ActiveSubscriptions  int
	DelayedSubscriptions int
	NextMarketOpenAt     *time.Time
}

type ProviderStatusService struct {
	reader     ProviderStatusReader
	now        func() time.Time
	usCalendar markettime.TradingCalendar
}

func NewProviderStatusService(reader ProviderStatusReader, now func() time.Time, usCalendar markettime.TradingCalendar) (*ProviderStatusService, error) {
	if reader == nil || now == nil || usCalendar == nil || usCalendar.Location() == nil {
		return nil, errors.New("provider status service dependencies are required")
	}
	return &ProviderStatusService{reader: reader, now: now, usCalendar: usCalendar}, nil
}

// List computes health from persisted observations only. It never probes a
// Provider or mutates collection state.
func (service *ProviderStatusService) List(ctx context.Context) ([]ProviderStatus, error) {
	if ctx == nil {
		return nil, ValidationError([]FieldViolation{{Field: "context", Reason: "is required"}})
	}
	checkedAt := service.now().UTC()
	if checkedAt.IsZero() {
		return nil, WrapError(domain.ErrInvalidData, ErrorCodeInternal, "provider status clock is invalid", false, nil)
	}
	sources, err := service.reader.ListProviderStatusSources(ctx, checkedAt)
	if err != nil {
		return nil, classifyProviderStatusFailure(err)
	}
	statuses := make([]ProviderStatus, 0, len(sources))
	for _, source := range sources {
		status, err := service.projectProvider(source, checkedAt)
		if err != nil {
			return nil, classifyProviderStatusFailure(err)
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ProviderCode.String() < statuses[j].ProviderCode.String() })
	return statuses, nil
}

func (service *ProviderStatusService) projectProvider(source ProviderStatusSource, checkedAt time.Time) (ProviderStatus, error) {
	if source.ProviderID.IsZero() || source.ProviderCode.IsZero() || source.DisplayName == "" {
		return ProviderStatus{}, domain.ErrInvalidData
	}
	if _, err := domain.ParseProviderType(string(source.ProviderType)); err != nil {
		return ProviderStatus{}, err
	}
	if _, err := domain.ParseProviderStatus(string(source.ConfiguredStatus)); err != nil {
		return ProviderStatus{}, err
	}
	status := ProviderStatus{
		ProviderID: source.ProviderID, ProviderCode: source.ProviderCode, DisplayName: source.DisplayName,
		ProviderType: source.ProviderType, ConfiguredStatus: source.ConfiguredStatus,
		HealthStatus: ProviderHealthUnknown, CheckedAt: checkedAt,
	}
	groups := make(map[providerScopeKey][]ProviderSubscriptionObservation)
	for _, observation := range source.Subscriptions {
		key, err := providerScopeFor(observation)
		if err != nil {
			return ProviderStatus{}, err
		}
		groups[key] = append(groups[key], observation)
		status.LastSuccessAt = latestTime(status.LastSuccessAt, observation.LastSuccessAt)
		status.LastFailureAt = latestTime(status.LastFailureAt, observation.LastFailureAt)
		if observation.ConsecutiveFailures > status.ConsecutiveFailures {
			status.ConsecutiveFailures = observation.ConsecutiveFailures
		}
	}
	keys := make([]providerScopeKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].market != keys[j].market {
			return keys[i].market < keys[j].market
		}
		if keys[i].sessionType != keys[j].sessionType {
			return keys[i].sessionType < keys[j].sessionType
		}
		return keys[i].interval < keys[j].interval
	})
	for _, key := range keys {
		scope, err := service.projectScope(key, groups[key], checkedAt)
		if err != nil {
			return ProviderStatus{}, err
		}
		if source.ConfiguredStatus == domain.ProviderStatusDisabled {
			scope.HealthStatus = ProviderHealthUnknown
		}
		status.Scopes = append(status.Scopes, scope)
	}
	if source.ConfiguredStatus != domain.ProviderStatusDisabled {
		status.HealthStatus = summarizeProviderHealth(status.Scopes)
		if source.ConfiguredStatus == domain.ProviderStatusDegraded && status.HealthStatus == ProviderHealthHealthy {
			status.HealthStatus = ProviderHealthDegraded
		}
	}
	return status, nil
}

type providerScopeKey struct {
	market, sessionType string
	interval            domain.BarInterval
}

func providerScopeFor(observation ProviderSubscriptionObservation) (providerScopeKey, error) {
	if observation.SubscriptionID.IsZero() || observation.ProviderMarket == "" || observation.CloseDelaySeconds < 0 || observation.ConsecutiveFailures < 0 {
		return providerScopeKey{}, domain.ErrInvalidData
	}
	if _, err := domain.ParseBarInterval(string(observation.Interval)); err != nil {
		return providerScopeKey{}, err
	}
	switch observation.AssetType {
	case domain.AssetTypeCrypto:
		return providerScopeKey{market: "crypto_" + observation.ProviderMarket, sessionType: "continuous", interval: observation.Interval}, nil
	case domain.AssetTypeFX:
		// FX reference-rate subscriptions (RM0 DEC-006) run on the same
		// continuous 7x24 UTC axis as crypto spot; without this case a live
		// CoinGecko subscription would make every /provider-status call fail
		// with domain.ErrInvalidData for every provider, not only CoinGecko.
		return providerScopeKey{market: "fx_" + observation.ProviderMarket, sessionType: "continuous", interval: observation.Interval}, nil
	case domain.AssetTypeStock, domain.AssetTypeETF:
		return providerScopeKey{market: observation.ProviderMarket + "_equity", sessionType: "regular", interval: observation.Interval}, nil
	default:
		return providerScopeKey{}, domain.ErrInvalidData
	}
}

func (service *ProviderStatusService) projectScope(key providerScopeKey, observations []ProviderSubscriptionObservation, checkedAt time.Time) (ProviderScopeStatus, error) {
	scope := ProviderScopeStatus{
		Market: key.market, SessionType: key.sessionType, Interval: key.interval,
		MarketState: "open", HealthStatus: ProviderHealthUnknown, FreshnessStatus: scheduler.FreshnessStatusUnknown,
		ActiveSubscriptions: len(observations),
	}
	if len(observations) == 0 {
		return scope, domain.ErrInvalidData
	}
	fresh, delayed, unknown, recentFailures, severe := 0, 0, 0, 0, 0
	allNotApplicable := true
	for _, observation := range observations {
		projection, calendarFailure, err := service.subscriptionFreshness(key, observation, checkedAt)
		if err != nil {
			return ProviderScopeStatus{}, err
		}
		observationSevere := calendarFailure
		if calendarFailure {
			scope.MarketState = "unknown"
		}
		if projection.MarketState == scheduler.MarketStateClosed && scope.MarketState != "unknown" {
			scope.MarketState = "closed"
		}
		if projection.NextMarketOpenAt != nil {
			scope.NextMarketOpenAt = earliestTime(scope.NextMarketOpenAt, projection.NextMarketOpenAt)
		}
		if projection.FreshnessStatus != scheduler.FreshnessStatusNotApplicable {
			allNotApplicable = false
		}
		switch projection.FreshnessStatus {
		case scheduler.FreshnessStatusFresh:
			fresh++
		case scheduler.FreshnessStatusDelayed:
			delayed++
			scope.DelayedSubscriptions++
			scope.DataDelaySeconds = maximumInt64(scope.DataDelaySeconds, projection.DataDelaySeconds)
			intervalSeconds := int64(time.Hour / time.Second)
			if key.interval == domain.BarInterval1Day {
				intervalSeconds = int64(24 * time.Hour / time.Second)
			}
			if projection.DataDelaySeconds != nil && *projection.DataDelaySeconds >= 3*intervalSeconds {
				observationSevere = true
			}
		case scheduler.FreshnessStatusUnknown:
			unknown++
		}
		if observation.ConsecutiveFailures >= providerUnhealthyFailureThreshold {
			observationSevere = true
		}
		if observationSevere {
			severe++
		}
		if observation.ConsecutiveFailures > 0 || (observation.LastFailureAt != nil && (observation.LastSuccessAt == nil || observation.LastFailureAt.After(*observation.LastSuccessAt))) {
			recentFailures++
		}
	}
	if allNotApplicable {
		scope.FreshnessStatus = scheduler.FreshnessStatusNotApplicable
		scope.DataDelaySeconds = nil
	} else if delayed > 0 {
		scope.FreshnessStatus = scheduler.FreshnessStatusDelayed
	} else if unknown > 0 {
		scope.FreshnessStatus = scheduler.FreshnessStatusUnknown
	} else {
		scope.FreshnessStatus = scheduler.FreshnessStatusFresh
		zero := int64(0)
		scope.DataDelaySeconds = &zero
	}
	switch {
	case severe > 0 && (severe >= len(observations) || hasFailureThreshold(observations)):
		scope.HealthStatus = ProviderHealthUnhealthy
	case delayed > 0 || recentFailures > 0 || severe > 0 || (unknown > 0 && fresh > 0):
		scope.HealthStatus = ProviderHealthDegraded
	case unknown == len(observations):
		scope.HealthStatus = ProviderHealthUnknown
	case allNotApplicable && hasAnySuccess(observations):
		scope.HealthStatus = ProviderHealthHealthy
	case fresh == len(observations):
		scope.HealthStatus = ProviderHealthHealthy
	}
	return scope, nil
}

type subscriptionFreshnessProjection struct {
	MarketState      scheduler.MarketState
	FreshnessStatus  scheduler.FreshnessStatus
	DataDelaySeconds *int64
	NextMarketOpenAt *time.Time
}

func (service *ProviderStatusService) subscriptionFreshness(key providerScopeKey, observation ProviderSubscriptionObservation, checkedAt time.Time) (subscriptionFreshnessProjection, bool, error) {
	delay := time.Duration(observation.CloseDelaySeconds) * time.Second
	if key.sessionType == "regular" {
		result, err := scheduler.CalculateUSFreshness(service.usCalendar, scheduler.USFreshnessInput{
			ObservedAt: checkedAt, Interval: key.interval, CloseDelay: delay, LastClosedOpenTime: observation.LastClosedOpenTime,
		})
		if errors.Is(err, markettime.ErrCalendarOutOfRange) {
			return subscriptionFreshnessProjection{FreshnessStatus: scheduler.FreshnessStatusUnknown}, true, nil
		}
		if err != nil {
			return subscriptionFreshnessProjection{}, false, err
		}
		return subscriptionFreshnessProjection{MarketState: result.MarketState, FreshnessStatus: result.FreshnessStatus, DataDelaySeconds: result.DataDelaySeconds, NextMarketOpenAt: result.NextMarketOpenAt}, false, nil
	}
	window, err := scheduler.CalculateLatestContinuousWindow(key.interval, scheduler.WindowTriggerClose, checkedAt, delay)
	if err != nil {
		return subscriptionFreshnessProjection{}, false, err
	}
	projection := subscriptionFreshnessProjection{MarketState: scheduler.MarketStateOpen, FreshnessStatus: scheduler.FreshnessStatusUnknown}
	if window == nil || observation.LastClosedOpenTime == nil {
		return projection, false, nil
	}
	zero := int64(0)
	if !observation.LastClosedOpenTime.Before(window.RangeStart) {
		projection.FreshnessStatus, projection.DataDelaySeconds = scheduler.FreshnessStatusFresh, &zero
		return projection, false, nil
	}
	seconds := int64(window.RangeStart.Sub(observation.LastClosedOpenTime.UTC()) / time.Second)
	projection.FreshnessStatus, projection.DataDelaySeconds = scheduler.FreshnessStatusDelayed, &seconds
	return projection, false, nil
}

func summarizeProviderHealth(scopes []ProviderScopeStatus) ProviderHealthStatus {
	if len(scopes) == 0 {
		return ProviderHealthUnknown
	}
	hasHealthy, hasUnknown := false, false
	for _, scope := range scopes {
		switch scope.HealthStatus {
		case ProviderHealthUnhealthy:
			return ProviderHealthUnhealthy
		case ProviderHealthDegraded:
			return ProviderHealthDegraded
		case ProviderHealthHealthy:
			hasHealthy = true
		case ProviderHealthUnknown:
			hasUnknown = true
		}
	}
	if hasUnknown && hasHealthy {
		return ProviderHealthDegraded
	}
	if hasHealthy {
		return ProviderHealthHealthy
	}
	return ProviderHealthUnknown
}

func classifyProviderStatusFailure(err error) error {
	if errors.Is(err, domain.ErrDatabaseUnavailable) {
		return WrapError(err, ErrorCodeDatabaseUnavailable, "database unavailable", true, nil)
	}
	if errors.Is(err, domain.ErrRetryable) {
		return WrapError(err, ErrorCodeServiceUnavailable, "service temporarily unavailable", true, nil)
	}
	return WrapError(err, ErrorCodeInternal, "provider status is unavailable", false, nil)
}

func hasFailureThreshold(values []ProviderSubscriptionObservation) bool {
	for _, value := range values {
		if value.ConsecutiveFailures >= providerUnhealthyFailureThreshold {
			return true
		}
	}
	return false
}
func hasAnySuccess(values []ProviderSubscriptionObservation) bool {
	for _, value := range values {
		if value.LastSuccessAt != nil {
			return true
		}
	}
	return false
}
func latestTime(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	value := candidate.UTC()
	if current == nil || value.After(*current) {
		return &value
	}
	return current
}
func earliestTime(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	value := candidate.UTC()
	if current == nil || value.Before(*current) {
		return &value
	}
	return current
}
func maximumInt64(current, candidate *int64) *int64 {
	if candidate == nil {
		return current
	}
	value := *candidate
	if current == nil || value > *current {
		return &value
	}
	return current
}
