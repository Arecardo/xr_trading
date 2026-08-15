package coingecko

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestDoGetClassifiesHTTPStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       int
		retryAfter   string
		body         string
		wantCode     ports.ProviderErrorCode
		wantRetry    bool
		wantMinDelay time.Duration
	}{
		{name: "rate limited with retry-after", status: http.StatusTooManyRequests, retryAfter: "30", wantCode: ports.ProviderErrorRateLimited, wantRetry: true, wantMinDelay: 30 * time.Second},
		{name: "rate limited without retry-after", status: http.StatusTooManyRequests, wantCode: ports.ProviderErrorRateLimited, wantRetry: true, wantMinDelay: defaultRateLimitDelay},
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: ports.ProviderErrorUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, wantCode: ports.ProviderErrorUnauthorized},
		{name: "bad request", status: http.StatusBadRequest, wantCode: ports.ProviderErrorBadRequest},
		{name: "not found", status: http.StatusNotFound, wantCode: ports.ProviderErrorBadRequest},
		{name: "server error", status: http.StatusBadGateway, wantCode: ports.ProviderErrorTemporaryUnavailable},
		{name: "unexpected status", status: http.StatusTeapot, wantCode: ports.ProviderErrorUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
				if test.retryAfter != "" {
					writer.Header().Set("Retry-After", test.retryAfter)
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			})
			_, err := adapter.doGet(context.Background(), simplePricePath, url.Values{})
			classified, ok := ports.AsProviderError(err)
			if !ok || classified.Code != test.wantCode {
				t.Fatalf("doGet() error = %#v, want code %s", err, test.wantCode)
			}
			if test.wantRetry {
				if classified.RetryAfter == nil || *classified.RetryAfter < test.wantMinDelay {
					t.Fatalf("RetryAfter = %v, want >= %v", classified.RetryAfter, test.wantMinDelay)
				}
			} else if classified.RetryAfter != nil {
				t.Fatalf("RetryAfter = %v, want nil", classified.RetryAfter)
			}
		})
	}
}

func TestDoGetRejectsOversizedAndMalformedResponses(t *testing.T) {
	t.Parallel()

	oversized, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"padding":"` + string(make([]byte, 64)) + `"}`))
	}, func(config *Config) { config.MaxResponseBytes = 8 })
	if _, err := oversized.doGet(context.Background(), simplePricePath, url.Values{}); !isCode(t, err, ports.ProviderErrorInvalidResponse) {
		t.Fatalf("oversized doGet() error = %v", err)
	}

	malformed, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{not json`))
	})
	if _, err := malformed.doGet(context.Background(), simplePricePath, url.Values{}); !isCode(t, err, ports.ProviderErrorInvalidResponse) {
		t.Fatalf("malformed doGet() error = %v", err)
	}
}

func TestDoGetRequiresContextAndReportsNetworkFailure(t *testing.T) {
	t.Parallel()

	adapter, server := newHTTPTestAdapter(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := adapter.doGet(nil, simplePricePath, url.Values{}); !isCode(t, err, ports.ProviderErrorBadRequest) {
		t.Fatalf("doGet(nil context) error = %v", err)
	}
	server.Close()
	if _, err := adapter.doGet(context.Background(), simplePricePath, url.Values{}); !isCode(t, err, ports.ProviderErrorNetwork) {
		t.Fatalf("doGet(closed server) error = %v", err)
	}
}

func TestDoGetHonorsCanceledContextOverTransportError(t *testing.T) {
	t.Parallel()

	adapter, server := newHTTPTestAdapter(t, func(http.ResponseWriter, *http.Request) {})
	server.Close()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.doGet(canceled, simplePricePath, url.Values{}); err == nil {
		t.Fatal("doGet(canceled) error = nil")
	}
}

func TestRetryDelayParsesHTTPDateFallback(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	future := time.Now().Add(2 * time.Minute).UTC()
	header.Set("Retry-After", future.Format(http.TimeFormat))
	delay := retryDelay(header, time.Now().UTC(), time.Second)
	if delay <= time.Minute {
		t.Fatalf("retryDelay(HTTP-date) = %v, want > 1m", delay)
	}
	if got := retryDelay(http.Header{}, time.Now(), 42*time.Second); got != 42*time.Second {
		t.Fatalf("retryDelay(no header) = %v", got)
	}
}

func isCode(t *testing.T, err error, code ports.ProviderErrorCode) bool {
	t.Helper()
	classified, ok := ports.AsProviderError(err)
	return ok && classified.Code == code
}
