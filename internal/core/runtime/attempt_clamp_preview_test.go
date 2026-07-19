package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// previewUAService counts Admit calls through the recorder so the
// nil-coordinator fallback test can assert the adapter path actually ran.
type previewUAService struct {
	previewAdmitRecorder
	calls atomic.Int32
}

func (s *previewUAService) Admit(ctx context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	s.calls.Add(1)
	return s.previewAdmitRecorder.Admit(ctx, in)
}

// TestPreviewAndApplyAttemptClamps_NilCoordinatorUsesUsageAuthority proves the
// V-15 fallback: single-provider deployments without a multi-provider
// AttemptCoordinator still get bounded, side-effect-free clamp preview
// through the direct UsageAuthority adapter instead of skipping preview.
func TestPreviewAndApplyAttemptClamps_NilCoordinatorUsesUsageAuthority(t *testing.T) {
	t.Parallel()

	svc := &previewUAService{}
	ex := &Executor{}
	ex.UsageAuthority = svc
	// AttemptCoordinator intentionally nil.
	max := 100
	call := lipapi.Call{ID: "req-ua", Options: lipapi.GenerationOptions{MaxOutputTokens: &max}}
	_, ran, err := ex.previewAndApplyAttemptClamps(context.Background(), &call, authorityCandidate(), "a-1", "b-ua")
	if err != nil {
		t.Fatalf("previewAndApplyAttemptClamps: %v", err)
	}
	if !ran {
		t.Fatal("expected preview to run via nil-coordinator UsageAuthority fallback")
	}
	if svc.calls.Load() < 1 {
		t.Fatal("expected UsageAuthority Admit via adapter PreviewAttempt when coordinator is nil")
	}
	if !svc.last.EstimateOnly || !svc.last.SkipEvidence {
		t.Fatalf("nil-coordinator preview must be EstimateOnly+SkipEvidence; got EstimateOnly=%v SkipEvidence=%v",
			svc.last.EstimateOnly, svc.last.SkipEvidence)
	}
}

// TestPreviewAndApplyAttemptClamps_AppliesSpendMoneyClamp proves a
// ClampMaxSpend preview decision flows through the bounded preview loop into
// a real catalog-rate MaxOutputTokens narrowing. Input-before-output cost
// ordering is covered directly by TestAuthorityClampChargesInputBeforeOutput.
func TestPreviewAndApplyAttemptClamps_AppliesSpendMoneyClamp(t *testing.T) {
	t.Parallel()

	prov := &previewSpendClampProvider{nano: 4_000_000_000} // $4 @ $4/1M output, zero input tokens => 1M tokens
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
	_, ran, err := ex.previewAndApplyAttemptClamps(context.Background(), &call, clampTestCandidate(), "a-1", "b-spend")
	if err != nil {
		t.Fatalf("previewAndApplyAttemptClamps: %v", err)
	}
	if !ran {
		t.Fatal("expected preview to run via coordinator")
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 1_000_000 {
		t.Fatalf("MaxOutputTokens=%v want 1000000 from spend clamp", call.Options.MaxOutputTokens)
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
