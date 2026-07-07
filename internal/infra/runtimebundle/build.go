package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// Build assembles continuity store, executor, and closers for the standard distribution.
//
// cfg must be non-nil. bus may be nil (replaced with an empty hooks.Bus). log must be non-nil.
// opts must be non-nil and opts.PluginRegistry must be non-nil; other BuildOptions fields are optional.
//
// The returned [Built.RuntimeSnapshot] (and executor snapshot) is shared by concurrent requests:
// do not mutate bus or snapshot contents after Build. Reload or reconfiguration must call Build
// again and swap to new [*Built] / executor instances so each generation gets its own snapshot.
func Build(cfg *config.Config, bus *hooks.Bus, log *slog.Logger, opts *BuildOptions) (*Built, error) {
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
	if err := pluginreg.ValidateCustomCompatibleBackendPrefixes(cfg.Plugins.Backends); err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	parent := context.Background()
	if opts != nil && opts.StartupContext != nil {
		parent = opts.StartupContext
	}
	bctx := buildContext{Cfg: cfg, Bus: bus, Log: log, Opts: opts, Parent: parent}
	// closers is the ordered disposal list for every resource Build opens. The
	// control-plane store is opened first (in buildControlPlaneRuntime), so its
	// closer is registered immediately and every later error path disposes it;
	// otherwise durable sqlite/postgres handles leak when a later step fails.
	closers := []func() error{}
	controlPlane, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: parent,
		Cfg:            cfg,
		Log:            log,
		Clock:          opts.Clock,
		StoreOverride:  opts.ControlPlaneStoreOverride,
	})
	if err != nil {
		return nil, err
	}
	if controlPlane != nil && controlPlane.closer != nil {
		closers = append(closers, controlPlane.closer)
	}
	reg := opts.PluginRegistry
	sec, err := buildSecurityRuntime(bctx, controlPlane)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}

	obs := buildObservabilityRuntime(bctx)

	model, closers, err := buildModelRuntime(bctx, obs.Upstream, closers)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	persist, closers, err := buildPersistenceRuntime(bctx, controlPlane, obs.Bundle, closers)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}

	nowFn := time.Now
	if opts.Clock != nil {
		nowFn = opts.Clock
	}
	var exec *runtime.Executor
	ext := buildExtensionRuntime(bctx, nowFn, func() auxreq.ExecutorRunner { return exec }, controlPlane)
	execRun, closers, err := buildExecutorRuntime(executorBuildInput{
		Bctx:          bctx,
		NowFn:         nowFn,
		Ext:           ext,
		Model:         model,
		Persistence:   persist,
		Security:      sec,
		Observability: &obs,
		ControlPlane:  controlPlane,
	}, closers)
	if err != nil {
		return nil, withDisposedClosers(err, closers)
	}
	exec = execRun.Exec
	return &Built{
		Executor:              execRun.Exec,
		Store:                 persist.Store,
		Closers:               closers,
		UpstreamHTTP:          obs.Upstream,
		RoutePrefixes:         model.RoutePrefixes,
		PluginRegistry:        reg,
		EffectiveDefaultRoute: execRun.EffectiveRoute,
		Metrics:               obs.Bundle,
		RuntimeSnapshot:       ext.Snap,
		HTTPAuthProviders:     sec.HTTPAuth,
		SecureSessionStore:    execRun.SecureSessionStore,
		AuthEventDispatcher:   sec.AuthEvents,
		CatalogRuntime:        execRun.CatalogRuntime,
		ModelRegistry:         model.Registry,
		ModelRegistryRuntime:  model.RegistryRuntime,
		TokenAccountingAdmin:  execRun.TokenAccountingAdmin,
		ControlPlaneQueries:   controlPlane.queriesHandle(),
		ControlPlaneStatus:    controlPlane.statusHandle(),
		ControlPlaneRetention: controlPlane.retentionHandle(),
	}, nil
}

func disposeClosers(closers []func() error) error {
	var out error
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i](); err != nil {
			out = errors.Join(out, fmt.Errorf("runtimebundle: dispose closer: %w", err))
		}
	}
	return out
}

// withDisposedClosers disposes closers and returns err unchanged on successful
// disposal, or errors.Join(err, derr) when disposal fails. It treats a nil err
// defensively so callers can pass build errors without a separate nil check.
func withDisposedClosers(err error, closers []func() error) error {
	if derr := disposeClosers(closers); derr != nil {
		return errors.Join(err, derr)
	}
	return err
}

// BuildExecutor wires enabled backends from configuration into a core executor with production
// defaults. Prefer Build for a structured composition result.
func BuildExecutor(
	cfg *config.Config,
	bus *hooks.Bus,
	log *slog.Logger,
	reg *pluginreg.Registry,
) (*runtime.Executor, b2bua.Store, []func() error, error) {
	b, err := Build(cfg, bus, log, &BuildOptions{PluginRegistry: reg})
	if err != nil {
		return nil, nil, nil, err
	}
	return b.Executor, b.Store, b.Closers, nil
}
