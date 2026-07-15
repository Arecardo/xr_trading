package longbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	protocol "github.com/longbridge/openapi-protocol/go"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestClassifyClientError(t *testing.T) {
	t.Parallel()
	code := mustLongbridgeCode(t, providerName)
	tests := []struct {
		name  string
		op    operation
		err   error
		code  ports.ProviderErrorCode
		retry bool
	}{
		{name: "rate", op: barOperation, err: protocol.NewError(3, 301606, "secret rate detail"), code: ports.ProviderErrorRateLimited, retry: true},
		{name: "server", op: barOperation, err: protocol.NewError(7, 301602, "server detail"), code: ports.ProviderErrorTemporaryUnavailable, retry: true},
		{name: "permission", op: barOperation, err: protocol.NewError(7, 301604, "permission detail"), code: ports.ProviderErrorUnauthorized},
		{name: "history quota", op: barOperation, err: protocol.NewError(7, 301607, "quota detail"), code: ports.ProviderErrorUnauthorized},
		{name: "quote size", op: quoteOperation, err: protocol.NewError(7, 301607, "size detail"), code: ports.ProviderErrorBadRequest},
		{name: "no quote", op: barOperation, err: protocol.NewError(7, 301603, "no quote detail"), code: ports.ProviderErrorInvalidInstrument},
		{name: "params", op: barOperation, err: protocol.NewError(7, 301600, "param detail"), code: ports.ProviderErrorBadRequest},
		{name: "protocol unknown", op: barOperation, err: protocol.NewError(7, 399999, "unknown detail"), code: ports.ProviderErrorUnknown, retry: true},
		{name: "network", op: barOperation, err: testNetworkError{}, code: ports.ProviderErrorNetwork, retry: true},
		{name: "plain unknown", op: barOperation, err: errors.New("technical detail"), code: ports.ProviderErrorUnknown, retry: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyClientError(code, test.op, context.Background(), test.err)
			classified, ok := ports.AsProviderError(err)
			if !ok || classified.Code != test.code || classified.Code.Retryable() != test.retry {
				t.Fatalf("error = %#v", err)
			}
			if classified.Error() == test.err.Error() {
				t.Fatal("safe error exposed technical cause")
			}
			if test.code == ports.ProviderErrorRateLimited && (classified.RetryAfter == nil || *classified.RetryAfter != time.Second) {
				t.Fatalf("RetryAfter = %#v", classified.RetryAfter)
			}
		})
	}
	if classifyClientError(code, barOperation, context.Background(), nil) != nil {
		t.Fatal("nil error was classified")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := classifyClientError(code, barOperation, canceled, errors.New("ignored")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestProviderErrorHelpers(t *testing.T) {
	t.Parallel()
	code := mustLongbridgeCode(t, providerName)
	tests := []struct {
		err  error
		code ports.ProviderErrorCode
	}{
		{badRequestError(code, "bad request", nil), ports.ProviderErrorBadRequest},
		{invalidInstrumentError(code, "invalid instrument", nil), ports.ProviderErrorInvalidInstrument},
		{unsupportedIntervalError(code, nil), ports.ProviderErrorUnsupportedInterval},
		{invalidResponseError(code, "invalid response", nil), ports.ProviderErrorInvalidResponse},
	}
	for _, test := range tests {
		if codeOf(test.err) != test.code {
			t.Fatalf("error = %#v", test.err)
		}
	}
	if err := providerError(domain.Code{}, ports.ProviderErrorNetwork, "network", nil, nil); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("invalid provider error = %v", err)
	}
}

type testNetworkError struct{}

func (testNetworkError) Error() string   { return "network detail" }
func (testNetworkError) Timeout() bool   { return true }
func (testNetworkError) Temporary() bool { return true }
