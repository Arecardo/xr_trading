package bybit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/ingestion/ports"
)

func TestHTTPStatusClassification(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	tests := []struct {
		name       string
		status     int
		header     http.Header
		code       ports.ProviderErrorCode
		retryable  bool
		retryAfter time.Duration
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, code: ports.ProviderErrorUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, code: ports.ProviderErrorRateLimited, retryable: true, retryAfter: 10 * time.Minute},
		{name: "rate limited", status: http.StatusTooManyRequests, header: http.Header{"Retry-After": []string{"2"}}, code: ports.ProviderErrorRateLimited, retryable: true, retryAfter: 2 * time.Second},
		{name: "bad request", status: http.StatusBadRequest, code: ports.ProviderErrorBadRequest},
		{name: "not found", status: http.StatusNotFound, code: ports.ProviderErrorBadRequest},
		{name: "method", status: http.StatusMethodNotAllowed, code: ports.ProviderErrorBadRequest},
		{name: "unavailable", status: http.StatusServiceUnavailable, code: ports.ProviderErrorTemporaryUnavailable, retryable: true},
		{name: "unexpected", status: http.StatusTeapot, code: ports.ProviderErrorUnknown, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
				for name, values := range test.header {
					for _, value := range values {
						writer.Header().Add(name, value)
					}
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte("technical upstream body"))
			})
			_, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
			classified, ok := ports.AsProviderError(err)
			if !ok || classified.Code != test.code || classified.Code.Retryable() != test.retryable {
				t.Fatalf("error = %#v, want %s retryable=%t", err, test.code, test.retryable)
			}
			if test.retryAfter > 0 && (classified.RetryAfter == nil || *classified.RetryAfter != test.retryAfter) {
				t.Fatalf("RetryAfter = %#v, want %s", classified.RetryAfter, test.retryAfter)
			}
			if strings.Contains(err.Error(), "technical upstream") {
				t.Fatalf("safe error leaked body: %v", err)
			}
		})
	}
}

func TestRetCodeClassification(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	tests := []struct {
		retCode int
		code    ports.ProviderErrorCode
	}{
		{10006, ports.ProviderErrorRateLimited},
		{429, ports.ProviderErrorRateLimited},
		{10000, ports.ProviderErrorTemporaryUnavailable},
		{10016, ports.ProviderErrorTemporaryUnavailable},
		{500000, ports.ProviderErrorTemporaryUnavailable},
		{170001, ports.ProviderErrorTemporaryUnavailable},
		{-2015, ports.ProviderErrorUnauthorized},
		{10003, ports.ProviderErrorUnauthorized},
		{10029, ports.ProviderErrorInvalidInstrument},
		{170121, ports.ProviderErrorInvalidInstrument},
		{181012, ports.ProviderErrorInvalidInstrument},
		{10001, ports.ProviderErrorBadRequest},
		{10002, ports.ProviderErrorBadRequest},
		{10017, ports.ProviderErrorBadRequest},
		{987654, ports.ProviderErrorUnknown},
	}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.retCode), func(t *testing.T) {
			adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("X-Bapi-Limit-Reset-Timestamp", "1784086203000")
				_, _ = writer.Write([]byte(`{"retCode":` + strconv.Itoa(test.retCode) + `,"retMsg":"secret key material","result":{},"time":1784086200000}`))
			})
			_, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
			classified, ok := ports.AsProviderError(err)
			if !ok || classified.Code != test.code {
				t.Fatalf("retCode %d error = %#v, want %s", test.retCode, err, test.code)
			}
			if strings.Contains(err.Error(), "secret key") {
				t.Fatalf("safe error leaked retMsg: %v", err)
			}
			if test.code == ports.ProviderErrorRateLimited && (classified.RetryAfter == nil || *classified.RetryAfter != 3*time.Second) {
				t.Fatalf("rate limit RetryAfter = %#v", classified.RetryAfter)
			}
		})
	}
}

func TestResponseEnvelopeValidation(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{`},
		{name: "trailing", body: `{"retCode":0,"result":{},"time":1784086200000}{}`},
		{name: "missing time", body: `{"retCode":0,"result":{"category":"spot","list":[]}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte(test.body)) })
			_, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
			classified, ok := ports.AsProviderError(err)
			if !ok || classified.Code != ports.ProviderErrorInvalidResponse {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestResponseSizeLimitAndReadFailure(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	adapter, _ := newHTTPTestAdapter(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", 20)))
	}, func(config *Config) {
		config.MaxResponseBytes = 10
	})
	_, err := adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
	assertProviderCode(t, err, ports.ProviderErrorInvalidResponse)

	readAdapter, err := New(Config{
		BaseURL: "https://example.com", Now: func() time.Time { return fixedBybitNow },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(failingReader{})}, nil
		})},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = readAdapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
	assertProviderCode(t, err, ports.ProviderErrorNetwork)
}

func TestTransportAndContextErrors(t *testing.T) {
	t.Parallel()

	reference := bybitReference(t, "BTCUSDT")
	transportCause := errors.New("dial failed with secret address")
	adapter, err := New(Config{
		BaseURL: "https://example.com", Now: func() time.Time { return fixedBybitNow },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, transportCause })},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = adapter.FetchLatestQuotes(context.Background(), []ports.ProviderInstrumentRef{reference})
	assertProviderCode(t, err, ports.ProviderErrorNetwork)
	if !errors.Is(err, transportCause) || strings.Contains(err.Error(), "secret address") {
		t.Fatalf("transport wrapping = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = adapter.FetchLatestQuotes(canceled, []ports.ProviderInstrumentRef{reference})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request error = %v", err)
	}
}

func TestRetryDelaySources(t *testing.T) {
	t.Parallel()

	now := fixedBybitNow
	if delay := retryDelay(http.Header{"Retry-After": []string{now.Add(4 * time.Second).Format(http.TimeFormat)}}, now, time.Second); delay != 4*time.Second {
		t.Fatalf("HTTP-date retry delay = %s", delay)
	}
	if delay := retryDelay(http.Header{"Retry-After": []string{"bad"}, "X-Bapi-Limit-Reset-Timestamp": []string{"bad"}}, now, 7*time.Second); delay != 7*time.Second {
		t.Fatalf("fallback retry delay = %s", delay)
	}
	if got := truncateTechnicalBody([]byte(strings.Repeat("x", 300))); len(got) != 256 {
		t.Fatalf("truncated body length = %d", len(got))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func assertProviderCode(t *testing.T, err error, code ports.ProviderErrorCode) {
	t.Helper()
	classified, ok := ports.AsProviderError(err)
	if !ok || classified.Code != code {
		t.Fatalf("error = %#v, want %s", err, code)
	}
}
