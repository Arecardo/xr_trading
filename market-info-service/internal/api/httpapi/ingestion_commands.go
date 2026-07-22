package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion"
	"xr-trading/market-info-service/internal/observability"
)

const ingestionTaskCommandPath = "/api/market-info/v1/ingestion-tasks/{task_id}"

// IngestionTaskCommands is the HTTP-facing administrator mutation contract.
type IngestionTaskCommands interface {
	Retry(context.Context, domain.ID, ingestion.TaskOperationAudit) (ingestion.ManualRetryResult, error)
	Cancel(context.Context, domain.ID, ingestion.TaskOperationAudit) (ingestion.TaskCancellationResult, error)
}

// RegisterIngestionTaskCommandRoutes attaches retry and cancel with the same
// explicit ingestion.manage capability used by backfill.
func RegisterIngestionTaskCommandRoutes(mux *http.ServeMux, service IngestionTaskCommands, authenticator application.Authenticator) error {
	if mux == nil || service == nil || authenticator == nil {
		return errors.New("ingestion task command route dependencies are required")
	}
	authenticate, err := NewAuthenticationMiddleware(authenticator)
	if err != nil {
		return err
	}
	manage, err := NewPermissionMiddleware(application.PermissionIngestionManage)
	if err != nil {
		return err
	}
	handler := &IngestionTaskCommandHandler{service: service}
	protect := func(value http.HandlerFunc) http.Handler { return authenticate(manage(value)) }
	mux.Handle("POST "+ingestionTaskCommandPath+"/retry", protect(handler.retry))
	mux.Handle("POST "+ingestionTaskCommandPath+"/cancel", protect(handler.cancel))
	return nil
}

type IngestionTaskCommandHandler struct{ service IngestionTaskCommands }

func (handler *IngestionTaskCommandHandler) retry(writer http.ResponseWriter, request *http.Request) {
	taskID, audit, err := taskCommandInput(writer, request)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	result, err := handler.service.Retry(request.Context(), taskID, audit)
	if err != nil {
		WriteError(writer, request, classifyTaskCommandFailure(err, true))
		return
	}
	createdAt, timeErr := domain.NewUTCInstant(result.CreatedAt)
	if timeErr != nil || result.RunID.IsZero() || result.TaskID.IsZero() || result.Status != "pending" {
		WriteError(writer, request, errors.New("manual retry service returned an invalid result"))
		return
	}
	response := manualRetryResponse{
		RunID: result.RunID, TaskID: result.TaskID, Status: result.Status, CreatedAt: timeValue{createdAt},
	}
	if err := WriteJSON(writer, http.StatusAccepted, response); err != nil {
		WriteError(writer, request, err)
		return
	}
	logContext := observability.WithCorrelation(request.Context(), observability.CorrelationFields{RunID: result.RunID, TaskID: result.TaskID})
	observability.LoggerFromContext(logContext).InfoContext(logContext, "ingestion task retry accepted",
		slog.String("retry_of_task_id", taskID.String()),
	)
}

func (handler *IngestionTaskCommandHandler) cancel(writer http.ResponseWriter, request *http.Request) {
	taskID, audit, err := taskCommandInput(writer, request)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	result, err := handler.service.Cancel(request.Context(), taskID, audit)
	if err != nil {
		WriteError(writer, request, classifyTaskCommandFailure(err, false))
		return
	}
	canceledAt, timeErr := domain.NewUTCInstant(result.CanceledAt)
	if timeErr != nil || result.TaskID != taskID || result.RunID.IsZero() || result.Status != "canceled" {
		WriteError(writer, request, errors.New("task cancellation service returned an invalid result"))
		return
	}
	response := taskCancellationResponse{
		TaskID: result.TaskID, RunID: result.RunID, Status: result.Status, CanceledAt: timeValue{canceledAt},
	}
	if err := WriteJSON(writer, http.StatusOK, response); err != nil {
		WriteError(writer, request, err)
		return
	}
	logContext := observability.WithCorrelation(request.Context(), observability.CorrelationFields{RunID: result.RunID, TaskID: result.TaskID})
	observability.LoggerFromContext(logContext).InfoContext(logContext, "ingestion task canceled")
}

type taskCommandRequest struct {
	Reason string `json:"reason"`
}

type manualRetryResponse struct {
	RunID     domain.ID `json:"run_id"`
	TaskID    domain.ID `json:"task_id"`
	Status    string    `json:"status"`
	CreatedAt timeValue `json:"created_at"`
}

type taskCancellationResponse struct {
	TaskID     domain.ID `json:"task_id"`
	RunID      domain.ID `json:"run_id"`
	Status     string    `json:"status"`
	CanceledAt timeValue `json:"canceled_at"`
}

func taskCommandInput(writer http.ResponseWriter, request *http.Request) (domain.ID, ingestion.TaskOperationAudit, error) {
	if request.URL.RawQuery != "" {
		return domain.ID{}, ingestion.TaskOperationAudit{}, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is not supported"}})
	}
	taskID, err := domain.ParseID(request.PathValue("task_id"))
	if err != nil {
		return domain.ID{}, ingestion.TaskOperationAudit{}, application.ValidationError([]application.FieldViolation{{Field: "task_id", Reason: "must be a canonical UUID"}})
	}
	var body taskCommandRequest
	if err := DecodeJSON(writer, request, &body, DefaultMaximumRequestBodyBytes); err != nil {
		return domain.ID{}, ingestion.TaskOperationAudit{}, err
	}
	if body.Reason == "" || body.Reason != strings.TrimSpace(body.Reason) || len([]rune(body.Reason)) > 512 {
		return domain.ID{}, ingestion.TaskOperationAudit{}, application.ValidationError([]application.FieldViolation{{Field: "reason", Reason: "must be non-empty, trimmed and at most 512 characters"}})
	}
	audit, exists := application.AuditContextFromContext(request.Context())
	if !exists {
		return domain.ID{}, ingestion.TaskOperationAudit{}, errors.New("task operation audit context is required")
	}
	return taskID, ingestion.TaskOperationAudit{
		RequestedBy: audit.RequestedBy(), ActorType: string(audit.ActorType()), RequestID: audit.RequestID(), Reason: body.Reason,
	}, nil
}

func classifyTaskCommandFailure(err error, retry bool) error {
	switch {
	case retry && errors.Is(err, ingestion.ErrManualRetryAlreadyRunning):
		return application.WrapError(err, application.ErrorCodeManualRetryAlreadyRunning, "a manual retry is already active", false, nil)
	case retry && errors.Is(err, ingestion.ErrManualRetrySourceUnavailable):
		return application.WrapError(err, application.ErrorCodeConflict, "the task collection source is no longer available", false, nil)
	case errors.Is(err, domain.ErrNotFound):
		return application.WrapError(err, application.ErrorCodeTaskNotFound, "ingestion task not found", false, nil)
	case errors.Is(err, domain.ErrConflict):
		return application.WrapError(err, application.ErrorCodeTaskStateConflict, "task state does not allow this operation", false, nil)
	case errors.Is(err, domain.ErrInvalidData):
		return application.WrapError(err, application.ErrorCodeInvalidArgument, "invalid task operation", false, nil)
	default:
		return err
	}
}
