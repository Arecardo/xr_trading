package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
)

const ingestionBackfillPath = "/api/market-info/v1/ingestion-runs/backfill"

// BackfillCreation is the HTTP-facing single-range backfill use case.
type BackfillCreation interface {
	Create(context.Context, ingestion.BackfillInput) (ingestion.BackfillResult, error)
}

// RegisterBackfillRoutes attaches the authenticated manual backfill endpoint.
func RegisterBackfillRoutes(mux *http.ServeMux, service BackfillCreation, authenticator application.Authenticator) error {
	if mux == nil || service == nil || authenticator == nil {
		return errors.New("backfill routes dependencies are required")
	}
	authenticate, err := NewAuthenticationMiddleware(authenticator)
	if err != nil {
		return err
	}
	manage, err := NewPermissionMiddleware(application.PermissionIngestionManage)
	if err != nil {
		return err
	}
	handler := &BackfillHandler{service: service}
	mux.Handle("POST "+ingestionBackfillPath, authenticate(manage(http.HandlerFunc(handler.create))))
	return nil
}

// BackfillHandler translates one bounded historical range into a durable Run
// and Task. Provider calls remain the responsibility of the existing Worker.
type BackfillHandler struct {
	service BackfillCreation
}

func (handler *BackfillHandler) create(writer http.ResponseWriter, request *http.Request) {
	var body createBackfillRequest
	if err := DecodeJSON(writer, request, &body, DefaultMaximumRequestBodyBytes); err != nil {
		WriteError(writer, request, err)
		return
	}
	input, err := backfillInputFromRequest(request, body)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	result, err := handler.service.Create(request.Context(), input)
	if err != nil {
		WriteError(writer, request, classifyBackfillFailure(err))
		return
	}
	createdAt, err := domain.NewUTCInstant(result.CreatedAt)
	if err != nil || result.RunID.IsZero() || result.TaskID.IsZero() || result.Status != "pending" {
		WriteError(writer, request, errors.New("backfill service returned an invalid result"))
		return
	}
	response := createBackfillResponse{
		RunID: result.RunID, TaskID: result.TaskID, Status: result.Status,
		CreatedAt: timeValue{createdAt},
	}
	if err := WriteJSON(writer, http.StatusAccepted, response); err != nil {
		WriteError(writer, request, err)
	}
}

type createBackfillRequest struct {
	Provider       string `json:"provider"`
	InstrumentCode string `json:"instrument_code"`
	Interval       string `json:"interval"`
	StartTime      string `json:"start_time"`
	EndTime        string `json:"end_time"`
	Reason         string `json:"reason"`
}

type createBackfillResponse struct {
	RunID     domain.ID `json:"run_id"`
	TaskID    domain.ID `json:"task_id"`
	Status    string    `json:"status"`
	CreatedAt timeValue `json:"created_at"`
}

func backfillInputFromRequest(request *http.Request, body createBackfillRequest) (ingestion.BackfillInput, error) {
	violations := make([]application.FieldViolation, 0, 6)
	if _, err := domain.ParseCode(body.Provider); err != nil {
		violations = append(violations, application.FieldViolation{Field: "provider", Reason: "must be a valid provider code"})
	}
	if _, err := domain.ParseCode(body.InstrumentCode); err != nil {
		violations = append(violations, application.FieldViolation{Field: "instrument_code", Reason: "must be a valid instrument code"})
	}
	if _, err := domain.ParseBarInterval(body.Interval); err != nil {
		violations = append(violations, application.FieldViolation{Field: "interval", Reason: "must be one of 1h or 1d"})
	}
	start, startErr := domain.ParseUTCInstant(body.StartTime)
	if startErr != nil {
		violations = append(violations, application.FieldViolation{Field: "start_time", Reason: "must be an RFC3339 timestamp"})
	}
	end, endErr := domain.ParseUTCInstant(body.EndTime)
	if endErr != nil {
		violations = append(violations, application.FieldViolation{Field: "end_time", Reason: "must be an RFC3339 timestamp"})
	}
	if startErr == nil && endErr == nil && !end.Time().After(start.Time()) {
		violations = append(violations, application.FieldViolation{Field: "start_time", Reason: "must be earlier than end_time"})
	}
	if body.Reason == "" || body.Reason != strings.TrimSpace(body.Reason) || len([]rune(body.Reason)) > 512 {
		violations = append(violations, application.FieldViolation{Field: "reason", Reason: "must be non-empty, trimmed and at most 512 characters"})
	}
	if len(violations) > 0 {
		return ingestion.BackfillInput{}, application.ValidationError(violations)
	}
	audit, exists := application.AuditContextFromContext(request.Context())
	if !exists {
		return ingestion.BackfillInput{}, errors.New("backfill audit context is required")
	}
	return ingestion.BackfillInput{
		ProviderCode: body.Provider, InstrumentCode: body.InstrumentCode, Interval: body.Interval,
		StartTime: start.Time(), EndTime: end.Time(), Reason: body.Reason,
		RequestedBy: audit.RequestedBy(), ActorType: string(audit.ActorType()), RequestID: audit.RequestID(),
	}, nil
}

func classifyBackfillFailure(err error) error {
	switch {
	case errors.Is(err, ingestion.ErrBackfillAlreadyRunning):
		return application.WrapError(err, application.ErrorCodeBackfillAlreadyRunning, "an equivalent backfill is already active", false, nil)
	case errors.Is(err, domain.ErrNotFound):
		return application.WrapError(err, application.ErrorCodeSubscriptionNotFound, "enabled collection subscription not found", false, nil)
	case errors.Is(err, domain.ErrInvalidData):
		return application.WrapError(err, application.ErrorCodeInvalidArgument, "invalid backfill request", false, nil)
	default:
		return err
	}
}
