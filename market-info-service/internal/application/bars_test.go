package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
)

type stubBarCatalog struct {
	instrument    domain.Instrument
	provider      domain.Provider
	instrumentErr error
	providerErr   error
}

func (stub *stubBarCatalog) FindInstrumentByCode(context.Context, string) (domain.Instrument, error) {
	return stub.instrument, stub.instrumentErr
}
func (stub *stubBarCatalog) FindProviderByCode(context.Context, string) (domain.Provider, error) {
	return stub.provider, stub.providerErr
}

type stubBarReader struct {
	source       BarSourceRecord
	sourceErr    error
	bars         []domain.MarketBar
	barsErr      error
	sourceFilter BarSourceFilter
	barFilter    BarReadFilter
	resolveCalls int
	listCalls    int
}

func (stub *stubBarReader) ResolveBarSource(_ context.Context, filter BarSourceFilter) (BarSourceRecord, error) {
	stub.resolveCalls++
	stub.sourceFilter = filter
	return stub.source, stub.sourceErr
}
func (stub *stubBarReader) ListBars(_ context.Context, filter BarReadFilter) ([]domain.MarketBar, error) {
	stub.listCalls++
	stub.barFilter = filter
	return append([]domain.MarketBar(nil), stub.bars...), stub.barsErr
}

func TestNewBarsServiceRequiresDependencies(t *testing.T) {
	catalog := &stubBarCatalog{}
	reader := &stubBarReader{}
	for _, test := range []struct {
		catalog BarCatalog
		reader  BarReader
		now     func() time.Time
	}{{nil, reader, time.Now}, {catalog, nil, time.Now}, {catalog, reader, nil}} {
		if _, err := NewBarsService(test.catalog, test.reader, test.now); err == nil {
			t.Fatal("NewBarsService() error = nil")
		}
	}
}

func TestBarsServiceReturnsStableDescendingPage(t *testing.T) {
	now := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	instrument, provider, source := testBarSourceFixtures(t)
	start := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)
	reader := &stubBarReader{source: source, bars: []domain.MarketBar{
		testApplicationBar(t, instrument.ID, source.ProviderInstrumentID, start.Add(2*time.Hour)),
		testApplicationBar(t, instrument.ID, source.ProviderInstrumentID, start.Add(time.Hour)),
		testApplicationBar(t, instrument.ID, source.ProviderInstrumentID, start),
	}}
	service, _ := NewBarsService(&stubBarCatalog{instrument: instrument, provider: provider}, reader, func() time.Time { return now })
	result, err := service.List(context.Background(), BarsInput{
		InstrumentCode: instrument.Code.String(), ProviderCode: provider.Code.String(), Interval: "1h",
		StartTime: &start, EndTime: &end, Order: BarOrderDescending, Limit: 2,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(result.Bars) != 2 || result.NextCursorOpenTime == nil || result.NextCursorOpenTime.Time() != start.Add(time.Hour) || result.Source.ProviderInstrumentID != source.ProviderInstrumentID {
		t.Fatalf("List() = %#v", result)
	}
	if reader.barFilter.Limit != 3 || reader.barFilter.Order != BarOrderDescending || reader.sourceFilter.InstrumentID != instrument.ID || !reader.sourceFilter.EffectiveAt.Equal(now.UTC()) {
		t.Fatalf("filters = source %#v bars %#v", reader.sourceFilter, reader.barFilter)
	}
}

func TestBarsServiceSupportsAscendingCursor(t *testing.T) {
	instrument, provider, source := testBarSourceFixtures(t)
	start := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)
	cursor := start
	reader := &stubBarReader{source: source, bars: []domain.MarketBar{
		testApplicationBar(t, instrument.ID, source.ProviderInstrumentID, start.Add(time.Hour)),
		testApplicationBar(t, instrument.ID, source.ProviderInstrumentID, start.Add(2*time.Hour)),
	}}
	service, _ := NewBarsService(&stubBarCatalog{instrument: instrument, provider: provider}, reader, time.Now)
	result, err := service.List(context.Background(), BarsInput{InstrumentCode: instrument.Code.String(), ProviderCode: provider.Code.String(), Interval: "1h", StartTime: &start, EndTime: &end, Order: BarOrderAscending, CursorOpenTime: &cursor, Limit: 10})
	if err != nil || len(result.Bars) != 2 || reader.barFilter.CursorOpenTime == nil || reader.barFilter.Order != BarOrderAscending || result.NextCursorOpenTime != nil {
		t.Fatalf("List() = (%#v, %v), filter=%#v", result, err, reader.barFilter)
	}
}

func TestBarsServiceValidatesInputBeforeDependencies(t *testing.T) {
	now := time.Now().UTC()
	after := now.Add(time.Hour)
	tests := []BarsInput{
		{},
		{InstrumentCode: "asset.crypto.btc", ProviderCode: "bybit", Interval: "1h", Limit: 10},
		{InstrumentCode: "instrument.crypto.btc-usdt", ProviderCode: "Bad Provider", Interval: "1h", Limit: 10},
		{InstrumentCode: "instrument.crypto.btc-usdt", ProviderCode: "bybit", Interval: "5m", Limit: 10},
		{InstrumentCode: "instrument.crypto.btc-usdt", ProviderCode: "bybit", Interval: "1h", Order: "sideways", Limit: 10},
		{InstrumentCode: "instrument.crypto.btc-usdt", ProviderCode: "bybit", Interval: "1h", Limit: MaximumBarsPageSize + 1},
		{InstrumentCode: "instrument.crypto.btc-usdt", ProviderCode: "bybit", Interval: "1h", StartTime: &after, EndTime: &now, Limit: 10},
		{InstrumentCode: "instrument.crypto.btc-usdt", ProviderCode: "bybit", Interval: "1h", StartTime: &now, EndTime: &after, CursorOpenTime: &after, Limit: 10},
	}
	for _, input := range tests {
		reader := &stubBarReader{}
		service, _ := NewBarsService(&stubBarCatalog{}, reader, time.Now)
		_, err := service.List(context.Background(), input)
		var appError *Error
		if !errors.As(err, &appError) || (appError.Code != ErrorCodeInvalidArgument && appError.Code != ErrorCodeInvalidTimeRange) || reader.resolveCalls != 0 {
			t.Fatalf("List(%#v) error=%v resolve calls=%d", input, err, reader.resolveCalls)
		}
	}
}

func TestBarsServiceMapsCatalogSourceAndIntervalErrors(t *testing.T) {
	instrument, provider, source := testBarSourceFixtures(t)
	input := BarsInput{InstrumentCode: instrument.Code.String(), ProviderCode: provider.Code.String(), Interval: "1h", Limit: 10}
	tests := []struct {
		name       string
		catalog    *stubBarCatalog
		reader     *stubBarReader
		wantCode   ErrorCode
		wantStatus error
	}{
		{"missing instrument", &stubBarCatalog{instrumentErr: domain.ErrNotFound}, &stubBarReader{}, ErrorCodeInstrumentNotFound, nil},
		{"unknown provider", &stubBarCatalog{instrument: instrument, providerErr: domain.ErrNotFound}, &stubBarReader{}, ErrorCodeInvalidArgument, nil},
		{"missing mapping", &stubBarCatalog{instrument: instrument, provider: provider}, &stubBarReader{sourceErr: domain.ErrNotFound}, ErrorCodeInvalidArgument, nil},
		{"unsupported interval", &stubBarCatalog{instrument: instrument, provider: provider}, &stubBarReader{source: func() BarSourceRecord {
			changed := source
			changed.Capabilities.Intervals = []domain.BarInterval{domain.BarInterval1Day}
			return changed
		}()}, ErrorCodeUnsupportedInterval, nil},
		{"reader unavailable", &stubBarCatalog{instrument: instrument, provider: provider}, &stubBarReader{source: source, barsErr: domain.ErrDatabaseUnavailable}, "", domain.ErrDatabaseUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := NewBarsService(test.catalog, test.reader, time.Now)
			_, err := service.List(context.Background(), input)
			if test.wantCode != "" {
				var appError *Error
				if !errors.As(err, &appError) || appError.Code != test.wantCode {
					t.Fatalf("List() error = %v, want %s", err, test.wantCode)
				}
			}
			if test.wantStatus != nil && !errors.Is(err, test.wantStatus) {
				t.Fatalf("List() error = %v, want %v", err, test.wantStatus)
			}
		})
	}
}

func TestBarsServiceRejectsUnavailableAndCorruptResults(t *testing.T) {
	instrument, provider, source := testBarSourceFixtures(t)
	input := BarsInput{InstrumentCode: instrument.Code.String(), ProviderCode: provider.Code.String(), Interval: "1h", Limit: 10}
	disabled := provider
	disabled.Status = domain.ProviderStatusDisabled
	corruptSource := source
	corruptSource.ProviderInstrumentID = domain.ID{}
	wrongBar := testApplicationBar(t, instrument.ID, source.ProviderInstrumentID, time.Now().UTC().Truncate(time.Hour))
	wrongBar.ProviderInstrumentID = testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891699")
	tests := []struct {
		name     string
		catalog  *stubBarCatalog
		reader   *stubBarReader
		wantCode ErrorCode
	}{
		{"disabled", &stubBarCatalog{instrument: instrument, provider: disabled}, &stubBarReader{}, ErrorCodeInvalidArgument},
		{"corrupt source", &stubBarCatalog{instrument: instrument, provider: provider}, &stubBarReader{source: corruptSource}, ErrorCodeInternal},
		{"wrong bar source", &stubBarCatalog{instrument: instrument, provider: provider}, &stubBarReader{source: source, bars: []domain.MarketBar{wrongBar}}, ErrorCodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := NewBarsService(test.catalog, test.reader, time.Now)
			_, err := service.List(context.Background(), input)
			var appError *Error
			if !errors.As(err, &appError) || appError.Code != test.wantCode {
				t.Fatalf("List() error = %v", err)
			}
		})
	}
}

func testBarSourceFixtures(t *testing.T) (domain.Instrument, domain.Provider, BarSourceRecord) {
	t.Helper()
	asset := testLatestQuoteAsset(t, domain.AssetStatusActive)
	instrument := testLatestQuoteInstrument(t, asset.ID, domain.InstrumentStatusActive, nil, nil)
	provider := testLatestQuoteProvider(t, "bybit", domain.ProviderStatusActive)
	source := BarSourceRecord{
		InstrumentID: instrument.ID, BaseAssetCode: asset.Code, QuoteCurrency: instrument.QuoteCurrency,
		ProviderID: provider.ID, ProviderInstrumentID: testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891610"),
		ProviderInstrumentCode: testInstrumentOptionsCode(t, "provider.bybit.btc-usdt"), ProviderSymbol: "BTCUSDT",
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: []domain.BarInterval{domain.BarInterval1Hour, domain.BarInterval1Day}},
	}
	return instrument, provider, source
}

func testApplicationBar(t *testing.T, instrumentID, providerInstrumentID domain.ID, openTime time.Time) domain.MarketBar {
	t.Helper()
	open, _ := domain.NewUTCInstant(openTime)
	closeTime, _ := domain.NewUTCInstant(openTime.Add(time.Hour))
	bar, err := domain.NewStoredBar(domain.Bar{
		InstrumentID: instrumentID, ProviderInstrumentID: providerInstrumentID, Interval: domain.BarInterval1Hour,
		OpenTime: open, CloseTime: closeTime, Revision: 1,
		OpenPrice: domain.DecimalFromExact(decimal.NewFromInt(100)), HighPrice: domain.DecimalFromExact(decimal.NewFromInt(110)),
		LowPrice: domain.DecimalFromExact(decimal.NewFromInt(90)), ClosePrice: domain.DecimalFromExact(decimal.NewFromInt(105)),
		IsClosed: true, IsCurrent: true, QualityStatus: domain.QualityStatusValid, CollectedAt: closeTime,
	})
	if err != nil {
		t.Fatalf("NewStoredBar() error = %v", err)
	}
	return bar
}
