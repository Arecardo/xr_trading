package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
)

func TestIDDatabaseConversion(t *testing.T) {
	databaseID := uuid.MustParse("019f1452-90f7-7992-a87a-ca272789160f")
	got := IDFromDatabase(databaseID)
	if got.String() != databaseID.String() {
		t.Fatalf("IDFromDatabase() = %s, want %s", got, databaseID)
	}
	if roundTrip := IDToDatabase(got); roundTrip != databaseID {
		t.Fatalf("IDToDatabase() = %s, want %s", roundTrip, databaseID)
	}
}

func TestTimeToDatabase(t *testing.T) {
	value := time.Date(2026, time.July, 12, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	got := TimeToDatabase(value)
	if got.Location() != time.UTC || got.Hour() != 0 {
		t.Fatalf("TimeToDatabase() = %v, want 00:00 UTC", got)
	}
}

func TestDecimalDatabaseConversion(t *testing.T) {
	value := decimal.RequireFromString("1.230000000000000001")
	if got := DecimalToDatabase(value); got != "1.230000000000000001" {
		t.Fatalf("DecimalToDatabase() = %q", got)
	}

	for _, input := range []any{"1.5", []byte("2.5"), value} {
		got, err := DecimalFromDatabase(input)
		if err != nil {
			t.Fatalf("DecimalFromDatabase(%T) error = %v", input, err)
		}
		if got.IsZero() {
			t.Fatalf("DecimalFromDatabase(%T) = zero", input)
		}
	}
	if _, err := DecimalFromDatabase(1.5); err == nil {
		t.Fatal("DecimalFromDatabase(float64) error = nil, want error")
	}
	if _, err := domain.ParseDecimal("bad"); err == nil {
		t.Fatal("ParseDecimal(bad) error = nil, want error")
	}
}
