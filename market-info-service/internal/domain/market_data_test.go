package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMarketDataSourceRequiresBothIdentities(t *testing.T) {
	instrumentID := testDomainID("019f1452-90f7-7992-a87a-ca2727891601")
	mappingID := testDomainID("019f1452-90f7-7992-a87a-ca2727891602")
	source, err := NewMarketDataSource(instrumentID, mappingID)
	if err != nil {
		t.Fatalf("NewMarketDataSource() error = %v", err)
	}
	if source.InstrumentID != instrumentID || source.ProviderInstrumentID != mappingID {
		t.Fatalf("NewMarketDataSource() = %#v", source)
	}
	if _, err := NewMarketDataSource(ID{}, mappingID); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewMarketDataSource(zero instrument) error = %v", err)
	}

	now := time.Now().UTC()
	mapping := validProviderInstrument(t, testDomainID("019f1452-90f7-7992-a87a-ca2727891603"), instrumentID, now)
	mapping.ID = mappingID
	if err := source.ValidateProviderInstrument(mapping); err != nil {
		t.Fatalf("ValidateProviderInstrument() error = %v", err)
	}
	source.ProviderInstrumentID = testDomainID("019f1452-90f7-7992-a87a-ca2727891604")
	if err := source.ValidateProviderInstrument(mapping); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("ValidateProviderInstrument(mismatch) error = %v", err)
	}
}

func TestTimeRangeUsesInclusiveStartExclusiveEnd(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	start := time.Date(2026, time.July, 14, 8, 0, 0, 0, location)
	end := start.Add(time.Hour)
	rangeValue, err := NewBoundedTimeRange(start, end)
	if err != nil {
		t.Fatalf("NewBoundedTimeRange() error = %v", err)
	}
	if !rangeValue.IsBounded() || rangeValue.Start.Time().Location() != time.UTC {
		t.Fatalf("NewBoundedTimeRange() = %#v", rangeValue)
	}
	if !rangeValue.Contains(marketDataInstant(t, start)) || !rangeValue.Contains(marketDataInstant(t, end.Add(-time.Nanosecond))) {
		t.Fatal("range does not contain its inclusive start or interior")
	}
	if rangeValue.Contains(marketDataInstant(t, end)) {
		t.Fatal("range contains exclusive end")
	}
	openEnded, err := NewTimeRange(&start, nil)
	if err != nil || !openEnded.Contains(marketDataInstant(t, end.Add(24*time.Hour))) {
		t.Fatalf("NewTimeRange(open ended) = (%#v, %v)", openEnded, err)
	}
	if _, err := NewBoundedTimeRange(end, start); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewBoundedTimeRange(reverse) error = %v", err)
	}
	zero := time.Time{}
	if _, err := NewTimeRange(&zero, nil); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewTimeRange(zero) error = %v", err)
	}
	invalid := TimeRange{Start: &UTCInstant{}}
	if invalid.Contains(marketDataInstant(t, start)) {
		t.Fatal("invalid range contains instant")
	}
}

func TestQuoteValidationAndNormalization(t *testing.T) {
	now := time.Now().UTC()
	quote := validQuote(t, now)
	quote.Metadata = nil
	normalized, err := NewQuote(quote)
	if err != nil {
		t.Fatalf("NewQuote() error = %v", err)
	}
	if string(normalized.Metadata) != "{}" || normalized.Source().ProviderInstrumentID != quote.ProviderInstrumentID {
		t.Fatalf("NewQuote() = %#v", normalized)
	}

	negative := marketDataDecimal(t, "-1")
	tooHigh := marketDataDecimal(t, "102")
	tooLow := marketDataDecimal(t, "89")
	tests := []struct {
		name   string
		mutate func(*Quote)
	}{
		{"zero source", func(value *Quote) { value.ProviderInstrumentID = ID{} }},
		{"zero market time", func(value *Quote) { value.MarketTime = UTCInstant{} }},
		{"negative last", func(value *Quote) { value.LastPrice = negative }},
		{"negative size", func(value *Quote) { value.BidSize = &negative }},
		{"crossed book", func(value *Quote) { value.BidPrice = &tooHigh; value.AskPrice = &tooLow }},
		{"high below low", func(value *Quote) { value.High24H = &tooLow; value.Low24H = &tooHigh }},
		{"open above high", func(value *Quote) { value.Open24H = &tooHigh; value.High24H = &tooLow }},
		{"open below low", func(value *Quote) { value.Open24H = &tooLow; value.Low24H = &tooHigh }},
		{"invalid quality", func(value *Quote) { value.QualityStatus = "trusted" }},
		{"invalid metadata", func(value *Quote) { value.Metadata = json.RawMessage(`[]`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validQuote(t, now)
			test.mutate(&value)
			if _, err := NewQuote(value); !errors.Is(err, ErrInvalidData) {
				t.Fatalf("NewQuote() error = %v, want ErrInvalidData", err)
			}
		})
	}
}

func TestBarValidationCoversOHLCVAndClosure(t *testing.T) {
	openAt := time.Now().UTC().Truncate(time.Hour)
	bar := validBar(t, openAt)
	if _, err := NewBar(bar); err != nil {
		t.Fatalf("NewBar() error = %v", err)
	}
	bar.Revision = 1
	if _, err := NewStoredBar(bar); err != nil {
		t.Fatalf("NewStoredBar() error = %v", err)
	}
	bar.Revision = 0
	if _, err := NewStoredBar(bar); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("NewStoredBar(zero revision) error = %v", err)
	}

	negative := marketDataDecimal(t, "-1")
	belowLow := marketDataDecimal(t, "89")
	aboveHigh := marketDataDecimal(t, "111")
	negativeCount := int64(-1)
	zeroInstant := UTCInstant{}
	tests := []struct {
		name   string
		mutate func(*Bar)
	}{
		{"zero source", func(value *Bar) { value.InstrumentID = ID{} }},
		{"unsupported interval", func(value *Bar) { value.Interval = "5m" }},
		{"zero time", func(value *Bar) { value.OpenTime = UTCInstant{} }},
		{"reversed time", func(value *Bar) { value.CloseTime = value.OpenTime }},
		{"negative revision", func(value *Bar) { value.Revision = -1 }},
		{"negative price", func(value *Bar) { value.OpenPrice = negative }},
		{"open below low", func(value *Bar) { value.OpenPrice = belowLow }},
		{"close above high", func(value *Bar) { value.ClosePrice = aboveHigh }},
		{"high below low", func(value *Bar) { value.HighPrice = belowLow }},
		{"negative volume", func(value *Bar) { value.BaseVolume = &negative }},
		{"negative trade count", func(value *Bar) { value.TradeCount = &negativeCount }},
		{"closed too early", func(value *Bar) { value.CollectedAt = value.OpenTime }},
		{"zero provider update", func(value *Bar) { value.ProviderUpdatedAt = &zeroInstant }},
		{"invalid quality", func(value *Bar) { value.QualityStatus = "trusted" }},
		{"long raw hash", func(value *Bar) { value.RawHash = strings.Repeat("a", 65) }},
		{"invalid metadata", func(value *Bar) { value.Metadata = json.RawMessage(`null`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validBar(t, openAt)
			test.mutate(&value)
			if _, err := NewBar(value); !errors.Is(err, ErrInvalidData) {
				t.Fatalf("NewBar() error = %v, want ErrInvalidData", err)
			}
		})
	}

	openBar := validBar(t, openAt)
	openBar.IsClosed = false
	openBar.CollectedAt = marketDataInstant(t, openAt.Add(30*time.Minute))
	if _, err := NewBar(openBar); err != nil {
		t.Fatalf("NewBar(open) error = %v", err)
	}
}

func TestMarketBarFilterAndQualityStatusJSON(t *testing.T) {
	openAt := time.Now().UTC().Truncate(time.Hour)
	bar := validBar(t, openAt)
	rangeValue, _ := NewBoundedTimeRange(openAt, openAt.Add(2*time.Hour))
	filter := MarketBarFilter{InstrumentID: bar.InstrumentID, ProviderInstrumentID: bar.ProviderInstrumentID, Interval: BarInterval1Hour, Range: rangeValue}
	if err := filter.Validate(); err != nil {
		t.Fatalf("MarketBarFilter.Validate() error = %v", err)
	}
	filter.Interval = "5m"
	if err := filter.Validate(); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("MarketBarFilter.Validate(invalid interval) error = %v", err)
	}

	encoded, err := json.Marshal(QualityStatusWarning)
	if err != nil || string(encoded) != `"warning"` {
		t.Fatalf("json.Marshal(QualityStatusWarning) = (%s, %v)", encoded, err)
	}
	var decoded QualityStatus
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != QualityStatusWarning {
		t.Fatalf("json.Unmarshal() = (%q, %v)", decoded, err)
	}
	if err := json.Unmarshal([]byte(`"trusted"`), &decoded); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("json.Unmarshal(invalid quality) error = %v", err)
	}
}

func validQuote(t *testing.T, now time.Time) Quote {
	t.Helper()
	last := marketDataDecimal(t, "100")
	bid := marketDataDecimal(t, "99")
	ask := marketDataDecimal(t, "101")
	open := marketDataDecimal(t, "95")
	high := marketDataDecimal(t, "110")
	low := marketDataDecimal(t, "90")
	volume := marketDataDecimal(t, "1000")
	instant := marketDataInstant(t, now)
	return Quote{
		InstrumentID:         testDomainID("019f1452-90f7-7992-a87a-ca2727891601"),
		ProviderInstrumentID: testDomainID("019f1452-90f7-7992-a87a-ca2727891602"),
		MarketTime:           instant,
		LastPrice:            last,
		BidPrice:             &bid,
		AskPrice:             &ask,
		Open24H:              &open,
		High24H:              &high,
		Low24H:               &low,
		BaseVolume24H:        &volume,
		QualityStatus:        QualityStatusValid,
		CollectedAt:          instant,
		Metadata:             json.RawMessage(`{}`),
	}
}

func validBar(t *testing.T, openAt time.Time) Bar {
	t.Helper()
	closeAt := openAt.Add(time.Hour)
	baseVolume := marketDataDecimal(t, "1000")
	quoteVolume := marketDataDecimal(t, "100000")
	tradeCount := int64(42)
	providerUpdatedAt := marketDataInstant(t, closeAt)
	return Bar{
		InstrumentID:         testDomainID("019f1452-90f7-7992-a87a-ca2727891601"),
		ProviderInstrumentID: testDomainID("019f1452-90f7-7992-a87a-ca2727891602"),
		Interval:             BarInterval1Hour,
		OpenTime:             marketDataInstant(t, openAt),
		CloseTime:            marketDataInstant(t, closeAt),
		OpenPrice:            marketDataDecimal(t, "100"),
		HighPrice:            marketDataDecimal(t, "110"),
		LowPrice:             marketDataDecimal(t, "90"),
		ClosePrice:           marketDataDecimal(t, "105"),
		BaseVolume:           &baseVolume,
		QuoteVolume:          &quoteVolume,
		TradeCount:           &tradeCount,
		IsClosed:             true,
		QualityStatus:        QualityStatusValid,
		ProviderUpdatedAt:    &providerUpdatedAt,
		CollectedAt:          marketDataInstant(t, closeAt),
		RawHash:              "sha256",
		Metadata:             json.RawMessage(`{}`),
	}
}

func marketDataInstant(t *testing.T, value time.Time) UTCInstant {
	t.Helper()
	instant, err := NewUTCInstant(value)
	if err != nil {
		t.Fatalf("NewUTCInstant() error = %v", err)
	}
	return instant
}

func marketDataDecimal(t *testing.T, value string) Decimal {
	t.Helper()
	parsed, err := ParseDecimal(value)
	if err != nil {
		t.Fatalf("ParseDecimal(%q) error = %v", value, err)
	}
	return parsed
}
