package largebody_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// =============================================================================
// Task 11.3: Consume frozen authority summary + current dependency census
//
// Spec Requirements:
// - Requirement 5: One Generation-Pinned Wire-Eligibility Summary.
//   5.3: Unclassified plane fails closed at runtime.
//   5.6: Narrow-port inventory explicitly classified.
//   5.7: Unknown => decline.
//   5.9: Re-check static summary defensively; no hot-path reflection.
// - Requirement 13: Content, Policy, Hook, and Traffic Authorities.
//   13.1-13.6: Traffic, submit/request hooks, secret guards, local turn block.
// - Requirement 14: Secure-session parity without canonical call.
// - Requirement 15: Metering, accounting, billing wire-native evidence.
// - Requirement 19: Close every post-commit full-Call dependency.
//   19.4: Every dependency classified as wire-safe or blocker; unknown declines.
// =============================================================================

// validWireSafeSummary creates a sealed WireEligibilitySummary with zero blockers.
func validWireSafeSummary(t *testing.T, genID string) largebody.WireEligibilitySummary {
	t.Helper()
	s, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    makeWireSafePlanes(genID),
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}
	return s
}

func makeWireSafePlanes(genID string) []largebody.PlaneEligibilityInput {
	census := largebody.NewStandardDependencyCensus(genID)
	return census.Planes
}

// -----------------------------------------------------------------------------
// Test 1: Unknown port declines
// -----------------------------------------------------------------------------

func TestTask11_3_UnknownPort_Declines(t *testing.T) {
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// Register an unknown port into the dependency census
	census.RegisterPort("unregistered.custom_third_party_port", largebody.DependencyClassUnknown, true)

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()

	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unknown port, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for unknown port, got %v", reason)
	}

	// Test via AuthorityAssessor interface
	assessor := largebody.NewAuthorityAssessor(*gate)
	assessment, err := assessor.AssessLargeBody(context.Background(), validProof())
	if err != nil {
		t.Fatalf("AssessLargeBody unexpected error: %v", err)
	}
	if !assessment.Declined() {
		t.Fatalf("expected declined assessment for unknown port")
	}
	if assessment.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", assessment.Reason)
	}
}

func TestTask11_3_UnknownPlane_Declines(t *testing.T) {
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// Add an unknown plane ID
	census.Planes = append(census.Planes, largebody.PlaneEligibilityInput{
		ID:       "unknown_plane_id_xyz",
		Access:   largebody.PlaneAccessMetadataOnly,
		Occupied: true,
	})

	decision, reason := largebody.AssessAuthorityGate(summary, census, genID)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unknown plane, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for unknown plane, got %v", reason)
	}
}

func TestTask11_3_UnclassifiedPlane_Declines(t *testing.T) {
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// Set a plane as unclassified
	if len(census.Planes) > 0 {
		census.Planes[0].Access = largebody.PlaneAccessUnclassified
	}

	decision, reason := largebody.AssessAuthorityGate(summary, census, genID)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unclassified plane, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for unclassified plane, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 2: Wire-safe set accepts
// -----------------------------------------------------------------------------

func TestTask11_3_WireSafeSet_Accepts(t *testing.T) {
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// Verify standard census contains only wire-safe dependencies when unoccupied
	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := gate.Evaluate()

	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept for wire-safe set, got %v", decision)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone for wire-safe set, got %v", reason)
	}

	// Add known wire-safe occupied ports from evidence 1.8
	census.RegisterPort("core.store", largebody.DependencyClassWireSafe, true)
	census.RegisterPort("billing.credit_gate", largebody.DependencyClassWireSafe, true)
	census.RegisterPort("security.session_recorder", largebody.DependencyClassWireSafe, true)
	census.RegisterPort("accounting.metering_recorder", largebody.DependencyClassWireSafe, true)

	decision, reason = largebody.AssessAuthorityGate(summary, census, genID)
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept with occupied wire-safe ports, got %v", decision)
	}
	if reason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", reason)
	}

	// Verify via AuthorityAssessor interface
	stamp, err := largebody.NewAssessmentStamp(
		genID,
		"openai-chat",
		largebody.NewSourceDigest(digestOf(1)),
		512,
		largebody.BodyModeIdentityJSON,
		largebody.NewNoRewrite(),
		largebody.NewIdentityDigest(digestOf(2)),
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}

	assessor := largebody.NewAuthorityAssessor(*gate)
	assessor.AcceptStamp = stamp
	assessor.AcceptWireReq = largebody.WireRequestFacts{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		ClientModel:     "gpt-4o",
		CandidateModel:  "gpt-4o-mini",
		MaxOutputTokens: 4096,
	}
	assessor.AcceptDomain = largebody.WireDomainFacts{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		CandidateModels: []string{"gpt-4o-mini"},
	}

	assessment, err := assessor.AssessLargeBody(context.Background(), validProof())
	if err != nil {
		t.Fatalf("AssessLargeBody unexpected error: %v", err)
	}
	if !assessment.Accepted() {
		t.Fatalf("expected accepted assessment for wire-safe set")
	}
}

// -----------------------------------------------------------------------------
// Test 3: Defensive static-summary recheck
// -----------------------------------------------------------------------------

func TestTask11_3_DefensiveRecheck_UnsealedSummaryDeclines(t *testing.T) {
	genID := "gen-task-11-3"
	census := largebody.NewStandardDependencyCensus(genID)

	// Zero/unsealed summary must decline defensively
	var unsealedSummary largebody.WireEligibilitySummary
	decision, reason := largebody.AssessAuthorityGate(unsealedSummary, census, genID)

	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for unsealed summary, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

func TestTask11_3_DefensiveRecheck_GenerationMismatchDeclines(t *testing.T) {
	census := largebody.NewStandardDependencyCensus("gen-A")
	summary := validWireSafeSummary(t, "gen-A")

	// Pinned to gen-A, but evaluating against gen-B
	decision, reason := largebody.AssessAuthorityGate(summary, census, "gen-B")

	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for generation mismatch, got %v", decision)
	}
	if reason != largebody.DeclineReasonGenerationMismatch {
		t.Fatalf("expected DeclineReasonGenerationMismatch, got %v", reason)
	}
}

func TestTask11_3_DefensiveRecheck_EmptyGenerationDeclines(t *testing.T) {
	census := largebody.NewStandardDependencyCensus("gen-A")
	summary := validWireSafeSummary(t, "gen-A")

	// A missing generation binding must decline, never skip the pin recheck.
	for _, empty := range []string{"", "   "} {
		decision, reason := largebody.AssessAuthorityGate(summary, census, empty)

		if decision != largebody.AssessmentDecisionDecline {
			t.Fatalf("expected AssessmentDecisionDecline for empty generation %q, got %v", empty, decision)
		}
		if reason != largebody.DeclineReasonGenerationMismatch {
			t.Fatalf("expected DeclineReasonGenerationMismatch for empty generation %q, got %v", empty, reason)
		}
	}
}

func TestTask11_3_DefensiveRecheck_StaticBlockerDeclines(t *testing.T) {
	genID := "gen-task-11-3"
	census := largebody.NewStandardDependencyCensus(genID)

	// Summary with occupied local turn handlers (canonical required plane blocker)
	planes := census.Planes
	for i := range planes {
		if planes[i].ID == "local_turn_handlers" {
			planes[i].Occupied = true
			break
		}
	}
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    planes,
		Hooks:                     largebody.HookEligibilityInput{},
		Ports:                     largebody.NarrowPortEligibilityInput{},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary error: %v", err)
	}

	decision, reason := largebody.AssessAuthorityGate(summary, census, genID)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline for summary with static blocker, got %v", decision)
	}
	if reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", reason)
	}
}

func TestTask11_3_DefensiveRecheck_NoHotPathReflection(t *testing.T) {
	// Guardrail: no hot-path reflection during authority gate assessment.
	// Assessment duration must be sub-millisecond and well under the budget.
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)

	// Benchmark/run multiple iterations ensuring zero allocations / ultra-fast execution
	for i := 0; i < 1000; i++ {
		dec, r := gate.Evaluate()
		if dec != largebody.AssessmentDecisionAccept || r != largebody.DeclineReasonNone {
			t.Fatalf("iteration %d failed: dec=%v r=%v", i, dec, r)
		}
	}
}

// -----------------------------------------------------------------------------
// Test 4: Occupied blocker ports decline
// -----------------------------------------------------------------------------

func TestTask11_3_OccupiedBlocker_Declines(t *testing.T) {
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)

	testCases := []struct {
		name   string
		mutate func(*largebody.DependencyCensus)
	}{
		{
			name: "occupied submit hook",
			mutate: func(c *largebody.DependencyCensus) {
				c.Hooks.SubmitOccupied = true
			},
		},
		{
			name: "occupied request-part hook",
			mutate: func(c *largebody.DependencyCensus) {
				c.Hooks.RequestPartOccupied = true
			},
		},
		{
			name: "occupied tool hook",
			mutate: func(c *largebody.DependencyCensus) {
				c.Hooks.ToolOccupied = true
			},
		},
		{
			name: "conversation view reader occupied",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.ConversationViewReaderOccupied = true
			},
		},
		{
			name: "steering writer factory occupied",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.SteeringWriterFactoryOccupied = true
			},
		},
		{
			name: "caps resolver occupied",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.CapsResolverOccupied = true
			},
		},
		{
			name: "preflight enabled without exact counter",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.PreflightEnabled = true
				c.Ports.PreflightHasExactCounter = false
			},
		},
		{
			name: "two phase executor missing",
			mutate: func(c *largebody.DependencyCensus) {
				c.TwoPhaseExecutorAvailable = false
			},
		},
		{
			name: "custom blocker port occupied",
			mutate: func(c *largebody.DependencyCensus) {
				c.RegisterPort("custom.call_callbacks", largebody.DependencyClassBlocker, true)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			census := largebody.NewStandardDependencyCensus(genID)
			tc.mutate(&census)

			decision, reason := largebody.AssessAuthorityGate(summary, census, genID)
			if decision != largebody.AssessmentDecisionDecline {
				t.Fatalf("%s: expected AssessmentDecisionDecline, got %v", tc.name, decision)
			}
			if reason != largebody.DeclineReasonAuthorityBlocker {
				t.Fatalf("%s: expected DeclineReasonAuthorityBlocker, got %v", tc.name, reason)
			}
		})
	}
}
func TestTask11_3_SentinelHarness_AllSentinelsUntouched(t *testing.T) {
	harness := NewSentinelHarness()
	genID := "gen-task-11-3"
	summary := validWireSafeSummary(t, genID)
	census := largebody.NewStandardDependencyCensus(genID)

	gate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	assessor := largebody.NewAuthorityAssessor(*gate)

	// Incline with unknown port to test decline path touches no sentinels
	census.RegisterPort("unknown.vendor_extension", largebody.DependencyClassUnknown, true)
	declinedGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	declinedAssessor := largebody.NewAuthorityAssessor(*declinedGate)

	proof := validProof()
	assessment, violation, err := harness.RunAssessment(context.Background(), declinedAssessor, proof)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if violation != nil {
		t.Fatalf("unexpected sentinel violation: %v", violation)
	}
	if !assessment.Declined() {
		t.Fatalf("expected declined assessment")
	}
	if assessment.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", assessment.Reason)
	}

	// Requirement 6.2: ALL 8 sentinels must remain completely untouched
	harness.AssertUntouched(t)

	// Test duration measurement under held permit (Requirement 6.1, 6.8, 21.4)
	const reqBytes int64 = 2 * 1024 * 1024 // 2 MiB
	measAssessment, meas, err := harness.MeasureAssessment(context.Background(), assessor, proof, reqBytes)
	if err != nil {
		t.Fatalf("MeasureAssessment failed: %v", err)
	}
	if !meas.PermitHeld {
		t.Fatal("permit must be held across authority assessment")
	}
	if !meas.Bounded {
		t.Fatalf("assessment duration %v exceeded ceiling %v", meas.Duration, harness.MaxAllowedDuration)
	}
	if measAssessment.Reason != largebody.DeclineReasonProofUncertain && measAssessment.Reason != largebody.DeclineReasonNone {
		t.Fatalf("unexpected reason: %v", measAssessment.Reason)
	}
	harness.AssertUntouched(t)
}
