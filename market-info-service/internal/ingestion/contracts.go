package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

// ErrTaskLeaseLost means a task is no longer owned by the claim attempting a
// final commit. It is a fencing result, not a provider failure.
var ErrTaskLeaseLost = errors.New("task lease lost")

// ExecutionContext is the immutable catalog and subscription snapshot needed
// to turn one durable Task into a provider request.
type ExecutionContext struct {
	Subscription       domain.CollectionSubscription
	Asset              domain.Asset
	Instrument         domain.Instrument
	Provider           domain.Provider
	ProviderInstrument domain.ProviderInstrument
}

// Reference builds the provider-independent adapter reference after checking
// all cross-entity identities.
func (execution ExecutionContext) Reference() (ports.ProviderInstrumentRef, error) {
	if err := execution.Validate(); err != nil {
		return ports.ProviderInstrumentRef{}, err
	}
	reference := ports.ProviderInstrumentRef{
		ProviderInstrumentID:   execution.ProviderInstrument.ID,
		ProviderInstrumentCode: execution.ProviderInstrument.Code,
		InstrumentID:           execution.Instrument.ID,
		AssetID:                execution.Asset.ID,
		ProviderCode:           execution.Provider.Code,
		ProviderMarket:         execution.ProviderInstrument.ProviderMarket,
		AssetType:              execution.Asset.AssetType,
		InstrumentType:         execution.Instrument.InstrumentType,
		ExternalSymbol:         execution.ProviderInstrument.ExternalSymbol,
		InstrumentCode:         execution.Instrument.Code,
		InstrumentSymbol:       execution.Instrument.Symbol,
		QuoteCurrency:          execution.Instrument.QuoteCurrency,
		Metadata:               append([]byte(nil), execution.ProviderInstrument.Metadata...),
	}
	if err := reference.Validate(); err != nil {
		return ports.ProviderInstrumentRef{}, err
	}
	return reference, nil
}

// Validate enforces the relationships and active configuration required for
// an already-created task to execute.
func (execution ExecutionContext) Validate() error {
	if execution.Subscription.ID.IsZero() || execution.Subscription.ProviderInstrumentID != execution.ProviderInstrument.ID || !execution.Subscription.Enabled {
		return domain.ErrInvalidState
	}
	if err := domain.ValidateAssetInstrument(execution.Asset, execution.Instrument); err != nil {
		return err
	}
	if err := domain.ValidateProviderMapping(execution.Provider, execution.Instrument, execution.ProviderInstrument); err != nil {
		return err
	}
	if execution.Asset.Status != domain.AssetStatusActive || execution.Instrument.Status != domain.InstrumentStatusActive || execution.Provider.Status == domain.ProviderStatusDisabled || !execution.ProviderInstrument.Enabled {
		return domain.ErrInvalidState
	}
	interval, err := domain.ParseBarInterval(execution.Subscription.Interval)
	if err != nil || !execution.ProviderInstrument.Capabilities.Historical || !containsInterval(execution.ProviderInstrument.Capabilities.Intervals, interval) {
		return domain.ErrInvalidState
	}
	return nil
}

// QualityResult is the normalized output accepted by the final transaction.
type QualityResult struct {
	Bars   []domain.MarketBar
	Issues []domain.DataQualityIssue
}

// BarQualityValidator performs provider-independent normalization and quality
// checks after every provider page has passed the adapter DTO contract.
type BarQualityValidator interface {
	ValidateBars(context.Context, ExecutionContext, []ports.ProviderBar) (QualityResult, error)
}

// SuccessCommit is the complete atomic write set for a successful Task.
type SuccessCommit struct {
	Claim      domain.TaskClaim
	Bars       []domain.MarketBar
	Issues     []domain.DataQualityIssue
	Checkpoint domain.IngestionCheckpoint
	FinishedAt time.Time
}

// FailureCommit is the complete atomic state transition for one failed
// execution attempt. A retry keeps the same Task and supplies NextAttemptAt;
// a terminal failure leaves it nil.
type FailureCommit struct {
	Claim         domain.TaskClaim
	Status        string
	NextAttemptAt *time.Time
	ErrorCode     string
	ErrorMessage  string
	ErrorDetails  json.RawMessage
	FinishedAt    time.Time
}

// RunTaskSnapshot is a point-in-time count of every durable Task status for a
// Run. Task rows remain the source of truth; the corresponding Run fields are
// only a query cache.
type RunTaskSnapshot struct {
	RunID             domain.ID
	PendingCount      int
	RunningCount      int
	RetryWaitCount    int
	SuccessCount      int
	FailedCount       int
	CanceledCount     int
	EarliestStartedAt *time.Time
	LatestFinishedAt  *time.Time
}

// TaskCount returns the number of Tasks represented by the snapshot.
func (snapshot RunTaskSnapshot) TaskCount() int {
	return snapshot.PendingCount + snapshot.RunningCount + snapshot.RetryWaitCount +
		snapshot.SuccessCount + snapshot.FailedCount + snapshot.CanceledCount
}

// RunSummary is the derived state persisted on ingestion_runs for fast reads.
// All per-status counts are retained so Store can reject a stale snapshot.
type RunSummary struct {
	RunID             domain.ID
	Status            string
	TaskCount         int
	PendingCount      int
	RunningCount      int
	RetryWaitCount    int
	SuccessCount      int
	FailedCount       int
	CanceledCount     int
	EarliestStartedAt *time.Time
	LatestFinishedAt  *time.Time
}

// RunStore reads Task truth and conditionally persists its derived Run cache.
// SaveRunSummary returns ErrConflict when Task statuses changed after the
// snapshot was loaded, allowing the service to recompute instead of writing a
// stale Run state.
type RunStore interface {
	LoadRunTaskSnapshot(context.Context, domain.ID) (RunTaskSnapshot, error)
	SaveRunSummary(context.Context, RunSummary) error
}

// Store loads execution context without a long transaction and performs the
// short, fenced final success transaction after provider calls complete.
type Store interface {
	RunStore
	LoadExecutionContext(context.Context, domain.ID) (ExecutionContext, error)
	CommitSuccess(context.Context, SuccessCommit) error
	CommitFailure(context.Context, FailureCommit) error
}

func containsInterval(values []domain.BarInterval, expected domain.BarInterval) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
