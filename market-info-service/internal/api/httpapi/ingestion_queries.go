package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"time"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

const (
	ingestionRunsPath         = "/api/market-info/v1/ingestion-runs"
	ingestionTasksPath        = "/api/market-info/v1/ingestion-tasks"
	ingestionRunsCursorScope  = "ingestion-runs"
	ingestionTasksCursorScope = "ingestion-tasks"
)

var ingestionManagementPageLimits = PageLimits{Default: 50, Maximum: application.MaximumIngestionManagementPageSize}

// IngestionQueries is the HTTP-facing operational read contract.
type IngestionQueries interface {
	ListRuns(context.Context, application.RunListInput) (application.RunPage, error)
	GetRun(context.Context, domain.ID) (application.RunRecord, error)
	ListTasks(context.Context, application.TaskListInput) (application.TaskPage, error)
	GetTask(context.Context, domain.ID) (application.TaskRecord, error)
}

// RegisterIngestionQueryRoutes attaches authenticated Run and Task reads.
func RegisterIngestionQueryRoutes(mux *http.ServeMux, service IngestionQueries, authenticator application.Authenticator) error {
	if mux == nil || service == nil || authenticator == nil {
		return errors.New("ingestion query routes dependencies are required")
	}
	authenticate, err := NewAuthenticationMiddleware(authenticator)
	if err != nil {
		return err
	}
	read, err := NewPermissionMiddleware(application.PermissionOperationsRead)
	if err != nil {
		return err
	}
	handler := &IngestionQueryHandler{service: service}
	protect := func(handler http.HandlerFunc) http.Handler { return authenticate(read(handler)) }
	mux.Handle("GET "+ingestionRunsPath, protect(handler.listRuns))
	mux.Handle("GET "+ingestionRunsPath+"/{run_id}", protect(handler.getRun))
	mux.Handle("GET "+ingestionTasksPath, protect(handler.listTasks))
	mux.Handle("GET "+ingestionTasksPath+"/{task_id}", protect(handler.getTask))
	return nil
}

type IngestionQueryHandler struct{ service IngestionQueries }

func (handler *IngestionQueryHandler) listRuns(writer http.ResponseWriter, request *http.Request) {
	input, err := parseRunListRequest(request)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	page, err := handler.service.ListRuns(request.Context(), input)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := runPageResponse(input, page)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, response); err != nil {
		WriteError(writer, request, err)
	}
}

func (handler *IngestionQueryHandler) getRun(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		WriteError(writer, request, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is not supported"}}))
		return
	}
	id, err := domain.ParseID(request.PathValue("run_id"))
	if err != nil {
		WriteError(writer, request, application.ValidationError([]application.FieldViolation{{Field: "run_id", Reason: "must be a canonical UUID"}}))
		return
	}
	record, err := handler.service.GetRun(request.Context(), id)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := runResponseFromRecord(record)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, struct {
		Run runResponse `json:"run"`
	}{Run: response}); err != nil {
		WriteError(writer, request, err)
	}
}

func (handler *IngestionQueryHandler) listTasks(writer http.ResponseWriter, request *http.Request) {
	input, err := parseTaskListRequest(request)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	page, err := handler.service.ListTasks(request.Context(), input)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := taskPageResponse(input, page)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, response); err != nil {
		WriteError(writer, request, err)
	}
}

func (handler *IngestionQueryHandler) getTask(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		WriteError(writer, request, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is not supported"}}))
		return
	}
	id, err := domain.ParseID(request.PathValue("task_id"))
	if err != nil {
		WriteError(writer, request, application.ValidationError([]application.FieldViolation{{Field: "task_id", Reason: "must be a canonical UUID"}}))
		return
	}
	record, err := handler.service.GetTask(request.Context(), id)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := taskResponseFromRecord(record)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, struct {
		Task taskResponse `json:"task"`
	}{Task: response}); err != nil {
		WriteError(writer, request, err)
	}
}

type runPageResponseBody struct {
	Items      []runResponse `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

type runResponse struct {
	ID             domain.ID         `json:"run_id"`
	RunKey         string            `json:"run_key"`
	Type           string            `json:"run_type"`
	TriggerType    string            `json:"trigger_type"`
	Status         string            `json:"status"`
	ScheduledAt    *timeValue        `json:"scheduled_at"`
	StartedAt      *timeValue        `json:"started_at"`
	FinishedAt     *timeValue        `json:"finished_at"`
	RequestedBy    *string           `json:"requested_by"`
	TaskCount      int               `json:"task_count"`
	PendingCount   int               `json:"pending_count"`
	RunningCount   int               `json:"running_count"`
	RetryWaitCount int               `json:"retry_wait_count"`
	SuccessCount   int               `json:"success_count"`
	FailedCount    int               `json:"failed_count"`
	CanceledCount  int               `json:"canceled_count"`
	Context        map[string]string `json:"context"`
	CreatedAt      timeValue         `json:"created_at"`
}

type taskPageResponseBody struct {
	Items      []taskResponse `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

type taskResponse struct {
	TaskID             domain.ID                  `json:"task_id"`
	Run                taskRunIdentity            `json:"run"`
	Subscription       taskSubscription           `json:"subscription"`
	Provider           taskProviderIdentity       `json:"provider"`
	Instrument         taskInstrumentIdentity     `json:"instrument"`
	ProviderInstrument providerInstrumentIdentity `json:"provider_instrument"`
	RetryOfTaskID      *domain.ID                 `json:"retry_of_task_id"`
	RangeStart         timeValue                  `json:"range_start"`
	RangeEnd           timeValue                  `json:"range_end"`
	Status             string                     `json:"status"`
	AttemptCount       int                        `json:"attempt_count"`
	MaxAttempts        int                        `json:"max_attempts"`
	NextAttemptAt      *timeValue                 `json:"next_attempt_at"`
	LockedBy           *string                    `json:"locked_by"`
	LockedUntil        *timeValue                 `json:"locked_until"`
	StartedAt          *timeValue                 `json:"started_at"`
	FinishedAt         *timeValue                 `json:"finished_at"`
	ProviderRequestID  *string                    `json:"provider_request_id"`
	ErrorCode          *string                    `json:"error_code"`
	ErrorSummary       *string                    `json:"error_summary"`
	ErrorDetails       map[string]string          `json:"error_details"`
	CanceledBy         *string                    `json:"canceled_by"`
	CancelReason       *string                    `json:"cancel_reason"`
	CreatedAt          timeValue                  `json:"created_at"`
	UpdatedAt          timeValue                  `json:"updated_at"`
}

type taskRunIdentity struct {
	RunID       domain.ID `json:"run_id"`
	RunType     string    `json:"run_type"`
	TriggerType string    `json:"trigger_type"`
}
type taskSubscription struct {
	SubscriptionID domain.ID `json:"subscription_id"`
	Interval       string    `json:"interval"`
}
type taskProviderIdentity struct {
	ProviderID   domain.ID   `json:"provider_id"`
	ProviderCode domain.Code `json:"provider_code"`
}
type taskInstrumentIdentity struct {
	InstrumentID   domain.ID   `json:"instrument_id"`
	InstrumentCode domain.Code `json:"instrument_code"`
}
type providerInstrumentIdentity struct {
	ProviderInstrumentID   domain.ID   `json:"provider_instrument_id"`
	ProviderInstrumentCode domain.Code `json:"provider_instrument_code"`
	ProviderSymbol         string      `json:"provider_symbol"`
}

func parseRunListRequest(request *http.Request) (application.RunListInput, error) {
	values, err := managementQuery(request, map[string]struct{}{"run_type": {}, "trigger_type": {}, "status": {}, "requested_by": {}, "created_from": {}, "created_to": {}, "limit": {}, "cursor": {}})
	if err != nil {
		return application.RunListInput{}, err
	}
	runType, err := singleQueryValue(values, "run_type", false)
	if err != nil {
		return application.RunListInput{}, err
	}
	trigger, err := singleQueryValue(values, "trigger_type", false)
	if err != nil {
		return application.RunListInput{}, err
	}
	status, err := singleQueryValue(values, "status", false)
	if err != nil {
		return application.RunListInput{}, err
	}
	requestedBy, err := singleQueryValue(values, "requested_by", false)
	if err != nil {
		return application.RunListInput{}, err
	}
	from, err := parseOptionalQueryTime(values, "created_from")
	if err != nil {
		return application.RunListInput{}, err
	}
	to, err := parseOptionalQueryTime(values, "created_to")
	if err != nil {
		return application.RunListInput{}, err
	}
	limitRaw, err := singleQueryValue(values, "limit", false)
	if err != nil {
		return application.RunListInput{}, err
	}
	limit, err := ParsePageSize(limitRaw, ingestionManagementPageLimits)
	if err != nil {
		return application.RunListInput{}, err
	}
	input := application.RunListInput{RunType: runType, TriggerType: trigger, Status: status, RequestedBy: requestedBy, CreatedFrom: from, CreatedTo: to, Limit: limit}
	cursor, err := singleQueryValue(values, "cursor", false)
	if err != nil {
		return application.RunListInput{}, err
	}
	if cursor != "" {
		positions, decodeErr := DecodeCursor(cursor, ingestionRunsCursorScope, 7)
		if decodeErr != nil || positions[0] != runType || positions[1] != trigger || positions[2] != status || positions[3] != requestedBy || positions[4] != timeScopeValue(from) || positions[5] != timeScopeValue(to) {
			return application.RunListInput{}, invalidCursor()
		}
		id, parseErr := domain.ParseID(positions[6])
		if parseErr != nil {
			return application.RunListInput{}, invalidCursor()
		}
		input.AfterID = &id
	}
	return input, nil
}

func parseTaskListRequest(request *http.Request) (application.TaskListInput, error) {
	values, err := managementQuery(request, map[string]struct{}{"run_id": {}, "status": {}, "provider": {}, "instrument_code": {}, "interval": {}, "created_from": {}, "created_to": {}, "limit": {}, "cursor": {}})
	if err != nil {
		return application.TaskListInput{}, err
	}
	runIDRaw, err := singleQueryValue(values, "run_id", false)
	if err != nil {
		return application.TaskListInput{}, err
	}
	status, err := singleQueryValue(values, "status", false)
	if err != nil {
		return application.TaskListInput{}, err
	}
	provider, err := singleQueryValue(values, "provider", false)
	if err != nil {
		return application.TaskListInput{}, err
	}
	instrumentCode, err := singleQueryValue(values, "instrument_code", false)
	if err != nil {
		return application.TaskListInput{}, err
	}
	interval, err := singleQueryValue(values, "interval", false)
	if err != nil {
		return application.TaskListInput{}, err
	}
	from, err := parseOptionalQueryTime(values, "created_from")
	if err != nil {
		return application.TaskListInput{}, err
	}
	to, err := parseOptionalQueryTime(values, "created_to")
	if err != nil {
		return application.TaskListInput{}, err
	}
	limitRaw, err := singleQueryValue(values, "limit", false)
	if err != nil {
		return application.TaskListInput{}, err
	}
	limit, err := ParsePageSize(limitRaw, ingestionManagementPageLimits)
	if err != nil {
		return application.TaskListInput{}, err
	}
	input := application.TaskListInput{Status: status, ProviderCode: provider, InstrumentCode: instrumentCode, Interval: interval, CreatedFrom: from, CreatedTo: to, Limit: limit}
	if runIDRaw != "" {
		id, parseErr := domain.ParseID(runIDRaw)
		if parseErr != nil {
			return application.TaskListInput{}, application.ValidationError([]application.FieldViolation{{Field: "run_id", Reason: "must be a canonical UUID"}})
		}
		input.RunID = &id
	}
	cursor, err := singleQueryValue(values, "cursor", false)
	if err != nil {
		return application.TaskListInput{}, err
	}
	if cursor != "" {
		positions, decodeErr := DecodeCursor(cursor, ingestionTasksCursorScope, 8)
		if decodeErr != nil || positions[0] != runIDRaw || positions[1] != status || positions[2] != provider || positions[3] != instrumentCode || positions[4] != interval || positions[5] != timeScopeValue(from) || positions[6] != timeScopeValue(to) {
			return application.TaskListInput{}, invalidCursor()
		}
		id, parseErr := domain.ParseID(positions[7])
		if parseErr != nil {
			return application.TaskListInput{}, invalidCursor()
		}
		input.AfterID = &id
	}
	return input, nil
}

func managementQuery(request *http.Request, allowed map[string]struct{}) (url.Values, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("HTTP request is required")
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return nil, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is malformed"}})
	}
	unknown := make([]string, 0)
	for key := range values {
		if _, exists := allowed[key]; !exists {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return values, nil
	}
	sort.Strings(unknown)
	violations := make([]application.FieldViolation, 0, len(unknown))
	for _, key := range unknown {
		violations = append(violations, application.FieldViolation{Field: key, Reason: "is not supported"})
	}
	return nil, application.ValidationError(violations)
}

func runPageResponse(input application.RunListInput, page application.RunPage) (runPageResponseBody, error) {
	response := runPageResponseBody{Items: make([]runResponse, 0, len(page.Items))}
	for _, record := range page.Items {
		item, err := runResponseFromRecord(record)
		if err != nil {
			return runPageResponseBody{}, err
		}
		response.Items = append(response.Items, item)
	}
	if page.NextAfterID != nil {
		cursor, err := EncodeCursor(ingestionRunsCursorScope, input.RunType, input.TriggerType, input.Status, input.RequestedBy, timeScopeValue(input.CreatedFrom), timeScopeValue(input.CreatedTo), page.NextAfterID.String())
		if err != nil {
			return runPageResponseBody{}, err
		}
		response.NextCursor = &cursor
	}
	return response, nil
}

func taskPageResponse(input application.TaskListInput, page application.TaskPage) (taskPageResponseBody, error) {
	response := taskPageResponseBody{Items: make([]taskResponse, 0, len(page.Items))}
	for _, record := range page.Items {
		item, err := taskResponseFromRecord(record)
		if err != nil {
			return taskPageResponseBody{}, err
		}
		response.Items = append(response.Items, item)
	}
	if page.NextAfterID != nil {
		cursor, err := EncodeCursor(ingestionTasksCursorScope, idScopeValue(input.RunID), input.Status, input.ProviderCode, input.InstrumentCode, input.Interval, timeScopeValue(input.CreatedFrom), timeScopeValue(input.CreatedTo), page.NextAfterID.String())
		if err != nil {
			return taskPageResponseBody{}, err
		}
		response.NextCursor = &cursor
	}
	return response, nil
}

func runResponseFromRecord(record application.RunRecord) (runResponse, error) {
	createdAt, err := requiredTimeValue(record.Run.CreatedAt)
	if err != nil {
		return runResponse{}, err
	}
	return runResponse{ID: record.Run.ID, RunKey: record.Run.RunKey, Type: record.Run.RunType, TriggerType: record.Run.TriggerType, Status: record.Summary.Status, ScheduledAt: optionalTimeValue(record.Run.ScheduledAt), StartedAt: optionalTimeValue(record.Summary.EarliestStartedAt), FinishedAt: optionalTimeValue(record.Summary.LatestFinishedAt), RequestedBy: record.Run.RequestedBy, TaskCount: record.Summary.TaskCount, PendingCount: record.Summary.PendingCount, RunningCount: record.Summary.RunningCount, RetryWaitCount: record.Summary.RetryWaitCount, SuccessCount: record.Summary.SuccessCount, FailedCount: record.Summary.FailedCount, CanceledCount: record.Summary.CanceledCount, Context: record.Context, CreatedAt: createdAt}, nil
}

func taskResponseFromRecord(record application.TaskRecord) (taskResponse, error) {
	start, err := requiredTimeValue(record.Task.RangeStart)
	if err != nil {
		return taskResponse{}, err
	}
	end, err := requiredTimeValue(record.Task.RangeEnd)
	if err != nil {
		return taskResponse{}, err
	}
	created, err := requiredTimeValue(record.Task.CreatedAt)
	if err != nil {
		return taskResponse{}, err
	}
	updated, err := requiredTimeValue(record.Task.UpdatedAt)
	if err != nil {
		return taskResponse{}, err
	}
	return taskResponse{
		TaskID: record.Task.ID,
		Run: taskRunIdentity{
			RunID: record.Task.RunID, RunType: record.RunType, TriggerType: record.TriggerType,
		},
		Subscription: taskSubscription{
			SubscriptionID: record.Task.SubscriptionID, Interval: record.SubscriptionInterval,
		},
		Provider: taskProviderIdentity{
			ProviderID: record.ProviderID, ProviderCode: record.ProviderCode,
		},
		Instrument: taskInstrumentIdentity{
			InstrumentID: record.InstrumentID, InstrumentCode: record.InstrumentCode,
		},
		ProviderInstrument: providerInstrumentIdentity{
			ProviderInstrumentID:   record.ProviderInstrumentID,
			ProviderInstrumentCode: record.ProviderInstrumentCode,
			ProviderSymbol:         record.ProviderSymbol,
		},
		RetryOfTaskID: record.Task.RetryOfTaskID,
		RangeStart:    start, RangeEnd: end, Status: record.Task.Status,
		AttemptCount: record.Task.AttemptCount, MaxAttempts: record.Task.MaxAttempts,
		NextAttemptAt: optionalTimeValue(record.Task.NextAttemptAt),
		LockedBy:      record.Task.LockedBy, LockedUntil: optionalTimeValue(record.Task.LockedUntil),
		StartedAt: optionalTimeValue(record.Task.StartedAt), FinishedAt: optionalTimeValue(record.Task.FinishedAt),
		ProviderRequestID: record.Task.ProviderRequestID,
		ErrorCode:         record.Task.ErrorCode, ErrorSummary: record.ErrorSummary, ErrorDetails: record.SafeErrorDetails,
		CanceledBy: record.Task.CanceledBy, CancelReason: record.Task.CancelReason,
		CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func requiredTimeValue(value time.Time) (timeValue, error) {
	instant, err := domain.NewUTCInstant(value)
	return timeValue{instant}, err
}
func optionalTimeValue(value *time.Time) *timeValue {
	if value == nil {
		return nil
	}
	converted, err := requiredTimeValue(*value)
	if err != nil {
		return nil
	}
	return &converted
}
func timeScopeValue(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
func idScopeValue(value *domain.ID) string {
	if value == nil {
		return ""
	}
	return value.String()
}
