package provider

import (
	"errors"
	"time"
)

// Window is one rolling usage limit reported by a provider.
type Window struct {
	Used         int64
	Limit        int64
	DurationMins int64
	Reset        time.Time
}

// Usage contains the two rate-limit windows shown by the device.
type Usage struct {
	Primary   Window
	Secondary Window
}

// Provider loads credentials and fetches usage for the selected service.
// Implementations reload credentials on every call so an external login can
// repair an expired session without restarting clue.
type Provider interface {
	CheckCredentials() error
	FetchUsage() (*Usage, error)
}

var ErrAuth = errors.New("authentication failed")
