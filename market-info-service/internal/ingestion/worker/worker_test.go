package worker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

type claimerFunc func(context.Context, string, time.Time, time.Duration) (*domain.TaskClaim, error)

func (function claimerFunc) ClaimNextTask(ctx context.Context, workerID string, now time.Time, lease time.Duration) (*domain.TaskClaim, error) {
	return function(ctx, workerID, now, lease)
}

type executorFunc func(context.Context, domain.TaskClaim) error

func (function executorFunc) ExecuteTask(ctx context.Context, claim domain.TaskClaim) error {
	return function(ctx, claim)
}

type stubClock struct {
	now  time.Time
	wait func(context.Context, time.Duration) error
}

func (clock stubClock) Now() time.Time { return clock.now }

func (clock stubClock) Wait(ctx context.Context, duration time.Duration) error {
	return clock.wait(ctx, duration)
}

func TestNewValidatesWorkerConfigurationAndDependencies(t *testing.T) {
	validConfig := testConfig("worker-a", 1)
	validDependencies := Dependencies{
		Claimer:  claimerFunc(func(context.Context, string, time.Time, time.Duration) (*domain.TaskClaim, error) { return nil, nil }),
		Executor: executorFunc(func(context.Context, domain.TaskClaim) error { return nil }),
	}
	tests := []struct {
		name         string
		config       Config
		dependencies Dependencies
	}{
		{"empty ID", withWorkerID(validConfig, ""), validDependencies},
		{"padded ID", withWorkerID(validConfig, " worker-a"), validDependencies},
		{"long ID", withWorkerID(validConfig, strings.Repeat("w", maximumWorkerIDLength+1)), validDependencies},
		{"zero concurrency", withConcurrency(validConfig, 0), validDependencies},
		{"zero lease", withDuration(validConfig, "lease"), validDependencies},
		{"zero poll", withDuration(validConfig, "poll"), validDependencies},
		{"zero backoff", withDuration(validConfig, "backoff"), validDependencies},
		{"nil claimer", validConfig, Dependencies{Executor: validDependencies.Executor}},
		{"nil executor", validConfig, Dependencies{Claimer: validDependencies.Claimer}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config, test.dependencies); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}

	created, err := New(validConfig, validDependencies)
	if err != nil || created.clock == nil || created.reportError == nil {
		t.Fatalf("New(valid) = (%#v, %v)", created, err)
	}
}

func TestWorkerIdlePollUsesUTCClaimTimeAndStopsOnCancellation(t *testing.T) {
	now := time.Date(2026, time.July, 16, 15, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	waits := make(chan time.Duration, 1)
	claimArguments := make(chan struct {
		workerID string
		now      time.Time
		lease    time.Duration
	}, 1)
	clock := stubClock{now: now, wait: func(ctx context.Context, duration time.Duration) error {
		waits <- duration
		<-ctx.Done()
		return ctx.Err()
	}}
	var unexpectedExecutions atomic.Int32
	instance, err := New(testConfig("worker-idle", 1), Dependencies{
		Claimer: claimerFunc(func(_ context.Context, workerID string, claimedAt time.Time, lease time.Duration) (*domain.TaskClaim, error) {
			claimArguments <- struct {
				workerID string
				now      time.Time
				lease    time.Duration
			}{workerID, claimedAt, lease}
			return nil, nil
		}),
		Executor: executorFunc(func(context.Context, domain.TaskClaim) error {
			unexpectedExecutions.Add(1)
			return nil
		}),
		Clock: clock,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- instance.Run(ctx) }()
	arguments := <-claimArguments
	if arguments.workerID != "worker-idle" || !arguments.now.Equal(now.UTC()) || arguments.now.Location() != time.UTC || arguments.lease != time.Minute {
		t.Fatalf("claim arguments = %#v", arguments)
	}
	if duration := <-waits; duration != 250*time.Millisecond {
		t.Fatalf("idle wait = %s", duration)
	}
	if err := instance.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run() error = %v", err)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if unexpectedExecutions.Load() != 0 {
		t.Fatalf("executions for empty queue = %d", unexpectedExecutions.Load())
	}
	if err := instance.Run(nil); err == nil {
		t.Fatal("Run(nil) error = nil")
	}
}

func TestWorkerRetriesClaimErrorsAndReportsExecutionErrors(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	claimFailure := errors.New("database unavailable")
	executionFailure := errors.New("final transition unavailable")
	var calls atomic.Int32
	claimed := testClaim(t, "worker-errors", now, "019f1452-90f7-7992-a87a-ca2727891701")
	executed := make(chan struct{}, 1)
	waits := make(chan time.Duration, 2)
	reports := make(chan error, 2)
	clock := stubClock{now: now, wait: func(ctx context.Context, duration time.Duration) error {
		waits <- duration
		if duration == time.Second {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	instance, err := New(testConfig("worker-errors", 1), Dependencies{
		Claimer: claimerFunc(func(context.Context, string, time.Time, time.Duration) (*domain.TaskClaim, error) {
			switch calls.Add(1) {
			case 1:
				return nil, claimFailure
			case 2:
				return &claimed, nil
			default:
				return nil, nil
			}
		}),
		Executor: executorFunc(func(context.Context, domain.TaskClaim) error {
			executed <- struct{}{}
			return executionFailure
		}),
		Clock:       clock,
		ReportError: func(err error) { reports <- err },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- instance.Run(ctx) }()
	<-executed
	first, second := <-reports, <-reports
	if !errors.Is(first, claimFailure) || !errors.Is(second, executionFailure) {
		t.Fatalf("reports = (%v, %v)", first, second)
	}
	if delay := <-waits; delay != time.Second {
		t.Fatalf("claim error wait = %s", delay)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestWorkerEnforcesConcurrencyAndCooperativelyCancelsExecutors(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	queue := newTaskQueue(t, 4)
	started := make(chan domain.ID, 4)
	release := make(chan struct{}, 4)
	var active atomic.Int32
	var maximum atomic.Int32
	instance, err := New(testConfig("worker-bounded", 2), Dependencies{
		Claimer: queue,
		Executor: executorFunc(func(ctx context.Context, claim domain.TaskClaim) error {
			current := active.Add(1)
			for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
			}
			started <- claim.Task.ID
			defer active.Add(-1)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
		Clock: stubClock{now: now, wait: func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- instance.Run(ctx) }()
	first, second := <-started, <-started
	if first == second {
		t.Fatalf("duplicate initial claim %s", first)
	}
	select {
	case unexpected := <-started:
		t.Fatalf("third task %s started before a slot was released", unexpected)
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}
	third := <-started
	if third == first || third == second {
		t.Fatalf("duplicate third claim %s", third)
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if active.Load() != 0 {
		t.Fatalf("active executors after cancellation = %d", active.Load())
	}
}

func TestMultipleWorkersDoNotExecuteTheSameClaim(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	queue := newTaskQueue(t, 4)
	started := make(chan domain.TaskClaim, 4)
	release := make(chan struct{}, 4)
	executor := executorFunc(func(ctx context.Context, claim domain.TaskClaim) error {
		started <- claim
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	clock := stubClock{now: now, wait: func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	first, err := New(testConfig("worker-one", 1), Dependencies{Claimer: queue, Executor: executor, Clock: clock})
	if err != nil {
		t.Fatalf("New(first) error = %v", err)
	}
	second, err := New(testConfig("worker-two", 1), Dependencies{Claimer: queue, Executor: executor, Clock: clock})
	if err != nil {
		t.Fatalf("New(second) error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 2)
	go func() { results <- first.Run(ctx) }()
	go func() { results <- second.Run(ctx) }()
	seenTasks := make(map[domain.ID]struct{}, 4)
	seenWorkers := make(map[string]struct{}, 2)
	for count := 0; count < 2; count++ {
		claim := <-started
		if _, exists := seenTasks[claim.Task.ID]; exists {
			t.Fatalf("task %s executed more than once", claim.Task.ID)
		}
		seenTasks[claim.Task.ID] = struct{}{}
		seenWorkers[*claim.Task.LockedBy] = struct{}{}
	}
	if len(seenWorkers) != 2 {
		t.Fatalf("initial workers that executed claims = %#v", seenWorkers)
	}
	release <- struct{}{}
	release <- struct{}{}
	for count := 2; count < 4; count++ {
		claim := <-started
		if _, exists := seenTasks[claim.Task.ID]; exists {
			t.Fatalf("task %s executed more than once", claim.Task.ID)
		}
		seenTasks[claim.Task.ID] = struct{}{}
		seenWorkers[*claim.Task.LockedBy] = struct{}{}
		release <- struct{}{}
	}
	if len(seenWorkers) != 2 {
		t.Fatalf("workers that executed claims = %#v", seenWorkers)
	}
	cancel()
	for index := 0; index < 2; index++ {
		if err := <-results; err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	}
}

func TestWorkerRejectsInvalidClaimWithoutExecuting(t *testing.T) {
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	invalid := testClaim(t, "another-worker", now, "019f1452-90f7-7992-a87a-ca2727891801")
	reported := make(chan error, 1)
	waiting := make(chan time.Duration, 1)
	var unexpectedExecutions atomic.Int32
	instance, err := New(testConfig("worker-validating", 1), Dependencies{
		Claimer: claimerFunc(func(context.Context, string, time.Time, time.Duration) (*domain.TaskClaim, error) {
			return &invalid, nil
		}),
		Executor: executorFunc(func(context.Context, domain.TaskClaim) error {
			unexpectedExecutions.Add(1)
			return nil
		}),
		Clock: stubClock{now: now, wait: func(ctx context.Context, duration time.Duration) error {
			waiting <- duration
			<-ctx.Done()
			return ctx.Err()
		}},
		ReportError: func(err error) { reported <- err },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- instance.Run(ctx) }()
	if err := <-reported; !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("reported error = %v", err)
	}
	if delay := <-waiting; delay != time.Second {
		t.Fatalf("invalid claim wait = %s", delay)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if unexpectedExecutions.Load() != 0 {
		t.Fatalf("executions for invalid claim = %d", unexpectedExecutions.Load())
	}
}

func TestWorkerReturnsUnexpectedClockFailure(t *testing.T) {
	waitFailure := errors.New("clock failed")
	instance, err := New(testConfig("worker-clock", 1), Dependencies{
		Claimer: claimerFunc(func(context.Context, string, time.Time, time.Duration) (*domain.TaskClaim, error) {
			return nil, nil
		}),
		Executor: executorFunc(func(context.Context, domain.TaskClaim) error { return nil }),
		Clock: stubClock{
			now: time.Now(),
			wait: func(context.Context, time.Duration) error {
				return waitFailure
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := instance.Run(context.Background()); !errors.Is(err, waitFailure) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestValidateClaimAndSystemClockBoundaries(t *testing.T) {
	now := time.Now().UTC()
	valid := testClaim(t, "worker", now, "019f1452-90f7-7992-a87a-ca2727891901")
	if err := validateClaim(valid, "worker", now); err != nil {
		t.Fatalf("validateClaim(valid) error = %v", err)
	}
	mutations := []func(*domain.IngestionTask){
		func(task *domain.IngestionTask) { task.ID = domain.ID{} },
		func(task *domain.IngestionTask) { task.Status = "pending" },
		func(task *domain.IngestionTask) { task.AttemptCount = 0 },
		func(task *domain.IngestionTask) { task.MaxAttempts = 0 },
		func(task *domain.IngestionTask) { task.LockedBy = nil },
		func(task *domain.IngestionTask) { expired := now; task.LockedUntil = &expired },
	}
	for index, mutate := range mutations {
		invalid := valid
		mutate(&invalid.Task)
		if err := validateClaim(invalid, "worker", now); !errors.Is(err, domain.ErrInvalidData) {
			t.Fatalf("validateClaim(invalid %d) error = %v", index, err)
		}
	}

	clock := systemClock{}
	if clock.Now().IsZero() {
		t.Fatal("systemClock.Now() is zero")
	}
	if err := clock.Wait(context.Background(), time.Nanosecond); err != nil {
		t.Fatalf("systemClock.Wait(timer) error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := clock.Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("systemClock.Wait(canceled) error = %v", err)
	}
}

type taskQueue struct {
	mu    sync.Mutex
	tasks []domain.IngestionTask
}

func newTaskQueue(t *testing.T, count int) *taskQueue {
	t.Helper()
	tasks := make([]domain.IngestionTask, 0, count)
	for index := 0; index < count; index++ {
		id, err := domain.ParseID([]string{
			"019f1452-90f7-7992-a87a-ca2727892001",
			"019f1452-90f7-7992-a87a-ca2727892002",
			"019f1452-90f7-7992-a87a-ca2727892003",
			"019f1452-90f7-7992-a87a-ca2727892004",
		}[index])
		if err != nil {
			t.Fatalf("ParseID() error = %v", err)
		}
		tasks = append(tasks, domain.IngestionTask{ID: id, MaxAttempts: 5})
	}
	return &taskQueue{tasks: tasks}
}

func (queue *taskQueue) ClaimNextTask(_ context.Context, workerID string, now time.Time, lease time.Duration) (*domain.TaskClaim, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.tasks) == 0 {
		return nil, nil
	}
	task := queue.tasks[0]
	queue.tasks = queue.tasks[1:]
	task.Status = "running"
	task.AttemptCount++
	task.LockedBy = &workerID
	lockedUntil := now.Add(lease)
	task.LockedUntil = &lockedUntil
	return &domain.TaskClaim{Task: task}, nil
}

func testClaim(t *testing.T, workerID string, now time.Time, idText string) domain.TaskClaim {
	t.Helper()
	id, err := domain.ParseID(idText)
	if err != nil {
		t.Fatalf("ParseID() error = %v", err)
	}
	lockedUntil := now.Add(time.Minute)
	return domain.TaskClaim{Task: domain.IngestionTask{
		ID: id, Status: "running", AttemptCount: 1, MaxAttempts: 5,
		LockedBy: &workerID, LockedUntil: &lockedUntil,
	}}
}

func testConfig(workerID string, concurrency int) Config {
	return Config{
		WorkerID: workerID, Concurrency: concurrency, LeaseDuration: time.Minute,
		PollInterval: 250 * time.Millisecond, ClaimErrorBackoff: time.Second,
	}
}

func withWorkerID(config Config, workerID string) Config {
	config.WorkerID = workerID
	return config
}

func withConcurrency(config Config, concurrency int) Config {
	config.Concurrency = concurrency
	return config
}

func withDuration(config Config, field string) Config {
	switch field {
	case "lease":
		config.LeaseDuration = 0
	case "poll":
		config.PollInterval = 0
	case "backoff":
		config.ClaimErrorBackoff = 0
	}
	return config
}
