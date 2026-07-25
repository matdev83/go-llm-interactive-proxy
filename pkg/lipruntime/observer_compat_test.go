package lipruntime_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

type noopTraffic struct{}

func (noopTraffic) OnObservation(context.Context, traffic.Observation) error { return nil }

type noopUsage struct{}

func (noopUsage) OnUsage(context.Context, usage.Event) error { return nil }

func TestBuild_PreservesFeatureObservers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath:       repoConfigPath(t),
		TrafficObservers: []traffic.Observer{noopTraffic{}},
		UsageObservers:   []usage.Observer{noopUsage{}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.HasTrafficObservers() || !rt.HasUsageObservers() {
		t.Fatal("production observers must remain supported through the facade")
	}
	if !rt.Ready() {
		t.Fatal("expected ready runtime")
	}
}

func TestBuild_AuthorityDescriptorAllowed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-authority",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: allowRequestProvider{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready")
	}
}
