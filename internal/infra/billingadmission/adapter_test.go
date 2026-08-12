package billingadmission_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	_ "modernc.org/sqlite"
)

func TestAdapterBuildsEstimateFromRoutePlanAndReserves(t *testing.T) {
	t.Parallel()
	store := newAdmissionTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{
		ID: "admit-acct", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	pricing := billing.PricingSnapshot{
		Ref: VersionRef("pricing", "v1"), Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 5, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: coreruntime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "admit-acct" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth:" + aLeg },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: VersionRef("policy", "v1"), PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn,
				IncludeInputTokens: true, IncludeOutputTokens: true, IncludeFixedCharges: true,
			}, nil
		},
		Pricing:        func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	cheap := routing.Primary{Backend: "backend", Model: "cheap"}
	expensive := routing.Primary{Backend: "backend", Model: "expensive"}
	auth, err := adapter.Authorize(ctx, coreruntime.BillingAdmissionInput{
		Call:    lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:cheap|backend:expensive"}},
		TraceID: "trace", ALegID: "aleg-1",
		Route: &routing.Selector{Alternatives: []routing.FailoverAlt{
			{Primary: &cheap}, {Primary: &expensive},
		}},
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth.PricingRef != pricing.Ref || auth.ChargePolicyRef != VersionRef("policy", "v1") {
		t.Fatalf("hold refs = %+v / %+v, want pricing/policy snapshots", auth.PricingRef, auth.ChargePolicyRef)
	}
	account, err := store.GetAccount(ctx, "admit-acct")
	if err != nil {
		t.Fatal(err)
	}
	// input 1*1 + output max 10*2 + fixed 5 = 26
	if account.ReservedNano != 26 {
		t.Fatalf("reserved = %d, want 26 from route-plan estimate", account.ReservedNano)
	}
}

func TestAdapterReservesWorstHeterogeneousRouteCard(t *testing.T) {
	t.Parallel()
	store := newAdmissionTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{
		ID: "admit-hetero", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	catalog := VersionRef("pricing", "v1")
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: coreruntime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "admit-hetero" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth:" + aLeg },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: VersionRef("policy", "v1"), PricingRef: catalog, Scope: billing.ChargeSurfacedTurn,
				IncludeInputTokens: true, IncludeOutputTokens: true, IncludeFixedCharges: true,
			}, nil
		},
		Pricing: func(_ context.Context, _, model string) (billing.PricingSnapshot, error) {
			card := billing.PricingSnapshot{
				Ref: catalog, Currency: "USD",
				InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
				InputRatePresent: true, OutputRatePresent: true,
				FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 5, Currency: "USD"}}},
			}
			if model == "expensive" {
				card.InputPerMillionNano = 2_000_000
				card.OutputPerMillionNano = 4_000_000
			}
			return card, nil
		},
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	cheap := routing.Primary{Backend: "backend", Model: "cheap"}
	expensive := routing.Primary{Backend: "backend", Model: "expensive"}
	if _, err := adapter.Authorize(ctx, coreruntime.BillingAdmissionInput{
		Call:    lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:cheap|backend:expensive"}},
		TraceID: "trace", ALegID: "aleg-hetero",
		Route: &routing.Selector{Alternatives: []routing.FailoverAlt{
			{Primary: &cheap}, {Primary: &expensive},
		}},
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
	}); err != nil {
		t.Fatal(err)
	}
	account, err := store.GetAccount(ctx, "admit-hetero")
	if err != nil {
		t.Fatal(err)
	}
	// expensive card: input 2 + output 40 + fixed 5 = 47
	if account.ReservedNano != 47 {
		t.Fatalf("reserved = %d, want 47 from the more expensive route card", account.ReservedNano)
	}
}

func TestAdapterDefaultsClientMaxOutputFromCallOptions(t *testing.T) {
	t.Parallel()
	store := newAdmissionTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{
		ID: "admit-client-max", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	pricing := billing.PricingSnapshot{
		Ref: VersionRef("pricing", "v1"), Currency: "USD",
		InputPerMillionNano: 1_000_000, OutputPerMillionNano: 2_000_000,
		InputRatePresent: true, OutputRatePresent: true,
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 5, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: coreruntime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "admit-client-max" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth:" + aLeg },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: VersionRef("policy", "v1"), PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn,
				IncludeInputTokens: true, IncludeOutputTokens: true, IncludeFixedCharges: true,
			}, nil
		},
		Pricing:        func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 10, true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	clientMax := 2
	cheap := routing.Primary{Backend: "backend", Model: "cheap"}
	expensive := routing.Primary{Backend: "backend", Model: "expensive"}
	_, err = adapter.Authorize(ctx, coreruntime.BillingAdmissionInput{
		Call: lipapi.Call{
			Route:   lipapi.RouteIntent{Selector: "backend:cheap|backend:expensive"},
			Options: lipapi.GenerationOptions{MaxOutputTokens: &clientMax},
		},
		TraceID: "trace", ALegID: "aleg-client-max",
		Route: &routing.Selector{Alternatives: []routing.FailoverAlt{
			{Primary: &cheap}, {Primary: &expensive},
		}},
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.GetAccount(ctx, "admit-client-max")
	if err != nil {
		t.Fatal(err)
	}
	// input 1*1 + output min(client 2, model 10)*2 + fixed 5 = 10
	if account.ReservedNano != 10 {
		t.Fatalf("reserved = %d, want 10 from client max_output_tokens cap", account.ReservedNano)
	}
}

func TestAdapterDeniesInsufficientSpendable(t *testing.T) {
	t.Parallel()
	store := newAdmissionTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{
		ID: "deny-acct", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 1, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	pricing := billing.PricingSnapshot{
		Ref: VersionRef("pricing", "v1"), Currency: "USD",
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: coreruntime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "deny-acct" },
			AuthorizationID: func(context.Context, lipapi.Call, string) string { return "auth-deny" },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: VersionRef("policy", "v1"), PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn,
				IncludeFixedCharges: true,
			}, nil
		},
		Pricing: func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	primary := routing.Primary{Backend: "backend", Model: "model"}
	_, err = adapter.Authorize(ctx, coreruntime.BillingAdmissionInput{
		Call: lipapi.Call{}, ALegID: "aleg-deny",
		Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 0},
	})
	if !errors.Is(err, billing.ErrInsufficientSpendable) {
		t.Fatalf("error = %v, want ErrInsufficientSpendable", err)
	}
}

func TestNewAdapterRejectsMissingStoreIdentityPolicyPricingCurrency(t *testing.T) {
	t.Parallel()
	identity := coreruntime.BillingIdentity{
		AccountID:       func(context.Context, lipapi.Call) string { return "acct" },
		AuthorizationID: func(context.Context, lipapi.Call, string) string { return "auth" },
	}
	policy := func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
		return billing.ChargePolicy{}, nil
	}
	pricing := func(context.Context, string, string) (billing.PricingSnapshot, error) {
		return billing.PricingSnapshot{}, nil
	}
	store := newAdmissionTestStore(t)
	tests := []struct {
		name string
		cfg  billingadmission.Config
	}{
		{name: "missing store", cfg: billingadmission.Config{Identity: identity, Policy: policy, Pricing: pricing, Currency: "USD"}},
		{name: "missing account identity", cfg: billingadmission.Config{Store: store, Identity: coreruntime.BillingIdentity{AuthorizationID: identity.AuthorizationID}, Policy: policy, Pricing: pricing, Currency: "USD"}},
		{name: "missing authorization identity", cfg: billingadmission.Config{Store: store, Identity: coreruntime.BillingIdentity{AccountID: identity.AccountID}, Policy: policy, Pricing: pricing, Currency: "USD"}},
		{name: "missing policy", cfg: billingadmission.Config{Store: store, Identity: identity, Pricing: pricing, Currency: "USD"}},
		{name: "missing pricing", cfg: billingadmission.Config{Store: store, Identity: identity, Policy: policy, Currency: "USD"}},
		{name: "missing currency", cfg: billingadmission.Config{Store: store, Identity: identity, Policy: policy, Pricing: pricing}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := billingadmission.NewAdapter(tt.cfg); err == nil {
				t.Fatal("expected constructor error")
			}
		})
	}
}

func TestAdapterReleaseUnusedClosesHoldWithExecutionNotStarted(t *testing.T) {
	t.Parallel()
	store := newAdmissionTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{
		ID: "release-unused", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	pricing := billing.PricingSnapshot{
		Ref: VersionRef("pricing", "v1"), Currency: "USD",
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: coreruntime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "release-unused" },
			AuthorizationID: func(context.Context, lipapi.Call, string) string { return "auth-unused" },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: VersionRef("policy", "v1"), PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn,
				IncludeFixedCharges: true,
			}, nil
		},
		Pricing: func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	primary := routing.Primary{Backend: "backend", Model: "model"}
	in := coreruntime.BillingAdmissionInput{
		Call: lipapi.Call{}, ALegID: "aleg-unused",
		Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
		RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 0},
	}
	if _, err := adapter.Authorize(ctx, in); err != nil {
		t.Fatal(err)
	}
	account, err := store.GetAccount(ctx, "release-unused")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 10 {
		t.Fatalf("reserved after authorize = %d, want 10", account.ReservedNano)
	}
	if err := adapter.ReleaseUnused(ctx, in); err != nil {
		t.Fatal(err)
	}
	account, err = store.GetAccount(ctx, "release-unused")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 0 {
		t.Fatalf("reserved after unused release = %d, want 0", account.ReservedNano)
	}
	holds, err := store.QueryOpenHolds(ctx, "release-unused", billing.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(holds.Items) != 0 {
		t.Fatalf("open holds after unused release = %+v", holds.Items)
	}
	journals, err := store.JournalTransactions(ctx, "release-unused")
	if err != nil {
		t.Fatal(err)
	}
	var released bool
	for _, journal := range journals {
		if journal.OperationKind == "authorization_release" {
			released = true
		}
	}
	if !released {
		t.Fatalf("expected authorization_release journal, got %+v", journals)
	}
}

func VersionRef(id, version string) billing.VersionRef {
	return billing.VersionRef{ID: id, Version: version}
}

func newAdmissionTestStore(t *testing.T) *billingstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:billing-admission-adapter-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", admissionTestSeq.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "admission-adapter"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

var admissionTestSeq atomic.Int64
