package largebody_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// trackingWireBackend records received facts for exact and domain resolvers
// to prove that immutable body mode and rewrite semantics are threaded to every resolver.
type trackingWireBackend struct {
	compatRequest    bool
	compatDomain     bool
	anyAcceptedModel bool
	reason           largebody.WireSupportReason
	needsRewrite     bool

	requestCalls []largebody.WireRequestFacts
	domainCalls  []largebody.WireDomainFacts
}

func (b *trackingWireBackend) ResolveWireRequest(_ context.Context, facts largebody.WireRequestFacts, _ routing.AttemptCandidate) largebody.WireRequestSupport {
	b.requestCalls = append(b.requestCalls, facts)
	if !b.compatRequest {
		r := b.reason
		if r == largebody.WireSupportReasonNone {
			r = largebody.WireSupportReasonUnsupported
		}
		return largebody.WireRequestSupport{Compatible: false, Reason: r}
	}
	return largebody.WireRequestSupport{
		Compatible:        true,
		NeedsModelRewrite: b.needsRewrite,
	}
}

func (b *trackingWireBackend) ResolveWireDomain(_ context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	b.domainCalls = append(b.domainCalls, facts)
	if !b.compatDomain {
		r := b.reason
		if r == largebody.WireSupportReasonNone {
			r = largebody.WireSupportReasonUnsupported
		}
		return largebody.WireDomainSupport{Compatible: false, Reason: r}
	}
	return largebody.WireDomainSupport{
		Compatible:       true,
		AnyAcceptedModel: b.anyAcceptedModel,
	}
}

func makeTask11_7Proof(routeSelector, clientModel string, mode largebody.BodyMode, rewrite largebody.RewriteSemantics) largebody.Proof {
	return largebody.Proof{
		ProfileID:       "openai-responses-v1",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		RouteSelector:   routeSelector,
		ClientModel:     clientModel,
		MaxOutputTokens: 2048,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "req-11-7",
			ControlCount:   1,
		},
		Mode:      mode,
		Rewrite:   rewrite,
		Identity:  largebody.NewIdentityDigest([32]byte{11, 7, 1}),
		Source:    largebody.NewSourceDigest([32]byte{11, 7, 2}),
		BodyBytes: 4096,
	}
}

// -----------------------------------------------------------------------------
// Test 1: Pass immutable body/rewrite facts to EVERY resolver (Task 11.7; Req 8.1, 8.2, 9)
// -----------------------------------------------------------------------------

func TestTask11_7_ImmutableBodyAndRewriteFactsThreadedToEveryResolver(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver)

	span := largebody.Span{Offset: 12, Length: 8}
	certifiedRewrite, err := largebody.NewModelTokenRewrite(span)
	if err != nil {
		t.Fatalf("failed to construct model token rewrite: %v", err)
	}

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, certifiedRewrite)

	decision, reason, wireReq, wireDomain, cands := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept, got %v / %v", decision, reason)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}

	// 1. Verify exact resolver received immutable body mode and rewrite semantics
	if len(be1.requestCalls) != 1 {
		t.Fatalf("expected 1 request call, got %d", len(be1.requestCalls))
	}
	reqFacts := be1.requestCalls[0]
	if reqFacts.BodyMode != largebody.BodyModeIdentityJSON {
		t.Errorf("expected BodyMode %v, got %v", largebody.BodyModeIdentityJSON, reqFacts.BodyMode)
	}
	if reqFacts.Rewrite != certifiedRewrite {
		t.Errorf("expected Rewrite %v, got %v", certifiedRewrite, reqFacts.Rewrite)
	}
	if reqFacts.Rewrite.Kind() != largebody.RewriteKindModelToken {
		t.Errorf("expected RewriteKindModelToken, got %v", reqFacts.Rewrite.Kind())
	}
	if reqFacts.Rewrite.Span() != span {
		t.Errorf("expected Span %v, got %v", span, reqFacts.Rewrite.Span())
	}

	// 2. Verify domain resolver received immutable body mode and rewrite semantics
	if len(be1.domainCalls) != 1 {
		t.Fatalf("expected 1 domain call, got %d", len(be1.domainCalls))
	}
	domFacts := be1.domainCalls[0]
	if domFacts.BodyMode != largebody.BodyModeIdentityJSON {
		t.Errorf("expected domain BodyMode %v, got %v", largebody.BodyModeIdentityJSON, domFacts.BodyMode)
	}
	if domFacts.Rewrite != certifiedRewrite {
		t.Errorf("expected domain Rewrite %v, got %v", certifiedRewrite, domFacts.Rewrite)
	}

	// 3. Verify returned wire facts carry immutable semantics
	if wireReq.BodyMode != largebody.BodyModeIdentityJSON || wireReq.Rewrite != certifiedRewrite {
		t.Errorf("wireReq does not carry immutable facts: %+v", wireReq)
	}
	if wireDomain.BodyMode != largebody.BodyModeIdentityJSON || wireDomain.Rewrite != certifiedRewrite {
		t.Errorf("wireDomain does not carry immutable facts: %+v", wireDomain)
	}
}

// -----------------------------------------------------------------------------
// Test 2: Homogeneous same-wire domain ACCEPTS (Task 11.7; Requirements 7, 8, 21)
// -----------------------------------------------------------------------------

func TestTask11_7_HomogeneousSameWireDomain_Accepts(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	be2 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}

	backendResolver := largebody.WireBackendMap{
		"backend-1": be1,
		"backend-2": be2,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}, "backend-2": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	assessor := largebody.NewBackendWireProofAssessor(
		*largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver),
	)

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	stamp, err := largebody.NewAssessmentStamp(
		"gen-11-7",
		proof.ProfileID,
		proof.Source,
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		proof.Identity,
	)
	if err != nil {
		t.Fatalf("stamp creation failed: %v", err)
	}
	assessor.AcceptStamp = stamp

	assessment, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("AssessLargeBody unexpected error: %v", err)
	}
	if !assessment.Accepted() {
		t.Fatalf("expected accepted assessment for homogeneous same-wire domain, got %v / %v", assessment.Decision, assessment.Reason)
	}
	if !assessment.WireDomain.UniversalModel {
		t.Errorf("expected universal model envelope in accepted assessment")
	}
	if len(be1.domainCalls) != 1 || len(be2.domainCalls) != 1 {
		t.Errorf("expected both backends in homogeneous domain to be proven via ResolveWireDomain, got be1=%d, be2=%d", len(be1.domainCalls), len(be2.domainCalls))
	}
}

// -----------------------------------------------------------------------------
// Test 3: Heterogeneous incompatible domain DECLINES ENTIRE request (NO PRUNING)
// Requirement 7.5, 7.7, 8.4: any domain member incompatibility => decline before BeginTurn.
// -----------------------------------------------------------------------------

func TestTask11_7_HeterogeneousIncompatibleDomain_DeclinesEntireRequest_NoPruning(t *testing.T) {
	t.Parallel()

	beWire := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	beIncompatible := &trackingWireBackend{compatRequest: false, compatDomain: false, reason: largebody.WireSupportReasonProfileUnsupported}

	backendResolver := largebody.WireBackendMap{
		"backend-wire":         beWire,
		"backend-incompatible": beIncompatible,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	// Initial route points to the wire-compatible backend-wire
	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-wire",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	// But the generation's route-override domain includes backend-incompatible
	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-wire",
		map[string]struct{}{"backend-wire": {}, "backend-incompatible": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	assessor := largebody.NewBackendWireProofAssessor(
		*largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver),
	)

	proof := makeTask11_7Proof("backend-wire:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	assessment, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("AssessLargeBody unexpected error: %v", err)
	}
	if !assessment.Declined() {
		t.Fatalf("expected decline for heterogeneous incompatible domain, got %v", assessment.Decision)
	}
	if assessment.Reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", assessment.Reason)
	}
}

// -----------------------------------------------------------------------------
// Test 4: Any initial candidate incompatibility declines entire request (Req 8.4)
// -----------------------------------------------------------------------------

func TestTask11_7_AnyCandidateIncompatibility_DeclinesEntireRequest(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	be2 := &trackingWireBackend{compatRequest: false, compatDomain: true, anyAcceptedModel: true, reason: largebody.WireSupportReasonModelUnsupported}

	backendResolver := largebody.WireBackendMap{
		"backend-1": be1,
		"backend-2": be2,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	proofGate := largebody.NewBackendWireProofGate(initialGate, nil, nil, backendResolver)

	proof := makeTask11_7Proof("backend-1:gpt-4o | backend-2:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, _, _ := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline when candidate is incompatible, got %v", decision)
	}
	if reason != largebody.DeclineReasonBackendIncompatible {
		t.Fatalf("expected DeclineReasonBackendIncompatible, got %v", reason)
	}
}

// -----------------------------------------------------------------------------
// Test 5: Post-BeginTurn override change inside accepted domain is ACCEPTED (Req 7.4, 8.3)
// -----------------------------------------------------------------------------

func TestTask11_7_PostBeginTurnOverride_InsideAcceptedDomain_Accepts(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	be2 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}

	backendResolver := largebody.WireBackendMap{
		"backend-1": be1,
		"backend-2": be2,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}, "backend-2": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver)

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	// Step 1: Pre-turn assessment accepts homogeneous domain
	decision, reason, _, domainFacts, _ := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected initial assessment accept, got %v / %v", decision, reason)
	}

	// Step 2: Simulate post-BeginTurn override occurring inside the session
	// The session override switches the route to "backend-2:gpt-4o-mini".
	overrideSelector := "backend-2:gpt-4o-mini"
	cands, ok, declineReason := proofGate.ComposeOverrideCandidate(context.Background(), overrideSelector, domainFacts)
	if !ok || declineReason != largebody.DeclineReasonNone {
		t.Fatalf("expected override candidate inside accepted domain to succeed, got ok=%t, reason=%v", ok, declineReason)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Primary.Backend != "backend-2" || cands[0].Primary.Model != "gpt-4o-mini" {
		t.Fatalf("unexpected candidate: %+v", cands[0])
	}
}

// -----------------------------------------------------------------------------
// Test 6: Post-BeginTurn override change OUTSIDE accepted domain REJECTS (Req 7.4, 8.3)
// -----------------------------------------------------------------------------

func TestTask11_7_PostBeginTurnOverride_OutsideAcceptedDomain_Rejects(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true}
	be2 := &trackingWireBackend{compatRequest: true, compatDomain: true}

	backendResolver := largebody.WireBackendMap{
		"backend-1": be1,
		"backend-2": be2,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}, "backend-2": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	// Bounded finite model catalog: only "gpt-4o" is proven
	overrideGate.CandidateModels = []string{"gpt-4o"}

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver)

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	// Pre-turn assessment accepts finite domain
	decision, reason, _, domainFacts, _ := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept, got %v / %v", decision, reason)
	}

	// Case A: Override targets unproven backend outside domain
	cands, ok, declineReason := proofGate.ComposeOverrideCandidate(context.Background(), "backend-unknown:gpt-4o", domainFacts)
	if ok || declineReason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected rejection for unproven backend, got ok=%t, reason=%v, cands=%v", ok, declineReason, cands)
	}

	// Case B: Override targets unproven model outside finite domain
	cands, ok, declineReason = proofGate.ComposeOverrideCandidate(context.Background(), "backend-2:claude-3-opus", domainFacts)
	if ok || declineReason != largebody.DeclineReasonRouteIncompatible {
		t.Fatalf("expected rejection for model outside finite domain, got ok=%t, reason=%v, cands=%v", ok, declineReason, cands)
	}
}

// -----------------------------------------------------------------------------
// Test 7: Rewrite semantics enforced across domain and post-BeginTurn override (Req 9)
// -----------------------------------------------------------------------------

func TestTask11_7_RewriteSemantics_DomainAndCandidateParity(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true, needsRewrite: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	proofGate := largebody.NewBackendWireProofGate(initialGate, nil, nil, backendResolver)

	// Proof with NO rewrite, but backend requires rewrite
	proofNoRewrite := makeTask11_7Proof("backend-1:gpt-4o-native", "gpt-4o-client", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, _, _ := proofGate.Evaluate(context.Background(), proofNoRewrite)
	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected decline when rewrite required but not certified, got %v", decision)
	}
	if reason != largebody.DeclineReasonRewriteUnsupported {
		t.Fatalf("expected DeclineReasonRewriteUnsupported, got %v", reason)
	}

	// Now with certified model rewrite
	span := largebody.Span{Offset: 10, Length: 13}
	certifiedRewrite, err := largebody.NewModelTokenRewrite(span)
	if err != nil {
		t.Fatalf("rewrite creation: %v", err)
	}
	proofWithRewrite := makeTask11_7Proof("backend-1:gpt-4o-native", "gpt-4o-client", largebody.BodyModeIdentityJSON, certifiedRewrite)

	decision, reason, _, _, _ = proofGate.Evaluate(context.Background(), proofWithRewrite)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept with certified rewrite, got %v / %v", decision, reason)
	}
}

// -----------------------------------------------------------------------------
// Test 8: Pure side-effect sentinel test (Req 6.2)
// -----------------------------------------------------------------------------

func TestTask11_7_PureSideEffectSentinel(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, backendResolver)
	assessor := largebody.NewBackendWireProofAssessor(*proofGate)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("AssessLargeBody panicked due to side-effect trap: %v", r)
		}
	}()

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())
	_, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sentinel.snapshotCalled {
		t.Fatal("sentinel snapshot was called during assessment")
	}
}

// -----------------------------------------------------------------------------
// Test 9: LateSelectorGate integration unions domain envelopes (Req 7.4)
// -----------------------------------------------------------------------------

func TestTask11_7_LateSelectorGate_Integration(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	be2 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}

	backendResolver := largebody.WireBackendMap{
		"backend-1": be1,
		"backend-2": be2,
	}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	sentinel := &panicSentinelOverrideReader{}
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"backend-1",
		map[string]struct{}{"backend-1": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(sentinel, validator, backendResolver)
	overrideGate.CandidateModels = []string{"gpt-4o"}

	lateContract := largebody.BoundedRouteDomainContract{
		TargetBackends: []string{"backend-2"},
		TargetModels:   []string{"gpt-4o-mini"},
	}
	lateGate := &largebody.LateSelectorAssessmentGate{
		RouteHints:      nil,
		BackendResolver: backendResolver,
		KnownBackends:   map[string]struct{}{"backend-1": {}, "backend-2": {}},
		RouteHintAuthorities: []largebody.RouteHintAuthority{
			{Contract: &lateContract},
		},
	}

	proofGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, lateGate, backendResolver)

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, domainFacts, _ := proofGate.Evaluate(context.Background(), proof)
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected accept for unioned domain, got %v / %v", decision, reason)
	}

	// Verify both models are present in unioned domain
	if len(domainFacts.CandidateModels) != 2 {
		t.Fatalf("expected 2 candidate models in unioned domain, got %v", domainFacts.CandidateModels)
	}

	// Verify both backends are legal in domain
	legalBackends := proofGate.LegalDomainBackends()
	if len(legalBackends) != 2 || legalBackends[0] != "backend-1" || legalBackends[1] != "backend-2" {
		t.Fatalf("expected [backend-1 backend-2] in legal domain backends, got %v", legalBackends)
	}

	// Post-BeginTurn override to backend-2:gpt-4o-mini (from late selector authority) succeeds
	cands, ok, declineReason := proofGate.ComposeOverrideCandidate(context.Background(), "backend-2:gpt-4o-mini", domainFacts)
	if !ok || declineReason != largebody.DeclineReasonNone {
		t.Fatalf("expected post-BeginTurn override to backend-2:gpt-4o-mini to succeed, got ok=%t, reason=%v", ok, declineReason)
	}
	if len(cands) != 1 || cands[0].Primary.Backend != "backend-2" {
		t.Fatalf("unexpected candidate: %+v", cands)
	}
}

// -----------------------------------------------------------------------------
// Test 10: Canceled context immediately declines (Req 8.5)
// -----------------------------------------------------------------------------

func TestTask11_7_CanceledContext_Declines(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	proofGate := largebody.NewBackendWireProofGate(initialGate, nil, nil, backendResolver)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proof := makeTask11_7Proof("backend-1:gpt-4o", "gpt-4o", largebody.BodyModeIdentityJSON, largebody.NewNoRewrite())

	decision, reason, _, _, _ := proofGate.Evaluate(ctx, proof)
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonCanceled {
		t.Fatalf("expected DeclineReasonCanceled, got %v / %v", decision, reason)
	}

	domainFacts := largebody.WireDomainFacts{
		ProfileID:       proof.ProfileID,
		Operation:       proof.Operation,
		Delivery:        proof.Delivery,
		BodyMode:        proof.Mode,
		UniversalModel:  true,
		CandidateModels: nil,
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-1", Model: "gpt-4o"},
	}
	ok, reason := proofGate.VerifyOverrideCandidate(ctx, domainFacts, cand)
	if ok || reason != largebody.DeclineReasonCanceled {
		t.Fatalf("expected DeclineReasonCanceled from VerifyOverrideCandidate, got ok=%t, reason=%v", ok, reason)
	}
}

// -----------------------------------------------------------------------------
// Test 11: ComposeOverrideCandidate respects aliases and default backend
// -----------------------------------------------------------------------------

func TestTask11_7_ComposeOverrideCandidate_AliasesAndDefaultBackend(t *testing.T) {
	t.Parallel()

	be1 := &trackingWireBackend{compatRequest: true, compatDomain: true, anyAcceptedModel: true}
	backendResolver := largebody.WireBackendMap{"backend-1": be1}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})

	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: "^fast$", Replacement: "backend-1:gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("failed to create alias resolver: %v", err)
	}

	initialGate := largebody.NewInitialRouteAssessmentGate(
		aliases,
		"backend-1",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		backendResolver,
	)

	proofGate := largebody.NewBackendWireProofGate(initialGate, nil, nil, backendResolver)

	domainFacts := largebody.WireDomainFacts{
		ProfileID:      "openai-responses-v1",
		Operation:      lipapi.OperationOpenAIChatCompletions,
		Delivery:       lipapi.DeliveryModeStreaming,
		BodyMode:       largebody.BodyModeIdentityJSON,
		UniversalModel: true,
	}

	// Alias "fast" resolves to "backend-1:gpt-4o-mini"
	cands, ok, reason := proofGate.ComposeOverrideCandidate(context.Background(), "fast", domainFacts)
	if !ok || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected alias override to succeed, got ok=%t, reason=%v", ok, reason)
	}
	if len(cands) != 1 || cands[0].Primary.Backend != "backend-1" || cands[0].Primary.Model != "gpt-4o-mini" {
		t.Fatalf("unexpected candidates for alias: %+v", cands)
	}

	// Bare model "gpt-4o" resolves using default backend "backend-1"
	cands, ok, reason = proofGate.ComposeOverrideCandidate(context.Background(), "gpt-4o", domainFacts)
	if !ok || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected bare model override to succeed with default backend, got ok=%t, reason=%v", ok, reason)
	}
	if len(cands) != 1 || cands[0].Primary.Backend != "backend-1" || cands[0].Primary.Model != "gpt-4o" {
		t.Fatalf("unexpected candidates for bare model: %+v", cands)
	}
}
