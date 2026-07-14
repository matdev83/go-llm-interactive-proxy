package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type mutableRuleSource struct {
	mu   sync.Mutex
	snap RuleSnapshot
}

func (m *mutableRuleSource) Snapshot(context.Context) (RuleSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.snap
	out.Rules = append([]domain.Rule(nil), m.snap.Rules...)
	return out, nil
}

func (m *mutableRuleSource) publish(snap RuleSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap = snap
}

func TestAdmitSettle_UsesBoundSnapshotVersionAfterPublish(t *testing.T) {
	t.Parallel()

	v1Rule := domain.Rule{
		ID:             "op.tokens",
		Kind:           domain.RuleKindQuota,
		Mode:           domain.RuleModeStrict,
		Unit:           domain.AmountUnitInputTokens,
		Limit:          domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 1000},
		Perspective:    metering.PerspectiveOperator,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Basis:          domain.BasisBackendEgress,
		Namespace:      domain.NamespaceDefault,
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}
	v2Rule := v1Rule
	v2Rule.Basis = domain.BasisBackendIngress // would not match egress facts

	src := &mutableRuleSource{snap: RuleSnapshot{
		ID:      "usage_authority",
		Version: "v1",
		State:   economics.SnapshotReady,
		Status:  domain.StatusFromBacking(domain.BackingCapabilityAtomic),
		Rules:   []domain.Rule{v1Rule},
	}}
	store := newFakeStateStore()
	store.readiness = domain.StatusFromBacking(domain.BackingCapabilityAtomic)
	store.capacityLimit = 1000
	svc := NewService(src, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	key := domain.ReservationKey{
		LogicalRequestID: "req-bind",
		ALegID:           "a-1",
		BLegID:           "b-1",
		AttemptID:        "b-1",
		RuleID:           v1Rule.ID,
		Sequence:         1,
		Namespace:        domain.NamespaceDefault,
	}
	admit, err := svc.Admit(context.Background(), AdmissionInput{
		Correlation:    controlplane.Correlation{RequestID: "req-bind", BackendID: "backend-1"},
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
	if admit.BoundVersion.Version != "v1" {
		t.Fatalf("bound=%+v", admit.BoundVersion)
	}

	src.publish(RuleSnapshot{
		ID:      "usage_authority",
		Version: "v2",
		State:   economics.SnapshotReady,
		Status:  domain.StatusFromBacking(domain.BackingCapabilityAtomic),
		Rules:   []domain.Rule{v2Rule},
	})

	_, err = svc.Settle(context.Background(), SettleInput{
		Correlation:          controlplane.Correlation{RequestID: "req-bind", BackendID: "backend-1"},
		ReservationKey:       key,
		ReservationID:        admit.ReservationID,
		RuleID:               v1Rule.ID,
		Kind:                 SettlementKindFinal,
		BoundVersion:         admit.BoundVersion,
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
		}},
	})
	if err != nil {
		t.Fatalf("settle with bound v1 must succeed after v2 publish: %v", err)
	}

	admit2, err := svc.Admit(context.Background(), AdmissionInput{
		Correlation:    controlplane.Correlation{RequestID: "req-bind-2", BackendID: "backend-1"},
		Dimensions:     domain.Dimensions{Backend: scope.Known("backend-1")},
		Request:        domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 10},
		Authority:      domain.AuthorityLevelEstimated,
		ReservationKey: domain.ReservationKey{LogicalRequestID: "req-bind-2", Sequence: 1, RuleID: v2Rule.ID, Namespace: domain.NamespaceDefault},
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
		t.Fatalf("admit2: %v", err)
	}
	if admit2.BoundVersion.Version != "v2" {
		t.Fatalf("new admit bound=%+v", admit2.BoundVersion)
	}
}
