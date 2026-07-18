package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
)

const collectionSubscriptionsPath = "/api/market-info/v1/collection-subscriptions"
const collectionSubscriptionsCursorScope = "collection-subscriptions"

var collectionSubscriptionsPageLimits = PageLimits{Default: 50, Maximum: application.MaximumSubscriptionsPageSize}

// SubscriptionManagement is the HTTP-facing subscription use-case contract.
type SubscriptionManagement interface {
	List(context.Context, application.SubscriptionListInput) (application.SubscriptionPage, error)
	Create(context.Context, application.CreateSubscriptionInput) (application.SubscriptionRecord, error)
	Update(context.Context, application.UpdateSubscriptionInput) (application.SubscriptionRecord, error)
}

// RegisterSubscriptionRoutes attaches authenticated subscription management
// endpoints with read/write permissions separated per method.
func RegisterSubscriptionRoutes(mux *http.ServeMux, service SubscriptionManagement, authenticator application.Authenticator) error {
	if mux == nil || service == nil || authenticator == nil {
		return errors.New("subscription routes dependencies are required")
	}
	authenticate, err := NewAuthenticationMiddleware(authenticator)
	if err != nil {
		return err
	}
	readPermission, err := NewPermissionMiddleware(application.PermissionOperationsRead)
	if err != nil {
		return err
	}
	writePermission, err := NewPermissionMiddleware(application.PermissionSubscriptionsManage)
	if err != nil {
		return err
	}
	handler := &SubscriptionHandler{service: service}
	mux.Handle("GET "+collectionSubscriptionsPath, authenticate(readPermission(http.HandlerFunc(handler.list))))
	mux.Handle("POST "+collectionSubscriptionsPath, authenticate(writePermission(http.HandlerFunc(handler.create))))
	mux.Handle("PATCH "+collectionSubscriptionsPath+"/{subscription_id}", authenticate(writePermission(http.HandlerFunc(handler.update))))
	return nil
}

// SubscriptionHandler translates the subscription management protocol.
type SubscriptionHandler struct {
	service SubscriptionManagement
}

func (handler *SubscriptionHandler) list(writer http.ResponseWriter, request *http.Request) {
	input, err := parseSubscriptionListRequest(request)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	page, err := handler.service.List(request.Context(), input)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := subscriptionPageResponse(input, page)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, response); err != nil {
		WriteError(writer, request, err)
	}
}

func (handler *SubscriptionHandler) create(writer http.ResponseWriter, request *http.Request) {
	var body createSubscriptionRequest
	if err := DecodeJSON(writer, request, &body, DefaultMaximumRequestBodyBytes); err != nil {
		WriteError(writer, request, err)
		return
	}
	input, err := createSubscriptionInputFromRequest(body)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	record, err := handler.service.Create(request.Context(), input)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := subscriptionResponseFromRecord(record)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusCreated, subscriptionMutationResponse{Subscription: response}); err != nil {
		WriteError(writer, request, err)
	}
}

func (handler *SubscriptionHandler) update(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseID(request.PathValue("subscription_id"))
	if err != nil {
		WriteError(writer, request, application.ValidationError([]application.FieldViolation{{Field: "subscription_id", Reason: "must be a canonical UUID"}}))
		return
	}
	var body updateSubscriptionRequest
	if err := DecodeJSON(writer, request, &body, DefaultMaximumRequestBodyBytes); err != nil {
		WriteError(writer, request, err)
		return
	}
	record, err := handler.service.Update(request.Context(), application.UpdateSubscriptionInput{
		ID: id, Enabled: body.Enabled, Priority: body.Priority, CloseDelaySeconds: body.CloseDelaySeconds,
		RevisionDelaySeconds: body.RevisionDelaySeconds.Value, RevisionDelaySecondsSet: body.RevisionDelaySeconds.Set,
		Reason: body.Reason,
	})
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response, err := subscriptionResponseFromRecord(record)
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	if err := WriteJSON(writer, http.StatusOK, subscriptionMutationResponse{Subscription: response}); err != nil {
		WriteError(writer, request, err)
	}
}

type createSubscriptionRequest struct {
	Provider             string `json:"provider"`
	InstrumentCode       string `json:"instrument_code"`
	Interval             string `json:"interval"`
	Enabled              *bool  `json:"enabled"`
	Priority             *int   `json:"priority"`
	CloseDelaySeconds    *int   `json:"close_delay_seconds"`
	RevisionDelaySeconds *int   `json:"revision_delay_seconds"`
	Reason               string `json:"reason"`
}

type updateSubscriptionRequest struct {
	Enabled              *bool            `json:"enabled"`
	Priority             *int             `json:"priority"`
	CloseDelaySeconds    *int             `json:"close_delay_seconds"`
	RevisionDelaySeconds nullablePatchInt `json:"revision_delay_seconds"`
	Reason               string           `json:"reason"`
}

// nullablePatchInt preserves PATCH's absent / null / integer distinction.
type nullablePatchInt struct {
	Set   bool
	Value *int
}

func (value *nullablePatchInt) UnmarshalJSON(data []byte) error {
	value.Set = true
	if string(data) == "null" {
		value.Value = nil
		return nil
	}
	var parsed int
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	value.Value = &parsed
	return nil
}

func createSubscriptionInputFromRequest(body createSubscriptionRequest) (application.CreateSubscriptionInput, error) {
	violations := make([]application.FieldViolation, 0, 3)
	if body.Enabled == nil {
		violations = append(violations, application.FieldViolation{Field: "enabled", Reason: "is required"})
	}
	if body.Priority == nil {
		violations = append(violations, application.FieldViolation{Field: "priority", Reason: "is required"})
	}
	if body.CloseDelaySeconds == nil {
		violations = append(violations, application.FieldViolation{Field: "close_delay_seconds", Reason: "is required"})
	}
	if len(violations) > 0 {
		return application.CreateSubscriptionInput{}, application.ValidationError(violations)
	}
	return application.CreateSubscriptionInput{
		ProviderCode: body.Provider, InstrumentCode: body.InstrumentCode, Interval: body.Interval,
		Enabled: *body.Enabled, Priority: *body.Priority, CloseDelaySeconds: *body.CloseDelaySeconds,
		RevisionDelaySeconds: body.RevisionDelaySeconds, Reason: body.Reason,
	}, nil
}

type subscriptionPageResponseBody struct {
	Items      []subscriptionResponse `json:"items"`
	NextCursor *string                `json:"next_cursor"`
}

type subscriptionMutationResponse struct {
	Subscription subscriptionResponse `json:"subscription"`
}

type subscriptionResponse struct {
	SubscriptionID         domain.ID   `json:"subscription_id"`
	Provider               domain.Code `json:"provider"`
	InstrumentCode         domain.Code `json:"instrument_code"`
	ProviderInstrumentID   domain.ID   `json:"provider_instrument_id"`
	ProviderInstrumentCode domain.Code `json:"provider_instrument_code"`
	ProviderSymbol         string      `json:"provider_symbol"`
	Interval               string      `json:"interval"`
	Enabled                bool        `json:"enabled"`
	Priority               int16       `json:"priority"`
	CloseDelaySeconds      int         `json:"close_delay_seconds"`
	RevisionDelaySeconds   *int        `json:"revision_delay_seconds"`
	CreatedAt              timeValue   `json:"created_at"`
	UpdatedAt              timeValue   `json:"updated_at"`
}

// timeValue reuses UTCInstant's strict RFC3339Nano JSON contract while keeping
// application records on time.Time.
type timeValue struct{ domain.UTCInstant }

func parseSubscriptionListRequest(request *http.Request) (application.SubscriptionListInput, error) {
	if request == nil || request.URL == nil {
		return application.SubscriptionListInput{}, errors.New("HTTP request is required")
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return application.SubscriptionListInput{}, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is malformed"}})
	}
	if err := validateSubscriptionQueryKeys(values); err != nil {
		return application.SubscriptionListInput{}, err
	}
	provider, err := singleQueryValue(values, "provider", false)
	if err != nil {
		return application.SubscriptionListInput{}, err
	}
	instrumentCode, err := singleQueryValue(values, "instrument_code", false)
	if err != nil {
		return application.SubscriptionListInput{}, err
	}
	interval, err := singleQueryValue(values, "interval", false)
	if err != nil {
		return application.SubscriptionListInput{}, err
	}
	var enabled *bool
	enabledValue, err := singleQueryValue(values, "enabled", false)
	if err != nil {
		return application.SubscriptionListInput{}, err
	}
	if enabledValue != "" {
		parsed, parseErr := strconv.ParseBool(enabledValue)
		if parseErr != nil || (enabledValue != "true" && enabledValue != "false") {
			return application.SubscriptionListInput{}, application.ValidationError([]application.FieldViolation{{Field: "enabled", Reason: "must be true or false"}})
		}
		enabled = &parsed
	}
	limitValue, err := singleQueryValue(values, "limit", false)
	if err != nil {
		return application.SubscriptionListInput{}, err
	}
	limit, err := ParsePageSize(limitValue, collectionSubscriptionsPageLimits)
	if err != nil {
		return application.SubscriptionListInput{}, err
	}
	input := application.SubscriptionListInput{ProviderCode: provider, InstrumentCode: instrumentCode, Interval: interval, Enabled: enabled, Limit: limit}
	cursor, err := singleQueryValue(values, "cursor", false)
	if err != nil {
		return application.SubscriptionListInput{}, err
	}
	if cursor != "" {
		positions, decodeErr := DecodeCursor(cursor, collectionSubscriptionsCursorScope, 5)
		if decodeErr != nil || positions[0] != provider || positions[1] != instrumentCode || positions[2] != interval || positions[3] != enabledScopeValue(enabled) {
			return application.SubscriptionListInput{}, invalidCursor()
		}
		id, parseErr := domain.ParseID(positions[4])
		if parseErr != nil {
			return application.SubscriptionListInput{}, invalidCursor()
		}
		input.AfterID = &id
	}
	return input, nil
}

func validateSubscriptionQueryKeys(values url.Values) error {
	allowed := map[string]struct{}{"provider": {}, "instrument_code": {}, "interval": {}, "enabled": {}, "limit": {}, "cursor": {}}
	unknown := make([]string, 0)
	for key := range values {
		if _, exists := allowed[key]; !exists {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	violations := make([]application.FieldViolation, 0, len(unknown))
	for _, key := range unknown {
		violations = append(violations, application.FieldViolation{Field: key, Reason: "is not supported"})
	}
	return application.ValidationError(violations)
}

func subscriptionPageResponse(input application.SubscriptionListInput, page application.SubscriptionPage) (subscriptionPageResponseBody, error) {
	response := subscriptionPageResponseBody{Items: make([]subscriptionResponse, 0, len(page.Items))}
	for _, record := range page.Items {
		item, err := subscriptionResponseFromRecord(record)
		if err != nil {
			return subscriptionPageResponseBody{}, err
		}
		response.Items = append(response.Items, item)
	}
	if page.NextAfterID != nil {
		cursor, err := EncodeCursor(collectionSubscriptionsCursorScope, input.ProviderCode, input.InstrumentCode, input.Interval, enabledScopeValue(input.Enabled), page.NextAfterID.String())
		if err != nil {
			return subscriptionPageResponseBody{}, err
		}
		response.NextCursor = &cursor
	}
	return response, nil
}

func subscriptionResponseFromRecord(record application.SubscriptionRecord) (subscriptionResponse, error) {
	createdAt, err := domain.NewUTCInstant(record.Subscription.CreatedAt)
	if err != nil {
		return subscriptionResponse{}, err
	}
	updatedAt, err := domain.NewUTCInstant(record.Subscription.UpdatedAt)
	if err != nil {
		return subscriptionResponse{}, err
	}
	return subscriptionResponse{
		SubscriptionID: record.Subscription.ID, Provider: record.ProviderCode, InstrumentCode: record.InstrumentCode,
		ProviderInstrumentID: record.Subscription.ProviderInstrumentID, ProviderInstrumentCode: record.ProviderInstrumentCode,
		ProviderSymbol: record.ProviderSymbol, Interval: record.Subscription.Interval, Enabled: record.Subscription.Enabled,
		Priority: record.Subscription.Priority, CloseDelaySeconds: record.Subscription.CloseDelaySeconds,
		RevisionDelaySeconds: record.Subscription.RevisionDelaySeconds,
		CreatedAt:            timeValue{createdAt}, UpdatedAt: timeValue{updatedAt},
	}, nil
}

func enabledScopeValue(enabled *bool) string {
	if enabled == nil {
		return ""
	}
	return strconv.FormatBool(*enabled)
}
