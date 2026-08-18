package runtimebundle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreRuntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestComposeBilling(t *testing.T) {
	t.Parallel()

	t.Run("complete injection accepted by BuildHost", func(t *testing.T) {
		t.Parallel()
		in, store, pricing, policy := validComposeInput(t)
		prod, err := runtimebundle.ComposeBilling(in)
		if err != nil {
			t.Fatalf("ComposeBilling: %v", err)
		}
		assertCompleteProduction(t, prod, store, in)

		cfg := composeBillingHostConfig()
		_, bundle := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
			PluginRegistry: pluginreg.NewRegistry(),
			Production:     prod,
		})
		if bundle.Executor() == nil || !bundle.Executor().BillingAuthoritative {
			t.Fatal("BuildHost did not accept composed authoritative billing")
		}
		if bundle.Executor().TerminalUsageSink == nil {
			t.Fatal("TerminalUsageSink was not wired")
		}
		httpIn := bundle.StandardHTTPInput(cfg, nil, "")
		if httpIn.Operations.BillingReportsPath != in.ReportsPath {
			t.Fatalf("reports path = %q, want %q", httpIn.Operations.BillingReportsPath, in.ReportsPath)
		}
		if httpIn.Operations.BillingReports == nil {
			t.Fatal("composed reports were not mounted from the injected store")
		}
		got, ok := httpIn.Operations.BillingProvisioner.(*completeJournal)
		if !ok || got != store {
			t.Fatalf("BillingProvisioner = %T, want injected completeJournal", httpIn.Operations.BillingProvisioner)
		}

		assertCatalogBackedAdmission(t, prod, store, pricing, policy)
		if err := bundle.Quiesce(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = bundle.Close()
	})

	t.Run("incomplete input fails closed before BuildHost", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name string
			mut  func(*runtimebundle.ComposeBillingInput)
		}{
			{name: "nil store", mut: func(in *runtimebundle.ComposeBillingInput) { in.Store = nil }},
			{name: "nil catalog", mut: func(in *runtimebundle.ComposeBillingInput) { in.Catalog = nil }},
			{name: "catalog defaults missing", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Catalog = billingcompose.NewSnapshotCatalog()
			}},
			{name: "empty currency", mut: func(in *runtimebundle.ComposeBillingInput) { in.Currency = "" }},
			{name: "whitespace currency", mut: func(in *runtimebundle.ComposeBillingInput) { in.Currency = "   " }},
			{name: "nil model max-output", mut: func(in *runtimebundle.ComposeBillingInput) { in.ModelMaxOutput = nil }},
			{name: "store missing usage record sink", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutTerminalSink{}
			}},
			{name: "store missing call-leg sink", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutCallLegSink{}
			}},
			{name: "store missing call sink", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutCallSink{}
			}},
			{name: "store missing exposure admission", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutExposure{}
			}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				in, _, _, _ := validComposeInput(t)
				tt.mut(&in)
				prod, err := runtimebundle.ComposeBilling(in)
				if !errors.Is(err, runtimebundle.ErrComposeBillingIncomplete) {
					t.Fatalf("ComposeBilling = %v, want ErrComposeBillingIncomplete", err)
				}
				if prod.BillingAuthoritative || prod.BillingStore != nil ||
					prod.BillingReports != nil || prod.BillingExposureAdmission != nil {
					t.Fatalf("incomplete compose returned a partial Production: %+v", prod)
				}
			})
		}
	})

	t.Run("nil identity uses stock principal session mapping", func(t *testing.T) {
		t.Parallel()
		in, _, pricing, policy := validComposeInput(t)
		in.Identity = nil
		prod, err := runtimebundle.ComposeBilling(in)
		if err != nil {
			t.Fatalf("ComposeBilling: %v", err)
		}
		if prod.BillingIdentity.AccountID == nil {
			t.Fatal("stock account identity resolver is required")
		}
		ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
			PrincipalID: scope.Known("acct-stock"),
		})
		call := lipapi.Call{Session: lipapi.SessionRef{
			AuthoritativeSessionID: "sess-stock",
			ClientSessionID:        "client-hint-must-ignore",
		}}
		if got := prod.BillingIdentity.AccountID(ctx, call); got != "acct-stock" {
			t.Fatalf("AccountID = %q, want principal id", got)
		}
		if prod.BillingIdentity.CustomerPricingRef == nil || prod.BillingIdentity.ChargePolicyRef == nil ||
			prod.BillingIdentity.OperatorRateRef == nil {
			t.Fatal("stock identity must stamp catalog snapshot refs")
		}
		if got := prod.BillingIdentity.CustomerPricingRef(ctx, call); got.ID != pricing.Ref.ID || got.Version != pricing.Ref.Version {
			t.Fatalf("CustomerPricingRef = %+v, want %+v", got, pricing.Ref)
		}
		if got := prod.BillingIdentity.ChargePolicyRef(ctx, call); got.ID != policy.Ref.ID || got.Version != policy.Ref.Version {
			t.Fatalf("ChargePolicyRef = %+v, want %+v", got, policy.Ref)
		}
	})

	t.Run("custom mapping is used when provided", func(t *testing.T) {
		t.Parallel()
		in, _, _, _ := validComposeInput(t)
		custom := coreRuntime.BillingIdentity{
			AccountID: func(context.Context, lipapi.Call) string { return "custom-acct" },
		}
		in.Identity = &custom
		prod, err := runtimebundle.ComposeBilling(in)
		if err != nil {
			t.Fatalf("ComposeBilling: %v", err)
		}
		ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-must-not-win"),
		})
		if got := prod.BillingIdentity.AccountID(ctx, lipapi.Call{}); got != "custom-acct" {
			t.Fatalf("AccountID = %q, want custom mapping", got)
		}
	})

	t.Run("custom empty identity fails closed at admission", func(t *testing.T) {
		t.Parallel()
		in, _, _, _ := validComposeInput(t)
		empty := coreRuntime.BillingIdentity{
			AccountID: func(context.Context, lipapi.Call) string { return "" },
		}
		in.Identity = &empty
		prod, err := runtimebundle.ComposeBilling(in)
		if err != nil {
			t.Fatalf("ComposeBilling: %v", err)
		}
		if prod.BillingExposureAdmission == nil {
			t.Fatal("exposure-only composition must retain operational admission")
		}
		_, err = prod.BillingExposureAdmission.Admit(context.Background(), coreRuntime.BillingExposureAdmissionInput{
			BillingAdmissionInput: coreRuntime.BillingAdmissionInput{
				Call: lipapi.Call{}, ALegID: "a-leg-1",
				Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &routing.Primary{Backend: "backend", Model: "model"}}}},
				RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
			},
			CallID: "bc_00000000000000000000000000000000",
		})
		if !errors.Is(err, billing.ErrExposureInvalid) {
			t.Fatalf("exposure admission = %v, want identity/store validation", err)
		}
	})
}

func assertCompleteProduction(t *testing.T, prod runtimebundle.ProductionOptions, store *completeJournal, in runtimebundle.ComposeBillingInput) {
	t.Helper()
	if !prod.BillingAuthoritative {
		t.Fatal("BillingAuthoritative was not enabled")
	}
	if prod.BillingStore != billing.AuthoritativeBilling(store) {
		t.Fatal("BillingStore is not the injected journal")
	}
	if prod.BillingReports != billing.ReportingStore(store) {
		t.Fatal("BillingReports is not the injected journal")
	}
	if prod.BillingTerminalUsageSink == nil {
		t.Fatal("BillingTerminalUsageSink is required")
	}
	if prod.BillingCreditGate == nil {
		t.Fatal("BillingCreditGate is required for authoritative composition")
	}
	if prod.BillingExposureAdmission == nil {
		t.Fatal("BillingExposureAdmission is required for authoritative composition")
	}
	if prod.BillingCallRatingResolver == nil {
		t.Fatal("BillingCallRatingResolver is required")
	}
	if prod.BillingIdentity.AccountID == nil {
		t.Fatal("BillingIdentity account resolver is required")
	}
	if prod.BillingReportsPath != in.ReportsPath {
		t.Fatalf("BillingReportsPath = %q, want %q", prod.BillingReportsPath, in.ReportsPath)
	}
	if prod.BillingPostTurnBatchSize != in.PostTurnBatchSize {
		t.Fatalf("BillingPostTurnBatchSize = %d, want %d", prod.BillingPostTurnBatchSize, in.PostTurnBatchSize)
	}
	if prod.BillingPostTurnInterval != in.PostTurnInterval {
		t.Fatalf("BillingPostTurnInterval = %s, want %s", prod.BillingPostTurnInterval, in.PostTurnInterval)
	}

	if prod.BillingCallRatingResolver == nil {
		t.Fatal("call rating resolver is required")
	}
}

func assertCatalogBackedAdmission(
	t *testing.T,
	prod runtimebundle.ProductionOptions,
	store *completeJournal,
	pricing billing.PricingSnapshot,
	policy billing.ChargePolicy,
) {
	t.Helper()
	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		PrincipalID: scope.Known("acct-compose"),
	})
	primary := routing.Primary{Backend: "openai-responses", Model: "gpt"}
	exposure, err := prod.BillingExposureAdmission.Admit(ctx, coreRuntime.BillingExposureAdmissionInput{
		BillingAdmissionInput: coreRuntime.BillingAdmissionInput{
			Call:        lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-compose"}},
			ALegID:      "a-compose",
			Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
			RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
		},
		CallID: "bc_00000000000000000000000000000000",
	})
	if err != nil {
		t.Fatalf("catalog-backed exposure admission: %v", err)
	}
	if exposure.PricingRef.ID != pricing.Ref.ID || exposure.PricingRef.Version != pricing.Ref.Version {
		t.Fatalf("exposure pricing ref = %+v, want catalog %+v", exposure.PricingRef, pricing.Ref)
	}
	if exposure.ChargePolicyRef.ID != policy.Ref.ID || exposure.ChargePolicyRef.Version != policy.Ref.Version {
		t.Fatalf("exposure policy ref = %+v, want catalog %+v", exposure.ChargePolicyRef, policy.Ref)
	}
}

func composeBillingHostConfig() *config.Config {
	return &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Accounting: config.AccountingConfig{Billing: config.AccountingBillingConfig{Authoritative: true, ReportsPath: "/admin/billing"}},
	}
}

func validComposeInput(t *testing.T) (runtimebundle.ComposeBillingInput, *completeJournal, billing.PricingSnapshot, billing.ChargePolicy) {
	t.Helper()
	catalog, pricing, policy := seededComposeCatalog(t)
	store := &completeJournal{}
	return runtimebundle.ComposeBillingInput{
		Store:               store,
		TerminalUsageSink:   store,
		Catalog:             catalog,
		Currency:            "USD",
		ModelMaxOutput:      composeModelMax,
		ReportsPath:         "/admin/billing-composed",
		PostTurnBatchSize:   8,
		PostTurnInterval:    50 * time.Millisecond,
		Strict:              true,
		ConservativeCeiling: &billing.Money{Nano: 1_000, Currency: "USD"},
	}, store, pricing, policy
}

func composeModelMax(context.Context, string, string) (int64, bool, error) {
	return 128, true, nil
}

func seededComposeCatalog(t *testing.T) (*billingcompose.SnapshotCatalog, billing.PricingSnapshot, billing.ChargePolicy) {
	t.Helper()
	c := billingcompose.NewSnapshotCatalog()
	pricing := composeCatalogPricing()
	policy := composeCatalogPolicy()
	if err := c.PutPricing(pricing); err != nil {
		t.Fatal(err)
	}
	if err := c.PutPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := c.PutOperatorRate(composeCatalogOperatorRate()); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatal(err)
	}
	return c, pricing, policy
}

func composeCatalogPricing() billing.PricingSnapshot {
	return billing.PricingSnapshot{
		Ref:                  billing.VersionRef{ID: "pricing", Version: "v7"},
		Currency:             "USD",
		InputPerMillionNano:  100,
		OutputPerMillionNano: 200,
		InputRatePresent:     true,
		OutputRatePresent:    true,
		FixedCharges:         []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 3, Currency: "USD"}}},
	}
}

func composeCatalogPolicy() billing.ChargePolicy {
	return billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "policy", Version: "v2"},
		PricingRef:          composeCatalogPricing().Ref,
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}
}

func composeCatalogOperatorRate() billing.OperatorRateSnapshot {
	return billing.OperatorRateSnapshot{
		Ref:                  billing.VersionRef{ID: "operator-rates", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  50,
		OutputPerMillionNano: 75,
		InputRatePresent:     true,
		OutputRatePresent:    true,
	}
}

// journalReports implements the read-side ReportingStore explicitly so report
// calls through the composed journal cannot hit a nil embedded interface.
type journalReports struct{}

type journalAccountReader struct{}

func (journalAccountReader) GetAccount(context.Context, string) (billing.Account, error) {
	return billing.Account{ID: "acct-compose", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, Version: 1}, nil
}

func (journalReports) AccountReport(context.Context, string, billing.PageRequest) (billing.AccountReport, error) {
	return billing.AccountReport{}, nil
}

func (journalReports) CallExplanation(context.Context, string) (billing.CallExplanation, error) {
	return billing.CallExplanation{}, nil
}

func (journalReports) OperatorCostReport(context.Context, billing.ReportFilter) (billing.OperatorCostReport, error) {
	return billing.OperatorCostReport{}, nil
}

func (journalReports) TrialBalanceReport(context.Context, billing.ReportFilter) (billing.TrialBalanceReport, error) {
	return billing.TrialBalanceReport{}, nil
}

func (journalReports) QueryOpenExposures(context.Context, string, billing.PageRequest) (billing.ExposurePage, error) {
	return billing.ExposurePage{}, nil
}

func (journalReports) QueryReconcileRequired(context.Context, billing.PageRequest) (billing.AccountStatePage, error) {
	return billing.AccountStatePage{}, nil
}

// journalPostTurn implements call settlement as a safe no-op for compose tests.
type journalPostTurn struct{}

func (journalPostTurn) ApplyCallBillingResult(context.Context, billing.ApplyCallBillingInput) (billing.CallSettlement, error) {
	return billing.CallSettlement{}, nil
}

type journalProvision struct{}

func (journalProvision) CreateAccount(context.Context, billing.Account) error { return nil }

func (journalProvision) PostFunding(context.Context, billing.FundingInput) (billing.Posting, error) {
	return billing.Posting{}, nil
}

func (journalProvision) ChangeCreditPolicy(context.Context, billing.CreditPolicyInput) (billing.PolicyChange, error) {
	return billing.PolicyChange{}, nil
}

type journalCallLegUsage struct{}

func (journalCallLegUsage) AppendLeg(context.Context, billing.CallLegUsageRecord) error {
	return nil
}

type journalCallUsage struct{}

func (journalCallUsage) AppendCall(context.Context, billing.CallUsageRecord) error {
	return nil
}

type journalExposure struct{}

func (*journalExposure) AdmitExposure(_ context.Context, in billing.AdmitExposureInput) (billing.CallExposure, error) {
	return billing.CallExposure{AccountID: in.AccountID, CallID: in.CallID, Max: in.Max, PricingRef: in.PricingRef, ChargePolicyRef: in.ChargePolicyRef, Status: billing.ExposureOpen}, nil
}

func (journalExposure) ClaimCompleteCall(context.Context, billing.BillingCallID) (billing.CompleteCall, error) {
	return billing.CompleteCall{}, billing.ErrCallIncomplete
}

func (journalExposure) ClaimCompleteCalls(context.Context, int) ([]billing.CompleteCall, error) {
	return nil, nil
}

func (journalExposure) GetCallExposure(context.Context, billing.BillingCallID) (billing.CallExposure, error) {
	return billing.CallExposure{}, billing.ErrExposureNotFound
}

func (journalExposure) RetryCompleteCall(context.Context, billing.BillingCallID, string) error {
	return nil
}

func (journalExposure) ListCallUsage(context.Context, string) ([]billing.CallUsageRecord, error) {
	return nil, nil
}

func (journalExposure) ListCallLegUsage(context.Context, billing.BillingCallID) ([]billing.CallLegUsageRecord, error) {
	return nil, nil
}

func (journalExposure) ListPendingProviderCostWork(context.Context, int) ([]billing.ProviderCostWork, error) {
	return nil, nil
}

func (journalExposure) ApplyProviderCost(context.Context, billing.ApplyProviderCostInput) (billing.Posting, error) {
	return billing.Posting{}, nil
}

type journalUsageAppendOutbox struct{}

func (journalUsageAppendOutbox) EnqueueCallUsageAppend(context.Context, billing.CallUsageRecord, string) error {
	return nil
}

func (journalUsageAppendOutbox) EnqueueCallLegUsageAppend(context.Context, billing.CallLegUsageRecord, string) error {
	return nil
}

func (journalUsageAppendOutbox) ListPendingUsageAppendWork(context.Context, int) ([]billing.UsageAppendWork, error) {
	return nil, nil
}

func (journalUsageAppendOutbox) MarkUsageAppendProcessed(context.Context, string) error { return nil }

func (journalUsageAppendOutbox) DeferUsageAppend(context.Context, string, string) error { return nil }

func (journalUsageAppendOutbox) FailUsageAppend(context.Context, string, string) error { return nil }

type completeJournal struct {
	journalUsageAppendOutbox
	journalReports
	journalAccountReader
	journalPostTurn
	journalCallLegUsage
	journalCallUsage
	journalExposure
	journalProvision
}

type journalWithoutTerminalSink struct {
	journalReports
	journalPostTurn
	journalCallLegUsage
	journalCallUsage
	journalExposure
	journalProvision
}

type journalWithoutCallLegSink struct {
	journalReports
	journalPostTurn
	journalCallUsage
	journalExposure
	journalProvision
}

type journalWithoutCallSink struct {
	journalReports
	journalPostTurn
	journalCallLegUsage
	journalExposure
	journalProvision
}

type journalWithoutUsageAppendOutbox struct {
	journalReports
	journalAccountReader
	journalPostTurn
	journalCallLegUsage
	journalCallUsage
	journalExposure
	journalProvision
}

type journalWithoutExposure struct {
	journalUsageAppendOutbox
	journalReports
	journalAccountReader
	journalPostTurn
	journalCallLegUsage
	journalCallUsage
	journalProvision
}

var (
	_ billing.AuthoritativeBilling = (*completeJournal)(nil)
	_ billing.TerminalUsageSink    = (*completeJournal)(nil)
	_ billing.AccountProvisioner   = (*completeJournal)(nil)
)

func TestComposeBillingDoesNotRequireHoldLifecycle(t *testing.T) {
	t.Parallel()
	var store billing.AuthoritativeBilling = (*completeJournal)(nil)
	type authorizePort interface {
		Authorize(context.Context, any) (any, error)
	}
	if _, ok := any(store).(authorizePort); ok {
		t.Fatal("AuthoritativeBilling must not require Authorize hold creation")
	}
	in, _, _, _ := validComposeInput(t)
	if _, err := runtimebundle.ComposeBilling(in); err != nil {
		t.Fatalf("ComposeBilling without hold lifecycle: %v", err)
	}
}
