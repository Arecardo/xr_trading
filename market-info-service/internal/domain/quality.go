package domain

import (
	"context"
	"encoding/json"
	"time"
)

// DataQualityIssue records an observed data problem and its human workflow.
type DataQualityIssue struct {
	ID                   ID
	InstrumentID         ID
	ProviderInstrumentID *ID
	Interval             *string
	OpenTime             *time.Time
	RuleCode             string
	Severity             string
	Status               string
	Summary              string
	Details              json.RawMessage
	DetectedAt           time.Time
	ResolvedAt           *time.Time
	ResolutionNote       *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// DataQualityIssueRepository persists quality issues and their state changes.
type DataQualityIssueRepository interface {
	OpenIssue(context.Context, DataQualityIssue) (bool, error)
	AcknowledgeIssue(context.Context, ID, time.Time) error
	ResolveIssue(context.Context, ID, string, time.Time) error
	IgnoreIssue(context.Context, ID, string, time.Time) error
}
