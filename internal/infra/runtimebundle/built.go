package runtimebundle

import (
	"context"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// Built holds assembled runtime dependencies for the standard distribution composition root.
type Built struct {
	Executor *runtime.Executor
	Store    b2bua.Store
	Closers  []func() error
	// EffectiveDefaultRoute is the selector used when clients omit explicit routing (see config.EffectiveDefaultRouteSelector), after model_aliases expansion when configured.
	// [Build] sets this from config and BuildOptions.WireModel (or standardplugins.DefaultWireModel when WireModel is nil).
	EffectiveDefaultRoute string
	// UpstreamHTTP is the shared outbound HTTP client passed to backends that need upstream HTTP.
	// Successful [Build] always sets this (explicit [BuildOptions.Infra.HTTPClient] or the default from httpclient).
	UpstreamHTTP *http.Client
	// RoutePrefixes are backend route-selector prefixes accepted from frontend protocol model fields.
	RoutePrefixes []string
	// DecodeAdmission is the shared frontend decode concurrency/byte limiter for stdhttp mounts.
	// Nil disables admission (custom/minimal Built only); [Build] always installs a finite limiter.
	DecodeAdmission lipsdk.DecodeAdmission
	// PluginRegistry is the registry used to construct backends and must be used when mounting frontends
	// or composing features. [Build] sets this from [BuildOptions.PluginRegistry].
	PluginRegistry *pluginreg.Registry
	// Metrics is non-nil when observability.metrics.enabled; [stdhttp.RunWithRuntime] uses it for /metrics and HTTP middleware.
	Metrics *metrics.Bundle
	// RuntimeSnapshot is the execution binding for feature stages and facades (design §15B).
	// Treat as read-only for the lifetime of this Built; see [extensions.RequestRuntimeSnapshot].
	RuntimeSnapshot *extensions.RequestRuntimeSnapshot
	// HTTPAuthProviders is copied from [BuildOptions] for stdhttp wiring (transport auth, R4).
	HTTPAuthProviders []httpauth.Provider
	// SecureSessionStore is optional; when non-nil with secure-session diagnostics config, stdhttp
	// mounts operator session routes (see [BuildOptions.Diagnostics.SecureSessionStore]).
	SecureSessionStore app.Store
	// AuthEventDispatcher emits auth decision and session-start events per config policy.
	// Always non-nil after [Build]; the underlying sink may be nil when event delivery is disabled.
	AuthEventDispatcher *auth.EventDispatcher
	// CatalogRuntime is non-nil when model_catalog.enabled or external_updates_enabled started catalog I/O.
	CatalogRuntime *modelcatalog.CatalogRuntime
	// ModelRegistry is the loaded backend model inventory for fast canonical model routing lookups.
	ModelRegistry *modelregistry.Registry
	// ModelRegistryRuntime owns cached backend model inventory refresh and live lookup publication.
	ModelRegistryRuntime *modelregistry.Runtime
	// TokenAccountingAdmin is non-nil when accounting.admin.enabled wires the operator count service.
	TokenAccountingAdmin *accountingapp.Service
	// ControlPlaneQueries is non-nil when control_plane.query.enabled and the
	// backing store opened successfully. stdhttp mounts the protected query
	// surface only when this is non-nil and diagnostics posture allows it.
	ControlPlaneQueries *controlplane.QueryService
	// ControlPlaneStatus is non-nil when control_plane.enabled. It reports
	// disabled, ready, degraded, or unavailable capability state for the
	// protected status route and operator diagnostics (requirement 7.1).
	ControlPlaneStatus *controlplane.Status
	// ControlPlaneRetention is non-nil when control_plane.retention.enabled and
	// the backing store opened successfully. stdhttp exposes operator retention
	// actions through the protected admin handler when configured.
	ControlPlaneRetention *controlplane.RetentionController
	// UsageAuthority is non-nil when accounting.authority.enabled wires the
	// config-backed rule source, store, and query/status service.
	UsageAuthority *authorityapp.Service
	// ConcurrencyAuthority is non-nil when accounting.concurrency.enabled wires
	// the lease service used by protected lease queries (Phase 8.4).
	ConcurrencyAuthority *concurrencyapp.Service
	// SnapshotGeneration publishes immutable usage/concurrency/rating generations
	// for admit-time binding (Phase 9.3). Always non-nil after successful Build.
	SnapshotGeneration *snapshotgen.Publisher
	// SnapshotController republishes generations from injectable sources via
	// Refresh (requirements 11.3, 11.6, 11.7). Nil when TestingOptions overrides
	// the publisher.
	SnapshotController *SnapshotController
	// MeteringQuerier is the optional production metering query mount injected via
	// ProductionOptions (requirement 12.1). Nil when not supplied.
	MeteringQuerier metering.Querier
	// ReadinessReport aggregates independent authority/journal readiness and
	// protected-traffic posture (requirements 15.7, 15.8).
	ReadinessReport *controlplane.ReadinessReportService
	// SecretGuardInventory carries safe secrets-guard inventory metadata for diagnostics.
	SecretGuardInventory *diag.InventoryExtras
	// TerminalWorkProcessor is non-nil when ProductionOptions injects a terminal-work
	// store (task 4.4). Callers own Start; Closers invoke Shutdown.
	TerminalWorkProcessor *terminalworkapp.Processor
	// TerminalWorkRegistry is the provider router paired with TerminalWorkProcessor.
	TerminalWorkRegistry *terminalworkapp.Registry
	// terminalWorkReady optionally checks store readiness for TerminalWorkReadiness.
	terminalWorkReady func(context.Context) error
}
