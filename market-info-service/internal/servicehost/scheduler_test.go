package servicehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/scheduler"
)

type stubIncrementalScheduler struct {
	mu       sync.Mutex
	results  []scheduler.IncrementalResult
	errors   []error
	calls    chan struct{}
	attempts int
}

func (stub *stubIncrementalScheduler) RunOnce(context.Context) (scheduler.IncrementalResult, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	index := stub.attempts
	stub.attempts++
	if stub.calls != nil {
		stub.calls <- struct{}{}
	}
	if len(stub.errors) > 0 {
		err := stub.errors[0]
		stub.errors = stub.errors[1:]
		return scheduler.IncrementalResult{}, err
	}
	result := scheduler.IncrementalResult{CreatedRuns: index + 1}
	stub.results = append(stub.results, result)
	return result, nil
}

type stubSchedulerReporter struct {
	mu       sync.Mutex
	results  []scheduler.IncrementalResult
	errors   []error
	reported chan struct{}
}

func (reporter *stubSchedulerReporter) ReportSchedulerResult(result scheduler.IncrementalResult) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	reporter.results = append(reporter.results, result)
	if reporter.reported != nil {
		reporter.reported <- struct{}{}
	}
}

func (reporter *stubSchedulerReporter) ReportSchedulerError(err error) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	reporter.errors = append(reporter.errors, err)
	if reporter.reported != nil {
		reporter.reported <- struct{}{}
	}
}

func TestSchedulerLoopRunsImmediatelyAndContinuesAfterFailure(t *testing.T) {
	t.Parallel()

	cycleFailure := errors.New("temporary")
	incremental := &stubIncrementalScheduler{errors: []error{cycleFailure}}
	reporter := &stubSchedulerReporter{reported: make(chan struct{}, 2)}
	loop, err := NewSchedulerLoop(SchedulerLoopConfig{Interval: time.Millisecond}, incremental, reporter)
	if err != nil {
		t.Fatalf("NewSchedulerLoop() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- loop.Run(ctx) }()
	<-reporter.reported
	<-reporter.reported
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if len(reporter.errors) != 1 || !errors.Is(reporter.errors[0], cycleFailure) || len(reporter.results) != 1 {
		t.Fatalf("reported errors=%v results=%v", reporter.errors, reporter.results)
	}
}

func TestSchedulerLoopValidationAndCanceledStart(t *testing.T) {
	t.Parallel()

	reporter := &stubSchedulerReporter{}
	incremental := &stubIncrementalScheduler{}
	if _, err := NewSchedulerLoop(SchedulerLoopConfig{}, incremental, reporter); err == nil {
		t.Fatal("NewSchedulerLoop(zero interval) error = nil")
	}
	if _, err := NewSchedulerLoop(SchedulerLoopConfig{Interval: time.Second}, nil, reporter); err == nil {
		t.Fatal("NewSchedulerLoop(nil scheduler) error = nil")
	}
	if _, err := NewSchedulerLoop(SchedulerLoopConfig{Interval: time.Second}, incremental, nil); err == nil {
		t.Fatal("NewSchedulerLoop(nil reporter) error = nil")
	}
	loop, err := NewSchedulerLoop(SchedulerLoopConfig{Interval: time.Second}, incremental, reporter)
	if err != nil {
		t.Fatalf("NewSchedulerLoop() error = %v", err)
	}
	if err := loop.Run(nil); err == nil {
		t.Fatal("Run(nil) error = nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := loop.Run(ctx); err != nil {
		t.Fatalf("Run(canceled) error = %v", err)
	}
	if err := (*SchedulerLoop)(nil).Run(context.Background()); err == nil {
		t.Fatal("nil Run() error = nil")
	}
}
