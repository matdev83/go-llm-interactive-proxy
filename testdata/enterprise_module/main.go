// Package main is a separate-module enterprise compile/execution fixture
// (requirements 12.6, 12.9, 17.7). It imports only public OSS packages.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

type enterpriseMeter struct{}

func (enterpriseMeter) Append(context.Context, metering.Fact) error { return nil }

type enterpriseQuerier struct{}

func (enterpriseQuerier) List(context.Context, metering.Query) (metering.Page, error) {
	return metering.Page{}, nil
}

type enterpriseEvidence struct {
	policyN int
}

func (e *enterpriseEvidence) RecordPolicyDecision(context.Context, policydecision.Record) error {
	e.policyN++
	return nil
}

func (e *enterpriseEvidence) RecordAccountingAuthority(context.Context, controlplane.Event) error {
	return nil
}

type enterpriseRater struct {
	calls      int
	quoteCalls int
}

func (r *enterpriseRater) Rate(_ context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	r.calls++
	return economics.RatingResult{
		Money:       economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
		Source:      "enterprise-fixture",
		Perspective: req.Perspective,
		RaterID:     "enterprise-fake",
	}, nil
}

func (r *enterpriseRater) QuoteOutputLimit(_ context.Context, req economics.OutputLimitRequest) (economics.OutputLimitResult, error) {
	r.quoteCalls++
	if !req.MaxMoney.Present {
		return economics.OutputLimitResult{
			Status:      economics.OutputLimitUnsupported,
			Reason:      "max_money absent",
			Perspective: req.Perspective,
			RaterID:     "enterprise-fake",
		}, nil
	}
	// Deterministic fixture bound: leave a positive enforceable output limit when
	// any monetary cap is present. Closed modules replace this with real inversion.
	return economics.OutputLimitResult{
		Status:          economics.OutputLimitOK,
		MaxOutputTokens: 1024,
		Source:          "enterprise-fixture",
		Perspective:     req.Perspective,
		RaterID:         "enterprise-fake",
		Version:         economics.VersionRef{ID: "enterprise-fixture", Version: "v1"},
	}, nil
}

type enterpriseRequestProvider struct{}

func (enterpriseRequestProvider) AdmitRequest(_ context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{
		Kind:       authority.DecisionAllow,
		ProviderID: "enterprise-request",
		Stage:      authority.StageRequestAdmit,
	}, nil
}

func (enterpriseRequestProvider) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (enterpriseRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

type enterpriseRuleSource struct{}

func (enterpriseRuleSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	now := time.Now().UTC()
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage_authority", Version: "enterprise-fixture-v1", EffectiveAt: now, FetchedAt: now,
		State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}

func repoConfigPath() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	// Prefer deterministic local-stub dogfood config for Execute smoke (no live keys).
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "config", "examples", "dogfood-local-stub.yaml")), nil
}

func run(ctx context.Context) error {
	cfgPath, err := repoConfigPath()
	if err != nil {
		return err
	}
	rater := &enterpriseRater{}
	evidence := &enterpriseEvidence{}
	querier := enterpriseQuerier{}
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath:          cfgPath,
		MeteringRecorder:    enterpriseMeter{},
		MeteringQuerier:     querier,
		EvidenceSink:        evidence,
		Rater:               rater,
		RequestProviders:    []authority.RequestProvider{enterpriseRequestProvider{}},
		UsageSnapshotSource: enterpriseRuleSource{},
	})
	if err != nil {
		return fmt.Errorf("lipruntime.Build: %w", err)
	}
	defer func() { _ = rt.Close(ctx) }()
	if !rt.Ready() || rt.ExecutorView() == nil {
		return fmt.Errorf("runtime not ready")
	}
	if rt.SnapshotGenerationID() == 0 {
		return fmt.Errorf("expected published generation")
	}
	if !rt.HasProductionEvidenceSink() || !rt.HasProductionRater() || !rt.HasProductionMeteringQuerier() {
		return fmt.Errorf("production evidence/rater/query mounts not wired")
	}
	if _, err := rater.Rate(ctx, economics.RatingRequest{Perspective: metering.PerspectiveOperator}); err != nil {
		return fmt.Errorf("fake rater: %w", err)
	}
	if rater.calls < 1 {
		return fmt.Errorf("expected fake rater invocation")
	}
	var quoter economics.OutputLimitQuoter = rater
	if _, err := quoter.QuoteOutputLimit(ctx, economics.OutputLimitRequest{
		Perspective: metering.PerspectiveOperator,
		MaxMoney:    economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
	}); err != nil {
		return fmt.Errorf("fake output-limit quoter: %w", err)
	}
	if rater.quoteCalls < 1 {
		return fmt.Errorf("expected fake QuoteOutputLimit invocation")
	}
	view := rt.ExecutorView()
	stream, err := view.Execute(ctx, &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "dogfood-local:stub-default"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("ping")}}},
	})
	if err != nil {
		return fmt.Errorf("ExecutorView.Execute: %w", err)
	}
	collected, err := lipapi.Collect(ctx, stream)
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	if collected.Text.String() == "" {
		return fmt.Errorf("expected assistant text from local stub")
	}
	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "enterprise_module: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("enterprise_module: ok")
}
