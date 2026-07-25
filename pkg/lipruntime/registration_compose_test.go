package lipruntime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestBuild_DescriptorBoundRequestRegistration_PreservesID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-quota",
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
	report, err := rt.ReadinessReport().Report(ctx)
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}
	ids := componentProviderIDs(report, controlplane.ReadinessComponentRequestCoordinator)
	if len(ids) != 1 || ids[0] != "enterprise-quota" {
		t.Fatalf("request coordinator provider ids=%v want [enterprise-quota]", ids)
	}
	for _, id := range ids {
		if strings.HasPrefix(id, "production-request-") {
			t.Fatalf("index-generated id %q forbidden", id)
		}
	}
}

func TestBuild_RejectsDuplicateRequestRegistrationIDs(t *testing.T) {
	t.Parallel()
	desc := func(id string) authority.ProviderDescriptor {
		return authority.ProviderDescriptor{
			ID:   id,
			Kind: authority.ProviderKindAuthority,
			Postures: []authority.StagePosture{{
				Stage:           authority.StageRequestAdmit,
				Strength:        authority.StrengthRequired,
				FailureBehavior: authority.FailureFailClosed,
			}},
		}
	}
	_, err := lipruntime.Build(context.Background(), lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{
			{Descriptor: desc("dup"), Priority: authority.RequestPriorityCreditWallet, Provider: allowRequestProvider{}},
			{Descriptor: desc("dup"), Priority: authority.RequestPriorityQuotaBudgetRate, Provider: allowRequestProvider{}},
		},
	})
	if err == nil {
		t.Fatal("duplicate request registration IDs must fail startup")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuild_RaterRegistration_ReadinessIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RaterRegistrations: []economics.RaterRegistration{{
			ID:          "enterprise-operator-rater",
			Perspective: metering.PerspectiveOperator,
			Rater:       &recordingRater{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.HasProductionRater() {
		t.Fatal("operator rater registration must attach EconomicsRater")
	}
	report, err := rt.ReadinessReport().Report(ctx)
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}
	ids := componentProviderIDs(report, controlplane.ReadinessComponentOperatorRater)
	if len(ids) != 1 || ids[0] != "enterprise-operator-rater" {
		t.Fatalf("operator rater ids=%v want [enterprise-operator-rater]", ids)
	}
}

func TestBuild_AttemptRegistration_PreservesID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		AttemptRegistrations: []authority.AttemptRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-hard-spend",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageAttemptAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.AttemptPriorityHardSpend,
			Provider: allowAttemptProvider{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	report, err := rt.ReadinessReport().Report(ctx)
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}
	ids := componentProviderIDs(report, controlplane.ReadinessComponentAttemptCoordinator)
	if len(ids) != 1 || ids[0] != "enterprise-hard-spend" {
		t.Fatalf("attempt coordinator provider ids=%v want [enterprise-hard-spend]", ids)
	}
}

func TestBuild_ConcurrencyRegistration_WiresProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-concurrency",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageLeaseAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Provider: allowConcurrencyProvider{},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime with concurrency registration")
	}
}

func TestBuild_RejectsSettleOnlyRequestRegistration(t *testing.T) {
	t.Parallel()
	_, err := lipruntime.Build(context.Background(), lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "settle-only",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestSettle,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: allowRequestProvider{},
		}},
	})
	if err == nil {
		t.Fatal("settle-only request registration must fail Build")
	}
	if !strings.Contains(err.Error(), "admit") {
		t.Fatalf("err=%v want admit posture error", err)
	}
}

func TestBuild_CustomerOnlyRater_DoesNotAttachOperatorEconomicsRater(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RaterRegistrations: []economics.RaterRegistration{{
			ID:          "enterprise-customer-rater",
			Perspective: metering.PerspectiveCustomer,
			Rater:       &recordingRater{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if rt.HasProductionRater() {
		t.Fatal("customer-only rater must not attach operator EconomicsRater")
	}
	report, err := rt.ReadinessReport().Report(ctx)
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}
	cust := componentProviderIDs(report, controlplane.ReadinessComponentCustomerRater)
	if len(cust) != 1 || cust[0] != "enterprise-customer-rater" {
		t.Fatalf("customer rater ids=%v want [enterprise-customer-rater]", cust)
	}
	op := componentProviderIDs(report, controlplane.ReadinessComponentOperatorRater)
	if len(op) != 0 {
		t.Fatalf("operator rater ids=%v want empty for customer-only", op)
	}
	for _, c := range report.Components {
		if c.Component == controlplane.ReadinessComponentOperatorRater && c.State == controlplane.CapabilityReady {
			t.Fatal("operator rater must not be ready for customer-only registration")
		}
		if c.Component == controlplane.ReadinessComponentCustomerRater && c.State != controlplane.CapabilityReady {
			t.Fatalf("customer rater state=%s want ready", c.State)
		}
	}
}

func TestBuild_MixedRaterRegistrations_SelectsOperator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RaterRegistrations: []economics.RaterRegistration{
			{ID: "customer-first", Perspective: metering.PerspectiveCustomer, Rater: &recordingRater{}},
			{ID: "operator-second", Perspective: metering.PerspectiveOperator, Rater: &recordingRater{}},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.HasProductionRater() {
		t.Fatal("mixed registrations must attach operator EconomicsRater")
	}
	report, err := rt.ReadinessReport().Report(ctx)
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}
	op := componentProviderIDs(report, controlplane.ReadinessComponentOperatorRater)
	if len(op) != 1 || op[0] != "operator-second" {
		t.Fatalf("operator rater ids=%v want [operator-second]", op)
	}
	cust := componentProviderIDs(report, controlplane.ReadinessComponentCustomerRater)
	if len(cust) != 1 || cust[0] != "customer-first" {
		t.Fatalf("customer rater ids=%v want [customer-first]", cust)
	}
}

type allowAttemptProvider struct{}

func (allowAttemptProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: "enterprise-hard-spend"}, nil
}

func (allowAttemptProvider) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (allowAttemptProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

type allowConcurrencyProvider struct{}

func (allowConcurrencyProvider) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "lease-1"}, nil
}

func (allowConcurrencyProvider) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "lease-1"}, nil
}

func (allowConcurrencyProvider) ReleaseLease(context.Context, authority.LeaseRelease) error {
	return nil
}

func (allowConcurrencyProvider) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func componentProviderIDs(report controlplane.ReadinessReport, component controlplane.ReadinessComponentID) []string {
	for _, c := range report.Components {
		if c.Component == component {
			return append([]string(nil), c.ProviderIDs...)
		}
	}
	return nil
}
