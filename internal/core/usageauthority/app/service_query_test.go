package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestQueryService(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status domain.AuthorityStatus
		want   controlplane.AccountingAuthorityState
	}{
		{name: "disabled", status: domain.AuthorityStatus{State: domain.AuthorityStateDisabled, Reason: domain.StatusReasonDisabledByConfig}, want: controlplane.AccountingAuthorityDisabled},
		{name: "advisory-only", status: domain.AuthorityStatus{State: domain.AuthorityStateAdvisoryOnly, Reason: domain.StatusReasonAdvisoryOnly}, want: controlplane.AccountingAuthorityAdvisoryOnly},
		{name: "degraded", status: domain.AuthorityStatus{State: domain.AuthorityStateDegraded, Reason: domain.StatusReasonBackingDegraded}, want: controlplane.AccountingAuthorityDegraded},
		{name: "unavailable", status: domain.AuthorityStatus{State: domain.AuthorityStateUnavailable, Reason: domain.StatusReasonBackingUnavailable}, want: controlplane.AccountingAuthorityUnavailable},
		{name: "ready", status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}, want: controlplane.AccountingAuthorityReady},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStateStore()
			store.readiness = tt.status
			svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
				Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
				Rules:  nil,
			}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

			got, err := svc.Status(context.Background())
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if got.State != tt.want {
				t.Fatalf("status state mismatch: got %q want %q", got.State, tt.want)
			}
		})
	}

	t.Run("query-pages-report-bounded-state", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
		store.limitPage = controlplane.Page[controlplane.AccountingLimitStatusRow]{Unsupported: []controlplane.UnsupportedFilter{{Field: "backend_id", Reason: "not indexed"}}, Visibility: controlplane.VisibilityDefault}
		store.decisionPage = controlplane.Page[controlplane.AccountingDecisionRow]{Visibility: controlplane.VisibilityDefault}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  nil,
		}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

		limits, err := svc.Limits(context.Background(), controlplane.AccountingLimitStatusQuery{Limit: 10})
		if err != nil {
			t.Fatalf("limits: %v", err)
		}
		if limits.State != QueryStateUnsupported {
			t.Fatalf("unsupported limit page must be classified: %#v", limits)
		}
		if len(limits.Page.Unsupported) != 1 {
			t.Fatalf("unsupported filters must be preserved: %#v", limits.Page)
		}

		decisions, err := svc.Decisions(context.Background(), controlplane.AccountingDecisionQuery{Limit: 10})
		if err != nil {
			t.Fatalf("decisions: %v", err)
		}
		if decisions.State != QueryStateEmpty {
			t.Fatalf("empty decision page must be classified: %#v", decisions)
		}
	})

	t.Run("zero-readiness-falls-back-to-snapshot-status", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		store.readiness = domain.AuthorityStatus{}
		store.limitPage = controlplane.Page[controlplane.AccountingLimitStatusRow]{Visibility: controlplane.VisibilityDefault}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  nil,
		}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

		got, err := svc.Status(context.Background())
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if got.State != controlplane.AccountingAuthorityReady {
			t.Fatalf("store zero readiness must fall back to snapshot ready state: %#v", got)
		}

		limits, err := svc.Limits(context.Background(), controlplane.AccountingLimitStatusQuery{Limit: 10})
		if err != nil {
			t.Fatalf("limits: %v", err)
		}
		if limits.State != QueryStateEmpty {
			t.Fatalf("store zero readiness must not collapse limit query to disabled: %#v", limits)
		}

		decisions, err := svc.Decisions(context.Background(), controlplane.AccountingDecisionQuery{Limit: 10})
		if err != nil {
			t.Fatalf("decisions: %v", err)
		}
		if decisions.State != QueryStateEmpty {
			t.Fatalf("store zero readiness must not collapse decision query to disabled: %#v", decisions)
		}
	})

	t.Run("invalid-and-too-broad-queries-are-distinct", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  nil,
		}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

		if _, err := svc.Limits(context.Background(), controlplane.AccountingLimitStatusQuery{}); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("zero limit must be invalid query, got %v", err)
		}

		tooBroad, err := svc.Decisions(context.Background(), controlplane.AccountingDecisionQuery{Limit: 101})
		if err != nil {
			t.Fatalf("too-broad queries should be classified, not rejected: %v", err)
		}
		if tooBroad.State != QueryStateTooBroad {
			t.Fatalf("too-broad query must be classified distinctly: %#v", tooBroad)
		}
	})
}
