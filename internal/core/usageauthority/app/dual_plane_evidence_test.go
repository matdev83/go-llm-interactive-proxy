package app

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func TestProjectAccountingAuthorityEventDualPlaneFields(t *testing.T) {
	t.Parallel()
	now := time.Unix(456, 0).UTC()
	status := domain.AuthorityStatus{State: domain.AuthorityStateReady}
	rule := domain.Rule{
		ID:             "tenant.tokens",
		Kind:           domain.RuleKindQuota,
		Perspective:    metering.PerspectiveCustomer,
		LifecycleScope: metering.LifecycleLogicalRequest,
		Basis:          domain.BasisFrontendIngress,
		Namespace:      "customer-ns",
		Version:        "snap-3",
	}
	input := applyRuleContext(Evidence{
		At: now,
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "req-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
		},
		Scope:              principalScope(),
		RuleID:             rule.ID,
		RuleType:           string(rule.Kind),
		Outcome:            controlplane.AccountingOutcomeReserve,
		ReasonCode:         policydecision.AccountingReasonReserved,
		ReservationID:      "res-1",
		SettlementState:    controlplane.AccountingSettlementPending,
		Unit:               "tokens",
		Limit:              100,
		Reserved:           10,
		BackendAttempted:   true,
		OutputCommitted:    true,
		BoundPolicyVersion: economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "usage_authority", Version: "snap-3"}},
		BoundRatingVersion: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "static-rater", Version: "r-1"}, RaterID: "static"},
	}, rule)

	ev, err := ProjectAccountingAuthorityEvent(status, true, input)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	d := ev.AccountingAuthority
	if d == nil {
		t.Fatal("missing accounting authority detail")
	}
	if d.AuthorityNamespace != "customer-ns" {
		t.Fatalf("namespace=%q", d.AuthorityNamespace)
	}
	if d.Perspective != controlplane.UsagePerspectiveCustomer {
		t.Fatalf("perspective=%q", d.Perspective)
	}
	if d.LifecycleScope != controlplane.UsageLifecycleLogicalRequest {
		t.Fatalf("lifecycle_scope=%q", d.LifecycleScope)
	}
	if d.Basis != string(domain.BasisFrontendIngress) {
		t.Fatalf("basis=%q", d.Basis)
	}
	if d.RuleVersion != "snap-3" {
		t.Fatalf("rule_version=%q", d.RuleVersion)
	}
	if d.Surfaced != controlplane.UsageSurfacedYes {
		t.Fatalf("surfaced=%q", d.Surfaced)
	}
	if d.ReservationType != controlplane.AuthorityHandleReservation {
		t.Fatalf("reservation_type=%q", d.ReservationType)
	}
	if d.BoundPolicyVersion.Version != "snap-3" {
		t.Fatalf("bound_policy_version=%#v", d.BoundPolicyVersion)
	}
	if d.BoundRatingVersion.ID != "static" || d.BoundRatingVersion.Version != "r-1" {
		t.Fatalf("bound_rating_version=%#v", d.BoundRatingVersion)
	}
	if d.ParentRequestID != "req-1" {
		t.Fatalf("parent_request_id=%q", d.ParentRequestID)
	}
}
