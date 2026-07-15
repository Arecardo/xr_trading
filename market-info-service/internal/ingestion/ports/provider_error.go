package ports

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"xr-trading/market-info-service/internal/domain"
)

// ProviderErrorCode is a stable provider-failure classification consumed by
// ingestion retry policy. It must never be inferred by matching error text.
type ProviderErrorCode string

const (
	ProviderErrorRateLimited          ProviderErrorCode = "rate_limited"
	ProviderErrorNetwork              ProviderErrorCode = "network"
	ProviderErrorTemporaryUnavailable ProviderErrorCode = "temporary_unavailable"
	ProviderErrorUnauthorized         ProviderErrorCode = "unauthorized"
	ProviderErrorInvalidInstrument    ProviderErrorCode = "invalid_instrument"
	ProviderErrorUnsupportedInterval  ProviderErrorCode = "unsupported_interval"
	ProviderErrorBadRequest           ProviderErrorCode = "bad_request"
	ProviderErrorInvalidResponse      ProviderErrorCode = "invalid_response"
	ProviderErrorUnknown              ProviderErrorCode = "unknown"
)

// ParseProviderErrorCode validates a stable error code.
func ParseProviderErrorCode(value string) (ProviderErrorCode, error) {
	code := ProviderErrorCode(value)
	switch code {
	case ProviderErrorRateLimited, ProviderErrorNetwork, ProviderErrorTemporaryUnavailable,
		ProviderErrorUnauthorized, ProviderErrorInvalidInstrument, ProviderErrorUnsupportedInterval,
		ProviderErrorBadRequest, ProviderErrorInvalidResponse, ProviderErrorUnknown:
		return code, nil
	default:
		return "", invalidContract("unsupported provider error code")
	}
}

// Retryable reports the default retry decision. Ingestion still enforces the
// task's maximum attempt count for every retryable classification.
func (code ProviderErrorCode) Retryable() bool {
	switch code {
	case ProviderErrorRateLimited, ProviderErrorNetwork, ProviderErrorTemporaryUnavailable, ProviderErrorUnknown:
		return true
	default:
		return false
	}
}

// ProviderError contains a safe message and an optional wrapped technical
// cause. Error deliberately excludes Cause so credentials or raw payloads do
// not leak into task error_message or normal logs.
type ProviderError struct {
	ProviderCode domain.Code
	Code         ProviderErrorCode
	Message      string
	RetryAfter   *time.Duration
	Cause        error
}

// NewProviderError constructs a validated provider error.
func NewProviderError(providerCode domain.Code, code ProviderErrorCode, safeMessage string, retryAfter *time.Duration, cause error) (*ProviderError, error) {
	providerError := &ProviderError{ProviderCode: providerCode, Code: code, Message: safeMessage, RetryAfter: retryAfter, Cause: cause}
	if err := providerError.Validate(); err != nil {
		return nil, err
	}
	return providerError, nil
}

// Validate protects stable classification and safe persisted fields.
func (providerError *ProviderError) Validate() error {
	if providerError == nil || providerError.ProviderCode.IsZero() {
		return invalidContract("provider error requires a provider code")
	}
	if _, err := ParseProviderErrorCode(string(providerError.Code)); err != nil {
		return err
	}
	if providerError.Message == "" || strings.TrimSpace(providerError.Message) != providerError.Message || utf8.RuneCountInString(providerError.Message) > 512 {
		return invalidContract("provider error safe message is invalid")
	}
	if providerError.RetryAfter != nil {
		if *providerError.RetryAfter <= 0 || !providerError.Code.Retryable() {
			return invalidContract("retry-after requires a positive retryable provider error")
		}
	}
	return nil
}

// Error returns only classified, explicitly safe fields.
func (providerError *ProviderError) Error() string {
	if providerError == nil {
		return "provider error"
	}
	return fmt.Sprintf("provider %s: %s: %s", providerError.ProviderCode.String(), providerError.Code, providerError.Message)
}

// Unwrap preserves errors.Is/errors.As access to the technical cause without
// including its possibly sensitive text in Error().
func (providerError *ProviderError) Unwrap() error {
	if providerError == nil {
		return nil
	}
	return providerError.Cause
}

// AsProviderError extracts and validates a classified error through wrapping.
func AsProviderError(err error) (*ProviderError, bool) {
	var providerError *ProviderError
	if !errors.As(err, &providerError) || providerError.Validate() != nil {
		return nil, false
	}
	return providerError, true
}

// IsRetryableProviderError applies only stable adapter classification.
func IsRetryableProviderError(err error) bool {
	providerError, ok := AsProviderError(err)
	return ok && providerError.Code.Retryable()
}
