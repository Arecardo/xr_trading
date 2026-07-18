package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"xr-trading/market-info-service/internal/domain"
)

const (
	defaultBackfillMaximumAttempts = 5
	maximumBackfillAuditTextLength = 128
	maximumBackfillReasonLength    = 512
)

// ErrBackfillAlreadyRunning means an equivalent backfill Task is still in an
// active state. ADM-002 maps this stable result to BACKFILL_ALREADY_RUNNING.
var ErrBackfillAlreadyRunning = errors.New("equivalent backfill is already running")

// BackfillConfig controls durable Task retry bounds.
type BackfillConfig struct {
	MaximumAttempts int
}

// BackfillInput is one explicit provider/instrument/interval/range request.
// Audit fields are populated from the authenticated request context by the
// future ADM-002 transport use case.
type BackfillInput struct {
	ProviderCode   string
	InstrumentCode string
	Interval       string
	StartTime      time.Time
	EndTime        time.Time
	Reason         string
	RequestedBy    string
	ActorType      string
	RequestID      string
}

// BackfillResult identifies the only Run and Task created by one request.
type BackfillResult struct {
	RunID     domain.ID
	TaskID    domain.ID
	Status    string
	CreatedAt time.Time
}

// BackfillStore resolves an enabled collection target and atomically creates
// exactly one Run and one Task. Duplicate active ranges return
// ErrBackfillAlreadyRunning.
type BackfillStore interface {
	ResolveBackfillTarget(context.Context, domain.Code, domain.Code, domain.BarInterval, time.Time) (ExecutionContext, error)
	CreateBackfillRunWithTask(context.Context, domain.IngestionRun, domain.IngestionTask) error
}

// IDGenerator makes UUIDv7 generation deterministic in unit tests.
type IDGenerator func() (domain.ID, error)

// BackfillService creates one durable backfill Task; the existing Worker and
// IngestionService execute its Provider pagination without page-level Tasks.
type BackfillService struct {
	store           BackfillStore
	now             func() time.Time
	newID           IDGenerator
	maximumAttempts int
}

// NewBackfillService constructs the single-task backfill use case.
func NewBackfillService(config BackfillConfig, store BackfillStore, now func() time.Time, newID IDGenerator) (*BackfillService, error) {
	if config.MaximumAttempts == 0 {
		config.MaximumAttempts = defaultBackfillMaximumAttempts
	}
	if config.MaximumAttempts < 1 {
		return nil, errors.New("backfill maximum attempts must be positive")
	}
	if store == nil || now == nil || newID == nil {
		return nil, errors.New("backfill service dependencies are required")
	}
	return &BackfillService{store: store, now: now, newID: newID, maximumAttempts: config.MaximumAttempts}, nil
}

// Create validates one bounded historical range and persists its pending Run
// and Task atomically. It never calls a Provider and never waits for execution.
func (service *BackfillService) Create(ctx context.Context, input BackfillInput) (BackfillResult, error) {
	if ctx == nil {
		return BackfillResult{}, fmt.Errorf("create backfill: %w", domain.ErrInvalidData)
	}
	request, err := parseBackfillInput(input, service.now().UTC())
	if err != nil {
		return BackfillResult{}, fmt.Errorf("create backfill: %w", err)
	}
	target, err := service.store.ResolveBackfillTarget(ctx, request.providerCode, request.instrumentCode, request.interval, request.createdAt)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("resolve backfill target: %w", err)
	}
	if err := validateBackfillTarget(target, request); err != nil {
		return BackfillResult{}, fmt.Errorf("validate backfill target: %w", err)
	}
	runID, err := service.newID()
	if err != nil || !validBackfillID(runID) {
		if err == nil {
			err = domain.ErrInvalidData
		}
		return BackfillResult{}, fmt.Errorf("generate backfill run ID: %w", err)
	}
	taskID, err := service.newID()
	if err != nil || !validBackfillID(taskID) || taskID == runID {
		if err == nil {
			err = domain.ErrInvalidData
		}
		return BackfillResult{}, fmt.Errorf("generate backfill task ID: %w", err)
	}
	contextJSON, err := backfillContextJSON(request)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("encode backfill context: %w", err)
	}
	requestedBy := request.requestedBy
	run := domain.IngestionRun{
		ID: runID, RunKey: "backfill.manual." + runID.String(), RunType: "backfill", TriggerType: "manual",
		Status: "pending", RequestedBy: &requestedBy, TaskCount: 1, Context: contextJSON,
		ErrorSummary: json.RawMessage(`{}`), CreatedAt: request.createdAt,
	}
	task := domain.IngestionTask{
		ID: taskID, RunID: runID, SubscriptionID: target.Subscription.ID,
		RangeStart: request.startTime, RangeEnd: request.endTime, Status: "pending",
		MaxAttempts: service.maximumAttempts, ErrorDetails: json.RawMessage(`{}`),
		CreatedAt: request.createdAt, UpdatedAt: request.createdAt,
	}
	if err := service.store.CreateBackfillRunWithTask(ctx, run, task); err != nil {
		return BackfillResult{}, fmt.Errorf("persist backfill: %w", err)
	}
	return BackfillResult{RunID: runID, TaskID: taskID, Status: "pending", CreatedAt: request.createdAt}, nil
}

type parsedBackfillInput struct {
	providerCode   domain.Code
	instrumentCode domain.Code
	interval       domain.BarInterval
	startTime      time.Time
	endTime        time.Time
	reason         string
	requestedBy    string
	actorType      string
	requestID      string
	createdAt      time.Time
}

func parseBackfillInput(input BackfillInput, now time.Time) (parsedBackfillInput, error) {
	providerCode, providerErr := domain.ParseCode(input.ProviderCode)
	instrumentCode, instrumentErr := domain.ParseCode(input.InstrumentCode)
	interval, intervalErr := domain.ParseBarInterval(input.Interval)
	rangeValue, rangeErr := domain.NewBoundedTimeRange(input.StartTime, input.EndTime)
	if providerErr != nil || instrumentErr != nil || intervalErr != nil || rangeErr != nil || now.IsZero() ||
		!validBackfillText(input.Reason, maximumBackfillReasonLength) ||
		!validBackfillText(input.RequestedBy, maximumBackfillAuditTextLength) ||
		!validBackfillText(input.RequestID, maximumBackfillAuditTextLength) ||
		(input.ActorType != "user" && input.ActorType != "service") {
		return parsedBackfillInput{}, domain.ErrInvalidData
	}
	startTime, endTime := rangeValue.Start.Time(), rangeValue.End.Time()
	if endTime.After(now) {
		return parsedBackfillInput{}, domain.ErrInvalidData
	}
	return parsedBackfillInput{
		providerCode: providerCode, instrumentCode: instrumentCode, interval: interval,
		startTime: startTime, endTime: endTime, reason: input.Reason,
		requestedBy: input.RequestedBy, actorType: input.ActorType, requestID: input.RequestID,
		createdAt: now,
	}, nil
}

func validateBackfillTarget(target ExecutionContext, request parsedBackfillInput) error {
	if err := target.Validate(); err != nil {
		return err
	}
	if target.Provider.Code != request.providerCode || target.Instrument.Code != request.instrumentCode ||
		target.Subscription.Interval != string(request.interval) ||
		target.Subscription.ProviderInstrumentID != target.ProviderInstrument.ID {
		return domain.ErrInvalidData
	}
	return nil
}

func backfillContextJSON(request parsedBackfillInput) (json.RawMessage, error) {
	return json.Marshal(map[string]string{
		"actor_type": request.actorType, "request_id": request.requestID, "reason": request.reason,
		"provider": request.providerCode.String(), "instrument_code": request.instrumentCode.String(),
		"interval": string(request.interval), "range_start": request.startTime.Format(time.RFC3339Nano),
		"range_end": request.endTime.Format(time.RFC3339Nano),
	})
}

func validBackfillText(value string, maximumLength int) bool {
	if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > maximumLength {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validBackfillID(id domain.ID) bool {
	return !id.IsZero() && id.UUID().Version() == uuid.Version(7)
}
