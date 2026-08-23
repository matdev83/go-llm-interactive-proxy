package config

import (
	"fmt"
	"strings"
	"time"
)

// AgentLoopGuardExplicitCompletionPolicy selects how a normalized frontend
// explicit completion fact is treated for eligible clean stops.
type AgentLoopGuardExplicitCompletionPolicy string

const (
	AgentLoopGuardExplicitCompletionPolicyTrust  AgentLoopGuardExplicitCompletionPolicy = "trust"
	AgentLoopGuardExplicitCompletionPolicyVerify AgentLoopGuardExplicitCompletionPolicy = "verify"
)

const (
	DefaultAgentLoopGuardVerifierRole             = "loop_guard"
	DefaultAgentLoopGuardVerifierTimeoutSeconds   = 4
	DefaultAgentLoopGuardMaxSemanticContinuations = 3
	DefaultAgentLoopGuardNoProgressLimit          = 2
)

// AgentLoopGuardConfig is the opt-in Agent Loop Guard surface. It owns no
// transport retry/idle knobs; existing stream_recovery settings stay
// authoritative. UNCERTAIN -> ALLOW_STOP is a fixed v1 safety invariant with
// no configuration key.
type AgentLoopGuardConfig struct {
	Enabled                  bool   `yaml:"enabled"`
	VerifierRole             string `yaml:"verifier_role"`
	VerifierTimeoutSeconds   int    `yaml:"verifier_timeout_seconds"`
	MaxSemanticContinuations int    `yaml:"max_semantic_continuations"`
	NoProgressLimit          int    `yaml:"no_progress_limit"`
	ExplicitCompletionPolicy string `yaml:"explicit_completion_policy"`
}

// EffectiveAgentLoopGuardConfig reports the guard posture after defaults.
type EffectiveAgentLoopGuardConfig struct {
	Enabled                  bool
	VerifierRole             string
	VerifierTimeoutSeconds   int
	VerifierTimeout          time.Duration
	MaxSemanticContinuations int
	NoProgressLimit          int
	ExplicitCompletionPolicy AgentLoopGuardExplicitCompletionPolicy
}

// EffectiveAgentLoopGuard applies defaults for zero values regardless of the
// enabled flag, so disabled deployments still observe a coherent surface and
// enabling later never inherits zero bounds.
func (c *Config) EffectiveAgentLoopGuard() EffectiveAgentLoopGuardConfig {
	g := c.AgentLoopGuard
	eff := EffectiveAgentLoopGuardConfig{
		Enabled:                  g.Enabled,
		VerifierRole:             DefaultAgentLoopGuardVerifierRole,
		VerifierTimeoutSeconds:   DefaultAgentLoopGuardVerifierTimeoutSeconds,
		MaxSemanticContinuations: DefaultAgentLoopGuardMaxSemanticContinuations,
		NoProgressLimit:          DefaultAgentLoopGuardNoProgressLimit,
		ExplicitCompletionPolicy: AgentLoopGuardExplicitCompletionPolicyTrust,
	}
	if role := strings.TrimSpace(g.VerifierRole); role != "" {
		eff.VerifierRole = role
	}
	if g.VerifierTimeoutSeconds > 0 {
		eff.VerifierTimeoutSeconds = g.VerifierTimeoutSeconds
	}
	if g.MaxSemanticContinuations > 0 {
		eff.MaxSemanticContinuations = g.MaxSemanticContinuations
	}
	if g.NoProgressLimit > 0 {
		eff.NoProgressLimit = g.NoProgressLimit
	}
	eff.VerifierTimeout = time.Duration(eff.VerifierTimeoutSeconds) * time.Second
	if pol, ok := parseAgentLoopGuardExplicitCompletionPolicy(g.ExplicitCompletionPolicy); ok {
		eff.ExplicitCompletionPolicy = pol
	}
	return eff
}

func validateAgentLoopGuard(cfg *Config) error {
	g := cfg.AgentLoopGuard
	if _, ok := parseAgentLoopGuardExplicitCompletionPolicy(g.ExplicitCompletionPolicy); !ok && strings.TrimSpace(g.ExplicitCompletionPolicy) != "" {
		return fmt.Errorf(
			"agent_loop_guard.explicit_completion_policy: unknown %q (want trust or verify)",
			g.ExplicitCompletionPolicy,
		)
	}
	if !g.Enabled {
		return nil
	}
	if strings.TrimSpace(g.VerifierRole) == "" {
		return fmt.Errorf("agent_loop_guard.verifier_role: required when agent_loop_guard.enabled is true")
	}
	for _, chk := range []struct {
		name string
		val  int
	}{
		{"verifier_timeout_seconds", g.VerifierTimeoutSeconds},
		{"max_semantic_continuations", g.MaxSemanticContinuations},
		{"no_progress_limit", g.NoProgressLimit},
	} {
		if chk.val <= 0 {
			return fmt.Errorf("agent_loop_guard.%s: must be positive when agent_loop_guard.enabled is true", chk.name)
		}
	}
	return nil
}

func parseAgentLoopGuardExplicitCompletionPolicy(raw string) (AgentLoopGuardExplicitCompletionPolicy, bool) {
	switch pol := AgentLoopGuardExplicitCompletionPolicy(strings.ToLower(strings.TrimSpace(raw))); pol {
	case AgentLoopGuardExplicitCompletionPolicyTrust, AgentLoopGuardExplicitCompletionPolicyVerify:
		return pol, true
	default:
		return "", false
	}
}
