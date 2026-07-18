package longbridge

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	lbquote "github.com/longbridge/openapi-go/quote"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
	"xr-trading/market-info-service/internal/markettime"
)

var usSymbolPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,123}\.US$`)

func (adapter *Adapter) validateUSReference(reference ports.ProviderInstrumentRef) error {
	if err := reference.Validate(); err != nil {
		return badRequestError(adapter.providerCode, "provider instrument reference is invalid", err)
	}
	compatibleType := (reference.AssetType == domain.AssetTypeStock && reference.InstrumentType == domain.InstrumentTypeEquity) ||
		(reference.AssetType == domain.AssetTypeETF && reference.InstrumentType == domain.InstrumentTypeETF)
	if reference.ProviderCode != adapter.providerCode || reference.ProviderMarket != usMarket || !compatibleType ||
		!usSymbolPattern.MatchString(reference.ExternalSymbol) || strings.Contains(reference.ExternalSymbol, "..") {
		return invalidInstrumentError(adapter.providerCode, "provider instrument is not a Longbridge US stock or ETF mapping", nil)
	}
	return nil
}

func longbridgePeriod(interval domain.BarInterval) (lbquote.Period, error) {
	switch interval {
	case domain.BarInterval1Hour:
		return lbquote.PeriodSixtyMinute, nil
	case domain.BarInterval1Day:
		return lbquote.PeriodDay, nil
	default:
		return 0, fmt.Errorf("unsupported Longbridge interval %q", interval)
	}
}

func requiredDecimal(value *decimal.Decimal) (domain.Decimal, error) {
	if value == nil {
		return domain.Decimal{}, fmt.Errorf("required decimal is missing")
	}
	return domain.DecimalFromExact(*value), nil
}

func optionalDecimal(value *decimal.Decimal) *domain.Decimal {
	if value == nil {
		return nil
	}
	parsed := domain.DecimalFromExact(*value)
	return &parsed
}

func volumeDecimal(value int64) (*domain.Decimal, error) {
	if value < 0 {
		return nil, fmt.Errorf("volume cannot be negative")
	}
	parsed, err := domain.ParseDecimal(strconv.FormatInt(value, 10))
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func instantFromSeconds(seconds int64) (domain.UTCInstant, error) {
	if seconds <= 0 {
		return domain.UTCInstant{}, fmt.Errorf("timestamp seconds must be positive")
	}
	return domain.NewUTCInstant(time.Unix(seconds, 0))
}

func (adapter *Adapter) regularSessionClose(open time.Time, interval domain.BarInterval) (domain.UTCInstant, error) {
	if adapter.marketCalendar == nil {
		return domain.UTCInstant{}, fmt.Errorf("US market calendar is required")
	}
	var date markettime.MarketDate
	var err error
	if interval == domain.BarInterval1Day {
		date, err = markettime.UTCDate(open)
	} else {
		date, err = markettime.DateInLocation(open, adapter.marketCalendar.Location())
	}
	if err != nil {
		return domain.UTCInstant{}, err
	}
	session, exists, err := adapter.marketCalendar.SessionForDate(date)
	if err != nil {
		return domain.UTCInstant{}, err
	}
	if !exists {
		return domain.UTCInstant{}, fmt.Errorf("bar belongs to a closed US market date")
	}
	closeAt := session.Close
	if interval == domain.BarInterval1Hour {
		open = open.UTC()
		if open.Before(session.Open) || !open.Before(session.Close) || open.Sub(session.Open)%time.Hour != 0 {
			return domain.UTCInstant{}, fmt.Errorf("hourly bar open is outside the regular US session")
		}
		closeAt = open.Add(time.Hour)
		if closeAt.After(session.Close) {
			closeAt = session.Close
		}
	} else if interval != domain.BarInterval1Day {
		return domain.UTCInstant{}, fmt.Errorf("unsupported regular-session interval %q", interval)
	}
	if !closeAt.After(open) {
		return domain.UTCInstant{}, fmt.Errorf("derived regular-session close does not follow open")
	}
	return domain.NewUTCInstant(closeAt)
}

func (adapter *Adapter) sdkOffset(effectiveEnd time.Time, interval domain.BarInterval) time.Time {
	offset := effectiveEnd.Add(-time.Nanosecond)
	if interval == domain.BarInterval1Hour {
		return offset.In(adapter.marketLocation)
	}
	return offset.UTC()
}
