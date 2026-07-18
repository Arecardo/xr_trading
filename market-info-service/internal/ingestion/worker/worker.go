// Package worker owns durable ingestion task claiming and bounded execution.
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

const maximumWorkerIDLength = 128

// ErrAlreadyRunning is returned when the same Worker is started twice.
var ErrAlreadyRunning = errors.New("worker is already running")

// TaskClaimer is the narrow durable-queue port needed by Worker. The
// PostgreSQL implementation performs the claim and lease update atomically.
type TaskClaimer interface {
	ClaimNextTask(context.Context, string, time.Time, time.Duration) (*domain.TaskClaim, error)
}

// TaskExecutor executes one already-claimed task. Implementations must honor
// context cancellation and own every task state transition after running.
type TaskExecutor interface {
	ExecuteTask(context.Context, domain.TaskClaim) error
}

// Clock makes claim timestamps and idle/error waits deterministic in tests.
// Implementations must be safe for concurrent use by all Worker slots.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

// ErrorReporter receives non-fatal claim and execution failures. Calls are
// serialized by Worker, so reporters do not need their own synchronization.
type ErrorReporter func(error)

// Config controls one Worker process. Concurrency is a strict upper bound on
// tasks executing in this process; it does not replace the database lease.
type Config struct {
	WorkerID          string
	Concurrency       int
	LeaseDuration     time.Duration
	PollInterval      time.Duration
	ClaimErrorBackoff time.Duration
}

// Dependencies contains the ports required by Worker. Clock and ReportError
// are optional; production-safe defaults are installed when omitted.
type Dependencies struct {
	Claimer     TaskClaimer
	Executor    TaskExecutor
	Clock       Clock
	ReportError ErrorReporter
}

// Worker repeatedly claims durable tasks and dispatches them to bounded
// execution slots.
type Worker struct {
	config      Config
	claimer     TaskClaimer
	executor    TaskExecutor
	clock       Clock
	reportError ErrorReporter
	reportMu    sync.Mutex
	running     atomic.Bool
}

// New constructs a Worker after validating all lifecycle settings.
func New(config Config, dependencies Dependencies) (*Worker, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if dependencies.Claimer == nil || dependencies.Executor == nil {
		return nil, errors.New("worker claimer and executor are required")
	}
	if dependencies.Clock == nil {
		dependencies.Clock = systemClock{}
	}
	if dependencies.ReportError == nil {
		dependencies.ReportError = func(error) {}
	}
	return &Worker{
		config:      config,
		claimer:     dependencies.Claimer,
		executor:    dependencies.Executor,
		clock:       dependencies.Clock,
		reportError: dependencies.ReportError,
	}, nil
}

// Run starts the configured number of claim loops and blocks until ctx is
// canceled or an internal clock failure makes continued operation unsafe.
// Context cancellation is a normal graceful shutdown and returns nil.
func (worker *Worker) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("worker context is required")
	}
	if !worker.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	defer worker.running.Store(false)

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, worker.config.Concurrency)
	for slot := 0; slot < worker.config.Concurrency; slot++ {
		go func() {
			err := worker.claimLoop(runContext)
			if err != nil {
				cancel()
			}
			results <- err
		}()
	}

	var runError error
	for slot := 0; slot < worker.config.Concurrency; slot++ {
		if err := <-results; err != nil && runError == nil {
			runError = err
		}
	}
	return runError
}

func (worker *Worker) claimLoop(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		claimedAt := worker.clock.Now().UTC()
		claim, err := worker.claimer.ClaimNextTask(ctx, worker.config.WorkerID, claimedAt, worker.config.LeaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			worker.report(fmt.Errorf("worker %q claim task: %w", worker.config.WorkerID, err))
			if err := worker.wait(ctx, worker.config.ClaimErrorBackoff); err != nil {
				return err
			}
			continue
		}
		if claim == nil {
			if err := worker.wait(ctx, worker.config.PollInterval); err != nil {
				return err
			}
			continue
		}
		if err := validateClaim(*claim, worker.config.WorkerID, claimedAt); err != nil {
			worker.report(err)
			if err := worker.wait(ctx, worker.config.ClaimErrorBackoff); err != nil {
				return err
			}
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := worker.executor.ExecuteTask(ctx, *claim); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			worker.report(fmt.Errorf("worker %q execute task %s: %w", worker.config.WorkerID, claim.Task.ID, err))
		}
	}
}

func (worker *Worker) wait(ctx context.Context, duration time.Duration) error {
	if err := worker.clock.Wait(ctx, duration); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("worker %q wait: %w", worker.config.WorkerID, err)
	}
	return nil
}

func (worker *Worker) report(err error) {
	worker.reportMu.Lock()
	defer worker.reportMu.Unlock()
	worker.reportError(err)
}

func validateConfig(config Config) error {
	if strings.TrimSpace(config.WorkerID) != config.WorkerID || config.WorkerID == "" || len(config.WorkerID) > maximumWorkerIDLength {
		return fmt.Errorf("invalid worker ID: must be 1-%d non-whitespace-padded bytes", maximumWorkerIDLength)
	}
	if config.Concurrency <= 0 {
		return errors.New("worker concurrency must be positive")
	}
	if config.LeaseDuration <= 0 || config.PollInterval <= 0 || config.ClaimErrorBackoff <= 0 {
		return errors.New("worker lease, poll interval, and claim error backoff must be positive")
	}
	return nil
}

func validateClaim(claim domain.TaskClaim, workerID string, claimedAt time.Time) error {
	task := claim.Task
	if task.ID.IsZero() || task.Status != "running" || task.AttemptCount <= 0 || task.MaxAttempts <= 0 || task.LockedBy == nil || *task.LockedBy != workerID || task.LockedUntil == nil || !task.LockedUntil.After(claimedAt) {
		return fmt.Errorf("worker %q received invalid task claim: %w", workerID, domain.ErrInvalidData)
	}
	return nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
