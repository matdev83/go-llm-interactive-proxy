package runtimebundle

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

type GenerationRuntime interface {
	runtimehost.PublishedRequestPlane
	runtimehost.ExecutorProvider
	runtimehost.PublishedWorkStarter
	runtimehost.ModelViewBinder
	runtimehost.BackendFactoryKindCounter
	TerminalProviders() terminalworkapp.TerminalProviderView
	ReadinessReport() controlplane.ReadinessReportReader
}

type generationExecution struct {
	executor   *runtime.Executor
	backendIDs []string
}

type generationHTTPPublication struct {
	handler       http.Handler
	routing       FrozenRoutingView
	frontends     []config.PluginConfig
	registrations []lipsdk.Registration
	httpAuth      []httpauth.Provider
}

type generationModelViews struct {
	models  *modelregistry.Runtime
	catalog *modelcatalog.CatalogRuntime
}

type generationOperations struct {
	terminalProviders        *terminalworkapp.FrozenTerminalProviders
	terminalDecisionProvider terminaldecision.Provider
	readiness                controlplane.ReadinessReportReader
}

type GenerationBundle struct {
	keepwarm         *keepwarm.Manager
	keepwarmRegistry *keepwarm.ManagerRegistry
	keepwarmID       uint64
	execution        generationExecution
	publication      generationHTTPPublication
	models           generationModelViews
	operations       generationOperations
	ledger           *ResourceLedger
}

var (
	_ GenerationRuntime                     = (*GenerationBundle)(nil)
	_ runtimehost.OwnedCloser               = (*GenerationBundle)(nil)
	_ runtimehost.QuiesceCloser             = (*GenerationBundle)(nil)
	_ runtimehost.PublishedRequestPlane     = (*GenerationBundle)(nil)
	_ runtimehost.PublishedWorkStarter      = (*GenerationBundle)(nil)
	_ runtimehost.ModelViewBinder           = (*GenerationBundle)(nil)
	_ runtimehost.ExecutorProvider          = (*GenerationBundle)(nil)
	_ runtimehost.BackendFactoryKindCounter = (*GenerationBundle)(nil)
	_ routing.NativeModelResolver           = modelregistry.BoundView{}
)

func (b *GenerationBundle) BindModelViews(ctx context.Context) context.Context {
	ctx = ctxOrBackground(ctx)
	if b == nil {
		return ctx
	}
	regView := b.models.models.BoundView()
	catView := b.models.catalog.BoundView()
	var configGen int64
	var configFP string
	if rb, ok := runtimehost.BindingFromContext(ctx); ok {
		meta := rb.Meta()
		configGen = meta.ID
		configFP = meta.PublicFingerprint
	}
	id := modelview.Derive(configGen, configFP, regView.Generation(), catView.Generation())
	ctx = modelregistry.WithBoundView(ctx, regView)
	ctx = modelcatalog.WithBoundView(ctx, catView)
	ctx = routing.WithNativeModelResolver(ctx, regView)
	ctx = modelview.WithIdentity(ctx, id)
	return ctx
}

func (b *GenerationBundle) TerminalProviders() terminalworkapp.TerminalProviderView {
	if b == nil || b.operations.terminalProviders == nil {
		return terminalworkapp.SnapshotTerminalProviders(nil)
	}
	return b.operations.terminalProviders
}

// TerminalDecisionProvider returns the provider frozen into this generation.
// The returned instance is never rebound while the generation is published;
// request admission snapshots the same value for the request lifetime.
func (b *GenerationBundle) TerminalDecisionProvider() terminaldecision.Provider {
	if b == nil {
		return nil
	}
	return b.operations.terminalDecisionProvider
}

func (b *GenerationBundle) Handler() http.Handler {
	if b == nil {
		return nil
	}
	return b.publication.handler
}

func (b *GenerationBundle) ExecutorView() lipsdk.ExecutorView {
	if b == nil || b.execution.executor == nil {
		return nil
	}
	return b.execution.executor
}

func (b *GenerationBundle) ReadinessReport() controlplane.ReadinessReportReader {
	if b == nil {
		return nil
	}
	return b.operations.readiness
}

func (b *GenerationBundle) BackendIDs() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.execution.backendIDs...)
}

func (b *GenerationBundle) BackendFactoryKindCounts() map[string]int {
	if b == nil || len(b.publication.registrations) == 0 {
		return nil
	}
	out := make(map[string]int)
	for _, r := range b.publication.registrations {
		if !r.Enabled || r.Kind != lipsdk.PluginKindBackend {
			continue
		}
		key := r.RegistryFactoryKey()
		if key == "" {
			continue
		}
		out[key]++
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (b *GenerationBundle) Routing() FrozenRoutingView {
	if b == nil {
		return FrozenRoutingView{}
	}
	return FrozenRoutingView{
		DefaultRoute:  b.publication.routing.DefaultRoute,
		RoutePrefixes: append([]string(nil), b.publication.routing.RoutePrefixes...),
	}
}

func (b *GenerationBundle) RoutePrefixes() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.publication.routing.RoutePrefixes...)
}

func (b *GenerationBundle) FrozenFrontends() []config.PluginConfig {
	if b == nil {
		return nil
	}
	return freezePluginConfigs(b.publication.frontends)
}

func (b *GenerationBundle) Registrations() []lipsdk.Registration {
	if b == nil {
		return nil
	}
	return freezeRegistrations(b.publication.registrations)
}

func (b *GenerationBundle) HTTPAuthProviders() []httpauth.Provider {
	if b == nil || b.publication.httpAuth == nil {
		return nil
	}
	return append([]httpauth.Provider(nil), b.publication.httpAuth...)
}

func (b *GenerationBundle) ResourceCount() int {
	if b == nil || b.ledger == nil {
		return 0
	}
	return b.ledger.Len()
}

func (b *GenerationBundle) StartPublished(ctx context.Context) error {
	if b == nil || b.ledger == nil {
		return nil
	}
	return b.ledger.Publish(ctx)
}

func (b *GenerationBundle) Quiesce(ctx context.Context) error {
	if b == nil {
		return nil
	}
	var err error
	if b.keepwarmRegistry != nil && b.keepwarmID != 0 {
		if unregErr := b.keepwarmRegistry.Unregister(b.keepwarmID); unregErr != nil && !errors.Is(unregErr, keepwarm.ErrManagerNotRegistered) {
			err = unregErr
		}
	}
	if b.keepwarm != nil {
		err = errors.Join(err, b.keepwarm.Quiesce(ctx))
	}
	if b.ledger != nil {
		err = errors.Join(err, b.ledger.Quiesce(ctx))
	}
	return err
}

func (b *GenerationBundle) Close() error {
	if b == nil {
		return nil
	}
	var err error
	// Close is also used for unpublished/rollback generations, so it must
	// perform the same registry detachment as Quiesce before stopping the
	// manager. Otherwise the process-owned registry retains a retired manager.
	if b.keepwarmRegistry != nil && b.keepwarmID != 0 {
		if unregErr := b.keepwarmRegistry.Unregister(b.keepwarmID); unregErr != nil && !errors.Is(unregErr, keepwarm.ErrManagerNotRegistered) {
			err = unregErr
		}
	}
	if b.keepwarm != nil {
		err = errors.Join(err, b.keepwarm.Quiesce(context.Background()))
	}
	if b.ledger != nil {
		err = errors.Join(err, b.ledger.Close(context.Background()))
	}
	return err
}

func backendIDsOf(exec *runtime.Executor) []string {
	if exec == nil || exec.Backends == nil {
		return nil
	}
	ids := make([]string, 0, len(exec.Backends))
	for id := range exec.Backends {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func newGenerationBundle(in generationBundleInput) *GenerationBundle {
	return &GenerationBundle{
		keepwarm:         in.keepwarm,
		keepwarmRegistry: in.keepwarmRegistry,
		keepwarmID:       in.keepwarmID,
		execution: generationExecution{
			executor:   in.executor,
			backendIDs: append([]string(nil), in.backendIDs...),
		},
		publication: generationHTTPPublication{
			handler:       in.handler,
			routing:       in.routing,
			frontends:     freezePluginConfigs(in.frontends),
			registrations: freezeRegistrations(in.registrations),
			httpAuth:      append([]httpauth.Provider(nil), in.httpAuth...),
		},
		models: generationModelViews{
			models:  in.models,
			catalog: in.catalog,
		},
		operations: generationOperations{
			terminalProviders:        in.terminalProviders,
			terminalDecisionProvider: in.terminalDecisionProvider,
			readiness:                in.readiness,
		},
		ledger: in.ledger,
	}
}

type generationBundleInput struct {
	handler                  http.Handler
	executor                 *runtime.Executor
	routing                  FrozenRoutingView
	frontends                []config.PluginConfig
	registrations            []lipsdk.Registration
	httpAuth                 []httpauth.Provider
	models                   *modelregistry.Runtime
	catalog                  *modelcatalog.CatalogRuntime
	backendIDs               []string
	ledger                   *ResourceLedger
	terminalProviders        *terminalworkapp.FrozenTerminalProviders
	terminalDecisionProvider terminaldecision.Provider
	readiness                controlplane.ReadinessReportReader
	keepwarm                 *keepwarm.Manager
	keepwarmRegistry         *keepwarm.ManagerRegistry
	keepwarmID               uint64
}
