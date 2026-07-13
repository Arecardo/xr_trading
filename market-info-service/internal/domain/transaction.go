package domain

import "context"

// Transaction is an opaque transaction handle. Domain contracts pass it to
// repository methods that require atomicity without exposing a SQL driver.
type Transaction interface {
	TransactionMarker()
}

// TransactionRunner executes fn atomically. A successful return commits the
// transaction; a returned error rolls it back.
type TransactionRunner interface {
	WithinTransaction(context.Context, func(context.Context, Transaction) error) error
}
