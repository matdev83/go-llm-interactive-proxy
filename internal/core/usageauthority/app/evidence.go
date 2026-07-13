package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// projectAuthorityEvidence projects the slim Evidence struct onto both
// the policydecision Record and the control-plane accounting-authority
// Event. Authority is derived from (status, reserved, in) via
// resolveAuthoritySource; WindowStart/End/ResetAt are left as the time.Time
// zero value to signal "no window" (the rule snapshot, not this projector,
// owns window metadata). Source identity, visibility, evidence/redaction
// state, and outcome→policy mapping are owned by this projector.
func projectAuthorityEvidence(status domain.AuthorityStatus, reserved bool, in Evidence) (policyAndControlPlane, error) {
	if !in.Outcome.IsKnown() {
		return policyAndControlPlane{}, fmt.Errorf("usage authority evidence: unknown accounting outcome")
	}
	if !in.SettlementState.IsKnown() && in.SettlementState != "" {
		return policyAndControlPlane{}, fmt.Errorf("usage authority evidence: unknown settlement state")
	}

	p := outcomeProjectionOf(in.Outcome)
	authority := resolveAuthoritySource(status, reserved, in)
	snapshot := scopeSnapshot(in.Scope)
	correlation := in.Correlation
	stage := in.Stage
	if stage == "" {
		stage = feature.StageIDPreRequest
	}

	record, ok := policydecision.ProjectAccountingRecord(policydecision.Record{
		TraceID:          correlation.TraceID,
		ALegID:           correlation.ALegID,
		BLegID:           correlation.BLegID,
		AttemptSeq:       correlation.AttemptSeq,
		Stage:            stage,
		Provider:         policydecision.ProviderRef{ID: sourceName, Stage: stage},
		Outcome:          p.PolicyOutcome,
		Effect:           p.Effect,
		Visibility:       policydecisionVisibility(visibilityLevel),
		Scope:            in.Scope.Clone(),
		OutputCommitted:  in.OutputCommitted,
		BackendAttempted: in.BackendAttempted,
	}, policydecision.AccountingProjection{
		ReasonCode:       in.ReasonCode,
		RuleID:           in.RuleID,
		Authority:        string(authority),
		ReservationID:    in.ReservationID,
		SettlementStatus: string(in.SettlementState),
	})
	if !ok {
		return policyAndControlPlane{}, fmt.Errorf("usage authority evidence: invalid policydecision projection")
	}
	if len(in.MatchedRuleIDs) > 0 {
		if record.Annotations == nil {
			record.Annotations = make(map[string]string)
		}
		ids := make([]string, 0, len(in.MatchedRuleIDs))
		for _, id := range in.MatchedRuleIDs {
			if strings.TrimSpace(id) == "" || len(ids) >= 32 {
				continue
			}
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			record.Annotations["accounting.rule_ids"] = strings.Join(ids, ",")
		}
	}
	if in.RequestedMax.Unit != "" {
		if record.Annotations == nil {
			record.Annotations = make(map[string]string)
		}
		record.Annotations["accounting.requested_max"] = in.RequestedMax.String()
	}
	if in.EffectiveMax.Unit != "" {
		if record.Annotations == nil {
			record.Annotations = make(map[string]string)
		}
		record.Annotations["accounting.effective_max"] = in.EffectiveMax.String()
	}
	if strings.TrimSpace(in.ClampReason) != "" {
		if record.Annotations == nil {
			record.Annotations = make(map[string]string)
		}
		record.Annotations["accounting.clamp_reason"] = strings.TrimSpace(in.ClampReason)
	}

	event := controlplane.Event{
		SourceEventKey: sourceEventKey(in),
		Category:       controlplane.CategoryAccountingAuthority,
		OccurredAt:     in.At.UTC(),
		RecordedAt:     in.At.UTC(),
		Correlation:    correlation,
		Scope:          snapshot,
		Source: controlplane.SourceRef{
			Name:    sourceName,
			Version: sourceVersion,
		},
		Visibility:     visibilityLevel,
		EvidenceState:  evidenceLevel,
		RedactionState: redactionLevel,
		Summary:        summaryForReason(in.ReasonCode),
		AccountingAuthority: &controlplane.AccountingAuthorityDetail{
			Correlation:     correlation,
			Scope:           snapshot,
			RuleID:          in.RuleID,
			RuleType:        in.RuleType,
			Outcome:         in.Outcome,
			ReasonCode:      string(in.ReasonCode),
			Authority:       authority,
			ReservationID:   in.ReservationID,
			SettlementState: in.SettlementState,
			Unit:            in.Unit,
			Currency:        in.Currency,
			Limit:           in.Limit,
			Consumed:        in.Consumed,
			Reserved:        in.Reserved,
			Remaining:       deriveRemaining(in.Limit, in.Consumed, in.Reserved),
			Adjustment:      in.Adjustment,
			WindowStart:     time.Time{},
			WindowEnd:       time.Time{},
			WindowResetAt:   time.Time{},
			EvidenceState:   evidenceLevel,
			RedactionState:  redactionLevel,
		},
	}
	if err := event.Validate(); err != nil {
		return policyAndControlPlane{}, err
	}

	return policyAndControlPlane{Policy: record, Event: event}, nil
}

// ProjectPolicyDecision projects safe accounting evidence into policydecision
// records.
func ProjectPolicyDecision(status domain.AuthorityStatus, reserved bool, in Evidence) (policydecision.Record, bool) {
	projected, err := projectAuthorityEvidence(status, reserved, in)
	if err != nil {
		return policydecision.Record{}, false
	}
	return projected.Policy, true
}

// ProjectAccountingAuthorityEvent projects safe accounting evidence into a
// control-plane accounting-authority event.
func ProjectAccountingAuthorityEvent(status domain.AuthorityStatus, reserved bool, in Evidence) (controlplane.Event, error) {
	projected, err := projectAuthorityEvidence(status, reserved, in)
	if err != nil {
		return controlplane.Event{}, err
	}
	return projected.Event, nil
}

func sourceEventKey(in Evidence) string {
	parts := []string{
		sourceName,
		in.Correlation.TraceID,
		in.Correlation.RequestID,
		in.Correlation.SessionID,
		in.Correlation.ALegID,
		in.Correlation.BLegID,
		strconv.Itoa(in.Correlation.AttemptSeq),
		in.RuleID,
		in.ReservationID,
		in.SourceKind,
		strconv.Itoa(in.SourceSequence),
		string(in.Outcome),
		string(in.ReasonCode),
	}
	return strings.Join(parts, "|")
}

func summaryForReason(reason policydecision.AccountingReasonCode) string {
	if reason == "" {
		return ""
	}
	return string(reason)
}

func scopeSnapshot(view scope.PrincipalScopeView) controlplane.ScopeSnapshot {
	return controlplane.ScopeSnapshot{
		Principal:      view.Clone(),
		PrincipalID:    view.PrincipalID,
		CredentialID:   view.CredentialID,
		TenantID:       view.TenantID,
		OrganizationID: view.OrganizationID,
		WorkspaceID:    view.WorkspaceID,
		ProjectID:      view.ProjectID,
		DepartmentID:   view.DepartmentID,
		CostCenterID:   view.CostCenterID,
	}
}

// deriveRemaining computes the safe-to-surface remaining amount as
// max(0, limit - consumed - reserved). Clamped to zero to match the
// authority store's limit row semantics and to keep operator-visible
// evidence non-negative even when reservation pressure exceeds the limit.
func deriveRemaining(limit, consumed, reserved int64) int64 {
	remaining := limit - consumed - reserved
	if remaining < 0 {
		return 0
	}
	return remaining
}
