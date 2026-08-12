package runtime

import (
	"context"
	"testing"
	"time"

	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// previewAdmitRecorder captures Admit inputs from usageAuthorityProviderAdapter.
type previewAdmitRecorder struct {
	last authorityapp.AdmissionInput
}

func (r *previewAdmitRecorder) Admit(_ context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	r.last = in
	return authorityapp.AdmissionResult{Allowed: true}, nil
}

func (r *previewAdmitRecorder) Settle(context.Context, authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	return authorityapp.SettleResult{}, nil
}

func (r *previewAdmitRecorder) Release(context.Context, authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	return authorityapp.ReleaseResult{}, nil
}

func (r *previewAdmitRecorder) ApplyUsage(context.Context, authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{}, nil
}

func TestPreviewAttemptSkipsDurableEvidence(t *testing.T) {
	t.Parallel()

	rec := &previewAdmitRecorder{}
	adapter := newUsageAuthorityProviderAdapter(rec)
	_, err := adapter.PreviewAttempt(context.Background(), authority.AttemptAdmission{
		RequestID: "req-1",
		AttemptID: "b-1",
		BLegID:    "b-1",
		ALegID:    "a-1",
		BackendID: "backend-1",
		Model:     "model-1",
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Money: economics.Money{
				NanoUnits: 100,
				Currency:  "USD",
				Present:   true,
			},
		},
	})
	if err != nil {
		t.Fatalf("PreviewAttempt: %v", err)
	}
	if !rec.last.EstimateOnly {
		t.Fatal("PreviewAttempt must use EstimateOnly")
	}
	if !rec.last.SkipEvidence {
		t.Fatal("PreviewAttempt must set SkipEvidence so Admit does not record durable evidence")
	}
}

func TestAdmitAttemptRecordsEvidence(t *testing.T) {
	t.Parallel()

	rec := &previewAdmitRecorder{}
	adapter := newUsageAuthorityProviderAdapter(rec)
	_, err := adapter.AdmitAttempt(context.Background(), authority.AttemptAdmission{
		RequestID: "req-1",
		AttemptID: "b-1",
		BLegID:    "b-1",
		ALegID:    "a-1",
	})
	if err != nil {
		t.Fatalf("AdmitAttempt: %v", err)
	}
	if rec.last.EstimateOnly {
		t.Fatal("AdmitAttempt must not be estimate-only")
	}
	if rec.last.SkipEvidence {
		t.Fatal("AdmitAttempt must record durable evidence")
	}
}

func TestMapAdmissionDecision_SpendClampRetiredFromAuthorityAdapter(t *testing.T) {
	t.Parallel()
	d := mapAdmissionDecision(authorityapp.AdmissionResult{
		Allowed: true,
		Clamp: &authorityapp.AdmissionClamp{
			RuleID: "tenant.spend_cap",
			EffectiveMax: domain.Amount{
				Unit:     domain.AmountUnitMoneyNano,
				Value:    700,
				Currency: "usd",
			},
		},
	}, "attempt-ua", authority.StageAttemptAdmit)
	if len(d.Clamps) != 0 {
		t.Fatalf("clamps=%+v want none (BillingAdmission owns monetary clamps)", d.Clamps)
	}
}

// previewEvidenceSink proves SkipEvidence through the real usageauthority Service path.
type previewEvidenceSink struct {
	policy     []policydecision.Record
	accounting []controlplane.Event
}

func (s *previewEvidenceSink) RecordPolicyDecision(_ context.Context, record policydecision.Record) error {
	s.policy = append(s.policy, record)
	return nil
}

func (s *previewEvidenceSink) RecordAccountingAuthority(_ context.Context, ev controlplane.Event) error {
	s.accounting = append(s.accounting, ev)
	return nil
}

type previewRuleSource struct {
	snap authorityapp.RuleSnapshot
}

func (s previewRuleSource) Snapshot(context.Context) (authorityapp.RuleSnapshot, error) {
	return s.snap, nil
}

type previewFixedClock struct{ now time.Time }

func (c previewFixedClock) Now() time.Time { return c.now }

// previewStateStore is a minimal StateStore that never mutates and reports ready.
type previewStateStore struct{}

func (previewStateStore) Reserve(context.Context, authorityapp.ReserveCommand) (authorityapp.ReserveResult, error) {
	return authorityapp.ReserveResult{}, nil
}

func (previewStateStore) Settle(context.Context, authorityapp.SettleCommand) (authorityapp.SettleResult, error) {
	return authorityapp.SettleResult{}, nil
}

func (previewStateStore) Release(context.Context, authorityapp.ReleaseCommand) (authorityapp.ReleaseResult, error) {
	return authorityapp.ReleaseResult{}, nil
}

func (previewStateStore) ApplyUsage(context.Context, authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{}, nil
}

func (previewStateStore) ActiveLimit(context.Context, authorityapp.ActiveLimitQuery) (controlplane.AccountingLimitStatusRow, bool, error) {
	return controlplane.AccountingLimitStatusRow{}, false, nil
}

func (previewStateStore) LimitStatus(context.Context, controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, nil
}

func (previewStateStore) DecisionHistory(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return controlplane.Page[controlplane.AccountingDecisionRow]{}, nil
}

func (previewStateStore) CheckReadiness(context.Context) (domain.AuthorityStatus, error) {
	return domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}, nil
}

func TestPreviewAttempt_RealServiceSkipsEvidenceSink(t *testing.T) {
	t.Parallel()

	evidence := &previewEvidenceSink{}
	svc := authorityapp.NewService(
		previewRuleSource{snap: authorityapp.RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules: []domain.Rule{{
				ID:    "tenant.requests",
				Kind:  domain.RuleKindQuota,
				Mode:  domain.RuleModeStrict,
				Unit:  domain.AmountUnitRequests,
				Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
				Match: domain.DimensionsMatcher{
					Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
				},
			}},
		}},
		previewStateStore{},
		evidence,
		previewFixedClock{now: time.Unix(100, 0).UTC()},
	)
	adapter := newUsageAuthorityProviderAdapter(svc)

	admission := authority.AttemptAdmission{
		RequestID: "req-preview-svc",
		AttemptID: "b-preview-svc",
		BLegID:    "b-preview-svc",
		ALegID:    "a-1",
		BackendID: "backend-1",
		Model:     "model-1",
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Money:       economics.Money{NanoUnits: 100, Currency: "USD", Present: true},
		},
	}

	if _, err := adapter.PreviewAttempt(context.Background(), admission); err != nil {
		t.Fatalf("PreviewAttempt: %v", err)
	}
	if len(evidence.policy) != 0 || len(evidence.accounting) != 0 {
		t.Fatalf("PreviewAttempt must not record durable evidence: policy=%d accounting=%d",
			len(evidence.policy), len(evidence.accounting))
	}

	if _, err := adapter.AdmitAttempt(context.Background(), admission); err != nil {
		t.Fatalf("AdmitAttempt: %v", err)
	}
	if len(evidence.policy) == 0 && len(evidence.accounting) == 0 {
		t.Fatal("AdmitAttempt control path must record durable evidence (proves SkipEvidence is what suppressed preview)")
	}
}
