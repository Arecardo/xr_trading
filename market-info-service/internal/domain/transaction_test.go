package domain

import (
	"context"
	"errors"
	"testing"
)

type fakeTransaction struct{}

func (fakeTransaction) TransactionMarker() {}

type fakeTransactionRunner struct {
	committed bool
}

func (runner *fakeTransactionRunner) WithinTransaction(ctx context.Context, fn func(context.Context, Transaction) error) error {
	err := fn(ctx, fakeTransaction{})
	runner.committed = err == nil
	return err
}

func TestTransactionRunnerContract(t *testing.T) {
	runner := &fakeTransactionRunner{}
	err := runner.WithinTransaction(context.Background(), func(_ context.Context, transaction Transaction) error {
		if transaction == nil {
			t.Fatal("transaction is nil")
		}
		return nil
	})
	if err != nil || !runner.committed {
		t.Fatalf("success transaction = (%v, committed=%t)", err, runner.committed)
	}

	want := errors.New("rollback")
	err = runner.WithinTransaction(context.Background(), func(context.Context, Transaction) error { return want })
	if !errors.Is(err, want) || runner.committed {
		t.Fatalf("failed transaction = (%v, committed=%t)", err, runner.committed)
	}
}
