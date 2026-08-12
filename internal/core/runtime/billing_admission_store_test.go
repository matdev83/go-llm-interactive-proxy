package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingadmission"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	_ "modernc.org/sqlite"
)

func TestExecutorRealBillingAdmissionDeniesBeforeBackendOpen(t *testing.T) {
	dsn := fmt.Sprintf("file:runtime-billing-admission-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", runtimeBillingTestSequence.Add(1))
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
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "runtime-test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAccount(context.Background(), billing.Account{
		ID: "runtime-billing-account", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 0, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}

	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: runtime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "runtime-billing-account" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "runtime-auth:" + aLeg },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref,
				Scope: billing.ChargeSurfacedTurn, IncludeFixedCharges: true,
			}, nil
		},
		Pricing: func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	b2buaStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = b2buaStore
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAdmission = adapter
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, errors.New("must not open")
			},
		},
	}
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:model"}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	_, err = ex.Execute(context.Background(), call)
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) || !errors.Is(err, billing.ErrInsufficientSpendable) {
		t.Fatalf("error = %v, want runtime denial wrapping insufficient spendable", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("backend opens = %d, want 0", opens.Load())
	}
}

var runtimeBillingTestSequence atomic.Int64

// failClosedStreamObserverFactory fails assemble after a successful backend Open so
// Execute aborts with a live upstream contact and empty billing evidence.
type failClosedStreamObserverFactory struct{}

func (failClosedStreamObserverFactory) ID() string { return "billing-admit-fail-closed-obs" }
func (failClosedStreamObserverFactory) Order() int { return 0 }
func (failClosedStreamObserverFactory) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (failClosedStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, errors.New("forced final-stream observation open failure")
}

func TestExecutorRetainsHoldWhenOpenSucceedsThenAssembleFails(t *testing.T) {
	dsn := fmt.Sprintf("file:runtime-billing-retain-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", runtimeBillingTestSequence.Add(1))
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
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "runtime-retain"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAccount(context.Background(), billing.Account{
		ID: "retain-after-open", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 1000, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: runtime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "retain-after-open" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth:" + aLeg },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref,
				Scope: billing.ChargeSurfacedTurn, IncludeFixedCharges: true,
			}, nil
		},
		Pricing: func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	b2buaStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = b2buaStore
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAdmission = adapter
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		StreamObserverFactories: []response.StreamObserverFactory{failClosedStreamObserverFactory{}},
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:model"}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected assemble failure after successful open")
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens = %d, want 1", opens.Load())
	}
	account, err := store.GetAccount(context.Background(), "retain-after-open")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 10 {
		t.Fatalf("hold must stay reserved after upstream open; reserved=%d want 10", account.ReservedNano)
	}
	holds, err := store.QueryOpenHolds(context.Background(), "retain-after-open", billing.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(holds.Items) != 1 || holds.Items[0].Status != string(billing.HoldStatusOpen) {
		t.Fatalf("open holds after open+assemble abort = %+v", holds.Items)
	}
}

func TestExecutorRetainsHoldWhenOpenSucceedsThenHandoffHasNoEvidence(t *testing.T) {
	dsn := fmt.Sprintf("file:runtime-billing-handoff-retain-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", runtimeBillingTestSequence.Add(1))
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
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "runtime-handoff-retain"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAccount(context.Background(), billing.Account{
		ID: "retain-handoff-after-open", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 1000, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: runtime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "retain-handoff-after-open" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth:" + aLeg },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref,
				Scope: billing.ChargeSurfacedTurn, IncludeFixedCharges: true,
			}, nil
		},
		Pricing: func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	b2buaStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = b2buaStore
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAdmission = adapter
	ex.BillingTerminalHandoff = store
	ex.BillingHoldReleaser = store
	ex.BillingIdentity = runtime.BillingIdentity{
		AccountID:       func(context.Context, lipapi.Call) string { return "retain-handoff-after-open" },
		AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth:" + aLeg },
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		StreamObserverFactories: []response.StreamObserverFactory{failClosedStreamObserverFactory{}},
	})
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	t.Cleanup(ex.WaitBillingHandoffRetriesForClose)
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:model"}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected assemble failure after successful open")
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens = %d, want 1", opens.Load())
	}
	ex.WaitBillingHandoffRetriesForClose()
	account, err := store.GetAccount(context.Background(), "retain-handoff-after-open")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 10 {
		t.Fatalf("hold must stay reserved after Open with empty handoff; reserved=%d want 10", account.ReservedNano)
	}
	holds, err := store.QueryOpenHolds(context.Background(), "retain-handoff-after-open", billing.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(holds.Items) != 1 || holds.Items[0].Status != string(billing.HoldStatusOpen) {
		t.Fatalf("open holds after Open+empty handoff = %+v", holds.Items)
	}
}

func TestExecutorReleasesHoldWhenOpenFailsAfterAdmission(t *testing.T) {
	dsn := fmt.Sprintf("file:runtime-billing-release-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", runtimeBillingTestSequence.Add(1))
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
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "runtime-release"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateAccount(context.Background(), billing.Account{
		ID: "release-on-open-fail", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 1000, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		FixedCharges: []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	adapter, err := billingadmission.NewAdapter(billingadmission.Config{
		Store: store, Releaser: store, Currency: "USD",
		Identity: runtime.BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "release-on-open-fail" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth:" + aLeg },
		},
		Policy: func(context.Context, lipapi.Call) (billing.ChargePolicy, error) {
			return billing.ChargePolicy{
				Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref,
				Scope: billing.ChargeSurfacedTurn, IncludeFixedCharges: true,
			}, nil
		},
		Pricing: func(context.Context, string, string) (billing.PricingSnapshot, error) { return pricing, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	b2buaStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = b2buaStore
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingAdmission = adapter
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("upstream unavailable")
			},
		},
	}
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "backend:model"}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected open failure")
	}
	account, err := store.GetAccount(context.Background(), "release-on-open-fail")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 0 {
		t.Fatalf("unused hold must be released after open failure, reserved=%d", account.ReservedNano)
	}
	holds, err := store.QueryOpenHolds(context.Background(), "release-on-open-fail", billing.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(holds.Items) != 0 {
		t.Fatalf("open holds after abort = %+v", holds.Items)
	}
}
