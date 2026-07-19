package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

type previewClampProvider struct {
	previewCalls atomic.Int32
	admitCalls   atomic.Int32
	value        int64
}

func (p *previewClampProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	p.admitCalls.Add(1)
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (p *previewClampProvider) SettleAttempt(context.Context, authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (p *previewClampProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

func (p *previewClampProvider) PreviewAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	p.previewCalls.Add(1)
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Clamps: []authority.Clamp{{
			Kind:  authority.ClampMaxOutputTokens,
			Value: p.value,
		}},
	}, nil
}

func TestPreviewAndApplyAttemptClamps_AppliesViaCoordinator(t *testing.T) {
	t.Parallel()

	prov := &previewClampProvider{value: 42}
	ex := &Executor{}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID:       "preview",
			Class:    authoritycoord.AttemptPriorityHardSpend,
			Provider: prov,
			Strength: authority.StrengthRequired,
		}},
	}
	max := 1000
	call := lipapi.Call{ID: "req-1", Options: lipapi.GenerationOptions{MaxOutputTokens: &max}}
	err := ex.previewAndApplyAttemptClamps(
		context.Background(),
		"trace-1",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-1", Seq: 1},
		&call,
		authorityCandidate(),
		accountingpreflight.Decision{},
	)
	if err != nil {
		t.Fatalf("previewAndApplyAttemptClamps: %v", err)
	}
	if prov.previewCalls.Load() < 1 {
		t.Fatal("expected PreviewAttempt via coordinator")
	}
	if prov.admitCalls.Load() != 0 {
		t.Fatalf("AdmitAttempt must not run during preview; calls=%d", prov.admitCalls.Load())
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 42 {
		t.Fatalf("MaxOutputTokens=%v want 42", call.Options.MaxOutputTokens)
	}
}

type previewUAService struct {
	previewAdmitRecorder
	calls atomic.Int32
}

func (s *previewUAService) Admit(ctx context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	s.calls.Add(1)
	return s.previewAdmitRecorder.Admit(ctx, in)
}

func TestPreviewAndApplyAttemptClamps_NilCoordinatorUsesUsageAuthority(t *testing.T) {
	t.Parallel()

	svc := &previewUAService{}
	ex := &Executor{}
	ex.UsageAuthority = svc
	// AttemptCoordinator intentionally nil.
	max := 100
	call := lipapi.Call{ID: "req-ua", Options: lipapi.GenerationOptions{MaxOutputTokens: &max}}
	err := ex.previewAndApplyAttemptClamps(
		context.Background(),
		"trace-ua",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-ua", Seq: 1},
		&call,
		authorityCandidate(),
		accountingpreflight.Decision{},
	)
	if err != nil {
		t.Fatalf("previewAndApplyAttemptClamps: %v", err)
	}
	if svc.calls.Load() < 1 {
		t.Fatal("expected UsageAuthority Admit via adapter PreviewAttempt when coordinator is nil")
	}
	if !svc.last.EstimateOnly || !svc.last.SkipEvidence {
		t.Fatalf("nil-coordinator preview must be EstimateOnly+SkipEvidence; got EstimateOnly=%v SkipEvidence=%v",
			svc.last.EstimateOnly, svc.last.SkipEvidence)
	}
}

func TestPreviewAndApplyAttemptClamps_AppliesSpendMoneyClamp(t *testing.T) {
	t.Parallel()

	prov := &previewSpendClampProvider{nano: 4_000_000_000} // $4 → 1M output after $2 input at test catalog rates
	ex := &Executor{AccountingRuntime: AccountingRuntime{AccountingPriceCatalog: clampTestCatalog(t)}}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID:       "spend",
			Class:    authoritycoord.AttemptPriorityHardSpend,
			Provider: prov,
			Strength: authority.StrengthRequired,
		}},
	}
	call := lipapi.Call{ID: "req-spend"}
	err := ex.previewAndApplyAttemptClamps(
		context.Background(),
		"trace-spend",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-spend", Seq: 1},
		&call,
		clampTestCandidate(),
		accountingpreflight.Decision{Count: accountingapp.CountResult{InputTokens: 1_000_000}},
	)
	if err != nil {
		t.Fatalf("previewAndApplyAttemptClamps: %v", err)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 500_000 {
		// Input 1M tokens @ $2/1M = $2; remaining $2 @ $4/1M output = 0.5M tokens.
		t.Fatalf("MaxOutputTokens=%v want 500000 from spend clamp", call.Options.MaxOutputTokens)
	}
}

type previewSpendClampProvider struct {
	nano int64
}

func (p *previewSpendClampProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (p *previewSpendClampProvider) SettleAttempt(context.Context, authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (p *previewSpendClampProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

func (p *previewSpendClampProvider) PreviewAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Clamps: []authority.Clamp{{
			Kind: authority.ClampMaxSpend,
			Money: economics.Money{
				NanoUnits: p.nano,
				Currency:  "USD",
				Present:   true,
			},
		}},
	}, nil
}
