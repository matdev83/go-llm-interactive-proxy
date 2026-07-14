package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type prodMeter struct{}

func (prodMeter) Append(context.Context, metering.Fact) error { return nil }

type prodRuleSource struct {
	ver string
}

func (p prodRuleSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage_authority", Version: p.ver, State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}

type prodAllowRequest struct{}

func (prodAllowRequest) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}
func (prodAllowRequest) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}
func (prodAllowRequest) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

func TestBuild_ProductionOptionsOutsideTesting(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Production = runtimebundle.ProductionOptions{
		MeteringRecorder:    prodMeter{},
		RequestProviders:    []authority.RequestProvider{prodAllowRequest{}},
		UsageSnapshotSource: prodRuleSource{ver: "prod-snap-v1"},
	}
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() {
		for _, c := range built.Closers {
			_ = c()
		}
	})
	if built.Executor == nil || built.Executor.MeteringRecorder == nil {
		t.Fatal("production metering must attach on executor")
	}
	if built.Executor.RequestCoordinator == nil || len(built.Executor.RequestCoordinator.Slots) == 0 {
		t.Fatal("production request provider must create coordinator slots")
	}
	cur := built.SnapshotGeneration.Current()
	if cur == nil || cur.Usage.Version != "prod-snap-v1" {
		t.Fatalf("generation usage=%+v", cur)
	}
	_ = time.Now()
}
