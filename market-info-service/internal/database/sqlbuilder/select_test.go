package sqlbuilder

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSelectBuildsWhereAndQuery(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	query, args, err := Select("instrument_id", "close_price").
		From("market_data.market_bars").
		Where(Eq("provider_code", "bybit")).
		And(Eq("instrument_code", "BTC-USDT-SPOT")).
		And(Gte("bar_time", at)).
		And(Lte("bar_time", at.Add(time.Hour))).
		OrderBy("bar_time DESC").
		Limit(100).
		Offset(10).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	wantQuery := "SELECT instrument_id, close_price FROM market_data.market_bars WHERE provider_code = $1 AND instrument_code = $2 AND bar_time >= $3 AND bar_time <= $4 ORDER BY bar_time DESC LIMIT 100 OFFSET 10"
	if query != wantQuery {
		t.Fatalf("query = %q, want %q", query, wantQuery)
	}
	wantArgs := []any{"bybit", "BTC-USDT-SPOT", at, at.Add(time.Hour)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestSelectBuildsRawPredicateWithMultipleArgs(t *testing.T) {
	t.Parallel()

	query, args, err := Select("id").
		From("market_data.provider_instruments").
		Where(Raw("(provider_code = ? OR provider_code = ?)", "bybit", "coingecko")).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if query != "SELECT id FROM market_data.provider_instruments WHERE (provider_code = $1 OR provider_code = $2)" {
		t.Fatalf("query = %q", query)
	}
	if !reflect.DeepEqual(args, []any{"bybit", "coingecko"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestSelectRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		build     func() (string, []any, error)
		wantError string
	}{
		{
			name:      "missing columns",
			build:     func() (string, []any, error) { return Select().From("market_data.providers").Build() },
			wantError: "select columns are required",
		},
		{
			name:      "missing table",
			build:     func() (string, []any, error) { return Select("id").Build() },
			wantError: "from table is required",
		},
		{
			name: "empty condition",
			build: func() (string, []any, error) {
				return Select("id").From("market_data.providers").Where(Condition{}).Build()
			},
			wantError: "condition SQL is required",
		},
		{
			name:      "negative limit",
			build:     func() (string, []any, error) { return Select("id").From("market_data.providers").Limit(-1).Build() },
			wantError: "limit must not be negative",
		},
		{
			name:      "negative offset",
			build:     func() (string, []any, error) { return Select("id").From("market_data.providers").Offset(-1).Build() },
			wantError: "offset must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := tt.build()
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Build() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}
