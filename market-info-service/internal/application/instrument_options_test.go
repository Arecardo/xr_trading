package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
)

type stubInstrumentOptionsCatalog struct {
	asset domain.Asset
	err   error
	calls int
}

func (stub *stubInstrumentOptionsCatalog) FindAssetByCode(context.Context, string) (domain.Asset, error) {
	stub.calls++
	return stub.asset, stub.err
}

func (*stubInstrumentOptionsCatalog) FindInstrumentByCode(context.Context, string) (domain.Instrument, error) {
	return domain.Instrument{}, errors.New("not implemented")
}

type stubInstrumentOptionsReader struct {
	rows   []InstrumentProviderOptionRecord
	err    error
	filter InstrumentOptionsFilter
	calls  int
}

func (stub *stubInstrumentOptionsReader) ListInstrumentProviderOptions(_ context.Context, filter InstrumentOptionsFilter) ([]InstrumentProviderOptionRecord, error) {
	stub.calls++
	stub.filter = filter
	return stub.rows, stub.err
}

func TestNewInstrumentOptionsServiceRequiresDependencies(t *testing.T) {
	catalog := &stubInstrumentOptionsCatalog{}
	reader := &stubInstrumentOptionsReader{}
	for _, test := range []struct {
		catalog domain.CatalogRepository
		reader  InstrumentOptionsReader
		now     func() time.Time
	}{{nil, reader, time.Now}, {catalog, nil, time.Now}, {catalog, reader, nil}} {
		if _, err := NewInstrumentOptionsService(test.catalog, test.reader, test.now); err == nil {
			t.Fatal("NewInstrumentOptionsService() error = nil")
		}
	}
}

func TestInstrumentOptionsServiceListsOrderedPage(t *testing.T) {
	now := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	asset := testInstrumentOptionsAsset(t, domain.AssetStatusActive)
	firstID := testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891601")
	secondID := testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891602")
	reader := &stubInstrumentOptionsReader{rows: []InstrumentProviderOptionRecord{
		testInstrumentOptionRecord(t, secondID, "instrument.crypto.btc-zusd", "BTC/ZUSD", "coingecko", false, 20, "provider.coingecko.btc-zusd", domain.BarInterval1Day),
		testInstrumentOptionRecord(t, firstID, "instrument.crypto.btc-usdt", "BTC/USDT", "coingecko", false, 5, "provider.coingecko.btc-usdt", domain.BarInterval1Day),
		testInstrumentOptionRecord(t, firstID, "instrument.crypto.btc-usdt", "BTC/USDT", "bybit", true, 10, "provider.bybit.btc-usdt", domain.BarInterval1Hour, domain.BarInterval1Day),
		// A second mapping for the same Provider is collapsed after the better
		// default mapping has been selected.
		testInstrumentOptionRecord(t, firstID, "instrument.crypto.btc-usdt", "BTC/USDT", "bybit", false, 1, "provider.bybit.btc-usdt-secondary", domain.BarInterval1Day),
	}}
	catalog := &stubInstrumentOptionsCatalog{asset: asset}
	service, err := NewInstrumentOptionsService(catalog, reader, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewInstrumentOptionsService() error = %v", err)
	}

	page, err := service.List(context.Background(), InstrumentOptionsInput{AssetCode: asset.Code.String(), Limit: 1})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Code.String() != "instrument.crypto.btc-usdt" || page.NextAfterInstrumentCode == nil || page.NextAfterInstrumentCode.String() != "instrument.crypto.btc-usdt" {
		t.Fatalf("List() page = %#v", page)
	}
	providers := page.Items[0].Providers
	if len(providers) != 2 || providers[0].Code.String() != "bybit" || !providers[0].IsDefault || providers[1].Code.String() != "coingecko" {
		t.Fatalf("List() providers = %#v", providers)
	}
	if len(providers[0].SupportedIntervals) != 2 || providers[0].SupportedIntervals[0] != domain.BarInterval1Hour {
		t.Fatalf("List() intervals = %#v", providers[0].SupportedIntervals)
	}
	if reader.filter.AssetID != asset.ID || reader.filter.InstrumentLimit != 2 || !reader.filter.EffectiveAt.Equal(now.UTC()) || reader.filter.EffectiveAt.Location() != time.UTC {
		t.Fatalf("reader filter = %#v", reader.filter)
	}
	reader.rows[2].Capabilities.Intervals[0] = domain.BarInterval1Day
	if providers[0].SupportedIntervals[0] != domain.BarInterval1Hour {
		t.Fatal("response intervals share reader storage")
	}
}

func TestInstrumentOptionsServiceUsesCursorAndReturnsCompleteLastPage(t *testing.T) {
	asset := testInstrumentOptionsAsset(t, domain.AssetStatusActive)
	reader := &stubInstrumentOptionsReader{rows: []InstrumentProviderOptionRecord{
		testInstrumentOptionRecord(t, testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891601"), "instrument.crypto.btc-usdt", "BTC/USDT", "bybit", false, 10, "provider.bybit.btc-usdt", domain.BarInterval1Hour),
	}}
	service, _ := NewInstrumentOptionsService(&stubInstrumentOptionsCatalog{asset: asset}, reader, time.Now)
	page, err := service.List(context.Background(), InstrumentOptionsInput{AssetCode: asset.Code.String(), AfterInstrumentCode: "instrument.crypto.btc-usdc", Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if page.NextAfterInstrumentCode != nil || len(page.Items) != 1 || reader.filter.AfterInstrumentCode == nil || reader.filter.AfterInstrumentCode.String() != "instrument.crypto.btc-usdc" {
		t.Fatalf("List() = %#v, filter = %#v", page, reader.filter)
	}
}

func TestInstrumentOptionsServiceValidationDoesNotCallRepositories(t *testing.T) {
	for _, input := range []InstrumentOptionsInput{
		{AssetCode: "", Limit: 10},
		{AssetCode: "provider.bybit", Limit: 10},
		{AssetCode: "asset.crypto.btc", Limit: 0},
		{AssetCode: "asset.crypto.btc", Limit: MaximumInstrumentOptionsPageSize + 1},
		{AssetCode: "asset.crypto.btc", AfterInstrumentCode: "asset.crypto.eth", Limit: 10},
	} {
		catalog := &stubInstrumentOptionsCatalog{}
		reader := &stubInstrumentOptionsReader{}
		service, _ := NewInstrumentOptionsService(catalog, reader, time.Now)
		_, err := service.List(context.Background(), input)
		var appError *Error
		if !errors.As(err, &appError) || appError.Code != ErrorCodeInvalidArgument || catalog.calls != 0 || reader.calls != 0 {
			t.Fatalf("List(%#v) = %v, catalog calls=%d reader calls=%d", input, err, catalog.calls, reader.calls)
		}
	}
}

func TestInstrumentOptionsServiceHandlesAssetStatesAndFailures(t *testing.T) {
	validInput := InstrumentOptionsInput{AssetCode: "asset.crypto.btc", Limit: 10}
	tests := []struct {
		name      string
		catalog   *stubInstrumentOptionsCatalog
		readerErr error
		wantCode  ErrorCode
		wantCause error
		wantEmpty bool
	}{
		{"missing", &stubInstrumentOptionsCatalog{err: domain.ErrNotFound}, nil, ErrorCodeAssetNotFound, domain.ErrNotFound, false},
		{"catalog corrupt", &stubInstrumentOptionsCatalog{asset: testInstrumentOptionsAssetWithCode(t, domain.AssetStatusActive, "asset.crypto.eth")}, nil, ErrorCodeInternal, domain.ErrInvalidData, false},
		{"catalog failure", &stubInstrumentOptionsCatalog{err: errors.New("broken")}, nil, ErrorCodeInternal, nil, false},
		{"inactive", &stubInstrumentOptionsCatalog{asset: testInstrumentOptionsAsset(t, domain.AssetStatusInactive)}, nil, "", nil, true},
		{"reader failure", &stubInstrumentOptionsCatalog{asset: testInstrumentOptionsAsset(t, domain.AssetStatusActive)}, errors.New("broken"), ErrorCodeInternal, nil, false},
		{"database unavailable", &stubInstrumentOptionsCatalog{asset: testInstrumentOptionsAsset(t, domain.AssetStatusActive)}, domain.ErrDatabaseUnavailable, "", domain.ErrDatabaseUnavailable, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &stubInstrumentOptionsReader{err: test.readerErr}
			service, _ := NewInstrumentOptionsService(test.catalog, reader, time.Now)
			page, err := service.List(context.Background(), validInput)
			if test.wantEmpty {
				if err != nil || len(page.Items) != 0 || reader.calls != 0 {
					t.Fatalf("List() = (%#v, %v), reader calls=%d", page, err, reader.calls)
				}
				return
			}
			if test.wantCode != "" {
				var appError *Error
				if !errors.As(err, &appError) || appError.Code != test.wantCode {
					t.Fatalf("List() error = %v, want code %s", err, test.wantCode)
				}
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("List() error = %v, want cause %v", err, test.wantCause)
			}
		})
	}
}

func TestGroupInstrumentOptionsRejectsInvalidProjection(t *testing.T) {
	valid := testInstrumentOptionRecord(t, testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891601"), "instrument.crypto.btc-usdt", "BTC/USDT", "bybit", true, 10, "provider.bybit.btc-usdt", domain.BarInterval1Hour)
	invalid := valid
	invalid.ProviderDisplayName = " "
	if _, err := groupInstrumentOptions([]InstrumentProviderOptionRecord{invalid}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("groupInstrumentOptions(invalid) error = %v", err)
	}
	inconsistent := valid
	inconsistent.InstrumentDisplayName = "Different"
	if _, err := groupInstrumentOptions([]InstrumentProviderOptionRecord{valid, inconsistent}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("groupInstrumentOptions(inconsistent) error = %v", err)
	}
}

func TestGroupInstrumentOptionsFallsBackToPriority(t *testing.T) {
	instrumentID := testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891601")
	higherPriority := testInstrumentOptionRecord(t, instrumentID, "instrument.crypto.btc-usdt", "BTC/USDT", "coingecko", false, 20, "provider.coingecko.btc-usdt", domain.BarInterval1Day)
	lowerPriority := testInstrumentOptionRecord(t, instrumentID, "instrument.crypto.btc-usdt", "BTC/USDT", "bybit", false, 10, "provider.bybit.btc-usdt", domain.BarInterval1Hour)
	items, err := groupInstrumentOptions([]InstrumentProviderOptionRecord{higherPriority, lowerPriority})
	if err != nil {
		t.Fatalf("groupInstrumentOptions() error = %v", err)
	}
	if len(items) != 1 || len(items[0].Providers) != 2 || items[0].Providers[0].Code.String() != "bybit" || items[0].Providers[0].IsDefault {
		t.Fatalf("groupInstrumentOptions() = %#v", items)
	}
}

func testInstrumentOptionsAsset(t *testing.T, status domain.AssetStatus) domain.Asset {
	t.Helper()
	return testInstrumentOptionsAssetWithCode(t, status, "asset.crypto.btc")
}

func testInstrumentOptionsAssetWithCode(t *testing.T, status domain.AssetStatus, codeValue string) domain.Asset {
	t.Helper()
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	asset, err := domain.NewAsset(domain.Asset{
		ID: testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca272789160f"), Code: testInstrumentOptionsCode(t, codeValue),
		AssetType: domain.AssetTypeCrypto, CanonicalSymbol: "BTC", Name: "Bitcoin", Status: status, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("NewAsset() error = %v", err)
	}
	return asset
}

func testInstrumentOptionRecord(t *testing.T, instrumentID domain.ID, instrumentCode, displayName, providerCode string, isDefault bool, priority int16, mappingCode string, intervals ...domain.BarInterval) InstrumentProviderOptionRecord {
	t.Helper()
	return InstrumentProviderOptionRecord{
		InstrumentID: instrumentID, InstrumentCode: testInstrumentOptionsCode(t, instrumentCode), InstrumentDisplayName: displayName,
		ProviderInstrumentID: testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891610"), ProviderInstrumentCode: testInstrumentOptionsCode(t, mappingCode),
		ProviderCode: testInstrumentOptionsCode(t, providerCode), ProviderDisplayName: providerCode, IsDefault: isDefault, Priority: priority,
		Capabilities: domain.ProviderCapabilities{Historical: true, Intervals: intervals},
	}
}

func testInstrumentOptionsID(value string) domain.ID {
	return domain.IDFromUUID(uuid.MustParse(value))
}

func testInstrumentOptionsCode(t *testing.T, value string) domain.Code {
	t.Helper()
	code, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return code
}
