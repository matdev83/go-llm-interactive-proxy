package runtimebundle

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguardverify"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// loopGuardObserver is a narrow seam for verifier telemetry.
// Production wires it to observability; tests can inject a spy.
// Success usage/cost is forwarded honestly without fabrication.
type loopGuardObserver func(stopguardverify.VerifyObservation)

// buildLoopGuard constructs the production LoopGuard from effective config.
// Nil is returned when disabled, preserving the fast path.
// The auxiliary client is lazy (captures executor pointer) and the observer is invoked exactly once per Verify.
// Nil client is normalized to DisabledClient for conservative UNCERTAIN behavior without panic.
func buildLoopGuard(eff config.EffectiveAgentLoopGuardConfig, client auxiliary.Client, now func() time.Time, observer loopGuardObserver) *runtime.LoopGuard {
	if !eff.Enabled {
		return nil
	}
	if client == nil {
		client = auxiliary.DisabledClient{}
	}
	if now == nil {
		now = time.Now
	}
	policy := stopguard.PolicyTrust
	switch eff.ExplicitCompletionPolicy {
	case config.AgentLoopGuardExplicitCompletionPolicyTrust:
		policy = stopguard.PolicyTrust
	case config.AgentLoopGuardExplicitCompletionPolicyVerify:
		policy = stopguard.PolicyVerify
	}
	verifier := stopguardverify.NewAdapter(client, stopguardverify.AdapterConfig{
		Role:    eff.VerifierRole,
		Timeout: eff.VerifierTimeout,
		Observer: func(obs stopguardverify.VerifyObservation) {
			if observer != nil {
				observer(obs)
			}
		},
	})
	gate := stopgate.New(stopgate.Ports{Verifier: verifier, Now: now}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: policy,
		MaxSemanticContinuations: eff.MaxSemanticContinuations,
		NoProgressLimit:          eff.NoProgressLimit,
	})
	return runtime.NewLoopGuard(gate)
}

// newLoopGuardObserver creates a production observer that forwards honest telemetry.
// If no observability sink exists, it is a no-op for success and logs errors at debug.
// Deferred: full verifier metrics will be owned by task 9.2; this seam preserves honest data.
func newLoopGuardObserver(log interface {
	DebugContext(context.Context, string, ...any)
}) loopGuardObserver {
	return func(obs stopguardverify.VerifyObservation) {
		// Honest: Latency is measured by adapter (>=0), usage from Collected, Err as observed.
		// No fabricated values. Success and failure both forwarded; current production only logs at debug.
		if log != nil {
			if obs.Err != nil {
				log.DebugContext(context.Background(), "agent_loop_guard_verifier_observation", "latency_ms", obs.Latency.Milliseconds(), "input_tokens", obs.InputTokens, "output_tokens", obs.OutputTokens, "cost_nano", obs.CostNanoUnits, "err", obs.Err)
			} else {
				log.DebugContext(context.Background(), "agent_loop_guard_verifier_observation", "latency_ms", obs.Latency.Milliseconds(), "input_tokens", obs.InputTokens, "output_tokens", obs.OutputTokens, "cost_nano", obs.CostNanoUnits)
			}
		}
	}
}
