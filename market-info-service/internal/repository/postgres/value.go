// Package postgres contains PostgreSQL-specific repository support.
package postgres

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"xr-trading/market-info-service/internal/domain"
)

// IDFromDatabase converts PostgreSQL's UUID representation into a domain ID.
func IDFromDatabase(value uuid.UUID) domain.ID {
	return domain.IDFromUUID(value)
}

// IDToDatabase converts a domain ID into PostgreSQL's UUID representation.
func IDToDatabase(value domain.ID) uuid.UUID {
	return value.UUID()
}

// TimeToDatabase normalizes timestamps to UTC while preserving zero values.
func TimeToDatabase(value time.Time) time.Time {
	return domain.UTC(value)
}

// DecimalToDatabase returns the exact decimal text accepted by PostgreSQL
// numeric columns.
func DecimalToDatabase(value decimal.Decimal) string {
	return value.String()
}

// DecimalFromDatabase converts a value scanned from a PostgreSQL numeric
// column. pgx may provide numeric text as string or []byte depending on codec.
func DecimalFromDatabase(value any) (decimal.Decimal, error) {
	switch typed := value.(type) {
	case string:
		parsed, err := domain.ParseDecimal(typed)
		return parsed.Exact(), err
	case []byte:
		parsed, err := domain.ParseDecimal(string(typed))
		return parsed.Exact(), err
	case decimal.Decimal:
		return typed, nil
	default:
		return decimal.Zero, fmt.Errorf("convert PostgreSQL numeric %T: %w", value, domain.ErrInvalidData)
	}
}
