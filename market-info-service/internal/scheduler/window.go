// Package scheduler calculates market-time collection windows and, in later
// phases, creates durable ingestion runs. It never calls provider fetch APIs.
package scheduler

import (
	"errors"
	"fmt"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

// WindowTrigger distinguishes the first collection after a bar closes from a
// later revision check. Both delays are measured from the bar close boundary.
type WindowTrigger string

const (
	WindowTriggerClose    WindowTrigger = "close"
	WindowTriggerRevision WindowTrigger = "revision"
)

// Clock supplies the scheduling instant. Window arithmetic remains a pure
// function and accepts the observed time explicitly.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production wall clock.
type SystemClock struct{}

// Now returns the current instant.
func (SystemClock) Now() time.Time { return time.Now() }

// DelayPolicy is the immutable timing configuration of a subscription.
// RevisionDelay is nil when no later revision collection is configured.
type DelayPolicy struct {
	CloseDelay    time.Duration
	RevisionDelay *time.Duration
}

// NewDelayPolicy converts the persisted second values without allowing a
// negative or overflowing duration.
func NewDelayPolicy(closeDelaySeconds int, revisionDelaySeconds *int) (DelayPolicy, error) {
	closeDelay, err := secondsDuration(closeDelaySeconds)
	if err != nil {
		return DelayPolicy{}, fmt.Errorf("construct close delay: %w", err)
	}
	policy := DelayPolicy{CloseDelay: closeDelay}
	if revisionDelaySeconds != nil {
		revisionDelay, err := secondsDuration(*revisionDelaySeconds)
		if err != nil {
			return DelayPolicy{}, fmt.Errorf("construct revision delay: %w", err)
		}
		policy.RevisionDelay = &revisionDelay
	}
	return policy, nil
}

// Delay returns the configured delay for one trigger. A disabled revision
// trigger returns enabled=false rather than inventing a revision schedule.
func (policy DelayPolicy) Delay(trigger WindowTrigger) (delay time.Duration, enabled bool, err error) {
	switch trigger {
	case WindowTriggerClose:
		if policy.CloseDelay < 0 {
			return 0, false, invalidWindow("close delay cannot be negative")
		}
		return policy.CloseDelay, true, nil
	case WindowTriggerRevision:
		if policy.RevisionDelay == nil {
			return 0, false, nil
		}
		if *policy.RevisionDelay < 0 {
			return 0, false, invalidWindow("revision delay cannot be negative")
		}
		return *policy.RevisionDelay, true, nil
	default:
		return 0, false, invalidWindow("unsupported window trigger")
	}
}

// ContinuousWindowRequest identifies the earliest bar not yet scheduled for a
// trigger. NextOpenTime must be aligned to the interval's UTC boundary.
type ContinuousWindowRequest struct {
	Interval     domain.BarInterval
	Trigger      WindowTrigger
	NextOpenTime time.Time
	Delay        time.Duration
}

// CollectionWindow is a contiguous, interval-aligned [RangeStart, RangeEnd)
// task range. It may contain several bars after a service restart.
type CollectionWindow struct {
	Interval   domain.BarInterval
	Trigger    WindowTrigger
	RangeStart time.Time
	RangeEnd   time.Time
	EligibleAt time.Time
}

// CalculateContinuousWindow computes the bars eligible at observedAt on a
// 7x24 UTC time axis. A nil window means no complete bar has reached its delay.
func CalculateContinuousWindow(request ContinuousWindowRequest, observedAt time.Time) (*CollectionWindow, error) {
	intervalDuration, err := durationForInterval(request.Interval)
	if err != nil {
		return nil, err
	}
	if request.Trigger != WindowTriggerClose && request.Trigger != WindowTriggerRevision {
		return nil, invalidWindow("unsupported window trigger")
	}
	if observedAt.IsZero() {
		return nil, invalidWindow("observed time is required")
	}
	if request.NextOpenTime.IsZero() {
		return nil, invalidWindow("next open time is required")
	}
	if request.Delay < 0 {
		return nil, invalidWindow("delay cannot be negative")
	}

	nextOpen := request.NextOpenTime.UTC()
	if !nextOpen.Equal(nextOpen.Truncate(intervalDuration)) {
		return nil, invalidWindow("next open time must align to the UTC interval boundary")
	}
	observedAt = observedAt.UTC()
	latestEligibleEnd := observedAt.Add(-request.Delay).Truncate(intervalDuration)
	if !nextOpen.Before(latestEligibleEnd) {
		return nil, nil
	}

	return &CollectionWindow{
		Interval:   request.Interval,
		Trigger:    request.Trigger,
		RangeStart: nextOpen,
		RangeEnd:   latestEligibleEnd,
		EligibleAt: latestEligibleEnd.Add(request.Delay),
	}, nil
}

// CalculateLatestContinuousWindow returns exactly the newest eligible bar on
// a 7x24 UTC axis. Missed earlier bars are handled by SCH-004 checkpoint catch-up.
func CalculateLatestContinuousWindow(interval domain.BarInterval, trigger WindowTrigger, observedAt time.Time, delay time.Duration) (*CollectionWindow, error) {
	duration, err := durationForInterval(interval)
	if err != nil {
		return nil, err
	}
	if observedAt.IsZero() || delay < 0 || (trigger != WindowTriggerClose && trigger != WindowTriggerRevision) {
		return nil, invalidWindow("latest continuous window input is invalid")
	}
	latestEnd := observedAt.UTC().Add(-delay).Truncate(duration)
	return CalculateContinuousWindow(ContinuousWindowRequest{
		Interval: interval, Trigger: trigger, NextOpenTime: latestEnd.Add(-duration), Delay: delay,
	}, observedAt)
}

// Planner binds the pure window calculator to an injected clock.
type Planner struct {
	clock Clock
}

// NewPlanner constructs a deterministic continuous-market planner.
func NewPlanner(clock Clock) (*Planner, error) {
	if clock == nil {
		return nil, errors.New("scheduler clock is required")
	}
	return &Planner{clock: clock}, nil
}

// ContinuousWindow calculates one close or revision window at Clock.Now().
func (planner *Planner) ContinuousWindow(request ContinuousWindowRequest) (*CollectionWindow, error) {
	if planner == nil || planner.clock == nil {
		return nil, errors.New("scheduler planner is not initialized")
	}
	return CalculateContinuousWindow(request, planner.clock.Now())
}

func durationForInterval(interval domain.BarInterval) (time.Duration, error) {
	if _, err := domain.ParseBarInterval(string(interval)); err != nil {
		return 0, invalidWindow("unsupported bar interval")
	}
	switch interval {
	case domain.BarInterval1Hour:
		return time.Hour, nil
	case domain.BarInterval1Day:
		return 24 * time.Hour, nil
	default:
		return 0, invalidWindow("unsupported bar interval")
	}
}

func secondsDuration(seconds int) (time.Duration, error) {
	if seconds < 0 || int64(seconds) > int64((time.Duration(1<<63-1))/time.Second) {
		return 0, invalidWindow("delay seconds are outside the supported range")
	}
	return time.Duration(seconds) * time.Second, nil
}

func invalidWindow(message string) error {
	return fmt.Errorf("%s: %w", message, domain.ErrInvalidData)
}
