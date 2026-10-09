package systemonecompat

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type failureUsageError struct {
	cause error
	usage lipapi.Event
}

func (e *failureUsageError) Error() string { return e.cause.Error() }

func (e *failureUsageError) Unwrap() error { return e.cause }

func withFailureUsage(body []byte, cause error) error {
	var envelope struct {
		Usage wireUsage `json:"usage"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return cause
	}
	u := envelope.Usage
	if u.Input == nil && u.Output == nil && u.Cost == nil {
		return cause
	}
	usage, err := u.event()
	if err != nil {
		return cause // Invalid accounting evidence is never repaired or invented.
	}
	usage.Accounting = lipapi.UsageAccountingMetadata{
		Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
		Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "systemone:failed-attempt-usage",
	}
	return &failureUsageError{cause: cause, usage: usage}
}

// failureUsageStream delivers only the pre-output failure. The existing host-only
// sideband retains provider evidence even though no answer/event is released.
type failureUsageStream struct {
	*lipapi.FixedEventStream
	failure  error
	mu       sync.Mutex
	evidence []lipapi.Event
}

func (s *failureUsageStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	return lipapi.Event{}, s.failure
}

func (s *failureUsageStream) DrainUsageEvidence() []lipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	evidence := s.evidence
	s.evidence = nil
	return evidence
}
