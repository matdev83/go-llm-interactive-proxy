package largebody_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
)

// Test 1: Metadata-only receive view is bounded, carries all facts required for
// receive/dispatch, and contains no lipapi.Call or prompt content (Requirements 13.7, 19.1, 19.4).
func TestTask12_3_MetadataOnly_ReceiveView_BoundedAndCallFree(t *testing.T) {
	turnFacts := largebody.DefaultTestWireTurnFacts()
	recvView := turnFacts.ToReceiveView()

	// Verify fields populated from WireTurnFacts
	if recvView.TraceID != turnFacts.Identity.TraceID {
		t.Fatalf("expected TraceID %s, got %s", turnFacts.Identity.TraceID, recvView.TraceID)
	}
	if recvView.ALegID != turnFacts.Session.Input.ALegID {
		t.Fatalf("expected ALegID %s, got %s", turnFacts.Session.Input.ALegID, recvView.ALegID)
	}
	if recvView.RequestID != turnFacts.Identity.RequestID {
		t.Fatalf("expected RequestID %s, got %s", turnFacts.Identity.RequestID, recvView.RequestID)
	}
	if recvView.SessionID != turnFacts.Session.Input.AuthoritativeSessionID {
		t.Fatalf("expected SessionID %s, got %s", turnFacts.Session.Input.AuthoritativeSessionID, recvView.SessionID)
	}
	if recvView.CandidateModel != turnFacts.Route.CandidateModel {
		t.Fatalf("expected CandidateModel %s, got %s", turnFacts.Route.CandidateModel, recvView.CandidateModel)
	}
	if recvView.ClientModel != turnFacts.Route.ClientModel {
		t.Fatalf("expected ClientModel %s, got %s", turnFacts.Route.ClientModel, recvView.ClientModel)
	}
	if recvView.BillingCallID != turnFacts.Economic.BillingCallID {
		t.Fatalf("expected BillingCallID %s, got %s", turnFacts.Economic.BillingCallID, recvView.BillingCallID)
	}
	if recvView.AccountID != turnFacts.Economic.AccountID {
		t.Fatalf("expected AccountID %s, got %s", turnFacts.Economic.AccountID, recvView.AccountID)
	}
	if recvView.CustomerPricing != turnFacts.Economic.CustomerPricingRef {
		t.Fatalf("expected CustomerPricing %s, got %s", turnFacts.Economic.CustomerPricingRef, recvView.CustomerPricing)
	}
	if recvView.ChargePolicy != turnFacts.Economic.ChargePolicyRef {
		t.Fatalf("expected ChargePolicy %s, got %s", turnFacts.Economic.ChargePolicyRef, recvView.ChargePolicy)
	}
	if recvView.Operation != turnFacts.Protocol.Operation {
		t.Fatalf("expected Operation %s, got %s", turnFacts.Protocol.Operation, recvView.Operation)
	}
	if recvView.Delivery != turnFacts.Protocol.Delivery {
		t.Fatalf("expected Delivery %s, got %s", turnFacts.Protocol.Delivery, recvView.Delivery)
	}
	if recvView.MaxOutputTokens != turnFacts.MaxOutput.MaxOutputTokens {
		t.Fatalf("expected MaxOutputTokens %d, got %d", turnFacts.MaxOutput.MaxOutputTokens, recvView.MaxOutputTokens)
	}

	// Bounds validation passes
	if err := recvView.Validate(4096); err != nil {
		t.Fatalf("expected valid bounds, got %v", err)
	}

	// Budget overflow is rejected
	if err := recvView.Validate(2); err == nil {
		t.Fatalf("expected budget error for tight budget")
	}

	// HookMeta produces metadata without Call
	trID, aID, bID, be, seq := recvView.HookMeta("bleg-123", 1, "backend-openai")
	if trID != recvView.TraceID || aID != recvView.ALegID || bID != "bleg-123" || be != "backend-openai" || seq != 1 {
		t.Fatalf("HookMeta returned unexpected values: %s %s %s %s %d", trID, aID, bID, be, seq)
	}
}

// Test 2: Metadata-only terminal facts view is bounded, carries facts needed for
// settlement, metering, billing, and affinity commit, and contains no lipapi.Call (Requirements 13.7, 19.1, 19.4).
func TestTask12_3_MetadataOnly_TerminalFacts_BoundedAndCallFree(t *testing.T) {
	turnFacts := largebody.DefaultTestWireTurnFacts()
	termFacts := turnFacts.ToTerminalFacts()

	if termFacts.TraceID != turnFacts.Identity.TraceID {
		t.Fatalf("expected TraceID %s, got %s", turnFacts.Identity.TraceID, termFacts.TraceID)
	}
	if termFacts.ALegID != turnFacts.Session.Input.ALegID {
		t.Fatalf("expected ALegID %s, got %s", turnFacts.Session.Input.ALegID, termFacts.ALegID)
	}
	if termFacts.BillingCallID != turnFacts.Economic.BillingCallID {
		t.Fatalf("expected BillingCallID %s, got %s", turnFacts.Economic.BillingCallID, termFacts.BillingCallID)
	}
	if termFacts.AccountID != turnFacts.Economic.AccountID {
		t.Fatalf("expected AccountID %s, got %s", turnFacts.Economic.AccountID, termFacts.AccountID)
	}
	if termFacts.BodyBytes != turnFacts.Source.BodyBytes {
		t.Fatalf("expected BodyBytes %d, got %d", turnFacts.Source.BodyBytes, termFacts.BodyBytes)
	}
	if termFacts.SourceDigest != turnFacts.Source.SourceDigest {
		t.Fatalf("expected SourceDigest %v, got %v", turnFacts.Source.SourceDigest, termFacts.SourceDigest)
	}
	if termFacts.CanonicalDigest != turnFacts.Identity.CanonicalDigest {
		t.Fatalf("expected CanonicalDigest %v, got %v", turnFacts.Identity.CanonicalDigest, termFacts.CanonicalDigest)
	}

	// Validate bounds
	if err := termFacts.Validate(4096); err != nil {
		t.Fatalf("expected valid bounds, got %v", err)
	}
	if err := termFacts.Validate(2); err == nil {
		t.Fatalf("expected budget error for tight budget")
	}
}

// Test 3: Metadata-only conversation observation view carries bounded metadata
// without prompt content (Requirements 13.7, 19.4).
func TestTask12_3_MetadataOnly_ConversationObserver_Bounded(t *testing.T) {
	turnFacts := largebody.DefaultTestWireTurnFacts()
	obsFacts := turnFacts.ToConversationObserverFacts("stage_early", "rev_1", 3)

	if obsFacts.ALegID != turnFacts.Session.Input.ALegID {
		t.Fatalf("expected ALegID %s, got %s", turnFacts.Session.Input.ALegID, obsFacts.ALegID)
	}
	if obsFacts.TraceID != turnFacts.Identity.TraceID {
		t.Fatalf("expected TraceID %s, got %s", turnFacts.Identity.TraceID, obsFacts.TraceID)
	}
	if obsFacts.Stage != "stage_early" {
		t.Fatalf("expected Stage stage_early, got %s", obsFacts.Stage)
	}
	if obsFacts.Revision != "rev_1" {
		t.Fatalf("expected Revision rev_1, got %s", obsFacts.Revision)
	}
	if obsFacts.RuleCount != 3 {
		t.Fatalf("expected RuleCount 3, got %d", obsFacts.RuleCount)
	}

	if err := obsFacts.Validate(4096); err != nil {
		t.Fatalf("expected valid bounds, got %v", err)
	}
	if err := obsFacts.Validate(2); err == nil {
		t.Fatalf("expected budget error")
	}
}

// Test 4: Metadata-only continuation lineage carries bounded identifiers
// without parent trajectory or message items (Requirements 13.7, 19.4).
func TestTask12_3_MetadataOnly_ContinuationLineage_Bounded(t *testing.T) {
	turnFacts := largebody.DefaultTestWireTurnFacts()
	lineage := turnFacts.ToContinuationLineage()

	if lineage.TrajectoryRef != turnFacts.Identity.RequestID {
		t.Fatalf("expected TrajectoryRef %s, got %s", turnFacts.Identity.RequestID, lineage.TrajectoryRef)
	}
	if lineage.ParentRef != "" {
		t.Fatalf("expected empty ParentRef for default wire facts, got %s", lineage.ParentRef)
	}

	if err := lineage.Validate(4096); err != nil {
		t.Fatalf("expected valid bounds, got %v", err)
	}
	if err := lineage.Validate(2); err == nil {
		t.Fatalf("expected budget error")
	}
}

// Test 5: Metadata-only compaction preservation carries bounded correlation
// without prompt content (Requirements 13.7, 19.4).
func TestTask12_3_MetadataOnly_CompactionMeta_Bounded(t *testing.T) {
	turnFacts := largebody.DefaultTestWireTurnFacts()
	compMeta := turnFacts.ToCompactionMeta("bleg-1", 1, "tx-99", "rule-42")

	if compMeta.TraceID != turnFacts.Identity.TraceID {
		t.Fatalf("expected TraceID %s, got %s", turnFacts.Identity.TraceID, compMeta.TraceID)
	}
	if compMeta.SessionID != turnFacts.Session.Input.AuthoritativeSessionID {
		t.Fatalf("expected SessionID %s, got %s", turnFacts.Session.Input.AuthoritativeSessionID, compMeta.SessionID)
	}
	if compMeta.ALegID != turnFacts.Session.Input.ALegID {
		t.Fatalf("expected ALegID %s, got %s", turnFacts.Session.Input.ALegID, compMeta.ALegID)
	}
	if compMeta.BLegID != "bleg-1" || compMeta.AttemptSeq != 1 || compMeta.TransactionID != "tx-99" || compMeta.RuleID != "rule-42" {
		t.Fatalf("unexpected compaction meta fields: %+v", compMeta)
	}

	if err := compMeta.Validate(4096); err != nil {
		t.Fatalf("expected valid bounds, got %v", err)
	}
	if err := compMeta.Validate(2); err == nil {
		t.Fatalf("expected budget error")
	}
}

// Test 6: Content/trajectory conversation uses (reader, tagger, steering writer,
// active overlays, neverBackend rules) are assessment blockers and cause decline
// with DeclineReasonAuthorityBlocker (Requirements 13.3, 19.2, 19.4).
func TestTask12_3_ContentTrajectory_Conversation_Declines(t *testing.T) {
	genID := "gen-task12-3"
	summary := buildValidSummary(genID)
	census := largebody.NewStandardDependencyCensus(genID)
	gate := largebody.NewConservativeDependencyAssessmentGate(summary, census, genID)

	turnFacts := largebody.DefaultTestWireTurnFacts()

	// Base clean dependency facts accept
	baseDep := turnFacts.ToDependencyFacts()
	dec, reason := gate.Evaluate(baseDep)
	if dec != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("expected clean dependencies to accept, got %v / %v", dec, reason)
	}

	// 1. Conversation Reader occupied -> decline
	dep1 := baseDep
	dep1.HasConversationReader = true
	dec, reason = gate.Evaluate(dep1)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for conversation reader, got %v / %v", dec, reason)
	}

	// 2. Conversation Tagger occupied -> decline
	dep2 := baseDep
	dep2.HasConversationTagger = true
	dec, reason = gate.Evaluate(dep2)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for conversation tagger, got %v / %v", dec, reason)
	}

	// 3. Steering Writer Factory occupied -> decline
	dep3 := baseDep
	dep3.HasSteeringWriter = true
	dec, reason = gate.Evaluate(dep3)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for steering writer, got %v / %v", dec, reason)
	}

	// 4. Active Steering Overlays in snapshot -> decline
	dep4 := baseDep
	dep4.ActiveSteeringOverlays = 1
	dec, reason = gate.Evaluate(dep4)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for active steering overlays, got %v / %v", dec, reason)
	}

	// 5. Active NeverBackend rules in snapshot -> decline
	dep5 := baseDep
	dep5.ActiveNeverBackendRules = 1
	dec, reason = gate.Evaluate(dep5)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for neverBackend rules, got %v / %v", dec, reason)
	}
}

// Test 7: Content/trajectory continuation uses (continuation intent, parent continuation,
// continuation port) are assessment blockers (Requirements 13.5, 19.2, 19.4).
func TestTask12_3_ContentTrajectory_Continuation_Declines(t *testing.T) {
	genID := "gen-task12-3"
	summary := buildValidSummary(genID)
	census := largebody.NewStandardDependencyCensus(genID)
	gate := largebody.NewConservativeDependencyAssessmentGate(summary, census, genID)

	baseDep := largebody.DefaultTestWireTurnFacts().ToDependencyFacts()

	// 1. Continuation intent present -> decline
	dep1 := baseDep
	dep1.HasContinuationIntent = true
	dec, reason := gate.Evaluate(dep1)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for continuation intent, got %v / %v", dec, reason)
	}

	// 2. Parent continuation requested -> decline
	dep2 := baseDep
	dep2.HasParentContinuation = true
	dec, reason = gate.Evaluate(dep2)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for parent continuation, got %v / %v", dec, reason)
	}

	// 3. Continuation port registered as occupied in census -> decline
	censusWithPort := census
	censusWithPort.AddPort("core.continuation", true)
	gateWithPort := largebody.NewConservativeDependencyAssessmentGate(summary, censusWithPort, genID)
	dec, reason = gateWithPort.Evaluate(baseDep)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for occupied core.continuation port, got %v / %v", dec, reason)
	}
}

// Test 8: Content/trajectory interleaved thinking uses (processor occupied, interleaved active)
// are assessment blockers (Requirements 13.3, 19.2, 19.4).
func TestTask12_3_ContentTrajectory_Interleaved_Declines(t *testing.T) {
	genID := "gen-task12-3"
	summary := buildValidSummary(genID)
	census := largebody.NewStandardDependencyCensus(genID)
	gate := largebody.NewConservativeDependencyAssessmentGate(summary, census, genID)

	baseDep := largebody.DefaultTestWireTurnFacts().ToDependencyFacts()

	// 1. Interleaved processor occupied -> decline
	dep1 := baseDep
	dep1.HasInterleavedProcessor = true
	dec, reason := gate.Evaluate(dep1)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for interleaved processor, got %v / %v", dec, reason)
	}

	// 2. Interleaved active -> decline
	dep2 := baseDep
	dep2.InterleavedActive = true
	dec, reason = gate.Evaluate(dep2)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for active interleaved mode, got %v / %v", dec, reason)
	}
}

// Test 9: Content/trajectory compaction uses (detector on request leg, preservers)
// are assessment blockers (Requirements 13.3, 19.2, 19.4).
func TestTask12_3_ContentTrajectory_Compaction_Declines(t *testing.T) {
	genID := "gen-task12-3"
	summary := buildValidSummary(genID)
	census := largebody.NewStandardDependencyCensus(genID)
	gate := largebody.NewConservativeDependencyAssessmentGate(summary, census, genID)

	baseDep := largebody.DefaultTestWireTurnFacts().ToDependencyFacts()

	// 1. Compaction detector on request leg -> decline
	dep1 := baseDep
	dep1.HasCompactionDetector = true
	dec, reason := gate.Evaluate(dep1)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for compaction detector, got %v / %v", dec, reason)
	}

	// 2. Compaction preserver on request leg -> decline
	dep2 := baseDep
	dep2.HasCompactionPreserver = true
	dec, reason = gate.Evaluate(dep2)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for compaction preserver, got %v / %v", dec, reason)
	}
}

// Test 10: Content/trajectory terminal decision uses (terminal decision active)
// are assessment blockers (Requirements 13.5, 19.2, 19.4).
func TestTask12_3_ContentTrajectory_TerminalDecision_Declines(t *testing.T) {
	genID := "gen-task12-3"
	summary := buildValidSummary(genID)
	census := largebody.NewStandardDependencyCensus(genID)
	gate := largebody.NewConservativeDependencyAssessmentGate(summary, census, genID)

	baseDep := largebody.DefaultTestWireTurnFacts().ToDependencyFacts()

	dep := baseDep
	dep.TerminalDecisionActive = true
	dec, reason := gate.Evaluate(dep)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected decline for terminal decision active, got %v / %v", dec, reason)
	}
}

// Test 11: Under Blocker 2 conservative fail-safe, occupied response-only planes
// cause assessment to decline because wire streaming bypasses response machinery.
func TestTask12_3_ResponseOnly_CompactionAndTerminal_DeclinesUnderBlocker2(t *testing.T) {
	genID := "gen-task12-3"
	summary := buildValidSummary(genID)
	census := largebody.NewStandardDependencyCensus(genID)

	// In census: response-only planes are occupied
	for i := range census.Planes {
		if census.Planes[i].Access == largebody.PlaneAccessResponseOnly {
			census.Planes[i].Occupied = true
		}
	}

	// Compaction background aux is occupied (wire-safe)
	census.AddPort("compaction.background_aux", true)

	gate := largebody.NewConservativeDependencyAssessmentGate(summary, census, genID)

	// Dependency facts with response-only compaction metadata (PreviewResponse/ResponseReleased)
	dep := largebody.DefaultTestWireTurnFacts().ToDependencyFacts()
	// No request-leg compaction detector or preserver: only response-side meta
	dep.HasCompactionDetector = false
	dep.HasCompactionPreserver = false

	dec, reason := gate.Evaluate(dep)
	if dec != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("expected response-only observation to decline under Blocker 2, got %v / %v", dec, reason)
	}
}

// Test 12: Standard narrow port census includes continuation and terminal decision
// ports classified as DependencyClassBlocker (Requirements 13, 19.4).
func TestTask12_3_StandardNarrowPortCensus_ContinuationAndTerminalPorts(t *testing.T) {
	for _, port := range []string{
		"core.continuation",
		"continuation.resolver",
		"terminal.decision_provider",
		"conversation.projection",
	} {
		class, ok := largebody.LookupStandardPort(port)
		if !ok {
			t.Fatalf("expected standard port %q to be registered", port)
		}
		if class != largebody.DependencyClassBlocker {
			t.Fatalf("expected standard port %q to be DependencyClassBlocker, got %v", port, class)
		}
	}
}

// Helper: build a valid sealed WireEligibilitySummary pinned to genID.
func buildValidSummary(genID string) largebody.WireEligibilitySummary {
	input := largebody.WireEligibilityInput{
		GenerationID: genID,
		Planes:       make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount),
		Hooks:        largebody.HookEligibilityInput{},
		Ports: largebody.NarrowPortEligibilityInput{
			BackendsEmpty: false,
		},
		TwoPhaseExecutorAvailable: true,
	}
	for i := 0; i < largebody.WireEligibilityPlaneCount; i++ {
		id, _ := largebody.WireEligibilityPlaneID(i)
		input.Planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessMetadataOnly,
			Occupied: false,
		}
	}
	summary, err := largebody.CompileWireEligibilitySummary(input, 4096)
	if err != nil {
		panic(err)
	}
	return summary
}
