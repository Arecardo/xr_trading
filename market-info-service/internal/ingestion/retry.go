package ingestion

import (
	"encoding/json"
	"errors"
	"time"

	"xr-trading/market-info-service/internal/domain"
	"xr-trading/market-info-service/internal/ingestion/ports"
)

const (
	taskStatusRetryWait = "retry_wait"
	taskStatusFailed    = "failed"
)

var defaultRetryBackoffs = []time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
	time.Hour,
}

type failureClassification struct {
	code       string
	message    string
	details    []byte
	retryable  bool
	retryAfter *time.Duration
}

func classifyFailure(err error) failureClassification {
	if providerError, ok := ports.AsProviderError(err); ok {
		details, _ := json.Marshal(map[string]string{"provider_code": providerError.ProviderCode.String()})
		return failureClassification{
			code: string(providerError.Code), message: providerError.Message, details: details,
			retryable: providerError.Code.Retryable(), retryAfter: providerError.RetryAfter,
		}
	}
	switch {
	case errors.Is(err, domain.ErrDatabaseUnavailable):
		return failureClassification{code: "database_unavailable", message: "database temporarily unavailable", details: []byte(`{}`), retryable: true}
	case errors.Is(err, domain.ErrRetryable):
		return failureClassification{code: "temporary_failure", message: "service temporarily unavailable", details: []byte(`{}`), retryable: true}
	case errors.Is(err, ports.ErrAdapterNotRegistered):
		return failureClassification{code: "adapter_not_registered", message: "provider adapter is not registered", details: []byte(`{}`)}
	case errors.Is(err, ports.ErrProviderLimitExceeded):
		return failureClassification{code: "provider_limit_exceeded", message: "provider pagination limit was exceeded", details: []byte(`{}`)}
	case errors.Is(err, domain.ErrNotFound):
		return failureClassification{code: "configuration_not_found", message: "ingestion configuration was not found", details: []byte(`{}`)}
	case errors.Is(err, domain.ErrInvalidData), errors.Is(err, domain.ErrInvalidState), errors.Is(err, domain.ErrReferenceViolation), errors.Is(err, domain.ErrConflict):
		return failureClassification{code: "configuration_invalid", message: "ingestion configuration or data contract is invalid", details: []byte(`{}`)}
	default:
		return failureClassification{code: "internal_error", message: "ingestion task failed", details: []byte(`{}`)}
	}
}

func (service *Service) failureCommit(claim domain.TaskClaim, failure error, finishedAt time.Time) FailureCommit {
	classified := classifyFailure(failure)
	commit := FailureCommit{
		Claim: claim, Status: taskStatusFailed, ErrorCode: classified.code,
		ErrorMessage: classified.message, ErrorDetails: append([]byte(nil), classified.details...), FinishedAt: finishedAt,
	}
	if !classified.retryable || claim.Task.AttemptCount >= claim.Task.MaxAttempts {
		return commit
	}
	delay := service.retryBackoff(claim.Task.AttemptCount)
	if classified.retryAfter != nil {
		delay = min(*classified.retryAfter, service.config.MaximumRetryDelay)
	}
	nextAttemptAt := finishedAt.Add(delay)
	commit.Status = taskStatusRetryWait
	commit.NextAttemptAt = &nextAttemptAt
	return commit
}

func (service *Service) retryBackoff(attemptCount int) time.Duration {
	index := attemptCount - 1
	if index < 0 {
		index = 0
	}
	if index >= len(service.config.RetryBackoffs) {
		index = len(service.config.RetryBackoffs) - 1
	}
	return min(service.config.RetryBackoffs[index], service.config.MaximumRetryDelay)
}
