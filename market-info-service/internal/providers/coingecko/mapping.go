package coingecko

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

// coinGeckoIDPattern matches CoinGecko's lowercase kebab-case coin ids, e.g.
// "tether", "usd-coin". It reuses ProviderInstrumentRef.ExternalSymbol so no
// new adapter-specific field is needed on the shared port.
var coinGeckoIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// currencyPattern matches the shared QuoteCurrency field's stored form (e.g.
// "USD"); the adapter lowercases it before sending it as CoinGecko's
// vs_currency query parameter.
var currencyPattern = regexp.MustCompile(`^[A-Z]{2,10}$`)

func (adapter *Adapter) validateFXReference(reference ports.ProviderInstrumentRef) error {
	if err := reference.Validate(); err != nil {
		return badRequestError(adapter.providerCode, "provider instrument reference is invalid", err)
	}
	if reference.ProviderCode != adapter.providerCode || reference.ProviderMarket != fxMarket ||
		reference.AssetType != domain.AssetTypeFX || reference.InstrumentType != domain.InstrumentTypeFX ||
		!coinGeckoIDPattern.MatchString(reference.ExternalSymbol) || !currencyPattern.MatchString(reference.QuoteCurrency) {
		return invalidInstrumentError(adapter.providerCode, "provider instrument is not a CoinGecko FX mapping", nil)
	}
	return nil
}

func parseDecimalFromNumber(value json.Number) (domain.Decimal, error) {
	if value == "" {
		return domain.Decimal{}, fmt.Errorf("required decimal is empty")
	}
	// value.String() returns exactly the literal digits CoinGecko sent on the
	// wire; parsing that text (rather than routing through float64) avoids
	// adding our own binary-floating-point rounding on top of whatever
	// precision CoinGecko's JSON response already has.
	return domain.ParseDecimal(value.String())
}

func instantFromMilliseconds(milliseconds int64) (domain.UTCInstant, error) {
	if milliseconds <= 0 {
		return domain.UTCInstant{}, fmt.Errorf("timestamp milliseconds must be positive")
	}
	return domain.NewUTCInstant(time.UnixMilli(milliseconds))
}

func instantFromSeconds(seconds int64) (domain.UTCInstant, error) {
	if seconds <= 0 {
		return domain.UTCInstant{}, fmt.Errorf("timestamp seconds must be positive")
	}
	return domain.NewUTCInstant(time.Unix(seconds, 0))
}

// civilDay is a UTC calendar day used to collapse CoinGecko's raw price
// points (daily, or intraday for very recent ranges) into exactly one daily
// bar per day, per the DEC-006 single-price-as-OHLC contract.
type civilDay struct {
	year  int
	month time.Month
	day   int
}

func civilDayOf(instant time.Time) civilDay {
	instant = instant.UTC()
	year, month, day := instant.Date()
	return civilDay{year: year, month: month, day: day}
}

func (value civilDay) midnightUTC() time.Time {
	return time.Date(value.year, value.month, value.day, 0, 0, 0, 0, time.UTC)
}

func (value civilDay) before(other civilDay) bool {
	return value.midnightUTC().Before(other.midnightUTC())
}
