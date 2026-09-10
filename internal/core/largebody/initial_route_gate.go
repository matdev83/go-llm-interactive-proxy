package largebody

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

// WireBackendResolver resolves a configured backend into its WireBackend capability
// for pure wire compatibility checks (Requirements 7, 8).
type WireBackendResolver interface {
	ResolveWireBackend(backendID string) (WireBackend, bool)
}

// WireBackendResolverFunc adapts a function into a WireBackendResolver.
type WireBackendResolverFunc func(backendID string) (WireBackend, bool)

func (f WireBackendResolverFunc) ResolveWireBackend(backendID string) (WireBackend, bool) {
	if f == nil {
		return nil, false
	}
	return f(backendID)
}

// WireBackendMap adapts a map into a WireBackendResolver.
type WireBackendMap map[string]WireBackend

func (m WireBackendMap) ResolveWireBackend(backendID string) (WireBackend, bool) {
	if m == nil {
		return nil, false
	}
	wb, ok := m[backendID]
	return wb, ok
}

// InitialRouteAssessmentGate evaluates whether the exact initial route candidate set
// derived under canonical rules is wire-compatible across all candidates (Task 11.4;
// Requirements 7, 8, 10).
//
// Invariants (Requirements 7.3, 8.3, 8.4, 10.3, 10.4):
// - Reuses current alias resolution, default backend, execution composition, and native model rules.
// - Preserves sequential/fallback, weighted, and parallel/race candidate order and membership exactly.
// - Proves backend wire support for every possible candidate; any incompatible candidate declines the whole request.
// - Core shall NOT prune or reorder candidates to force wire compatibility.
// - Pure and side-effect-free: zero provider network I/O, zero store reads/writes.
type InitialRouteAssessmentGate struct {
	Aliases                    *routing.AliasResolver
	DefaultBackend             string
	BackendExecutionResolver   routing.BackendExecutionResolver
	ExecutionCompositionPolicy config.ExecutionCompositionPolicy
	NativeModelResolver        routing.NativeModelResolver
	BackendResolver            WireBackendResolver
}

// NewInitialRouteAssessmentGate constructs an InitialRouteAssessmentGate.
func NewInitialRouteAssessmentGate(
	aliases *routing.AliasResolver,
	defaultBackend string,
	execResolver routing.BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
	nativeResolver routing.NativeModelResolver,
	backendResolver WireBackendResolver,
) *InitialRouteAssessmentGate {
	return &InitialRouteAssessmentGate{
		Aliases:                    aliases,
		DefaultBackend:             defaultBackend,
		BackendExecutionResolver:   execResolver,
		ExecutionCompositionPolicy: policy,
		NativeModelResolver:        nativeResolver,
		BackendResolver:            backendResolver,
	}
}

// Evaluate evaluates proof through canonical routing composition and verifies pure
// wire support across the entire initial candidate set.
// If any candidate is incompatible, the entire request declines (no pruning).
func (g *InitialRouteAssessmentGate) Evaluate(ctx context.Context, proof Proof) (AssessmentDecision, DeclineReason, []routing.AttemptCandidate) {
	if ctx != nil && ctx.Err() != nil {
		return AssessmentDecisionDecline, DeclineReasonCanceled, nil
	}
	if strings.TrimSpace(proof.ProfileID) == "" {
		return AssessmentDecisionDecline, DeclineReasonProofUncertain, nil
	}

	rawSelector := strings.TrimSpace(proof.RouteSelector)
	if rawSelector == "" {
		rawSelector = strings.TrimSpace(proof.ClientModel)
	}
	if rawSelector == "" {
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, nil
	}

	// 1. Canonical routing composition: alias, parse, default backend, execution composition, native binding
	cands, sel, err := routing.ComposeInitialCandidates(
		rawSelector,
		g.Aliases,
		g.DefaultBackend,
		g.BackendExecutionResolver,
		g.ExecutionCompositionPolicy,
		g.NativeModelResolver,
	)
	if err != nil || sel == nil || len(cands) == 0 {
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, nil
	}

	// 2. Candidate set verification (order and membership must match InitialCandidates)
	expectedCands := routing.InitialCandidates(sel)
	if !routing.CandidateSetsEqual(cands, expectedCands) {
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, nil
	}

	// 3. Prove wire support for every candidate in the set (NO PRUNING)
	for _, cand := range cands {
		backendID := strings.TrimSpace(cand.Primary.Backend)
		if backendID == "" {
			return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, nil
		}
		if g.BackendResolver == nil {
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, nil
		}
		wb, ok := g.BackendResolver.ResolveWireBackend(backendID)
		if !ok || wb == nil {
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, nil
		}

		candModel := cand.Primary.WireModel()
		candFacts := WireRequestFacts{
			ProfileID:       proof.ProfileID,
			Operation:       proof.Operation,
			Delivery:        proof.Delivery,
			BodyMode:        proof.Mode,
			Rewrite:         proof.Rewrite,
			ClientModel:     proof.ClientModel,
			CandidateModel:  candModel,
			MaxOutputTokens: proof.MaxOutputTokens,
		}

		backendCand := routing.BackendFacingCandidate(cand)
		support := wb.ResolveWireRequest(ctx, candFacts, backendCand)
		if !support.Compatible {
			// Any incompatible candidate declines the entire wire request (Requirement 8.4)
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, nil
		}

		if support.NeedsModelRewrite && !proof.Rewrite.NeedsModelRewrite() {
			return AssessmentDecisionDecline, DeclineReasonRewriteUnsupported, nil
		}
		if candModel != proof.ClientModel {
			if !support.NeedsModelRewrite || !proof.Rewrite.NeedsModelRewrite() {
				return AssessmentDecisionDecline, DeclineReasonRewriteUnsupported, nil
			}
		}
	}

	return AssessmentDecisionAccept, DeclineReasonNone, cands
}

// ProveCandidateSet verifies that expected matches the canonical composition output in exact order
// and membership, and that every candidate in that set is wire-compatible without pruning.
func (g *InitialRouteAssessmentGate) ProveCandidateSet(ctx context.Context, proof Proof, expected []routing.AttemptCandidate) (AssessmentDecision, DeclineReason) {
	decision, reason, cands := g.Evaluate(ctx, proof)
	if decision == AssessmentDecisionDecline {
		return decision, reason
	}
	if !routing.CandidateSetsEqual(cands, expected) {
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible
	}
	return AssessmentDecisionAccept, DeclineReasonNone
}

// InitialRouteAssessor implements LargeBodyAssessor using the Task 11.4 initial route gate
// (Requirements 7, 8, 10).
type InitialRouteAssessor struct {
	Gate          InitialRouteAssessmentGate
	AcceptStamp   AssessmentStamp
	AcceptWireReq WireRequestFacts
	AcceptDomain  WireDomainFacts
}

// NewInitialRouteAssessor constructs an InitialRouteAssessor.
func NewInitialRouteAssessor(gate InitialRouteAssessmentGate) *InitialRouteAssessor {
	return &InitialRouteAssessor{
		Gate: gate,
	}
}

// AssessLargeBody evaluates proof through the initial route assessment gate.
func (a *InitialRouteAssessor) AssessLargeBody(ctx context.Context, proof Proof) (Assessment, error) {
	if proof.ProfileID == "" {
		return NewDeclinedAssessment(DeclineReasonProofUncertain)
	}

	decision, reason, _ := a.Gate.Evaluate(ctx, proof)
	if decision == AssessmentDecisionDecline {
		return NewDeclinedAssessment(reason)
	}

	if a.AcceptStamp.IsZero() {
		return NewDeclinedAssessment(DeclineReasonProofUncertain)
	}

	accepted, err := NewAcceptedAssessment(a.AcceptStamp, a.AcceptWireReq, a.AcceptDomain)
	if err != nil {
		return Assessment{}, fmt.Errorf("largebody: accepted assessment construction failed: %w", err)
	}
	return accepted, nil
}
