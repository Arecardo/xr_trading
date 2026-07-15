package longbridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	protocol "github.com/longbridge/openapi-protocol/go"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

type operation uint8

const (
	quoteOperation operation = iota + 1
	barOperation
)

func providerError(providerCode domain.Code, code ports.ProviderErrorCode, message string, retryAfter *time.Duration, cause error) error {
	classified, err := ports.NewProviderError(providerCode, code, message, retryAfter, cause)
	if err != nil {
		return fmt.Errorf("construct Longbridge provider error: %w", err)
	}
	return classified
}

func badRequestError(providerCode domain.Code, message string, cause error) error {
	return providerError(providerCode, ports.ProviderErrorBadRequest, message, nil, cause)
}

func invalidInstrumentError(providerCode domain.Code, message string, cause error) error {
	return providerError(providerCode, ports.ProviderErrorInvalidInstrument, message, nil, cause)
}

func unsupportedIntervalError(providerCode domain.Code, cause error) error {
	return providerError(providerCode, ports.ProviderErrorUnsupportedInterval, "provider interval is unsupported", nil, cause)
}

func invalidResponseError(providerCode domain.Code, message string, cause error) error {
	return providerError(providerCode, ports.ProviderErrorInvalidResponse, message, nil, cause)
}

func classifyClientError(providerCode domain.Code, op operation, ctx context.Context, cause error) error {
	if cause == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	var protocolError *protocol.LBError
	if errors.As(cause, &protocolError) {
		switch protocolError.Code {
		case 301606:
			delay := time.Second
			return providerError(providerCode, ports.ProviderErrorRateLimited, "provider rate limit reached", &delay, cause)
		case 301602:
			return providerError(providerCode, ports.ProviderErrorTemporaryUnavailable, "provider service is temporarily unavailable", nil, cause)
		case 301604, 301607:
			if protocolError.Code == 301607 && op == quoteOperation {
				return badRequestError(providerCode, "provider rejected quote request size", cause)
			}
			return providerError(providerCode, ports.ProviderErrorUnauthorized, "provider quote permission was rejected", nil, cause)
		case 301603:
			return invalidInstrumentError(providerCode, "provider has no quote for instrument", cause)
		case 301600:
			return badRequestError(providerCode, "provider rejected market request parameters", cause)
		default:
			return providerError(providerCode, ports.ProviderErrorUnknown, "provider returned an unclassified protocol error", nil, cause)
		}
	}
	var networkError net.Error
	if errors.As(cause, &networkError) {
		return providerError(providerCode, ports.ProviderErrorNetwork, "provider network request failed", nil, cause)
	}
	return providerError(providerCode, ports.ProviderErrorUnknown, "provider client returned an unclassified error", nil, cause)
}
