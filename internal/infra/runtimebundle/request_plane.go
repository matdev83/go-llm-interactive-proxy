package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	ssessionapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// HandlerComposer builds a complete standard http.Handler from one focused,
// lifecycle-free StandardHTTPInput projection without binding a listener or
// owning process services. cfg/log are explicit parameters (not carried on the
// input) because prepareStandardHandler needs the frozen candidate config and
// process logger for middleware composition outside the mount groups.
// Implemented by stdhttp to avoid an import cycle with this package (design
// Generation Compiler; task 3.4).
type HandlerComposer func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error)

// FrozenRoutingView is an immutable routing projection for a generation.
type FrozenRoutingView struct {
	DefaultRoute  string
	RoutePrefixes []string
}

// RequestPlane is the frozen generation-owned surface used to compose the
// standard HTTP request plane. Exported accessors return interfaces, immutable
// values, or defensive copies. It does not expose *runtime.App, *Built, or
// process-owned closers. A private frozen config projection is retained only for
// stack/middleware composition and is never returned.
type RequestPlane struct {
	log           *slog.Logger
	frozen        *config.Config // private projection; never returned
	registrations []lipsdk.Registration
	route         FrozenRoutingView

	executor        *runtime.Executor
	store           b2bua.Store
	upstreamHTTP    *http.Client
	decodeAdmission lipsdk.DecodeAdmission
	pluginRegistry  *pluginreg.Registry
	metrics         *metrics.Bundle
	runtimeSnap     *extensions.RequestRuntimeSnapshot
	httpAuth        []httpauth.Provider
	secureSessions  ssessionapp.Store
	authEvents      *auth.EventDispatcher
	catalog         *modelcatalog.CatalogRuntime
	modelRegistry   *modelregistry.Registry
	modelRuntime    *modelregistry.Runtime
	tokenAdmin      *accountingapp.Service
	cpQueries       *controlplane.QueryService
	cpStatus        *controlplane.Status
	cpRetention     *controlplane.RetentionController
	usageAuthority  *authorityapp.Service
	concurrency     *concurrencyapp.Service
	snapshots       *snapshotgen.Publisher
	snapshotCtrl    *SnapshotController
	meteringQuerier metering.Querier
	readiness       *controlplane.ReadinessReportService
	secretGuardInv  *diag.InventoryExtras
	terminalProc    *terminalworkapp.Processor
	terminalReg     *terminalworkapp.Registry
	terminalQueries *terminalworkapp.QueryService
	terminalMetrics *terminalworkapp.MetricsObserver
}

// Logger returns the process logger (non-owning).
func (p RequestPlane) Logger() *slog.Logger { return p.log }

// Frontends returns a defensive copy of frontend plugin rows.
func (p RequestPlane) Frontends() []config.PluginConfig {
	if p.frozen == nil {
		return nil
	}
	return freezePluginConfigs(p.frozen.Plugins.Frontends)
}

// Registrations returns a defensive deep copy of plugin registrations.
func (p RequestPlane) Registrations() []lipsdk.Registration {
	if p.registrations == nil {
		return nil
	}
	return freezeRegistrations(p.registrations)
}

// Routing returns the frozen routing view with copied prefixes.
func (p RequestPlane) Routing() FrozenRoutingView {
	return FrozenRoutingView{
		DefaultRoute:  p.route.DefaultRoute,
		RoutePrefixes: append([]string(nil), p.route.RoutePrefixes...),
	}
}

// Executor returns the generation-owned executor.
func (p RequestPlane) Executor() *runtime.Executor { return p.executor }

// Store returns the process continuity store (non-owning).
func (p RequestPlane) Store() b2bua.Store { return p.store }

// UpstreamHTTP returns the generation upstream client.
func (p RequestPlane) UpstreamHTTP() *http.Client { return p.upstreamHTTP }

// DecodeAdmission returns the process decode limiter (non-owning).
func (p RequestPlane) DecodeAdmission() lipsdk.DecodeAdmission { return p.decodeAdmission }

// PluginRegistry returns the process factory catalog (non-owning).
func (p RequestPlane) PluginRegistry() *pluginreg.Registry { return p.pluginRegistry }

// Metrics returns process metrics (non-owning).
func (p RequestPlane) Metrics() *metrics.Bundle { return p.metrics }

// RuntimeSnapshot returns the generation extension snapshot.
func (p RequestPlane) RuntimeSnapshot() *extensions.RequestRuntimeSnapshot { return p.runtimeSnap }

// HTTPAuthProviders returns a defensive copy of auth providers.
func (p RequestPlane) HTTPAuthProviders() []httpauth.Provider {
	if p.httpAuth == nil {
		return nil
	}
	return append([]httpauth.Provider(nil), p.httpAuth...)
}

// SecureSessionStore returns the process secure-session store (non-owning).
func (p RequestPlane) SecureSessionStore() ssessionapp.Store { return p.secureSessions }

// AuthEventDispatcher returns the generation auth event dispatcher.
func (p RequestPlane) AuthEventDispatcher() *auth.EventDispatcher { return p.authEvents }

// CatalogRuntime returns the generation catalog runtime.
func (p RequestPlane) CatalogRuntime() *modelcatalog.CatalogRuntime { return p.catalog }

// ModelRegistry returns the generation model registry.
func (p RequestPlane) ModelRegistry() *modelregistry.Registry { return p.modelRegistry }

// ModelRegistryRuntime returns the generation model registry runtime.
func (p RequestPlane) ModelRegistryRuntime() *modelregistry.Runtime { return p.modelRuntime }

// TokenAccountingAdmin returns the generation accounting admin service.
func (p RequestPlane) TokenAccountingAdmin() *accountingapp.Service { return p.tokenAdmin }

// ControlPlaneQueries returns process control-plane queries (non-owning).
func (p RequestPlane) ControlPlaneQueries() *controlplane.QueryService { return p.cpQueries }

// ControlPlaneStatus returns process control-plane status (non-owning).
func (p RequestPlane) ControlPlaneStatus() *controlplane.Status { return p.cpStatus }

// ControlPlaneRetention returns process control-plane retention (non-owning).
func (p RequestPlane) ControlPlaneRetention() *controlplane.RetentionController { return p.cpRetention }

// UsageAuthority returns process usage authority (non-owning).
func (p RequestPlane) UsageAuthority() *authorityapp.Service { return p.usageAuthority }

// ConcurrencyAuthority returns process concurrency authority (non-owning).
func (p RequestPlane) ConcurrencyAuthority() *concurrencyapp.Service { return p.concurrency }

// SnapshotGeneration returns process snapshot publisher (non-owning).
func (p RequestPlane) SnapshotGeneration() *snapshotgen.Publisher { return p.snapshots }

// SnapshotController returns process snapshot controller (non-owning).
func (p RequestPlane) SnapshotController() *SnapshotController { return p.snapshotCtrl }

// MeteringQuerier returns process metering querier (non-owning).
func (p RequestPlane) MeteringQuerier() metering.Querier { return p.meteringQuerier }

// ReadinessReport returns the generation readiness report service.
func (p RequestPlane) ReadinessReport() *controlplane.ReadinessReportService { return p.readiness }

// SecretGuardInventory returns generation secret-guard inventory extras.
func (p RequestPlane) SecretGuardInventory() *diag.InventoryExtras { return p.secretGuardInv }

// TerminalWorkProcessor returns process terminal-work processor (non-owning).
func (p RequestPlane) TerminalWorkProcessor() *terminalworkapp.Processor { return p.terminalProc }

// TerminalWorkRegistry returns process terminal-work registry (non-owning).
func (p RequestPlane) TerminalWorkRegistry() *terminalworkapp.Registry { return p.terminalReg }

// TerminalWorkQueries returns process terminal-work queries (non-owning).
func (p RequestPlane) TerminalWorkQueries() *terminalworkapp.QueryService { return p.terminalQueries }

// TerminalWorkMetrics returns process terminal-work metrics (non-owning).
func (p RequestPlane) TerminalWorkMetrics() *terminalworkapp.MetricsObserver {
	return p.terminalMetrics
}

// StackConfig returns a defensive clone of the private frozen config projection
// for handler middleware composition. Callers may retain the clone in closures;
// mutations must not affect the bundle's internal frozen source.
func (p RequestPlane) StackConfig() *config.Config {
	if p.frozen == nil {
		return nil
	}
	cloned, err := freezeConfig(p.frozen)
	if err != nil {
		return nil
	}
	return cloned
}

// NewCompatRequestPlane builds a transitional RequestPlane projection directly
// from one already-compiled candidate for stdhttp.ComposeRequestPlane
// compatibility tests only. The canonical CompileGeneration path never calls
// this; it disappears together with RequestPlane at task 3.5.
func NewCompatRequestPlane(cand *CandidateRuntime, frozen *config.Config, log *slog.Logger, registrations []lipsdk.Registration, route FrozenRoutingView) RequestPlane {
	if cand == nil {
		return RequestPlane{}
	}
	return RequestPlane{
		log:             log,
		frozen:          frozen,
		registrations:   freezeRegistrations(registrations),
		route:           FrozenRoutingView{DefaultRoute: route.DefaultRoute, RoutePrefixes: append([]string(nil), route.RoutePrefixes...)},
		executor:        cand.Executor,
		store:           cand.Store,
		upstreamHTTP:    cand.UpstreamHTTP,
		decodeAdmission: cand.DecodeAdmission,
		pluginRegistry:  cand.PluginRegistry,
		metrics:         cand.Metrics,
		runtimeSnap:     cand.RuntimeSnapshot,
		httpAuth:        append([]httpauth.Provider(nil), cand.HTTPAuthProviders...),
		secureSessions:  cand.SecureSessionStore,
		authEvents:      cand.AuthEventDispatcher,
		catalog:         cand.CatalogRuntime,
		modelRegistry:   cand.ModelRegistry,
		modelRuntime:    cand.ModelRegistryRuntime,
		tokenAdmin:      cand.TokenAccountingAdmin,
		cpQueries:       cand.ControlPlaneQueries,
		cpStatus:        cand.ControlPlaneStatus,
		cpRetention:     cand.ControlPlaneRetention,
		usageAuthority:  cand.UsageAuthority,
		concurrency:     cand.ConcurrencyAuthority,
		snapshots:       cand.SnapshotGeneration,
		snapshotCtrl:    cand.SnapshotController,
		meteringQuerier: cand.MeteringQuerier,
		readiness:       cand.ReadinessReport,
		secretGuardInv:  cand.SecretGuardInventory,
		terminalProc:    cand.TerminalWorkProcessor,
		terminalReg:     cand.TerminalWorkRegistry,
		terminalQueries: cand.TerminalWorkQueries,
		terminalMetrics: cand.TerminalWorkMetrics,
	}
}
