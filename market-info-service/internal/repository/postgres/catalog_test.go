package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

type scanFunc func(...any) error

func (function scanFunc) Scan(destinations ...any) error {
	return function(destinations...)
}

type fakeCatalogDatabase struct {
	exec     func(context.Context, string, ...any) (pgconn.CommandTag, error)
	queryRow func(context.Context, string, ...any) pgx.Row
	query    func(context.Context, string, ...any) (pgx.Rows, error)
}

func (database fakeCatalogDatabase) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return database.exec(ctx, query, args...)
}

func (database fakeCatalogDatabase) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return database.queryRow(ctx, query, args...)
}

func (database fakeCatalogDatabase) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return database.query(ctx, query, args...)
}

type fakeRows struct {
	rows   []scanFunc
	index  int
	err    error
	closed bool
}

func (rows *fakeRows) Close()                                       { rows.closed = true }
func (rows *fakeRows) Err() error                                   { return rows.err }
func (rows *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("SELECT 0") }
func (rows *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (rows *fakeRows) Next() bool {
	if rows.index >= len(rows.rows) {
		rows.closed = true
		return false
	}
	rows.index++
	return true
}
func (rows *fakeRows) Scan(destinations ...any) error {
	return rows.rows[rows.index-1](destinations...)
}
func (rows *fakeRows) Values() ([]any, error) { return nil, nil }
func (rows *fakeRows) RawValues() [][]byte    { return nil }
func (rows *fakeRows) Conn() *pgx.Conn        { return nil }

func TestNewCatalogRepositoryRequiresPool(t *testing.T) {
	if _, err := NewCatalogRepository(nil); err == nil {
		t.Fatal("NewCatalogRepository(nil) error = nil, want error")
	}
}

func TestScanAssetNormalizesStorageValues(t *testing.T) {
	createdAt := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	updatedAt := createdAt.Add(time.Hour)
	id := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f")
	asset, err := scanAsset(scanFunc(func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = id
		*destinations[1].(*string) = "asset.crypto.btc"
		*destinations[2].(*string) = "CRYPTO"
		*destinations[3].(*string) = "BTC"
		*destinations[4].(*string) = "Bitcoin"
		*destinations[5].(*string) = "active"
		*destinations[6].(*[]byte) = []byte(`{"network":"bitcoin"}`)
		*destinations[7].(*time.Time) = createdAt
		*destinations[8].(*time.Time) = updatedAt
		return nil
	}))
	if err != nil {
		t.Fatalf("scanAsset() error = %v", err)
	}
	if asset.ID.String() != id.String() || asset.CreatedAt.Location() != time.UTC || string(asset.Metadata) != `{"network":"bitcoin"}` {
		t.Fatalf("scanAsset() = %#v", asset)
	}
}

func TestScanInstrumentAndProviderInstrumentOptionalValues(t *testing.T) {
	id := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f")
	assetID := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160e")
	quoteID := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160d")
	validFrom := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	priceScale := int16(8)
	quantityScale := int16(6)
	lotSize := decimal.RequireFromString("0.000001")
	minQuantity := decimal.RequireFromString("0.00001")
	instrument, err := scanInstrument(scanFunc(func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = id
		*destinations[1].(*string) = "instrument.bybit.spot.btc-usdt"
		*destinations[2].(*uuid.UUID) = assetID
		*destinations[3].(*string) = "BYBIT"
		*destinations[4].(*string) = "SPOT"
		*destinations[5].(*string) = "BTC-USDT"
		*destinations[6].(**uuid.UUID) = &quoteID
		*destinations[7].(*string) = "USDT"
		*destinations[8].(*string) = "UTC"
		*destinations[9].(**int16) = &priceScale
		*destinations[10].(**int16) = &quantityScale
		*destinations[11].(**decimal.Decimal) = &lotSize
		*destinations[12].(**decimal.Decimal) = &minQuantity
		*destinations[13].(*string) = "active"
		*destinations[14].(**time.Time) = &validFrom
		*destinations[15].(**time.Time) = nil
		*destinations[16].(*[]byte) = nil
		*destinations[17].(*time.Time) = validFrom
		*destinations[18].(*time.Time) = validFrom
		return nil
	}))
	if err != nil {
		t.Fatalf("scanInstrument() error = %v", err)
	}
	if instrument.QuoteAssetID == nil || instrument.ValidFrom == nil || instrument.ValidTo != nil || instrument.LotSize == nil || instrument.LotSize.String() != "0.000001" || string(instrument.Metadata) != "{}" {
		t.Fatalf("scanInstrument() = %#v", instrument)
	}

	mapping, err := scanProviderInstrument(scanFunc(func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = id
		*destinations[1].(*string) = "provider.bybit.spot.btcusdt"
		*destinations[2].(*uuid.UUID) = assetID
		*destinations[3].(*uuid.UUID) = quoteID
		*destinations[4].(*string) = "BTCUSDT"
		*destinations[5].(*string) = "spot"
		*destinations[6].(*[]byte) = []byte(`{"quote":true}`)
		*destinations[7].(*int16) = 10
		*destinations[8].(*bool) = true
		*destinations[9].(*bool) = true
		*destinations[10].(**time.Time) = nil
		*destinations[11].(**time.Time) = nil
		*destinations[12].(*[]byte) = nil
		*destinations[13].(*time.Time) = validFrom
		*destinations[14].(*time.Time) = validFrom
		return nil
	}))
	if err != nil {
		t.Fatalf("scanProviderInstrument() error = %v", err)
	}
	if mapping.Priority != 10 || !mapping.IsDefault || mapping.ValidFrom != nil || string(mapping.Metadata) != "{}" {
		t.Fatalf("scanProviderInstrument() = %#v", mapping)
	}
}

func TestScanFunctionsPropagateErrors(t *testing.T) {
	want := errors.New("scan")
	row := scanFunc(func(...any) error { return want })
	if _, err := scanAsset(row); !errors.Is(err, want) {
		t.Fatalf("scanAsset() error = %v, want %v", err, want)
	}
	if _, err := scanInstrument(row); !errors.Is(err, want) {
		t.Fatalf("scanInstrument() error = %v, want %v", err, want)
	}
	if _, err := scanProvider(row); !errors.Is(err, want) {
		t.Fatalf("scanProvider() error = %v, want %v", err, want)
	}
	if _, err := scanProviderInstrument(row); !errors.Is(err, want) {
		t.Fatalf("scanProviderInstrument() error = %v, want %v", err, want)
	}
}

func TestCatalogHelpers(t *testing.T) {
	if got := jsonValue(nil); got != "{}" {
		t.Fatalf("jsonValue(nil) = %q", got)
	}
	if got := string(copyJSON(nil)); got != "{}" {
		t.Fatalf("copyJSON(nil) = %q", got)
	}
	if got := optionalIDFromDatabase(nil); got != nil {
		t.Fatalf("optionalIDFromDatabase(nil) = %v", got)
	}
	if got := optionalTimeFromDatabase(nil); got != nil {
		t.Fatalf("optionalTimeFromDatabase(nil) = %v", got)
	}
	if got := optionalTimeToDatabase(nil); got != nil {
		t.Fatalf("optionalTimeToDatabase(nil) = %v", got)
	}
	if got := IDToDatabase(domain.ID{}); got != uuid.Nil {
		t.Fatalf("IDToDatabase(zero) = %s", got)
	}
}

func TestCatalogRepositoryQueriesAndWrites(t *testing.T) {
	assetID := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f")
	providerID := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160e")
	instrumentID := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160d")
	now := time.Now().UTC()
	assetRow := scanFunc(func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = assetID
		*destinations[1].(*string) = "asset.crypto.btc"
		*destinations[2].(*string) = "CRYPTO"
		*destinations[3].(*string) = "BTC"
		*destinations[4].(*string) = "Bitcoin"
		*destinations[5].(*string) = "active"
		*destinations[6].(*[]byte) = []byte(`{}`)
		*destinations[7].(*time.Time) = now
		*destinations[8].(*time.Time) = now
		return nil
	})
	providerRow := scanFunc(func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = providerID
		*destinations[1].(*string) = "bybit"
		*destinations[2].(*string) = "Bybit"
		*destinations[3].(*string) = "EXCHANGE"
		*destinations[4].(*string) = "active"
		*destinations[5].(*[]byte) = []byte(`{}`)
		*destinations[6].(*time.Time) = now
		*destinations[7].(*time.Time) = now
		return nil
	})
	mappingRow := providerInstrumentRow(providerID, instrumentID, now)
	rows := &fakeRows{rows: []scanFunc{mappingRow}}
	database := fakeCatalogDatabase{
		exec: func(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
			if query == "" {
				t.Fatal("Exec query is empty")
			}
			return pgconn.NewCommandTag("INSERT 0 1"), nil
		},
		queryRow: func(_ context.Context, query string, args ...any) pgx.Row {
			if len(args) != 1 {
				t.Fatalf("QueryRow args = %d, want 1", len(args))
			}
			if args[0] == "asset.crypto.btc" {
				return assetRow
			}
			return providerRow
		},
		query: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return rows, nil },
	}
	repository, err := newCatalogRepository(database)
	if err != nil {
		t.Fatalf("newCatalogRepository() error = %v", err)
	}
	if _, err := repository.FindAssetByCode(context.Background(), "asset.crypto.btc"); err != nil {
		t.Fatalf("FindAssetByCode() error = %v", err)
	}
	if _, err := repository.FindProviderByCode(context.Background(), "bybit"); err != nil {
		t.Fatalf("FindProviderByCode() error = %v", err)
	}
	provider := domain.Provider{ID: domain.IDFromUUID(providerID), Code: testCode(t, "bybit"), Name: "Bybit", ProviderType: "EXCHANGE", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateProvider(context.Background(), provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	mapping := domain.ProviderInstrument{ID: domain.IDFromUUID(assetID), Code: testCode(t, "provider.bybit.spot.btcusdt"), ProviderID: domain.IDFromUUID(providerID), InstrumentID: domain.IDFromUUID(instrumentID), ExternalSymbol: "BTCUSDT", ProviderMarket: "spot", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateProviderInstrument(context.Background(), mapping); err != nil {
		t.Fatalf("CreateProviderInstrument() error = %v", err)
	}
	loaded, err := repository.ListActiveProviderInstruments(context.Background(), mapping.InstrumentID)
	if err != nil || len(loaded) != 1 || !rows.closed {
		t.Fatalf("ListActiveProviderInstruments() = (%#v, %v, closed=%t)", loaded, err, rows.closed)
	}
}

func TestCatalogRepositoryMapsErrorsAndRejectsInvalidIDs(t *testing.T) {
	database := fakeCatalogDatabase{
		exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, &pgconn.PgError{Code: "23505"}
		},
		queryRow: func(context.Context, string, ...any) pgx.Row {
			return scanFunc(func(...any) error { return sql.ErrNoRows })
		},
		query: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, &pgconn.PgError{Code: "08006"} },
	}
	repository, err := newCatalogRepository(database)
	if err != nil {
		t.Fatalf("newCatalogRepository() error = %v", err)
	}
	if _, err := repository.FindAssetByCode(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("FindAssetByCode() error = %v", err)
	}
	if _, err := repository.FindAssetByID(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("FindAssetByID(zero) error = %v", err)
	}
	if err := repository.CreateProvider(context.Background(), domain.Provider{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CreateProvider(zero) error = %v", err)
	}
	now := time.Now().UTC()
	provider := domain.Provider{ID: domain.IDFromUUID(uuid.New()), Code: testCode(t, "bybit"), Name: "Bybit", ProviderType: "EXCHANGE", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateProvider(context.Background(), provider); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateProvider(conflict) error = %v", err)
	}
	if err := repository.CreateProviderInstrument(context.Background(), domain.ProviderInstrument{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("CreateProviderInstrument(zero) error = %v", err)
	}
	if _, err := repository.ListActiveProviderInstruments(context.Background(), domain.ID{}); !errors.Is(err, domain.ErrInvalidData) {
		t.Fatalf("ListActiveProviderInstruments(zero) error = %v", err)
	}
	if _, err := repository.ListActiveProviderInstruments(context.Background(), domain.IDFromUUID(uuid.New())); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListActiveProviderInstruments(unavailable) error = %v", err)
	}
}

func TestCatalogRepositoryFindsAssetByID(t *testing.T) {
	id := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f")
	now := time.Now().UTC()
	database := fakeCatalogDatabase{queryRow: func(_ context.Context, query string, args ...any) pgx.Row {
		if !strings.Contains(query, "WHERE id = $1") || len(args) != 1 || args[0] != id {
			t.Fatalf("FindAssetByID query=%q args=%#v", query, args)
		}
		return scanFunc(func(destinations ...any) error {
			*destinations[0].(*uuid.UUID) = id
			*destinations[1].(*string) = "asset.crypto.btc"
			*destinations[2].(*string) = "CRYPTO"
			*destinations[3].(*string) = "BTC"
			*destinations[4].(*string) = "Bitcoin"
			*destinations[5].(*string) = "active"
			*destinations[6].(*[]byte) = []byte(`{}`)
			*destinations[7].(*time.Time) = now
			*destinations[8].(*time.Time) = now
			return nil
		})
	}}
	repository, _ := newCatalogRepository(database)
	asset, err := repository.FindAssetByID(context.Background(), domain.IDFromUUID(id))
	if err != nil || asset.ID.UUID() != id {
		t.Fatalf("FindAssetByID() = (%#v, %v)", asset, err)
	}
}

func TestCatalogRepositoryListsInstrumentProviderOptions(t *testing.T) {
	now := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC)
	assetID := domain.IDFromUUID(uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f"))
	instrumentID := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160d")
	mappingID := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160c")
	after := testCode(t, "instrument.crypto.btc-usdc")
	rows := &fakeRows{rows: []scanFunc{func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = instrumentID
		*destinations[1].(*string) = "instrument.crypto.btc-usdt"
		*destinations[2].(*string) = "BTC/USDT"
		*destinations[3].(*uuid.UUID) = mappingID
		*destinations[4].(*string) = "provider.bybit.btc-usdt"
		*destinations[5].(*string) = "bybit"
		*destinations[6].(*string) = "Bybit"
		*destinations[7].(*bool) = true
		*destinations[8].(*int16) = 10
		*destinations[9].(*[]byte) = []byte(`{"historical":true,"intervals":["1h","1d"]}`)
		return nil
	}}}
	database := fakeCatalogDatabase{
		query: func(_ context.Context, query string, args ...any) (pgx.Rows, error) {
			if !strings.Contains(query, "WITH selected_instruments") || !strings.Contains(query, "provider.status IN ('active', 'degraded')") || !strings.Contains(query, "mapping.valid_to > $2") {
				t.Fatalf("query does not contain active/effective projection: %s", query)
			}
			if len(args) != 4 || args[0] != assetID.UUID() || args[1] != now || args[2] != after.String() || args[3] != 11 {
				t.Fatalf("query args = %#v", args)
			}
			return rows, nil
		},
	}
	repository, _ := newCatalogRepository(database)
	records, err := repository.ListInstrumentProviderOptions(context.Background(), application.InstrumentOptionsFilter{
		AssetID: assetID, AfterInstrumentCode: &after, EffectiveAt: now, InstrumentLimit: 11,
	})
	if err != nil {
		t.Fatalf("ListInstrumentProviderOptions() error = %v", err)
	}
	if len(records) != 1 || records[0].InstrumentID.UUID() != instrumentID || records[0].ProviderInstrumentID.UUID() != mappingID || records[0].ProviderCode.String() != "bybit" || len(records[0].Capabilities.Intervals) != 2 || !rows.closed {
		t.Fatalf("ListInstrumentProviderOptions() = %#v, closed=%t", records, rows.closed)
	}
}

func TestCatalogRepositoryInstrumentProviderOptionsFailures(t *testing.T) {
	now := time.Now().UTC()
	assetID := domain.IDFromUUID(uuid.New())
	repository, _ := newCatalogRepository(fakeCatalogDatabase{})
	for _, filter := range []application.InstrumentOptionsFilter{
		{},
		{AssetID: assetID, EffectiveAt: now, InstrumentLimit: 0},
		{AssetID: assetID, EffectiveAt: now, InstrumentLimit: application.MaximumInstrumentOptionsPageSize + 2},
	} {
		if _, err := repository.ListInstrumentProviderOptions(context.Background(), filter); !errors.Is(err, domain.ErrInvalidData) {
			t.Fatalf("ListInstrumentProviderOptions(%#v) error = %v", filter, err)
		}
	}

	databaseError := &pgconn.PgError{Code: "08006"}
	repository, _ = newCatalogRepository(fakeCatalogDatabase{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return nil, databaseError
	}})
	if _, err := repository.ListInstrumentProviderOptions(context.Background(), application.InstrumentOptionsFilter{AssetID: assetID, EffectiveAt: now, InstrumentLimit: 10}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListInstrumentProviderOptions(query error) = %v", err)
	}

	badRows := &fakeRows{rows: []scanFunc{func(...any) error { return errors.New("scan") }}}
	repository, _ = newCatalogRepository(fakeCatalogDatabase{query: func(context.Context, string, ...any) (pgx.Rows, error) { return badRows, nil }})
	if _, err := repository.ListInstrumentProviderOptions(context.Background(), application.InstrumentOptionsFilter{AssetID: assetID, EffectiveAt: now, InstrumentLimit: 10}); err == nil || !strings.Contains(err.Error(), "scan instrument provider option") {
		t.Fatalf("ListInstrumentProviderOptions(scan error) = %v", err)
	}

	iterationRows := &fakeRows{err: databaseError}
	repository, _ = newCatalogRepository(fakeCatalogDatabase{query: func(context.Context, string, ...any) (pgx.Rows, error) { return iterationRows, nil }})
	if _, err := repository.ListInstrumentProviderOptions(context.Background(), application.InstrumentOptionsFilter{AssetID: assetID, EffectiveAt: now, InstrumentLimit: 10}); !errors.Is(err, domain.ErrDatabaseUnavailable) {
		t.Fatalf("ListInstrumentProviderOptions(iteration error) = %v", err)
	}
}

func testCode(t *testing.T, value string) domain.Code {
	t.Helper()
	code, err := domain.ParseCode(value)
	if err != nil {
		t.Fatalf("ParseCode(%q) error = %v", value, err)
	}
	return code
}

func providerInstrumentRow(providerID, instrumentID uuid.UUID, now time.Time) scanFunc {
	return func(destinations ...any) error {
		*destinations[0].(*uuid.UUID) = uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160c")
		*destinations[1].(*string) = "provider.bybit.spot.btcusdt"
		*destinations[2].(*uuid.UUID) = providerID
		*destinations[3].(*uuid.UUID) = instrumentID
		*destinations[4].(*string) = "BTCUSDT"
		*destinations[5].(*string) = "spot"
		*destinations[6].(*[]byte) = []byte(`{}`)
		*destinations[7].(*int16) = 1
		*destinations[8].(*bool) = true
		*destinations[9].(*bool) = true
		*destinations[10].(**time.Time) = nil
		*destinations[11].(**time.Time) = nil
		*destinations[12].(*[]byte) = []byte(`{}`)
		*destinations[13].(*time.Time) = now
		*destinations[14].(*time.Time) = now
		return nil
	}
}
