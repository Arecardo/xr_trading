package coingecko

import (
	"fmt"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

func providerError(providerCode domain.Code, code ports.ProviderErrorCode, message string, retryAfter *time.Duration, cause error) error {
	classified, err := ports.NewProviderError(providerCode, code, message, retryAfter, cause)
	if err != nil {
		return fmt.Errorf("construct coingecko provider error: %w", err)
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
