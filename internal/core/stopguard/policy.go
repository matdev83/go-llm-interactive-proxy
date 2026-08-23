package stopguard

import "strings"

// NormalizeVerdict applies the fixed v1 safety invariants to a raw verifier
// outcome. Any error, unknown/empty verdict kind, or CONTINUE without a
// concrete remaining objective normalizes to VerdictUncertain, which callers
// must treat as an allowed stop. Propagated text is bounded.
func NormalizeVerdict(v Verdict, err error) Verdict {
	if err != nil {
		return Verdict{Kind: VerdictUncertain}
	}
	switch v.Kind {
	case VerdictContinue:
		objective := strings.TrimSpace(v.RemainingObjective)
		if objective == "" {
			return Verdict{Kind: VerdictUncertain}
		}
		return Verdict{
			Kind:               VerdictContinue,
			Reason:             boundText(v.Reason, MaxReasonBytes),
			RemainingObjective: boundText(objective, MaxRemainingObjectiveBytes),
		}
	case VerdictAllowStop, VerdictNeedsUser, VerdictBlocked:
		return Verdict{
			Kind:   v.Kind,
			Reason: boundText(v.Reason, MaxReasonBytes),
		}
	case VerdictUncertain:
		return Verdict{Kind: VerdictUncertain}
	default:
		return Verdict{Kind: VerdictUncertain}
	}
}

// Decide maps a runtime-classified candidate to an immediate action or a
// verifier request. Non-semantic control outcomes are never reinterpreted as
// unfinished work; pre-output transport failures delegate to the existing
// recovery policy; post-output interruptions continue only from proven-safe
// canonical state and otherwise surface one controlled final outcome.
func Decide(c Candidate, policy ExplicitCompletionPolicy) Decision {
	switch c.Cause {
	case CauseClientCancel, CauseRefusalOrFilter, CauseUnknownTerminal, CauseOutputLimit:
		return Decision{Action: ActionForwardTerminal}
	case CauseTransportEOFPreCommit, CauseIdlePreCommit:
		if c.OutputCommitted {
			return Decision{Action: ActionSurfaceFailure}
		}
		return Decision{Action: ActionDelegatePreOutputRecovery}
	case CauseEmptyNormalEnd:
		if !c.OutputCommitted && c.EmptyRetryEligible {
			return Decision{Action: ActionDelegatePreOutputRecovery}
		}
	case CauseProviderPause:
		if c.SafeNativeResume {
			return Decision{Action: ActionContinueLeg}
		}
		return Decision{Action: ActionForwardTerminal}
	case CausePartialToolCall:
		if c.SafeNativeResume {
			return Decision{Action: ActionContinueLeg}
		}
		return Decision{Action: ActionSurfaceFailure}
	case CauseTransportEOFPostCommit, CauseIdlePostCommit:
		if c.SafeCanonicalContinuation {
			return Decision{Action: ActionContinueLeg}
		}
		return Decision{Action: ActionSurfaceFailure}
	case CauseNormalEnd:
	default:
		return Decision{Action: ActionForwardTerminal}
	}

	if c.ExplicitCompletion && policy == PolicyTrust {
		return Decision{Action: ActionForwardTerminal}
	}
	return Decision{Verify: true}
}

// DecideWithVerdict resolves a verifier-backed clean-stop candidate. Only a
// normalized actionable CONTINUE authorizes continuation; every other verdict
// releases exactly one final terminal.
func DecideWithVerdict(c Candidate, v Verdict, err error) Action {
	normalized := NormalizeVerdict(v, err)
	if normalized.Kind == VerdictContinue {
		return ActionContinueLeg
	}
	return ActionForwardTerminal
}

func boundText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut
}
