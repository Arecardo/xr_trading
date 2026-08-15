package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/markettime"
)

const (
	defaultSchedulingPageSize        = 50
	maximumSchedulingPageSize        = 100
	defaultSchedulingMaximumAttempts = 5
	defaultMaximumCatchUpBars        = 500
	maximumCatchUpBars               = 10000
	defaultMaximumCatchUpRuns        = 20
	maximumCatchUpRuns               = 100
)

// SchedulingTarget is the validated catalog projection needed to choose a
// market-time algorithm. It intentionally contains no Adapter.
type SchedulingTarget struct {
	Subscription   domain.CollectionSubscription
	ProviderCode   domain.Code
	ProviderMarket string
	InstrumentCode domain.Code
	AssetType      domain.AssetType
	InstrumentType domain.InstrumentType
	Capabilities   domain.ProviderCapabilities
}

// Validate checks that the target is currently executable and classifiable as
// either continuous crypto spot or a US regular-session instrument.
func (target SchedulingTarget) Validate() error {
	if target.Subscription.ID.IsZero() || target.Subscription.ProviderInstrumentID.IsZero() || !target.Subscription.Enabled ||
		target.Subscription.CreatedAt.IsZero() || target.Subscription.UpdatedAt.IsZero() || target.ProviderCode.IsZero() ||
		target.InstrumentCode.IsZero() || strings.TrimSpace(target.ProviderMarket) != target.ProviderMarket || target.ProviderMarket == "" ||
		target.Subscription.CloseDelaySeconds < 0 || (target.Subscription.RevisionDelaySeconds != nil && *target.Subscription.RevisionDelaySeconds < 0) {
		return domain.ErrInvalidData
	}
	interval, err := domain.ParseBarInterval(target.Subscription.Interval)
	if err != nil || !target.Capabilities.Historical || !supportsInterval(target.Capabilities.Intervals, interval) {
		return domain.ErrInvalidState
	}
	if _, err := schedulingMarket(target); err != nil {
		return err
	}
	return nil
}

// SchedulingTargetPage is one stable UUIDv7-ordered scan page.
type SchedulingTargetPage struct {
	Items       []SchedulingTarget
	NextAfterID *domain.ID
}

// ScheduledBatch is the one-Run/one-Task atomic write contract.
type ScheduledBatch struct {
	Trigger WindowTrigger
	Run     domain.IngestionRun
	Task    domain.IngestionTask
}

// Validate enforces scheduler-owned identity and pending-state invariants.
func (batch ScheduledBatch) Validate() error {
	run, task := batch.Run, batch.Task
	wantRunType := "incremental"
	if batch.Trigger == WindowTriggerRevision {
		wantRunType = "revision"
	} else if batch.Trigger != WindowTriggerClose {
		return domain.ErrInvalidData
	}
	if run.ID.IsZero() || task.ID.IsZero() || run.ID.UUID().Version() != uuid.Version(7) || task.ID.UUID().Version() != uuid.Version(7) ||
		run.ID == task.ID || task.RunID != run.ID || task.SubscriptionID.IsZero() ||
		run.RunKey != StableRunKey(batch.Trigger, task.SubscriptionID, task.RangeStart, task.RangeEnd) ||
		run.RunType != wantRunType || run.TriggerType != "scheduler" || run.Status != "pending" || run.ScheduledAt == nil ||
		run.StartedAt != nil || run.FinishedAt != nil || run.RequestedBy != nil || run.TaskCount != 1 || run.SuccessCount != 0 || run.FailedCount != 0 ||
		run.CreatedAt.IsZero() || run.ScheduledAt.After(run.CreatedAt) || !validJSONObject(run.Context) || !validJSONObject(run.ErrorSummary) ||
		task.Status != "pending" || task.AttemptCount != 0 || task.MaxAttempts < 1 || !task.RangeEnd.After(task.RangeStart) || task.RangeEnd.After(*run.ScheduledAt) ||
		task.RetryOfTaskID != nil || task.NextAttemptAt != nil || task.LockedBy != nil || task.LockedUntil != nil || task.StartedAt != nil || task.FinishedAt != nil ||
		task.ProviderRequestID != nil || task.ErrorCode != nil || task.ErrorMessage != nil || task.CanceledBy != nil || task.CancelReason != nil ||
		!task.CreatedAt.Equal(run.CreatedAt) || !task.UpdatedAt.Equal(run.CreatedAt) || !validJSONObject(task.ErrorDetails) {
		return domain.ErrInvalidData
	}
	return nil
}

// StableRunKey identifies a logical scheduled range independently of which
// process observed it or when a duplicate scan ran.
func StableRunKey(trigger WindowTrigger, subscriptionID domain.ID, start, end time.Time) string {
	runType := "incremental"
	if trigger == WindowTriggerRevision {
		runType = "revision"
	}
	return strings.Join([]string{
		runType, "scheduler", string(trigger), subscriptionID.String(),
		start.UTC().Format("20060102T150405.000000000Z"),
		end.UTC().Format("20060102T150405.000000000Z"),
	}, ".")
}

// IncrementalStore scans current targets and creates one idempotent batch.
// created=false means an equivalent stable run_key already exists.
type IncrementalStore interface {
	ListSchedulingTargets(context.Context, *domain.ID, int, time.Time) (SchedulingTargetPage, error)
	LoadSchedulingCheckpoint(context.Context, domain.ID) (*domain.IngestionCheckpoint, error)
	ListClosedBarOpenTimes(context.Context, SchedulingTarget, time.Time, time.Time) ([]time.Time, error)
	CreateScheduledBatch(context.Context, ScheduledBatch) (created bool, err error)
	RecoverExpiredTasks(context.Context, time.Time) (int64, error)
}

// IncrementalConfig controls bounded scans and durable retry limits.
type IncrementalConfig struct {
	PageSize           int
	MaximumAttempts    int
	MaximumCatchUpBars int
	MaximumCatchUpRuns int
}

// IncrementalResult summarizes one database scan without implying that Worker
// execution has completed.
type IncrementalResult struct {
	ScannedSubscriptions int
	DueWindows           int
	CreatedRuns          int
	ExistingRuns         int
	RecoveredTasks       int64
}

// IncrementalScheduler creates durable tasks but never fetches market data.
type IncrementalScheduler struct {
	store              IncrementalStore
	clock              Clock
	newID              func() (domain.ID, error)
	usCalendar         markettime.TradingCalendar
	pageSize           int
	maximumAttempts    int
	maximumCatchUpBars int
	maximumCatchUpRuns int
}

// NewIncrementalScheduler validates dependencies and installs bounded defaults.
func NewIncrementalScheduler(config IncrementalConfig, store IncrementalStore, clock Clock, newID func() (domain.ID, error), usCalendar markettime.TradingCalendar) (*IncrementalScheduler, error) {
	if config.PageSize == 0 {
		config.PageSize = defaultSchedulingPageSize
	}
	if config.MaximumAttempts == 0 {
		config.MaximumAttempts = defaultSchedulingMaximumAttempts
	}
	if config.MaximumCatchUpBars == 0 {
		config.MaximumCatchUpBars = defaultMaximumCatchUpBars
	}
	if config.MaximumCatchUpRuns == 0 {
		config.MaximumCatchUpRuns = defaultMaximumCatchUpRuns
	}
	if config.PageSize < 1 || config.PageSize > maximumSchedulingPageSize || config.MaximumAttempts < 1 ||
		config.MaximumCatchUpBars < 1 || config.MaximumCatchUpBars > maximumCatchUpBars ||
		config.MaximumCatchUpRuns < 1 || config.MaximumCatchUpRuns > maximumCatchUpRuns {
		return nil, errors.New("incremental scheduler configuration is invalid")
	}
	if store == nil || clock == nil || newID == nil || usCalendar == nil || usCalendar.Location() == nil {
		return nil, errors.New("incremental scheduler dependencies are required")
	}
	return &IncrementalScheduler{
		store: store, clock: clock, newID: newID, usCalendar: usCalendar,
		pageSize: config.PageSize, maximumAttempts: config.MaximumAttempts,
		maximumCatchUpBars: config.MaximumCatchUpBars, maximumCatchUpRuns: config.MaximumCatchUpRuns,
	}, nil
}

// RunOnce scans every enabled target at one fixed observed time. Rows created
// by earlier pages remain valid if a later target fails; each batch itself is
// atomic and the next scan safely retries by stable run_key.
func (scheduler *IncrementalScheduler) RunOnce(ctx context.Context) (IncrementalResult, error) {
	if scheduler == nil || scheduler.store == nil || scheduler.clock == nil || scheduler.newID == nil || scheduler.usCalendar == nil {
		return IncrementalResult{}, errors.New("incremental scheduler is not initialized")
	}
	if ctx == nil {
		return IncrementalResult{}, fmt.Errorf("run incremental scheduler: %w", domain.ErrInvalidData)
	}
	observedAt := scheduler.clock.Now().UTC()
	if observedAt.IsZero() {
		return IncrementalResult{}, fmt.Errorf("run incremental scheduler: %w", domain.ErrInvalidData)
	}

	var result IncrementalResult
	recovered, err := scheduler.store.RecoverExpiredTasks(ctx, observedAt)
	if err != nil {
		return result, fmt.Errorf("recover expired ingestion tasks: %w", err)
	}
	result.RecoveredTasks = recovered
	var afterID *domain.ID
	for {
		page, err := scheduler.store.ListSchedulingTargets(ctx, afterID, scheduler.pageSize, observedAt)
		if err != nil {
			return result, fmt.Errorf("list incremental scheduling targets: %w", err)
		}
		if len(page.Items) == 0 && page.NextAfterID != nil {
			return result, fmt.Errorf("list incremental scheduling targets returned an invalid cursor: %w", domain.ErrInvalidData)
		}
		for _, target := range page.Items {
			result.ScannedSubscriptions++
			if err := target.Validate(); err != nil {
				return result, fmt.Errorf("validate scheduling target %s: %w", target.Subscription.ID, err)
			}
			policy, err := NewDelayPolicy(target.Subscription.CloseDelaySeconds, target.Subscription.RevisionDelaySeconds)
			if err != nil {
				return result, fmt.Errorf("construct scheduling delay policy: %w", err)
			}
			for _, trigger := range []WindowTrigger{WindowTriggerClose, WindowTriggerRevision} {
				delay, enabled, err := policy.Delay(trigger)
				if err != nil {
					return result, err
				}
				if !enabled {
					continue
				}
				var windows []CollectionWindow
				if trigger == WindowTriggerClose {
					windows, err = scheduler.catchUpWindows(ctx, target, delay, observedAt)
				} else {
					var window *CollectionWindow
					window, err = scheduler.latestWindow(target, trigger, delay, observedAt)
					if window != nil && window.RangeEnd.After(target.Subscription.UpdatedAt.UTC()) {
						windows = []CollectionWindow{*window}
					}
				}
				if err != nil {
					return result, fmt.Errorf("calculate %s window for subscription %s: %w", trigger, target.Subscription.ID, err)
				}
				for _, window := range windows {
					result.DueWindows++
					batch, err := scheduler.newBatch(target, window, observedAt)
					if err != nil {
						return result, err
					}
					created, err := scheduler.store.CreateScheduledBatch(ctx, batch)
					if err != nil {
						return result, fmt.Errorf("persist scheduled batch %s: %w", batch.Run.RunKey, err)
					}
					if created {
						result.CreatedRuns++
					} else {
						result.ExistingRuns++
					}
				}
			}
		}
		if page.NextAfterID == nil {
			return result, nil
		}
		if (afterID != nil && *page.NextAfterID == *afterID) || (len(page.Items) > 0 && *page.NextAfterID != page.Items[len(page.Items)-1].Subscription.ID) {
			return result, fmt.Errorf("list incremental scheduling targets returned a non-advancing cursor: %w", domain.ErrInvalidData)
		}
		next := *page.NextAfterID
		afterID = &next
	}
}

func (scheduler *IncrementalScheduler) latestWindow(target SchedulingTarget, trigger WindowTrigger, delay time.Duration, observedAt time.Time) (*CollectionWindow, error) {
	interval, _ := domain.ParseBarInterval(target.Subscription.Interval)
	market, err := schedulingMarket(target)
	if err != nil {
		return nil, err
	}
	if market == "continuous" {
		return CalculateLatestContinuousWindow(interval, trigger, observedAt, delay)
	}
	return CalculateLatestUSWindow(scheduler.usCalendar, interval, trigger, observedAt, delay)
}

func (scheduler *IncrementalScheduler) newBatch(target SchedulingTarget, window CollectionWindow, observedAt time.Time) (ScheduledBatch, error) {
	runID, err := scheduler.newID()
	if err != nil || !validScheduledID(runID) {
		if err == nil {
			err = domain.ErrInvalidData
		}
		return ScheduledBatch{}, fmt.Errorf("generate scheduled run ID: %w", err)
	}
	taskID, err := scheduler.newID()
	if err != nil || !validScheduledID(taskID) || taskID == runID {
		if err == nil {
			err = domain.ErrInvalidData
		}
		return ScheduledBatch{}, fmt.Errorf("generate scheduled task ID: %w", err)
	}
	contextJSON, err := json.Marshal(map[string]string{
		"trigger": string(window.Trigger), "subscription_id": target.Subscription.ID.String(),
		"provider": target.ProviderCode.String(), "instrument_code": target.InstrumentCode.String(),
		"interval": string(window.Interval), "range_start": window.RangeStart.Format(time.RFC3339Nano),
		"range_end": window.RangeEnd.Format(time.RFC3339Nano),
	})
	if err != nil {
		return ScheduledBatch{}, fmt.Errorf("encode scheduled run context: %w", err)
	}
	runType := "incremental"
	if window.Trigger == WindowTriggerRevision {
		runType = "revision"
	}
	scheduledAt := window.EligibleAt.UTC()
	run := domain.IngestionRun{
		ID: runID, RunKey: StableRunKey(window.Trigger, target.Subscription.ID, window.RangeStart, window.RangeEnd),
		RunType: runType, TriggerType: "scheduler", Status: "pending", ScheduledAt: &scheduledAt,
		TaskCount: 1, Context: contextJSON, ErrorSummary: json.RawMessage(`{}`), CreatedAt: observedAt,
	}
	task := domain.IngestionTask{
		ID: taskID, RunID: runID, SubscriptionID: target.Subscription.ID,
		RangeStart: window.RangeStart, RangeEnd: window.RangeEnd, Status: "pending", MaxAttempts: scheduler.maximumAttempts,
		ErrorDetails: json.RawMessage(`{}`), CreatedAt: observedAt, UpdatedAt: observedAt,
	}
	batch := ScheduledBatch{Trigger: window.Trigger, Run: run, Task: task}
	if err := batch.Validate(); err != nil {
		return ScheduledBatch{}, fmt.Errorf("construct scheduled batch: %w", err)
	}
	return batch, nil
}

func schedulingMarket(target SchedulingTarget) (string, error) {
	if target.AssetType == domain.AssetTypeCrypto && target.InstrumentType == domain.InstrumentTypeSpot {
		return "continuous", nil
	}
	// FX reference rates (RM0 DEC-006) are collected on the same 7x24 UTC axis
	// as crypto spot: they are not gated by any single market's trading
	// calendar and must stay available whenever a non-base-currency holding
	// exists in the asset universe (doc/technical/03_universe_data.md §7).
	if target.AssetType == domain.AssetTypeFX && target.InstrumentType == domain.InstrumentTypeFX {
		return "continuous", nil
	}
	if target.ProviderMarket == "us" && ((target.AssetType == domain.AssetTypeStock && target.InstrumentType == domain.InstrumentTypeEquity) ||
		(target.AssetType == domain.AssetTypeETF && target.InstrumentType == domain.InstrumentTypeETF)) {
		return "us_regular", nil
	}
	return "", domain.ErrInvalidState
}

func supportsInterval(values []domain.BarInterval, expected domain.BarInterval) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func validScheduledID(id domain.ID) bool {
	return !id.IsZero() && id.UUID().Version() == uuid.Version(7)
}

func validJSONObject(value json.RawMessage) bool {
	var object map[string]any
	return json.Valid(value) && json.Unmarshal(value, &object) == nil && object != nil
}
