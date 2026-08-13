package runtimebundle

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routeoverride"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
)

// persistenceRuntime holds the continuity (B2BUA-wrapped) store and the secure-
// session runtime produced by [buildPersistenceRuntime].
type persistenceRuntime struct {
	Store         b2bua.Store
	OverrideStore routeoverride.Store
	SecureSession *secureSessionRuntime
}

// buildPersistenceRuntime opens the continuity store (postgres handles shared via
// the process pool registry), wraps it with the control-plane B2BUA projection,
// then builds the secure-session runtime. It appends the continuity closer (when
// present) and the secure-session closer to closers, in that order, and returns
// the updated slice. Continuity errors get "runtimebundle: %w"; secure-session
// errors are returned unwrapped (the helper attaches context).
func buildPersistenceRuntime(bctx buildContext, cp *controlPlaneRuntime, bundle *metrics.Bundle, closers []func() error) (*persistenceRuntime, []func() error, error) {
	cfg, parent, log := bctx.Cfg, bctx.Parent, bctx.Log
	store, storeCloser, err := openContinuityStore(parent, cfg, bctx.PostgresPools, bctx.DualPlaneMigrator)
	if err != nil {
		return nil, closers, fmt.Errorf("runtimebundle: %w", err)
	}
	if storeCloser != nil {
		closers = append(closers, storeCloser)
	}
	overrideStore, _ := routeoverride.AsStore(store)
	store = cp.wrapB2BUA(store)

	ssRun, err := buildSecureSessionRuntime(secureSessionBuildInput{
		StartupContext:        parent,
		Cfg:                   cfg,
		B2B:                   store,
		Log:                   log,
		Bundle:                bundle,
		ControlPlaneStoreWrap: cp.wrapSecureSession,
		PostgresPools:         bctx.PostgresPools,
		DualPlaneMigrator:     bctx.DualPlaneMigrator,
	})
	if err != nil {
		return nil, closers, err
	}
	if ssRun.closer != nil {
		closers = append(closers, ssRun.closer)
	}
	return &persistenceRuntime{Store: store, OverrideStore: overrideStore, SecureSession: ssRun}, closers, nil
}
