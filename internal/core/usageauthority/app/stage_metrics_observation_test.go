package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type recordingStageMetrics struct {
	mu   sync.Mutex
	seen []stageObservation
}

type stageObservation struct {
	stage, provider, outcome string
}

func (r *recordingStageMetrics) ObserveStage(stage, provider, outcome string, _ float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, stageObservation{stage: stage, provider: provider, outcome: outcome})
}

func (r *recordingStageMetrics) stages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seen))
	for _, s := range r.seen {
		out = append(out, s.stage)
	}
	return out
}

// TestService_ObservesSettleReleaseAndQueryStages covers req 16.5: stage metrics
// must fire for settle/release/query, not only admit.
func TestService_ObservesSettleReleaseAndQueryStages(t *testing.T) {
	t.Parallel()
	metrics := &recordingStageMetrics{}
	store := newFakeStateStore()
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-1", ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}}
	store.settleResult = SettleResult{Applied: true, ReservationID: "reservation-1", ReleasedDelta: domain.Amount{Unit: domain.AmountUnitRequests, Value: 4}}
	store.releaseResult = ReleaseResult{Applied: true, ReservationID: "reservation-1", ReleasedDelta: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}}
	quotaRule := domain.Rule{
		ID: "tenant.requests", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict,
		Unit: domain.AmountUnitRequests, Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{quotaRule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()}, ServiceOptions{StageMetrics: metrics})

	_, err := svc.Settle(context.Background(), SettleInput{
		Correlation:    controlplane.Correlation{RequestID: "request-1", BackendID: "backend-1"},
		Scope:          scope.PrincipalScopeView{PrincipalID: scope.Known("p1")},
		ReservationKey: domain.ReservationKey{LogicalRequestID: "request-1", RuleID: quotaRule.ID, Sequence: 1},
		RuleID:         quotaRule.ID, ReservationID: "reservation-1", Kind: SettlementKindFinal,
		FinalUsage:    domain.Amount{Unit: domain.AmountUnitRequests, Value: 6},
		ReservedUsage: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Authority:     domain.AuthorityLevelAuthoritative, Stage: feature.StageIDAttemptLifecycle,
		BackendAttempted: true, OutputCommitted: true,
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	_, err = svc.Release(context.Background(), ReleaseInput{
		Correlation:    controlplane.Correlation{RequestID: "request-2"},
		ReservationKey: domain.ReservationKey{LogicalRequestID: "request-2", RuleID: quotaRule.ID, Sequence: 1},
		RuleID:         quotaRule.ID, ReservationID: "reservation-2", Kind: ReleaseKindAdmissionFailure,
		Amount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, Authority: domain.AuthorityLevelAuthoritative,
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	_, err = svc.Limits(context.Background(), controlplane.AccountingLimitStatusQuery{Limit: 10})
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	_, err = svc.Decisions(context.Background(), controlplane.AccountingDecisionQuery{Limit: 10})
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}

	stages := metrics.stages()
	want := map[string]bool{StageSettle: false, StageRelease: false, StageQuery: false}
	for _, st := range stages {
		if _, ok := want[st]; ok {
			want[st] = true
		}
	}
	for st, seen := range want {
		if !seen {
			t.Fatalf("missing stage observation %q; seen=%v", st, stages)
		}
	}
	queryCount := 0
	for _, st := range stages {
		if st == StageQuery {
			queryCount++
		}
	}
	if queryCount < 2 {
		t.Fatalf("expected Limits+Decisions to observe query twice, got %d in %v", queryCount, stages)
	}
}
