// Package migrations embeds and executes market information database migrations.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

const (
	// VersionTable is owned by the market-data migration role.
	VersionTable = "market_data.schema_migrations"
	// LatestVersion is the newest migration compiled into this build.
	LatestVersion int64 = 7
)

//go:embed sql/*.sql
var embedded embed.FS

// SchemaExecer is the minimum database capability needed before goose can
// create its schema-qualified version table.
type SchemaExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type migrationApplier interface {
	Up(ctx context.Context) ([]*goose.MigrationResult, error)
}

type providerFactory func() (migrationApplier, error)

// Files returns the embedded migration filesystem rooted at the SQL files.
func Files() (fs.FS, error) {
	return fs.Sub(embedded, "sql")
}

// PrepareSchema creates the service-owned schema. It must be called with the
// migration identity, never with the runtime identity.
func PrepareSchema(ctx context.Context, db SchemaExecer) error {
	if db == nil {
		return errors.New("database is required")
	}
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS market_data"); err != nil {
		return fmt.Errorf("prepare market_data schema: %w", err)
	}
	return nil
}

// NewProvider constructs a goose provider over the migrations embedded in the
// binary. Constructing it does not connect to or modify the database.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	migrationFS, err := Files()
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrationFS,
		goose.WithTableName(VersionTable),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return nil, fmt.Errorf("create migration provider: %w", err)
	}
	return provider, nil
}

// Up prepares the service schema and applies all pending migrations.
func Up(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("database is required")
	}
	return apply(ctx, db, func() (migrationApplier, error) {
		return NewProvider(db)
	})
}

func apply(ctx context.Context, db SchemaExecer, factory providerFactory) error {
	if err := PrepareSchema(ctx, db); err != nil {
		return err
	}
	provider, err := factory()
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
