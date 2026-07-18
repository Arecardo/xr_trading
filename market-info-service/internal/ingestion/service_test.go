package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

type stubStore struct {
	execution     ExecutionContext
	loadErr       error
	commitErr     error
	loadCalls     int
	commitCalls   int
	committed     SuccessCommit
	failureErr    error
	failureCalls  int
	failed        FailureCommit
	beforeCommit  func(SuccessCommit)
	snapshot      RunTaskSnapshot
	snapshotErr   error
	snapshotCalls int
	saveErr       error
	saveCalls     int
	saved         RunSummary
}

func (store *stubStore) LoadRunTaskSnapshot(_ context.Context, runID domain.ID) (RunTaskSnapshot, error) {
	store.snapshotCalls++
	if store.snapshotErr != nil {
		return RunTaskSnapshot{}, store.snapshotErr
	}
	if !store.snapshot.RunID.IsZero() {
		return store.snapshot, nil
	}
	snapshot := RunTaskSnapshot{RunID: runID}
	switch {
	case store.commitCalls > 0:
		snapshot.SuccessCount = 1
	case store.failureCalls > 0 && store.failed.Status == taskStatusRetryWait:
		snapshot.RetryWaitCount = 1
	case store.failureCalls > 0:
		snapshot.FailedCount = 1
	default:
		snapshot.RunningCount = 1
	}
	return snapshot, nil
}

func (store *stubStore) SaveRunSummary(_ context.Context, summary RunSummary) error {
	store.saveCalls++
	store.saved = summary
	return store.saveErr
}

func (store *stubStore) CommitFailure(_ context.Context, request FailureCommit) error {
	store.failureCalls++
	store.failed = request
	return store.failureErr
}

func (store *stubStore) LoadExecutionContext(context.Context, domain.ID) (ExecutionContext, error) {
	store.loadCalls++
	return store.execution, store.loadErr
}

func (store *stubStore) CommitSuccess(_ context.Context, request SuccessCommit) error {
	store.commitCalls++
	store.committed = request
	if store.beforeCommit != nil {
		store.beforeCommit(request)
	}
	return store.commitErr
}

type stubAdapter struct {
	code       domain.Code
	fetch      func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error)
	fetchCalls int
	requests   []ports.FetchBarsRequest
}

func (adapter *stubAdapter) ProviderCode() domain.Code { return adapter.code }
func (*stubAdapter) Capabilities(context.Context) (ports.ProviderCapabilities, error) {
	return ports.ProviderCapabilities{}, nil
}
func (*stubAdapter) FetchLatestQuotes(context.Context, []ports.ProviderInstrumentRef) ([]ports.ProviderQuote, error) {
	return nil, errors.New("not implemented")
}
func (adapter *stubAdapter) FetchBars(ctx context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
	adapter.fetchCalls++
	adapter.requests = append(adapter.requests, request)
	return adapter.fetch(ctx, request)
}

type stubRegistry struct {
	adapter     ports.MarketDataAdapter
	validateErr error
	validations int
}

func (registry *stubRegistry) Get(domain.Code) (ports.MarketDataAdapter, bool) {
	return registry.adapter, registry.adapter != nil
}
func (registry *stubRegistry) List() []ports.MarketDataAdapter { return nil }
func (*stubRegistry) Capabilities(domain.Code) (ports.ProviderCapabilities, bool) {
	return ports.ProviderCapabilities{}, false
}
func (*stubRegistry) ValidateLatestQuoteRequest(domain.Code, []ports.ProviderInstrumentRef) error {
	return nil
}
func (registry *stubRegistry) ValidateBarsRequest(ports.FetchBarsRequest) error {
	registry.validations++
	return registry.validateErr
}

type qualityFunc func(context.Context, ExecutionContext, []ports.ProviderBar) (QualityResult, error)

func (function qualityFunc) ValidateBars(ctx context.Context, execution ExecutionContext, bars []ports.ProviderBar) (QualityResult, error) {
	return function(ctx, execution, bars)
}

func TestServiceExecutesPaginatedBarsAndCommitsOnce(t *testing.T) {
	fixture := newIngestionFixture(t)
	pages := map[string]ports.FetchBarsResult{
		"": {
			Bars: []ports.ProviderBar{
				fixture.providerBar(t, fixture.rangeStart.Add(time.Hour), true),
				fixture.providerBar(t, fixture.rangeStart.Add(2*time.Hour), false),
			},
			HasMore: true, NextCursor: "older",
		},
		"older": {Bars: []ports.ProviderBar{fixture.providerBar(t, fixture.rangeStart, true)}},
	}
	adapter := &stubAdapter{code: fixture.execution.Provider.Code, fetch: func(_ context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
		return pages[request.Cursor], nil
	}}
	store := &stubStore{execution: fixture.execution}
	store.beforeCommit = func(SuccessCommit) {
		if adapter.fetchCalls != 2 {
			t.Errorf("provider calls before final commit = %d", adapter.fetchCalls)
		}
	}
	registry := &stubRegistry{adapter: adapter}
	service, err := NewService(Config{BarsPerPage: 2, MaximumPages: 3}, store, registry, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	if err := service.ExecuteTask(context.Background(), fixture.claim); err != nil {
		t.Fatalf("ExecuteTask() error = %v", err)
	}
	if store.loadCalls != 1 || store.commitCalls != 1 || registry.validations != 2 || adapter.fetchCalls != 2 {
		t.Fatalf("calls load=%d commit=%d validate=%d fetch=%d", store.loadCalls, store.commitCalls, registry.validations, adapter.fetchCalls)
	}
	if len(store.committed.Bars) != 3 || !store.committed.Bars[0].OpenTime.Time().Equal(fixture.rangeStart) || store.committed.Bars[2].QualityStatus != domain.QualityStatusWarning {
		t.Fatalf("committed bars = %#v", store.committed.Bars)
	}
	if store.committed.Bars[0].RawHash == "" || len(store.committed.Bars[0].RawHash) != 64 || string(store.committed.Bars[0].Metadata) != `{"provider_payload":{"source":"fixture"}}` {
		t.Fatalf("normalized first bar = %#v", store.committed.Bars[0])
	}
	wantCheckpoint := fixture.rangeStart.Add(time.Hour)
	if store.committed.Checkpoint.LastSuccessOpenTime == nil || !store.committed.Checkpoint.LastSuccessOpenTime.Equal(wantCheckpoint) || store.committed.Checkpoint.ConsecutiveFailures != 0 {
		t.Fatalf("checkpoint = %#v", store.committed.Checkpoint)
	}
	if adapter.requests[0].Cursor != "" || adapter.requests[1].Cursor != "older" || adapter.requests[0].Limit != 2 {
		t.Fatalf("adapter requests = %#v", adapter.requests)
	}
}

func TestServicePersistsValidatedQualityIssues(t *testing.T) {
	fixture := newIngestionFixture(t)
	providerBar := fixture.providerBar(t, fixture.rangeStart, true)
	adapter := &stubAdapter{code: fixture.execution.Provider.Code, fetch: func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
		return ports.FetchBarsResult{Bars: []ports.ProviderBar{providerBar}}, nil
	}}
	validator := qualityFunc(func(ctx context.Context, execution ExecutionContext, bars []ports.ProviderBar) (QualityResult, error) {
		result, err := NewStructuralBarQualityValidator().ValidateBars(ctx, execution, bars)
		if err != nil {
			return QualityResult{}, err
		}
		providerInstrumentID := execution.ProviderInstrument.ID
		interval := execution.Subscription.Interval
		openTime := bars[0].OpenTime.Time()
		result.Issues = []domain.DataQualityIssue{{
			ID: fixture.issueID, InstrumentID: execution.Instrument.ID, ProviderInstrumentID: &providerInstrumentID,
			Interval: &interval, OpenTime: &openTime, RuleCode: "fixture_warning", Severity: "warning",
			Summary: "fixture warning", Details: json.RawMessage(`{"expected":true}`), DetectedAt: fixture.executeAt,
			CreatedAt: fixture.executeAt, UpdatedAt: fixture.executeAt,
		}}
		return result, nil
	})
	store := &stubStore{execution: fixture.execution}
	service, _ := NewService(Config{BarsPerPage: 10, MaximumPages: 1}, store, &stubRegistry{adapter: adapter}, validator, sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
	if err := service.ExecuteTask(context.Background(), fixture.claim); err != nil {
		t.Fatalf("ExecuteTask() error = %v", err)
	}
	if len(store.committed.Issues) != 1 || store.committed.Issues[0].RuleCode != "fixture_warning" {
		t.Fatalf("committed issues = %#v", store.committed.Issues)
	}
}

func TestServiceRunRefreshFailureDoesNotReverseCommittedTask(t *testing.T) {
	fixture := newIngestionFixture(t)
	refreshFailure := errors.New("run refresh failed")
	adapter := &stubAdapter{code: fixture.execution.Provider.Code, fetch: func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
		return ports.FetchBarsResult{}, nil
	}}
	store := &stubStore{execution: fixture.execution, saveErr: refreshFailure}
	service, _ := NewService(Config{BarsPerPage: 10, MaximumPages: 1}, store, &stubRegistry{adapter: adapter}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
	err := service.ExecuteTask(context.Background(), fixture.claim)
	if !errors.Is(err, refreshFailure) || !strings.Contains(err.Error(), "refresh successful ingestion run") {
		t.Fatalf("ExecuteTask() error = %v", err)
	}
	if store.commitCalls != 1 || store.failureCalls != 0 || store.saveCalls != 1 {
		t.Fatalf("calls success=%d failure=%d refresh=%d", store.commitCalls, store.failureCalls, store.saveCalls)
	}
}

func TestServiceFailureRefreshErrorPreservesOriginalFailure(t *testing.T) {
	fixture := newIngestionFixture(t)
	executionFailure := errors.New("quality failed")
	refreshFailure := errors.New("run refresh failed")
	adapter := &stubAdapter{code: fixture.execution.Provider.Code, fetch: func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
		return ports.FetchBarsResult{}, nil
	}}
	quality := qualityFunc(func(context.Context, ExecutionContext, []ports.ProviderBar) (QualityResult, error) {
		return QualityResult{}, executionFailure
	})
	store := &stubStore{execution: fixture.execution, saveErr: refreshFailure}
	service, _ := NewService(Config{BarsPerPage: 10, MaximumPages: 1}, store, &stubRegistry{adapter: adapter}, quality, sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
	err := service.ExecuteTask(context.Background(), fixture.claim)
	if !errors.Is(err, executionFailure) || !errors.Is(err, refreshFailure) || !strings.Contains(err.Error(), "refresh failed ingestion run") {
		t.Fatalf("ExecuteTask() error = %v", err)
	}
	if store.failureCalls != 1 || store.saveCalls != 1 {
		t.Fatalf("calls failure=%d refresh=%d", store.failureCalls, store.saveCalls)
	}
}

func TestNewServiceAndClaimValidation(t *testing.T) {
	fixture := newIngestionFixture(t)
	store := &stubStore{execution: fixture.execution}
	registry := &stubRegistry{}
	quality := NewStructuralBarQualityValidator()
	valid := Config{BarsPerPage: 1, MaximumPages: 1}
	tests := []struct {
		name     string
		config   Config
		store    Store
		registry ports.AdapterRegistry
		quality  BarQualityValidator
		now      func() time.Time
	}{
		{"page size", Config{MaximumPages: 1}, store, registry, quality, time.Now},
		{"maximum pages", Config{BarsPerPage: 1}, store, registry, quality, time.Now},
		{"store", valid, nil, registry, quality, time.Now},
		{"registry", valid, store, nil, quality, time.Now},
		{"quality", valid, store, registry, nil, time.Now},
		{"clock", valid, store, registry, quality, nil},
		{"maximum retry delay", Config{BarsPerPage: 1, MaximumPages: 1, MaximumRetryDelay: -time.Second}, store, registry, quality, time.Now},
		{"descending retry backoff", Config{BarsPerPage: 1, MaximumPages: 1, RetryBackoffs: []time.Duration{time.Minute, time.Second}}, store, registry, quality, time.Now},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewService(test.config, test.store, test.registry, test.quality, test.now); err == nil {
				t.Fatal("NewService() error = nil")
			}
		})
	}
	service, _ := NewService(valid, store, registry, quality, func() time.Time { return fixture.executeAt })
	if err := service.ExecuteTask(nil, fixture.claim); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ExecuteTask(nil) error = %v", err)
	}
	invalid := fixture.claim
	invalid.Task.LockedBy = nil
	if err := service.ExecuteTask(context.Background(), invalid); !errors.Is(err, domain.ErrInvalidData) || store.loadCalls != 0 {
		t.Fatalf("ExecuteTask(invalid) error = %v, load calls=%d", err, store.loadCalls)
	}
	expired := fixture.claim
	until := fixture.executeAt
	expired.Task.LockedUntil = &until
	if err := service.ExecuteTask(context.Background(), expired); !errors.Is(err, ErrTaskLeaseLost) || store.loadCalls != 0 {
		t.Fatalf("ExecuteTask(expired) error = %v, load calls=%d", err, store.loadCalls)
	}
}

func TestServiceFailureStagesDoNotCommit(t *testing.T) {
	stageError := errors.New("stage failed")
	tests := []struct {
		name      string
		configure func(*stubStore, *stubRegistry, *stubAdapter) BarQualityValidator
		want      error
	}{
		{"load", func(store *stubStore, _ *stubRegistry, _ *stubAdapter) BarQualityValidator {
			store.loadErr = stageError
			return NewStructuralBarQualityValidator()
		}, stageError},
		{"adapter missing", func(_ *stubStore, registry *stubRegistry, _ *stubAdapter) BarQualityValidator {
			registry.adapter = nil
			return NewStructuralBarQualityValidator()
		}, ports.ErrAdapterNotRegistered},
		{"request validation", func(_ *stubStore, registry *stubRegistry, _ *stubAdapter) BarQualityValidator {
			registry.validateErr = stageError
			return NewStructuralBarQualityValidator()
		}, stageError},
		{"provider", func(_ *stubStore, _ *stubRegistry, adapter *stubAdapter) BarQualityValidator {
			adapter.fetch = func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
				return ports.FetchBarsResult{}, stageError
			}
			return NewStructuralBarQualityValidator()
		}, stageError},
		{"quality", func(_ *stubStore, _ *stubRegistry, _ *stubAdapter) BarQualityValidator {
			return qualityFunc(func(context.Context, ExecutionContext, []ports.ProviderBar) (QualityResult, error) {
				return QualityResult{}, stageError
			})
		}, stageError},
		{"commit", func(store *stubStore, _ *stubRegistry, _ *stubAdapter) BarQualityValidator {
			store.commitErr = stageError
			return NewStructuralBarQualityValidator()
		}, stageError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIngestionFixture(t)
			adapter := &stubAdapter{code: fixture.execution.Provider.Code, fetch: func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
				return ports.FetchBarsResult{}, nil
			}}
			store := &stubStore{execution: fixture.execution}
			registry := &stubRegistry{adapter: adapter}
			quality := test.configure(store, registry, adapter)
			service, _ := NewService(Config{BarsPerPage: 10, MaximumPages: 1}, store, registry, quality, sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
			err := service.ExecuteTask(context.Background(), fixture.claim)
			if !errors.Is(err, test.want) {
				t.Fatalf("ExecuteTask() error = %v, want %v", err, test.want)
			}
			if test.name != "commit" && store.commitCalls != 0 {
				t.Fatalf("commit calls = %d", store.commitCalls)
			}
		})
	}
}

func TestServiceRejectsPaginationAndQualityContractFailures(t *testing.T) {
	t.Run("invalid page", func(t *testing.T) {
		fixture := newIngestionFixture(t)
		adapter := &stubAdapter{code: fixture.execution.Provider.Code, fetch: func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
			return ports.FetchBarsResult{HasMore: true}, nil
		}}
		store := &stubStore{execution: fixture.execution}
		service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 2}, store, &stubRegistry{adapter: adapter}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
		if err := service.ExecuteTask(context.Background(), fixture.claim); !errors.Is(err, domain.ErrInvalidData) || store.commitCalls != 0 {
			t.Fatalf("ExecuteTask() = %v, commit calls=%d", err, store.commitCalls)
		}
	})

	t.Run("maximum pages", func(t *testing.T) {
		fixture := newIngestionFixture(t)
		adapter := &stubAdapter{code: fixture.execution.Provider.Code, fetch: func(_ context.Context, request ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
			return ports.FetchBarsResult{Bars: []ports.ProviderBar{fixture.providerBar(t, fixture.rangeStart, true)}, HasMore: true, NextCursor: request.Cursor + "next"}, nil
		}}
		store := &stubStore{execution: fixture.execution}
		service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 1}, store, &stubRegistry{adapter: adapter}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
		if err := service.ExecuteTask(context.Background(), fixture.claim); !errors.Is(err, ports.ErrProviderLimitExceeded) || store.commitCalls != 0 {
			t.Fatalf("ExecuteTask() = %v, commit calls=%d", err, store.commitCalls)
		}
	})

	t.Run("invalid quality result", func(t *testing.T) {
		fixture := newIngestionFixture(t)
		providerBar := fixture.providerBar(t, fixture.rangeStart, true)
		adapter := &stubAdapter{code: fixture.execution.Provider.Code, fetch: func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
			return ports.FetchBarsResult{Bars: []ports.ProviderBar{providerBar}}, nil
		}}
		quality := qualityFunc(func(context.Context, ExecutionContext, []ports.ProviderBar) (QualityResult, error) {
			return QualityResult{Bars: []domain.MarketBar{{}}}, nil
		})
		store := &stubStore{execution: fixture.execution}
		service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 1}, store, &stubRegistry{adapter: adapter}, quality, sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
		if err := service.ExecuteTask(context.Background(), fixture.claim); !errors.Is(err, domain.ErrInvalidData) || store.commitCalls != 0 {
			t.Fatalf("ExecuteTask() = %v, commit calls=%d", err, store.commitCalls)
		}
	})
}

func TestServiceAppliesRetryPolicy(t *testing.T) {
	providerCode, _ := domain.ParseCode("bybit")
	tests := []struct {
		name        string
		attempt     int
		maxAttempts int
		code        ports.ProviderErrorCode
		retryAfter  *time.Duration
		wantStatus  string
		wantDelay   time.Duration
	}{
		{"first network", 1, 6, ports.ProviderErrorNetwork, nil, taskStatusRetryWait, time.Minute},
		{"second rate limit", 2, 6, ports.ProviderErrorRateLimited, nil, taskStatusRetryWait, 5 * time.Minute},
		{"third unavailable", 3, 6, ports.ProviderErrorTemporaryUnavailable, nil, taskStatusRetryWait, 15 * time.Minute},
		{"fourth unknown", 4, 6, ports.ProviderErrorUnknown, nil, taskStatusRetryWait, 30 * time.Minute},
		{"fifth capped schedule", 5, 6, ports.ProviderErrorNetwork, nil, taskStatusRetryWait, time.Hour},
		{"attempt limit", 5, 5, ports.ProviderErrorNetwork, nil, taskStatusFailed, 0},
		{"unauthorized", 1, 5, ports.ProviderErrorUnauthorized, nil, taskStatusFailed, 0},
		{"invalid instrument", 1, 5, ports.ProviderErrorInvalidInstrument, nil, taskStatusFailed, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIngestionFixture(t)
			fixture.claim.Task.AttemptCount = test.attempt
			fixture.claim.Task.MaxAttempts = test.maxAttempts
			cause := error(errors.New("secret technical cause"))
			if test.name == "first network" {
				cause = context.DeadlineExceeded
			}
			providerError, err := ports.NewProviderError(providerCode, test.code, "safe provider failure", test.retryAfter, cause)
			if err != nil {
				t.Fatalf("NewProviderError() error = %v", err)
			}
			adapter := &stubAdapter{code: providerCode, fetch: func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
				return ports.FetchBarsResult{}, providerError
			}}
			store := &stubStore{execution: fixture.execution}
			finishedAt := fixture.executeAt.Add(time.Second)
			service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 1}, store, &stubRegistry{adapter: adapter}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, finishedAt))
			err = service.ExecuteTask(context.Background(), fixture.claim)
			if !errors.Is(err, providerError) || store.failureCalls != 1 {
				t.Fatalf("ExecuteTask() error=%v failure calls=%d", err, store.failureCalls)
			}
			if store.failed.Status != test.wantStatus || store.failed.ErrorCode != string(test.code) || store.failed.ErrorMessage != "safe provider failure" || string(store.failed.ErrorDetails) != `{"provider_code":"bybit"}` {
				t.Fatalf("failure transition = %#v", store.failed)
			}
			if test.wantDelay == 0 {
				if store.failed.NextAttemptAt != nil {
					t.Fatalf("NextAttemptAt = %v", store.failed.NextAttemptAt)
				}
			} else if store.failed.NextAttemptAt == nil || !store.failed.NextAttemptAt.Equal(finishedAt.Add(test.wantDelay)) {
				t.Fatalf("NextAttemptAt = %v, want %v", store.failed.NextAttemptAt, finishedAt.Add(test.wantDelay))
			}
			if strings.Contains(store.failed.ErrorMessage, "secret") || strings.Contains(string(store.failed.ErrorDetails), "secret") {
				t.Fatal("persisted provider failure leaked technical cause")
			}
		})
	}
}

func TestServicePrefersAndCapsProviderRetryAfter(t *testing.T) {
	fixture := newIngestionFixture(t)
	providerCode := fixture.execution.Provider.Code
	for _, test := range []struct {
		name       string
		retryAfter time.Duration
		want       time.Duration
	}{
		{"preferred", 17 * time.Minute, 17 * time.Minute},
		{"capped", 2 * time.Hour, 45 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			providerError, _ := ports.NewProviderError(providerCode, ports.ProviderErrorRateLimited, "rate limited", &test.retryAfter, nil)
			adapter := &stubAdapter{code: providerCode, fetch: func(context.Context, ports.FetchBarsRequest) (ports.FetchBarsResult, error) {
				return ports.FetchBarsResult{}, providerError
			}}
			store := &stubStore{execution: fixture.execution}
			finishedAt := fixture.executeAt.Add(time.Second)
			service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 1, MaximumRetryDelay: 45 * time.Minute}, store, &stubRegistry{adapter: adapter}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, finishedAt))
			_ = service.ExecuteTask(context.Background(), fixture.claim)
			if store.failed.NextAttemptAt == nil || !store.failed.NextAttemptAt.Equal(finishedAt.Add(test.want)) {
				t.Fatalf("NextAttemptAt = %v", store.failed.NextAttemptAt)
			}
		})
	}
}

func TestServiceClassifiesInfrastructureAndConfigurationFailures(t *testing.T) {
	tests := []struct {
		failure    error
		wantCode   string
		wantStatus string
	}{
		{domain.ErrDatabaseUnavailable, "database_unavailable", taskStatusRetryWait},
		{domain.ErrRetryable, "temporary_failure", taskStatusRetryWait},
		{domain.ErrNotFound, "configuration_not_found", taskStatusFailed},
		{domain.ErrInvalidState, "configuration_invalid", taskStatusFailed},
		{ports.ErrAdapterNotRegistered, "adapter_not_registered", taskStatusFailed},
		{ports.ErrProviderLimitExceeded, "provider_limit_exceeded", taskStatusFailed},
		{errors.New("unclassified secret"), "internal_error", taskStatusFailed},
	}
	for _, test := range tests {
		t.Run(test.wantCode, func(t *testing.T) {
			fixture := newIngestionFixture(t)
			store := &stubStore{execution: fixture.execution, loadErr: test.failure}
			service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 1}, store, &stubRegistry{}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
			err := service.ExecuteTask(context.Background(), fixture.claim)
			if !errors.Is(err, test.failure) || store.failureCalls != 1 || store.failed.ErrorCode != test.wantCode || store.failed.Status != test.wantStatus {
				t.Fatalf("ExecuteTask() error=%v transition=%#v", err, store.failed)
			}
		})
	}
}

func TestServiceDoesNotTransitionCanceledOrLostExecution(t *testing.T) {
	fixture := newIngestionFixture(t)
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, ErrTaskLeaseLost} {
		store := &stubStore{execution: fixture.execution, loadErr: failure}
		service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 1}, store, &stubRegistry{}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
		if err := service.ExecuteTask(context.Background(), fixture.claim); !errors.Is(err, failure) || store.failureCalls != 0 {
			t.Fatalf("ExecuteTask(%v) error=%v failure calls=%d", failure, err, store.failureCalls)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	store := &stubStore{execution: fixture.execution, loadErr: errors.New("ignored after cancellation")}
	service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 1}, store, &stubRegistry{}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
	if err := service.ExecuteTask(canceled, fixture.claim); err == nil || store.failureCalls != 0 {
		t.Fatalf("ExecuteTask(canceled) error=%v failure calls=%d", err, store.failureCalls)
	}
}

func TestServiceReportsFailureTransitionError(t *testing.T) {
	fixture := newIngestionFixture(t)
	executionFailure := domain.ErrRetryable
	transitionFailure := errors.New("transition unavailable")
	store := &stubStore{execution: fixture.execution, loadErr: executionFailure, failureErr: transitionFailure}
	service, _ := NewService(Config{BarsPerPage: 1, MaximumPages: 1}, store, &stubRegistry{}, NewStructuralBarQualityValidator(), sequenceClock(fixture.executeAt, fixture.executeAt.Add(time.Second)))
	err := service.ExecuteTask(context.Background(), fixture.claim)
	if !errors.Is(err, executionFailure) || !errors.Is(err, transitionFailure) || store.failureCalls != 1 {
		t.Fatalf("ExecuteTask() error=%v failure calls=%d", err, store.failureCalls)
	}
}

func TestStructuralBarQualityValidator(t *testing.T) {
	fixture := newIngestionFixture(t)
	closed := fixture.providerBar(t, fixture.rangeStart, true)
	open := fixture.providerBar(t, fixture.rangeStart.Add(time.Hour), false)
	validator := NewStructuralBarQualityValidator()
	result, err := validator.ValidateBars(context.Background(), fixture.execution, []ports.ProviderBar{closed, open})
	if err != nil || len(result.Bars) != 2 || result.Bars[0].QualityStatus != domain.QualityStatusValid || result.Bars[1].QualityStatus != domain.QualityStatusWarning {
		t.Fatalf("ValidateBars() = (%#v, %v)", result, err)
	}
	changedTransport := closed
	changedTransport.RawPayload = json.RawMessage(`{"transport":"changed"}`)
	changedTransport.ReceivedAt = instant(t, closed.ReceivedAt.Time().Add(time.Minute))
	second, err := validator.ValidateBars(context.Background(), fixture.execution, []ports.ProviderBar{changedTransport})
	if err != nil || second.Bars[0].RawHash != result.Bars[0].RawHash {
		t.Fatalf("transport-only hash changed: %v / %v", result.Bars[0].RawHash, second.Bars[0].RawHash)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := validator.ValidateBars(canceled, fixture.execution, []ports.ProviderBar{closed}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ValidateBars(canceled) error = %v", err)
	}
	if _, err := validator.ValidateBars(nil, fixture.execution, nil); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ValidateBars(nil) error = %v", err)
	}
}

func TestExecutionContextValidation(t *testing.T) {
	fixture := newIngestionFixture(t)
	reference, err := fixture.execution.Reference()
	if err != nil || reference.ExternalSymbol != "BTCUSDT" || reference.ProviderCode.String() != "bybit" {
		t.Fatalf("Reference() = (%#v, %v)", reference, err)
	}
	invalid := fixture.execution
	invalid.Subscription.Enabled = false
	if err := invalid.Validate(); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("Validate(disabled) error = %v", err)
	}
	invalid = fixture.execution
	invalid.ProviderInstrument.Capabilities.Intervals = []domain.BarInterval{domain.BarInterval1Day}
	if err := invalid.Validate(); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("Validate(unsupported) error = %v", err)
	}
	invalid = fixture.execution
	invalid.Asset.ID = domain.ID{}
	if _, err := invalid.Reference(); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Reference(invalid) error = %v", err)
	}
}

type ingestionFixture struct {
	execution  ExecutionContext
	claim      domain.TaskClaim
	issueID    domain.ID
	rangeStart time.Time
	executeAt  time.Time
}

func newIngestionFixture(t *testing.T) ingestionFixture {
	t.Helper()
	createdAt := time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)
	rangeStart := createdAt.Add(-6 * time.Hour)
	assetID := mustID(t, "019f1452-90f7-7992-a87a-ca2727893001")
	instrumentID := mustID(t, "019f1452-90f7-7992-a87a-ca2727893002")
	providerID := mustID(t, "019f1452-90f7-7992-a87a-ca2727893003")
	mappingID := mustID(t, "019f1452-90f7-7992-a87a-ca2727893004")
	subscriptionID := mustID(t, "019f1452-90f7-7992-a87a-ca2727893005")
	assetCode, _ := domain.ParseCode("asset.crypto.btc")
	instrumentCode, _ := domain.ParseCode("instrument.bybit.spot.btc-usdt")
	providerCode, _ := domain.ParseCode("bybit")
	mappingCode, _ := domain.ParseCode("provider.bybit.spot.btcusdt")
	execution := ExecutionContext{
		Subscription:       domain.CollectionSubscription{ID: subscriptionID, ProviderInstrumentID: mappingID, Interval: "1h", Enabled: true, CreatedAt: createdAt, UpdatedAt: createdAt},
		Asset:              domain.Asset{ID: assetID, Code: assetCode, AssetType: domain.AssetTypeCrypto, CanonicalSymbol: "BTC", Name: "Bitcoin", Status: domain.AssetStatusActive, Metadata: json.RawMessage(`{}`), CreatedAt: createdAt, UpdatedAt: createdAt},
		Instrument:         domain.Instrument{ID: instrumentID, Code: instrumentCode, AssetID: assetID, Venue: "BYBIT", InstrumentType: domain.InstrumentTypeSpot, Symbol: "BTC-USDT", QuoteCurrency: "USDT", MarketTimezone: "UTC", Status: domain.InstrumentStatusActive, Metadata: json.RawMessage(`{}`), CreatedAt: createdAt, UpdatedAt: createdAt},
		Provider:           domain.Provider{ID: providerID, Code: providerCode, Name: "Bybit", ProviderType: domain.ProviderTypeExchange, Status: domain.ProviderStatusActive, Metadata: json.RawMessage(`{}`), CreatedAt: createdAt, UpdatedAt: createdAt},
		ProviderInstrument: domain.ProviderInstrument{ID: mappingID, Code: mappingCode, ProviderID: providerID, InstrumentID: instrumentID, ExternalSymbol: "BTCUSDT", ProviderMarket: "spot", Capabilities: domain.ProviderCapabilities{Quote: true, Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour}}, Enabled: true, Metadata: json.RawMessage(`{}`), CreatedAt: createdAt, UpdatedAt: createdAt},
	}
	workerID := "worker-ingestion"
	lockedUntil := createdAt.Add(time.Hour)
	claim := domain.TaskClaim{Task: domain.IngestionTask{
		ID: mustID(t, "019f1452-90f7-7992-a87a-ca2727893006"), RunID: mustID(t, "019f1452-90f7-7992-a87a-ca2727893007"), SubscriptionID: subscriptionID,
		RangeStart: rangeStart, RangeEnd: rangeStart.Add(3 * time.Hour), Status: "running", AttemptCount: 1, MaxAttempts: 5,
		LockedBy: &workerID, LockedUntil: &lockedUntil, CreatedAt: createdAt, UpdatedAt: createdAt,
	}}
	return ingestionFixture{execution: execution, claim: claim, issueID: mustID(t, "019f1452-90f7-7992-a87a-ca2727893008"), rangeStart: rangeStart, executeAt: createdAt}
}

func (fixture ingestionFixture) providerBar(t *testing.T, openTime time.Time, closed bool) ports.ProviderBar {
	t.Helper()
	return ports.ProviderBar{
		ProviderInstrumentID: fixture.execution.ProviderInstrument.ID, InstrumentID: fixture.execution.Instrument.ID,
		AssetID: fixture.execution.Asset.ID, ProviderCode: fixture.execution.Provider.Code, Interval: domain.BarInterval1Hour,
		OpenTime: instant(t, openTime), CloseTime: instant(t, openTime.Add(time.Hour)),
		Open: exact("100"), High: exact("110"), Low: exact("90"), Close: exact("105"),
		IsClosed: closed, ReceivedAt: instant(t, fixture.executeAt), RawPayload: json.RawMessage(`{"source":"fixture"}`),
	}
}

func mustID(t *testing.T, text string) domain.ID {
	t.Helper()
	id, err := domain.ParseID(text)
	if err != nil {
		t.Fatalf("ParseID() error = %v", err)
	}
	return id
}

func instant(t *testing.T, value time.Time) domain.UTCInstant {
	t.Helper()
	parsed, err := domain.NewUTCInstant(value)
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return parsed
}

func exact(value string) domain.Decimal {
	return domain.DecimalFromExact(decimal.RequireFromString(value))
}

func sequenceClock(values ...time.Time) func() time.Time {
	var mu sync.Mutex
	index := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if index >= len(values) {
			return values[len(values)-1]
		}
		value := values[index]
		index++
		return value
	}
}
