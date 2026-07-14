// Package application contains use-case contracts shared by transport and
// infrastructure boundaries.
package application

import (
	"fmt"
)

// ErrorCode is a stable machine-readable application failure classification.
type ErrorCode string

const (
	ErrorCodeInvalidArgument           ErrorCode = "INVALID_ARGUMENT"
	ErrorCodeInvalidTimeRange          ErrorCode = "INVALID_TIME_RANGE"
	ErrorCodeUnsupportedInterval       ErrorCode = "UNSUPPORTED_INTERVAL"
	ErrorCodeUnauthenticated           ErrorCode = "UNAUTHENTICATED"
	ErrorCodePermissionDenied          ErrorCode = "PERMISSION_DENIED"
	ErrorCodeNotFound                  ErrorCode = "NOT_FOUND"
	ErrorCodeAssetNotFound             ErrorCode = "ASSET_NOT_FOUND"
	ErrorCodeInstrumentNotFound        ErrorCode = "INSTRUMENT_NOT_FOUND"
	ErrorCodeSubscriptionNotFound      ErrorCode = "SUBSCRIPTION_NOT_FOUND"
	ErrorCodeTaskNotFound              ErrorCode = "TASK_NOT_FOUND"
	ErrorCodeConflict                  ErrorCode = "CONFLICT"
	ErrorCodeSubscriptionAlreadyExists ErrorCode = "SUBSCRIPTION_ALREADY_EXISTS"
	ErrorCodeTaskStateConflict         ErrorCode = "TASK_STATE_CONFLICT"
	ErrorCodeBackfillAlreadyRunning    ErrorCode = "BACKFILL_ALREADY_RUNNING"
	ErrorCodeManualRetryAlreadyRunning ErrorCode = "MANUAL_RETRY_ALREADY_RUNNING"
	ErrorCodeRateLimited               ErrorCode = "RATE_LIMITED"
	ErrorCodeInternal                  ErrorCode = "INTERNAL_ERROR"
	ErrorCodeServiceUnavailable        ErrorCode = "SERVICE_UNAVAILABLE"
	ErrorCodeDatabaseUnavailable       ErrorCode = "DATABASE_UNAVAILABLE"
)

var knownErrorCodes = map[ErrorCode]struct{}{
	ErrorCodeInvalidArgument: {}, ErrorCodeInvalidTimeRange: {}, ErrorCodeUnsupportedInterval: {},
	ErrorCodeUnauthenticated: {}, ErrorCodePermissionDenied: {}, ErrorCodeNotFound: {},
	ErrorCodeAssetNotFound: {}, ErrorCodeInstrumentNotFound: {}, ErrorCodeSubscriptionNotFound: {},
	ErrorCodeTaskNotFound: {}, ErrorCodeConflict: {}, ErrorCodeSubscriptionAlreadyExists: {},
	ErrorCodeTaskStateConflict: {}, ErrorCodeBackfillAlreadyRunning: {}, ErrorCodeManualRetryAlreadyRunning: {},
	ErrorCodeRateLimited: {}, ErrorCodeInternal: {}, ErrorCodeServiceUnavailable: {},
	ErrorCodeDatabaseUnavailable: {},
}

// Valid reports whether the code is part of the frozen public contract.
func (code ErrorCode) Valid() bool {
	_, exists := knownErrorCodes[code]
	return exists
}

// Error is a transport-independent application error. Cause is never exposed
// to clients; Details must contain only explicitly safe structured values.
type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Details   map[string]any
	Cause     error
}

// NewError constructs an application error without an internal cause.
func NewError(code ErrorCode, message string, retryable bool, details map[string]any) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable, Details: cloneDetails(details)}
}

// WrapError constructs an application error while preserving an internal cause
// for errors.Is/errors.As and server-side logging.
func WrapError(cause error, code ErrorCode, message string, retryable bool, details map[string]any) *Error {
	appError := NewError(code, message, retryable, details)
	appError.Cause = cause
	return appError
}

// Error returns safe context plus the internal cause for diagnostics.
func (appError *Error) Error() string {
	if appError == nil {
		return "<nil>"
	}
	if appError.Cause != nil {
		return fmt.Sprintf("%s: %v", appError.Message, appError.Cause)
	}
	return appError.Message
}

// Unwrap exposes the internal cause to errors.Is/errors.As.
func (appError *Error) Unwrap() error {
	if appError == nil {
		return nil
	}
	return appError.Cause
}

// Validate checks whether the error is safe to map directly to a public API.
func (appError *Error) Validate() error {
	if appError == nil {
		return fmt.Errorf("application error is required")
	}
	if !appError.Code.Valid() {
		return fmt.Errorf("unknown application error code %q", appError.Code)
	}
	if appError.Message == "" {
		return fmt.Errorf("application error message is required")
	}
	return nil
}

// FieldViolation is a safe validation detail returned in one combined 400
// response instead of forcing clients through one error per field.
type FieldViolation struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// ValidationError constructs the standard INVALID_ARGUMENT error.
func ValidationError(violations []FieldViolation) *Error {
	details := map[string]any(nil)
	if len(violations) > 0 {
		copied := append([]FieldViolation(nil), violations...)
		details = map[string]any{"fields": copied}
	}
	return NewError(ErrorCodeInvalidArgument, "request validation failed", false, details)
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	cloned := make(map[string]any, len(details))
	for key, value := range details {
		cloned[key] = value
	}
	return cloned
}
