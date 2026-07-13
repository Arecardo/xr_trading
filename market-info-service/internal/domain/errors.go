package domain

import "errors"

// Stable errors used by application code and HTTP error mapping. Repository
// implementations retain the original driver error while wrapping one of these.
var (
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrReferenceViolation  = errors.New("reference violation")
	ErrInvalidData         = errors.New("invalid data")
	ErrRetryable           = errors.New("retryable database error")
	ErrDatabaseUnavailable = errors.New("database unavailable")
	ErrInvalidState        = errors.New("invalid state")
)
