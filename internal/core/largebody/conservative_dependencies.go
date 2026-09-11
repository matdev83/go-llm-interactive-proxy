package largebody

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// WireReceiveView provides a bounded, read-only metadata view of receive-turn facts
// for wire execution without retaining or dereferencing lipapi.Call (Requirements 13.7, 19.1, 19.4).
type WireReceiveView struct {
	TraceID         string
	ALegID          string
	RequestID       string
	SessionID       string
	RoutePrefs      []string
	CandidateModel  string
	ClientModel     string
	BillingCallID   string
	AccountID       string
	CustomerPricing string
	ChargePolicy    string
	IdentityStamped bool
	Operation       lipapi.Operation
	Delivery        lipapi.DeliveryMode
	MaxOutputTokens int64
}

// Validate checks string lengths and numeric bounds under maxFactBytes.
func (v WireReceiveView) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, val := range map[string]string{
		"trace id":         v.TraceID,
		"aleg id":          v.ALegID,
		"request id":       v.RequestID,
		"session id":       v.SessionID,
		"candidate model":  v.CandidateModel,
		"client model":     v.ClientModel,
		"billing call id":  v.BillingCallID,
		"account id":       v.AccountID,
		"customer pricing": v.CustomerPricing,
		"charge policy":    v.ChargePolicy,
	} {
		if int64(len(val)) > maxFactBytes {
			return fmt.Errorf("largebody: receive view %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if int64(len(v.RoutePrefs)) > maxFactBytes {
		return fmt.Errorf("largebody: receive view route prefs count exceeds %d", maxFactBytes)
	}
	for i, pref := range v.RoutePrefs {
		if int64(len(pref)) > maxFactBytes {
			return fmt.Errorf("largebody: receive view route pref %d exceeds %d bytes", i, maxFactBytes)
		}
	}
	if int64(len(v.Operation)) > maxFactBytes {
		return fmt.Errorf("largebody: receive view operation exceeds %d bytes", maxFactBytes)
	}
	if v.MaxOutputTokens < 0 {
		return fmt.Errorf("largebody: receive view max output tokens must be >= 0, got %d", v.MaxOutputTokens)
	}
	return nil
}

// HookMeta returns bounded metadata for hook invocation without lipapi.Call.
func (v WireReceiveView) HookMeta(blegID string, attemptSeq int, backend string) (traceID, aLegID, bLegID, backendID string, seq int) {
	return v.TraceID, v.ALegID, blegID, strings.TrimSpace(backend), attemptSeq
}

// WireTerminalFacts is the bounded terminal-facing metadata view
// required for terminal settlement, billing, and affinity commit
// without retaining lipapi.Call (Requirements 13.7, 19.1, 19.4).
type WireTerminalFacts struct {
	TraceID         string
	ALegID          string
	RequestID       string
	SessionID       string
	BillingCallID   string
	AccountID       string
	CustomerPricing string
	ChargePolicy    string
	IdentityStamped bool
	RoutePrefs      []string
	CandidateModel  string
	ClientModel     string
	Operation       lipapi.Operation
	Delivery        lipapi.DeliveryMode
	BodyBytes       int64
	SourceDigest    SourceDigest
	CanonicalDigest IdentityDigest
}

// Validate checks string lengths and numeric bounds under maxFactBytes.
func (f WireTerminalFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, val := range map[string]string{
		"trace id":         f.TraceID,
		"aleg id":          f.ALegID,
		"request id":       f.RequestID,
		"session id":       f.SessionID,
		"billing call id":  f.BillingCallID,
		"account id":       f.AccountID,
		"customer pricing": f.CustomerPricing,
		"charge policy":    f.ChargePolicy,
		"candidate model":  f.CandidateModel,
		"client model":     f.ClientModel,
	} {
		if int64(len(val)) > maxFactBytes {
			return fmt.Errorf("largebody: terminal facts %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if int64(len(f.RoutePrefs)) > maxFactBytes {
		return fmt.Errorf("largebody: terminal facts route prefs count exceeds %d", maxFactBytes)
	}
	for i, pref := range f.RoutePrefs {
		if int64(len(pref)) > maxFactBytes {
			return fmt.Errorf("largebody: terminal facts route pref %d exceeds %d bytes", i, maxFactBytes)
		}
	}
	if int64(len(f.Operation)) > maxFactBytes {
		return fmt.Errorf("largebody: terminal facts operation exceeds %d bytes", maxFactBytes)
	}
	if f.BodyBytes < 0 {
		return fmt.Errorf("largebody: terminal facts body bytes must be >= 0, got %d", f.BodyBytes)
	}
	return nil
}

// WireConversationObserverFacts carries bounded observation metadata for conversation
// events without prompt content (Requirements 13.7, 19.4).
type WireConversationObserverFacts struct {
	ALegID    string
	TraceID   string
	Stage     string
	Revision  string
	RuleCount int
}

// Validate checks bounds under maxFactBytes.
func (f WireConversationObserverFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, val := range map[string]string{
		"aleg id":  f.ALegID,
		"trace id": f.TraceID,
		"stage":    f.Stage,
		"revision": f.Revision,
	} {
		if int64(len(val)) > maxFactBytes {
			return fmt.Errorf("largebody: conversation observer %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if f.RuleCount < 0 {
		return fmt.Errorf("largebody: conversation observer rule count must be >= 0, got %d", f.RuleCount)
	}
	return nil
}

// WireContinuationLineage provides bounded metadata for continuation lineage
// without retaining parent trajectory or prompt content (Requirements 13.7, 19.4).
type WireContinuationLineage struct {
	ParentRef     string
	TrajectoryRef string
	ProgressRef   string
	Attempt       uint8
}

// Validate checks bounds under maxFactBytes.
func (l WireContinuationLineage) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, val := range map[string]string{
		"parent ref":     l.ParentRef,
		"trajectory ref": l.TrajectoryRef,
		"progress ref":   l.ProgressRef,
	} {
		if int64(len(val)) > maxFactBytes {
			return fmt.Errorf("largebody: continuation lineage %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	return nil
}

// WireCompactionMeta provides bounded preservation metadata for compaction observation
// without retaining lipapi.Call (Requirements 13.7, 19.4).
type WireCompactionMeta struct {
	TraceID       string
	SessionID     string
	ALegID        string
	BLegID        string
	AttemptSeq    int
	TransactionID string
	RuleID        string
}

// Validate checks bounds under maxFactBytes.
func (m WireCompactionMeta) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, val := range map[string]string{
		"trace id":       m.TraceID,
		"session id":     m.SessionID,
		"aleg id":        m.ALegID,
		"bleg id":        m.BLegID,
		"transaction id": m.TransactionID,
		"rule id":        m.RuleID,
	} {
		if int64(len(val)) > maxFactBytes {
			return fmt.Errorf("largebody: compaction meta %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if m.AttemptSeq < 0 {
		return fmt.Errorf("largebody: compaction meta attempt seq must be >= 0, got %d", m.AttemptSeq)
	}
	return nil
}

// WireInterleavedStateFacts provides bounded metadata for interleaved thinking state
// without prompt content (Requirements 13.7, 19.4).
type WireInterleavedStateFacts struct {
	Enabled    bool
	CycleSeq   int
	ActiveRole string
}

// Validate checks bounds under maxFactBytes.
func (f WireInterleavedStateFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if int64(len(f.ActiveRole)) > maxFactBytes {
		return fmt.Errorf("largebody: interleaved state active role exceeds %d bytes", maxFactBytes)
	}
	if f.CycleSeq < 0 {
		return fmt.Errorf("largebody: interleaved state cycle seq must be >= 0, got %d", f.CycleSeq)
	}
	return nil
}

// WireDependencyFacts carries the per-request dependency state across the
// receive, terminal, conversation, continuation, interleaved, and compaction
// surfaces (Requirements 13, 19; Task 12.3).
type WireDependencyFacts struct {
	ReceiveView             WireReceiveView
	TerminalFacts           WireTerminalFacts
	HasConversationReader   bool
	HasConversationTagger   bool
	HasSteeringWriter       bool
	ActiveSteeringOverlays  int
	ActiveNeverBackendRules int
	HasContinuationIntent   bool
	HasParentContinuation   bool
	ContinuationLineage     WireContinuationLineage
	HasInterleavedProcessor bool
	InterleavedActive       bool
	HasCompactionDetector   bool
	HasCompactionPreserver  bool
	CompactionMeta          WireCompactionMeta
	TerminalDecisionActive  bool
}

// ToReceiveView projects bounded receive view from WireTurnFacts.
func (f WireTurnFacts) ToReceiveView() WireReceiveView {
	cand := f.Route.CandidateModel
	if cand == "" {
		cand = f.Route.ClientModel
	}
	sessID := f.Session.Input.AuthoritativeSessionID
	if sessID == "" {
		sessID = f.Identity.RequestID
	}
	alegID := f.Session.Input.ALegID
	if alegID == "" {
		alegID = f.Identity.RequestID
	}
	bCallID := f.Economic.BillingCallID
	if bCallID == "" {
		bCallID = f.Identity.RequestID
	}
	return WireReceiveView{
		TraceID:         f.Identity.TraceID,
		ALegID:          alegID,
		RequestID:       f.Identity.RequestID,
		SessionID:       sessID,
		RoutePrefs:      f.Route.RoutePrefs,
		CandidateModel:  cand,
		ClientModel:     f.Route.ClientModel,
		BillingCallID:   bCallID,
		AccountID:       f.Economic.AccountID,
		CustomerPricing: f.Economic.CustomerPricingRef,
		ChargePolicy:    f.Economic.ChargePolicyRef,
		IdentityStamped: f.Economic.CustomerPricingRef != "" || f.Economic.ChargePolicyRef != "",
		Operation:       f.Protocol.Operation,
		Delivery:        f.Protocol.Delivery,
		MaxOutputTokens: f.MaxOutput.MaxOutputTokens,
	}
}

// ToTerminalFacts projects bounded terminal facts from WireTurnFacts.
func (f WireTurnFacts) ToTerminalFacts() WireTerminalFacts {
	cand := f.Route.CandidateModel
	if cand == "" {
		cand = f.Route.ClientModel
	}
	sessID := f.Session.Input.AuthoritativeSessionID
	if sessID == "" {
		sessID = f.Identity.RequestID
	}
	alegID := f.Session.Input.ALegID
	if alegID == "" {
		alegID = f.Identity.RequestID
	}
	bCallID := f.Economic.BillingCallID
	if bCallID == "" {
		bCallID = f.Identity.RequestID
	}
	return WireTerminalFacts{
		TraceID:         f.Identity.TraceID,
		ALegID:          alegID,
		RequestID:       f.Identity.RequestID,
		SessionID:       sessID,
		BillingCallID:   bCallID,
		AccountID:       f.Economic.AccountID,
		CustomerPricing: f.Economic.CustomerPricingRef,
		ChargePolicy:    f.Economic.ChargePolicyRef,
		IdentityStamped: f.Economic.CustomerPricingRef != "" || f.Economic.ChargePolicyRef != "",
		RoutePrefs:      f.Route.RoutePrefs,
		CandidateModel:  cand,
		ClientModel:     f.Route.ClientModel,
		Operation:       f.Protocol.Operation,
		Delivery:        f.Protocol.Delivery,
		BodyBytes:       f.Source.BodyBytes,
		SourceDigest:    f.Source.SourceDigest,
		CanonicalDigest: f.Identity.CanonicalDigest,
	}
}

// ToConversationObserverFacts projects bounded conversation observer facts.
func (f WireTurnFacts) ToConversationObserverFacts(stage, revision string, ruleCount int) WireConversationObserverFacts {
	alegID := f.Session.Input.ALegID
	if alegID == "" {
		alegID = f.Identity.RequestID
	}
	return WireConversationObserverFacts{
		ALegID:    alegID,
		TraceID:   f.Identity.TraceID,
		Stage:     stage,
		Revision:  revision,
		RuleCount: ruleCount,
	}
}

// ToContinuationLineage projects bounded continuation lineage facts.
func (f WireTurnFacts) ToContinuationLineage() WireContinuationLineage {
	traj := f.Identity.RequestID
	if traj == "" {
		traj = f.Identity.TraceID
	}
	return WireContinuationLineage{
		ParentRef:     "",
		TrajectoryRef: traj,
		ProgressRef:   traj,
		Attempt:       0,
	}
}

// ToCompactionMeta projects bounded compaction preservation metadata.
func (f WireTurnFacts) ToCompactionMeta(blegID string, attemptSeq int, transactionID, ruleID string) WireCompactionMeta {
	alegID := f.Session.Input.ALegID
	if alegID == "" {
		alegID = f.Identity.RequestID
	}
	sessID := f.Session.Input.AuthoritativeSessionID
	if sessID == "" {
		sessID = f.Identity.RequestID
	}
	return WireCompactionMeta{
		TraceID:       f.Identity.TraceID,
		SessionID:     sessID,
		ALegID:        alegID,
		BLegID:        blegID,
		AttemptSeq:    attemptSeq,
		TransactionID: transactionID,
		RuleID:        ruleID,
	}
}

// ToDependencyFacts constructs a baseline WireDependencyFacts from WireTurnFacts,
// populated with clean (non-blocking) metadata-only views.
func (f WireTurnFacts) ToDependencyFacts() WireDependencyFacts {
	return WireDependencyFacts{
		ReceiveView:             f.ToReceiveView(),
		TerminalFacts:           f.ToTerminalFacts(),
		HasConversationReader:   false,
		HasConversationTagger:   false,
		HasSteeringWriter:       false,
		ActiveSteeringOverlays:  0,
		ActiveNeverBackendRules: 0,
		HasContinuationIntent:   false,
		HasParentContinuation:   false,
		ContinuationLineage:     f.ToContinuationLineage(),
		HasInterleavedProcessor: false,
		InterleavedActive:       false,
		HasCompactionDetector:   false,
		HasCompactionPreserver:  false,
		CompactionMeta:          f.ToCompactionMeta("", 0, "", ""),
		TerminalDecisionActive:  false,
	}
}

// ConservativeDependencyAssessmentGate evaluates authority eligibility combining
// static census validation with per-request dependency assessment
// (Requirements 13, 19; Task 12.3).
type ConservativeDependencyAssessmentGate struct {
	AuthorityGate AuthorityAssessmentGate
}

// NewConservativeDependencyAssessmentGate constructs a ConservativeDependencyAssessmentGate.
func NewConservativeDependencyAssessmentGate(summary WireEligibilitySummary, census DependencyCensus, generationID string) *ConservativeDependencyAssessmentGate {
	return &ConservativeDependencyAssessmentGate{
		AuthorityGate: *NewAuthorityAssessmentGate(summary, census, generationID),
	}
}

// Evaluate evaluates both static authority census and per-request dependency facts.
// Content/trajectory uses cause decline with DeclineReasonAuthorityBlocker.
// Metadata-only uses and response-only uses accept.
func (g *ConservativeDependencyAssessmentGate) Evaluate(facts WireDependencyFacts) (AssessmentDecision, DeclineReason) {
	// 1. Static authority gate evaluation (planes, hooks, ports, generation pin).
	decision, reason := g.AuthorityGate.Evaluate()
	if decision == AssessmentDecisionDecline {
		return decision, reason
	}

	// 2. Conversation content/trajectory dependencies:
	// Active reader, tagger, steering writer, overlays, or neverBackend rules block.
	if facts.HasConversationReader ||
		facts.HasConversationTagger ||
		facts.HasSteeringWriter ||
		facts.ActiveSteeringOverlays > 0 ||
		facts.ActiveNeverBackendRules > 0 {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}

	// 3. Continuation content/trajectory dependencies:
	// Active continuation intent or parent continuation resolution blocks.
	if facts.HasContinuationIntent || facts.HasParentContinuation {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}

	// 4. Interleaved thinking content/trajectory dependencies:
	// Active processor or interleaved mode blocks.
	if facts.HasInterleavedProcessor || facts.InterleavedActive {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}

	// 5. Compaction content/trajectory dependencies:
	// Request-leg detector or preserver blocks.
	if facts.HasCompactionDetector || facts.HasCompactionPreserver {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}

	// 6. Terminal decision content/trajectory dependencies:
	// Active terminal decision provider blocks unless source-backed contract exists.
	if facts.TerminalDecisionActive {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}

	return AssessmentDecisionAccept, DeclineReasonNone
}
