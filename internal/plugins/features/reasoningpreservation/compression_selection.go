package reasoningpreservation

import (
	"context"
	"crypto/sha256"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// ReasoningViewResult is the immutable stage result for 6.1.
// It is content-free and carries the surrogate eligibility before any substitution (6.2).
type ReasoningViewResult struct {
	Kind   ViewKind
	Reason string
}

// ReasoningViewStage is the immutable selection stage seam for 6.1.
// It is policy-aware, has no cross-attempt state, and is injected via constructor.
// Stage evaluates current EgressPolicy once per artifact with trusted control-plane
// input (Route/Purpose/Source/Principal) and threads the prepared hash/sanitization
// into the pure SelectReasoningView. 6.2 may consume the local map or call pure again.
type ReasoningViewStage func(ctx context.Context, cfg CompressionConfig, cs CompressionStore, svc CompressionServices, partition SessionPartition, candidates []restoreCandidate, support lipapi.ReasoningReplaySupport, meta request.AttemptMeta) map[string]ReasoningViewResult

// selectReasoningViews is the private helper that implements the stage logic.
// It iterates restorable candidates, fetches the attached surrogate, evaluates
// current EgressPolicy exactly once per artifact (bounded, no model content),
// and runs the pure SelectReasoningViewWithCurrentPolicy. No mutation, no goroutines.
func selectReasoningViews(ctx context.Context, cfg CompressionConfig, cs CompressionStore, svc CompressionServices, partition SessionPartition, candidates []restoreCandidate, support lipapi.ReasoningReplaySupport, meta request.AttemptMeta) map[string]ReasoningViewResult {
	if cs == nil || len(candidates) == 0 {
		return nil
	}
	out := make(map[string]ReasoningViewResult, len(candidates))
	for _, c := range candidates {
		if c.Unsupported {
			continue
		}
		st, ok, err := cs.GetCompressionState(ctx, partition, c.ArtifactID)
		if err != nil || !ok || st.Surrogate == nil {
			continue
		}
		// Policy-aware current check without model content and bounded latency.
		// One synchronous Decide per artifact with trusted control-plane input.
		curHash, curSan, curAction, curVersion, curErr := currentPolicyForSelection(ctx, cfg, svc, meta)
		if curErr != nil || curVersion == "" || curAction == EgressDeny {
			out[c.ArtifactID] = ReasoningViewResult{Kind: ViewOriginal, Reason: "policy_no_longer_permits"}
			continue
		}
		// Prepare current hash/sanitization for pure check.
		// Only Allow/Redact are considered; Deny already handled.
		if curAction != EgressAllow && curAction != EgressRedactThenAllow {
			out[c.ArtifactID] = ReasoningViewResult{Kind: ViewOriginal, Reason: "policy_no_longer_permits"}
			continue
		}
		kind, reason := SelectReasoningViewWithCurrentPolicy(cfg, c.Artifact, st.Surrogate, support, c.Classification, &curHash, &curSan)
		out[c.ArtifactID] = ReasoningViewResult{Kind: kind, Reason: reason}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// currentPolicyForSelection evaluates the current EgressPolicy exactly once per
// selection with trusted control-plane input. Scope is taken from meta.Scope
// if present, otherwise from the trusted context via scope.ScopeFromContext.
// No model content is inspected; call is bounded synchronous.
func currentPolicyForSelection(ctx context.Context, cfg CompressionConfig, svc CompressionServices, meta request.AttemptMeta) (hash [32]byte, sanitization string, action EgressAction, version string, err error) {
	if svc.EgressPolicy == nil {
		return [32]byte{}, "", EgressDeny, "", nil
	}
	// Resolve principal scope without model content.
	var principal scope.PrincipalScopeView
	if meta.Scope.PrincipalID.String() != "" || meta.Scope.SubjectKind != "" {
		principal = meta.Scope
	} else if v, ok := scope.ScopeFromContext(ctx); ok {
		principal = v
	}
	// Egress input is narrow, trusted control-plane only.
	in := CompressionEgressInput{
		Route:       cfg.Route,
		Purpose:     EgressPurposeReasoningSemanticCompression,
		SourceClass: EgressSourceClassSemanticText,
		Principal:   NewEgressPrincipalView(principal.PrincipalID.String()),
	}
	dec, decErr := svc.EgressPolicy.Decide(ctx, in)
	if decErr != nil {
		return [32]byte{}, "", EgressDeny, "", decErr
	}
	if dec.PolicyVersion == "" {
		return [32]byte{}, "", EgressDeny, "", nil
	}
	if dec.Action == EgressDeny {
		return [32]byte{}, "", EgressDeny, dec.PolicyVersion, nil
	}
	san := SanitizationNone
	if dec.Action == EgressRedactThenAllow {
		san = SanitizationRedacted
	}
	h := ComputeEgressPolicyHash(dec, cfg.Route)
	return h, san, dec.Action, dec.PolicyVersion, nil
}

// identityReasoningViewStage is the default stage that delegates to selectReasoningViews.
func identityReasoningViewStage(ctx context.Context, cfg CompressionConfig, cs CompressionStore, svc CompressionServices, partition SessionPartition, candidates []restoreCandidate, support lipapi.ReasoningReplaySupport, meta request.AttemptMeta) map[string]ReasoningViewResult {
	return selectReasoningViews(ctx, cfg, cs, svc, partition, candidates, support, meta)
}

// ReasoningViewConsumerStage is the immutable consumer seam for 6.1 decisions.
// Default identity does nothing (shadow); 6.2 injects ephemeral builder that
// would replace only validated semantic-text Reasoning.Text fields.
// It has no cross-attempt state; call is defensively cloned by consumer if needed.
type ReasoningViewConsumerStage func(ctx context.Context, call *lipapi.Call, decisions map[string]ReasoningViewResult) *lipapi.Call

func identityReasoningViewConsumerStage(_ context.Context, call *lipapi.Call, _ map[string]ReasoningViewResult) *lipapi.Call {
	return call
}

// ViewKind is the pure selection decision before any mutation.
// 6.1 may return Original/Surrogate + reason but must NOT substitute yet (6.2).
type ViewKind string

const (
	ViewOriginal  ViewKind = "original"
	ViewSurrogate ViewKind = "surrogate"
)

// SelectReasoningView is a pure eligibility function over the original artifact
// + surrogate + candidate ReplaySupport + client classification.
// It is a wrapper without current-policy check for backward compatibility.
func SelectReasoningView(cfg CompressionConfig, artifact TurnArtifact, surrogate *ReasoningSurrogate, support lipapi.ReasoningReplaySupport, classification Classification) (ViewKind, string) {
	return SelectReasoningViewWithCurrentPolicy(cfg, artifact, surrogate, support, classification, nil, nil)
}

// SelectReasoningViewWithCurrentPolicy is the policy-aware pure variant.
// If currentPolicyHash/currentSanitization are non-nil, they are the trusted
// current EgressPolicy result prepared by the stage (hash via ComputeEgressPolicyHash,
// sanitization via EgressAllow/Redact). When provided, they must match the
// stored surrogate's EgressPolicyHash and Sanitization, otherwise
// policy_no_longer_permits. No I/O inside pure; stage does the Decide call.
func SelectReasoningViewWithCurrentPolicy(cfg CompressionConfig, artifact TurnArtifact, surrogate *ReasoningSurrogate, support lipapi.ReasoningReplaySupport, classification Classification, currentPolicyHash *[32]byte, currentSanitization *string) (ViewKind, string) {
	if surrogate == nil {
		return ViewOriginal, "no_surrogate"
	}
	// Existing client reasoning precedence preserved.
	if classification == ClassPreserved {
		return ViewOriginal, "client_preserved"
	}
	if classification != ClassMissing {
		return ViewOriginal, "client_not_missing"
	}
	// Re-run canonical classifier on each original placement.
	semanticIndexes := make([]int, 0, len(artifact.Reasoning))
	exactOrUnknown := make(map[int]struct{}, len(artifact.Reasoning))
	semanticDialects := make([]lipapi.ReasoningDialect, 0, len(artifact.Reasoning))
	for i, pr := range artifact.Reasoning {
		sem := ClassifyReasoningPart(pr.Part)
		switch sem {
		case ReplaySemanticText:
			semanticIndexes = append(semanticIndexes, i)
			if pr.Part.Reasoning != nil {
				d := lipapi.NormalizeReasoningDialect(pr.Part.Reasoning.Dialect)
				semanticDialects = append(semanticDialects, d)
			}
		case ReplayExactRequired, ReplayUnknown:
			exactOrUnknown[i] = struct{}{}
		default:
			exactOrUnknown[i] = struct{}{}
		}
	}
	if len(semanticIndexes) == 0 {
		return ViewOriginal, "no_semantic_text"
	}
	// Destination must support original dialect via existing ReasoningReplaySupport/dialectSet.
	supported := dialectSet(support.Dialects)
	for _, d := range semanticDialects {
		if _, ok := supported[d]; !ok {
			return ViewOriginal, "unsupported_destination"
		}
	}
	// Surrogate correlation: exact original digest/semantic/policy/sanitization/route and expected indexes.
	if surrogate.OriginalDigest != artifact.Anchor {
		return ViewOriginal, "stale_original_digest"
	}
	if surrogate.SemanticDigest != computeSemanticDigest(artifact.Reasoning) {
		return ViewOriginal, "stale_semantic_digest"
	}
	if surrogate.PolicyRevision != cfg.EgressPolicyRef {
		return ViewOriginal, "stale_policy"
	}
	if !isValidSanitization(surrogate.Sanitization) {
		return ViewOriginal, "stale_sanitization"
	}
	var zeroHash [32]byte
	if surrogate.AuthorizedRouteHash == zeroHash {
		return ViewOriginal, "stale_route"
	}
	routeHash := sha256.Sum256([]byte(cfg.Route))
	if surrogate.AuthorizedRouteHash != routeHash {
		return ViewOriginal, "stale_route"
	}
	if surrogate.EgressPolicyHash == zeroHash {
		return ViewOriginal, "stale_egress_hash"
	}
	// EgressPolicyHash is the authoritative hash of decision PolicyVersion+route+purpose+source,
	// not sha256(EgressPolicyRef). Its correctness is enforced at attach time via
	// pending/surrogate correlation; selection only requires it be non-zero and that
	// PolicyRevision matches the current config ref (checked above).
	if currentPolicyHash != nil {
		if *currentPolicyHash != surrogate.EgressPolicyHash {
			return ViewOriginal, "policy_no_longer_permits"
		}
	}
	if currentSanitization != nil {
		if *currentSanitization != surrogate.Sanitization {
			return ViewOriginal, "policy_no_longer_permits"
		}
	}
	// Expected indexes: surrogate must exactly equal the current semantic subset.
	if len(surrogate.Segments) != len(semanticIndexes) {
		return ViewOriginal, "stale_indexes"
	}
	segSet := make(map[int]struct{}, len(surrogate.Segments))
	for _, seg := range surrogate.Segments {
		if _, isExact := exactOrUnknown[seg.PlacementIndex]; isExact {
			return ViewOriginal, "stale_indexes_exact"
		}
		if _, dup := segSet[seg.PlacementIndex]; dup {
			return ViewOriginal, "stale_indexes"
		}
		segSet[seg.PlacementIndex] = struct{}{}
	}
	semSet := make(map[int]struct{}, len(semanticIndexes))
	for _, idx := range semanticIndexes {
		semSet[idx] = struct{}{}
	}
	for k := range segSet {
		if _, ok := semSet[k]; !ok {
			return ViewOriginal, "stale_indexes"
		}
	}
	for k := range semSet {
		if _, ok := segSet[k]; !ok {
			return ViewOriginal, "stale_indexes"
		}
	}
	return ViewSurrogate, "eligible"
}
