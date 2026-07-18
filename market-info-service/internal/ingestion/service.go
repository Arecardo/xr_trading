package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

// Config bounds provider pagination for one Task.
type Config struct {
	BarsPerPage       int
	MaximumPages      int
	RetryBackoffs     []time.Duration
	MaximumRetryDelay time.Duration
}

// Service executes one claimed K-line task outside a database transaction and
// delegates only the final write set to a short fenced transaction.
type Service struct {
	config   Config
	store    Store
	runs     *RunService
	registry ports.AdapterRegistry
	quality  BarQualityValidator
	now      func() time.Time
}

// NewService constructs the ingestion use case.
func NewService(config Config, store Store, registry ports.AdapterRegistry, quality BarQualityValidator, now func() time.Time) (*Service, error) {
	if config.BarsPerPage <= 0 || config.MaximumPages <= 0 {
		return nil, errors.New("ingestion page size and maximum pages must be positive")
	}
	if len(config.RetryBackoffs) == 0 {
		config.RetryBackoffs = append([]time.Duration(nil), defaultRetryBackoffs...)
	}
	if config.MaximumRetryDelay == 0 {
		config.MaximumRetryDelay = time.Hour
	}
	if err := validateRetryConfig(config); err != nil {
		return nil, err
	}
	if store == nil || registry == nil || quality == nil || now == nil {
		return nil, errors.New("ingestion service dependencies are required")
	}
	config.RetryBackoffs = append([]time.Duration(nil), config.RetryBackoffs...)
	runs, err := NewRunService(store)
	if err != nil {
		return nil, err
	}
	return &Service{config: config, store: store, runs: runs, registry: registry, quality: quality, now: now}, nil
}

// ExecuteTask implements worker.TaskExecutor. Provider calls and quality work
// complete before Store opens the final transaction.
func (service *Service) ExecuteTask(ctx context.Context, claim domain.TaskClaim) error {
	if ctx == nil {
		return fmt.Errorf("execute ingestion task: %w", domain.ErrInvalidData)
	}
	startedAt := service.now().UTC()
	if err := validateExecutableClaim(claim, startedAt); err != nil {
		return fmt.Errorf("execute ingestion task: %w", err)
	}
	err := service.executeClaim(ctx, claim)
	if err == nil {
		if _, refreshErr := service.runs.Refresh(ctx, claim.Task.RunID); refreshErr != nil {
			return fmt.Errorf("refresh successful ingestion run: %w", refreshErr)
		}
		return nil
	}
	if errors.Is(err, ErrTaskLeaseLost) || ctx.Err() != nil {
		return err
	}
	if _, classifiedProviderError := ports.AsProviderError(err); !classifiedProviderError && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return err
	}
	finishedAt := service.now().UTC()
	if leaseErr := validateExecutableClaim(claim, finishedAt); leaseErr != nil {
		return errors.Join(err, fmt.Errorf("transition ingestion failure: %w", leaseErr))
	}
	if transitionErr := service.store.CommitFailure(ctx, service.failureCommit(claim, err, finishedAt)); transitionErr != nil {
		return errors.Join(err, fmt.Errorf("commit ingestion failure: %w", transitionErr))
	}
	if _, refreshErr := service.runs.Refresh(ctx, claim.Task.RunID); refreshErr != nil {
		return errors.Join(err, fmt.Errorf("refresh failed ingestion run: %w", refreshErr))
	}
	return err
}

func (service *Service) executeClaim(ctx context.Context, claim domain.TaskClaim) error {
	execution, err := service.store.LoadExecutionContext(ctx, claim.Task.SubscriptionID)
	if err != nil {
		return fmt.Errorf("load ingestion context: %w", err)
	}
	if execution.Subscription.ID != claim.Task.SubscriptionID {
		return fmt.Errorf("load ingestion context: subscription mismatch: %w", domain.ErrInvalidData)
	}
	reference, err := execution.Reference()
	if err != nil {
		return fmt.Errorf("validate ingestion context: %w", err)
	}
	interval, err := domain.ParseBarInterval(execution.Subscription.Interval)
	if err != nil {
		return fmt.Errorf("parse ingestion interval: %w", err)
	}
	startTime, err := domain.NewUTCInstant(claim.Task.RangeStart)
	if err != nil {
		return fmt.Errorf("parse ingestion range start: %w", err)
	}
	endTime, err := domain.NewUTCInstant(claim.Task.RangeEnd)
	if err != nil {
		return fmt.Errorf("parse ingestion range end: %w", err)
	}
	adapter, exists := service.registry.Get(execution.Provider.Code)
	if !exists || adapter == nil {
		return fmt.Errorf("lookup ingestion adapter %s: %w", execution.Provider.Code, ports.ErrAdapterNotRegistered)
	}

	providerBars, err := service.fetchAllBars(ctx, adapter, ports.FetchBarsRequest{
		Instrument: reference, Interval: interval, StartTime: startTime, EndTime: endTime, Limit: service.config.BarsPerPage,
	})
	if err != nil {
		return err
	}
	quality, err := service.quality.ValidateBars(ctx, execution, providerBars)
	if err != nil {
		return fmt.Errorf("validate ingestion bars: %w", err)
	}
	if err := validateQualityResult(quality, reference, interval, startTime, endTime); err != nil {
		return fmt.Errorf("validate ingestion quality result: %w", err)
	}
	finishedAt := service.now().UTC()
	if err := validateExecutableClaim(claim, finishedAt); err != nil {
		return fmt.Errorf("commit ingestion task: %w", err)
	}
	checkpoint := successfulCheckpoint(execution.Subscription.ID, quality.Bars, finishedAt)
	if err := service.store.CommitSuccess(ctx, SuccessCommit{
		Claim: claim, Bars: quality.Bars, Issues: quality.Issues, Checkpoint: checkpoint, FinishedAt: finishedAt,
	}); err != nil {
		return fmt.Errorf("commit ingestion task: %w", err)
	}
	return nil
}

func validateRetryConfig(config Config) error {
	if config.MaximumRetryDelay <= 0 || len(config.RetryBackoffs) == 0 {
		return errors.New("ingestion retry backoffs and maximum delay must be positive")
	}
	var previous time.Duration
	for _, delay := range config.RetryBackoffs {
		if delay <= 0 || delay < previous {
			return errors.New("ingestion retry backoffs must be positive and non-decreasing")
		}
		previous = delay
	}
	return nil
}

func (service *Service) fetchAllBars(ctx context.Context, adapter ports.MarketDataAdapter, request ports.FetchBarsRequest) ([]ports.ProviderBar, error) {
	bars := make([]ports.ProviderBar, 0)
	seen := make(map[domain.UTCInstant]struct{})
	for page := 0; page < service.config.MaximumPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := service.registry.ValidateBarsRequest(request); err != nil {
			return nil, fmt.Errorf("validate ingestion provider request: %w", err)
		}
		result, err := adapter.FetchBars(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("fetch ingestion bars: %w", err)
		}
		if err := result.Validate(request); err != nil {
			return nil, fmt.Errorf("validate ingestion provider response: %w", err)
		}
		for _, bar := range result.Bars {
			if _, duplicate := seen[bar.OpenTime]; duplicate {
				return nil, fmt.Errorf("provider returned a bar duplicated across pages: %w", domain.ErrInvalidData)
			}
			seen[bar.OpenTime] = struct{}{}
			bars = append(bars, bar)
		}
		if !result.HasMore {
			sort.Slice(bars, func(left, right int) bool { return bars[left].OpenTime.Time().Before(bars[right].OpenTime.Time()) })
			return bars, nil
		}
		request.Cursor = result.NextCursor
	}
	return nil, fmt.Errorf("provider pagination exceeded %d pages: %w", service.config.MaximumPages, ports.ErrProviderLimitExceeded)
}

func validateExecutableClaim(claim domain.TaskClaim, now time.Time) error {
	task := claim.Task
	if task.ID.IsZero() || task.RunID.IsZero() || task.SubscriptionID.IsZero() || task.Status != "running" || task.AttemptCount <= 0 || task.MaxAttempts <= 0 || !task.RangeEnd.After(task.RangeStart) || task.LockedBy == nil || *task.LockedBy == "" || task.LockedUntil == nil {
		return domain.ErrInvalidData
	}
	if !task.LockedUntil.After(now) {
		return ErrTaskLeaseLost
	}
	return nil
}

func validateQualityResult(result QualityResult, reference ports.ProviderInstrumentRef, interval domain.BarInterval, start, end domain.UTCInstant) error {
	seen := make(map[domain.UTCInstant]struct{}, len(result.Bars))
	for index, bar := range result.Bars {
		if err := bar.Validate(); err != nil || bar.InstrumentID != reference.InstrumentID || bar.ProviderInstrumentID != reference.ProviderInstrumentID || bar.Interval != interval || bar.OpenTime.Time().Before(start.Time()) || !bar.OpenTime.Time().Before(end.Time()) {
			return domain.ErrInvalidData
		}
		if _, duplicate := seen[bar.OpenTime]; duplicate {
			return domain.ErrInvalidData
		}
		seen[bar.OpenTime] = struct{}{}
		if index > 0 && !bar.OpenTime.Time().After(result.Bars[index-1].OpenTime.Time()) {
			return domain.ErrInvalidData
		}
	}
	for _, issue := range result.Issues {
		if issue.ID.IsZero() || issue.InstrumentID != reference.InstrumentID || issue.ProviderInstrumentID == nil || *issue.ProviderInstrumentID != reference.ProviderInstrumentID || issue.RuleCode == "" || issue.Summary == "" || issue.DetectedAt.IsZero() || issue.CreatedAt.IsZero() || issue.UpdatedAt.IsZero() || !validIssueSeverity(issue.Severity) {
			return domain.ErrInvalidData
		}
		if issue.Interval != nil && *issue.Interval != string(interval) {
			return domain.ErrInvalidData
		}
		if issue.OpenTime != nil && (issue.OpenTime.Before(start.Time()) || !issue.OpenTime.Before(end.Time())) {
			return domain.ErrInvalidData
		}
		if len(issue.Details) > 0 {
			var details map[string]any
			if !json.Valid(issue.Details) || json.Unmarshal(issue.Details, &details) != nil || details == nil {
				return domain.ErrInvalidData
			}
		}
	}
	return nil
}

func validIssueSeverity(value string) bool {
	switch value {
	case "info", "warning", "error", "critical":
		return true
	default:
		return false
	}
}

func successfulCheckpoint(subscriptionID domain.ID, bars []domain.MarketBar, now time.Time) domain.IngestionCheckpoint {
	checkpoint := domain.IngestionCheckpoint{SubscriptionID: subscriptionID, LastAttemptAt: &now, LastSuccessAt: &now, UpdatedAt: now}
	for index := range bars {
		if !bars[index].IsClosed {
			continue
		}
		openTime := bars[index].OpenTime.Time()
		if checkpoint.LastClosedOpenTime == nil || openTime.After(*checkpoint.LastClosedOpenTime) {
			checkpoint.LastClosedOpenTime = &openTime
			checkpoint.LastSuccessOpenTime = &openTime
		}
	}
	return checkpoint
}
