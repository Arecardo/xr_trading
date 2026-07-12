// Package readiness verifies database dependencies required by the HTTP service.
package readiness

import (
	"context"
	"errors"
	"fmt"

	"xr-trading/market-info-service/internal/observability"
)

const migrationVersionQuery = `
SELECT version_id
FROM market_data.schema_migrations
WHERE is_applied
ORDER BY version_id DESC
LIMIT 1`

// DB is the minimal database surface needed by the readiness checker.
type DB interface {
	Ping(context.Context) error
	QueryRow(context.Context, string, ...any) Row
}

// Row is the minimal pgx row surface needed by the readiness checker.
type Row interface {
	Scan(...any) error
}

// Checker verifies PostgreSQL availability and migration compatibility.
type Checker struct {
	db            DB
	latestVersion int64
}

// New constructs a database readiness checker.
func New(db DB, latestVersion int64) (*Checker, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if latestVersion < 1 {
		return nil, errors.New("latest migration version must be positive")
	}
	return &Checker{db: db, latestVersion: latestVersion}, nil
}

// Check returns nil only when PostgreSQL is reachable and the schema version
// exactly matches the version compiled into this service.
func (c *Checker) Check(ctx context.Context) error {
	if err := c.db.Ping(ctx); err != nil {
		return fmt.Errorf("%w: %v", observability.ErrDatabaseUnavailable, err)
	}

	var appliedVersion int64
	if err := c.db.QueryRow(ctx, migrationVersionQuery).Scan(&appliedVersion); err != nil {
		return fmt.Errorf("%w: read schema migration version: %v", observability.ErrMigrationIncompatible, err)
	}
	if appliedVersion != c.latestVersion {
		return fmt.Errorf("%w: database version %d, binary version %d",
			observability.ErrMigrationIncompatible,
			appliedVersion,
			c.latestVersion,
		)
	}
	return nil
}
