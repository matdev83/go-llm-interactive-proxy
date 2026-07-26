package response

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// StreamOutcome is the exactly-once terminal disposition for one observer lifecycle.
type StreamOutcome string

const (
	// OutcomeSuccessReleased means the runtime released a successful response_finished
	// toward the frontend encoder (not client transport acknowledgement).
	OutcomeSuccessReleased StreamOutcome = "success_released"
	// OutcomeFailed means the active B-leg failed before successful release.
	OutcomeFailed StreamOutcome = "failed"
	// OutcomeCancelled means the request was cancelled before successful release.
	OutcomeCancelled StreamOutcome = "cancelled"
	// OutcomeClosed means the client closed the stream before successful release.
	OutcomeClosed StreamOutcome = "closed"
	// OutcomeReplaced means a pre-output recv replacement discarded this B-leg.
	OutcomeReplaced StreamOutcome = "replaced"
	// OutcomeGateReplaced means a completion gate replaced the buffered original stream.
	OutcomeGateReplaced StreamOutcome = "gate_replaced"
)

// StreamMeta carries attempt-scoped identifiers for final-stream observers.
// Authoritative session identity lives on Session (SessionView.AuthoritativeSessionID);
// raw session partitions are intentionally not exposed.
// BackendPrefixes is provider-neutral candidate prefix evidence for capture gating;
// runtime clones before open, and observers must treat the slice as immutable.
type StreamMeta struct {
	TraceID         string
	ALegID          string
	BLegID          string
	CandidateKey    string
	BackendID       string
	BackendPrefixes []string
	Model           string
	AttemptSeq      int

	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace workspace.WorkspaceView
}

// Services is a forward-compatible observer service bag. Final-stream observation is
// read-only and does not require State/Aux injection in v1.
type Services struct{}

// StreamObserverFactory opens one StreamObserver for an active surfaced B-leg.
type StreamObserverFactory interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Open(context.Context, StreamMeta, Services) (StreamObserver, error)
}

// StreamObserver receives defensive final canonical events and a single Finish.
type StreamObserver interface {
	Observe(context.Context, lipapi.Event) error
	Finish(context.Context, StreamOutcome) error
}
