package runtimebundle

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuationsafety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguardverify"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildLoopGuard(eff config.EffectiveAgentLoopGuardConfig, client auxiliary.Client, now func() time.Time, observer loopGuardObserver) *runtime.LoopGuard {
	f := buildLoopGuardFactory(eff, client, now, observer)
	if f == nil {
		return nil
	}
	return f.NewGuard()
}

// fakeAuxForWiring captures verifier request and returns controlled response.
type fakeAuxForWiring struct {
	capturedReq  auxiliary.Request
	called       int
	text         string
	inputTokens  int
	outputTokens int
	totalTokens  int
	costNano     int64
	err          error
}

func (f *fakeAuxForWiring) Collect(_ context.Context, req auxiliary.Request) (lipapi.Collected, error) {
	f.called++
	f.capturedReq = req
	if f.err != nil {
		return lipapi.Collected{}, f.err
	}
	var col lipapi.Collected
	col.Text.WriteString(f.text)
	col.InputTokens = f.inputTokens
	col.OutputTokens = f.outputTokens
	col.TotalTokens = f.totalTokens
	col.CostNanoUnits = f.costNano
	return col, nil
}

func (f *fakeAuxForWiring) Stream(_ context.Context, req auxiliary.Request) (lipapi.EventStream, error) {
	f.capturedReq = req
	return nil, f.err
}

func cleanTerminalFacts() stopgate.TerminalFacts {
	return stopgate.TerminalFacts{
		Candidate:            stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true},
		Tail:                 continuationsafety.TailState{},
		Prior:                continuationsafety.PriorSummary{Record: lipcont.ContinuationRecord{ID: lipcont.ResponseID("resp-1")}},
		Bounds:               lipcont.DefaultBounds(),
		SupportsContinuation: true,
	}
}

func TestLoopGuardWiring_EnabledBuildsGate(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.AgentLoopGuard.Enabled = true
	cfg.AgentLoopGuard.VerifierRole = "loop_guard"
	cfg.AgentLoopGuard.VerifierTimeoutSeconds = 4
	cfg.AgentLoopGuard.MaxSemanticContinuations = 3
	cfg.AgentLoopGuard.NoProgressLimit = 2
	cfg.AgentLoopGuard.ExplicitCompletionPolicy = "trust"
	eff := cfg.EffectiveAgentLoopGuard()
	require.True(t, eff.Enabled)
	gate := buildLoopGuard(eff, nil, time.Now, nil)
	require.NotNil(t, gate, "expected LoopGuard non-nil when enabled")
	require.NotPanics(t, func() { _ = gate })
}

func TestLoopGuardWiring_DisabledIsNil(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	eff := cfg.EffectiveAgentLoopGuard()
	require.False(t, eff.Enabled)
	gate := buildLoopGuard(eff, nil, time.Now, nil)
	require.Nil(t, gate, "expected nil LoopGuard when disabled")
}

func TestLoopGuardWiring_FieldMapping_TrustSkipsVerifier(t *testing.T) {
	t.Parallel()
	fake := &fakeAuxForWiring{text: `{"kind":"continue","reason":"x","remaining_objective":"y"}`}
	cfg := &config.Config{}
	cfg.AgentLoopGuard.Enabled = true
	cfg.AgentLoopGuard.VerifierRole = "loop_guard"
	cfg.AgentLoopGuard.VerifierTimeoutSeconds = 4
	cfg.AgentLoopGuard.MaxSemanticContinuations = 3
	cfg.AgentLoopGuard.NoProgressLimit = 2
	cfg.AgentLoopGuard.ExplicitCompletionPolicy = "trust"
	eff := cfg.EffectiveAgentLoopGuard()
	gate := buildLoopGuard(eff, fake, time.Now, nil)
	require.NotNil(t, gate)
	// Trust: explicit completion true should skip verifier
	tf := cleanTerminalFacts()
	tf.Candidate.ExplicitCompletion = true
	out := gate.ObserveCandidate(context.Background(), tf)
	assert.Equal(t, stopguard.ActionForwardTerminal, out.Action)
	assert.Equal(t, 0, fake.called, "trust with explicit should not call verifier")
}

func TestLoopGuardWiring_FieldMapping_VerifyCallsVerifier(t *testing.T) {
	t.Parallel()
	fake := &fakeAuxForWiring{text: `{"kind":"allow_stop","reason":"ok"}`}
	cfg := &config.Config{}
	cfg.AgentLoopGuard.Enabled = true
	cfg.AgentLoopGuard.VerifierRole = "custom_role"
	cfg.AgentLoopGuard.VerifierTimeoutSeconds = 2
	cfg.AgentLoopGuard.MaxSemanticContinuations = 2
	cfg.AgentLoopGuard.NoProgressLimit = 2
	cfg.AgentLoopGuard.ExplicitCompletionPolicy = "verify"
	eff := cfg.EffectiveAgentLoopGuard()
	gate := buildLoopGuard(eff, fake, time.Now, nil)
	require.NotNil(t, gate)
	tf := cleanTerminalFacts()
	tf.Candidate.ExplicitCompletion = true
	out := gate.ObserveCandidate(context.Background(), tf)
	assert.Equal(t, stopguard.ActionForwardTerminal, out.Action)
	assert.Equal(t, 1, fake.called, "verify policy must call verifier even with explicit")
	assert.Equal(t, "custom_role", fake.capturedReq.Role)
}

func TestLoopGuardWiring_BoundsEnforced(t *testing.T) {
	t.Parallel()
	fake := &fakeAuxForWiring{text: `{"kind":"continue","reason":"r","remaining_objective":"obj"}`}
	cfg := &config.Config{}
	cfg.AgentLoopGuard.Enabled = true
	cfg.AgentLoopGuard.VerifierRole = "loop_guard"
	cfg.AgentLoopGuard.VerifierTimeoutSeconds = 4
	cfg.AgentLoopGuard.MaxSemanticContinuations = 1
	cfg.AgentLoopGuard.NoProgressLimit = 10
	cfg.AgentLoopGuard.ExplicitCompletionPolicy = "trust"
	eff := cfg.EffectiveAgentLoopGuard()
	gate := buildLoopGuard(eff, fake, time.Now, nil)
	require.NotNil(t, gate)
	tf := cleanTerminalFacts()
	out1 := gate.ObserveCandidate(context.Background(), tf)
	require.Equal(t, stopguard.ActionContinueLeg, out1.Action)
	assert.False(t, out1.HoldReleased)
	// Second CONTINUE with same budget should latch to forward
	tf2 := cleanTerminalFacts()
	tf2.Tail.CommittedAssistantItems = []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "progress-2"}}}}
	out2 := gate.ObserveCandidate(context.Background(), tf2)
	assert.Equal(t, stopguard.ActionForwardTerminal, out2.Action)
	assert.True(t, out2.HoldReleased)
}

func TestLoopGuardWiring_VerifierRoleAndTimeout(t *testing.T) {
	t.Parallel()
	fake := &fakeAuxForWiring{text: `{"kind":"allow_stop","reason":"ok"}`}
	cfg := &config.Config{}
	cfg.AgentLoopGuard.Enabled = true
	cfg.AgentLoopGuard.VerifierRole = "my_role"
	cfg.AgentLoopGuard.VerifierTimeoutSeconds = 7
	cfg.AgentLoopGuard.MaxSemanticContinuations = 3
	cfg.AgentLoopGuard.NoProgressLimit = 2
	cfg.AgentLoopGuard.ExplicitCompletionPolicy = "trust"
	eff := cfg.EffectiveAgentLoopGuard()
	gate := buildLoopGuard(eff, fake, time.Now, nil)
	require.NotNil(t, gate)
	tf := cleanTerminalFacts()
	_ = gate.ObserveCandidate(context.Background(), tf)
	require.Equal(t, 1, fake.called)
	assert.Equal(t, "my_role", fake.capturedReq.Role)
	assert.Equal(t, "private", fake.capturedReq.Visibility)
	assert.Equal(t, 7, eff.VerifierTimeoutSeconds)
}

func TestLoopGuardWiring_NilClientConservative(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.AgentLoopGuard.Enabled = true
	cfg.AgentLoopGuard.VerifierRole = "loop_guard"
	cfg.AgentLoopGuard.VerifierTimeoutSeconds = 4
	cfg.AgentLoopGuard.MaxSemanticContinuations = 3
	cfg.AgentLoopGuard.NoProgressLimit = 2
	cfg.AgentLoopGuard.ExplicitCompletionPolicy = "trust"
	eff := cfg.EffectiveAgentLoopGuard()
	gate := buildLoopGuard(eff, nil, time.Now, nil)
	require.NotNil(t, gate)
	require.NotPanics(t, func() {
		out := gate.ObserveCandidate(context.Background(), cleanTerminalFacts())
		// Nil client should be conservative UNCERTAIN -> forward terminal
		assert.Equal(t, stopguard.ActionForwardTerminal, out.Action)
		assert.True(t, out.HoldReleased)
		assert.Contains(t, out.Reason, "uncertain")
	})
}

func TestLoopGuardWiring_ObserverSeamHonest(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.AgentLoopGuard.Enabled = true
	cfg.AgentLoopGuard.VerifierRole = "loop_guard"
	cfg.AgentLoopGuard.VerifierTimeoutSeconds = 4
	cfg.AgentLoopGuard.MaxSemanticContinuations = 3
	cfg.AgentLoopGuard.NoProgressLimit = 2
	cfg.AgentLoopGuard.ExplicitCompletionPolicy = "trust"
	eff := cfg.EffectiveAgentLoopGuard()
	fake := &fakeAuxForWiring{
		text:         `{"kind":"allow_stop","reason":"ok"}`,
		inputTokens:  11,
		outputTokens: 22,
		totalTokens:  33,
		costNano:     99,
	}
	var observed stopguardverify.VerifyObservation
	var called int
	obs := func(o stopguardverify.VerifyObservation) {
		called++
		observed = o
	}
	gate := buildLoopGuard(eff, fake, time.Now, obs)
	require.NotNil(t, gate)
	out := gate.ObserveCandidate(context.Background(), cleanTerminalFacts())
	require.Equal(t, stopguard.ActionForwardTerminal, out.Action)
	require.Equal(t, 1, called, "observer must be called exactly once")
	assert.GreaterOrEqual(t, observed.Latency, time.Duration(0))
	assert.Equal(t, 11, observed.InputTokens)
	assert.Equal(t, 22, observed.OutputTokens)
	assert.Equal(t, 33, observed.TotalTokens)
	assert.Equal(t, int64(99), observed.CostNanoUnits)
	assert.NoError(t, observed.Err)
	// Error path
	fakeErr := &fakeAuxForWiring{err: assert.AnError}
	called = 0
	gateErr := buildLoopGuard(eff, fakeErr, time.Now, obs)
	out2 := gateErr.ObserveCandidate(context.Background(), cleanTerminalFacts())
	assert.Equal(t, stopguard.ActionForwardTerminal, out2.Action)
	require.Equal(t, 1, called)
	assert.Error(t, observed.Err)
}
