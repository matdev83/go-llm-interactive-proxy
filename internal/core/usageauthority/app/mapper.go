package app

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// Bounded defaults applied by the projector for source identity, visibility,
// evidence state, and redaction state. These never vary across call sites.
const (
	sourceName      = "usageauthority"
	sourceVersion   = "app"
	visibilityLevel = controlplane.VisibilityDefault
	evidenceLevel   = controlplane.EvidenceRecorded
	redactionLevel  = controlplane.RedactionSummarized
)

// evidenceProjection is the canonical typed mapping from an accounting
// outcome onto its SDK policydecision representation. Adding a new
// AccountingOutcome means adding a case in outcomeProjectionOf.
type evidenceProjection struct {
	ReasonCode    policydecision.AccountingReasonCode
	PolicyOutcome policydecision.Outcome
	Effect        policydecision.Effect
}

func outcomeProjectionOf(out controlplane.AccountingOutcome) evidenceProjection {
	switch out {
	case controlplane.AccountingOutcomeDeny:
		return evidenceProjection{
			ReasonCode:    policydecision.AccountingReasonQuotaExceeded,
			PolicyOutcome: policydecision.OutcomeDeny,
			Effect:        policydecision.EffectNone,
		}
	case controlplane.AccountingOutcomeError:
		return evidenceProjection{
			ReasonCode:    policydecision.AccountingReasonError,
			PolicyOutcome: policydecision.OutcomeError,
			Effect:        policydecision.EffectNone,
		}
	case controlplane.AccountingOutcomeUnavailable:
		return evidenceProjection{
			ReasonCode:    policydecision.AccountingReasonUnavailable,
			PolicyOutcome: policydecision.OutcomeAllow,
			Effect:        policydecision.EffectAnnotate,
		}
	case controlplane.AccountingOutcomeAdvisory:
		return evidenceProjection{
			ReasonCode:    policydecision.AccountingReasonAdvisory,
			PolicyOutcome: policydecision.OutcomeAllow,
			Effect:        policydecision.EffectAnnotate,
		}
	case controlplane.AccountingOutcomeClamp:
		return evidenceProjection{
			ReasonCode:    policydecision.AccountingReasonClamped,
			PolicyOutcome: policydecision.OutcomeAllow,
			Effect:        policydecision.EffectAnnotate,
		}
	default: // Allow, Reserve, Reconcile, and unknown
		return evidenceProjection{
			ReasonCode:    policydecision.AccountingReasonAllowed,
			PolicyOutcome: policydecision.OutcomeAllow,
			Effect:        policydecision.EffectAnnotate,
		}
	}
}

// reasonForAdmission produces the admission reason code for a domain
// DecisionOutcome, an authority status, and an optional reservation
// reference. Status-aware overrides (Deny + Unavailable state →
// Unavailable reason, Deny + Degraded state → Error reason) and the
// Reserved-with-id priority live here so call sites pass a fully
// resolved reason into Evidence.
func reasonForAdmission(outcome domain.DecisionOutcome, status domain.AuthorityStatus, reserved bool, reservationID string) policydecision.AccountingReasonCode {
	if reserved && reservationID != "" {
		return policydecision.AccountingReasonReserved
	}
	switch outcome {
	case domain.DecisionOutcomeDeny:
		switch status.State {
		case domain.AuthorityStateUnavailable:
			return policydecision.AccountingReasonUnavailable
		case domain.AuthorityStateDegraded:
			return policydecision.AccountingReasonError
		default:
			return policydecision.AccountingReasonQuotaExceeded
		}
	case domain.DecisionOutcomeAdvisory:
		return policydecision.AccountingReasonAdvisory
	case domain.DecisionOutcomeClamp:
		return policydecision.AccountingReasonClamped
	case domain.DecisionOutcomeUnavailable:
		return policydecision.AccountingReasonUnavailable
	case domain.DecisionOutcomeError:
		return policydecision.AccountingReasonError
	default:
		return policydecision.AccountingReasonAllowed
	}
}

// sdkOutcomeFromAdmission maps a domain.DecisionOutcome onto the SDK
// AccountingOutcome used in Evidence.Outcome.
func sdkOutcomeFromAdmission(outcome domain.DecisionOutcome) controlplane.AccountingOutcome {
	switch outcome {
	case domain.DecisionOutcomeDeny:
		return controlplane.AccountingOutcomeDeny
	case domain.DecisionOutcomeClamp:
		return controlplane.AccountingOutcomeClamp
	case domain.DecisionOutcomeAdvisory:
		return controlplane.AccountingOutcomeAdvisory
	case domain.DecisionOutcomeUnavailable:
		return controlplane.AccountingOutcomeUnavailable
	case domain.DecisionOutcomeError:
		return controlplane.AccountingOutcomeError
	default:
		return controlplane.AccountingOutcomeAllow
	}
}

// settlementProjection returns the (outcome, reason, settlement state)
// tuple for a surfaced-attempt settlement or a release. Kind-driven
// transitions take priority; delta-driven (`Overage`, `Adjustment`)
// transitions fill the post-settlement state for finalized attempts.
func settlementProjection(kind SettlementKind, result SettleResult) (controlplane.AccountingOutcome, policydecision.AccountingReasonCode, controlplane.AccountingSettlementState) {
	switch kind {
	case SettlementKindPartial, SettlementKindUnavailable, SettlementKindCancellation:
		return controlplane.AccountingOutcomeUnavailable, policydecision.AccountingReasonUnavailable, controlplane.AccountingSettlementUnavailable
	case SettlementKindSwallowed, SettlementKindLosing:
		return controlplane.AccountingOutcomeReconcile, policydecision.AccountingReasonReserved, controlplane.AccountingSettlementReleased
	}
	state := controlplane.AccountingSettlementSettled
	if result.OverageDelta.Value > 0 {
		state = controlplane.AccountingSettlementOverage
	}
	if result.ReleasedDelta.Value > 0 || result.AdjustmentDelta.Value != 0 {
		state = controlplane.AccountingSettlementAdjusted
	}
	return controlplane.AccountingOutcomeReconcile, policydecision.AccountingReasonReconciled, state
}

// resolveAuthoritySource is the canonical projector-side authority source
// resolver. It distinguishes settlement/release paths (which report
// Reconciled) from reserved admissions (which report Reserved) and falls
// back to the readiness posture for unreserved admissions.
func resolveAuthoritySource(status domain.AuthorityStatus, reserved bool, in Evidence) controlplane.AccountingAuthoritySource {
	switch in.SettlementState {
	case controlplane.AccountingSettlementSettled,
		controlplane.AccountingSettlementOverage,
		controlplane.AccountingSettlementAdjusted:
		return controlplane.AccountingAuthoritySourceReconciled
	}
	if in.Outcome == controlplane.AccountingOutcomeUnavailable && in.SettlementState == controlplane.AccountingSettlementUnavailable {
		return controlplane.AccountingAuthoritySourceReconciled
	}
	if reserved && in.ReservationID != "" {
		return controlplane.AccountingAuthoritySourceReserved
	}
	switch status.State {
	case domain.AuthorityStateAdvisoryOnly:
		return controlplane.AccountingAuthoritySourceAdvisory
	case domain.AuthorityStateUnavailable:
		return controlplane.AccountingAuthoritySourceUnavailable
	case domain.AuthorityStateReady:
		return controlplane.AccountingAuthoritySourceAuthoritative
	default:
		return controlplane.AccountingAuthoritySourceEstimated
	}
}

// settlementStateForAdmission returns the post-admission settlement
// state. Reserved admissions report Pending; non-reserved admissions
// report the zero value so the projector treats them as "no settlement".
func settlementStateForAdmission(reserved bool) controlplane.AccountingSettlementState {
	if reserved {
		return controlplane.AccountingSettlementPending
	}
	return ""
}

// policydecisionVisibility converts a control-plane Visibility to the
// policydecision analog used by ProjectAccountingRecord.
func policydecisionVisibility(v controlplane.Visibility) policydecision.EvidenceVisibility {
	if v == controlplane.VisibilityPrivileged {
		return policydecision.EvidencePrivileged
	}
	return policydecision.EvidenceDefault
}

// firstRuleID returns the first element of ruleIDs or fallback when the
// slice is empty.
func firstRuleID(ruleIDs []string, fallback string) string {
	if len(ruleIDs) > 0 {
		return ruleIDs[0]
	}
	return fallback
}
