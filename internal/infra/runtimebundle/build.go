package runtimebundle

import (
	"fmt"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

// Build assembles continuity store, executor, and closers for the standard distribution.
// cfg must be non-nil, bus/log must be non-nil, and opts.PluginRegistry must be set.
// The returned [Built.RuntimeSnapshot] is shared by concurrent requests.
//
// Build is a compatibility wrapper: it constructs [ProcessServices] once, compiles
// one candidate via [CompileCandidate], and retains historical aggregate Closers
// semantics (generation closers + process Close). Prefer the split APIs when
// compiling multiple candidates against shared process services.
func Build(cfg *config.Config, bus *hooks.Bus, log *slog.Logger, opts *BuildOptions) (built *Built, err error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	if opts == nil || opts.PluginRegistry == nil {
		return nil, fmt.Errorf("runtimebundle: nil PluginRegistry")
	}
	if log == nil {
		return nil, fmt.Errorf("runtimebundle: nil logger")
	}
	if err := validateRequiredAuthorityEvidenceWiring(cfg); err != nil {
		return nil, err
	}
	if err := standardplugins.ValidateCustomCompatibleBackendPrefixes(cfg.Plugins.Backends); err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}

	ps, err := NewProcessServices(nil, ProcessServicesInput{
		Cfg:     cfg,
		Log:     log,
		Opts:    opts,
		Tracing: opts.Infra.ProcessTracing,
	})
	if err != nil {
		return nil, err
	}
	if !ps.Tracing.Active && opts.Infra.OutboundTracing {
		ps.Tracing.Active = true
	}

	cand, err := CompileCandidate(ps.parent, GenerationCompileInput{
		Process: ps,
		Bus:     bus,
	})
	if err != nil {
		ps.ensurePoolCloser()
		return nil, withDisposedClosers(err, []func() error{ps.Close})
	}

	// Historical aggregate cleanup: when process services acquired closers,
	// register process Close first so reverse-order disposeClosers runs
	// generation closers before process services. Empty process closer bags
	// preserve historical zero-closer aggregates for in-memory builds.
	closers := cand.Closers
	if len(ps.closers) > 0 {
		closers = append([]func() error{ps.Close}, cand.Closers...)
	}
	return &Built{
		Executor:              cand.Executor,
		Store:                 cand.Store,
		Closers:               closers,
		UpstreamHTTP:          cand.UpstreamHTTP,
		RoutePrefixes:         cand.RoutePrefixes,
		DecodeAdmission:       cand.DecodeAdmission,
		PluginRegistry:        cand.PluginRegistry,
		EffectiveDefaultRoute: cand.EffectiveDefaultRoute,
		Metrics:               cand.Metrics,
		RuntimeSnapshot:       cand.RuntimeSnapshot,
		HTTPAuthProviders:     cand.HTTPAuthProviders,
		SecureSessionStore:    cand.SecureSessionStore,
		AuthEventDispatcher:   cand.AuthEventDispatcher,
		CatalogRuntime:        cand.CatalogRuntime,
		ModelRegistry:         cand.ModelRegistry,
		ModelRegistryRuntime:  cand.ModelRegistryRuntime,
		TokenAccountingAdmin:  cand.TokenAccountingAdmin,
		ControlPlaneQueries:   cand.ControlPlaneQueries,
		ControlPlaneStatus:    cand.ControlPlaneStatus,
		ControlPlaneRetention: cand.ControlPlaneRetention,
		UsageAuthority:        cand.UsageAuthority,
		ConcurrencyAuthority:  cand.ConcurrencyAuthority,
		SnapshotGeneration:    cand.SnapshotGeneration,
		SnapshotController:    cand.SnapshotController,
		MeteringQuerier:       cand.MeteringQuerier,
		ReadinessReport:       cand.ReadinessReport,
		SecretGuardInventory:  cand.SecretGuardInventory,
		TerminalWorkProcessor: cand.TerminalWorkProcessor,
		TerminalWorkRegistry:  cand.TerminalWorkRegistry,
		TerminalWorkQueries:   cand.TerminalWorkQueries,
		TerminalWorkMetrics:   cand.TerminalWorkMetrics,
		terminalWorkReady:     cand.terminalWorkReady,
		terminalWorkRT:        cand.terminalWorkRT,
	}, nil
}
