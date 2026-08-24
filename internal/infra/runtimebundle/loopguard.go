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

// buildLoopGuardFactory constructs a per-request LoopGuard factory sharing verifier/config.
// Nil is returned when disabled, preserving the fast path.
func buildLoopGuardFactory(eff config.EffectiveAgentLoopGuardConfig, client auxiliary.Client, now func() time.Time, observer loopGuardObserver) *runtime.LoopGuardFactory {
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
	return runtime.NewLoopGuardFactory(stopgate.Ports{Verifier: verifier, Now: now}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: policy,
		MaxSemanticContinuations: eff.MaxSemanticContinuations,
		NoProgressLimit:          eff.NoProgressLimit,
	})
}

type debugLogger interface {
	DebugContext(context.Context, string, ...any)
}

// newLoopGuardObserver creates a production observer that forwards honest telemetry.
// If no observability sink exists, it is a no-op for success and logs errors at debug.
func newLoopGuardObserver(log debugLogger) loopGuardObserver {
	return func(obs stopguardverify.VerifyObservation) {
		// Honest: Latency is measured by adapter (>=0), usage from Collected, Err as observed.
		// No fabricated values. Success and failure both forwarded.
		if log != nil {
			attrs := []any{
				"latency_ms", obs.Latency.Milliseconds(),
				"input_tokens", obs.InputTokens,
				"output_tokens", obs.OutputTokens,
				"cost_nano", obs.CostNanoUnits,
			}
			if obs.ParentTraceID != "" {
				attrs = append(attrs, "trace_id", obs.ParentTraceID)
			}
			if obs.ParentALegID != "" {
				attrs = append(attrs, "a_leg_id", obs.ParentALegID)
			}
			if obs.ParentBLegID != "" {
				attrs = append(attrs, "b_leg_id", obs.ParentBLegID)
			}
			if obs.Err != nil {
				attrs = append(attrs, "err", obs.Err)
			}
			log.DebugContext(context.Background(), "agent_loop_guard_verifier_observation", attrs...)
		}
	}
}
