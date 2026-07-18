package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"xr-trading/market-info-service/internal/application"
	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/scheduler"
)

const providerStatusPath = "/api/market-info/v1/providers/status"

type ProviderStatusQuery interface {
	List(context.Context) ([]application.ProviderStatus, error)
}

func RegisterProviderStatusRoutes(mux *http.ServeMux, service ProviderStatusQuery, authenticator application.Authenticator) error {
	if mux == nil || service == nil || authenticator == nil {
		return errors.New("provider status route dependencies are required")
	}
	authenticate, err := NewAuthenticationMiddleware(authenticator)
	if err != nil {
		return err
	}
	read, err := NewPermissionMiddleware(application.PermissionOperationsRead)
	if err != nil {
		return err
	}
	handler := &ProviderStatusHandler{service: service}
	mux.Handle("GET "+providerStatusPath, authenticate(read(http.HandlerFunc(handler.list))))
	return nil
}

type ProviderStatusHandler struct{ service ProviderStatusQuery }

func (handler *ProviderStatusHandler) list(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		WriteError(writer, request, application.ValidationError([]application.FieldViolation{{Field: "query", Reason: "is not supported"}}))
		return
	}
	items, err := handler.service.List(request.Context())
	if err != nil {
		WriteError(writer, request, err)
		return
	}
	response := providerStatusListResponse{Items: make([]providerStatusResponse, 0, len(items))}
	for _, item := range items {
		encoded, err := providerStatusResponseFromApplication(item)
		if err != nil {
			WriteError(writer, request, err)
			return
		}
		response.Items = append(response.Items, encoded)
	}
	if err := WriteJSON(writer, http.StatusOK, response); err != nil {
		WriteError(writer, request, err)
	}
}

type providerStatusListResponse struct {
	Items []providerStatusResponse `json:"items"`
}

type providerStatusResponse struct {
	ProviderID          domain.ID                        `json:"provider_id"`
	ProviderCode        domain.Code                      `json:"provider_code"`
	DisplayName         string                           `json:"display_name"`
	ProviderType        string                           `json:"provider_type"`
	ConfiguredStatus    domain.ProviderStatus            `json:"configured_status"`
	HealthStatus        application.ProviderHealthStatus `json:"health_status"`
	LastSuccessAt       *timeValue                       `json:"last_success_at"`
	LastFailureAt       *timeValue                       `json:"last_failure_at"`
	ConsecutiveFailures int                              `json:"consecutive_failures"`
	CheckedAt           timeValue                        `json:"checked_at"`
	Scopes              []providerScopeStatusResponse    `json:"scopes"`
}

type providerScopeStatusResponse struct {
	Market               string                           `json:"market"`
	SessionType          string                           `json:"session_type"`
	Interval             domain.BarInterval               `json:"interval"`
	MarketState          string                           `json:"market_state"`
	HealthStatus         application.ProviderHealthStatus `json:"health_status"`
	FreshnessStatus      scheduler.FreshnessStatus        `json:"freshness_status"`
	DataDelaySeconds     *int64                           `json:"data_delay_seconds"`
	ActiveSubscriptions  int                              `json:"active_subscriptions"`
	DelayedSubscriptions int                              `json:"delayed_subscriptions"`
	NextMarketOpenAt     *timeValue                       `json:"next_market_open_at"`
}

func providerStatusResponseFromApplication(status application.ProviderStatus) (providerStatusResponse, error) {
	checkedAt, err := domain.NewUTCInstant(status.CheckedAt)
	if err != nil || status.ProviderID.IsZero() || status.ProviderCode.IsZero() || status.DisplayName == "" {
		return providerStatusResponse{}, errors.New("provider status service returned invalid data")
	}
	response := providerStatusResponse{
		ProviderID: status.ProviderID, ProviderCode: status.ProviderCode, DisplayName: status.DisplayName,
		ProviderType: strings.ToLower(string(status.ProviderType)), ConfiguredStatus: status.ConfiguredStatus,
		HealthStatus: status.HealthStatus, LastSuccessAt: optionalTimeValue(status.LastSuccessAt),
		LastFailureAt: optionalTimeValue(status.LastFailureAt), ConsecutiveFailures: status.ConsecutiveFailures,
		CheckedAt: timeValue{checkedAt}, Scopes: make([]providerScopeStatusResponse, 0, len(status.Scopes)),
	}
	for _, scope := range status.Scopes {
		response.Scopes = append(response.Scopes, providerScopeStatusResponse{
			Market: scope.Market, SessionType: scope.SessionType, Interval: scope.Interval,
			MarketState: scope.MarketState, HealthStatus: scope.HealthStatus, FreshnessStatus: scope.FreshnessStatus,
			DataDelaySeconds: scope.DataDelaySeconds, ActiveSubscriptions: scope.ActiveSubscriptions,
			DelayedSubscriptions: scope.DelayedSubscriptions, NextMarketOpenAt: optionalTimeValue(scope.NextMarketOpenAt),
		})
	}
	return response, nil
}
