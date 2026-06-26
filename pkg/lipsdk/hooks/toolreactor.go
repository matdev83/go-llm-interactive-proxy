package hooks

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// ToolDecision is the outcome of a tool reactor for one tool event.
type ToolDecision int

const (
	ToolDecisionUnspecified ToolDecision = iota
	ToolPass
	ToolRewrite
	ToolSwallow
	ToolReplace
)

// ToolMeta carries stream and session context for tool reactors.
// Principal, Session, and Workspace are optional: the standard hook bus copies them from the
// request-scoped views attached to context when present (R9 policy context).
type ToolMeta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int

	Principal execview.PrincipalView
	Session   session.SessionView
	Workspace workspace.WorkspaceView
}

// ToolReactor observes canonical tool lifecycle events and may rewrite output.
type ToolReactor interface {
	ID() string
	Order() int
	HandleToolEvent(ctx context.Context, te lipapi.ToolEvent, meta ToolMeta) (ToolDecision, lipapi.ToolEvent, error)
}

// ToolReactorErrorPolicy selects how the hook bus treats a non-nil error from HandleToolEvent.
type ToolReactorErrorPolicy int

const (
	ToolReactorErrorPolicyUnspecified ToolReactorErrorPolicy = iota
	// ToolReactorErrorsFailOpen preserves the current event and continues the chain (default).
	ToolReactorErrorsFailOpen
	// ToolReactorErrorsFailClosed stops the chain and surfaces the error to the stream runner.
	ToolReactorErrorsFailClosed
	// ToolReactorErrorsSwallowEvent drops the current tool event (same effect as ToolSwallow).
	ToolReactorErrorsSwallowEvent
)
