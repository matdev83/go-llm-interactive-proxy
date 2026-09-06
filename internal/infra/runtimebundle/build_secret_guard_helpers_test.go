package runtimebundle

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// testBuildSecretGuardRuntime is a test helper for unit tests testing secret guard runtime composition.
func testBuildSecretGuardRuntime(cfg *config.Config, log *slog.Logger, opts *BuildOptions, regs []lipsdk.Registration) (*secretGuardRuntime, error) {
	if opts == nil {
		return nil, nil
	}
	mode := accessmode.ModeSingleUser
	if cfg != nil {
		var err error
		mode, err = cfg.EffectiveAccessMode()
		if err != nil {
			return nil, err
		}
	}
	// A nil logger intentionally reaches secretguardcompose through a zero
	// facade value (the historical degenerate-facade contract): the compose
	// layer raises the pinned audit error only when audit is required, while
	// explicit-observer configs succeed. A non-nil logger takes the production
	// NewProcess constructor, which owns nothing at this checkpoint.
	var fh *featurehost.Runtime
	if log != nil {
		var err error
		if fh, err = featurehost.NewProcess(context.Background(), featurehost.ProcessInput{Logger: log}); err != nil {
			return nil, err
		}
	} else {
		fh = &featurehost.Runtime{}
	}
	out, err := fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations:    regs,
		Planes:           opts.FeaturePlanes,
		AccessMode:       mode,
		SecretEnv:        opts.Extensions.SecretGuardEnvironment,
		SecretInputs:     opts.Extensions.SecretGuardInputs,
		DecisionObserver: opts.Extensions.SecretDecisionObserver,
	})
	if err != nil {
		if unwrapped := errors.Unwrap(err); unwrapped != nil {
			return nil, unwrapped
		}
		return nil, err
	}
	return &secretGuardRuntime{
		Plane:     out.SecretGuard,
		Inventory: out.SecretGuardInventory,
	}, nil
}

// bindSecretGuardAudit is a test helper alias for secret guard composition with a
// discard logger fallback when log is nil.
func bindSecretGuardAudit(cfg *config.Config, opts *BuildOptions, regs []lipsdk.Registration, log *slog.Logger) (*secretGuardRuntime, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	}
	return testBuildSecretGuardRuntime(cfg, log, opts, regs)
}
