package ports

import "errors"

// Stable registry errors let scheduling and ingestion distinguish a missing
// runtime adapter from an unsupported provider capability without inspecting
// error text.
var (
	ErrAdapterNotRegistered  = errors.New("adapter not registered")
	ErrCapabilityUnsupported = errors.New("provider capability unsupported")
	ErrProviderLimitExceeded = errors.New("provider request limit exceeded")
)
