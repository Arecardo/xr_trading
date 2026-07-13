package readiness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"xr-trading/market-info-service/internal/observability"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		db            DB
		latestVersion int64
		wantErr       string
	}{
		{
			name:          "valid",
			db:            &stubDB{},
			latestVersion: 4,
		},
		{
			name:          "missing database",
			latestVersion: 4,
			wantErr:       "database is required",
		},
		{
			name:          "invalid version",
			db:            &stubDB{},
			latestVersion: 0,
			wantErr:       "latest migration version must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker, err := New(tt.db, tt.latestVersion)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("New() error = %v", err)
				}
				if checker == nil {
					t.Fatal("New() returned nil checker")
				}
				return
			}
			if err == nil {
				t.Fatal("New() error = nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckerCheck(t *testing.T) {
	t.Parallel()

	pingErr := errors.New("connection refused")
	queryErr := errors.New("relation does not exist")

	tests := []struct {
		name        string
		db          *stubDB
		wantErrIs   error
		wantErrText string
	}{
		{
			name: "ready",
			db:   &stubDB{version: 4},
		},
		{
			name:        "ping failure",
			db:          &stubDB{pingErr: pingErr},
			wantErrIs:   observability.ErrDatabaseUnavailable,
			wantErrText: "connection refused",
		},
		{
			name:        "missing migration table",
			db:          &stubDB{rowErr: queryErr},
			wantErrIs:   observability.ErrMigrationIncompatible,
			wantErrText: "read schema migration version",
		},
		{
			name:        "database behind binary",
			db:          &stubDB{version: 3},
			wantErrIs:   observability.ErrMigrationIncompatible,
			wantErrText: "database version 3, binary version 4",
		},
		{
			name:        "database ahead of binary",
			db:          &stubDB{version: 5},
			wantErrIs:   observability.ErrMigrationIncompatible,
			wantErrText: "database version 5, binary version 4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker, err := New(tt.db, 4)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			err = checker.Check(context.Background())
			if tt.wantErrIs == nil {
				if err != nil {
					t.Fatalf("Check() error = %v", err)
				}
				if tt.db.query != migrationVersionQuery {
					t.Fatalf("query = %q, want %q", tt.db.query, migrationVersionQuery)
				}
				return
			}
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("Check() error = %v, want errors.Is %v", err, tt.wantErrIs)
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("Check() error = %q, want substring %q", err, tt.wantErrText)
			}
		})
	}
}

type stubDB struct {
	pingErr error
	rowErr  error
	version int64
	query   string
}

func (s *stubDB) Ping(context.Context) error {
	return s.pingErr
}

func (s *stubDB) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	s.query = query
	return stubRow{version: s.version, err: s.rowErr}
}

type stubRow struct {
	version int64
	err     error
}

func (s stubRow) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	target, ok := dest[0].(*int64)
	if !ok {
		return errors.New("destination must be *int64")
	}
	*target = s.version
	return nil
}
