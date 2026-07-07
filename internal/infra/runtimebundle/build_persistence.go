package runtimebundle

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
)

// persistenceRuntime holds the continuity (B2BUA-wrapped) store and the secure-
// session runtime produced by [buildPersistenceRuntime].
type persistenceRuntime struct {
	Store         b2bua.Store
	SecureSession *secureSessionRuntime
}

// buildPersistenceRuntime opens the continuity store, wraps it with the control-
// plane B2BUA projection, then builds the secure-session runtime. It appends the
// continuity closer (when the store implements io.Closer) and the secure-session
// closer to closers, in that order, and returns the updated slice. Error wrapping
// matches the former inline block: continuity errors get "runtimebundle: %w";
// secure-session errors are returned unwrapped (the helper attaches context).
func buildPersistenceRuntime(bctx buildContext, cp *controlPlaneRuntime, bundle *metrics.Bundle, closers []func() error) (*persistenceRuntime, []func() error, error) {
	cfg, parent, log := bctx.Cfg, bctx.Parent, bctx.Log
	store, err := OpenContinuityStore(parent, cfg)
	if err != nil {
		return nil, closers, fmt.Errorf("runtimebundle: %w", err)
	}
	if c, ok := store.(interface{ Close() error }); ok {
		closers = append(closers, c.Close)
	}
	store = cp.wrapB2BUA(store)

	ssRun, err := buildSecureSessionRuntime(secureSessionBuildInput{
		StartupContext:        parent,
		Cfg:                   cfg,
		B2B:                   store,
		Log:                   log,
		Bundle:                bundle,
		ControlPlaneStoreWrap: cp.wrapSecureSession,
	})
	if err != nil {
		return nil, closers, err
	}
	if ssRun.closer != nil {
		closers = append(closers, ssRun.closer)
	}
	return &persistenceRuntime{Store: store, SecureSession: ssRun}, closers, nil
}
