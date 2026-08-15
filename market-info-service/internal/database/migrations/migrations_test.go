package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

type execerStub struct {
	query string
	err   error
}

type applierStub struct {
	err error
}

func (s applierStub) Up(context.Context) ([]*goose.MigrationResult, error) {
	return nil, s.err
}

func (s *execerStub) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	s.query = query
	return nil, s.err
}

func TestFilesContainsPrerequisiteMigration(t *testing.T) {
	t.Parallel()

	migrationFS, err := Files()
	if err != nil {
		t.Fatalf("Files() error = %v", err)
	}
	contents, err := fs.ReadFile(migrationFS, "00001_verify_database_prerequisites.sql")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, expected := range []string{"server_version_num", "core.assets", "core.instrument_aliases"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("migration does not contain %q", expected)
		}
	}
}

func TestFilesContainContinuousMigrations(t *testing.T) {
	t.Parallel()

	migrationFS, err := Files()
	if err != nil {
		t.Fatalf("Files() error = %v", err)
	}
	entries, err := fs.ReadDir(migrationFS, ".")
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if int64(len(entries)) != LatestVersion {
		t.Fatalf("migration count = %d, LatestVersion = %d", len(entries), LatestVersion)
	}
	for index, entry := range entries {
		prefix := fmt.Sprintf("%05d_", index+1)
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			t.Fatalf("migration %d = %q, want prefix %q", index, entry.Name(), prefix)
		}
	}
}

func TestInitialMigrationsContainDesignedObjects(t *testing.T) {
	t.Parallel()

	migrationFS, err := Files()
	if err != nil {
		t.Fatalf("Files() error = %v", err)
	}
	var combined strings.Builder
	for _, name := range []string{
		"00002_create_provider_catalog.sql",
		"00003_create_market_data_tables.sql",
		"00004_create_ingestion_tables.sql",
	} {
		contents, readErr := fs.ReadFile(migrationFS, name)
		if readErr != nil {
			t.Fatalf("ReadFile(%q) error = %v", name, readErr)
		}
		combined.Write(contents)
	}
	for _, object := range []string{
		"market_data.providers",
		"market_data.provider_instruments",
		"market_data.collection_subscriptions",
		"market_data.market_bars",
		"market_data.latest_quotes",
		"market_data.ingestion_runs",
		"market_data.ingestion_tasks",
		"market_data.ingestion_checkpoints",
		"market_data.data_quality_issues",
		"NULLS NOT DISTINCT",
	} {
		if !strings.Contains(combined.String(), object) {
			t.Fatalf("initial migrations do not contain %q", object)
		}
	}
}

func TestRuntimeGrantMigrationUsesDedicatedRolesAndNoDeletePrivilege(t *testing.T) {
	t.Parallel()

	migrationFS, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := fs.ReadFile(migrationFS, "00005_grant_runtime_permissions.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, expected := range []string{"xr_market_data_owner", "xr_market_data_runtime", "GRANT USAGE", "GRANT SELECT, INSERT, UPDATE", "ALTER DEFAULT PRIVILEGES"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("runtime grant migration does not contain %q", expected)
		}
	}
	if strings.Contains(text, "GRANT DELETE") || strings.Contains(text, "GRANT ALL") {
		t.Fatalf("runtime grant migration is over-privileged: %s", text)
	}
}

func TestCoinGeckoFXSeedMigrationContainsExpectedObjects(t *testing.T) {
	t.Parallel()

	migrationFS, err := Files()
	if err != nil {
		t.Fatalf("Files() error = %v", err)
	}
	contents, err := fs.ReadFile(migrationFS, "00007_seed_coingecko_fx_provider.sql")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(contents)
	for _, expected := range []string{
		"market_data.providers", "'coingecko'", "market_data.provider_instruments",
		"market_data.collection_subscriptions", "'1d'", "enabled",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("migration does not contain %q", expected)
		}
	}
	// Unlike every other seeded collection_subscriptions row, this one must
	// ship already enabled: FX collection scope is driven by "does the asset
	// universe contain a non-base-currency holding", not by a
	// backend-initiated subscription-sync call (RM0 DEC-006).
	if !strings.Contains(text, "true, 100, 300, NULL") {
		t.Fatalf("coingecko subscription is not seeded already-enabled: %s", text)
	}
}

func TestPrepareSchema(t *testing.T) {
	t.Parallel()

	stub := &execerStub{}
	if err := PrepareSchema(context.Background(), stub); err != nil {
		t.Fatalf("PrepareSchema() error = %v", err)
	}
	if stub.query != "CREATE SCHEMA IF NOT EXISTS market_data" {
		t.Fatalf("query = %q", stub.query)
	}
}

func TestPrepareSchemaRejectsInvalidDatabase(t *testing.T) {
	t.Parallel()

	if err := PrepareSchema(context.Background(), nil); err == nil {
		t.Fatal("PrepareSchema(nil) error = nil")
	}
	want := errors.New("denied")
	err := PrepareSchema(context.Background(), &execerStub{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("PrepareSchema() error = %v, want wrapping %v", err, want)
	}
}

func TestNewProvider(t *testing.T) {
	t.Parallel()

	if _, err := NewProvider(nil); err == nil {
		t.Fatal("NewProvider(nil) error = nil")
	}
	db, err := sql.Open("pgx", "postgres://unused")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := NewProvider(db); err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
}

func TestUpRejectsNilDatabase(t *testing.T) {
	t.Parallel()

	if err := Up(context.Background(), nil); err == nil {
		t.Fatal("Up(nil) error = nil")
	}
}

func TestApply(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prepareErr  error
		providerErr error
		applyErr    error
		wantError   error
	}{
		{name: "success"},
		{name: "prepare", prepareErr: errors.New("prepare"), wantError: errors.New("prepare")},
		{name: "provider", providerErr: errors.New("provider"), wantError: errors.New("provider")},
		{name: "up", applyErr: errors.New("up"), wantError: errors.New("up")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stub := &execerStub{err: test.prepareErr}
			err := apply(context.Background(), stub, func() (migrationApplier, error) {
				if test.providerErr != nil {
					return nil, test.providerErr
				}
				return applierStub{err: test.applyErr}, nil
			})
			if test.wantError == nil && err != nil {
				t.Fatalf("apply() error = %v", err)
			}
			if test.wantError != nil && (err == nil || !strings.Contains(err.Error(), test.wantError.Error())) {
				t.Fatalf("apply() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}
