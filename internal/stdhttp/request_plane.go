package stdhttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

// ComposeRequestPlane builds the complete standard request-plane http.Handler
// for one generation without binding a listener, without owning runtime.App,
// and without starting/stopping feature lifecycles (ledger owns those).
//
// This is the HandlerComposer injected into runtimebundle.CompileGeneration so
// runtimebundle never imports stdhttp (no import cycle). Route registration
// conflicts are returned as [ErrRouteConflict] rather than panicking.
// Management reload/status routes are intentionally absent: they remain
// process-owned outside this swappable request-plane graph (req 12.1).
func ComposeRequestPlane(ctx context.Context, plane runtimebundle.RequestPlane) (http.Handler, error) {
	if ctx == nil {
		return nil, errors.New("stdhttp: nil context")
	}
	cfg := plane.StackConfig()
	if cfg == nil {
		return nil, errors.New("stdhttp: nil request-plane config projection")
	}
	log := plane.Logger()
	if log == nil {
		return nil, errors.New("stdhttp: nil logger")
	}
	if plane.Executor() == nil {
		return nil, errors.New("stdhttp: nil executor")
	}
	if plane.PluginRegistry() == nil {
		return nil, errors.New("stdhttp: nil plugin registry")
	}
	if err := validateStartupSecurity(cfg); err != nil {
		return nil, err
	}

	input := standardHTTPInputFromRequestPlane(plane)
	handler, err := prepareStandardHandler(ctx, cfg, log, input)
	if err != nil {
		return nil, err
	}
	// Intentionally no runtime.App Start/Shutdown: feature lifecycles are owned
	// by the candidate resource ledger (singular Start/Stop).
	return handler, nil
}

// Ensure ComposeRequestPlane remains assignable to the generation HandlerComposer.
var _ runtimebundle.HandlerComposer = ComposeRequestPlane
