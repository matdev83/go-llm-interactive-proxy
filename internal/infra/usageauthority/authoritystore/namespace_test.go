package authoritystore_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestLimitRowsFromRules_NamespaceSeparatesPerspectives(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	customer := domain.Rule{
		ID:             "shared.id",
		Kind:           domain.RuleKindQuota,
		Mode:           domain.RuleModeStrict,
		Unit:           domain.AmountUnitRequests,
		Limit:          domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Perspective:    metering.PerspectiveCustomer,
		LifecycleScope: metering.LifecycleLogicalRequest,
		Basis:          domain.BasisFrontendIngress,
		Namespace:      domain.NamespaceDefault,
		Version:        "1",
	}
	operator := customer
	operator.Perspective = metering.PerspectiveOperator
	operator.LifecycleScope = metering.LifecycleBackendAttempt
	operator.Basis = domain.BasisBackendIngress
	operator.Namespace = "operator-ns"

	rows, err := authoritystore.LimitRowsFromRules([]domain.Rule{customer, operator}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].AuthorityNamespace == rows[1].AuthorityNamespace && rows[0].Perspective == rows[1].Perspective {
		t.Fatal("expected distinct namespace/perspective identity")
	}
	if rows[0].AuthorityNamespace != domain.NamespaceDefault {
		t.Fatalf("customer ns=%q", rows[0].AuthorityNamespace)
	}
	if rows[1].AuthorityNamespace != "operator-ns" {
		t.Fatalf("operator ns=%q", rows[1].AuthorityNamespace)
	}
}

func TestLimitRowsFromRules_LegacyNamespaceDefault(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{
		ID:    "legacy.req",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 3},
		Basis: domain.BasisLegacyProviderPreferredAttempt,
	}
	rows, err := authoritystore.LimitRowsFromRules([]domain.Rule{rule}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AuthorityNamespace != domain.NamespaceLegacy {
		t.Fatalf("rows=%v", rows)
	}
}

func TestReservationKey_NamespacePrefix(t *testing.T) {
	t.Parallel()
	legacy := domain.ReservationKey{LogicalRequestID: "r", RuleID: "rule", Sequence: 1}
	if got, want := legacy.String(), "r||||rule|1"; got != want {
		t.Fatalf("legacy key=%q want %q", got, want)
	}
	named := legacy
	named.Namespace = domain.NamespaceDefault
	if got := named.String(); got != domain.NamespaceDefault+"|"+legacy.String() {
		t.Fatalf("namespaced key=%q", got)
	}
}
