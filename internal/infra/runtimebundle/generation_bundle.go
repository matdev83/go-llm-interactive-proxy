package runtimebundle

import (
	"context"
	"net/http"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// GenerationRuntime is the canonical immutable publication and generation-resource
// ownership contract (design GenerationRuntime; Task 3.3). Concrete runtimes
// satisfy it directly — no generationOwner delegate, CandidateRuntime owner, or
// generic dependency lookup surface.
type GenerationRuntime interface {
	runtimehost.PublishedRequestPlane
	runtimehost.ExecutorProvider
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
	terminalProviders *terminalworkapp.FrozenTerminalProviders
	readiness         controlplane.ReadinessReportReader
}

// GenerationBundle is the concrete GenerationRuntime: immutable publication unit
// for one request-plane generation. Fields are private cohesive groups plus the
// canonical ResourceLedger pointer; accessors return narrow interfaces,
// immutable values, or defensive copies. It never stores CandidateRuntime,
// generationOwner, mutable *config.Config, *runtime.App, *Built, RequestPlane,
// ProcessServices ownership, or a dependency map. Lifecycle is delegated
// directly to the ledger (task 7.2).
type GenerationBundle struct {
	execution   generationExecution
	publication generationHTTPPublication
	models      generationModelViews
	operations  generationOperations
	ledger      *ResourceLedger
}

var (
	_ GenerationRuntime                     = (*GenerationBundle)(nil)
	_ runtimehost.OwnedCloser               = (*GenerationBundle)(nil)
	_ runtimehost.QuiesceCloser             = (*GenerationBundle)(nil)
	_ runtimehost.PublishedRequestPlane     = (*GenerationBundle)(nil)
	_ runtimehost.ModelViewBinder           = (*GenerationBundle)(nil)
	_ runtimehost.ExecutorProvider          = (*GenerationBundle)(nil)
	_ runtimehost.BackendFactoryKindCounter = (*GenerationBundle)(nil)
	_ routing.NativeModelResolver           = modelregistry.BoundView{}
)

// BindModelViews captures this generation's model-registry and catalog
// publications into ctx exactly once for the logical request (req 9.4-9.5).
// It also attaches one aggregate model-view identity for diagnostics/ETag.
func (b *GenerationBundle) BindModelViews(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
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
	// Attach the same frozen registry as the routing native resolver so the
	// executor can bind leaf NativeModel without importing modelregistry.
	ctx = routing.WithNativeModelResolver(ctx, regView)
	ctx = modelview.WithIdentity(ctx, id)
	return ctx
}

// TerminalProviders returns this generation's immutable terminal-effect provider view.
func (b *GenerationBundle) TerminalProviders() terminalworkapp.TerminalProviderView {
	if b == nil || b.operations.terminalProviders == nil {
		return terminalworkapp.SnapshotTerminalProviders(nil)
	}
	return b.operations.terminalProviders
}

// Handler returns the generation request-plane handler (no listener).
func (b *GenerationBundle) Handler() http.Handler {
	if b == nil {
		return nil
	}
	return b.publication.handler
}

// ExecutorView returns the narrow SDK executor view.
func (b *GenerationBundle) ExecutorView() lipsdk.ExecutorView {
	if b == nil || b.execution.executor == nil {
		return nil
	}
	return b.execution.executor
}

// ReadinessReport returns the generation readiness report service, or nil.
func (b *GenerationBundle) ReadinessReport() controlplane.ReadinessReportReader {
	if b == nil {
		return nil
	}
	return b.operations.readiness
}

// BackendIDs returns a defensive copy of generation backend instance IDs.
func (b *GenerationBundle) BackendIDs() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.execution.backendIDs...)
}

// BackendFactoryKindCounts returns enabled backend factory-kind occurrence
// counts for LiveFactoryKinds admission (task 5.1 / req 8.8).
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

// Routing returns a defensive copy of the frozen routing view.
func (b *GenerationBundle) Routing() FrozenRoutingView {
	if b == nil {
		return FrozenRoutingView{}
	}
	return FrozenRoutingView{
		DefaultRoute:  b.publication.routing.DefaultRoute,
		RoutePrefixes: append([]string(nil), b.publication.routing.RoutePrefixes...),
	}
}

// RoutePrefixes returns a defensive copy of route-selector prefixes.
func (b *GenerationBundle) RoutePrefixes() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.publication.routing.RoutePrefixes...)
}

// FrozenFrontends returns a defensive copy of frontend plugin rows.
func (b *GenerationBundle) FrozenFrontends() []config.PluginConfig {
	if b == nil {
		return nil
	}
	return freezePluginConfigs(b.publication.frontends)
}

// Registrations returns a defensive deep copy of plugin registrations.
func (b *GenerationBundle) Registrations() []lipsdk.Registration {
	if b == nil {
		return nil
	}
	return freezeRegistrations(b.publication.registrations)
}

// HTTPAuthProviders returns a defensive copy of transport-auth providers.
func (b *GenerationBundle) HTTPAuthProviders() []httpauth.Provider {
	if b == nil || b.publication.httpAuth == nil {
		return nil
	}
	return append([]httpauth.Provider(nil), b.publication.httpAuth...)
}

// ResourceCount returns the current generation-owned ledger entry count.
// Intended for tests and diagnostics; it does not expose mutation controls.
func (b *GenerationBundle) ResourceCount() int {
	if b == nil || b.ledger == nil {
		return 0
	}
	return b.ledger.Len()
}

// Quiesce stops admission-independent generation workers via the canonical
// ResourceLedger (req 10.5 / 8.3-8.4).
func (b *GenerationBundle) Quiesce(ctx context.Context) error {
	if b == nil || b.ledger == nil {
		return nil
	}
	return b.ledger.Quiesce(ctx)
}

// Close rolls back/closes generation-owned resources via the canonical ledger.
// It never closes ProcessServices.
func (b *GenerationBundle) Close() error {
	if b == nil || b.ledger == nil {
		return nil
	}
	return b.ledger.Close(context.Background())
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
			terminalProviders: in.terminalProviders,
			readiness:         in.readiness,
		},
		ledger: in.ledger,
	}
}

type generationBundleInput struct {
	handler           http.Handler
	executor          *runtime.Executor
	routing           FrozenRoutingView
	frontends         []config.PluginConfig
	registrations     []lipsdk.Registration
	httpAuth          []httpauth.Provider
	models            *modelregistry.Runtime
	catalog           *modelcatalog.CatalogRuntime
	backendIDs        []string
	ledger            *ResourceLedger
	terminalProviders *terminalworkapp.FrozenTerminalProviders
	readiness         controlplane.ReadinessReportReader
}
