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
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
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
			{name: "store missing usage-record appender", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutAppender{}
			}},
			{name: "store missing hold releaser", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutReleaser{}
			}},
			{name: "store missing account provisioner", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutProvisioner{}
			}},
			{name: "store missing authorization lookup", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutLookup{}
			}},
			{name: "store missing authorization store", mut: func(in *runtimebundle.ComposeBillingInput) {
				in.Store = &journalWithoutAuthorize{}
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
				if prod.BillingAuthoritative || prod.BillingAdmission != nil || prod.BillingStore != nil ||
					prod.BillingRatingResolver != nil || prod.BillingReports != nil || prod.BillingTerminalHandoff != nil {
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
		if prod.BillingIdentity.AccountID == nil || prod.BillingIdentity.AuthorizationID == nil {
			t.Fatal("stock identity resolvers are required")
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
		if got := prod.BillingIdentity.AuthorizationID(ctx, call, "a-leg-1"); got != "sess-stock:a-leg-1" {
			t.Fatalf("AuthorizationID = %q, want session:a-leg", got)
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
			AccountID:       func(context.Context, lipapi.Call) string { return "custom-acct" },
			AuthorizationID: func(context.Context, lipapi.Call, string) string { return "custom-auth" },
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
		if got := prod.BillingIdentity.AuthorizationID(ctx, lipapi.Call{}, "a-leg"); got != "custom-auth" {
			t.Fatalf("AuthorizationID = %q, want custom mapping", got)
		}
	})

	t.Run("custom empty identity fails closed at admission", func(t *testing.T) {
		t.Parallel()
		in, _, _, _ := validComposeInput(t)
		empty := coreRuntime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "" },
			AuthorizationID: func(context.Context, lipapi.Call, string) string { return "" },
		}
		in.Identity = &empty
		prod, err := runtimebundle.ComposeBilling(in)
		if err != nil {
			t.Fatalf("ComposeBilling: %v", err)
		}
		_, err = prod.BillingAdmission.Authorize(context.Background(), coreRuntime.BillingAdmissionInput{
			Call:   lipapi.Call{},
			ALegID: "a-leg-1",
		})
		if !errors.Is(err, billing.ErrAuthorizationInvalid) {
			t.Fatalf("Authorize = %v, want ErrAuthorizationInvalid", err)
		}
	})
}

func TestBuildAppliesYAMLBillingHoldTTLWhenComposeOmitsIt(t *testing.T) {
	t.Parallel()
	in, _, _, _ := validComposeInput(t)
	prod, err := runtimebundle.ComposeBilling(in)
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	adapter, ok := prod.BillingAdmission.(*billingadmission.Adapter)
	if !ok {
		t.Fatalf("BillingAdmission = %T, want *billingadmission.Adapter", prod.BillingAdmission)
	}
	cfg := composeBillingHostConfig()
	cfg.Accounting.Billing.HoldTTL = "30m"
	_, bundle := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Production:     prod,
	})
	if adapter.HoldTTL() != 30*time.Minute {
		t.Fatalf("HoldTTL after YAML overlay = %s, want 30m", adapter.HoldTTL())
	}
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = bundle.Close()
}

func TestBuildKeepsComposeBillingHoldTTLOverYAML(t *testing.T) {
	t.Parallel()
	in, _, _, _ := validComposeInput(t)
	in.HoldTTL = 45 * time.Minute
	prod, err := runtimebundle.ComposeBilling(in)
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	adapter, ok := prod.BillingAdmission.(*billingadmission.Adapter)
	if !ok {
		t.Fatalf("BillingAdmission = %T, want *billingadmission.Adapter", prod.BillingAdmission)
	}
	cfg := composeBillingHostConfig()
	cfg.Accounting.Billing.HoldTTL = "30m"
	_, bundle := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Production:     prod,
	})
	if adapter.HoldTTL() != 45*time.Minute {
		t.Fatalf("explicit ComposeBilling HoldTTL lost, got %s", adapter.HoldTTL())
	}
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = bundle.Close()
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
	if prod.BillingTerminalHandoff != billing.UsageRecordAppender(store) {
		t.Fatal("BillingTerminalHandoff is not the injected journal")
	}
	if prod.BillingAdmission == nil {
		t.Fatal("BillingAdmission is required")
	}
	if prod.BillingRatingResolver == nil {
		t.Fatal("BillingRatingResolver is required")
	}
	if prod.BillingIdentity.AccountID == nil || prod.BillingIdentity.AuthorizationID == nil {
		t.Fatal("BillingIdentity resolvers are required")
	}
	if prod.BillingReportsPath != in.ReportsPath {
		t.Fatalf("BillingReportsPath = %q, want %q", prod.BillingReportsPath, in.ReportsPath)
	}
	if prod.BillingHoldTTL != in.HoldTTL {
		t.Fatalf("BillingHoldTTL = %s, want %s", prod.BillingHoldTTL, in.HoldTTL)
	}
	if prod.BillingPostTurnBatchSize != in.PostTurnBatchSize {
		t.Fatalf("BillingPostTurnBatchSize = %d, want %d", prod.BillingPostTurnBatchSize, in.PostTurnBatchSize)
	}
	if prod.BillingPostTurnInterval != in.PostTurnInterval {
		t.Fatalf("BillingPostTurnInterval = %s, want %s", prod.BillingPostTurnInterval, in.PostTurnInterval)
	}

	record := billing.TurnUsageRecord{
		AccountID:          "acct-1",
		Key:                "tur-1",
		CustomerPricingRef: composeCatalogPricing().Ref,
		ChargePolicyRef:    composeCatalogPolicy().Ref,
	}
	rated, err := prod.BillingRatingResolver.ResolveRating(context.Background(), record)
	if err != nil {
		t.Fatalf("ResolveRating: %v", err)
	}
	if rated.Authorization.Amount.Nano != 42 || rated.Authorization.TURKey != record.Key {
		t.Fatalf("rating hold = %+v, want lookup from the same store", rated.Authorization)
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
	auth, err := prod.BillingAdmission.Authorize(ctx, coreRuntime.BillingAdmissionInput{
		Call:        lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-compose"}},
		ALegID:      "a-compose",
		Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
	})
	if err != nil {
		t.Fatalf("catalog-backed Authorize: %v", err)
	}
	if auth.PricingRef.ID != pricing.Ref.ID || auth.PricingRef.Version != pricing.Ref.Version {
		t.Fatalf("hold pricing ref = %+v, want catalog %+v", auth.PricingRef, pricing.Ref)
	}
	if auth.ChargePolicyRef.ID != policy.Ref.ID || auth.ChargePolicyRef.Version != policy.Ref.Version {
		t.Fatalf("hold policy ref = %+v, want catalog %+v", auth.ChargePolicyRef, policy.Ref)
	}
	if store.last.AccountID != "acct-compose" {
		t.Fatalf("authorize account = %q, want stock principal mapping", store.last.AccountID)
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

func (journalReports) AccountReport(context.Context, string, billing.PageRequest) (billing.AccountReport, error) {
	return billing.AccountReport{}, nil
}

func (journalReports) TurnExplanation(context.Context, string) (billing.TurnExplanation, error) {
	return billing.TurnExplanation{}, nil
}

func (journalReports) OperatorCostReport(context.Context, billing.ReportFilter) (billing.OperatorCostReport, error) {
	return billing.OperatorCostReport{}, nil
}

func (journalReports) TrialBalanceReport(context.Context, billing.ReportFilter) (billing.TrialBalanceReport, error) {
	return billing.TrialBalanceReport{}, nil
}

func (journalReports) SessionReport(context.Context, string, string, billing.PageRequest) (billing.SessionReport, error) {
	return billing.SessionReport{}, nil
}

func (journalReports) QueryProcessing(context.Context, billing.ReportFilter) (billing.ProcessingPage, error) {
	return billing.ProcessingPage{}, nil
}

func (journalReports) QueryOpenHolds(context.Context, string, billing.PageRequest) (billing.HoldPage, error) {
	return billing.HoldPage{}, nil
}

func (journalReports) QueryReconcileRequired(context.Context, billing.PageRequest) (billing.AccountStatePage, error) {
	return billing.AccountStatePage{}, nil
}

// journalPostTurn implements the durable post-turn boundary (settlement plus
// processing metadata) as safe no-ops.
type journalPostTurn struct{}

func (journalPostTurn) ApplyBillingResult(context.Context, billing.ApplyBillingInput) (billing.Settlement, error) {
	return billing.Settlement{}, nil
}

func (journalPostTurn) ClaimPending(context.Context, int) ([]billing.TurnUsageRecord, error) {
	return nil, nil
}

func (journalPostTurn) MarkProcessingRetryable(context.Context, string, string, string) error {
	return nil
}

func (journalPostTurn) MarkProcessingTerminal(context.Context, string, string, string) error {
	return nil
}

func (journalPostTurn) MarkProcessingUnreconciledCost(context.Context, string, string, string) error {
	return nil
}

func (journalPostTurn) MarkProcessingProcessed(context.Context, string, string, string) error {
	return nil
}

func (journalPostTurn) MarkProcessingInvariantFailure(context.Context, billing.TurnUsageRecord, string) error {
	return nil
}

type journalHandoff struct{}

func (journalHandoff) AppendUsageRecord(context.Context, billing.TurnUsageRecord) error {
	return nil
}

type journalRelease struct{}

func (journalRelease) ReleaseAuthorization(context.Context, billing.ReleaseAuthorizationInput) (billing.Posting, error) {
	return billing.Posting{}, nil
}

type journalProvision struct{}

func (journalProvision) CreateAccount(context.Context, billing.Account) error { return nil }

func (journalProvision) PostFunding(context.Context, billing.FundingInput) (billing.Posting, error) {
	return billing.Posting{}, nil
}

func (journalProvision) ChangeCreditPolicy(context.Context, billing.CreditPolicyInput) (billing.PolicyChange, error) {
	return billing.PolicyChange{}, nil
}

type journalLookup struct{}

func (journalLookup) GetAuthorization(_ context.Context, accountID, turKey string) (billing.Authorization, error) {
	return billing.Authorization{
		ID:        "hold-1",
		AccountID: accountID,
		TURKey:    turKey,
		Amount:    billing.Money{Nano: 42, Currency: "USD"},
		Status:    billing.HoldStatusOpen,
	}, nil
}

type journalAuthorize struct {
	last billing.AuthorizeInput
}

func (s *journalAuthorize) Authorize(_ context.Context, in billing.AuthorizeInput) (billing.Authorization, error) {
	s.last = in
	return billing.Authorization{
		ID:              in.ID,
		AccountID:       in.AccountID,
		TURKey:          in.TURKey,
		Amount:          in.Amount,
		PricingRef:      in.PricingRef,
		ChargePolicyRef: in.ChargePolicyRef,
		Status:          billing.HoldStatusOpen,
		ExpiresAt:       in.ExpiresAt,
	}, nil
}

type completeJournal struct {
	journalReports
	journalPostTurn
	journalHandoff
	journalRelease
	journalProvision
	journalLookup
	journalAuthorize
}

type journalWithoutAppender struct {
	journalReports
	journalPostTurn
	journalRelease
	journalProvision
	journalLookup
	journalAuthorize
}

type journalWithoutReleaser struct {
	journalReports
	journalPostTurn
	journalHandoff
	journalProvision
	journalLookup
	journalAuthorize
}

type journalWithoutProvisioner struct {
	journalReports
	journalPostTurn
	journalHandoff
	journalRelease
	journalLookup
	journalAuthorize
}

type journalWithoutLookup struct {
	journalReports
	journalPostTurn
	journalHandoff
	journalRelease
	journalProvision
	journalAuthorize
}

type journalWithoutAuthorize struct {
	journalReports
	journalPostTurn
	journalHandoff
	journalRelease
	journalProvision
	journalLookup
}

var (
	_ billing.AuthoritativeBilling = (*completeJournal)(nil)
	_ billing.UsageRecordAppender  = (*completeJournal)(nil)
	_ billing.PostTurnStore        = (*completeJournal)(nil)
	_ billing.HoldReleaser         = (*completeJournal)(nil)
	_ billing.AccountProvisioner   = (*completeJournal)(nil)
	_ billing.AuthorizationLookup  = (*completeJournal)(nil)
	_ billing.AuthorizationStore   = (*completeJournal)(nil)
)
