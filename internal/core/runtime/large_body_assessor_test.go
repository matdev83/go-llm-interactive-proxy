package runtime_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

type testWireBackend struct {
	reqSupport    largebody.WireRequestSupport
	domainSupport largebody.WireDomainSupport
}

func (b testWireBackend) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	return b.reqSupport
}

func (b testWireBackend) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	return b.domainSupport
}

func newTestEligibleAssessor(t *testing.T, genID string, anyAcceptedModel bool) *coreruntime.ProductionLargeBodyAssessor {
	t.Helper()

	census := largebody.NewStandardDependencyCensus(genID)
	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)

	wb := testWireBackend{
		reqSupport: largebody.WireRequestSupport{
			Compatible: true,
			Reason:     largebody.WireSupportReasonNone,
		},
		domainSupport: largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: anyAcceptedModel,
			Reason:           largebody.WireSupportReasonNone,
		},
	}
	resolver := largebody.WireBackendMap{"test-backend": wb}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"test-backend",
		map[string]struct{}{"test-backend": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"test-backend",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		resolver,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(nil, validator, resolver)
	wireGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, resolver)

	return coreruntime.NewProductionLargeBodyAssessor(
		genID,
		genID,
		authGate,
		wireGate,
		coreruntime.StandardLaneDomainPolicies(),
	)
}

func validTestProof(profileID string, op lipapi.Operation, delivery lipapi.DeliveryMode) largebody.Proof {
	return largebody.Proof{
		ProfileID:       profileID,
		Operation:       op,
		Delivery:        delivery,
		RouteSelector:   "gpt-4o",
		ClientModel:     "gpt-4o",
		MaxOutputTokens: 4096,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "std-req-v1",
			ControlCount:   1,
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
		Identity:  largebody.NewIdentityDigest([32]byte{1, 2, 3}),
		Source:    largebody.NewSourceDigest([32]byte{4, 5, 6}),
		BodyBytes: 1024,
	}
}

func TestProductionLargeBodyAssessor_ContextCanceled(t *testing.T) {
	t.Parallel()

	assessor := newTestEligibleAssessor(t, "gen-1", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proof := validTestProof("openai_chat_v1", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeStreaming)

	assessment, err := assessor.AssessLargeBody(ctx, proof)
	if err != nil {
		t.Fatalf("same-permit decline discipline: err must be nil, got %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("canceled context must decline, got %v", assessment.Decision)
	}
	if assessment.Reason != largebody.DeclineReasonCanceled {
		t.Fatalf("expected DeclineReasonCanceled, got %v", assessment.Reason)
	}
}

func TestProductionLargeBodyAssessor_EmptyProfile(t *testing.T) {
	t.Parallel()

	assessor := newTestEligibleAssessor(t, "gen-1", true)
	proof := validTestProof("", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeStreaming)

	assessment, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("same-permit decline discipline: err must be nil, got %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("empty profile must decline, got %v", assessment.Decision)
	}
	if assessment.Reason != largebody.DeclineReasonProofUncertain {
		t.Fatalf("expected DeclineReasonProofUncertain, got %v", assessment.Reason)
	}
}

func TestProductionLargeBodyAssessor_StreamingOnlyDelivery(t *testing.T) {
	t.Parallel()

	assessor := newTestEligibleAssessor(t, "gen-1", true)
	proof := validTestProof("openai_chat_v1", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeNonStreaming)

	assessment, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("same-permit decline discipline: err must be nil, got %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("non-streaming delivery must decline, got %v", assessment.Decision)
	}
	if assessment.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker for non-streaming delivery, got %v", assessment.Reason)
	}
}

func TestProductionLargeBodyAssessor_AuthorityBlocker(t *testing.T) {
	t.Parallel()

	genID := "gen-blocker"
	census := largebody.NewStandardDependencyCensus(genID)
	// Add an occupied submit hook blocker
	census.Hooks.SubmitOccupied = true

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	assessor := coreruntime.NewProductionLargeBodyAssessor(
		genID,
		genID,
		authGate,
		largebody.NewBackendWireProofGate(nil, nil, nil, nil),
		coreruntime.StandardLaneDomainPolicies(),
	)

	proof := validTestProof("openai_chat_v1", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeStreaming)

	assessment, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("same-permit decline discipline: err must be nil, got %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("authority blocker must decline, got %v", assessment.Decision)
	}
	if assessment.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected DeclineReasonAuthorityBlocker, got %v", assessment.Reason)
	}
}

func TestProductionLargeBodyAssessor_UniversalDomainPolicy(t *testing.T) {
	t.Parallel()

	t.Run("Lane3_RejectsWhenNotUniversalModel", func(t *testing.T) {
		t.Parallel()
		assessor := newTestEligibleAssessor(t, "gen-lane3-finite", false)
		proof := validTestProof("openresponses_v1", lipapi.OperationOpenResponsesCreate, lipapi.DeliveryModeStreaming)
		assessment, err := assessor.AssessLargeBody(context.Background(), proof)
		if err != nil {
			t.Fatalf("same-permit decline: err must be nil, got %v", err)
		}
		if assessment.Decision != largebody.AssessmentDecisionDecline {
			t.Fatalf("Lane 3 without AnyAcceptedModel must decline, got %v", assessment.Decision)
		}
	})

	t.Run("Lane3_AcceptsWhenUniversalModel", func(t *testing.T) {
		t.Parallel()
		assessor := newTestEligibleAssessor(t, "gen-lane3-universal", true)
		proof := validTestProof("openresponses_v1", lipapi.OperationOpenResponsesCreate, lipapi.DeliveryModeStreaming)
		assessment, err := assessor.AssessLargeBody(context.Background(), proof)
		if err != nil {
			t.Fatalf("same-permit decline: err must be nil, got %v", err)
		}
		if assessment.Decision != largebody.AssessmentDecisionAccept {
			t.Fatalf("Lane 3 with AnyAcceptedModel must accept, got decision=%v reason=%v", assessment.Decision, assessment.Reason)
		}
	})
}

func TestProductionLargeBodyAssessor_AcceptanceAndStamp(t *testing.T) {
	t.Parallel()

	genID := "gen-acceptance-1"
	assessor := newTestEligibleAssessor(t, genID, true)

	proof := validTestProof("openai_chat_v1", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeStreaming)

	assessment, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("AssessLargeBody: %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("eligible setup must accept, got decision=%v reason=%v", assessment.Decision, assessment.Reason)
	}
	if assessment.Stamp.IsZero() {
		t.Fatal("accepted assessment must carry non-zero Stamp")
	}
	if assessment.Stamp.GenerationID() != genID {
		t.Fatalf("stamp generation ID mismatch: got %q want %q", assessment.Stamp.GenerationID(), genID)
	}
	if assessment.Stamp.ProfileID() != "openai_chat_v1" {
		t.Fatalf("stamp profile ID mismatch: got %q want openai_chat_v1", assessment.Stamp.ProfileID())
	}
}

func TestProductionLargeBodyAssessor_StaticDisposition(t *testing.T) {
	t.Parallel()

	t.Run("StaticBlockerWhenHasStaticBlocker", func(t *testing.T) {
		t.Parallel()
		genID := "gen-disp-blocker"
		census := largebody.NewStandardDependencyCensus(genID)
		census.Hooks.SubmitOccupied = true

		summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
			GenerationID:              genID,
			Planes:                    census.Planes,
			Hooks:                     census.Hooks,
			Ports:                     census.Ports,
			TwoPhaseExecutorAvailable: true,
		}, 4096)
		if err != nil {
			t.Fatalf("CompileWireEligibilitySummary: %v", err)
		}

		authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
		assessor := coreruntime.NewProductionLargeBodyAssessor(
			genID,
			genID,
			authGate,
			nil,
			coreruntime.StandardLaneDomainPolicies(),
		)

		disp, reason := assessor.LargeBodyStaticDisposition("openai_chat_v1")
		if !disp.IsDefinitelyCanonical() {
			t.Fatalf("expected DefinitelyCanonical, got %v", disp)
		}
		if reason != largebody.StaticWireReasonStaticBlocker {
			t.Fatalf("expected StaticWireReasonStaticBlocker, got %v", reason)
		}
	})

	t.Run("NeedsRequestAssessmentWhenEligible", func(t *testing.T) {
		t.Parallel()
		assessor := newTestEligibleAssessor(t, "gen-disp-eligible", true)
		disp, reason := assessor.LargeBodyStaticDisposition("openai_chat_v1")
		if !disp.IsNeedsRequestAssessment() {
			t.Fatalf("expected NeedsRequestAssessment, got %v", disp)
		}
		if reason != largebody.StaticWireReasonNone {
			t.Fatalf("expected StaticWireReasonNone, got %v", reason)
		}
	})
}
