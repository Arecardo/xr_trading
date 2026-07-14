package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
)

type stubLatestQuoteCatalog struct {
	assetByCode   domain.Asset
	assetByID     domain.Asset
	instrument    domain.Instrument
	provider      domain.Provider
	assetCodeErr  error
	assetIDErr    error
	instrumentErr error
	providerErr   error
	assetIDCalls  int
}

func (stub *stubLatestQuoteCatalog) FindAssetByCode(context.Context, string) (domain.Asset, error) {
	return stub.assetByCode, stub.assetCodeErr
}
func (stub *stubLatestQuoteCatalog) FindAssetByID(context.Context, domain.ID) (domain.Asset, error) {
	stub.assetIDCalls++
	return stub.assetByID, stub.assetIDErr
}
func (stub *stubLatestQuoteCatalog) FindInstrumentByCode(context.Context, string) (domain.Instrument, error) {
	return stub.instrument, stub.instrumentErr
}
func (stub *stubLatestQuoteCatalog) FindProviderByCode(context.Context, string) (domain.Provider, error) {
	return stub.provider, stub.providerErr
}

type stubLatestQuoteReader struct {
	records []LatestQuoteRecord
	err     error
	filter  LatestQuoteFilter
	calls   int
}

func (stub *stubLatestQuoteReader) ListLatestQuoteRecords(_ context.Context, filter LatestQuoteFilter) ([]LatestQuoteRecord, error) {
	stub.calls++
	stub.filter = filter
	return append([]LatestQuoteRecord(nil), stub.records...), stub.err
}

func TestNewLatestQuotesServiceRequiresDependencies(t *testing.T) {
	catalog := &stubLatestQuoteCatalog{}
	reader := &stubLatestQuoteReader{}
	for _, test := range []struct {
		catalog LatestQuoteCatalog
		reader  LatestQuoteReader
		now     func() time.Time
	}{{nil, reader, time.Now}, {catalog, nil, time.Now}, {catalog, reader, nil}} {
		if _, err := NewLatestQuotesService(test.catalog, test.reader, test.now); err == nil {
			t.Fatal("NewLatestQuotesService() error = nil")
		}
	}
}

func TestLatestQuotesServiceListsEverySourceInStableOrder(t *testing.T) {
	now := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	asset := testLatestQuoteAsset(t, domain.AssetStatusActive)
	instrument := testLatestQuoteInstrument(t, asset.ID, domain.InstrumentStatusActive, nil, nil)
	bybit := testLatestQuoteProvider(t, "bybit", domain.ProviderStatusActive)
	coingecko := testLatestQuoteProvider(t, "coingecko", domain.ProviderStatusDegraded)
	reader := &stubLatestQuoteReader{records: []LatestQuoteRecord{
		testLatestQuoteRecord(t, instrument, coingecko, "provider.coingecko.btc-usdt", "BTC", "101.25"),
		testLatestQuoteRecord(t, instrument, bybit, "provider.bybit.btc-usdt-secondary", "BTCUSDT-2", "100.50"),
		testLatestQuoteRecord(t, instrument, bybit, "provider.bybit.btc-usdt", "BTCUSDT", "100.25"),
	}}
	catalog := &stubLatestQuoteCatalog{assetByCode: asset, assetByID: asset, instrument: instrument}
	service, _ := NewLatestQuotesService(catalog, reader, func() time.Time { return now })

	result, err := service.List(context.Background(), LatestQuotesInput{AssetCode: asset.Code.String()})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Asset.ID != asset.ID || len(result.Quotes) != 3 {
		t.Fatalf("List() = %#v", result)
	}
	if result.Quotes[0].ProviderInstrumentCode.String() != "provider.bybit.btc-usdt" || result.Quotes[1].ProviderInstrumentCode.String() != "provider.bybit.btc-usdt-secondary" || result.Quotes[2].ProviderCode.String() != "coingecko" {
		t.Fatalf("stable multi-source order = %#v", result.Quotes)
	}
	if reader.filter.AssetID != asset.ID || reader.filter.InstrumentID != nil || reader.filter.ProviderID != nil || !reader.filter.EffectiveAt.Equal(now.UTC()) || reader.filter.EffectiveAt.Location() != time.UTC {
		t.Fatalf("reader filter = %#v", reader.filter)
	}
}

func TestLatestQuotesServiceResolvesInstrumentAndProviderFilters(t *testing.T) {
	asset := testLatestQuoteAsset(t, domain.AssetStatusActive)
	instrument := testLatestQuoteInstrument(t, asset.ID, domain.InstrumentStatusActive, nil, nil)
	provider := testLatestQuoteProvider(t, "bybit", domain.ProviderStatusActive)
	record := testLatestQuoteRecord(t, instrument, provider, "provider.bybit.btc-usdt", "BTCUSDT", "100.25")
	catalog := &stubLatestQuoteCatalog{assetByCode: asset, assetByID: asset, instrument: instrument, provider: provider}
	reader := &stubLatestQuoteReader{records: []LatestQuoteRecord{record}}
	service, _ := NewLatestQuotesService(catalog, reader, time.Now)

	result, err := service.List(context.Background(), LatestQuotesInput{InstrumentCode: instrument.Code.String(), ProviderCode: provider.Code.String()})
	if err != nil || len(result.Quotes) != 1 || catalog.assetIDCalls != 1 || reader.filter.InstrumentID == nil || *reader.filter.InstrumentID != instrument.ID || reader.filter.ProviderID == nil || *reader.filter.ProviderID != provider.ID {
		t.Fatalf("List() = (%#v, %v), catalog=%#v filter=%#v", result, err, catalog, reader.filter)
	}

	catalog.assetIDCalls = 0
	_, err = service.List(context.Background(), LatestQuotesInput{AssetCode: asset.Code.String(), InstrumentCode: instrument.Code.String()})
	if err != nil || catalog.assetIDCalls != 0 {
		t.Fatalf("List(asset+instrument) error=%v asset ID calls=%d", err, catalog.assetIDCalls)
	}
}

func TestLatestQuotesServiceRejectsInvalidInputsBeforeReading(t *testing.T) {
	tests := []LatestQuotesInput{
		{},
		{ProviderCode: "bybit"},
		{AssetCode: "provider.bybit"},
		{InstrumentCode: "asset.crypto.btc"},
		{AssetCode: "asset.crypto.btc", ProviderCode: "Bad Provider"},
	}
	for _, input := range tests {
		reader := &stubLatestQuoteReader{}
		service, _ := NewLatestQuotesService(&stubLatestQuoteCatalog{}, reader, time.Now)
		_, err := service.List(context.Background(), input)
		var appError *Error
		if !errors.As(err, &appError) || appError.Code != ErrorCodeInvalidArgument || reader.calls != 0 {
			t.Fatalf("List(%#v) error=%v reader calls=%d", input, err, reader.calls)
		}
	}
}

func TestLatestQuotesServiceMapsCatalogAndCombinationErrors(t *testing.T) {
	asset := testLatestQuoteAsset(t, domain.AssetStatusActive)
	otherAsset := testLatestQuoteAssetWithCode(t, domain.AssetStatusActive, "asset.crypto.eth", "ETH")
	instrument := testLatestQuoteInstrument(t, otherAsset.ID, domain.InstrumentStatusActive, nil, nil)
	provider := testLatestQuoteProvider(t, "bybit", domain.ProviderStatusActive)
	tests := []struct {
		name     string
		input    LatestQuotesInput
		catalog  *stubLatestQuoteCatalog
		wantCode ErrorCode
	}{
		{"asset missing", LatestQuotesInput{AssetCode: "asset.crypto.btc"}, &stubLatestQuoteCatalog{assetCodeErr: domain.ErrNotFound}, ErrorCodeAssetNotFound},
		{"instrument missing", LatestQuotesInput{InstrumentCode: "instrument.crypto.btc-usdt"}, &stubLatestQuoteCatalog{instrumentErr: domain.ErrNotFound}, ErrorCodeInstrumentNotFound},
		{"unknown provider", LatestQuotesInput{AssetCode: asset.Code.String(), ProviderCode: "bybit"}, &stubLatestQuoteCatalog{assetByCode: asset, providerErr: domain.ErrNotFound}, ErrorCodeInvalidArgument},
		{"mismatched pair", LatestQuotesInput{AssetCode: asset.Code.String(), InstrumentCode: instrument.Code.String()}, &stubLatestQuoteCatalog{assetByCode: asset, instrument: instrument}, ErrorCodeInvalidArgument},
		{"catalog failure", LatestQuotesInput{AssetCode: asset.Code.String()}, &stubLatestQuoteCatalog{assetCodeErr: errors.New("broken")}, ErrorCodeInternal},
		{"corrupt provider", LatestQuotesInput{AssetCode: asset.Code.String(), ProviderCode: "bybit"}, &stubLatestQuoteCatalog{assetByCode: asset, provider: testLatestQuoteProvider(t, "coingecko", domain.ProviderStatusActive)}, ErrorCodeInternal},
		{"valid pair", LatestQuotesInput{AssetCode: otherAsset.Code.String(), InstrumentCode: instrument.Code.String(), ProviderCode: provider.Code.String()}, &stubLatestQuoteCatalog{assetByCode: otherAsset, instrument: instrument, provider: provider}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := NewLatestQuotesService(test.catalog, &stubLatestQuoteReader{}, time.Now)
			_, err := service.List(context.Background(), test.input)
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("List() error = %v", err)
				}
				return
			}
			var appError *Error
			if !errors.As(err, &appError) || appError.Code != test.wantCode {
				t.Fatalf("List() error = %v, want %s", err, test.wantCode)
			}
		})
	}
}

func TestLatestQuotesServiceReturnsEmptyForUnavailableCatalogEntities(t *testing.T) {
	now := time.Now().UTC()
	asset := testLatestQuoteAsset(t, domain.AssetStatusActive)
	activeInstrument := testLatestQuoteInstrument(t, asset.ID, domain.InstrumentStatusActive, nil, nil)
	activeProvider := testLatestQuoteProvider(t, "bybit", domain.ProviderStatusActive)
	future := now.Add(time.Hour)
	tests := []struct {
		name       string
		asset      domain.Asset
		instrument domain.Instrument
		provider   domain.Provider
	}{
		{"inactive asset", testLatestQuoteAsset(t, domain.AssetStatusInactive), activeInstrument, activeProvider},
		{"suspended instrument", asset, testLatestQuoteInstrument(t, asset.ID, domain.InstrumentStatusSuspended, nil, nil), activeProvider},
		{"future instrument", asset, testLatestQuoteInstrument(t, asset.ID, domain.InstrumentStatusActive, &future, nil), activeProvider},
		{"disabled provider", asset, activeInstrument, testLatestQuoteProvider(t, "bybit", domain.ProviderStatusDisabled)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &stubLatestQuoteReader{}
			catalog := &stubLatestQuoteCatalog{assetByCode: test.asset, instrument: test.instrument, provider: test.provider}
			service, _ := NewLatestQuotesService(catalog, reader, func() time.Time { return now })
			result, err := service.List(context.Background(), LatestQuotesInput{AssetCode: test.asset.Code.String(), InstrumentCode: test.instrument.Code.String(), ProviderCode: test.provider.Code.String()})
			if err != nil || len(result.Quotes) != 0 || reader.calls != 0 {
				t.Fatalf("List() = (%#v, %v), reader calls=%d", result, err, reader.calls)
			}
		})
	}
}

func TestLatestQuotesServiceRejectsReaderCorruptionAndPreservesDatabaseErrors(t *testing.T) {
	asset := testLatestQuoteAsset(t, domain.AssetStatusActive)
	instrument := testLatestQuoteInstrument(t, asset.ID, domain.InstrumentStatusActive, nil, nil)
	provider := testLatestQuoteProvider(t, "bybit", domain.ProviderStatusActive)
	valid := testLatestQuoteRecord(t, instrument, provider, "provider.bybit.btc-usdt", "BTCUSDT", "100")
	invalid := valid
	invalid.ProviderSymbol = ""
	for _, test := range []struct {
		name     string
		records  []LatestQuoteRecord
		readErr  error
		wantCode ErrorCode
		wantIs   error
	}{
		{"invalid projection", []LatestQuoteRecord{invalid}, nil, ErrorCodeInternal, domain.ErrInvalidData},
		{"reader failure", nil, errors.New("broken"), ErrorCodeInternal, nil},
		{"database unavailable", nil, domain.ErrDatabaseUnavailable, "", domain.ErrDatabaseUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &stubLatestQuoteReader{records: test.records, err: test.readErr}
			service, _ := NewLatestQuotesService(&stubLatestQuoteCatalog{assetByCode: asset}, reader, time.Now)
			_, err := service.List(context.Background(), LatestQuotesInput{AssetCode: asset.Code.String()})
			if test.wantCode != "" {
				var appError *Error
				if !errors.As(err, &appError) || appError.Code != test.wantCode {
					t.Fatalf("List() error = %v", err)
				}
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("List() error = %v, want %v", err, test.wantIs)
			}
		})
	}
}

func TestEffectiveAtWithin(t *testing.T) {
	now := time.Now().UTC()
	before, after := now.Add(-time.Minute), now.Add(time.Minute)
	if !effectiveAtWithin(now, &before, &after) || effectiveAtWithin(time.Time{}, nil, nil) || effectiveAtWithin(now, &after, nil) || effectiveAtWithin(now, nil, &before) {
		t.Fatal("effectiveAtWithin() boundary behavior is incorrect")
	}
}

func testLatestQuoteAsset(t *testing.T, status domain.AssetStatus) domain.Asset {
	t.Helper()
	return testLatestQuoteAssetWithCode(t, status, "asset.crypto.btc", "BTC")
}

func testLatestQuoteAssetWithCode(t *testing.T, status domain.AssetStatus, codeValue, symbol string) domain.Asset {
	t.Helper()
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	asset, err := domain.NewAsset(domain.Asset{ID: testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca272789160f"), Code: testInstrumentOptionsCode(t, codeValue), AssetType: domain.AssetTypeCrypto, CanonicalSymbol: symbol, Name: symbol, Status: status, CreatedAt: now, UpdatedAt: now})
	if codeValue == "asset.crypto.eth" {
		asset.ID = testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca272789161f")
	}
	if err != nil {
		t.Fatalf("NewAsset() error = %v", err)
	}
	return asset
}

func testLatestQuoteInstrument(t *testing.T, assetID domain.ID, status domain.InstrumentStatus, validFrom, validTo *time.Time) domain.Instrument {
	t.Helper()
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	instrument, err := domain.NewInstrument(domain.Instrument{
		ID: testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891601"), Code: testInstrumentOptionsCode(t, "instrument.crypto.btc-usdt"),
		AssetID: assetID, Venue: "BYBIT", InstrumentType: domain.InstrumentTypeSpot, Symbol: "BTC-USDT", QuoteCurrency: "USDT",
		MarketTimezone: "UTC", Status: status, ValidFrom: validFrom, ValidTo: validTo, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("NewInstrument() error = %v", err)
	}
	return instrument
}

func testLatestQuoteProvider(t *testing.T, codeValue string, status domain.ProviderStatus) domain.Provider {
	t.Helper()
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	id := testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891602")
	if codeValue == "coingecko" {
		id = testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891603")
	}
	provider, err := domain.NewProvider(domain.Provider{ID: id, Code: testInstrumentOptionsCode(t, codeValue), Name: codeValue, ProviderType: domain.ProviderTypeExchange, Status: status, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	return provider
}

func testLatestQuoteRecord(t *testing.T, instrument domain.Instrument, provider domain.Provider, mappingCode, symbol, price string) LatestQuoteRecord {
	t.Helper()
	mappingID := testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891610")
	if mappingCode == "provider.bybit.btc-usdt-secondary" {
		mappingID = testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891611")
	} else if provider.Code.String() == "coingecko" {
		mappingID = testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891612")
	}
	instant, _ := domain.NewUTCInstant(time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC))
	quote, err := domain.NewQuote(domain.Quote{
		InstrumentID: instrument.ID, ProviderInstrumentID: mappingID, MarketTime: instant,
		LastPrice: domain.DecimalFromExact(decimal.RequireFromString(price)), QualityStatus: domain.QualityStatusValid, CollectedAt: instant,
	})
	if err != nil {
		t.Fatalf("NewQuote() error = %v", err)
	}
	return LatestQuoteRecord{InstrumentCode: instrument.Code, QuoteCurrency: instrument.QuoteCurrency, ProviderID: provider.ID, ProviderCode: provider.Code, ProviderInstrumentCode: testInstrumentOptionsCode(t, mappingCode), ProviderSymbol: symbol, Quote: quote}
}
