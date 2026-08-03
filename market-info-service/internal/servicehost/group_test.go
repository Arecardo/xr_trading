package servicehost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type runnerFunc func(context.Context) error

func (run runnerFunc) Run(ctx context.Context) error { return run(ctx) }

func TestRunCoordinatesCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 2)
	stopped := make(chan struct{}, 2)
	component := func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return nil
	}
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx,
			Component{Name: "first", Runner: runnerFunc(component)},
			Component{Name: "second", Runner: runnerFunc(component)},
		)
	}()
	<-started
	<-started
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	<-stopped
	<-stopped
}

func TestRunCancelsSiblingOnFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("boom")
	siblingStopped := make(chan struct{})
	var once sync.Once
	err := Run(context.Background(),
		Component{Name: "failing", Runner: runnerFunc(func(context.Context) error { return failure })},
		Component{Name: "sibling", Runner: runnerFunc(func(ctx context.Context) error {
			<-ctx.Done()
			once.Do(func() { close(siblingStopped) })
			return nil
		})},
	)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "failing component failed") {
		t.Fatalf("Run() error = %v", err)
	}
	<-siblingStopped
}

func TestRunRejectsInvalidAndUnexpectedComponents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ctx        context.Context
		components []Component
		want       string
	}{
		{name: "nil context", components: []Component{{Name: "x", Runner: runnerFunc(func(context.Context) error { return nil })}}, want: "context"},
		{name: "empty", ctx: context.Background(), want: "at least one"},
		{name: "missing name", ctx: context.Background(), components: []Component{{Runner: runnerFunc(func(context.Context) error { return nil })}}, want: "name and runner"},
		{name: "missing runner", ctx: context.Background(), components: []Component{{Name: "x"}}, want: "name and runner"},
		{name: "unexpected stop", ctx: context.Background(), components: []Component{{Name: "x", Runner: runnerFunc(func(context.Context) error { return nil })}}, want: "stopped unexpectedly"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Run(test.ctx, test.components...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
