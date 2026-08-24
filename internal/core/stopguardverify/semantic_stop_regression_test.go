package stopguardverify_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguardverify"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/semantic_stop_fixtures.json
var semanticStopFixturesJSON []byte

type trajectoryItemJSON struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	CallID string `json:"call_id"`
	Status string `json:"status"`
	Args   string `json:"args,omitempty"`
	Output string `json:"output,omitempty"`
}

type expectedVerdictJSON struct {
	Kind               string `json:"kind"`
	Reason             string `json:"reason"`
	RemainingObjective string `json:"remaining_objective"`
}

type semanticStopFixtureJSON struct {
	ID                     string               `json:"id"`
	Description            string               `json:"description"`
	Category               string               `json:"category"`
	UserObjective          []string             `json:"user_objective"`
	CandidateAssistantText string               `json:"candidate_assistant_text"`
	CandidateReasoningText string               `json:"candidate_reasoning_text,omitempty"`
	CandidateHasRefusal    bool                 `json:"candidate_has_refusal,omitempty"`
	RecentTrajectory       []trajectoryItemJSON `json:"recent_trajectory"`
	Cause                  string               `json:"cause"`
	OutputCommitted        bool                 `json:"output_committed"`
	ExplicitCompletion     bool                 `json:"explicit_completion"`
	VerifierRawResponse    string               `json:"verifier_raw_response"`
	SimulatedVerifierError string               `json:"simulated_verifier_error,omitempty"`
	ExpectedVerdict        expectedVerdictJSON  `json:"expected_verdict"`
	ExpectedNormalizedKind string               `json:"expected_normalized_kind"`
	ExpectedGateAction     string               `json:"expected_gate_action"`
}

func buildEvidenceFromFixture(fix semanticStopFixtureJSON) stopguard.Evidence {
	var userMsgs []lipapi.Message
	for _, uo := range fix.UserObjective {
		userMsgs = append(userMsgs, lipapi.Message{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(uo)},
		})
	}

	var candidateItems []lipapi.Item
	if fix.CandidateHasRefusal {
		candidateItems = append(candidateItems, lipapi.Item{
			Kind:   lipapi.ItemKindMessage,
			Role:   lipapi.RoleAssistant,
			Status: lipapi.ItemStatusCompleted,
			Content: []lipapi.ContentPart{
				{Kind: lipapi.ContentPartRefusal, Refusal: fix.CandidateAssistantText},
			},
		})
	} else if fix.CandidateReasoningText != "" && fix.CandidateAssistantText == "" {
		candidateItems = append(candidateItems, lipapi.Item{
			Kind:   lipapi.ItemKindMessage,
			Role:   lipapi.RoleAssistant,
			Status: lipapi.ItemStatusCompleted,
			Content: []lipapi.ContentPart{
				{
					Kind: lipapi.ContentPartReasoning,
					Reasoning: &lipapi.ReasoningPart{
						Dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
						Text:    fix.CandidateReasoningText,
					},
				},
			},
		})
	} else if fix.CandidateAssistantText != "" {
		var parts []lipapi.ContentPart
		if fix.CandidateReasoningText != "" {
			parts = append(parts, lipapi.ContentPart{
				Kind: lipapi.ContentPartReasoning,
				Reasoning: &lipapi.ReasoningPart{
					Dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
					Text:    fix.CandidateReasoningText,
				},
			})
		}
		parts = append(parts, lipapi.ContentPart{
			Kind: lipapi.ContentPartText,
			Text: fix.CandidateAssistantText,
		})
		candidateItems = append(candidateItems, lipapi.Item{
			Kind:    lipapi.ItemKindMessage,
			Role:    lipapi.RoleAssistant,
			Status:  lipapi.ItemStatusCompleted,
			Content: parts,
		})
	}

	var trajItems []lipapi.Item
	completedTools := 0
	for _, tj := range fix.RecentTrajectory {
		switch tj.Kind {
		case "tool_call":
			trajItems = append(trajItems, lipapi.Item{
				Kind:   lipapi.ItemKindToolCall,
				Status: lipapi.ItemStatus(tj.Status),
				ToolCall: &lipapi.ToolCallItem{
					CallID:    tj.CallID,
					Name:      tj.Name,
					Arguments: json.RawMessage(tj.Args),
				},
			})
		case "tool_result":
			completedTools++
			trajItems = append(trajItems, lipapi.Item{
				Kind:   lipapi.ItemKindToolResult,
				Status: lipapi.ItemStatus(tj.Status),
				ToolResult: &lipapi.ToolResultItem{
					CallID: tj.CallID,
					Name:   tj.Name,
					Output: tj.Output,
				},
			})
		}
	}

	return stopguard.Evidence{
		Cause:               stopguard.Cause(fix.Cause),
		UserObjective:       userMsgs,
		CandidateAssistant:  candidateItems,
		RecentTrajectory:    trajItems,
		ToolState:           stopguard.ToolCompletionState{CompletedToolResults: completedTools},
		OutputCommitted:     fix.OutputCommitted,
		ExplicitCompletion:  fix.ExplicitCompletion,
		ContinuationLineage: stopguard.ContinuationRef{ContinuationID: "cont-regression-test"},
		RecoveryAttempt:     1,
		ParentTraceID:       "trace-reg-1",
		ParentALegID:        "a-reg-1",
		ParentBLegID:        "b-reg-1",
		ParentBranchBinding: "branch-reg-1",
	}
}

// TestSemanticStopRegression_JSONFixtures proves all table-driven fixtures from
// testdata/semantic_stop_fixtures.json satisfy prompt instruction requirements,
// evidence bounding, strict structured parsing, conservative normalization, and gate decisions.
// (Requirements 5.1-5.7, 6.1-6.7, 7.1-7.6, 8.1-8.6, 12.1-12.9)
func TestSemanticStopRegression_JSONFixtures(t *testing.T) {
	t.Parallel()

	var fixtures []semanticStopFixtureJSON
	err := json.Unmarshal(semanticStopFixturesJSON, &fixtures)
	require.NoError(t, err, "must parse semantic_stop_fixtures.json")
	require.NotEmpty(t, fixtures, "must have fixtures in dataset")

	for _, fix := range fixtures {
		t.Run(fix.ID, func(t *testing.T) {
			t.Parallel()

			ev := buildEvidenceFromFixture(fix)

			// 1. Evidence Projection validation
			evidenceBlock := stopguardverify.ProjectEvidence(ev)
			require.NotEmpty(t, evidenceBlock)
			assert.LessOrEqual(t, len(evidenceBlock), stopguardverify.MaxEvidenceBytes)
			assert.Contains(t, evidenceBlock, "Cause: "+fix.Cause)
			assert.Contains(t, evidenceBlock, "UserObjective:")
			assert.Contains(t, evidenceBlock, "CandidateAssistant:")
			assert.Contains(t, evidenceBlock, "RecentTrajectory:")

			// 2. Verifier Instruction Prompt Contract validation
			instr := stopguardverify.BuildInstruction(evidenceBlock)
			assert.Contains(t, instr, "1. Decide whether the already requested work is complete")
			assert.Contains(t, instr, "2. Return CONTINUE only if you can name concrete unfinished work")
			assert.Contains(t, instr, "3. Do not count")
			assert.Contains(t, instr, "4. If the next step requires any user answer, approval, permission, or choice, return NEEDS_USER")
			assert.Contains(t, instr, "5. If evidence is insufficient")
			assert.Contains(t, instr, "6. Output one structured verdict")
			assert.Contains(t, instr, "Completed answers")
			assert.Contains(t, instr, "I can also")
			assert.Contains(t, instr, "Next steps")
			assert.Contains(t, instr, "Direct questions")
			assert.Contains(t, instr, "Quoted future-action language")
			assert.Contains(t, instr, "First-person immediate in-scope commitments")

			// 3. Gate Policy decision before verifier
			candidate := stopguard.Candidate{
				Cause:              stopguard.Cause(fix.Cause),
				OutputCommitted:    fix.OutputCommitted,
				ExplicitCompletion: fix.ExplicitCompletion,
			}
			policy := stopguard.PolicyTrust
			if fix.ID == "explicit_completion_verify_policy_evidence" {
				policy = stopguard.PolicyVerify
			}

			decision := stopguard.Decide(candidate, policy)

			if decision.Verify {
				var fake fakeAuxClient
				switch fix.SimulatedVerifierError {
				case "transport_error":
					fake.err = errors.New("simulated transport failure")
				case "timeout":
					fake.err = context.DeadlineExceeded
				default:
					if fix.VerifierRawResponse != "" {
						fake.collected = collectedWithText(fix.VerifierRawResponse)
					}
				}

				cfg := stopguardverify.AdapterConfig{
					Role:    "loop_guard",
					Timeout: 4 * time.Second,
				}
				adapter := stopguardverify.NewAdapter(&fake, cfg)

				// 4. Verification and normalization
				verdict, verifyErr := adapter.Verify(context.Background(), ev)
				normalized := stopguard.NormalizeVerdict(verdict, verifyErr)

				assert.Equal(t, stopguard.VerdictKind(fix.ExpectedNormalizedKind), normalized.Kind,
					"normalized verdict kind mismatch for fixture %s", fix.ID)

				action := stopguard.DecideWithVerdict(candidate, normalized, verifyErr)
				assert.Equal(t, stopguard.Action(fix.ExpectedGateAction), action,
					"gate action mismatch after verification for fixture %s", fix.ID)

				// 5. Category-specific assertions
				switch fix.Category {
				case "positive_unfinished":
					assert.Equal(t, stopguard.VerdictContinue, normalized.Kind,
						"positive unfinished fixture must normalize to CONTINUE")
					assert.NotEmpty(t, normalized.RemainingObjective,
						"positive unfinished fixture must have non-empty remaining_objective")
					assert.Equal(t, stopguard.ActionContinueLeg, action)
				case "critical_negative":
					assert.NotEqual(t, stopguard.VerdictContinue, normalized.Kind,
						"critical negative fixture must never normalize to CONTINUE (false continuation prevention)")
					if fix.ExpectedNormalizedKind == "needs_user" {
						assert.Equal(t, stopguard.VerdictNeedsUser, normalized.Kind)
					}
					assert.Equal(t, stopguard.ActionForwardTerminal, action)
				case "verifier_error":
					assert.Equal(t, stopguard.VerdictUncertain, normalized.Kind,
						"verifier error/timeout must normalize conservatively to UNCERTAIN")
					assert.Equal(t, stopguard.ActionForwardTerminal, action)
				}
			} else {
				// Non-verified candidate (refusal, client cancel, trusted explicit completion, etc.)
				assert.Equal(t, stopguard.Action(fix.ExpectedGateAction), decision.Action,
					"gate action mismatch for non-verified fixture %s", fix.ID)
				assert.False(t, decision.Verify, "fixture %s expected verifier bypass", fix.ID)
			}
		})
	}
}

// TestPositiveUnfinished_ImmediatePromisedAction_TrajectoryEvidence proves that
// when trajectory already contains evidence of the promised action executing,
// verifier conclusion of complete work (ALLOW_STOP) is respected and not continued.
func TestPositiveUnfinished_ImmediatePromisedAction_TrajectoryEvidence(t *testing.T) {
	t.Parallel()

	ev := stopguard.Evidence{
		Cause: stopguard.CauseNormalEnd,
		UserObjective: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("Run tests and verify.")}},
		},
		CandidateAssistant: []lipapi.Item{
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleAssistant,
				Status:  lipapi.ItemStatusCompleted,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "I ran the test suite as requested. All 14 tests pass."}},
			},
		},
		RecentTrajectory: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindToolCall,
				Status: lipapi.ItemStatusCompleted,
				ToolCall: &lipapi.ToolCallItem{
					CallID:    "call_test_1",
					Name:      "run_tests",
					Arguments: []byte(`{}`),
				},
			},
			{
				Kind:   lipapi.ItemKindToolResult,
				Status: lipapi.ItemStatusCompleted,
				ToolResult: &lipapi.ToolResultItem{
					CallID: "call_test_1",
					Name:   "run_tests",
					Output: "PASS",
				},
			},
		},
		ToolState:       stopguard.ToolCompletionState{CompletedToolResults: 1},
		OutputCommitted: true,
	}

	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"allow_stop","reason":"test run evidenced in trajectory and all tests pass"}`)}
	adapter := stopguardverify.NewAdapter(fake, stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second})

	verdict, err := adapter.Verify(context.Background(), ev)
	require.NoError(t, err)
	normalized := stopguard.NormalizeVerdict(verdict, err)
	assert.Equal(t, stopguard.VerdictAllowStop, normalized.Kind)

	candidate := stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true}
	assert.Equal(t, stopguard.ActionForwardTerminal, stopguard.DecideWithVerdict(candidate, normalized, nil))
}

// TestPositiveUnfinished_ReasoningOnlyEvidence_ContinuesLeg proves that
// reasoning/thinking-only evidence without completed assistant answer allows
// verifier CONTINUE to open continuation leg.
func TestPositiveUnfinished_ReasoningOnlyEvidence_ContinuesLeg(t *testing.T) {
	t.Parallel()

	ev := stopguard.Evidence{
		Cause: stopguard.CauseNormalEnd,
		UserObjective: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("Solve the riddle.")}},
		},
		CandidateAssistant: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindMessage,
				Role:   lipapi.RoleAssistant,
				Status: lipapi.ItemStatusCompleted,
				Content: []lipapi.ContentPart{
					{
						Kind: lipapi.ContentPartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
							Text:    "Thinking through the clues... clue 1 means X, clue 2 means Y...",
						},
					},
				},
			},
		},
		OutputCommitted: true,
	}

	fake := &fakeAuxClient{collected: collectedWithText(`{"kind":"continue","reason":"only internal thinking was emitted; answer not delivered","remaining_objective":"deliver solution to riddle"}`)}
	adapter := stopguardverify.NewAdapter(fake, stopguardverify.AdapterConfig{Role: "loop_guard", Timeout: 4 * time.Second})

	verdict, err := adapter.Verify(context.Background(), ev)
	require.NoError(t, err)
	normalized := stopguard.NormalizeVerdict(verdict, err)
	assert.Equal(t, stopguard.VerdictContinue, normalized.Kind)
	assert.Equal(t, "deliver solution to riddle", normalized.RemainingObjective)

	candidate := stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true}
	assert.Equal(t, stopguard.ActionContinueLeg, stopguard.DecideWithVerdict(candidate, normalized, nil))
}
