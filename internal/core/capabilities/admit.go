package capabilities

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// CandidateFacts carries resolved backend facts used for pre-network candidate admission.
type CandidateFacts struct {
	Caps               lipapi.BackendCaps
	TransportCaps      lipapi.BackendTransportCaps
	ReplaySupport      lipapi.ReasoningReplaySupport
	DialectSupport     lipapi.DialectSupport
	ProjectionTarget   lipapi.LegacyProjectionTarget
	FrozenRequirements *lipapi.ProtocolRequirements
}

// AdmitCandidate evaluates one candidate against call requirements before upstream work.
func AdmitCandidate(ctx context.Context, call lipapi.Call, inv lipapi.Invocation, cand routing.AttemptCandidate, facts CandidateFacts) lipapi.CandidateAdmissionResult {
	_ = ctx
	_ = cand
	target := facts.ProjectionTarget
	target.Caps = facts.Caps
	target.ReplaySupport = facts.ReplaySupport
	return lipapi.AdmitCandidate(lipapi.CandidateAdmissionInput{
		Call:               call,
		Invocation:         inv,
		BackendCaps:        facts.Caps,
		TransportCaps:      facts.TransportCaps,
		TransportPolicy:    lipapi.TransportFallbackCompatibility,
		ReplaySupport:      facts.ReplaySupport,
		DialectSupport:     facts.DialectSupport,
		ProjectionTarget:   target,
		FrozenRequirements: facts.FrozenRequirements,
	})
}

// FailoverRequirementSet retains the complete requirement set that every failover candidate must satisfy.
type FailoverRequirementSet struct {
	Required lipapi.ProtocolRequirements
}

// NewFailoverRequirementSet derives the requirement set from the baseline call.
func NewFailoverRequirementSet(call lipapi.Call) FailoverRequirementSet {
	return FailoverRequirementSet{Required: lipapi.DeriveProtocolRequirements(call)}
}

// CandidateMatchesFailoverRequirements reports whether candidate support satisfies the baseline requirement set.
func (f FailoverRequirementSet) CandidateMatchesFailoverRequirements(supported lipapi.ProtocolRequirements, replay lipapi.ReasoningReplaySupport) bool {
	res := lipapi.MatchRequirements(f.Required, supported, replay)
	return res.Kind != lipapi.NegotiationReject
}
