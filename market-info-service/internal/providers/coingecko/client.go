package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xr-trading/market-info-service/internal/ingestion/ports"
)

const (
	simplePricePath          = "/api/v3/simple/price"
	marketChartRangePathTmpl = "/api/v3/coins/%s/market_chart/range"
	// defaultRateLimitDelay is used when CoinGecko returns 429 without a
	// usable Retry-After header. The free tier's documented budget is on the
	// order of 10-30 calls/minute, so a one-minute backoff is a conservative
	// default rather than a measured server-communicated value.
	defaultRateLimitDelay = time.Minute
)

// doGet performs one CoinGecko GET request and returns the raw, size-bounded
// response body. Callers decode the body themselves because simple/price and
// market_chart/range have unrelated JSON shapes; there is no shared envelope
// like Bybit's retCode wrapper to decode generically here.
func (adapter *Adapter) doGet(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if ctx == nil {
		return nil, badRequestError(adapter.providerCode, "market request context is required", nil)
	}
	requestURL := *adapter.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + path
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, badRequestError(adapter.providerCode, "market request could not be constructed", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", adapter.userAgent)

	response, err := adapter.httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, providerError(adapter.providerCode, ports.ProviderErrorNetwork, "provider network request failed", nil, err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, adapter.maxResponseBytes+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, providerError(adapter.providerCode, ports.ProviderErrorNetwork, "provider response could not be read", nil, readErr)
	}
	if int64(len(body)) > adapter.maxResponseBytes {
		return nil, invalidResponseError(adapter.providerCode, "provider response exceeded size limit", nil)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, classifyHTTPError(adapter, response, body)
	}
	if !json.Valid(body) {
		return nil, invalidResponseError(adapter.providerCode, "provider returned malformed JSON", nil)
	}
	return body, nil
}

func classifyHTTPError(adapter *Adapter, response *http.Response, body []byte) error {
	cause := fmt.Errorf("coingecko HTTP status %d: %s", response.StatusCode, truncateTechnicalBody(body))
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return providerError(adapter.providerCode, ports.ProviderErrorUnauthorized, "provider rejected authorization", nil, cause)
	case http.StatusTooManyRequests:
		// This is the rate-limit path this task exists to handle safely: the
		// caller must retry/back off rather than silently treat a throttled
		// day as if it had no data to collect. See ingestion/retry.go, which
		// reads RetryAfter off this classified error to schedule the retry.
		delay := retryDelay(response.Header, adapter.now(), defaultRateLimitDelay)
		return providerError(adapter.providerCode, ports.ProviderErrorRateLimited, "provider rate limit reached", &delay, cause)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed:
		return badRequestError(adapter.providerCode, "provider rejected market request", cause)
	default:
		if response.StatusCode >= http.StatusInternalServerError {
			return providerError(adapter.providerCode, ports.ProviderErrorTemporaryUnavailable, "provider service is unavailable", nil, cause)
		}
		return providerError(adapter.providerCode, ports.ProviderErrorUnknown, "provider returned an unexpected HTTP status", nil, cause)
	}
}

func retryDelay(header http.Header, now time.Time, fallback time.Duration) time.Duration {
	if value := strings.TrimSpace(header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		if instant, err := http.ParseTime(value); err == nil && instant.After(now) {
			return instant.Sub(now)
		}
	}
	return fallback
}

func truncateTechnicalBody(body []byte) string {
	const maximum = 256
	text := strings.TrimSpace(string(body))
	if len(text) > maximum {
		return text[:maximum]
	}
	return text
}
