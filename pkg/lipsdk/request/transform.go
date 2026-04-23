package request

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

// RequestMeta is read-only policy and routing context for request-wide transforms.
// AttemptView is unset at this pipeline position (BLeg not allocated yet).
type RequestMeta struct {
	TraceID     string
	Annotations map[string]string
	Principal   execview.PrincipalView
	Session     session.SessionView
	Workspace   workspace.WorkspaceView
}

// Services exposes narrow capabilities for transforms (design §2).
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}

// Transform mutates the canonical call with full visibility over messages, tools, and options.
// It is distinct from [sdkhooks.SubmitHook] (early reject/annotate) and from per-part hooks.
type Transform interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Handle(ctx context.Context, call *lipapi.Call, meta RequestMeta, svc Services) error
}
