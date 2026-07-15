package app

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// TestSettle_DualPlaneUsesEgressFactNotWrongFinalUsage proves requirement 9.5:
// store settle mutations use SelectAmount from operator egress facts, not the
// undifferentiated FinalUsage decoy (wrong perspective/boundary amount).
func TestSettle_DualPlaneUsesEgressFactNotWrongFinalUsage(t *testing.T) {
	t.Parallel()

	rule := domain.Rule{
		ID:             "op.tokens",
		Kind:           domain.RuleKindQuota,
		Mode:           domain.RuleModeStrict,
		Unit:           domain.AmountUnitInputTokens,
		Limit:          domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 1000},
		Perspective:    metering.PerspectiveOperator,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Basis:          domain.BasisBackendIngress,
		Namespace:      domain.NamespaceDefault,
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}
	store := newFakeStateStore()
	store.readiness = domain.StatusFromBacking(domain.BackingCapabilityAtomic)
	store.capacityLimit = 1000
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: store.readiness,
		Rules:  []domain.Rule{rule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	key := domain.ReservationKey{
		LogicalRequestID: "req-1",
		ALegID:           "a-1",
		BLegID:           "b-1",
		AttemptID:        "b-1",
		RuleID:           rule.ID,
		Sequence:         1,
		Namespace:        domain.NamespaceDefault,
	}
	admit, err := svc.Admit(context.Background(), AdmissionInput{
		Correlation:    controlplane.Correlation{RequestID: "req-1", BackendID: "backend-1"},
		Dimensions:     domain.Dimensions{Backend: scope.Known("backend-1")},
		Request:        domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 10},
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: key,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Quantities: []metering.Quantity{{
				Component: metering.ComponentInputToken,
				Unit:      metering.UnitToken,
				Value:     10,
				Present:   true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !admit.Reserved {
		t.Fatalf("admit=%+v", admit)
	}

	_, err = svc.Settle(context.Background(), SettleInput{
		Correlation:          controlplane.Correlation{RequestID: "req-1", BackendID: "backend-1"},
		ReservationKey:       key,
		ReservationID:        admit.ReservationID,
		RuleID:               rule.ID,
		Kind:                 SettlementKindFinal,
		FinalUsage:           domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 999},
		FinalUsagePresent:    true,
		ReservedUsage:        domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 10},
		Authority:            domain.AuthorityLevelAuthoritative,
		MeasurementAuthority: MeasurementAuthority{Usage: domain.AuthorityLevelAuthoritative},
		Facts: []metering.Fact{{
			FactID:      "be-egress-1",
			StreamID:    "s1",
			Sequence:    1,
			Kind:        metering.FactKindCumulative,
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendEgress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Quantities: []metering.Quantity{{
				Component: metering.ComponentInputToken,
				Unit:      metering.UnitToken,
				Value:     42,
				Present:   true,
			}},
			Source:    metering.SourceProviderReported,
			Authority: metering.AuthorityAuthoritative,
			Presence:  metering.PresencePresent,
		}},
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if len(store.settleCalls) != 1 {
		t.Fatalf("settle calls=%d", len(store.settleCalls))
	}
	got := store.settleCalls[0]
	if got.FinalUsage.Value != 42 {
		t.Fatalf("cmd FinalUsage=%v want 42 from egress fact (not decoy 999)", got.FinalUsage)
	}
	if len(got.Reservations) != 1 || got.Reservations[0].FinalUsage.Value != 42 {
		t.Fatalf("descriptor FinalUsage=%v", got.Reservations)
	}
}

func TestSettle_DualPlaneMissingFactsFailsClosed(t *testing.T) {
	t.Parallel()

	rule := domain.Rule{
		ID:             "op.tokens",
		Kind:           domain.RuleKindQuota,
		Mode:           domain.RuleModeStrict,
		Unit:           domain.AmountUnitInputTokens,
		Limit:          domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 1000},
		Perspective:    metering.PerspectiveOperator,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Basis:          domain.BasisBackendIngress,
		Namespace:      domain.NamespaceDefault,
	}
	store := newFakeStateStore()
	store.readiness = domain.StatusFromBacking(domain.BackingCapabilityAtomic)
	store.capacityLimit = 1000
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	key := domain.ReservationKey{LogicalRequestID: "r", RuleID: rule.ID, Sequence: 1, Namespace: domain.NamespaceDefault}
	admit, err := svc.Admit(context.Background(), AdmissionInput{
		Request:        domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 1},
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: key,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Quantities:  []metering.Quantity{{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true}},
		},
	})
	if err != nil || !admit.Reserved {
		t.Fatalf("admit err=%v res=%+v", err, admit)
	}

	_, err = svc.Settle(context.Background(), SettleInput{
		ReservationKey:    key,
		ReservationID:     admit.ReservationID,
		RuleID:            rule.ID,
		Kind:              SettlementKindFinal,
		FinalUsage:        domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 1},
		FinalUsagePresent: true,
		Authority:         domain.AuthorityLevelAuthoritative,
	})
	if err == nil {
		t.Fatal("expected dual-plane settle without Facts/Exposure to fail")
	}
	if len(store.settleCalls) != 0 {
		t.Fatalf("store must not mutate on selection failure; got %d settles", len(store.settleCalls))
	}
}
