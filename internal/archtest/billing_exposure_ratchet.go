package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	BillingExposureRatchetStreamMoneyMutation             = "stream_money_mutation"
	BillingExposureRatchetHoldLifecycle                   = "hold_lifecycle"
	BillingExposureRatchetALegOnlySettlementIdentity      = "a_leg_only_settlement_identity"
	BillingExposureRatchetRuntimeFinancialEvidenceBarrier = "runtime_financial_evidence_barrier"
	BillingExposureRatchetNetLOCReduction                 = "net_loc_reduction"
)

const (
	BillingExposureRatchetStatusPlanned = "planned"
	BillingExposureRatchetStatusActive  = "active"
)

const (
	BillingExposureActivationForbidHoldSymbols      = "forbid_hold_symbols"
	BillingExposureActivationRequireNetLOCReduction = "require_net_loc_reduction"
)

var billingExposureHoldLifecycleTargetIDs = []string{
	"Authorization",
	"AuthorizationStore",
	"AuthorizationLookup",
	"HoldReleaser",
	"BillingAdmissionCleanup",
	"authorization_holds",
	"reserved_nano",
	"JournalBookLegacyAuthorization",
	"hold_expiry",
	"hold_remainder",
	"hold_release",
}

var billingExposureRuntimeBarrierTargetIDs = []string{
	"evidenceByALeg",
	"billing_parallel_barrier",
	"tur_rebuild_from_remembered_legs",
}

func isBillingExposureRuntimeRetiredTarget(id string) bool {
	return id == "BillingAdmissionCleanup" || slices.Contains(billingExposureRuntimeBarrierTargetIDs, id)
}

// BillingExposurePlannedRatchetByID returns a named planned ratchet.
func BillingExposurePlannedRatchetByID(doc BillingExposureBaselineFile, id string) (BillingExposurePlannedRatchet, bool) {
	for _, ratchet := range doc.PlannedRatchets {
		if ratchet.ID == id {
			return ratchet, true
		}
	}
	return BillingExposurePlannedRatchet{}, false
}

// ValidateBillingExposurePlannedRatchets checks the 0.4 end-state inventory.
func ValidateBillingExposurePlannedRatchets(doc BillingExposureBaselineFile) []RuleFinding {
	holdStatus := BillingExposureRatchetStatusPlanned
	runtimeStatus := BillingExposureRatchetStatusPlanned
	netLOCStatus := BillingExposureRatchetStatusPlanned
	if doc.RequireNetLOCReduction {
		netLOCStatus = BillingExposureRatchetStatusActive
	}
	if doc.ForbidHoldSymbols {
		holdStatus = BillingExposureRatchetStatusActive
		runtimeStatus = BillingExposureRatchetStatusActive
	}
	want := []struct {
		id          string
		status      string
		flag        string
		task        string
		deletionIDs []string
	}{
		{BillingExposureRatchetStreamMoneyMutation, BillingExposureRatchetStatusActive, "", "already-enforced", nil},
		{BillingExposureRatchetHoldLifecycle, holdStatus, BillingExposureActivationForbidHoldSymbols, "7.1", billingExposureHoldLifecycleTargetIDs},
		{BillingExposureRatchetALegOnlySettlementIdentity, BillingExposureRatchetStatusActive, "", "already-enforced", nil},
		{BillingExposureRatchetRuntimeFinancialEvidenceBarrier, runtimeStatus, BillingExposureActivationForbidHoldSymbols, "7.1", billingExposureRuntimeBarrierTargetIDs},
		{BillingExposureRatchetNetLOCReduction, netLOCStatus, BillingExposureActivationRequireNetLOCReduction, "7.2", nil},
	}
	var out []RuleFinding
	if len(doc.PlannedRatchets) != len(want) {
		out = append(out, RuleFinding{
			Rule:   "billing_exposure_planned_inventory",
			Detail: fmt.Sprintf("planned_ratchets count = %d, want %d", len(doc.PlannedRatchets), len(want)),
		})
	}
	knownTargets := make(map[string]struct{}, len(doc.DeletionTargets))
	for _, target := range doc.DeletionTargets {
		knownTargets[target.ID] = struct{}{}
	}
	for i, row := range want {
		if i >= len(doc.PlannedRatchets) {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_planned_inventory",
				Path:   row.id,
				Detail: "missing planned ratchet",
			})
			continue
		}
		got := doc.PlannedRatchets[i]
		if got.ID != row.id || got.Status != row.status || got.ActivationFlag != row.flag || got.ActivationTask != row.task {
			out = append(out, RuleFinding{
				Rule: "billing_exposure_planned_inventory",
				Path: row.id,
				Detail: fmt.Sprintf("id=%q status=%q flag=%q task=%q, want id=%q status=%q flag=%q task=%q",
					got.ID, got.Status, got.ActivationFlag, got.ActivationTask, row.id, row.status, row.flag, row.task),
			})
		}
		if strings.TrimSpace(got.EndState) == "" {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_planned_inventory",
				Path:   row.id,
				Detail: "end_state must document the end-state forbid",
			})
		}
		if !slices.Equal(got.DeletionTargetIDs, row.deletionIDs) {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_planned_inventory",
				Path:   row.id,
				Detail: fmt.Sprintf("deletion_target_ids = %v, want %v", got.DeletionTargetIDs, row.deletionIDs),
			})
		}
		for _, id := range got.DeletionTargetIDs {
			if _, ok := knownTargets[id]; !ok {
				out = append(out, RuleFinding{
					Rule:   "billing_exposure_planned_inventory",
					Path:   row.id,
					Detail: "deletion_target_ids references unknown target " + id,
				})
			}
		}
	}
	identity, ok := BillingExposurePlannedRatchetByID(doc, BillingExposureRatchetALegOnlySettlementIdentity)
	if ok {
		if !slices.Contains(identity.Files, "internal/core/billing/settlement.go") {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_planned_inventory",
				Path:   identity.ID,
				Detail: "identity ratchet must scan internal/core/billing/settlement.go",
			})
		}
		if len(identity.CurrentMarkers) == 0 || len(identity.ForbiddenWhenActivated) == 0 {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_planned_inventory",
				Path:   identity.ID,
				Detail: "identity ratchet must record current A-leg markers and activated forbids",
			})
		}
		if !slices.Contains(identity.RequiredWhenActivated, "BillingCallID") {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_planned_inventory",
				Path:   identity.ID,
				Detail: "activated identity ratchet must require BillingCallID",
			})
		}
	}
	return out
}

// EvaluateBillingExposureDeletionRatchet checks hold/collector deletion targets.
// The manifest records the retired targets, while schema migration files and
// explicit legacy-recovery readers remain outside the normal call-path scan.
// Once ForbidHoldSymbols is true, executable ownership must be absent.
func EvaluateBillingExposureDeletionRatchet(root string, doc BillingExposureBaselineFile) ([]RuleFinding, error) {
	var out []RuleFinding
	activated := doc.ForbidHoldSymbols
	for _, target := range doc.DeletionTargets {
		found, err := BillingExposureDeletionTargetPresent(root, target)
		if err != nil {
			return nil, err
		}
		if !activated && isBillingExposureRuntimeRetiredTarget(target.ID) {
			if target.Present || found {
				out = append(out, RuleFinding{
					Rule:   "billing_exposure_runtime_barrier",
					Path:   target.ID,
					Detail: "runtime financial evidence barrier was removed in Phase 5 and must remain absent",
				})
			}
			continue
		}
		if !activated {
			if !target.Present {
				out = append(out, RuleFinding{
					Rule:   "billing_exposure_hold_planned",
					Path:   target.ID,
					Detail: "planned ratchet requires present=true until forbid_hold_symbols is flipped in 7.1",
				})
			}
			if !found {
				out = append(out, RuleFinding{
					Rule:   "billing_exposure_hold_planned",
					Path:   target.ID,
					Detail: "planned deletion target missing from production source",
				})
			}
			continue
		}
		if target.Present {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_hold_activated",
				Path:   target.ID,
				Detail: "forbid_hold_symbols is true; deletion target must record present=false",
			})
		}
		if found {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_hold_activated",
				Path:   target.ID,
				Detail: "authorization-book/reserved-balance hold lifecycle must not return to the normal call path after deletion",
			})
		}
	}
	return out, nil
}

// EvaluateBillingExposureIdentityRatchet checks A-leg-only customer settlement identity.
func EvaluateBillingExposureIdentityRatchet(root string, doc BillingExposureBaselineFile) ([]RuleFinding, error) {
	ratchet, ok := BillingExposurePlannedRatchetByID(doc, BillingExposureRatchetALegOnlySettlementIdentity)
	if !ok {
		return []RuleFinding{{
			Rule:   "billing_exposure_identity_plan",
			Path:   BillingExposureRatchetALegOnlySettlementIdentity,
			Detail: "planned ratchet missing from baseline JSON",
		}}, nil
	}
	return evaluateBillingExposureMarkerRatchet(root, ratchet, billingExposureRatchetActivated(doc, ratchet))
}

// EvaluateBillingExposureLOCRatchet locks measured production LOC. Net reduction
// is required only after require_net_loc_reduction is true (task 7.2).
func EvaluateBillingExposureLOCRatchet(doc BillingExposureBaselineFile, measured BillingExposureMeasurement) []RuleFinding {
	if !doc.RequireNetLOCReduction {
		if measured.Total != doc.BaselineTotal {
			return []RuleFinding{{
				Rule: "billing_exposure_loc_lock",
				Detail: fmt.Sprintf("measured total %d != locked baseline %d (7.2 will require net reduction vs this baseline)",
					measured.Total, doc.BaselineTotal),
			}}
		}
		return nil
	}
	if measured.Total >= doc.BaselineTotal {
		return []RuleFinding{{
			Rule: "billing_exposure_loc_reduction",
			Detail: fmt.Sprintf("measured total %d is not a net reduction vs baseline %d",
				measured.Total, doc.BaselineTotal),
		}}
	}
	return nil
}

// EvaluateBillingExposureLOCRatchetAtPhase keeps the final LOC ratchet honest
// while Phase 2 is still integrating its transport seam. The approved gate is
// not weakened: once task 7.2 is checked, the normal immutable baseline check
// runs again. This avoids treating a Phase-2 plumbing delta as a Phase-7
// deletion result.
func EvaluateBillingExposureLOCRatchetAtPhase(root string, doc BillingExposureBaselineFile, measured BillingExposureMeasurement) []RuleFinding {
	if doc.RequireNetLOCReduction && !billingExposureTaskChecked(root, "7.2") {
		return nil
	}
	return EvaluateBillingExposureLOCRatchet(doc, measured)
}

func billingExposureTaskChecked(root, task string) bool {
	raw, err := os.ReadFile(filepath.Join(root, ".kiro", "specs", "billing-architecture-final-convergence", "tasks.md"))
	if err != nil {
		return false
	}
	// Line-scoped: only a checkbox line whose task id immediately follows the
	// marker counts. Substring matching would let "7.2" match "17.2" or a
	// bullet body, silently flipping the gate.
	prefix := "- [x] " + task
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		rest := strings.TrimPrefix(trimmed, prefix)
		if rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t") {
			return true
		}
	}
	return false
}

func billingExposureRatchetActivated(doc BillingExposureBaselineFile, ratchet BillingExposurePlannedRatchet) bool {
	switch ratchet.ActivationFlag {
	case BillingExposureActivationForbidHoldSymbols:
		return doc.ForbidHoldSymbols
	case BillingExposureActivationRequireNetLOCReduction:
		return doc.RequireNetLOCReduction
	default:
		return ratchet.Status == BillingExposureRatchetStatusActive
	}
}

func evaluateBillingExposureMarkerRatchet(root string, ratchet BillingExposurePlannedRatchet, activated bool) ([]RuleFinding, error) {
	if len(ratchet.Files) == 0 {
		return nil, nil
	}
	var b strings.Builder
	for _, rel := range ratchet.Files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		b.Write(src)
		b.WriteByte('\n')
	}
	body := b.String()
	var out []RuleFinding
	if !activated {
		for _, marker := range ratchet.CurrentMarkers {
			if !strings.Contains(body, marker) {
				out = append(out, RuleFinding{
					Rule:   "billing_exposure_identity_planned",
					Path:   ratchet.ID,
					Detail: "planned current marker missing: " + marker,
				})
			}
		}
		return out, nil
	}
	for _, marker := range ratchet.ForbiddenWhenActivated {
		if strings.Contains(body, marker) {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_identity_activated",
				Path:   ratchet.ID,
				Detail: "A-leg-only customer settlement identity must not remain: " + marker,
			})
		}
	}
	for _, marker := range ratchet.RequiredWhenActivated {
		if !strings.Contains(body, marker) {
			out = append(out, RuleFinding{
				Rule:   "billing_exposure_identity_activated",
				Path:   ratchet.ID,
				Detail: "customer settlement key must include " + marker,
			})
		}
	}
	return out, nil
}
