package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routeoverride"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	adminov "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/routeoverride"
)

type generationSelectorValidator struct {
	aliases        *routing.AliasResolver
	defaultBackend string
	knownBackends  map[string]struct{}
	execResolver   routing.BackendExecutionResolver
	policy         config.ExecutionCompositionPolicy
}

func (v generationSelectorValidator) ValidateSelector(_ context.Context, raw string) error {
	sel, err := routing.CompileSelector(raw, v.aliases, v.defaultBackend)
	if err != nil {
		return err
	}
	if err := routing.RejectUnknownBackends(sel, v.knownBackends); err != nil {
		return err
	}
	return routing.ValidateExecutionComposition(sel, v.execResolver, v.policy)
}

func knownBackendsOf(exec *runtime.Executor) map[string]struct{} {
	if exec == nil {
		return nil
	}
	out := make(map[string]struct{}, len(exec.Backends))
	for id := range exec.Backends {
		out[id] = struct{}{}
	}
	return out
}

func bindGenerationRouteOverride(ps *ProcessServices, cfg *config.Config, exec *runtime.Executor, nowFn func() time.Time) (http.Handler, error) {
	if cfg == nil || !cfg.Routing.OverrideAdmin.Enabled {
		return nil, nil
	}
	if ps == nil || ps.RouteOverrideStore == nil || exec == nil {
		return nil, fmt.Errorf("runtimebundle: routing.override_admin.enabled requires a continuity store that implements routeoverride.Store")
	}
	svc, err := routeoverride.NewService(ps.RouteOverrideStore, generationSelectorValidator{
		aliases:        exec.SelectorAliases,
		defaultBackend: exec.DefaultBackend,
		knownBackends:  knownBackendsOf(exec),
		execResolver:   exec.BackendExecutionResolver,
		policy:         exec.ExecutionCompositionPolicy,
	}, nowFn)
	if err != nil {
		return nil, err
	}
	return adminov.NewHandler(adminov.Options{
		Service:      svc,
		MaxBodyBytes: cfg.Routing.OverrideAdmin.MaxBodyBytes,
		Log:          ps.Logger,
	})
}
