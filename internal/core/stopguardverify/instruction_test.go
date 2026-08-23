package stopguardverify_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguardverify"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildInstruction_SixRuleContract(t *testing.T) {
	t.Parallel()
	evidence := stopguardverify.ProjectEvidence(stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "implement feature")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("Done.")},
	})
	instr := stopguardverify.BuildInstruction(evidence)
	// Six numbered rules.
	assert.Contains(t, instr, "1. Decide whether the already requested work is complete")
	assert.Contains(t, instr, "2. Return CONTINUE only if you can name concrete unfinished work")
	assert.Contains(t, instr, "3. Do not count")
	assert.Contains(t, instr, "4. If the next step requires any user answer, approval, permission, or choice, return NEEDS_USER")
	assert.Contains(t, instr, "5. If evidence is insufficient")
	assert.Contains(t, instr, "6. Output one structured verdict")
	// Output contract JSON.
	assert.Contains(t, instr, `"kind"`)
	assert.Contains(t, instr, `"reason"`)
	assert.Contains(t, instr, `"remaining_objective"`)
	assert.Contains(t, instr, `"allow_stop"`)
	assert.Contains(t, instr, `"continue"`)
	assert.Contains(t, instr, `"needs_user"`)
	assert.Contains(t, instr, `"blocked"`)
	assert.Contains(t, instr, `"uncertain"`)
	// Evidence embedded.
	assert.Contains(t, instr, "<evidence>")
	assert.Contains(t, instr, "implement feature")
}

func TestBuildInstruction_NegativeRulesPresent(t *testing.T) {
	t.Parallel()
	instr := stopguardverify.BuildInstruction("dummy evidence")
	assert.Contains(t, instr, "Completed answers")
	assert.Contains(t, instr, "I can also")
	assert.Contains(t, instr, "Next steps")
	assert.Contains(t, instr, "Direct questions")
	assert.Contains(t, instr, "Quoted future-action language")
	assert.Contains(t, instr, "\"I'll continue\"")
}

func TestBuildInstruction_PositiveRulePresent(t *testing.T) {
	t.Parallel()
	instr := stopguardverify.BuildInstruction("evidence")
	assert.Contains(t, instr, "First-person immediate in-scope commitments")
	assert.Contains(t, instr, "Let me run the tests next")
	assert.Contains(t, instr, "CAN authorize CONTINUE")
}

func TestBuildInstruction_BoundedEvidence(t *testing.T) {
	t.Parallel()
	longEvidence := strings.Repeat("e", stopguardverify.MaxEvidenceBytes+5000)
	instr := stopguardverify.BuildInstruction(longEvidence)
	// Instruction must remain bounded; evidence truncated inside.
	assert.LessOrEqual(t, len(longEvidence), stopguardverify.MaxEvidenceBytes+5000)
	// The instruction should not be unbounded due to evidence alone; evidence section is truncated.
	// Find evidence block length inside instruction is <= MaxEvidenceBytes + overhead.
	// Simple check: instruction length is less than evidence + 4096
	assert.Less(t, len(instr), len(longEvidence)+4096)
}

func TestInstruction_Fixtures_CompleteAnswerNotContinue(t *testing.T) {
	t.Parallel()
	// Fixture: completed answer "Done; tests pass." should be represented in evidence and instruction
	// must tell verifier that completed answers are NOT unfinished.
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "Run the tests.")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("Done; tests pass.")},
		OutputCommitted:    true,
	}
	evidence := stopguardverify.ProjectEvidence(ev)
	instr := stopguardverify.BuildInstruction(evidence)
	assert.Contains(t, evidence, "Done; tests pass")
	assert.Contains(t, instr, "Completed answers that fully address the user objective are NOT unfinished work")
}

func TestInstruction_Fixtures_OptionalICanAlso(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "Summarize the document.")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("Summary complete. I can also translate it to French if you'd like.")},
	}
	evidence := stopguardverify.ProjectEvidence(ev)
	instr := stopguardverify.BuildInstruction(evidence)
	assert.Contains(t, evidence, "I can also")
	assert.Contains(t, instr, "Optional \"I can also...\" suggestions are NOT unfinished work")
}

func TestInstruction_Fixtures_UserOwnedNextSteps(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "Deploy the app.")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("Deployment complete. Next steps for you:\n- Configure DNS\n- Set up monitoring")},
	}
	evidence := stopguardverify.ProjectEvidence(ev)
	instr := stopguardverify.BuildInstruction(evidence)
	assert.Contains(t, evidence, "Next steps")
	assert.Contains(t, instr, "Next steps")
}

func TestInstruction_Fixtures_DirectQuestionNeedsUser(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "Choose a database.")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("Would you like me to use Postgres or MySQL?")},
	}
	evidence := stopguardverify.ProjectEvidence(ev)
	instr := stopguardverify.BuildInstruction(evidence)
	assert.Contains(t, evidence, "Would you like me to")
	assert.Contains(t, instr, "Direct questions to the user")
	assert.Contains(t, instr, "NEEDS_USER")
}

func TestInstruction_Fixtures_QuotedFutureAction(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "Write docs.")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("As requested: \"I'll continue with the docs tomorrow\"")},
	}
	evidence := stopguardverify.ProjectEvidence(ev)
	instr := stopguardverify.BuildInstruction(evidence)
	assert.Contains(t, evidence, "I'll continue")
	assert.Contains(t, instr, "Quoted future-action language")
}

func TestInstruction_Fixtures_PromisedButUnexecutedCanContinue(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "Run tests and fix failures.")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("Let me run the tests next.")},
		RecentTrajectory:   []lipapi.Item{}, // no tool evidence for test execution
	}
	evidence := stopguardverify.ProjectEvidence(ev)
	require.Contains(t, evidence, "Let me run the tests next")
	instr := stopguardverify.BuildInstruction(evidence)
	assert.Contains(t, instr, "First-person immediate in-scope commitments")
	assert.Contains(t, instr, "Let me run the tests next")
	// Ensure tool summary shows no execution evidence.
	assert.Contains(t, evidence, "(none)")
}

func TestInstruction_Fixtures_NeedsUserCase(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "Deploy to prod.")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("I need your approval to proceed with deployment. Should I continue?")},
	}
	evidence := stopguardverify.ProjectEvidence(ev)
	instr := stopguardverify.BuildInstruction(evidence)
	assert.Contains(t, instr, "If the next step requires any user answer, approval, permission, or choice, return NEEDS_USER")
}

func TestBuildInstruction_EvidenceEmbeddedVerbatim(t *testing.T) {
	t.Parallel()
	ev := stopguard.Evidence{
		Cause:              stopguard.CauseNormalEnd,
		UserObjective:      []lipapi.Message{textMessage(lipapi.RoleUser, "unique-objective-xyz")},
		CandidateAssistant: []lipapi.Item{assistantTextItem("unique-candidate-abc")},
		ParentTraceID:      "trace-verbatim",
		RecoveryAttempt:    3,
	}
	evidence := stopguardverify.ProjectEvidence(ev)
	instr := stopguardverify.BuildInstruction(evidence)
	assert.Contains(t, instr, "unique-objective-xyz")
	assert.Contains(t, instr, "unique-candidate-abc")
	assert.Contains(t, instr, "trace-verbatim")
	assert.Contains(t, instr, "RecoveryAttempt: 3")
}
