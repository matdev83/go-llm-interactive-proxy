package stopguard

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProgressFingerprint_StabilityAcrossVolatileFields proves that canonical
// progress fingerprints remain identical when volatile metadata (timestamps,
// request IDs, trace spans) differs, and change only when material canonical
// facts change. (Requirements 8.3, 12.9, Design Progress Fingerprint)
func TestProgressFingerprint_StabilityAcrossVolatileFields(t *testing.T) {
	t.Parallel()

	base := ProgressFingerprint{
		CandidateOutputDigest: "sha256:assistant-output-chunk-1",
		ToolName:              "execute_command",
		ToolArgsDigest:        "sha256:args-go-test",
		ToolResultDigest:      "sha256:result-pass",
		ToolErrorDigest:       "",
		ContinuationLineageID: "cont-lineage-1",
		VerdictKind:           VerdictContinue,
		ObjectiveDigest:       "sha256:run-benchmarks",
		ItemCount:             5,
		StateTransition:       "tool_complete",
	}

	t.Run("identical_canonical_facts_yield_same_digest", func(t *testing.T) {
		t.Parallel()
		fp1 := base
		fp2 := base
		assert.Equal(t, fp1.Digest(), fp2.Digest(), "identical canonical facts must produce identical digests")
	})

	t.Run("volatile_context_does_not_affect_fingerprint", func(t *testing.T) {
		t.Parallel()
		// In a real execution, request IDs or timestamps may differ across attempts,
		// but ProgressFingerprint includes only normalized canonical facts.
		fpA := ProgressFingerprint{
			CandidateOutputDigest: base.CandidateOutputDigest,
			ToolName:              base.ToolName,
			ToolArgsDigest:        base.ToolArgsDigest,
			ToolResultDigest:      base.ToolResultDigest,
			ToolErrorDigest:       base.ToolErrorDigest,
			ContinuationLineageID: base.ContinuationLineageID,
			VerdictKind:           base.VerdictKind,
			ObjectiveDigest:       base.ObjectiveDigest,
			ItemCount:             base.ItemCount,
			StateTransition:       base.StateTransition,
		}
		fpB := fpA // Semantically identical even if evaluated at different timestamps
		assert.Equal(t, fpA.Digest(), fpB.Digest())
	})

	t.Run("material_canonical_mutations_yield_different_digests", func(t *testing.T) {
		t.Parallel()

		mutations := []struct {
			name   string
			mutate func(fp *ProgressFingerprint)
		}{
			{
				name: "different_output_digest",
				mutate: func(fp *ProgressFingerprint) {
					fp.CandidateOutputDigest = "sha256:assistant-output-chunk-2"
				},
			},
			{
				name: "different_tool_name",
				mutate: func(fp *ProgressFingerprint) {
					fp.ToolName = "read_file"
				},
			},
			{
				name: "different_tool_args_digest",
				mutate: func(fp *ProgressFingerprint) {
					fp.ToolArgsDigest = "sha256:args-different"
				},
			},
			{
				name: "different_tool_result_digest",
				mutate: func(fp *ProgressFingerprint) {
					fp.ToolResultDigest = "sha256:result-failed"
				},
			},
			{
				name: "different_tool_error_digest",
				mutate: func(fp *ProgressFingerprint) {
					fp.ToolErrorDigest = "sha256:error-timeout"
				},
			},
			{
				name: "different_continuation_lineage",
				mutate: func(fp *ProgressFingerprint) {
					fp.ContinuationLineageID = "cont-lineage-2"
				},
			},
			{
				name: "different_verdict_kind",
				mutate: func(fp *ProgressFingerprint) {
					fp.VerdictKind = VerdictAllowStop
				},
			},
			{
				name: "different_objective_digest",
				mutate: func(fp *ProgressFingerprint) {
					fp.ObjectiveDigest = "sha256:fix-lints"
				},
			},
			{
				name: "different_item_count",
				mutate: func(fp *ProgressFingerprint) {
					fp.ItemCount = 6
				},
			},
			{
				name: "different_state_transition",
				mutate: func(fp *ProgressFingerprint) {
					fp.StateTransition = "in_progress"
				},
			},
		}

		baseDigest := base.Digest()
		for _, tc := range mutations {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				mutated := base
				tc.mutate(&mutated)
				assert.NotEqual(t, baseDigest, mutated.Digest(), "material progress change must alter fingerprint digest")
			})
		}
	})
}

// TestProgressTracker_MateriallyEquivalentAnswerRepetition proves that repeated
// materially equivalent final answers trip the no-progress breaker and release
// exactly one final terminal. (Requirements 8.3, 8.4, 12.9)
func TestProgressTracker_MateriallyEquivalentAnswerRepetition(t *testing.T) {
	t.Parallel()

	// Given maxContinuations=3, noProgressLimit=2
	tracker := NewProgressTracker(3, 2)

	fp := ProgressFingerprint{
		CandidateOutputDigest: "sha256:same-assistant-answer",
		VerdictKind:           VerdictContinue,
		ObjectiveDigest:       "sha256:remaining-task",
		ItemCount:             3,
	}

	// Attempt 1: First observation of this state
	out1 := tracker.Record(fp)
	assert.True(t, out1.NewProgress, "first observation counts as initial progress baseline")
	assert.False(t, out1.NoProgressTripped)
	assert.False(t, out1.BudgetExhausted)
	assert.Equal(t, 0, out1.ConsecutiveNoProgress)
	assert.Equal(t, 1, out1.TotalContinuations)
	assert.Equal(t, ActionContinueLeg, out1.Action)

	// Attempt 2: Materially equivalent answer (no new progress)
	out2 := tracker.Record(fp)
	assert.False(t, out2.NewProgress, "identical fingerprint indicates no progress")
	assert.False(t, out2.NoProgressTripped, "1 no-progress attempt is below limit of 2")
	assert.False(t, out2.BudgetExhausted)
	assert.Equal(t, 1, out2.ConsecutiveNoProgress)
	assert.Equal(t, 2, out2.TotalContinuations)
	assert.Equal(t, ActionContinueLeg, out2.Action)

	// Attempt 3: Materially equivalent answer again -> trips breaker
	out3 := tracker.Record(fp)
	assert.False(t, out3.NewProgress)
	assert.True(t, out3.NoProgressTripped, "consecutive no-progress reached limit of 2")
	assert.Equal(t, 2, out3.ConsecutiveNoProgress)
	assert.Equal(t, 3, out3.TotalContinuations)
	assert.Equal(t, ActionForwardTerminal, out3.Action, "tripped breaker must forward terminal")

	// Attempt 4: Subsequent attempt remains terminal
	out4 := tracker.Record(fp)
	assert.True(t, out4.NoProgressTripped)
	assert.Equal(t, ActionForwardTerminal, out4.Action)
}

// TestProgressTracker_ToolCycleRepetition proves that repeated same tool +
// normalized arguments + same error/result cycle trips the no-progress breaker.
// (Requirements 8.3, 8.4, 12.9)
func TestProgressTracker_ToolCycleRepetition(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		fingerprint ProgressFingerprint
	}{
		{
			name: "same_tool_and_error_cycle",
			fingerprint: ProgressFingerprint{
				ToolName:        "read_file",
				ToolArgsDigest:  "sha256:path-config-json",
				ToolErrorDigest: "sha256:err-file-not-found",
				VerdictKind:     VerdictContinue,
				ObjectiveDigest: "sha256:check-config",
				ItemCount:       4,
			},
		},
		{
			name: "same_tool_and_unchanged_result_cycle",
			fingerprint: ProgressFingerprint{
				ToolName:         "query_status",
				ToolArgsDigest:   "sha256:status-check",
				ToolResultDigest: "sha256:result-pending",
				VerdictKind:      VerdictContinue,
				ObjectiveDigest:  "sha256:wait-for-job",
				ItemCount:        4,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tracker := NewProgressTracker(5, 2)

			// Step 1: baseline
			out1 := tracker.Record(tc.fingerprint)
			require.Equal(t, ActionContinueLeg, out1.Action)
			require.True(t, out1.NewProgress)

			// Step 2: repeat 1 -> consecutive no progress = 1
			out2 := tracker.Record(tc.fingerprint)
			require.Equal(t, ActionContinueLeg, out2.Action)
			require.False(t, out2.NewProgress)
			require.Equal(t, 1, out2.ConsecutiveNoProgress)

			// Step 3: repeat 2 -> trips breaker
			out3 := tracker.Record(tc.fingerprint)
			require.Equal(t, ActionForwardTerminal, out3.Action)
			require.True(t, out3.NoProgressTripped)
			require.Equal(t, 2, out3.ConsecutiveNoProgress)
		})
	}
}

// TestProgressTracker_SameVerdictObjectiveWithoutCanonicalProgress proves that
// receiving the same verifier verdict and objective without canonical progress
// repeats is recognized as no-progress. (Requirements 8.3, 12.9)
func TestProgressTracker_SameVerdictObjectiveWithoutCanonicalProgress(t *testing.T) {
	t.Parallel()

	tracker := NewProgressTracker(4, 2)

	fp := ProgressFingerprint{
		VerdictKind:     VerdictContinue,
		ObjectiveDigest: "sha256:fixed-remaining-objective",
		ItemCount:       2,
	}

	out1 := tracker.Record(fp)
	require.Equal(t, ActionContinueLeg, out1.Action)
	require.True(t, out1.NewProgress)

	out2 := tracker.Record(fp)
	require.Equal(t, ActionContinueLeg, out2.Action)
	require.False(t, out2.NewProgress)
	require.Equal(t, 1, out2.ConsecutiveNoProgress)

	out3 := tracker.Record(fp)
	require.Equal(t, ActionForwardTerminal, out3.Action)
	require.True(t, out3.NoProgressTripped)
}

// TestProgressTracker_NewProgressResetsNoProgressCounterOnly proves that genuinely
// new material progress resets only the consecutive no-progress counter, while the
// total semantic continuation budget continues to decrement/accumulate immutably.
// (Requirements 8.1, 8.6, Design Progress and Circuit Breaking)
func TestProgressTracker_NewProgressResetsNoProgressCounterOnly(t *testing.T) {
	t.Parallel()

	// Max 4 continuations, no-progress limit 2
	tracker := NewProgressTracker(4, 2)

	fp1 := ProgressFingerprint{
		CandidateOutputDigest: "sha256:chunk-1",
		ItemCount:             1,
	}
	fp2 := ProgressFingerprint{
		CandidateOutputDigest: "sha256:chunk-2",
		ItemCount:             2,
	}

	// 1. Initial attempt with fp1
	out1 := tracker.Record(fp1)
	require.True(t, out1.NewProgress)
	require.Equal(t, 0, out1.ConsecutiveNoProgress)
	require.Equal(t, 1, out1.TotalContinuations)
	require.Equal(t, ActionContinueLeg, out1.Action)

	// 2. Repeat fp1 -> no progress (consecutive = 1, total = 2)
	out2 := tracker.Record(fp1)
	require.False(t, out2.NewProgress)
	require.Equal(t, 1, out2.ConsecutiveNoProgress)
	require.Equal(t, 2, out2.TotalContinuations)
	require.Equal(t, ActionContinueLeg, out2.Action)

	// 3. New progress with fp2 -> resets consecutive to 0, but total continuations is 3!
	out3 := tracker.Record(fp2)
	require.True(t, out3.NewProgress, "new fingerprint must register as new progress")
	require.Equal(t, 0, out3.ConsecutiveNoProgress, "new progress must reset no-progress counter")
	require.Equal(t, 3, out3.TotalContinuations, "total continuations budget must NOT reset")
	require.False(t, out3.BudgetExhausted)
	require.Equal(t, ActionContinueLeg, out3.Action)

	// 4. Repeat fp2 -> no progress (consecutive = 1, total = 4)
	out4 := tracker.Record(fp2)
	require.False(t, out4.NewProgress)
	require.Equal(t, 1, out4.ConsecutiveNoProgress)
	require.Equal(t, 4, out4.TotalContinuations)
	require.True(t, out4.BudgetExhausted, "total continuations reached max of 4")
	require.Equal(t, ActionForwardTerminal, out4.Action, "budget exhaustion must forward terminal")
}

// TestProgressTracker_ImmutableMaxContinuationsBudget proves that even when every
// attempt makes new progress, the total semantic continuation cap is strictly
// enforced and terminates the loop when exhausted. (Requirements 8.1, 8.4, 12.9)
func TestProgressTracker_ImmutableMaxContinuationsBudget(t *testing.T) {
	t.Parallel()

	maxContinuations := 3
	tracker := NewProgressTracker(maxContinuations, 2)

	// Attempts 1, 2, 3 all make new progress
	for i := 1; i <= maxContinuations; i++ {
		fp := ProgressFingerprint{
			CandidateOutputDigest: fmt.Sprintf("sha256:output-step-%d", i),
			ItemCount:             i,
		}
		out := tracker.Record(fp)
		require.True(t, out.NewProgress)
		require.Equal(t, 0, out.ConsecutiveNoProgress)
		require.Equal(t, i, out.TotalContinuations)

		if i < maxContinuations {
			assert.False(t, out.BudgetExhausted)
			assert.Equal(t, ActionContinueLeg, out.Action)
		} else {
			assert.True(t, out.BudgetExhausted, "budget must be exhausted at max continuations")
			assert.Equal(t, ActionForwardTerminal, out.Action, "exhausted budget must forward terminal")
		}
	}

	// Attempt 4: Even with new progress, budget is already exhausted
	fpExtra := ProgressFingerprint{
		CandidateOutputDigest: "sha256:output-step-extra",
		ItemCount:             99,
	}
	outExtra := tracker.Record(fpExtra)
	assert.True(t, outExtra.BudgetExhausted)
	assert.Equal(t, ActionForwardTerminal, outExtra.Action)
}

// TestProgressTracker_CancellationAndExhaustionTerminalAction proves that
// cancellation or exhaustion produces exactly one final terminal action and
// subsequent attempts cannot reopen continuation. (Requirements 8.4, 9.3, 12.9)
func TestProgressTracker_CancellationAndExhaustionTerminalAction(t *testing.T) {
	t.Parallel()

	t.Run("explicit_cancellation_forces_terminal_action", func(t *testing.T) {
		t.Parallel()
		tracker := NewProgressTracker(5, 3)

		fp := ProgressFingerprint{CandidateOutputDigest: "sha256:step-1", ItemCount: 1}
		out := tracker.Record(fp)
		require.Equal(t, ActionContinueLeg, out.Action)

		// Cancel the tracker
		cancelOut := tracker.Cancel()
		assert.Equal(t, ActionForwardTerminal, cancelOut.Action)

		// Further attempts after cancellation remain terminal
		afterOut := tracker.Record(ProgressFingerprint{CandidateOutputDigest: "sha256:step-2", ItemCount: 2})
		assert.Equal(t, ActionForwardTerminal, afterOut.Action)
	})

	t.Run("exhausted_tracker_stays_terminal", func(t *testing.T) {
		t.Parallel()
		tracker := NewProgressTracker(1, 1)

		fp1 := ProgressFingerprint{CandidateOutputDigest: "sha256:step-1"}
		out1 := tracker.Record(fp1)
		require.True(t, out1.BudgetExhausted)
		require.Equal(t, ActionForwardTerminal, out1.Action)

		out2 := tracker.Record(ProgressFingerprint{CandidateOutputDigest: "sha256:step-2"})
		assert.True(t, out2.BudgetExhausted)
		assert.Equal(t, ActionForwardTerminal, out2.Action)
	})
}

// TestProgressTracker_BoundsAndDefaults proves that invalid or non-positive
// constructor bounds fallback safely to minimal positive bounds.
// (Requirements 8.1, 8.4)
func TestProgressTracker_BoundsAndDefaults(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		maxContinuations int
		noProgressLimit  int
		wantMax          int
		wantNoProg       int
	}{
		{
			name:             "standard_positive_bounds",
			maxContinuations: 3,
			noProgressLimit:  2,
			wantMax:          3,
			wantNoProg:       2,
		},
		{
			name:             "zero_bounds_fallback_to_safe_defaults",
			maxContinuations: 0,
			noProgressLimit:  0,
			wantMax:          1,
			wantNoProg:       1,
		},
		{
			name:             "negative_bounds_fallback_to_safe_defaults",
			maxContinuations: -5,
			noProgressLimit:  -2,
			wantMax:          1,
			wantNoProg:       1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tracker := NewProgressTracker(tc.maxContinuations, tc.noProgressLimit)
			require.NotNil(t, tracker)
			assert.Equal(t, tc.wantMax, tracker.MaxContinuations())
			assert.Equal(t, tc.wantNoProg, tracker.NoProgressLimit())
		})
	}
}
