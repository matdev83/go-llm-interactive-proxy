package runtimebundle

import (
	"context"
	"net/http"
	"sort"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// generationOwner is the narrow quiesce/close surface held by a GenerationBundle.
// CandidateRuntime satisfies it; the concrete type stays private to composition.
type generationOwner interface {
	Quiesce(ctx context.Context) error
	Close() error
}

// GenerationBundle is the immutable publication unit for one request-plane
// generation (design Immutable Generation Bundle). Fields are unexported;
// accessors return narrow interfaces, immutable values, or defensive copies.
// It never stores or exposes mutable *config.Config, *runtime.App, *Built, or
// process-owned closers / ProcessServices ownership.
type GenerationBundle struct {
	handler       http.Handler
	executor      *runtime.Executor // private; exposed only via ExecutorView / BackendIDs
	routing       FrozenRoutingView
	frontends     []config.PluginConfig
	registrations []lipsdk.Registration
	httpAuth      []httpauth.Provider
	models        *modelregistry.Runtime
	catalog       *modelcatalog.CatalogRuntime
	backendIDs    []string
	ledger        *ResourceLedger // private; ResourceCount only
	owner         generationOwner
	// terminalProviders is an immutable snapshot of terminal effect providers
	// captured at compile time (task 3.6). It must not share mutable registry state.
	terminalProviders *terminalworkapp.FrozenTerminalProviders

	quiesceOnce sync.Once
	quiesceErr  error
	closeOnce   sync.Once
	closeErr    error
}

var (
	_ runtimehost.OwnedCloser           = (*GenerationBundle)(nil)
	_ runtimehost.QuiesceCloser         = (*GenerationBundle)(nil)
	_ runtimehost.PublishedRequestPlane = (*GenerationBundle)(nil)
	_ runtimehost.ModelViewBinder       = (*GenerationBundle)(nil)
	_ routing.NativeModelResolver       = modelregistry.BoundView{}
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
	regView := b.models.BoundView()
	catView := b.catalog.BoundView()
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
	if b == nil || b.terminalProviders == nil {
		return terminalworkapp.SnapshotTerminalProviders(nil)
	}
	return b.terminalProviders
}

// Handler returns the generation request-plane handler (no listener).
func (b *GenerationBundle) Handler() http.Handler {
	if b == nil {
		return nil
	}
	return b.handler
}

// ExecutorView returns the narrow SDK executor view.
func (b *GenerationBundle) ExecutorView() lipsdk.ExecutorView {
	if b == nil || b.executor == nil {
		return nil
	}
	return b.executor
}

// BackendIDs returns a defensive copy of generation backend instance IDs.
func (b *GenerationBundle) BackendIDs() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.backendIDs...)
}

// Routing returns a defensive copy of the frozen routing view.
func (b *GenerationBundle) Routing() FrozenRoutingView {
	if b == nil {
		return FrozenRoutingView{}
	}
	return FrozenRoutingView{
		DefaultRoute:  b.routing.DefaultRoute,
		RoutePrefixes: append([]string(nil), b.routing.RoutePrefixes...),
	}
}

// RoutePrefixes returns a defensive copy of route-selector prefixes.
func (b *GenerationBundle) RoutePrefixes() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.routing.RoutePrefixes...)
}

// FrozenFrontends returns a defensive copy of frontend plugin rows.
func (b *GenerationBundle) FrozenFrontends() []config.PluginConfig {
	if b == nil {
		return nil
	}
	return freezePluginConfigs(b.frontends)
}

// Registrations returns a defensive deep copy of plugin registrations.
func (b *GenerationBundle) Registrations() []lipsdk.Registration {
	if b == nil {
		return nil
	}
	return freezeRegistrations(b.registrations)
}

// HTTPAuthProviders returns a defensive copy of transport-auth providers.
func (b *GenerationBundle) HTTPAuthProviders() []httpauth.Provider {
	if b == nil || b.httpAuth == nil {
		return nil
	}
	return append([]httpauth.Provider(nil), b.httpAuth...)
}

// ResourceCount returns the current generation-owned ledger entry count.
// Intended for tests and diagnostics; it does not expose mutation controls.
func (b *GenerationBundle) ResourceCount() int {
	if b == nil || b.ledger == nil {
		return 0
	}
	return b.ledger.Len()
}

// Quiesce stops admission-independent generation workers exactly once by
// forwarding to the candidate/ledger owner (req 10.5).
func (b *GenerationBundle) Quiesce(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.quiesceOnce.Do(func() {
		if b.owner != nil {
			b.quiesceErr = b.owner.Quiesce(ctx)
		}
	})
	return b.quiesceErr
}

// Close rolls back/closes generation-owned resources exactly once.
// It never closes ProcessServices.
func (b *GenerationBundle) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		if b.owner != nil {
			b.closeErr = b.owner.Close()
			return
		}
		if b.ledger != nil {
			b.closeErr = b.ledger.Rollback(context.Background())
		}
	})
	return b.closeErr
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
