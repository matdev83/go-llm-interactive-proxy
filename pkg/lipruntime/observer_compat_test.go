package lipruntime_test

import (
	"context"
	"strings"
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
		ProviderDescriptors: []authority.ProviderDescriptor{{
			ID:   "traffic-observer",
			Kind: authority.ProviderKindObserver,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageRequestAdmit,
				Strength:        authority.StrengthAdvisory,
				FailureBehavior: authority.FailureFailOpen,
			}},
		}},
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

func TestBuild_RejectsObserverRequiredStrength(t *testing.T) {
	t.Parallel()
	_, err := lipruntime.Build(context.Background(), lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		ProviderDescriptors: []authority.ProviderDescriptor{{
			ID:   "bad-observer",
			Kind: authority.ProviderKindObserver,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageRequestAdmit,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		}},
	})
	if err == nil {
		t.Fatal("expected observer+required to fail (requirement 12.7)")
	}
	if !strings.Contains(err.Error(), "observer cannot declare required strength") {
		t.Fatalf("err=%v", err)
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
