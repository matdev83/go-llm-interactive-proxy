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

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type enterpriseMeter struct{}

func (enterpriseMeter) Append(context.Context, metering.Fact) error { return nil }

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
	// testdata/enterprise_module -> repo root config
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "config", "config.yaml")), nil
}

func run(ctx context.Context) error {
	cfgPath, err := repoConfigPath()
	if err != nil {
		return err
	}
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath:          cfgPath,
		MeteringRecorder:    enterpriseMeter{},
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
	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "enterprise_module: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("enterprise_module: ok")
}
