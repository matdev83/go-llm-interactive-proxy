package routing

import (
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

var (
	ErrContinuationCandidatePinned = errors.New("routing: continuation candidate is pinned")
	ErrContinuationNativeMismatch  = errors.New("routing: continuation native lineage mismatch")
)

// ContinuationConstraint is the routing projection of stored continuation
// lineage. Portable records leave BackendID/Model empty and may reroute.
type ContinuationConstraint struct {
	Lineage            lipcont.Lineage
	Requirements       lipapi.ProtocolRequirements
	NativeRequirements []lipcont.NativeRequirement
}

// CheckContinuationCandidate rejects a candidate that cannot safely replay a
// provider-bound record. Canonical requirements remain available to the normal
// admission check; this check only enforces exact lineage/pinning.
func CheckContinuationCandidate(constraint ContinuationConstraint, candidate AttemptCandidate) error {
	if !constraint.Lineage.ProviderBound && len(constraint.NativeRequirements) == 0 {
		return nil
	}
	if backend := strings.TrimSpace(constraint.Lineage.ProviderID); backend != "" && backend != strings.TrimSpace(candidate.Primary.Backend) {
		return fmt.Errorf("%w: backend %q", ErrContinuationCandidatePinned, backend)
	}
	if model := strings.TrimSpace(constraint.Lineage.Model); model != "" && model != strings.TrimSpace(candidate.Primary.Model) {
		return fmt.Errorf("%w: model %q", ErrContinuationCandidatePinned, model)
	}
	if key := strings.TrimSpace(constraint.Lineage.CandidateKey); key != "" && key != strings.TrimSpace(candidate.Key) {
		return fmt.Errorf("%w: candidate %q", ErrContinuationCandidatePinned, key)
	}
	for _, req := range constraint.NativeRequirements {
		if strings.TrimSpace(req.BackendID) != strings.TrimSpace(candidate.Primary.Backend) || strings.TrimSpace(req.Model) != strings.TrimSpace(candidate.Primary.Model) {
			return fmt.Errorf("%w: %s", ErrContinuationNativeMismatch, req.Kind)
		}
	}
	return nil
}

// CandidateAdmissionCheck evaluates whether one planned candidate can represent the call before upstream work.
type CandidateAdmissionCheck struct {
	Call              lipapi.Call
	Invocation        lipapi.Invocation
	Candidate         AttemptCandidate
	BackendCaps       lipapi.BackendCaps
	TransportCaps     lipapi.BackendTransportCaps
	ReplaySupport     lipapi.ReasoningReplaySupport
	DialectSupport    lipapi.DialectSupport
	ProjectionTarget  lipapi.LegacyProjectionTarget
	TransportPolicy   lipapi.TransportFallbackPolicy
	RequireProjection bool
	Continuation      *ContinuationConstraint
}

// Evaluate runs the canonical admission sequence for one candidate.
func (c CandidateAdmissionCheck) Evaluate() lipapi.CandidateAdmissionResult {
	if c.Continuation != nil {
		if err := CheckContinuationCandidate(*c.Continuation, c.Candidate); err != nil {
			return lipapi.CandidateAdmissionResult{Kind: lipapi.NegotiationReject, ProjectionError: err}
		}
	}
	target := c.ProjectionTarget
	target.Caps = c.BackendCaps
	target.ReplaySupport = c.ReplaySupport
	policy := c.TransportPolicy
	if policy == "" {
		policy = lipapi.TransportFallbackCompatibility
	}
	var frozen *lipapi.ProtocolRequirements
	if c.Continuation != nil && len(c.Continuation.Requirements.Capabilities)+len(c.Continuation.Requirements.ItemDialects)+len(c.Continuation.Requirements.ReasoningDialects)+len(c.Continuation.Requirements.CompactionDialects)+len(c.Continuation.Requirements.ExtensionTypes) > 0 {
		reqs := lipapi.NormalizeProtocolRequirements(c.Continuation.Requirements)
		frozen = &reqs
	}
	return lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call:               c.Call,
		Invocation:         c.Invocation,
		BackendCaps:        c.BackendCaps,
		TransportCaps:      c.TransportCaps,
		TransportPolicy:    policy,
		ReplaySupport:      c.ReplaySupport,
		DialectSupport:     c.DialectSupport,
		ProjectionTarget:   target,
		FrozenRequirements: frozen,
	})
}
