package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"xr-trading/market-info-service/internal/domain"
)

type stubInstrumentPrecisionCatalog struct {
	instruments []domain.Instrument
	err         error
	requested   []domain.ID
	calls       int
}

func (stub *stubInstrumentPrecisionCatalog) FindInstrumentsByIDs(_ context.Context, ids []domain.ID) ([]domain.Instrument, error) {
	stub.calls++
	stub.requested = append([]domain.ID(nil), ids...)
	return stub.instruments, stub.err
}

func TestNewInstrumentPrecisionServiceRequiresDependencies(t *testing.T) {
	catalog := &stubInstrumentPrecisionCatalog{}
	for _, test := range []struct {
		catalog InstrumentPrecisionCatalog
		now     func() time.Time
	}{{nil, time.Now}, {catalog, nil}} {
		if _, err := NewInstrumentPrecisionService(test.catalog, test.now); err == nil {
			t.Fatal("NewInstrumentPrecisionService() error = nil")
		}
	}
}

func TestInstrumentPrecisionServiceBatchValidatesInput(t *testing.T) {
	catalog := &stubInstrumentPrecisionCatalog{}
	service, err := NewInstrumentPrecisionService(catalog, func() time.Time { return testInstrumentPrecisionNow })
	if err != nil {
		t.Fatalf("NewInstrumentPrecisionService() error = %v", err)
	}

	tooMany := make([]string, MaximumInstrumentPrecisionBatchSize+1)
	for index := range tooMany {
		tooMany[index] = "019f1452-90f7-7992-a87a-ca2727891601"
	}

	tests := []struct {
		name  string
		input InstrumentPrecisionInput
	}{
		{name: "empty", input: InstrumentPrecisionInput{}},
		{name: "invalid UUID", input: InstrumentPrecisionInput{InstrumentIDs: []string{"not-a-uuid"}}},
		{name: "too many", input: InstrumentPrecisionInput{InstrumentIDs: tooMany}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Batch(context.Background(), test.input); err == nil {
				t.Fatalf("Batch(%s) error = nil", test.name)
			}
		})
	}
	if catalog.calls != 0 {
		t.Fatalf("catalog.calls = %d, want 0 (validation must fail before querying storage)", catalog.calls)
	}
}

func TestInstrumentPrecisionServiceBatchDeduplicatesAndPreservesOrder(t *testing.T) {
	nvda := testInstrumentPrecisionInstrument(t, "019f1452-90f7-7992-a87a-ca2727891601", "instrument.nasdaq.equity.nvda", domain.InstrumentStatusActive, nil, nil)
	btc := testInstrumentPrecisionInstrument(t, "019f1452-90f7-7992-a87a-ca2727891602", "instrument.bybit.spot.btc-usdt", domain.InstrumentStatusActive, nil, nil)
	catalog := &stubInstrumentPrecisionCatalog{instruments: []domain.Instrument{btc, nvda}}
	service, err := NewInstrumentPrecisionService(catalog, func() time.Time { return testInstrumentPrecisionNow })
	if err != nil {
		t.Fatalf("NewInstrumentPrecisionService() error = %v", err)
	}

	result, err := service.Batch(context.Background(), InstrumentPrecisionInput{InstrumentIDs: []string{
		nvda.ID.String(), btc.ID.String(), nvda.ID.String(),
	}})
	if err != nil {
		t.Fatalf("Batch() error = %v", err)
	}
	if len(catalog.requested) != 2 || catalog.requested[0] != nvda.ID || catalog.requested[1] != btc.ID {
		t.Fatalf("requested IDs = %#v, want deduplicated [nvda, btc]", catalog.requested)
	}
	if len(result.Items) != 2 || result.Items[0].InstrumentID != nvda.ID || result.Items[1].InstrumentID != btc.ID {
		t.Fatalf("Batch() items = %#v, want [nvda, btc] in request order", result.Items)
	}
	if len(result.MissingInstrumentIDs) != 0 {
		t.Fatalf("MissingInstrumentIDs = %#v, want none", result.MissingInstrumentIDs)
	}
	if result.Items[0].PriceScale != 2 || result.Items[0].QuantityScale != 0 || result.Items[0].LotSize.String() != "1" || result.Items[0].MinQuantity.String() != "1" {
		t.Fatalf("Batch() first item = %#v", result.Items[0])
	}
	if result.Items[0].AsOf.Time() != testInstrumentPrecisionNow.Add(-time.Hour) {
		t.Fatalf("Batch() as_of = %v, want the Instrument's UpdatedAt", result.Items[0].AsOf.Time())
	}
}

func TestInstrumentPrecisionServiceBatchReportsMissingInstruments(t *testing.T) {
	unknownID := "019f1452-90f7-7992-a87a-ca2727891699"
	incomplete := testInstrumentPrecisionInstrument(t, "019f1452-90f7-7992-a87a-ca2727891602", "instrument.bybit.spot.eth-usdt", domain.InstrumentStatusActive, nil, nil)
	incomplete.PriceScale = nil
	suspended := testInstrumentPrecisionInstrument(t, "019f1452-90f7-7992-a87a-ca2727891603", "instrument.bybit.spot.ltc-usdt", domain.InstrumentStatusSuspended, nil, nil)
	future := testInstrumentPrecisionNow.Add(time.Hour)
	notYetEffective := testInstrumentPrecisionInstrument(t, "019f1452-90f7-7992-a87a-ca2727891604", "instrument.bybit.spot.sol-usdt", domain.InstrumentStatusActive, &future, nil)

	catalog := &stubInstrumentPrecisionCatalog{instruments: []domain.Instrument{incomplete, suspended, notYetEffective}}
	service, err := NewInstrumentPrecisionService(catalog, func() time.Time { return testInstrumentPrecisionNow })
	if err != nil {
		t.Fatalf("NewInstrumentPrecisionService() error = %v", err)
	}

	result, err := service.Batch(context.Background(), InstrumentPrecisionInput{InstrumentIDs: []string{
		unknownID, incomplete.ID.String(), suspended.ID.String(), notYetEffective.ID.String(),
	}})
	if err != nil {
		t.Fatalf("Batch() error = %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("Batch() items = %#v, want none", result.Items)
	}
	if len(result.MissingInstrumentIDs) != 4 {
		t.Fatalf("MissingInstrumentIDs = %#v, want 4 entries", result.MissingInstrumentIDs)
	}
	if result.MissingInstrumentIDs[0].String() != unknownID {
		t.Fatalf("MissingInstrumentIDs[0] = %v, want unknown ID first (request order preserved)", result.MissingInstrumentIDs[0])
	}
}

func TestInstrumentPrecisionServiceBatchClassifiesStorageFailures(t *testing.T) {
	requestID := "019f1452-90f7-7992-a87a-ca2727891601"

	unavailable := &stubInstrumentPrecisionCatalog{err: domain.ErrDatabaseUnavailable}
	service, err := NewInstrumentPrecisionService(unavailable, func() time.Time { return testInstrumentPrecisionNow })
	if err != nil {
		t.Fatalf("NewInstrumentPrecisionService() error = %v", err)
	}
	if _, err := service.Batch(context.Background(), InstrumentPrecisionInput{InstrumentIDs: []string{requestID}}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("Batch(unavailable) error = %v", err)
	}

	other := &stubInstrumentPrecisionCatalog{err: errors.New("boom")}
	service, err = NewInstrumentPrecisionService(other, func() time.Time { return testInstrumentPrecisionNow })
	if err != nil {
		t.Fatalf("NewInstrumentPrecisionService() error = %v", err)
	}
	if _, err := service.Batch(context.Background(), InstrumentPrecisionInput{InstrumentIDs: []string{requestID}}); err == nil {
		t.Fatal("Batch(storage error) error = nil")
	}

	malformed := &stubInstrumentPrecisionCatalog{instruments: []domain.Instrument{{}}}
	service, err = NewInstrumentPrecisionService(malformed, func() time.Time { return testInstrumentPrecisionNow })
	if err != nil {
		t.Fatalf("NewInstrumentPrecisionService() error = %v", err)
	}
	if _, err := service.Batch(context.Background(), InstrumentPrecisionInput{InstrumentIDs: []string{requestID}}); err == nil {
		t.Fatal("Batch(invalid projection) error = nil")
	}
}

var testInstrumentPrecisionNow = time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)

func testInstrumentPrecisionInstrument(t *testing.T, idValue, codeValue string, status domain.InstrumentStatus, validFrom, validTo *time.Time) domain.Instrument {
	t.Helper()
	updatedAt := testInstrumentPrecisionNow.Add(-time.Hour)
	priceScale := int16(2)
	quantityScale := int16(0)
	lotSize, err := domain.ParseDecimal("1")
	if err != nil {
		t.Fatalf("ParseDecimal() error = %v", err)
	}
	minQuantity, err := domain.ParseDecimal("1")
	if err != nil {
		t.Fatalf("ParseDecimal() error = %v", err)
	}
	instrument, err := domain.NewInstrument(domain.Instrument{
		ID: testInstrumentOptionsID(idValue), Code: testInstrumentOptionsCode(t, codeValue),
		AssetID: testInstrumentOptionsID("019f1452-90f7-7992-a87a-ca2727891600"), Venue: "NASDAQ",
		InstrumentType: domain.InstrumentTypeEquity, Symbol: "NVDA", QuoteCurrency: "USD", MarketTimezone: "America/New_York",
		PriceScale: &priceScale, QuantityScale: &quantityScale, LotSize: &lotSize, MinQuantity: &minQuantity,
		Status: status, ValidFrom: validFrom, ValidTo: validTo,
		CreatedAt: testInstrumentPrecisionNow.Add(-24 * time.Hour), UpdatedAt: updatedAt,
	})
	if err != nil {
		t.Fatalf("NewInstrument() error = %v", err)
	}
	return instrument
}
