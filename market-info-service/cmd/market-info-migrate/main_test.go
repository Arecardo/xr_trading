package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestRun(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("sentinel")
	tests := []struct {
		name      string
		lookup    lookupEnv
		open      openDatabase
		migrate   migrateDatabase
		wantError string
	}{
		{name: "missing dependencies", wantError: "dependencies"},
		{
			name:      "missing URL",
			lookup:    func(string) (string, bool) { return "", false },
			open:      sql.Open,
			migrate:   func(context.Context, *sql.DB) error { return nil },
			wantError: "MIGRATION_DATABASE_URL",
		},
		{
			name:   "open error",
			lookup: func(string) (string, bool) { return "postgres://test", true },
			open: func(string, string) (*sql.DB, error) {
				return nil, sentinel
			},
			migrate:   func(context.Context, *sql.DB) error { return nil },
			wantError: "open database",
		},
		{
			name:   "migration error",
			lookup: func(string) (string, bool) { return "postgres://test", true },
			open:   sql.Open,
			migrate: func(context.Context, *sql.DB) error {
				return sentinel
			},
			wantError: "migrate database",
		},
		{
			name:    "success",
			lookup:  func(string) (string, bool) { return "postgres://test", true },
			open:    sql.Open,
			migrate: func(context.Context, *sql.DB) error { return nil },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := run(context.Background(), test.lookup, test.open, test.migrate)
			if test.wantError == "" && err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("run() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestEntrypointFailsWithoutDatabaseURL(t *testing.T) {
	t.Setenv("MARKET_INFO_MIGRATION_DATABASE_URL", "")
	if code := entrypoint(); code != 1 {
		t.Fatalf("entrypoint() = %d, want 1", code)
	}
}
