package stopguardverify_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/lineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguardverify"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ stopguard.Verifier = (*stopguardverify.Adapter)(nil)

type fakeAuxClient struct {
	calls       int
	lastReq     auxiliary.Request
	lastCtx     context.Context
	hasDeadline bool
	deadline    time.Time
	collected   lipapi.Collected
	err         error
}

func (f *fakeAuxClient) Collect(ctx context.Context, req auxiliary.Request) (lipapi.Collected, error) {
	f.calls++
	f.lastReq = req
	f.lastCtx = ctx
	if d, ok := ctx.Deadline(); ok {
		f.hasDeadline = true
		f.deadline = d
	}
	if f.err != nil {
		return lipapi.Collected{}, f.err
	}
	return f.collected, nil
}

func (f *fakeAuxClient) Stream(_ context.Context, _ auxiliary.Request) (lipapi.EventStream, error) {
	return nil, errors.New("stream not used in verifier")
}

func collectedWithText(s string) lipapi.Collected {
	var c lipapi.Collected
	c.Text.WriteString(s)
	return c
}

// TestAdapter_RequestProperties_RoleVisibilityDetachedAndLineageRecursionSuppressed asserts
// dedicated Role==cfg.Role, internal/detached visibility + SessionModeDetached per auxiliary
// conventions, parent trace/A-leg/B-leg/branch lineage plumbed from ctx or Evidence, and
// DisablePlugins includes the loop-guard plugin identifier so recursion is suppressed.
// ParentTraceID/ParentALegID/ParentBLegID/ParentBranchBinding are copied verbatim from
// Evidence.Parent* fields (keyed-literal lineage inheritance); DisablePlugins via "agent_loop_guard",
// visibility via "private", session mode via auxiliary.SessionModeDetached.
func TestAdapter_RequestProperties_RoleVisibilityDetachedAndLineageRecursionSuppressed(t *testing.T) {
	t.Parallel()
	cfg := stopguardverify.AdapterConfig{
		Role:    config.DefaultAgentLoopGuardVerifierRole,
		Timeout: 4 * time.Second,
	}
	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"allow_stop","reason":"done","remaining_objective":""}`)}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	ctx := lineage.WithTraceID(context.Background(), "trace-xyz")
	ctx = lineage.WithALeg(ctx, "a-leg-123")
	evidence := stopguard.Evidence{
		Cause:               stopguard.CauseNormalEnd,
		ParentTraceID:       "trace-xyz",
		ParentALegID:        "a-leg-123",
		ParentBLegID:        "b-leg-789",
		ParentBranchBinding: "branch-abc",
		ContinuationLineage: stopguard.ContinuationRef{ContinuationID: "branch-legacy-ignored"},
		ToolState:           stopguard.ToolCompletionState{PendingToolCallID: "b-leg-ignored"},
	}
	_, err := adapter.Verify(ctx, evidence)
	require.NoError(t, err)
	require.Equal(t, 1, fake.calls)
	req := fake.lastReq
	assert.Equal(t, cfg.Role, req.Role)
	assert.Equal(t, auxiliary.SessionModeDetached, req.SessionMode)
	assert.Equal(t, "private", req.Visibility)
	assert.Equal(t, "trace-xyz", req.ParentTraceID)
	assert.Equal(t, "a-leg-123", req.ParentALegID)
	assert.Equal(t, "b-leg-789", req.ParentBLegID)
	assert.Equal(t, "branch-abc", req.ParentBranchBinding)
	assert.Contains(t, req.DisablePlugins, "agent_loop_guard")
}

// TestAdapter_BoundedDeadline_DeadlinePresentAndWithinTimeout asserts bounded deadline.
func TestAdapter_BoundedDeadline_DeadlinePresentAndWithinTimeout(t *testing.T) {
	t.Parallel()
	timeout := 400 * time.Millisecond
	cfg := stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: timeout}
	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"allow_stop","reason":"ok"}`)}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	start := time.Now()
	_, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
	require.NoError(t, err)
	require.True(t, fake.hasDeadline, "verifier request must have a bounded deadline")
	remaining := time.Until(fake.deadline)
	assert.Greater(t, remaining, time.Duration(0))
	assert.LessOrEqual(t, remaining, timeout)
	// also ensure deadline is not far in future relative to start
	assert.LessOrEqual(t, fake.deadline.Sub(start), timeout+50*time.Millisecond)
}

// TestAdapter_BoundedDeadline_CancelledContextDoesNotCallClient asserts already-cancelled ctx returns error without calling client.
func TestAdapter_BoundedDeadline_CancelledContextDoesNotCallClient(t *testing.T) {
	t.Parallel()
	cfg := stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second}
	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"allow_stop","reason":"ok"}`)}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := adapter.Verify(ctx, stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
	require.Error(t, err)
	assert.Equal(t, 0, fake.calls)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestAdapter_StrictStructuredParsing_AllFiveKinds asserts strict JSON parsing for all five verdict kinds.
func TestAdapter_StrictStructuredParsing_AllFiveKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		json     string
		wantKind stopguard.VerdictKind
	}{
		{name: "allow_stop", json: `{"kind":"allow_stop","reason":"done","remaining_objective":""}`, wantKind: stopguard.VerdictAllowStop},
		{name: "continue", json: `{"kind":"continue","reason":"more work","remaining_objective":"run tests"}`, wantKind: stopguard.VerdictContinue},
		{name: "needs_user", json: `{"kind":"needs_user","reason":"need approval"}`, wantKind: stopguard.VerdictNeedsUser},
		{name: "blocked", json: `{"kind":"blocked","reason":"external block"}`, wantKind: stopguard.VerdictBlocked},
		{name: "uncertain", json: `{"kind":"uncertain","reason":"not sure"}`, wantKind: stopguard.VerdictUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second}
			fake := &fakeAuxClient{collected: collectedWithText(tc.json)}
			adapter := stopguardverify.NewAdapter(fake, cfg)
			verdict, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
			require.NoError(t, err)
			assert.Equal(t, tc.wantKind, verdict.Kind)
		})
	}
}

// TestAdapter_StrictStructuredParsing_UnknownKindIsConservative asserts unknown kind normalizes conservatively via stopguard.NormalizeVerdict.
func TestAdapter_StrictStructuredParsing_UnknownKindIsConservative(t *testing.T) {
	t.Parallel()
	cfg := stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second}
	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"bogus","reason":"x"}`)}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	verdict, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
	normalized := stopguard.NormalizeVerdict(verdict, err)
	assert.Equal(t, stopguard.VerdictUncertain, normalized.Kind)
	assert.NotEqual(t, stopguard.VerdictContinue, normalized.Kind)
}

// TestAdapter_ConservativeNormalization_Inputs asserts timeout, transport error, malformed output, unknown verdict,
// and CONTINUE without concrete remaining objective normalize conservatively (NormalizeVerdict yields non-CONTINUE).
func TestAdapter_ConservativeNormalization_Inputs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		fakeSetup func() *fakeAuxClient
	}{
		{
			name: "timeout_error",
			fakeSetup: func() *fakeAuxClient {
				return &fakeAuxClient{err: context.DeadlineExceeded}
			},
		},
		{
			name: "transport_error",
			fakeSetup: func() *fakeAuxClient {
				return &fakeAuxClient{err: errors.New("transport failure")}
			},
		},
		{
			name: "malformed_json",
			fakeSetup: func() *fakeAuxClient {
				return &fakeAuxClient{collected: collectedWithText(`{ not json`)}
			},
		},
		{
			name: "unknown_kind",
			fakeSetup: func() *fakeAuxClient {
				return &fakeAuxClient{collected: collectedWithText(`{"kind":"unknown_kind","reason":"x"}`)}
			},
		},
		{
			name: "continue_without_objective_empty",
			fakeSetup: func() *fakeAuxClient {
				return &fakeAuxClient{collected: collectedWithText(`{"kind":"continue","reason":"more","remaining_objective":""}`)}
			},
		},
		{
			name: "continue_without_objective_whitespace",
			fakeSetup: func() *fakeAuxClient {
				return &fakeAuxClient{collected: collectedWithText(`{"kind":"continue","reason":"more","remaining_objective":"   "}`)}
			},
		},
		{
			name: "continue_missing_objective",
			fakeSetup: func() *fakeAuxClient {
				return &fakeAuxClient{collected: collectedWithText(`{"kind":"continue","reason":"more"}`)}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second}
			fake := tc.fakeSetup()
			adapter := stopguardverify.NewAdapter(fake, cfg)
			verdict, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
			normalized := stopguard.NormalizeVerdict(verdict, err)
			assert.Equal(t, stopguard.VerdictUncertain, normalized.Kind, "case %s should normalize to uncertain", tc.name)
			// CONTINUE without objective must never authorize continuation
			assert.NotEqual(t, stopguard.VerdictContinue, normalized.Kind)
			// For error cases, Verify should return error so that NormalizeVerdict yields uncertain via error path
			if tc.name == "timeout_error" || tc.name == "transport_error" || tc.name == "malformed_json" || tc.name == "unknown_kind" {
				// unknown_kind may alternatively return verdict with error; accept either but normalized must be uncertain
				if err == nil {
					assert.Equal(t, stopguard.VerdictUncertain, verdict.Kind)
				}
			}
		})
	}
}

// TestAdapter_NoToolsExposed asserts no tools are needed/exposed for the verifier path.
func TestAdapter_NoToolsExposed(t *testing.T) {
	t.Parallel()
	cfg := stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second}
	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"allow_stop","reason":"done"}`)}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	evidence := stopguard.Evidence{
		Cause: stopguard.CauseNormalEnd,
		ToolState: stopguard.ToolCompletionState{
			CompletedToolResults: 2,
			PendingToolCallID:    "pending-call",
		},
		CandidateAssistant: []lipapi.Item{
			{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "bash"}},
		},
		RecentTrajectory: []lipapi.Item{
			{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "c2", Name: "write"}},
		},
		UserObjective: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("do work")}}},
	}
	_, err := adapter.Verify(context.Background(), evidence)
	require.NoError(t, err)
	req := fake.lastReq
	require.NotNil(t, req.Call)
	assert.Empty(t, req.Call.Tools)
	assert.Equal(t, lipapi.ToolChoice{}, req.Call.ToolChoice)
}

// TestAdapter_ReasonAndObjectiveBounded asserts reason/objective propagation bounded per stopguard limits.
func TestAdapter_ReasonAndObjectiveBounded(t *testing.T) {
	t.Parallel()
	cfg := stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second}
	longReason := strings.Repeat("r", stopguard.MaxReasonBytes+100)
	longObj := strings.Repeat("o", stopguard.MaxRemainingObjectiveBytes+50)
	jsonStr := fmt.Sprintf(`{"kind":"continue","reason":%q,"remaining_objective":%q}`, longReason, longObj)
	fake := &fakeAuxClient{collected: collectedWithText(jsonStr)}
	adapter := stopguardverify.NewAdapter(fake, cfg)
	verdict, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(verdict.Reason), stopguard.MaxReasonBytes)
	assert.LessOrEqual(t, len(verdict.RemainingObjective), stopguard.MaxRemainingObjectiveBytes)
	assert.Equal(t, stopguard.VerdictContinue, verdict.Kind)
	// also ensure allow_stop reason is bounded
	t.Run("allow_stop_long_reason_bounded", func(t *testing.T) {
		t.Parallel()
		longR := strings.Repeat("x", stopguard.MaxReasonBytes+200)
		js := fmt.Sprintf(`{"kind":"allow_stop","reason":%q}`, longR)
		f2 := &fakeAuxClient{collected: collectedWithText(js)}
		a2 := stopguardverify.NewAdapter(f2, cfg)
		v2, err2 := a2.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
		require.NoError(t, err2)
		assert.LessOrEqual(t, len(v2.Reason), stopguard.MaxReasonBytes)
		assert.Equal(t, stopguard.VerdictAllowStop, v2.Kind)
	})
}
