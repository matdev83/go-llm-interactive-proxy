package stopguard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func userMsg(text string) lipapi.Message {
	return lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(text)}}
}

func assistantItem(text string) lipapi.Item {
	return lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		Role:    lipapi.RoleAssistant,
		Content: []lipapi.ContentPart{lipapi.ContentPart{Text: text}},
	}
}

// TestCauseValues proves the canonical, provider-neutral cause vocabulary.
// (Requirement 2.1, Design Terminal Causes)
func TestCauseValues(t *testing.T) {
	t.Parallel()

	for _, cause := range []Cause{
		CauseNormalEnd, CauseEmptyNormalEnd, CauseProviderPause, CauseOutputLimit,
		CauseTransportEOFPreCommit, CauseTransportEOFPostCommit,
		CauseIdlePreCommit, CauseIdlePostCommit,
		CausePartialToolCall, CauseRefusalOrFilter, CauseClientCancel, CauseUnknownTerminal,
	} {
		assert.NotEmpty(t, string(cause))
	}
}

// TestNormalizeVerdict_ConservativeFallbacks proves every verifier failure mode
// normalizes to an allowed stop: error, unknown verdict kind, empty kind, and
// CONTINUE without a concrete remaining objective.
// (Requirements 5.6, 12.8, Design Verifier Verdict)
func TestNormalizeVerdict_ConservativeFallbacks(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		verdict  Verdict
		err      error
		wantKind VerdictKind
	}{
		{name: "transport_error", verdict: Verdict{}, err: errors.New("aux unavailable"), wantKind: VerdictUncertain},
		{name: "unknown_kind", verdict: Verdict{Kind: VerdictKind("MAYBE")}, wantKind: VerdictUncertain},
		{name: "empty_kind", verdict: Verdict{}, wantKind: VerdictUncertain},
		{
			name:     "continue_without_objective",
			verdict:  Verdict{Kind: VerdictContinue, Reason: "tests missing"},
			wantKind: VerdictUncertain,
		},
		{
			name:     "continue_whitespace_objective",
			verdict:  Verdict{Kind: VerdictContinue, RemainingObjective: "   "},
			wantKind: VerdictUncertain,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeVerdict(tc.verdict, tc.err)
			assert.Equal(t, tc.wantKind, got.Kind, "uncertainty must normalize toward allowing the stop")
		})
	}
}

// TestNormalizeVerdict_ActionableContinueAndBoundedFields proves an actionable
// CONTINUE requires a non-empty remaining objective and that reason/objective
// text is bounded. (Requirements 5.3, 6.3, Design Verifier Verdict)
func TestNormalizeVerdict_ActionableContinueAndBoundedFields(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", MaxReasonBytes*4)
	got := NormalizeVerdict(Verdict{
		Kind:               VerdictContinue,
		Reason:             long,
		RemainingObjective: strings.Repeat("y", MaxReasonBytes*4),
	}, nil)

	require.Equal(t, VerdictContinue, got.Kind)
	assert.NotEmpty(t, got.RemainingObjective)
	assert.LessOrEqual(t, len(got.Reason), MaxReasonBytes)
	assert.LessOrEqual(t, len(got.RemainingObjective), MaxRemainingObjectiveBytes)
}

// TestDecide_NonSemanticCauses proves explicit control outcomes are never
// reinterpreted as unfinished work and pre-output transport failures delegate to
// existing recovery instead of the guard. (Requirements 2.5, 3.1, 5.7)
func TestDecide_NonSemanticCauses(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		candidate Candidate
		want      Decision
	}{
		{
			name:      "client_cancel_preserved",
			candidate: Candidate{Cause: CauseClientCancel},
			want:      Decision{Action: ActionForwardTerminal},
		},
		{
			name:      "refusal_preserved",
			candidate: Candidate{Cause: CauseRefusalOrFilter},
			want:      Decision{Action: ActionForwardTerminal},
		},
		{
			name:      "precommit_eof_delegates_to_existing_recovery",
			candidate: Candidate{Cause: CauseTransportEOFPreCommit, OutputCommitted: false},
			want:      Decision{Action: ActionDelegatePreOutputRecovery},
		},
		{
			name:      "precommit_idle_delegates_to_existing_recovery",
			candidate: Candidate{Cause: CauseIdlePreCommit},
			want:      Decision{Action: ActionDelegatePreOutputRecovery},
		},
		{
			name:      "empty_normal_end_retries_via_existing_path_before_commit",
			candidate: Candidate{Cause: CauseEmptyNormalEnd, OutputCommitted: false, EmptyRetryEligible: true},
			want:      Decision{Action: ActionDelegatePreOutputRecovery},
		},
		{
			name:      "output_limit_owned_elsewhere",
			candidate: Candidate{Cause: CauseOutputLimit},
			want:      Decision{Action: ActionForwardTerminal},
		},
		{
			name:      "provider_pause_with_native_resume_continues",
			candidate: Candidate{Cause: CauseProviderPause, SafeNativeResume: true},
			want:      Decision{Action: ActionContinueLeg},
		},
		{
			name:      "provider_pause_without_native_resume_stops",
			candidate: Candidate{Cause: CauseProviderPause},
			want:      Decision{Action: ActionForwardTerminal},
		},
		{
			name:      "partial_tool_call_without_safe_resume_surfaces_failure",
			candidate: Candidate{Cause: CausePartialToolCall, OutputCommitted: true},
			want:      Decision{Action: ActionSurfaceFailure},
		},
		{
			name:      "partial_tool_call_with_proven_native_resume_continues",
			candidate: Candidate{Cause: CausePartialToolCall, OutputCommitted: true, SafeNativeResume: true},
			want:      Decision{Action: ActionContinueLeg},
		},
		{
			name:      "unknown_terminal_is_conservative",
			candidate: Candidate{Cause: CauseUnknownTerminal},
			want:      Decision{Action: ActionForwardTerminal},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Decide(tc.candidate, PolicyTrust)
			assert.Equal(t, tc.want.Action, got.Action)
			assert.False(t, got.Verify, "non-clean-stop causes must not spend a verifier call")
		})
	}
}

// TestDecide_PostOutputInterruption proves post-output transport interruptions
// continue from the retained trajectory only when the runtime-proven canonical
// state is safe, and otherwise surface one controlled final outcome.
// (Requirements 4.1, 4.2, 4.5, 12.6, 12.7)
func TestDecide_PostOutputInterruption(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		candidate Candidate
		want      Action
	}{
		{
			name:      "postcommit_eof_with_safe_trajectory_continues",
			candidate: Candidate{Cause: CauseTransportEOFPostCommit, OutputCommitted: true, SafeCanonicalContinuation: true},
			want:      ActionContinueLeg,
		},
		{
			name:      "postcommit_idle_with_safe_trajectory_continues",
			candidate: Candidate{Cause: CauseIdlePostCommit, OutputCommitted: true, SafeCanonicalContinuation: true},
			want:      ActionContinueLeg,
		},
		{
			name:      "postcommit_eof_unsafe_state_surfaces_failure_no_replay",
			candidate: Candidate{Cause: CauseTransportEOFPostCommit, OutputCommitted: true},
			want:      ActionSurfaceFailure,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, Decide(tc.candidate, PolicyTrust).Action)
		})
	}
}

// TestDecide_ExplicitCompletionPolicy proves trust skips verification for clean
// stops carrying the normalized completion fact, verify still verifies, and a
// clean stop without the fact always verifies regardless of policy.
// (Requirement 5.7, Design Frontend Explicit Completion Signals)
func TestDecide_ExplicitCompletionPolicy(t *testing.T) {
	t.Parallel()

	clean := Candidate{Cause: CauseNormalEnd}

	trust := Decide(clean, PolicyTrust)
	assert.True(t, trust.Verify)

	explicit := clean
	explicit.ExplicitCompletion = true

	assert.Equal(t, ActionForwardTerminal, Decide(explicit, PolicyTrust).Action,
		"trusted explicit completion releases the terminal without verification")
	assert.False(t, Decide(explicit, PolicyTrust).Verify)

	verifyDecision := Decide(explicit, PolicyVerify)
	assert.True(t, verifyDecision.Verify, "verify policy treats explicit completion as evidence only")

	assert.True(t, Decide(clean, PolicyVerify).Verify)
}

// TestDecideWithVerdict_VerdictActions proves only a high-confidence actionable
// CONTINUE continues; ALLOW_STOP, NEEDS_USER, BLOCKED, and normalized UNCERTAIN
// release exactly one final terminal. (Requirements 5.2-5.6, 12.2-12.4, 12.8)
func TestDecideWithVerdict_VerdictActions(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		verdict Verdict
		err     error
		want    Action
	}{
		{name: "allow_stop_releases", verdict: Verdict{Kind: VerdictAllowStop}, want: ActionForwardTerminal},
		{name: "needs_user_releases", verdict: Verdict{Kind: VerdictNeedsUser, Reason: "asks user"}, want: ActionForwardTerminal},
		{name: "blocked_releases", verdict: Verdict{Kind: VerdictBlocked, Reason: "external"}, want: ActionForwardTerminal},
		{name: "uncertain_releases", verdict: Verdict{Kind: VerdictUncertain}, want: ActionForwardTerminal},
		{name: "verifier_error_allows_stop", err: context.DeadlineExceeded, want: ActionForwardTerminal},
		{
			name:    "actionable_continue_continues_leg",
			verdict: Verdict{Kind: VerdictContinue, RemainingObjective: "run the requested test suite"},
			want:    ActionContinueLeg,
		},
		{
			name:    "continue_without_objective_never_continues",
			verdict: Verdict{Kind: VerdictContinue},
			want:    ActionForwardTerminal,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, DecideWithVerdict(Candidate{Cause: CauseNormalEnd}, tc.verdict, tc.err))
		})
	}
}

// TestVerifierContract proves the consumer-owned verifier boundary accepts
// canonical evidence and returns a verdict. (Design Verifier Verdict, Guard Evidence)
func TestVerifierContract(t *testing.T) {
	t.Parallel()

	var verifier Verifier = VerifierFunc(func(ctx context.Context, evidence Evidence) (Verdict, error) {
		require.Equal(t, CauseNormalEnd, evidence.Cause)
		require.True(t, evidence.OutputCommitted)
		require.Len(t, evidence.UserObjective, 1)
		return Verdict{Kind: VerdictAllowStop}, nil
	})

	v, err := verifier.Verify(context.Background(), Evidence{
		Cause:              CauseNormalEnd,
		UserObjective:      []lipapi.Message{userMsg("fix the failing tests")},
		CandidateAssistant: []lipapi.Item{assistantItem("Done; tests pass.")},
		OutputCommitted:    true,
	})
	require.NoError(t, err)
	assert.Equal(t, VerdictAllowStop, v.Kind)
}
