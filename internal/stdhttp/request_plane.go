package stdhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

// ComposeStandardHTTP is the canonical HandlerComposer: it builds the complete
// standard http.Handler for one generation from a focused StandardHTTPInput
// without binding a listener, without owning runtime.App, and without
// starting/stopping feature lifecycles (the candidate resource ledger owns
// those). This is the composer injected into runtimebundle.CompileGeneration /
// GenerationCompiler so runtimebundle never imports stdhttp (no import
// cycle; task 3.4). Route registration conflicts are returned as
// [ErrRouteConflict] rather than panicking. Management reload/status routes
// are intentionally absent: they remain process-owned outside this swappable
// request-plane graph (req 12.1).
func ComposeStandardHTTP(ctx context.Context, cfg *config.Config, log *slog.Logger, in StandardHTTPInput) (http.Handler, error) {
	if ctx == nil {
		return nil, errors.New("stdhttp: nil context")
	}
	if cfg == nil {
		return nil, errors.New("stdhttp: nil config")
	}
	if log == nil {
		return nil, errors.New("stdhttp: nil logger")
	}
	if in.Core.Executor == nil {
		return nil, errors.New("stdhttp: nil executor")
	}
	if in.Frontends.Registry == nil {
		return nil, errors.New("stdhttp: nil plugin registry")
	}
	if err := validateStartupSecurity(cfg); err != nil {
		return nil, err
	}
	handler, err := prepareStandardHandler(ctx, cfg, log, in)
	if err != nil {
		return nil, err
	}
	// Intentionally no runtime.App Start/Shutdown: feature lifecycles are owned
	// by the candidate resource ledger (singular Start/Stop).
	return handler, nil
}

// Ensure ComposeStandardHTTP remains assignable to the canonical HandlerComposer.
var _ runtimebundle.HandlerComposer = ComposeStandardHTTP

// ComposeRequestPlane is the transitional (pre-3.5) request-plane composer. It
// remains for compatibility callers that still hold a runtimebundle.RequestPlane
// but is NOT assignable to the canonical runtimebundle.HandlerComposer and must
// not be used by the canonical compiler/bootstrap/reload/command/public path
// (task 3.4; deleted with RequestPlane in task 3.5).
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
