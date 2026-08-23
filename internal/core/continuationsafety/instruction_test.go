package continuationsafety

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryInstruction_AutomatedRecoveryTagAndInternalWording(t *testing.T) {
	t.Parallel()
	in := RecoveryInput{
		Reason:             "verifier reason",
		RemainingObjective: "finish wiring B-leg",
		Attempt:            1,
		MaxAttempts:        3,
		WorkComplete:       false,
		NeedsUser:          false,
	}
	out := BuildRecoveryInstruction(in)
	lower := strings.ToLower(out)
	assert.Contains(t, out, "<automated-recovery>", "must contain open automated-recovery tag")
	assert.Contains(t, out, "</automated-recovery>", "must contain close automated-recovery tag")
	assert.Contains(t, lower, "internal recovery instruction", "must state it is internal recovery instruction")
	assert.Contains(t, lower, "not a new user request", "must negate new user request")
	assert.Contains(t, lower, "approval", "must negate approval")
	assert.Contains(t, lower, "expansion of scope", "must negate expansion of scope")
	assert.Contains(t, out, "The user has not sent a new message", "must state user has not sent a new message")
	// Ensure negation phrases are present in lower as well for robustness.
	assert.Contains(t, lower, "not", "negation must be present")
}

func TestRecoveryInstruction_CompleteWorkBranch(t *testing.T) {
	t.Parallel()
	in := RecoveryInput{
		Reason:             "work appears complete",
		RemainingObjective: "",
		Attempt:            2,
		MaxAttempts:        3,
		WorkComplete:       true,
		NeedsUser:          false,
	}
	out := BuildRecoveryInstruction(in)
	lower := strings.ToLower(out)
	assert.Contains(t, lower, "end normally", "completed worker must be told to end normally")
	// Prohibitions listing invent/repeat/broaden/optimize/discover
	assert.Contains(t, out, "DO NOT invent", "must contain DO NOT invent prohibition")
	assert.Contains(t, lower, "repeat", "must prohibit repeat")
	assert.Contains(t, lower, "broaden", "must prohibit broaden")
	assert.Contains(t, lower, "optimize", "must prohibit optimize")
	assert.Contains(t, lower, "discover", "must prohibit discover")
}

func TestRecoveryInstruction_UnfinishedBranch(t *testing.T) {
	t.Parallel()
	in := RecoveryInput{
		Reason:             "tests not yet executed",
		RemainingObjective: "run go test ./...",
		Attempt:            1,
		MaxAttempts:        3,
		WorkComplete:       false,
		NeedsUser:          false,
	}
	out := BuildRecoveryInstruction(in)
	lower := strings.ToLower(out)
	assert.Contains(t, lower, "resume exactly that work", "unfinished worker must be told to resume exactly that work")
	assert.Contains(t, lower, "last safe point", "must reference last safe point")
}

func TestRecoveryInstruction_UserDependentBranch(t *testing.T) {
	t.Parallel()
	in := RecoveryInput{
		Reason:             "needs permission to proceed",
		RemainingObjective: "await approval for deployment",
		Attempt:            1,
		MaxAttempts:        3,
		WorkComplete:       false,
		NeedsUser:          true,
	}
	out := BuildRecoveryInstruction(in)
	lower := strings.ToLower(out)
	assert.Contains(t, lower, "do not assume", "user-dependent worker must not infer answer/permission")
	assert.Contains(t, lower, "end normally so the user can respond", "must instruct to end normally so user can respond")
}

func TestRecoveryInstruction_FactsLineAndBoundedEcho(t *testing.T) {
	t.Parallel()
	// Verify attempt formatting and bounded echo.
	reason := "bounded reason"
	objective := "bounded objective"
	in := RecoveryInput{
		Reason:             reason,
		RemainingObjective: objective,
		Attempt:            2,
		MaxAttempts:        5,
		WorkComplete:       false,
		NeedsUser:          false,
	}
	out := BuildRecoveryInstruction(in)
	assert.Contains(t, out, "Attempt 2/5", "must contain formatted attempt current/maximum")
	assert.Contains(t, out, reason, "must echo bounded reason")
	assert.Contains(t, out, objective, "must echo bounded objective")
	// Inputs longer than bounds are truncated by BoundRecoveryText.
	longReason := strings.Repeat("r", MaxRecoveryReasonBytes+100)
	longObjective := strings.Repeat("o", MaxRecoveryObjectiveBytes+100)
	boundedReason := BoundRecoveryText(longReason, MaxRecoveryReasonBytes)
	boundedObjective := BoundRecoveryText(longObjective, MaxRecoveryObjectiveBytes)
	require.LessOrEqual(t, len(boundedReason), MaxRecoveryReasonBytes)
	require.LessOrEqual(t, len(boundedObjective), MaxRecoveryObjectiveBytes)
	require.True(t, utf8.ValidString(boundedReason), "BoundRecoveryText must not produce partial runes for reason")
	require.True(t, utf8.ValidString(boundedObjective), "BoundRecoveryText must not produce partial runes for objective")
	// The builder must use bounded truncation — long inputs must not appear verbatim.
	inLong := RecoveryInput{
		Reason:             longReason,
		RemainingObjective: longObjective,
		Attempt:            1,
		MaxAttempts:        3,
		WorkComplete:       false,
		NeedsUser:          false,
	}
	outLong := BuildRecoveryInstruction(inLong)
	assert.NotContains(t, outLong, longReason, "builder must not emit unbounded reason verbatim")
	assert.NotContains(t, outLong, longObjective, "builder must not emit unbounded objective verbatim")
	assert.Contains(t, outLong, boundedReason, "builder must contain truncated reason")
	assert.Contains(t, outLong, boundedObjective, "builder must contain truncated objective")
	assert.Contains(t, outLong, "Recovery reason:", "must carry recovery reason fact")
	assert.Contains(t, outLong, "Remaining objective:", "must carry remaining objective fact")
}

func TestBoundRecoveryText_UTF8SafeNoPartialRunes(t *testing.T) {
	t.Parallel()
	// Multi-byte rune input: each "€" is 3 bytes, "世" is 3 bytes.
	multi := strings.Repeat("世", 300) // 900 bytes
	require.Greater(t, len(multi), MaxRecoveryReasonBytes)
	truncated := BoundRecoveryText(multi, MaxRecoveryReasonBytes)
	require.LessOrEqual(t, len(truncated), MaxRecoveryReasonBytes)
	require.True(t, utf8.ValidString(truncated), "must not cut in middle of UTF-8 rune")
	// Verify last rune is not partial.
	if len(truncated) > 0 {
		_, size := utf8.DecodeLastRuneInString(truncated)
		require.NotEqual(t, utf8.RuneError, rune(truncated[len(truncated)-size]), "last rune must be valid")
	}
	// ASCII short input not truncated.
	short := "hello"
	assert.Equal(t, short, BoundRecoveryText(short, MaxRecoveryObjectiveBytes))
	// Exact bound.
	exact := strings.Repeat("a", MaxRecoveryObjectiveBytes)
	assert.Equal(t, exact, BoundRecoveryText(exact, MaxRecoveryObjectiveBytes))
	// Over-bound by 1.
	over := strings.Repeat("a", MaxRecoveryObjectiveBytes+1)
	got := BoundRecoveryText(over, MaxRecoveryObjectiveBytes)
	assert.Equal(t, MaxRecoveryObjectiveBytes, len(got))
	// Max param 512 must behave as truncation limit.
	assert.Equal(t, 512, MaxRecoveryReasonBytes)
	assert.Equal(t, 512, MaxRecoveryObjectiveBytes)
	long512 := strings.Repeat("x", 600)
	tr512 := BoundRecoveryText(long512, 512)
	assert.Equal(t, 512, len(tr512))
	// UTF-8 mixed truncation edge: cut point lands inside rune.
	mixed := strings.Repeat("a", 510) + "世" // 510 + 3 = 513
	trMixed := BoundRecoveryText(mixed, 512)
	require.LessOrEqual(t, len(trMixed), 512)
	require.True(t, utf8.ValidString(trMixed), "mixed truncation must remain valid UTF-8")
	assert.NotContains(t, trMixed, "世", "rune that would exceed bound must be dropped entirely, not split")
}

func TestRecoveryInstruction_IsInternalControlContent(t *testing.T) {
	t.Parallel()
	// Absence from A-side transcript is a runtime guarantee; here we assert the
	// builder output is internal control content only: it starts with the
	// automation tag and never a user-role preamble.
	in := RecoveryInput{
		Reason:             "internal check",
		RemainingObjective: "resume work",
		Attempt:            1,
		MaxAttempts:        3,
		WorkComplete:       false,
		NeedsUser:          false,
	}
	out := BuildRecoveryInstruction(in)
	trimmed := strings.TrimSpace(out)
	require.True(t, strings.HasPrefix(trimmed, "<automated-recovery>"), "builder output must start with automation tag, never a user-role preamble; runtime guarantees it is absent from A-side user-authored transcript/output")
	assert.NotContains(t, strings.ToLower(trimmed), "role: user", "must not contain user-role preamble")
	assert.Contains(t, out, "</automated-recovery>")
}

func TestRecoveryInstruction_BoundedFactsConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 512, MaxRecoveryReasonBytes, "MaxRecoveryReasonBytes must be 512")
	assert.Equal(t, 512, MaxRecoveryObjectiveBytes, "MaxRecoveryObjectiveBytes must be 512")
	reason := strings.Repeat("€", 200) // 600 bytes
	bounded := BoundRecoveryText(reason, MaxRecoveryReasonBytes)
	assert.LessOrEqual(t, len(bounded), 512)
	assert.True(t, utf8.ValidString(bounded))
}
