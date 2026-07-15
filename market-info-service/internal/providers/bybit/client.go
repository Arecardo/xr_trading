package bybit

import (
	"bytes"
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
	tickersPath = "/v5/market/tickers"
	klinePath   = "/v5/market/kline"
)

type responseEnvelope[T any] struct {
	RetCode int             `json:"retCode"`
	RetMsg  string          `json:"retMsg"`
	Result  T               `json:"result"`
	Time    int64           `json:"time"`
	ExtInfo json.RawMessage `json:"retExtInfo"`
}

func getJSON[T any](ctx context.Context, adapter *Adapter, path string, query url.Values) (responseEnvelope[T], error) {
	var envelope responseEnvelope[T]
	if ctx == nil {
		return envelope, badRequestError(adapter.providerCode, "market request context is required", nil)
	}
	requestURL := *adapter.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + path
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return envelope, badRequestError(adapter.providerCode, "market request could not be constructed", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", adapter.userAgent)

	response, err := adapter.httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return envelope, ctxErr
		}
		return envelope, providerError(adapter.providerCode, ports.ProviderErrorNetwork, "provider network request failed", nil, err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, adapter.maxResponseBytes+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return envelope, providerError(adapter.providerCode, ports.ProviderErrorNetwork, "provider response could not be read", nil, readErr)
	}
	if int64(len(body)) > adapter.maxResponseBytes {
		return envelope, invalidResponseError(adapter.providerCode, "provider response exceeded size limit", nil)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return envelope, classifyHTTPError(adapter, response, body)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, invalidResponseError(adapter.providerCode, "provider returned malformed JSON", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return envelope, invalidResponseError(adapter.providerCode, "provider returned trailing JSON", nil)
	}
	if envelope.RetCode != 0 {
		return envelope, classifyRetCode(adapter, response.Header, envelope.RetCode, envelope.RetMsg)
	}
	if envelope.Time <= 0 {
		return envelope, invalidResponseError(adapter.providerCode, "provider response time is missing", nil)
	}
	return envelope, nil
}

func classifyHTTPError(adapter *Adapter, response *http.Response, body []byte) error {
	cause := fmt.Errorf("bybit HTTP status %d: %s", response.StatusCode, truncateTechnicalBody(body))
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return providerError(adapter.providerCode, ports.ProviderErrorUnauthorized, "provider rejected authorization", nil, cause)
	case http.StatusForbidden:
		delay := retryDelay(response.Header, adapter.now(), 10*time.Minute)
		return providerError(adapter.providerCode, ports.ProviderErrorRateLimited, "provider access is temporarily forbidden", &delay, cause)
	case http.StatusTooManyRequests:
		delay := retryDelay(response.Header, adapter.now(), time.Second)
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

func classifyRetCode(adapter *Adapter, header http.Header, retCode int, retMessage string) error {
	cause := fmt.Errorf("bybit retCode %d: %s", retCode, retMessage)
	switch retCode {
	case 10006, 429:
		delay := retryDelay(header, adapter.now(), time.Second)
		return providerError(adapter.providerCode, ports.ProviderErrorRateLimited, "provider rate limit reached", &delay, cause)
	case 10000, 10016, 500000, 170001, 170007, 170032:
		return providerError(adapter.providerCode, ports.ProviderErrorTemporaryUnavailable, "provider service is temporarily unavailable", nil, cause)
	case -2015, 33004, 10003, 10004, 10005, 10007, 10008, 10009, 10010, 10024, 10027:
		return providerError(adapter.providerCode, ports.ProviderErrorUnauthorized, "provider authorization or access was rejected", nil, cause)
	case 10029, 170121, 181012:
		return providerError(adapter.providerCode, ports.ProviderErrorInvalidInstrument, "provider instrument is invalid", nil, cause)
	case 10001, 10002, 10017:
		return badRequestError(adapter.providerCode, "provider rejected market request parameters", cause)
	default:
		return providerError(adapter.providerCode, ports.ProviderErrorUnknown, "provider returned an unclassified error", nil, cause)
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
	if value := strings.TrimSpace(header.Get("X-Bapi-Limit-Reset-Timestamp")); value != "" {
		if milliseconds, err := strconv.ParseInt(value, 10, 64); err == nil {
			reset := time.UnixMilli(milliseconds)
			if reset.After(now) {
				return reset.Sub(now)
			}
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
