package ports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

func TestProviderErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code      ProviderErrorCode
		retryable bool
	}{
		{ProviderErrorRateLimited, true},
		{ProviderErrorNetwork, true},
		{ProviderErrorTemporaryUnavailable, true},
		{ProviderErrorUnauthorized, false},
		{ProviderErrorInvalidInstrument, false},
		{ProviderErrorUnsupportedInterval, false},
		{ProviderErrorBadRequest, false},
		{ProviderErrorInvalidResponse, false},
		{ProviderErrorUnknown, true},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			if parsed, err := ParseProviderErrorCode(string(test.code)); err != nil || parsed != test.code {
				t.Fatalf("ParseProviderErrorCode() = (%q, %v)", parsed, err)
			}
			if test.code.Retryable() != test.retryable {
				t.Fatalf("Retryable() = %t, want %t", test.code.Retryable(), test.retryable)
			}
		})
	}
	if _, err := ParseProviderErrorCode("tls_problem"); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ParseProviderErrorCode(invalid) error = %v", err)
	}
}

func TestProviderErrorSafeWrapping(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret upstream body with api_key")
	delay := 2 * time.Second
	providerError, err := NewProviderError(mustAdapterCode(t, "bybit"), ProviderErrorRateLimited, "provider rate limit reached", &delay, cause)
	if err != nil {
		t.Fatalf("NewProviderError() error = %v", err)
	}
	wrapped := fmt.Errorf("fetch quotes: %w", providerError)
	classified, ok := AsProviderError(wrapped)
	if !ok || classified != providerError || !IsRetryableProviderError(wrapped) || !errors.Is(wrapped, cause) {
		t.Fatalf("classified provider error = (%#v, %t)", classified, ok)
	}
	if strings.Contains(providerError.Error(), "api_key") || !strings.Contains(providerError.Error(), "rate_limited") {
		t.Fatalf("ProviderError.Error() leaked cause or omitted classification: %q", providerError.Error())
	}
	if (*ProviderError)(nil).Error() != "provider error" || (*ProviderError)(nil).Unwrap() != nil {
		t.Fatal("nil provider error methods are not safe")
	}
}

func TestProviderErrorRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	positive := time.Second
	zero := time.Duration(0)
	tests := []struct {
		name       string
		provider   domain.Code
		code       ProviderErrorCode
		message    string
		retryAfter *time.Duration
	}{
		{name: "provider", code: ProviderErrorNetwork, message: "network error"},
		{name: "code", provider: mustAdapterCode(t, "bybit"), code: "tls", message: "network error"},
		{name: "message", provider: mustAdapterCode(t, "bybit"), code: ProviderErrorNetwork, message: " network error"},
		{name: "zero retry", provider: mustAdapterCode(t, "bybit"), code: ProviderErrorNetwork, message: "network error", retryAfter: &zero},
		{name: "non-retryable delay", provider: mustAdapterCode(t, "bybit"), code: ProviderErrorUnauthorized, message: "credentials rejected", retryAfter: &positive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewProviderError(test.provider, test.code, test.message, test.retryAfter, nil); !errors.Is(err, domain.ErrInvalidData) {
				t.Fatalf("NewProviderError() error = %v, want invalid data", err)
			}
		})
	}

	invalid := &ProviderError{ProviderCode: mustAdapterCode(t, "bybit"), Code: "bad", Message: "bad code"}
	if _, ok := AsProviderError(invalid); ok || IsRetryableProviderError(errors.New("ordinary")) {
		t.Fatal("invalid or ordinary errors must not be classified")
	}
}
