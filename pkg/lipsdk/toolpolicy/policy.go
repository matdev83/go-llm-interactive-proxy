package toolpolicy

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// Decision is the policy outcome for a canonical tool-call lifecycle event.
type Decision int

const (
	DecisionUnspecified Decision = iota
	DecisionAllow
	DecisionDeny
)

// Meta carries read-only execution context for tool-call policy decisions.
type Meta struct {
	TraceID    string
	ALegID     string
	BLegID     string
	AttemptSeq int

	Principal execview.PrincipalView
	Session   session.SessionView
	Workspace workspace.WorkspaceView
}

// Services exposes narrow capabilities for policy hooks.
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}

// Policy decides whether a canonical model-emitted tool event is allowed to continue.
// It is distinct from toolcatalog.Filter, which shapes the advertised catalog before
// backend execution, and from hooks.ToolReactor, which can rewrite tool events.
type Policy interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Handle(ctx context.Context, event lipapi.ToolEvent, meta Meta, svc Services) (Decision, error)
}
