// Package testkit provides deterministic concurrency, clock, identity and
// fault-injection helpers shared by market information service tests.
package testkit

import (
	"context"
	"errors"
	"sync"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

var (
	ErrGateUnavailable     = errors.New("test gate is unavailable")
	ErrIDSequenceExhausted = errors.New("test ID sequence is exhausted")
)

// ManualClock is a thread-safe clock controlled explicitly by a test.
type ManualClock struct {
	mu  sync.RWMutex
	now time.Time
}

func NewManualClock(now time.Time) *ManualClock {
	return &ManualClock{now: now.UTC()}
}

func (clock *ManualClock) Now() time.Time {
	if clock == nil {
		return time.Time{}
	}
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *ManualClock) Set(now time.Time) {
	if clock == nil {
		return
	}
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = now.UTC()
}

func (clock *ManualClock) Advance(duration time.Duration) time.Time {
	if clock == nil {
		return time.Time{}
	}
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
	return clock.now
}

// IDSequence supplies stable identities in call order and fails instead of
// silently generating an unexpected random ID after the fixture is exhausted.
type IDSequence struct {
	mu    sync.Mutex
	ids   []domain.ID
	calls int
}

func NewIDSequence(ids ...domain.ID) *IDSequence {
	return &IDSequence{ids: append([]domain.ID(nil), ids...)}
}

func (sequence *IDSequence) Next() (domain.ID, error) {
	if sequence == nil {
		return domain.ID{}, ErrIDSequenceExhausted
	}
	sequence.mu.Lock()
	defer sequence.mu.Unlock()
	sequence.calls++
	if len(sequence.ids) == 0 {
		return domain.ID{}, ErrIDSequenceExhausted
	}
	next := sequence.ids[0]
	sequence.ids = sequence.ids[1:]
	return next, nil
}

func (sequence *IDSequence) Calls() int {
	if sequence == nil {
		return 0
	}
	sequence.mu.Lock()
	defer sequence.mu.Unlock()
	return sequence.calls
}

func (sequence *IDSequence) Remaining() int {
	if sequence == nil {
		return 0
	}
	sequence.mu.Lock()
	defer sequence.mu.Unlock()
	return len(sequence.ids)
}

// Gate reports when a goroutine reaches a deterministic boundary and blocks
// it until the test releases the boundary or cancels the context.
type Gate struct {
	entered     chan struct{}
	released    chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func NewGate() *Gate {
	return &Gate{entered: make(chan struct{}), released: make(chan struct{})}
}

func (gate *Gate) Wait(ctx context.Context) error {
	if gate == nil || ctx == nil {
		return ErrGateUnavailable
	}
	gate.enteredOnce.Do(func() { close(gate.entered) })
	select {
	case <-gate.released:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (gate *Gate) AwaitEntered(ctx context.Context) error {
	if gate == nil || ctx == nil {
		return ErrGateUnavailable
	}
	select {
	case <-gate.entered:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (gate *Gate) Release() {
	if gate == nil {
		return
	}
	gate.releaseOnce.Do(func() { close(gate.released) })
}

// FaultPlan returns configured errors at named checkpoints in deterministic
// order. Missing or exhausted points are no-ops.
type FaultPlan struct {
	mu       sync.Mutex
	planned  map[string][]error
	hitCount map[string]int
}

func NewFaultPlan(planned map[string][]error) *FaultPlan {
	copy := make(map[string][]error, len(planned))
	for point, failures := range planned {
		copy[point] = append([]error(nil), failures...)
	}
	return &FaultPlan{planned: copy, hitCount: make(map[string]int)}
}

func (plan *FaultPlan) Check(point string) error {
	if plan == nil {
		return nil
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.hitCount[point]++
	failures := plan.planned[point]
	if len(failures) == 0 {
		return nil
	}
	failure := failures[0]
	plan.planned[point] = failures[1:]
	return failure
}

func (plan *FaultPlan) Hits(point string) int {
	if plan == nil {
		return 0
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	return plan.hitCount[point]
}

func (plan *FaultPlan) Remaining(point string) int {
	if plan == nil {
		return 0
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	return len(plan.planned[point])
}
