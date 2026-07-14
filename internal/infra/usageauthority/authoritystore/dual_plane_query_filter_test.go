package authoritystore

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestLimitRowMatchesQueryPerspectiveAndLifecycle(t *testing.T) {
	t.Parallel()
	row := controlplane.AccountingLimitStatusRow{
		RuleID:         "dual-rule",
		Perspective:    string(controlplane.UsagePerspectiveCustomer),
		LifecycleScope: string(controlplane.UsageLifecycleLogicalRequest),
	}
	if !limitRowMatchesQuery(row, controlplane.AccountingLimitStatusQuery{
		RuleID:         "dual-rule",
		Perspective:    controlplane.UsagePerspectiveCustomer,
		LifecycleScope: controlplane.UsageLifecycleLogicalRequest,
	}) {
		t.Fatal("expected customer/logical_request row to match")
	}
	if limitRowMatchesQuery(row, controlplane.AccountingLimitStatusQuery{
		RuleID:      "dual-rule",
		Perspective: controlplane.UsagePerspectiveOperator,
	}) {
		t.Fatal("operator perspective must not match customer row")
	}
	if limitRowMatchesQuery(row, controlplane.AccountingLimitStatusQuery{
		RuleID:         "dual-rule",
		LifecycleScope: controlplane.UsageLifecycleBackendAttempt,
	}) {
		t.Fatal("backend_attempt lifecycle must not match logical_request row")
	}
}

func TestDecisionRowMatchesQueryPerspectiveAndLifecycle(t *testing.T) {
	t.Parallel()
	row := controlplane.AccountingDecisionRow{
		RuleID:         "dual-rule",
		Perspective:    controlplane.UsagePerspectiveCustomer,
		LifecycleScope: controlplane.UsageLifecycleLogicalRequest,
	}
	if !decisionRowMatchesQuery(row, controlplane.AccountingDecisionQuery{
		RuleID:         "dual-rule",
		Perspective:    controlplane.UsagePerspectiveCustomer,
		LifecycleScope: controlplane.UsageLifecycleLogicalRequest,
	}) {
		t.Fatal("expected customer/logical_request row to match")
	}
	if decisionRowMatchesQuery(row, controlplane.AccountingDecisionQuery{
		RuleID:      "dual-rule",
		Perspective: controlplane.UsagePerspectiveOperator,
	}) {
		t.Fatal("operator perspective must not match customer row")
	}
}

func TestLimitRowMatchesQueryBasis(t *testing.T) {
	t.Parallel()
	row := controlplane.AccountingLimitStatusRow{
		RuleID: "dual-rule",
		Basis:  "frontend_ingress",
	}
	if !limitRowMatchesQuery(row, controlplane.AccountingLimitStatusQuery{
		RuleID: "dual-rule",
		Basis:  "frontend_ingress",
	}) {
		t.Fatal("expected matching basis to pass")
	}
	if limitRowMatchesQuery(row, controlplane.AccountingLimitStatusQuery{
		RuleID: "dual-rule",
		Basis:  "backend_egress",
	}) {
		t.Fatal("mismatched basis must not silently widen")
	}
	if !limitRowMatchesQuery(row, controlplane.AccountingLimitStatusQuery{
		RuleID: "dual-rule",
		Basis:  "  frontend_ingress  ",
	}) {
		t.Fatal("query basis must trim before compare")
	}
}

func TestDecisionRowMatchesQueryBasis(t *testing.T) {
	t.Parallel()
	row := controlplane.AccountingDecisionRow{
		RuleID: "dual-rule",
		Basis:  "frontend_ingress",
	}
	if !decisionRowMatchesQuery(row, controlplane.AccountingDecisionQuery{
		RuleID: "dual-rule",
		Basis:  "frontend_ingress",
	}) {
		t.Fatal("expected matching basis to pass")
	}
	if decisionRowMatchesQuery(row, controlplane.AccountingDecisionQuery{
		RuleID: "dual-rule",
		Basis:  "backend_egress",
	}) {
		t.Fatal("mismatched basis must not silently widen")
	}
}
