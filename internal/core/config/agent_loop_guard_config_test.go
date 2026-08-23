package config_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentLoopGuardConfig_DefaultsDisabled proves that when unconfigured or
// empty, Agent Loop Guard defaults to disabled with authoritative defaults:
// enabled=false, verifier_role="loop_guard", verifier_timeout_seconds=4,
// max_semantic_continuations=3, no_progress_limit=2,
// explicit_completion_policy=trust. (Requirements 1.1, 1.5, 8.1, 8.2)
func TestAgentLoopGuardConfig_DefaultsDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	eff := cfg.EffectiveAgentLoopGuard()

	assert.False(t, eff.Enabled, "agent_loop_guard must default to disabled")
	assert.Equal(t, "loop_guard", eff.VerifierRole)
	assert.Equal(t, 4, eff.VerifierTimeoutSeconds)
	assert.Equal(t, 4*time.Second, eff.VerifierTimeout)
	assert.Equal(t, 3, eff.MaxSemanticContinuations)
	assert.Equal(t, 2, eff.NoProgressLimit)
	assert.Equal(t, config.AgentLoopGuardExplicitCompletionPolicyTrust, eff.ExplicitCompletionPolicy)

	require.NoError(t, config.Validate(&config.Config{}), "default config must pass validation")
}

// TestAgentLoopGuardConfig_YAMLDecodingAndEffectiveValues proves strict decoding
// of the agent_loop_guard block and effective-value reflection.
// (Requirements 1.1, 8.1, 8.2)
func TestAgentLoopGuardConfig_YAMLDecodingAndEffectiveValues(t *testing.T) {
	t.Parallel()

	rawYAML := []byte(`
server:
  address: "127.0.0.1:0"
agent_loop_guard:
  enabled: true
  verifier_role: "custom_verifier"
  verifier_timeout_seconds: 8
  max_semantic_continuations: 5
  no_progress_limit: 4
  explicit_completion_policy: "verify"
`)

	cfg, cat, err := config.StrictDecode(rawYAML)
	require.NoError(t, err, "StrictDecode should succeed for valid Agent Loop Guard YAML")
	require.Equal(t, config.CategoryOK, cat)

	eff := cfg.EffectiveAgentLoopGuard()
	assert.True(t, eff.Enabled)
	assert.Equal(t, "custom_verifier", eff.VerifierRole)
	assert.Equal(t, 8, eff.VerifierTimeoutSeconds)
	assert.Equal(t, 8*time.Second, eff.VerifierTimeout)
	assert.Equal(t, 5, eff.MaxSemanticContinuations)
	assert.Equal(t, 4, eff.NoProgressLimit)
	assert.Equal(t, config.AgentLoopGuardExplicitCompletionPolicyVerify, eff.ExplicitCompletionPolicy)

	require.NoError(t, config.Validate(cfg))
}

// TestAgentLoopGuardConfig_ExplicitCompletionPolicyNormalization proves the two
// supported enum values ("trust", "verify") with case/space normalization.
// (Requirements 5.7, Design Configuration)
func TestAgentLoopGuardConfig_ExplicitCompletionPolicyNormalization(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		policy     string
		wantPolicy config.AgentLoopGuardExplicitCompletionPolicy
	}{
		{name: "lowercase_trust", policy: "trust", wantPolicy: config.AgentLoopGuardExplicitCompletionPolicyTrust},
		{name: "uppercase_trust", policy: "TRUST", wantPolicy: config.AgentLoopGuardExplicitCompletionPolicyTrust},
		{name: "whitespace_trust", policy: "  trust  ", wantPolicy: config.AgentLoopGuardExplicitCompletionPolicyTrust},
		{name: "lowercase_verify", policy: "verify", wantPolicy: config.AgentLoopGuardExplicitCompletionPolicyVerify},
		{name: "uppercase_verify", policy: "VERIFY", wantPolicy: config.AgentLoopGuardExplicitCompletionPolicyVerify},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				AgentLoopGuard: config.AgentLoopGuardConfig{
					Enabled:                  true,
					VerifierRole:             "loop_guard",
					VerifierTimeoutSeconds:   4,
					MaxSemanticContinuations: 3,
					NoProgressLimit:          2,
					ExplicitCompletionPolicy: tc.policy,
				},
			}
			require.NoError(t, config.Validate(cfg))
			assert.Equal(t, tc.wantPolicy, cfg.EffectiveAgentLoopGuard().ExplicitCompletionPolicy)
		})
	}
}

// TestAgentLoopGuardConfig_Validation_EnabledRejectsInvalidBounds proves that
// when Agent Loop Guard is enabled, non-positive bounds, empty verifier roles,
// and invalid enum values are rejected using dot-path error naming.
// (Requirements 8.1, 8.2, Design Configuration)
func TestAgentLoopGuardConfig_Validation_EnabledRejectsInvalidBounds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		mutate       func(c *config.AgentLoopGuardConfig)
		errSubstring string
	}{
		{
			name:         "zero_verifier_timeout",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.VerifierTimeoutSeconds = 0 },
			errSubstring: "agent_loop_guard.verifier_timeout_seconds",
		},
		{
			name:         "negative_verifier_timeout",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.VerifierTimeoutSeconds = -1 },
			errSubstring: "agent_loop_guard.verifier_timeout_seconds",
		},
		{
			name:         "zero_max_semantic_continuations",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.MaxSemanticContinuations = 0 },
			errSubstring: "agent_loop_guard.max_semantic_continuations",
		},
		{
			name:         "negative_max_semantic_continuations",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.MaxSemanticContinuations = -3 },
			errSubstring: "agent_loop_guard.max_semantic_continuations",
		},
		{
			name:         "zero_no_progress_limit",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.NoProgressLimit = 0 },
			errSubstring: "agent_loop_guard.no_progress_limit",
		},
		{
			name:         "negative_no_progress_limit",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.NoProgressLimit = -2 },
			errSubstring: "agent_loop_guard.no_progress_limit",
		},
		{
			name:         "empty_verifier_role",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.VerifierRole = "" },
			errSubstring: "agent_loop_guard.verifier_role",
		},
		{
			name:         "whitespace_only_verifier_role",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.VerifierRole = "   " },
			errSubstring: "agent_loop_guard.verifier_role",
		},
		{
			name:         "unknown_explicit_completion_policy_enum",
			mutate:       func(c *config.AgentLoopGuardConfig) { c.ExplicitCompletionPolicy = "always_continue" },
			errSubstring: "agent_loop_guard.explicit_completion_policy",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			guard := config.AgentLoopGuardConfig{
				Enabled:                  true,
				VerifierRole:             "loop_guard",
				VerifierTimeoutSeconds:   4,
				MaxSemanticContinuations: 3,
				NoProgressLimit:          2,
				ExplicitCompletionPolicy: "trust",
			}
			tc.mutate(&guard)
			cfg := &config.Config{AgentLoopGuard: guard}
			err := config.Validate(cfg)
			require.Error(t, err, "expected validation failure for %s", tc.name)
			assert.Contains(t, err.Error(), tc.errSubstring)
		})
	}
}

// TestAgentLoopGuardConfig_Validation_DisabledAcceptsZeroValues proves backward
// compatibility: when Agent Loop Guard is disabled, zero/empty fields do not
// fail validation and effective accessors still return safe defaults.
// (Requirements 1.1, 1.5)
func TestAgentLoopGuardConfig_Validation_DisabledAcceptsZeroValues(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		AgentLoopGuard: config.AgentLoopGuardConfig{
			Enabled:                  false,
			VerifierRole:             "",
			VerifierTimeoutSeconds:   0,
			MaxSemanticContinuations: 0,
			NoProgressLimit:          0,
			ExplicitCompletionPolicy: "",
		},
	}

	require.NoError(t, config.Validate(cfg), "disabled guard with zero values must pass validation")

	eff := cfg.EffectiveAgentLoopGuard()
	assert.False(t, eff.Enabled)
	assert.Equal(t, "loop_guard", eff.VerifierRole)
	assert.Equal(t, 4, eff.VerifierTimeoutSeconds)
	assert.Equal(t, 4*time.Second, eff.VerifierTimeout)
	assert.Equal(t, 3, eff.MaxSemanticContinuations)
	assert.Equal(t, 2, eff.NoProgressLimit)
	assert.Equal(t, config.AgentLoopGuardExplicitCompletionPolicyTrust, eff.ExplicitCompletionPolicy)
}

// TestAgentLoopGuardConfig_UncertainSafetyInvariantNotConfigurable asserts that
// UNCERTAIN -> ALLOW_STOP is a fixed v1 safety invariant with no configuration
// knob; attempts to supply one must be rejected as unknown fields.
// (Requirements 5.6, 12.8, Design Configuration)
func TestAgentLoopGuardConfig_UncertainSafetyInvariantNotConfigurable(t *testing.T) {
	t.Parallel()

	for _, knob := range []string{"uncertain_policy", "on_uncertain", "uncertain_action", "uncertain_behavior"} {
		raw := []byte("server:\n  address: \"127.0.0.1:0\"\nagent_loop_guard:\n  " + knob + ": continue\n")
		_, cat, err := config.StrictDecode(raw)
		require.Error(t, err, "StrictDecode must reject non-configurable uncertain knob %q", knob)
		assert.Equal(t, config.CategoryUnknownCoreField, cat)
	}
}

// TestAgentLoopGuardConfig_NoSuspiciousOnlyPrefilterKnob asserts there is no
// wording-based heuristic prefilter in v1: enabling the guard verifies every
// eligible clean normal stop unless a stronger canonical exclusion applies.
// (Requirements 5.1, Design Semantic Candidate Scope)
func TestAgentLoopGuardConfig_NoSuspiciousOnlyPrefilterKnob(t *testing.T) {
	t.Parallel()

	for _, knob := range []string{"suspicious_only", "heuristic_filter", "wording_filter"} {
		raw := []byte("server:\n  address: \"127.0.0.1:0\"\nagent_loop_guard:\n  " + knob + ": true\n")
		_, cat, err := config.StrictDecode(raw)
		require.Error(t, err, "StrictDecode must reject heuristic prefilter knob %q", knob)
		assert.Equal(t, config.CategoryUnknownCoreField, cat)
	}
}

// TestAgentLoopGuardConfig_StreamRecoveryDefaultsUnchangedAndNoDuplicateTransportKnobs
// proves existing stream_recovery_* defaults remain authoritative and unchanged,
// and that no duplicate transport retry/idle knobs are introduced under the
// agent_loop_guard block. (Requirements 1.1, 3.1, Design Configuration)
func TestAgentLoopGuardConfig_StreamRecoveryDefaultsUnchangedAndNoDuplicateTransportKnobs(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	eff, err := config.EffectiveStreamRecoveryAutoResume(cfg, config.StreamRecoveryOverrides{})
	require.NoError(t, err)

	assert.False(t, eff.Enabled, "stream recovery auto-resume must default to disabled")
	assert.Equal(t, 45*time.Second, eff.IdleTimeout)
	assert.Equal(t, 3*time.Second, eff.GracePeriod)
	assert.Equal(t, config.StreamRecoveryPostOutputFinishWithWarning, eff.PostOutputPolicy)
	assert.Equal(t, 12*time.Second, eff.KeepaliveInterval)
	assert.True(t, eff.EmitWarning)

	for _, knob := range []string{"idle_timeout", "retry_attempts", "grace_period", "keepalive_interval", "transport_retry"} {
		raw := []byte("server:\n  address: \"127.0.0.1:0\"\nagent_loop_guard:\n  " + knob + ": 30s\n")
		_, cat, err := config.StrictDecode(raw)
		require.Error(t, err, "StrictDecode must reject duplicate transport knob %q", knob)
		assert.Equal(t, config.CategoryUnknownCoreField, cat)
	}
}
