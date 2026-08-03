// Package servicehost coordinates long-running market information components.
package servicehost

import (
	"context"
	"errors"
	"fmt"
)

// Runner is implemented by HTTP servers, ingestion workers, and periodic
// scheduler drivers. A canceled context must result in a prompt, nil return.
type Runner interface {
	Run(context.Context) error
}

// Component gives a runner a stable, non-sensitive name for lifecycle errors.
type Component struct {
	Name   string
	Runner Runner
}

type componentResult struct {
	name string
	err  error
}

// Run starts every component, cancels siblings when one stops, and waits for
// all goroutines before returning. A component stopping while the parent
// context remains active is treated as a process failure.
func Run(ctx context.Context, components ...Component) error {
	if ctx == nil {
		return errors.New("service host context is required")
	}
	if len(components) == 0 {
		return errors.New("at least one service host component is required")
	}
	for _, component := range components {
		if component.Name == "" || component.Runner == nil {
			return errors.New("service host component name and runner are required")
		}
	}

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan componentResult, len(components))
	for _, component := range components {
		component := component
		go func() {
			results <- componentResult{name: component.Name, err: component.Runner.Run(runContext)}
		}()
	}

	first := <-results
	cancel()
	all := []componentResult{first}
	for len(all) < len(components) {
		all = append(all, <-results)
	}
	for _, result := range all {
		if result.err != nil {
			return fmt.Errorf("%s component failed: %w", result.name, result.err)
		}
	}
	if ctx.Err() == nil {
		return fmt.Errorf("%s component stopped unexpectedly", first.name)
	}
	return nil
}
