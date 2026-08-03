package servicehost

import (
	"context"
	"errors"
	"time"

	"xr-trading/market-info-service/internal/scheduler"
)

// IncrementalScheduler is the single-cycle durable scheduling boundary.
type IncrementalScheduler interface {
	RunOnce(context.Context) (scheduler.IncrementalResult, error)
}

// SchedulerReporter receives bounded summaries and safe operational errors.
type SchedulerReporter interface {
	ReportSchedulerResult(scheduler.IncrementalResult)
	ReportSchedulerError(error)
}

// SchedulerLoopConfig controls how frequently durable scheduling is scanned.
type SchedulerLoopConfig struct {
	Interval time.Duration
}

// SchedulerLoop repeatedly invokes the canonical RunOnce implementation. A
// failed cycle is reported and retried on the next interval; it does not stop
// the Worker that may still be draining already durable tasks.
type SchedulerLoop struct {
	interval  time.Duration
	scheduler IncrementalScheduler
	reporter  SchedulerReporter
}

// NewSchedulerLoop validates the periodic scheduling dependencies.
func NewSchedulerLoop(config SchedulerLoopConfig, incremental IncrementalScheduler, reporter SchedulerReporter) (*SchedulerLoop, error) {
	if config.Interval <= 0 {
		return nil, errors.New("scheduler interval must be positive")
	}
	if incremental == nil || reporter == nil {
		return nil, errors.New("scheduler and reporter are required")
	}
	return &SchedulerLoop{interval: config.Interval, scheduler: incremental, reporter: reporter}, nil
}

// Run performs one immediate scan and then scans at the configured interval.
// Context cancellation is a normal graceful shutdown.
func (loop *SchedulerLoop) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scheduler loop context is required")
	}
	if loop == nil || loop.scheduler == nil || loop.reporter == nil || loop.interval <= 0 {
		return errors.New("scheduler loop is not initialized")
	}
	if ctx.Err() != nil {
		return nil
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			result, err := loop.scheduler.RunOnce(ctx)
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				loop.reporter.ReportSchedulerError(err)
			} else {
				loop.reporter.ReportSchedulerResult(result)
			}
			timer.Reset(loop.interval)
		}
	}
}
