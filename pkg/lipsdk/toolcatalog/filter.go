package toolcatalog

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

// CatalogMeta is read-only context for catalog filters (no provider types).
type CatalogMeta struct {
	TraceID     string
	Annotations map[string]string
	Principal   execview.PrincipalView
	Session     session.SessionView
	Workspace   workspace.WorkspaceView
}

// Services exposes narrow capabilities for catalog filters.
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}

// Filter removes or annotates tool definitions before backend translation (R9).
type Filter interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Handle(ctx context.Context, call *lipapi.Call, meta CatalogMeta, svc Services) error
}
