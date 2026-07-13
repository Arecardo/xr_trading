package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"xr-trading/market-info-service/internal/database/migrations"
)

const migrationTimeout = 2 * time.Minute

type openDatabase func(driverName, dataSourceName string) (*sql.DB, error)
type migrateDatabase func(context.Context, *sql.DB) error
type lookupEnv func(string) (string, bool)

func main() {
	os.Exit(entrypoint())
}

func entrypoint() int {
	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()
	if err := run(ctx, os.LookupEnv, sql.Open, migrations.Up); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, lookup lookupEnv, open openDatabase, migrate migrateDatabase) error {
	if lookup == nil || open == nil || migrate == nil {
		return errors.New("migration dependencies are required")
	}
	databaseURL, ok := lookup("MARKET_INFO_MIGRATION_DATABASE_URL")
	if !ok || databaseURL == "" {
		return errors.New("MARKET_INFO_MIGRATION_DATABASE_URL is required")
	}
	db, err := open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}
