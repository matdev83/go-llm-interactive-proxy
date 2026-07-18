package runtimebundle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestBuild_RequestRegistration_NoIndexGeneratedIDs(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "prod-quota",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthAdvisory,
					FailureBehavior: authority.FailureFailOpen,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: prodAllowRequest{},
		}},
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
	slots := built.Executor.RequestCoordinator.Slots
	if len(slots) == 0 {
		t.Fatal("expected request slots from registration")
	}
	for _, s := range slots {
		if strings.HasPrefix(s.ID, "production-request-") {
			t.Fatalf("index-generated id %q forbidden", s.ID)
		}
		if s.ID == "prod-quota" {
			if s.Strength != authority.StrengthAdvisory {
				t.Fatalf("strength=%q want advisory", s.Strength)
			}
			if s.FailureBehavior != authority.FailureFailOpen {
				t.Fatalf("failure=%q want fail_open", s.FailureBehavior)
			}
			return
		}
	}
	t.Fatalf("slot prod-quota not found in %+v", slots)
}

func TestBuild_RejectsDuplicateRegistrationIDs(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	desc := authority.ProviderDescriptor{
		ID:   "dup",
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{
			{Descriptor: desc, Priority: authority.RequestPriorityCreditWallet, Provider: prodAllowRequest{}},
			{Descriptor: desc, Priority: authority.RequestPriorityQuotaBudgetRate, Provider: prodAllowRequest{}},
		},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), opts)
	if err == nil {
		t.Fatal("duplicate registration IDs must fail Build")
	}
}

func TestBuild_RegistrationReadinessProviderIDs(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Production = runtimebundle.ProductionOptions{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "ready-quota",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: prodAllowRequest{},
		}},
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
	if built.ReadinessReport == nil {
		t.Fatal("expected readiness report")
	}
	report, err := built.ReadinessReport.Report(context.Background())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	found := false
	for _, c := range report.Components {
		if c.Component != controlplane.ReadinessComponentRequestCoordinator {
			continue
		}
		found = true
		if len(c.ProviderIDs) != 1 || c.ProviderIDs[0] != "ready-quota" {
			t.Fatalf("ProviderIDs=%v want [ready-quota]", c.ProviderIDs)
		}
	}
	if !found {
		t.Fatal("request coordinator component missing")
	}
}

func TestBuild_AttemptAndConcurrencyRegistrations(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Production = runtimebundle.ProductionOptions{
		AttemptRegistrations: []authority.AttemptRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "prod-hard",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageAttemptAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.AttemptPriorityHardSpend,
			Provider: prodAllowAttempt{},
		}},
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: authority.ProviderDescriptor{
				ID:   "prod-lease",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageLeaseAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Provider: prodAllowConcurrency{},
		},
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
	if built.Executor.ConcurrencyProvider == nil {
		t.Fatal("concurrency registration must wire ConcurrencyProvider")
	}
	if built.Executor.AttemptCoordinator == nil || len(built.Executor.AttemptCoordinator.Slots) == 0 {
		t.Fatal("attempt registration must create attempt slots")
	}
	found := false
	for _, s := range built.Executor.AttemptCoordinator.Slots {
		if s.ID == "prod-hard" {
			found = true
			if s.Strength != authority.StrengthRequired || s.FailureBehavior != authority.FailureFailClosed {
				t.Fatalf("attempt slot posture strength=%q failure=%q", s.Strength, s.FailureBehavior)
			}
		}
		if strings.HasPrefix(s.ID, "production-attempt-") {
			t.Fatalf("index-generated attempt id %q forbidden", s.ID)
		}
	}
	if !found {
		t.Fatal("prod-hard attempt slot missing")
	}
}

func TestBuild_RejectsUnboundLegacyAuthoritySlices(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	cases := []struct {
		name string
		prod runtimebundle.ProductionOptions
	}{
		{
			name: "request_providers",
			prod: runtimebundle.ProductionOptions{RequestProviders: []authority.RequestProvider{prodAllowRequest{}}},
		},
		{
			name: "attempt_providers",
			prod: runtimebundle.ProductionOptions{AttemptProviders: []authority.AttemptProvider{prodAllowAttempt{}}},
		},
		{
			name: "concurrency_provider",
			prod: runtimebundle.ProductionOptions{ConcurrencyProvider: prodAllowConcurrency{}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := baseAuthorityOptions(t, nil)
			opts.Production = tc.prod
			_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), opts)
			if err == nil {
				t.Fatal("unbound legacy authority must fail Build")
			}
			if !strings.Contains(err.Error(), "deprecated") {
				t.Fatalf("err=%v want deprecated", err)
			}
		})
	}
}

func TestBuild_CustomerOnlyRater_NotEconomicsRater(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(false, "fail_closed")
	opts := baseAuthorityOptions(t, nil)
	opts.Production = runtimebundle.ProductionOptions{
		RaterRegistrations: []economics.RaterRegistration{{
			ID: "cust-only", Perspective: metering.PerspectiveCustomer, Rater: prodRater{},
		}},
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
	if built.Executor.EconomicsRater != nil {
		t.Fatal("customer-only rater must not fill EconomicsRater")
	}
	report, err := built.ReadinessReport.Report(context.Background())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	for _, c := range report.Components {
		switch c.Component {
		case controlplane.ReadinessComponentCustomerRater:
			if c.State != controlplane.CapabilityReady || len(c.ProviderIDs) != 1 || c.ProviderIDs[0] != "cust-only" {
				t.Fatalf("customer rater component=%+v", c)
			}
		case controlplane.ReadinessComponentOperatorRater:
			if c.State == controlplane.CapabilityReady {
				t.Fatal("operator rater must stay disabled for customer-only")
			}
		}
	}
}

type prodAllowAttempt struct{}

func (prodAllowAttempt) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (prodAllowAttempt) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (prodAllowAttempt) ReleaseAttempt(context.Context, authority.AttemptRelease) error { return nil }

type prodAllowConcurrency struct{}

func (prodAllowConcurrency) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "l1"}, nil
}

func (prodAllowConcurrency) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "l1"}, nil
}

func (prodAllowConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }

func (prodAllowConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}
