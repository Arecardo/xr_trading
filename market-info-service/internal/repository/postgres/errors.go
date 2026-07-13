package postgres

import (
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xr-trading/market-info-service/internal/domain"
)

// MapError classifies PostgreSQL and driver failures into stable domain errors.
// The original error remains available through errors.Is and errors.As.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return errors.Join(domain.ErrNotFound, err)
	}

	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) {
		return err
	}

	switch databaseError.Code {
	case "23505":
		return errors.Join(domain.ErrConflict, err)
	case "23503":
		return errors.Join(domain.ErrReferenceViolation, err)
	case "23514", "22001", "22003", "22007", "22P02":
		return errors.Join(domain.ErrInvalidData, err)
	case "40001", "40P01":
		return errors.Join(domain.ErrRetryable, err)
	}
	if len(databaseError.Code) >= 2 && databaseError.Code[:2] == "08" {
		return errors.Join(domain.ErrDatabaseUnavailable, err)
	}
	return err
}
