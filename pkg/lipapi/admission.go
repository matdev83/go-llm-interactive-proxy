package lipapi

// CandidateAdmissionInput carries everything required to reject incompatible candidates before upstream work.
type CandidateAdmissionInput struct {
	Call              Call
	Invocation        Invocation
	BackendCaps       BackendCaps
	TransportCaps     BackendTransportCaps
	TransportPolicy   TransportFallbackPolicy
	ReplaySupport     ReasoningReplaySupport
	DialectSupport    DialectSupport
	ProjectionTarget  LegacyProjectionTarget
	RequireProjection bool
	// FrozenRequirements, when set, is the immutable baseline requirement set every
	// candidate must satisfy; transforms must not weaken it during failover.
	FrozenRequirements *ProtocolRequirements
}

// CandidateAdmissionResult is the deterministic admission outcome for one candidate.
type CandidateAdmissionResult struct {
	Kind            NegotiationKind
	Capability      NegotiationResult
	Transport       TransportNegotiationResult
	Requirements    RequirementsMatchResult
	ProjectionError error
}

// AdmitCandidate evaluates operation/transport, semantic capability, exact dialect, and projector feasibility.
func AdmitCandidate(in CandidateAdmissionInput) CandidateAdmissionResult {
	out := CandidateAdmissionResult{}

	out.Transport = NegotiateTransport(in.Invocation, in.TransportCaps, in.TransportPolicy)
	if out.Transport.Kind == NegotiationReject {
		out.Kind = NegotiationReject
		return out
	}

	target := in.ProjectionTarget
	target.Caps = in.BackendCaps
	target.ReplaySupport = in.ReplaySupport

	var req ProtocolRequirements
	if in.FrozenRequirements != nil {
		frozen := NormalizeProtocolRequirements(*in.FrozenRequirements)
		current, curErr := DeriveCandidateRequirements(in.Call, in.BackendCaps, target)
		if curErr != nil {
			out.ProjectionError = curErr
			out.Kind = NegotiationReject
			return out
		}
		req = UnionProtocolRequirements(frozen, current)
		out.Capability = Negotiate(admissionCapabilityRequirements(in.Call, in.BackendCaps, target, req), in.BackendCaps)
		if out.Capability.Kind == NegotiationReject {
			out.Kind = NegotiationReject
			return out
		}
	} else {
		candReq, candErr := DeriveCandidateRequirements(in.Call, in.BackendCaps, target)
		if candErr != nil {
			out.ProjectionError = candErr
			out.Kind = NegotiationReject
			return out
		}
		out.Capability = Negotiate(admissionCapabilityRequirements(in.Call, in.BackendCaps, target, candReq), in.BackendCaps)
		if out.Capability.Kind == NegotiationReject {
			out.Kind = NegotiationReject
			return out
		}
		req = candReq
	}
	req.Capabilities = nil // semantic capabilities were evaluated above; requirements match dialects/extensions only.
	supported := ProtocolRequirements{Capabilities: capabilitySlice(in.BackendCaps)}
	supported.ItemDialects = append([]DialectRequirement(nil), in.DialectSupport.ItemDialects...)
	supported.ReasoningDialects = append([]DialectRequirement(nil), in.DialectSupport.ReasoningDialects...)
	supported.CompactionDialects = append([]DialectRequirement(nil), in.DialectSupport.CompactionDialects...)
	supported.ExtensionTypes = append([]ExtensionRequirement(nil), in.DialectSupport.ExtensionTypes...)
	out.Requirements = MatchRequirements(req, supported, in.ReplaySupport)
	if out.Requirements.Kind == NegotiationReject {
		out.Kind = NegotiationReject
		return out
	}

	if RequiresProjectionAdaptation(in.Call, in.BackendCaps) {
		if err := CheckProjectionFeasibility(in.Call, target); err != nil {
			out.ProjectionError = err
			out.Kind = NegotiationReject
			return out
		}
	}

	if out.Capability.Kind == NegotiationDowngrade {
		out.Kind = NegotiationDowngrade
		return out
	}
	out.Kind = NegotiationLossless
	return out
}

// Err returns the first hard reject error, if any.
func (r CandidateAdmissionResult) Err() error {
	if r.Kind != NegotiationReject {
		return nil
	}
	if r.ProjectionError != nil {
		return r.ProjectionError
	}
	if err := r.Requirements.Err(); err != nil {
		return err
	}
	if err := r.Transport.Err(); err != nil {
		return err
	}
	return r.Capability.Err()
}

func capabilitySlice(caps BackendCaps) []Capability {
	if len(caps) == 0 {
		return nil
	}
	out := make([]Capability, 0, len(caps))
	for c := range caps {
		out = append(out, c)
	}
	return out
}
