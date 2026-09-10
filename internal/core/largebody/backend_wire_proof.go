package largebody

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

// BackendWireProofGate composes exact initial-route candidate proof and late-route
// domain proof (route override and late selector envelopes), verifying pure backend
// wire support across all candidates and domain members (Task 11.7; Requirements 7, 8, 9, 21).
//
// Invariants (Requirements 7.2, 7.3, 7.4, 7.5, 8.1, 8.2, 8.3, 8.4, 9.1, 9.6, 21.5):
// - Pass immutable body mode and rewrite facts to every resolver call (both ResolveWireRequest and ResolveWireDomain).
// - Any candidate or domain member incompatibility declines the entire request (no pruning).
// - Homogeneous same-wire domain accepts; heterogeneous incompatible domain declines.
// - Proves whether a post-BeginTurn route override change falls strictly inside the accepted domain.
// - Pure and side-effect-free: strictly zero live store reads, zero provider network I/O.
type BackendWireProofGate struct {
	InitialGate      *InitialRouteAssessmentGate
	OverrideGate     *RouteOverrideAssessmentGate
	LateSelectorGate *LateSelectorAssessmentGate
	BackendResolver  WireBackendResolver
}

// NewBackendWireProofGate constructs a BackendWireProofGate.
func NewBackendWireProofGate(
	initialGate *InitialRouteAssessmentGate,
	overrideGate *RouteOverrideAssessmentGate,
	lateSelectorGate *LateSelectorAssessmentGate,
	backendResolver WireBackendResolver,
) *BackendWireProofGate {
	return &BackendWireProofGate{
		InitialGate:      initialGate,
		OverrideGate:     overrideGate,
		LateSelectorGate: lateSelectorGate,
		BackendResolver:  backendResolver,
	}
}

func (g *BackendWireProofGate) resolveBackend(backendID string) (WireBackend, bool) {
	if g.BackendResolver != nil {
		if wb, ok := g.BackendResolver.ResolveWireBackend(backendID); ok && wb != nil {
			return wb, true
		}
	}
	if g.InitialGate != nil && g.InitialGate.BackendResolver != nil {
		if wb, ok := g.InitialGate.BackendResolver.ResolveWireBackend(backendID); ok && wb != nil {
			return wb, true
		}
	}
	if g.OverrideGate != nil && g.OverrideGate.BackendResolver != nil {
		if wb, ok := g.OverrideGate.BackendResolver.ResolveWireBackend(backendID); ok && wb != nil {
			return wb, true
		}
	}
	if g.LateSelectorGate != nil && g.LateSelectorGate.BackendResolver != nil {
		if wb, ok := g.LateSelectorGate.BackendResolver.ResolveWireBackend(backendID); ok && wb != nil {
			return wb, true
		}
	}
	return nil, false
}

// Evaluate evaluates exact initial candidate wire proof and late route domain wire proof.
// Threads immutable body/rewrite facts to every resolver call.
// Any candidate or domain member incompatibility declines the entire request.
func (g *BackendWireProofGate) Evaluate(
	ctx context.Context,
	proof Proof,
) (AssessmentDecision, DeclineReason, WireRequestFacts, WireDomainFacts, []routing.AttemptCandidate) {
	if ctx != nil && ctx.Err() != nil {
		return AssessmentDecisionDecline, DeclineReasonCanceled, WireRequestFacts{}, WireDomainFacts{}, nil
	}
	if strings.TrimSpace(proof.ProfileID) == "" {
		return AssessmentDecisionDecline, DeclineReasonProofUncertain, WireRequestFacts{}, WireDomainFacts{}, nil
	}

	var wireReq WireRequestFacts
	var cands []routing.AttemptCandidate

	// 1. Exact initial candidate set wire proof (if configured)
	if g.InitialGate != nil {
		decision, reason, initialCands := g.InitialGate.Evaluate(ctx, proof)
		if decision == AssessmentDecisionDecline {
			return AssessmentDecisionDecline, reason, WireRequestFacts{}, WireDomainFacts{}, nil
		}
		cands = initialCands
		if len(cands) > 0 {
			candModel := cands[0].Primary.WireModel()
			wireReq = WireRequestFacts{
				ProfileID:       proof.ProfileID,
				Operation:       proof.Operation,
				Delivery:        proof.Delivery,
				BodyMode:        proof.Mode,
				Rewrite:         proof.Rewrite,
				ClientModel:     proof.ClientModel,
				CandidateModel:  candModel,
				MaxOutputTokens: proof.MaxOutputTokens,
			}
		}
	}

	// 2. Late route-override compatibility envelope (if configured)
	var domainFacts WireDomainFacts
	if g.OverrideGate != nil {
		ovDecision, ovReason, ovDomain := g.OverrideGate.Evaluate(ctx, proof)
		if ovDecision == AssessmentDecisionDecline {
			return AssessmentDecisionDecline, ovReason, WireRequestFacts{}, WireDomainFacts{}, nil
		}
		domainFacts = ovDomain
	}

	// 3. Other late selector authorities evaluation (if configured)
	if g.LateSelectorGate != nil {
		lsDecision, lsReason, lsDomain := g.LateSelectorGate.Evaluate(ctx, proof)
		if lsDecision == AssessmentDecisionDecline {
			return AssessmentDecisionDecline, lsReason, WireRequestFacts{}, WireDomainFacts{}, nil
		}
		unioned, unionReason, ok := unionWireDomainFacts(ctx, domainFacts, lsDomain)
		if !ok {
			return AssessmentDecisionDecline, unionReason, WireRequestFacts{}, WireDomainFacts{}, nil
		}
		domainFacts = unioned
	}

	return AssessmentDecisionAccept, DeclineReasonNone, wireReq, domainFacts, cands
}

// LegalDomainBackends returns the sorted union of all backend IDs that can legally be selected
// by post-commit route authorities under the gate's configured generation and late authorities.
func (g *BackendWireProofGate) LegalDomainBackends() []string {
	if g == nil {
		return nil
	}
	backendMap := make(map[string]struct{})
	if g.OverrideGate != nil {
		for _, be := range g.OverrideGate.CandidateBackends() {
			backendMap[be] = struct{}{}
		}
	}
	if g.LateSelectorGate != nil {
		for _, be := range g.LateSelectorGate.CandidateBackends() {
			backendMap[be] = struct{}{}
		}
	}
	if len(backendMap) == 0 {
		return nil
	}
	result := make([]string, 0, len(backendMap))
	for be := range backendMap {
		result = append(result, be)
	}
	sort.Strings(result)
	return result
}

// VerifyOverrideCandidate proves whether a candidate produced post-BeginTurn by a live
// route override falls strictly inside the accepted domain envelope and is wire-compatible
// without requiring canonical fallback (Task 11.7; Requirements 7.4, 8.3).
func (g *BackendWireProofGate) VerifyOverrideCandidate(
	ctx context.Context,
	acceptedDomain WireDomainFacts,
	cand routing.AttemptCandidate,
) (bool, DeclineReason) {
	if ctx != nil && ctx.Err() != nil {
		return false, DeclineReasonCanceled
	}
	backendID := strings.TrimSpace(cand.Primary.Backend)
	if backendID == "" {
		return false, DeclineReasonRouteIncompatible
	}

	// 1. Verify backend membership in the legal domain backends
	legalBackends := g.LegalDomainBackends()
	if legalBackends != nil {
		if !containsString(legalBackends, backendID) {
			return false, DeclineReasonRouteIncompatible
		}
	}

	// 2. Verify model membership against the accepted domain envelope
	candModel := cand.Primary.WireModel()
	if !acceptedDomain.UniversalModel {
		if !containsString(acceptedDomain.CandidateModels, candModel) {
			return false, DeclineReasonRouteIncompatible
		}
	}

	// 3. Resolve wire backend capability and verify pure wire compatibility
	wb, ok := g.resolveBackend(backendID)
	if !ok || wb == nil {
		return false, DeclineReasonBackendIncompatible
	}

	candFacts := WireRequestFacts{
		ProfileID:       acceptedDomain.ProfileID,
		Operation:       acceptedDomain.Operation,
		Delivery:        acceptedDomain.Delivery,
		BodyMode:        acceptedDomain.BodyMode,
		Rewrite:         acceptedDomain.Rewrite,
		ClientModel:     candModel,
		CandidateModel:  candModel,
		MaxOutputTokens: 0,
	}

	backendCand := routing.BackendFacingCandidate(cand)
	support := wb.ResolveWireRequest(ctx, candFacts, backendCand)
	if !support.Compatible {
		return false, DeclineReasonBackendIncompatible
	}

	if support.NeedsModelRewrite && !acceptedDomain.Rewrite.NeedsModelRewrite() {
		return false, DeclineReasonRewriteUnsupported
	}

	return true, DeclineReasonNone
}

// ComposeOverrideCandidate composes the candidate set for an override selector under
// the active generation's routing rules and verifies that every resulting candidate is
// wire-compatible inside the accepted domain (Requirements 7.4, 8.3).
func (g *BackendWireProofGate) ComposeOverrideCandidate(
	ctx context.Context,
	overrideSelector string,
	acceptedDomain WireDomainFacts,
) ([]routing.AttemptCandidate, bool, DeclineReason) {
	if ctx != nil && ctx.Err() != nil {
		return nil, false, DeclineReasonCanceled
	}
	overrideSelector = strings.TrimSpace(overrideSelector)
	if overrideSelector == "" {
		return nil, false, DeclineReasonRouteIncompatible
	}

	var aliases *routing.AliasResolver
	var defaultBackend string
	var execResolver routing.BackendExecutionResolver
	var policy config.ExecutionCompositionPolicy
	var nativeResolver routing.NativeModelResolver

	if g.InitialGate != nil {
		aliases = g.InitialGate.Aliases
		defaultBackend = g.InitialGate.DefaultBackend
		execResolver = g.InitialGate.BackendExecutionResolver
		policy = g.InitialGate.ExecutionCompositionPolicy
		nativeResolver = g.InitialGate.NativeModelResolver
	} else if g.OverrideGate != nil && g.OverrideGate.GenerationValidator != nil {
		aliases = g.OverrideGate.GenerationValidator.Aliases
		defaultBackend = g.OverrideGate.GenerationValidator.DefaultBackend
		execResolver = g.OverrideGate.GenerationValidator.BackendExecutionResolver
		policy = g.OverrideGate.GenerationValidator.ExecutionCompositionPolicy
	}

	cands, sel, err := routing.ComposeInitialCandidates(
		overrideSelector,
		aliases,
		defaultBackend,
		execResolver,
		policy,
		nativeResolver,
	)
	if err != nil || sel == nil || len(cands) == 0 {
		return nil, false, DeclineReasonRouteIncompatible
	}

	for _, cand := range cands {
		ok, reason := g.VerifyOverrideCandidate(ctx, acceptedDomain, cand)
		if !ok {
			return nil, false, reason
		}
	}

	return cands, true, DeclineReasonNone
}

// BackendWireProofAssessor implements LargeBodyAssessor using BackendWireProofGate
// (Task 11.7; Requirements 7, 8, 9, 21).
type BackendWireProofAssessor struct {
	Gate          BackendWireProofGate
	AcceptStamp   AssessmentStamp
	AcceptWireReq WireRequestFacts
	AcceptDomain  WireDomainFacts
}

// NewBackendWireProofAssessor constructs a BackendWireProofAssessor.
func NewBackendWireProofAssessor(gate BackendWireProofGate) *BackendWireProofAssessor {
	return &BackendWireProofAssessor{
		Gate: gate,
	}
}

// AssessLargeBody evaluates proof across exact candidate and domain backend proof gates.
func (a *BackendWireProofAssessor) AssessLargeBody(ctx context.Context, proof Proof) (Assessment, error) {
	if proof.ProfileID == "" {
		return NewDeclinedAssessment(DeclineReasonProofUncertain)
	}

	decision, reason, wireReq, wireDomain, _ := a.Gate.Evaluate(ctx, proof)
	if decision == AssessmentDecisionDecline {
		return NewDeclinedAssessment(reason)
	}

	if a.AcceptStamp.IsZero() {
		return NewDeclinedAssessment(DeclineReasonProofUncertain)
	}

	if a.AcceptWireReq.ProfileID != "" {
		wireReq = a.AcceptWireReq
	}
	if a.AcceptDomain.ProfileID != "" {
		wireDomain = a.AcceptDomain
	}

	accepted, err := NewAcceptedAssessment(a.AcceptStamp, wireReq, wireDomain)
	if err != nil {
		return Assessment{}, fmt.Errorf("largebody: accepted assessment construction failed: %w", err)
	}
	return accepted, nil
}

// VerifyOverrideCandidate delegates candidate verification to the underlying gate.
func (a *BackendWireProofAssessor) VerifyOverrideCandidate(
	ctx context.Context,
	acceptedDomain WireDomainFacts,
	cand routing.AttemptCandidate,
) (bool, DeclineReason) {
	return a.Gate.VerifyOverrideCandidate(ctx, acceptedDomain, cand)
}

// ComposeOverrideCandidate delegates candidate composition and verification to the underlying gate.
func (a *BackendWireProofAssessor) ComposeOverrideCandidate(
	ctx context.Context,
	overrideSelector string,
	acceptedDomain WireDomainFacts,
) ([]routing.AttemptCandidate, bool, DeclineReason) {
	return a.Gate.ComposeOverrideCandidate(ctx, overrideSelector, acceptedDomain)
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
