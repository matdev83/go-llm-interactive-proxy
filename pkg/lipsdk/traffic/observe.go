package traffic

import (
	"context"
	"time"
)

// Leg identifies one hop in the four-leg observation model (design §10).
type Leg string

const (
	LegCTP Leg = "client_to_proxy"
	LegPTB Leg = "proxy_to_backend"
	LegBTP Leg = "backend_to_proxy"
	LegPTC Leg = "proxy_to_client"
)

// Observation is a structured, non-privileged traffic sample for plugins (redacted path).
type Observation struct {
	Leg         Leg
	TraceID     string
	ALegID      string
	BLegID      string
	PrincipalID string
	SessionID   string
	AttemptSeq  int
	BackendID   string
	FrontendID  string
	Protocol    string
	ContentType string
	Body        []byte
	RecordedAt  time.Time
}

// Observer receives non-mutating traffic observations (design §10).
type Observer interface {
	OnObservation(ctx context.Context, ev Observation) error
}

// NoopObserver drops observations (safe default for tests and early wiring).
type NoopObserver struct{}

func (NoopObserver) OnObservation(context.Context, Observation) error { return nil }

// CaptureMeta correlates a privileged raw capture write without embedding transport types.
type CaptureMeta struct {
	TraceID     string
	ALegID      string
	BLegID      string
	PrincipalID string
	SessionID   string
	AttemptSeq  int
	BackendID   string
	FrontendID  string
}

// RawCaptureSink receives verbatim bytes for privileged capture paths (design §10).
type RawCaptureSink interface {
	WriteRaw(ctx context.Context, leg Leg, meta CaptureMeta, payload []byte) error
}

// DisabledRawCapture rejects raw capture until explicitly granted by core policy.
type DisabledRawCapture struct{}

func (DisabledRawCapture) WriteRaw(context.Context, Leg, CaptureMeta, []byte) error {
	return ErrNotConfigured
}
