package scheduler

import (
	"errors"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

func TestCalculateContinuousWindowBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		interval   domain.BarInterval
		trigger    WindowTrigger
		nextOpen   string
		observedAt string
		delay      time.Duration
		wantStart  string
		wantEnd    string
		wantDue    string
		wantNone   bool
	}{
		{
			name:     "hour close is not due one nanosecond before delay",
			interval: domain.BarInterval1Hour, trigger: WindowTriggerClose,
			nextOpen: "2026-07-18T10:00:00Z", observedAt: "2026-07-18T11:01:59.999999999Z", delay: 2 * time.Minute,
			wantNone: true,
		},
		{
			name:     "hour close becomes due exactly at delay boundary",
			interval: domain.BarInterval1Hour, trigger: WindowTriggerClose,
			nextOpen: "2026-07-18T10:00:00Z", observedAt: "2026-07-18T11:02:00Z", delay: 2 * time.Minute,
			wantStart: "2026-07-18T10:00:00Z", wantEnd: "2026-07-18T11:00:00Z", wantDue: "2026-07-18T11:02:00Z",
		},
		{
			name:     "restart catches up every eligible hour in one range",
			interval: domain.BarInterval1Hour, trigger: WindowTriggerClose,
			nextOpen: "2026-07-18T07:00:00Z", observedAt: "2026-07-18T11:37:00Z", delay: 2 * time.Minute,
			wantStart: "2026-07-18T07:00:00Z", wantEnd: "2026-07-18T11:00:00Z", wantDue: "2026-07-18T11:02:00Z",
		},
		{
			name:     "UTC plus eight input is normalized before hourly calculation",
			interval: domain.BarInterval1Hour, trigger: WindowTriggerClose,
			nextOpen: "2026-07-18T18:00:00+08:00", observedAt: "2026-07-18T19:02:00+08:00", delay: 2 * time.Minute,
			wantStart: "2026-07-18T10:00:00Z", wantEnd: "2026-07-18T11:00:00Z", wantDue: "2026-07-18T11:02:00Z",
		},
		{
			name:     "crypto day is anchored at UTC midnight",
			interval: domain.BarInterval1Day, trigger: WindowTriggerClose,
			nextOpen: "2026-07-16T00:00:00Z", observedAt: "2026-07-18T00:02:00Z", delay: 2 * time.Minute,
			wantStart: "2026-07-16T00:00:00Z", wantEnd: "2026-07-18T00:00:00Z", wantDue: "2026-07-18T00:02:00Z",
		},
		{
			name:     "revision uses its independent delay from bar close",
			interval: domain.BarInterval1Day, trigger: WindowTriggerRevision,
			nextOpen: "2026-07-17T00:00:00Z", observedAt: "2026-07-18T00:59:59Z", delay: time.Hour,
			wantNone: true,
		},
		{
			name:     "revision is due at its exact delayed boundary",
			interval: domain.BarInterval1Day, trigger: WindowTriggerRevision,
			nextOpen: "2026-07-17T00:00:00Z", observedAt: "2026-07-18T01:00:00Z", delay: time.Hour,
			wantStart: "2026-07-17T00:00:00Z", wantEnd: "2026-07-18T00:00:00Z", wantDue: "2026-07-18T01:00:00Z",
		},
		{
			name:     "already scheduled cursor produces no duplicate range",
			interval: domain.BarInterval1Hour, trigger: WindowTriggerClose,
			nextOpen: "2026-07-18T11:00:00Z", observedAt: "2026-07-18T11:02:00Z", delay: 2 * time.Minute,
			wantNone: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := ContinuousWindowRequest{
				Interval: test.interval, Trigger: test.trigger,
				NextOpenTime: mustTime(t, test.nextOpen), Delay: test.delay,
			}
			window, err := CalculateContinuousWindow(request, mustTime(t, test.observedAt))
			if err != nil {
				t.Fatalf("CalculateContinuousWindow() error = %v", err)
			}
			if test.wantNone {
				if window != nil {
					t.Fatalf("CalculateContinuousWindow() = %#v, want nil", window)
				}
				return
			}
			if window == nil || window.Interval != test.interval || window.Trigger != test.trigger {
				t.Fatalf("CalculateContinuousWindow() = %#v", window)
			}
			assertTime(t, "RangeStart", window.RangeStart, test.wantStart)
			assertTime(t, "RangeEnd", window.RangeEnd, test.wantEnd)
			assertTime(t, "EligibleAt", window.EligibleAt, test.wantDue)
		})
	}
}

func TestCalculateContinuousWindowRejectsInvalidInput(t *testing.T) {
	valid := ContinuousWindowRequest{
		Interval: domain.BarInterval1Hour, Trigger: WindowTriggerClose,
		NextOpenTime: mustTime(t, "2026-07-18T10:00:00Z"), Delay: 2 * time.Minute,
	}
	observedAt := mustTime(t, "2026-07-18T11:02:00Z")
	tests := []struct {
		name   string
		mutate func(*ContinuousWindowRequest, *time.Time)
	}{
		{"unsupported interval", func(request *ContinuousWindowRequest, _ *time.Time) { request.Interval = "5m" }},
		{"unsupported trigger", func(request *ContinuousWindowRequest, _ *time.Time) { request.Trigger = "repair" }},
		{"zero next open", func(request *ContinuousWindowRequest, _ *time.Time) { request.NextOpenTime = time.Time{} }},
		{"unaligned hour", func(request *ContinuousWindowRequest, _ *time.Time) {
			request.NextOpenTime = request.NextOpenTime.Add(time.Minute)
		}},
		{"negative delay", func(request *ContinuousWindowRequest, _ *time.Time) { request.Delay = -time.Second }},
		{"zero observed time", func(_ *ContinuousWindowRequest, observedAt *time.Time) { *observedAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			now := observedAt
			test.mutate(&request, &now)
			if _, err := CalculateContinuousWindow(request, now); !errors.Is(err, domain.ErrInvalidData) {
				t.Fatalf("CalculateContinuousWindow() error = %v, want ErrInvalidData", err)
			}
		})
	}
}

func TestDelayPolicy(t *testing.T) {
	revisionSeconds := 3600
	policy, err := NewDelayPolicy(120, &revisionSeconds)
	if err != nil {
		t.Fatalf("NewDelayPolicy() error = %v", err)
	}
	if delay, enabled, err := policy.Delay(WindowTriggerClose); err != nil || !enabled || delay != 2*time.Minute {
		t.Fatalf("Delay(close) = (%v, %t, %v)", delay, enabled, err)
	}
	if delay, enabled, err := policy.Delay(WindowTriggerRevision); err != nil || !enabled || delay != time.Hour {
		t.Fatalf("Delay(revision) = (%v, %t, %v)", delay, enabled, err)
	}
	withoutRevision, err := NewDelayPolicy(0, nil)
	if err != nil {
		t.Fatalf("NewDelayPolicy(no revision) error = %v", err)
	}
	if delay, enabled, err := withoutRevision.Delay(WindowTriggerRevision); err != nil || enabled || delay != 0 {
		t.Fatalf("Delay(disabled revision) = (%v, %t, %v)", delay, enabled, err)
	}
	if _, _, err := policy.Delay("repair"); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Delay(invalid trigger) error = %v", err)
	}
	negativeRevision := -1
	if _, err := NewDelayPolicy(-1, nil); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("NewDelayPolicy(negative close) error = %v", err)
	}
	if _, err := NewDelayPolicy(0, &negativeRevision); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("NewDelayPolicy(negative revision) error = %v", err)
	}
	if _, _, err := (DelayPolicy{CloseDelay: -time.Second}).Delay(WindowTriggerClose); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Delay(invalid close policy) error = %v", err)
	}
	negativeDuration := -time.Second
	if _, _, err := (DelayPolicy{RevisionDelay: &negativeDuration}).Delay(WindowTriggerRevision); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("Delay(invalid revision policy) error = %v", err)
	}
}

func TestPlannerUsesInjectedClock(t *testing.T) {
	request := ContinuousWindowRequest{
		Interval: domain.BarInterval1Hour, Trigger: WindowTriggerClose,
		NextOpenTime: mustTime(t, "2026-07-18T10:00:00Z"), Delay: 2 * time.Minute,
	}
	planner, err := NewPlanner(fixedClock{now: mustTime(t, "2026-07-18T11:02:00Z")})
	if err != nil {
		t.Fatalf("NewPlanner() error = %v", err)
	}
	window, err := planner.ContinuousWindow(request)
	if err != nil || window == nil || !window.RangeEnd.Equal(mustTime(t, "2026-07-18T11:00:00Z")) {
		t.Fatalf("ContinuousWindow() = (%#v, %v)", window, err)
	}
	if _, err := NewPlanner(nil); err == nil {
		t.Fatal("NewPlanner(nil) error = nil")
	}
	var nilPlanner *Planner
	if _, err := nilPlanner.ContinuousWindow(request); err == nil {
		t.Fatal("nil Planner.ContinuousWindow() error = nil")
	}
	if (SystemClock{}).Now().IsZero() {
		t.Fatal("SystemClock.Now() is zero")
	}
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("time.Parse(%q) error = %v", value, err)
	}
	return parsed
}

func assertTime(t *testing.T, field string, got time.Time, want string) {
	t.Helper()
	wantTime := mustTime(t, want)
	if !got.Equal(wantTime) || got.Location() != time.UTC {
		t.Fatalf("%s = %v (%v), want %v UTC", field, got, got.Location(), wantTime)
	}
}
