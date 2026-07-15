package bybit

import (
	"fmt"
	"regexp"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

var spotSymbolPattern = regexp.MustCompile(`^[A-Z0-9]+$`)

func (adapter *Adapter) validateSpotReference(reference ports.ProviderInstrumentRef) error {
	if err := reference.Validate(); err != nil {
		return badRequestError(adapter.providerCode, "provider instrument reference is invalid", err)
	}
	if reference.ProviderCode != adapter.providerCode || reference.ProviderMarket != spotMarket ||
		reference.AssetType != domain.AssetTypeCrypto || reference.InstrumentType != domain.InstrumentTypeSpot ||
		!spotSymbolPattern.MatchString(reference.ExternalSymbol) {
		return invalidInstrumentError(adapter.providerCode, "provider instrument is not a Bybit Spot mapping", nil)
	}
	return nil
}

func bybitInterval(interval domain.BarInterval) (string, time.Duration, error) {
	switch interval {
	case domain.BarInterval1Hour:
		return "60", time.Hour, nil
	case domain.BarInterval1Day:
		return "D", 24 * time.Hour, nil
	default:
		return "", 0, fmt.Errorf("unsupported Bybit interval %q", interval)
	}
}

func parseRequiredDecimal(value string) (domain.Decimal, error) {
	if value == "" {
		return domain.Decimal{}, fmt.Errorf("required decimal is empty")
	}
	return domain.ParseDecimal(value)
}

func parseOptionalDecimal(value string) (*domain.Decimal, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := domain.ParseDecimal(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func instantFromMilliseconds(milliseconds int64) (domain.UTCInstant, error) {
	if milliseconds <= 0 {
		return domain.UTCInstant{}, fmt.Errorf("timestamp milliseconds must be positive")
	}
	return domain.NewUTCInstant(time.UnixMilli(milliseconds))
}
