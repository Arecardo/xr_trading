package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xr-trading/market-info-service/internal/domain"
)

// DataQualityIssueRepository stores quality issue lifecycle state.
type DataQualityIssueRepository struct{ database marketDataDatabase }

// NewDataQualityIssueRepository constructs a quality issue repository over pool.
func NewDataQualityIssueRepository(pool *pgxpool.Pool) (*DataQualityIssueRepository, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return newDataQualityIssueRepository(pgxMarketDataDatabase{pool: pool})
}

func newDataQualityIssueRepository(database marketDataDatabase) (*DataQualityIssueRepository, error) {
	if database == nil {
		return nil, errors.New("PostgreSQL database is required")
	}
	return &DataQualityIssueRepository{database: database}, nil
}

// OpenIssue creates an open quality issue. It returns false when an equivalent
// open or acknowledged issue already exists, including when nullable dimensions match.
func (repository *DataQualityIssueRepository) OpenIssue(ctx context.Context, issue domain.DataQualityIssue) (bool, error) {
	return openQualityIssue(ctx, repository.database, issue)
}

func openQualityIssue(ctx context.Context, database catalogDatabase, issue domain.DataQualityIssue) (bool, error) {
	if issue.ID.IsZero() || issue.InstrumentID.IsZero() || issue.RuleCode == "" || issue.DetectedAt.IsZero() || issue.CreatedAt.IsZero() || issue.UpdatedAt.IsZero() {
		return false, fmt.Errorf("open quality issue: %w", domain.ErrInvalidData)
	}
	var id uuid.UUID
	err := database.QueryRow(ctx, `INSERT INTO market_data.data_quality_issues (id, instrument_id, provider_instrument_id, interval, open_time, rule_code, severity, status, summary, details, detected_at, resolved_at, resolution_note, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, 'open', $8, $9::jsonb, $10, NULL, NULL, $11, $12) ON CONFLICT (instrument_id, provider_instrument_id, interval, open_time, rule_code) WHERE status IN ('open', 'acknowledged') DO NOTHING RETURNING id`, IDToDatabase(issue.ID), IDToDatabase(issue.InstrumentID), optionalIDToDatabase(issue.ProviderInstrumentID), optionalString(issue.Interval), optionalTimeToDatabase(issue.OpenTime), issue.RuleCode, issue.Severity, issue.Summary, jsonValue(issue.Details), TimeToDatabase(issue.DetectedAt), TimeToDatabase(issue.CreatedAt), TimeToDatabase(issue.UpdatedAt)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open quality issue: %w", MapError(err))
	}
	return true, nil
}

// AcknowledgeIssue transitions an open issue to acknowledged.
func (repository *DataQualityIssueRepository) AcknowledgeIssue(ctx context.Context, id domain.ID, updatedAt time.Time) error {
	return repository.transitionIssue(ctx, id, []string{"open"}, "acknowledged", nil, nil, updatedAt)
}

// ResolveIssue transitions an open or acknowledged issue to resolved.
func (repository *DataQualityIssueRepository) ResolveIssue(ctx context.Context, id domain.ID, note string, updatedAt time.Time) error {
	return repository.transitionIssue(ctx, id, []string{"open", "acknowledged"}, "resolved", pointerToString(note), &updatedAt, updatedAt)
}

// IgnoreIssue transitions an open or acknowledged issue to ignored.
func (repository *DataQualityIssueRepository) IgnoreIssue(ctx context.Context, id domain.ID, note string, updatedAt time.Time) error {
	return repository.transitionIssue(ctx, id, []string{"open", "acknowledged"}, "ignored", pointerToString(note), &updatedAt, updatedAt)
}

func (repository *DataQualityIssueRepository) transitionIssue(ctx context.Context, id domain.ID, from []string, to string, note *string, resolvedAt *time.Time, updatedAt time.Time) error {
	if id.IsZero() || updatedAt.IsZero() {
		return fmt.Errorf("transition quality issue: %w", domain.ErrInvalidData)
	}
	command, err := repository.database.Exec(ctx, `UPDATE market_data.data_quality_issues SET status = $1, resolved_at = $2, resolution_note = $3, updated_at = $4 WHERE id = $5 AND status = ANY($6)`, to, optionalTimeToDatabase(resolvedAt), optionalString(note), TimeToDatabase(updatedAt), IDToDatabase(id), from)
	if err != nil {
		return fmt.Errorf("transition quality issue: %w", MapError(err))
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	err = repository.database.QueryRow(ctx, "SELECT true FROM market_data.data_quality_issues WHERE id = $1", IDToDatabase(id)).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("transition quality issue: %w", domain.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("check quality issue state: %w", MapError(err))
	}
	return fmt.Errorf("transition quality issue: %w", domain.ErrInvalidState)
}

func pointerToString(value string) *string { return &value }

var _ domain.DataQualityIssueRepository = (*DataQualityIssueRepository)(nil)
