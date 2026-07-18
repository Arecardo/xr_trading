package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

const MaximumIngestionManagementPageSize = 100

// RunListInput contains management Run filters. CreatedTo is exclusive.
type RunListInput struct {
	RunType, TriggerType, Status, RequestedBy string
	CreatedFrom, CreatedTo                    *time.Time
	AfterID                                   *domain.ID
	Limit                                     int
}

// TaskListInput contains management Task filters. CreatedTo is exclusive.
type TaskListInput struct {
	RunID                                *domain.ID
	Status, ProviderCode, InstrumentCode string
	Interval                             string
	CreatedFrom, CreatedTo               *time.Time
	AfterID                              *domain.ID
	Limit                                int
}

// RunReadFilter and TaskReadFilter are normalized repository contracts.
type RunReadFilter = RunListInput
type TaskReadFilter = TaskListInput

// RunRecord is a Run plus Task truth used to derive current summary fields.
type RunRecord struct {
	Run      domain.IngestionRun
	Snapshot ingestion.RunTaskSnapshot
	Summary  ingestion.RunSummary
	Context  map[string]string
}

// TaskRecord is the joined operational Task projection.
type TaskRecord struct {
	Task                   domain.IngestionTask
	RunType, TriggerType   string
	SubscriptionInterval   string
	ProviderID             domain.ID
	ProviderCode           domain.Code
	InstrumentID           domain.ID
	InstrumentCode         domain.Code
	ProviderInstrumentID   domain.ID
	ProviderInstrumentCode domain.Code
	ProviderSymbol         string
	ErrorSummary           *string
	SafeErrorDetails       map[string]string
}

type RunPage struct {
	Items       []RunRecord
	NextAfterID *domain.ID
}

type TaskPage struct {
	Items       []TaskRecord
	NextAfterID *domain.ID
}

// IngestionQueryReader supplies joined read models without exposing SQL.
type IngestionQueryReader interface {
	ListRunRecords(context.Context, RunReadFilter) ([]RunRecord, error)
	GetRunRecord(context.Context, domain.ID) (RunRecord, error)
	ListTaskRecords(context.Context, TaskReadFilter) ([]TaskRecord, error)
	GetTaskRecord(context.Context, domain.ID) (TaskRecord, error)
}

// IngestionQueryService serves management reads and derives Run state from
// Task truth rather than trusting the denormalized Run cache.
type IngestionQueryService struct{ reader IngestionQueryReader }

func NewIngestionQueryService(reader IngestionQueryReader) (*IngestionQueryService, error) {
	if reader == nil {
		return nil, errors.New("ingestion query reader is required")
	}
	return &IngestionQueryService{reader: reader}, nil
}

func (service *IngestionQueryService) ListRuns(ctx context.Context, input RunListInput) (RunPage, error) {
	filter, err := validateRunListInput(input)
	if err != nil {
		return RunPage{}, err
	}
	records, err := service.reader.ListRunRecords(ctx, filter)
	if err != nil {
		return RunPage{}, classifyIngestionQueryFailure(err, false)
	}
	for index := range records {
		if err := enrichRunRecord(&records[index]); err != nil {
			return RunPage{}, classifyIngestionQueryFailure(err, false)
		}
	}
	page := RunPage{Items: records}
	if len(records) > input.Limit {
		page.Items = records[:input.Limit]
		next := page.Items[len(page.Items)-1].Run.ID
		page.NextAfterID = &next
	}
	return page, nil
}

func (service *IngestionQueryService) GetRun(ctx context.Context, id domain.ID) (RunRecord, error) {
	if ctx == nil || id.IsZero() {
		return RunRecord{}, ValidationError([]FieldViolation{{Field: "run_id", Reason: "must be a valid UUID"}})
	}
	record, err := service.reader.GetRunRecord(ctx, id)
	if err != nil {
		return RunRecord{}, classifyIngestionQueryFailure(err, false)
	}
	if err := enrichRunRecord(&record); err != nil {
		return RunRecord{}, classifyIngestionQueryFailure(err, false)
	}
	return record, nil
}

func (service *IngestionQueryService) ListTasks(ctx context.Context, input TaskListInput) (TaskPage, error) {
	filter, err := validateTaskListInput(input)
	if err != nil {
		return TaskPage{}, err
	}
	records, err := service.reader.ListTaskRecords(ctx, filter)
	if err != nil {
		return TaskPage{}, classifyIngestionQueryFailure(err, true)
	}
	for index := range records {
		if err := enrichTaskRecord(&records[index]); err != nil {
			return TaskPage{}, classifyIngestionQueryFailure(err, true)
		}
	}
	page := TaskPage{Items: records}
	if len(records) > input.Limit {
		page.Items = records[:input.Limit]
		next := page.Items[len(page.Items)-1].Task.ID
		page.NextAfterID = &next
	}
	return page, nil
}

func (service *IngestionQueryService) GetTask(ctx context.Context, id domain.ID) (TaskRecord, error) {
	if ctx == nil || id.IsZero() {
		return TaskRecord{}, ValidationError([]FieldViolation{{Field: "task_id", Reason: "must be a valid UUID"}})
	}
	record, err := service.reader.GetTaskRecord(ctx, id)
	if err != nil {
		return TaskRecord{}, classifyIngestionQueryFailure(err, true)
	}
	if err := enrichTaskRecord(&record); err != nil {
		return TaskRecord{}, classifyIngestionQueryFailure(err, true)
	}
	return record, nil
}

func validateRunListInput(input RunListInput) (RunReadFilter, error) {
	violations := make([]FieldViolation, 0, 7)
	if input.RunType != "" && !oneOf(input.RunType, "incremental", "backfill", "repair", "revision") {
		violations = append(violations, FieldViolation{Field: "run_type", Reason: "is not supported"})
	}
	if input.TriggerType != "" && !oneOf(input.TriggerType, "scheduler", "manual", "recovery") {
		violations = append(violations, FieldViolation{Field: "trigger_type", Reason: "is not supported"})
	}
	if input.Status != "" && !oneOf(input.Status, "pending", "running", "partial", "success", "failed", "canceled") {
		violations = append(violations, FieldViolation{Field: "status", Reason: "is not supported"})
	}
	if input.RequestedBy != "" && !validOperationalText(input.RequestedBy, 128) {
		violations = append(violations, FieldViolation{Field: "requested_by", Reason: "must be trimmed and at most 128 characters"})
	}
	violations = append(violations, validateManagementPage(input.CreatedFrom, input.CreatedTo, input.AfterID, input.Limit)...)
	if len(violations) > 0 {
		return RunReadFilter{}, ValidationError(violations)
	}
	input.CreatedFrom, input.CreatedTo = utcPointers(input.CreatedFrom, input.CreatedTo)
	input.Limit++
	return input, nil
}

func validateTaskListInput(input TaskListInput) (TaskReadFilter, error) {
	violations := make([]FieldViolation, 0, 8)
	if input.RunID != nil && input.RunID.IsZero() {
		violations = append(violations, FieldViolation{Field: "run_id", Reason: "must be a valid UUID"})
	}
	if input.Status != "" && !oneOf(input.Status, "pending", "running", "retry_wait", "success", "failed", "canceled") {
		violations = append(violations, FieldViolation{Field: "status", Reason: "is not supported"})
	}
	if input.ProviderCode != "" {
		if _, err := domain.ParseCode(input.ProviderCode); err != nil {
			violations = append(violations, FieldViolation{Field: "provider", Reason: "must be a valid provider code"})
		}
	}
	if input.InstrumentCode != "" {
		if code, err := domain.ParseCode(input.InstrumentCode); err != nil || !strings.HasPrefix(code.String(), "instrument.") {
			violations = append(violations, FieldViolation{Field: "instrument_code", Reason: "must be a valid instrument code"})
		}
	}
	if input.Interval != "" {
		if _, err := domain.ParseBarInterval(input.Interval); err != nil {
			violations = append(violations, FieldViolation{Field: "interval", Reason: "must be one of 1h or 1d"})
		}
	}
	violations = append(violations, validateManagementPage(input.CreatedFrom, input.CreatedTo, input.AfterID, input.Limit)...)
	if len(violations) > 0 {
		return TaskReadFilter{}, ValidationError(violations)
	}
	input.CreatedFrom, input.CreatedTo = utcPointers(input.CreatedFrom, input.CreatedTo)
	input.Limit++
	return input, nil
}

func validateManagementPage(from, to *time.Time, after *domain.ID, limit int) []FieldViolation {
	violations := make([]FieldViolation, 0, 3)
	if from != nil && from.IsZero() {
		violations = append(violations, FieldViolation{Field: "created_from", Reason: "must be a valid timestamp"})
	}
	if to != nil && to.IsZero() {
		violations = append(violations, FieldViolation{Field: "created_to", Reason: "must be a valid timestamp"})
	}
	if from != nil && to != nil && !to.After(*from) {
		violations = append(violations, FieldViolation{Field: "created_from", Reason: "must be earlier than created_to"})
	}
	if after != nil && after.IsZero() {
		violations = append(violations, FieldViolation{Field: "cursor", Reason: "contains an invalid position"})
	}
	if limit < 1 || limit > MaximumIngestionManagementPageSize {
		violations = append(violations, FieldViolation{Field: "limit", Reason: "must be between 1 and 100"})
	}
	return violations
}

func enrichRunRecord(record *RunRecord) error {
	if record == nil || record.Run.ID.IsZero() || record.Snapshot.RunID != record.Run.ID || record.Run.CreatedAt.IsZero() ||
		!oneOf(record.Run.RunType, "incremental", "backfill", "repair", "revision") || !oneOf(record.Run.TriggerType, "scheduler", "manual", "recovery") {
		return domain.ErrInvalidData
	}
	summary, err := ingestion.SummarizeRun(record.Snapshot)
	if err != nil {
		return err
	}
	record.Summary = summary
	record.Context = sanitizeStringObject(record.Run.Context, map[string]struct{}{
		"trigger": {}, "reason": {}, "actor_type": {}, "request_id": {}, "provider": {},
		"instrument_code": {}, "interval": {}, "range_start": {}, "range_end": {},
	})
	return nil
}

func enrichTaskRecord(record *TaskRecord) error {
	if record == nil || record.Task.ID.IsZero() || record.Task.RunID.IsZero() || record.Task.SubscriptionID.IsZero() ||
		record.ProviderID.IsZero() || record.InstrumentID.IsZero() || record.ProviderInstrumentID.IsZero() ||
		record.ProviderCode.IsZero() || record.InstrumentCode.IsZero() || record.ProviderInstrumentCode.IsZero() ||
		record.ProviderSymbol == "" || !record.Task.RangeEnd.After(record.Task.RangeStart) || record.Task.CreatedAt.IsZero() || record.Task.UpdatedAt.IsZero() ||
		!oneOf(record.Task.Status, "pending", "running", "retry_wait", "success", "failed", "canceled") ||
		!oneOf(record.RunType, "incremental", "backfill", "repair", "revision") || !oneOf(record.TriggerType, "scheduler", "manual", "recovery") {
		return domain.ErrInvalidData
	}
	if _, err := domain.ParseBarInterval(record.SubscriptionInterval); err != nil {
		return domain.ErrInvalidData
	}
	record.Task.ErrorCode, record.ErrorSummary = normalizeTaskError(record.Task.ErrorCode)
	record.SafeErrorDetails = sanitizeStringObject(record.Task.ErrorDetails, map[string]struct{}{"provider_code": {}})
	if providerCode, exists := record.SafeErrorDetails["provider_code"]; exists {
		if _, err := domain.ParseCode(providerCode); err != nil {
			delete(record.SafeErrorDetails, "provider_code")
		}
	}
	return nil
}

func normalizeTaskError(code *string) (*string, *string) {
	if code == nil {
		return nil, nil
	}
	messages := map[string]string{
		"rate_limited": "provider rate limit exceeded", "network": "provider network request failed",
		"temporary_unavailable": "provider temporarily unavailable", "unauthorized": "provider authentication failed",
		"invalid_instrument": "provider instrument is invalid", "unsupported_interval": "provider interval is unsupported",
		"bad_request": "provider rejected the request", "invalid_response": "provider returned an invalid response",
		"unknown": "provider request failed", "database_unavailable": "database temporarily unavailable",
		"temporary_failure": "service temporarily unavailable", "adapter_not_registered": "provider adapter is not registered",
		"provider_limit_exceeded": "provider pagination limit was exceeded", "configuration_not_found": "ingestion configuration was not found",
		"configuration_invalid": "ingestion configuration or data contract is invalid", "internal_error": "ingestion task failed",
		"lease_expired": "worker lease expired before completion", "lease_recovery_exhausted": "worker lease recovery limit was exhausted",
	}
	safeCode := *code
	message, exists := messages[safeCode]
	if !exists {
		safeCode = "internal_error"
		message = "ingestion task failed"
	}
	return &safeCode, &message
}

func sanitizeStringObject(raw json.RawMessage, allowed map[string]struct{}) map[string]string {
	result := map[string]string{}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return result
	}
	for key, value := range object {
		if _, exists := allowed[key]; !exists {
			continue
		}
		text, ok := value.(string)
		if ok && validOperationalText(text, 512) {
			result[key] = text
		}
	}
	return result
}

func classifyIngestionQueryFailure(err error, task bool) error {
	if errors.Is(err, domain.ErrNotFound) {
		if task {
			return WrapError(err, ErrorCodeTaskNotFound, "ingestion task not found", false, nil)
		}
		return WrapError(err, ErrorCodeNotFound, "ingestion run not found", false, nil)
	}
	if errors.Is(err, domain.ErrDatabaseUnavailable) || errors.Is(err, domain.ErrRetryable) {
		return err
	}
	return WrapError(err, ErrorCodeInternal, "internal server error", false, nil)
}

func validOperationalText(value string, maximum int) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func utcPointers(from, to *time.Time) (*time.Time, *time.Time) {
	var normalizedFrom, normalizedTo *time.Time
	if from != nil {
		value := from.UTC()
		normalizedFrom = &value
	}
	if to != nil {
		value := to.UTC()
		normalizedTo = &value
	}
	return normalizedFrom, normalizedTo
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
