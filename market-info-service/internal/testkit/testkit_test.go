package testkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
)

func TestManualClockSupportsConcurrentReadsAndExplicitMovement(t *testing.T) {
	start := time.Date(2026, time.July, 20, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	clock := NewManualClock(start)
	if got := clock.Now(); got.Location() != time.UTC || !got.Equal(start) {
		t.Fatalf("Now() = %v", got)
	}

	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			if clock.Now().IsZero() {
				t.Error("concurrent Now() returned zero")
			}
		}()
	}
	readers.Wait()

	if got := clock.Advance(90 * time.Minute); !got.Equal(start.Add(90 * time.Minute)) {
		t.Fatalf("Advance() = %v", got)
	}
	want := time.Date(2026, time.July, 21, 1, 2, 3, 0, time.UTC)
	clock.Set(want)
	if got := clock.Now(); got != want {
		t.Fatalf("Now() after Set = %v", got)
	}
	var nilClock *ManualClock
	if !nilClock.Now().IsZero() || !nilClock.Advance(time.Hour).IsZero() {
		t.Fatal("nil ManualClock must return zero time")
	}
	nilClock.Set(want)
}

func TestIDSequenceIsOrderedAndFailsWhenExhausted(t *testing.T) {
	first := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897001"))
	second := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca2727897002"))
	input := []domain.ID{first, second}
	sequence := NewIDSequence(input...)
	input[0] = domain.ID{}

	for _, want := range []domain.ID{first, second} {
		got, err := sequence.Next()
		if err != nil || got != want {
			t.Fatalf("Next() = (%s, %v), want %s", got, err, want)
		}
	}
	if got, err := sequence.Next(); got != (domain.ID{}) || !errors.Is(err, ErrIDSequenceExhausted) {
		t.Fatalf("Next(exhausted) = (%s, %v)", got, err)
	}
	if sequence.Calls() != 3 || sequence.Remaining() != 0 {
		t.Fatalf("sequence calls=%d remaining=%d", sequence.Calls(), sequence.Remaining())
	}
	var nilSequence *IDSequence
	if _, err := nilSequence.Next(); !errors.Is(err, ErrIDSequenceExhausted) || nilSequence.Calls() != 0 || nilSequence.Remaining() != 0 {
		t.Fatal("nil IDSequence contract violated")
	}
}

func TestGateCoordinatesWithoutSleepingAndSupportsCancellation(t *testing.T) {
	gate := NewGate()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- gate.Wait(ctx) }()
	if err := gate.AwaitEntered(ctx); err != nil {
		t.Fatalf("AwaitEntered() error = %v", err)
	}
	gate.Release()
	gate.Release()
	if err := <-result; err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	canceled, stop := context.WithCancel(context.Background())
	cancelGate := NewGate()
	stop()
	if err := cancelGate.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait(canceled) error = %v", err)
	}
	if err := cancelGate.AwaitEntered(context.Background()); err != nil {
		t.Fatalf("AwaitEntered(canceled gate) error = %v", err)
	}
	var nilGate *Gate
	if !errors.Is(nilGate.Wait(ctx), ErrGateUnavailable) || !errors.Is(nilGate.AwaitEntered(ctx), ErrGateUnavailable) {
		t.Fatal("nil Gate contract violated")
	}
	nilGate.Release()
}

func TestFaultPlanConsumesNamedFailuresInOrder(t *testing.T) {
	first := errors.New("first failure")
	second := errors.New("second failure")
	planned := map[string][]error{"commit": {first, nil, second}}
	plan := NewFaultPlan(planned)
	planned["commit"][0] = nil

	for _, want := range []error{first, nil, second, nil} {
		if got := plan.Check("commit"); !errors.Is(got, want) {
			t.Fatalf("Check(commit) = %v, want %v", got, want)
		}
	}
	if got := plan.Check("other"); got != nil {
		t.Fatalf("Check(other) = %v", got)
	}
	if plan.Hits("commit") != 4 || plan.Remaining("commit") != 0 || plan.Hits("other") != 1 {
		t.Fatalf("fault plan hits=%d other=%d remaining=%d", plan.Hits("commit"), plan.Hits("other"), plan.Remaining("commit"))
	}
	var nilPlan *FaultPlan
	if nilPlan.Check("point") != nil || nilPlan.Hits("point") != 0 || nilPlan.Remaining("point") != 0 {
		t.Fatal("nil FaultPlan contract violated")
	}
}
