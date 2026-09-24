package runtimebundle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var runtime43StoreSequence atomic.Uint64

// runtime43RevisionStore models a custom revision result store that can
// persist pure valuation but does not expose the durable cross-path cutover.
// It intentionally still exposes the legacy provider ports so simultaneous
// configuration cannot accidentally pass through one path.
type runtime43RevisionStore struct {
	processBillingStore
	listEconomicCalls atomic.Int32
}

func (s *runtime43RevisionStore) GetAccount(context.Context, string) (billing.Account, error) {
	return billing.Account{ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, Version: 1}, nil
}

func (s *runtime43RevisionStore) AdmitExposure(context.Context, billing.AdmitExposureInput) (billing.CallExposure, error) {
	return billing.CallExposure{}, nil
}

func (s *runtime43RevisionStore) ListPendingEconomicRevisionWork(context.Context, billing.EconomicQueue, int) ([]billing.EconomicRevisionWork, error) {
	s.listEconomicCalls.Add(1)
	return nil, nil
}

func (s *runtime43RevisionStore) AppendEconomicRevisionResult(context.Context, billing.EconomicRevisionWork, billing.EconomicRevisionResult) error {
	return nil
}

func (s *runtime43RevisionStore) AppendProviderMaintenance(context.Context, billing.ProviderMaintenanceUsage) error {
	return nil
}

func (s *runtime43RevisionStore) ApplyProviderCostRevision(context.Context, billing.ProviderCostRevisionInput) (billing.ProviderCostRevisionResult, error) {
	return billing.ProviderCostRevisionResult{Applied: true}, nil
}

type runtime43PureValuationStore struct {
	processBillingStore
	listEconomicCalls atomic.Int32
	listProviderCalls atomic.Int32
}

func (s *runtime43PureValuationStore) GetAccount(context.Context, string) (billing.Account, error) {
	return billing.Account{ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, Version: 1}, nil
}

func (s *runtime43PureValuationStore) AdmitExposure(context.Context, billing.AdmitExposureInput) (billing.CallExposure, error) {
	return billing.CallExposure{}, nil
}

func (s *runtime43PureValuationStore) ListPendingEconomicRevisionWork(context.Context, billing.EconomicQueue, int) ([]billing.EconomicRevisionWork, error) {
	s.listEconomicCalls.Add(1)
	return nil, nil
}

func (s *runtime43PureValuationStore) ListPendingProviderCostWork(context.Context, int) ([]billing.ProviderCostWork, error) {
	s.listProviderCalls.Add(1)
	return nil, nil
}

func (s *runtime43PureValuationStore) AppendEconomicRevisionResult(context.Context, billing.EconomicRevisionWork, billing.EconomicRevisionResult) error {
	return nil
}

type runtime43LegacyStore struct {
	processBillingStore
	listProviderCalls atomic.Int32
	legacyApplies     atomic.Int32
}

func (s *runtime43LegacyStore) ListPendingProviderCostWork(context.Context, int) ([]billing.ProviderCostWork, error) {
	s.listProviderCalls.Add(1)
	return nil, nil
}

func (s *runtime43LegacyStore) ClaimProviderCostWorkWithCutover(context.Context, int) ([]billing.ClaimedProviderCostWork, error) {
	s.listProviderCalls.Add(1)
	return nil, nil
}

func (s *runtime43LegacyStore) ApplyProviderCost(context.Context, billing.ApplyProviderCostInput) (billing.Posting, error) {
	s.legacyApplies.Add(1)
	return billing.Posting{}, nil
}

type runtime43FencedStore struct {
	runtime43RevisionStore
	claimCalls    atomic.Int32
	legacyApplies atomic.Int32
}

func newRuntime43FencedStore() *runtime43FencedStore {
	return &runtime43FencedStore{}
}

func (s *runtime43FencedStore) ListPendingProviderCostWork(context.Context, int) ([]billing.ProviderCostWork, error) {
	return []billing.ProviderCostWork{{AccountID: "acct"}}, nil
}

func (s *runtime43FencedStore) ClaimProviderCostWorkWithCutover(context.Context, int) ([]billing.ClaimedProviderCostWork, error) {
	return []billing.ClaimedProviderCostWork{{Work: billing.ProviderCostWork{AccountID: "acct"}}}, nil
}

func (s *runtime43FencedStore) ClaimProviderCostWorkForRevision(context.Context, billing.ProviderCostWork) (bool, error) {
	s.claimCalls.Add(1)
	return true, nil
}

func (s *runtime43FencedStore) ApplyProviderCost(context.Context, billing.ApplyProviderCostInput) (billing.Posting, error) {
	s.legacyApplies.Add(1)
	return billing.Posting{}, nil
}

type runtime43Rater struct{}

func (runtime43Rater) Rate(context.Context, economics.PostUsageRatingInput) (economics.Valuation, error) {
	return economics.Valuation{}, nil
}

func runtime43BaseProduction(store billing.AuthoritativeBilling, sink billing.TerminalUsageSink) ProductionOptions {
	return ProductionOptions{
		BillingStore:             store,
		BillingTerminalUsageSink: sink,
		BillingCreditGate:        processBillingCreditGate{},
		BillingExposureAdmission: processBillingAdmission{},
		BillingIdentity: runtimecore.BillingIdentity{
			AccountID: func(context.Context, lipapi.Call) string { return "acct" },
		},
		BillingCallRatingResolver: processBillingCallResolver{},
	}
}

func cleanupRuntime43Owner(t *testing.T, closers *[]func() error) {
	t.Helper()
	t.Cleanup(func() {
		for i := len(*closers) - 1; i >= 0; i-- {
			if err := (*closers)[i](); err != nil {
				t.Errorf("cleanup process resource: %v", err)
			}
		}
	})
}

func TestRefinement43ComposeBillingRejectsRevisionMoneyWithoutCutover(t *testing.T) {
	t.Parallel()
	catalog := runtime43Catalog(t)
	store := &runtime43RevisionStore{}
	_, err := ComposeBilling(ComposeBillingInput{
		Store: store, TerminalUsageSink: &processBillingSink{}, Catalog: catalog,
		Currency: "USD", ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128, true, nil
		},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrComposeBillingIncomplete)
	require.ErrorIs(t, err, ErrProviderCostCutoverRequired)
	require.Contains(t, err.Error(), "cutover")
}

func TestRefinement43RuntimeRejectsSimultaneousUnfencedProviderWriters(t *testing.T) {
	t.Parallel()
	store := &runtime43RevisionStore{}
	sink := &processBillingSink{}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	cleanupRuntime43Owner(t, &closers)
	prod := runtime43BaseProduction(store, sink)
	prod.BillingEconomicRevisionRater = runtime43Rater{}
	prod.BillingProviderCostResolver = processBillingProviderResolver{}

	_, err := buildProcessBillingRuntime(owner, "", prod)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAuthoritativeBillingRequired)
	require.ErrorIs(t, err, ErrProviderCostCutoverRequired)
	require.Empty(t, closers, "invalid monetary composition must fail before any process resource starts")
}

func TestRefinement43RuntimePureValuationWithoutProviderWriterRemainsInert(t *testing.T) {
	t.Parallel()
	store := &runtime43PureValuationStore{}
	sink := &processBillingSink{}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	cleanupRuntime43Owner(t, &closers)
	prod := runtime43BaseProduction(store, sink)
	prod.BillingEconomicRevisionRater = runtime43Rater{}

	_, err := buildProcessBillingRuntime(owner, "", prod)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return store.listEconomicCalls.Load() >= 2 }, time.Second, 5*time.Millisecond)
	require.Zero(t, store.listProviderCalls.Load(), "pure valuation must not start the legacy money writer")
}

func TestRefinement43RuntimeLegacyOnlyStoreStillStartsLegacyWorker(t *testing.T) {
	t.Parallel()
	store := &runtime43LegacyStore{}
	sink := &processBillingSink{}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	cleanupRuntime43Owner(t, &closers)
	prod := runtime43BaseProduction(store, sink)
	prod.BillingProviderCostResolver = processBillingProviderResolver{}

	_, err := buildProcessBillingRuntime(owner, "", prod)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return store.listProviderCalls.Load() > 0 }, time.Second, 5*time.Millisecond)
	require.Zero(t, store.legacyApplies.Load())
}

func TestRefinement43RuntimeFencedCutoverDoesNotDoubleFeedLegacyWriter(t *testing.T) {
	t.Parallel()
	store := newRuntime43FencedStore()
	sink := &processBillingSink{}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	cleanupRuntime43Owner(t, &closers)
	prod := runtime43BaseProduction(store, sink)
	prod.BillingEconomicRevisionRater = runtime43Rater{}
	prod.BillingProviderCostResolver = processBillingProviderResolver{}

	_, err := buildProcessBillingRuntime(owner, "", prod)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return store.claimCalls.Load() > 0 }, time.Second, 5*time.Millisecond)
	require.Zero(t, store.legacyApplies.Load(), "fenced legacy worker must retire work before resolver/posting")
}

func TestRefinement43DurableStoreCompositionStartsRevisionAndCutoverSafely(t *testing.T) {
	store := openRuntime43DurableStore(t)
	require.Implements(t, (*billing.ProviderCostRevisionStore)(nil), store)
	require.Implements(t, (*billing.ProviderCostWorkCutoverStore)(nil), store)
	catalog := runtime43Catalog(t)
	prod, err := ComposeBilling(ComposeBillingInput{
		Store: store, TerminalUsageSink: store, Catalog: catalog,
		Currency: "USD", ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128, true, nil
		}, PostTurnBatchSize: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, prod.BillingEconomicRevisionRater)
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	cleanupRuntime43Owner(t, &closers)
	_, err = buildProcessBillingRuntime(owner, "", prod)
	require.NoError(t, err)
	require.NotEmpty(t, closers)
}

func TestRefinement43RuntimeRegistersStopBeforeWorkerStart(t *testing.T) {
	t.Parallel()
	var order []string
	var mu sync.Mutex
	worker := processBillingLifecycleFuncs{
		start: func(context.Context) error {
			mu.Lock()
			order = append(order, "start")
			mu.Unlock()
			return errors.New("start failed")
		},
		stop: func(context.Context) error {
			mu.Lock()
			order = append(order, "stop")
			mu.Unlock()
			return nil
		},
	}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) {
		mu.Lock()
		order = append(order, "register")
		mu.Unlock()
		closers = append(closers, close)
	}}
	require.Error(t, startProcessBillingWorker(owner, worker))
	require.Len(t, closers, 1)
	require.NoError(t, closers[0]())
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"register", "start", "stop"}, order)
}

type processBillingLifecycleFuncs struct {
	start func(context.Context) error
	stop  func(context.Context) error
}

func (w processBillingLifecycleFuncs) Start(ctx context.Context) error { return w.start(ctx) }
func (w processBillingLifecycleFuncs) Stop(ctx context.Context) error  { return w.stop(ctx) }

// Compile-time references make the intended consumer-side capability visible
// to the test matrix without exporting a runtime service registry.
var (
	_ billing.ProviderCostRevisionStore   = (*runtime43RevisionStore)(nil)
	_ billing.EconomicRevisionResultStore = (*runtime43RevisionStore)(nil)
	_ billing.EconomicRevisionWorkReader  = (*runtime43RevisionStore)(nil)
)

func runtime43Catalog(t *testing.T) *billingcompose.SnapshotCatalog {
	t.Helper()
	catalog := billingcompose.NewSnapshotCatalog()
	pricing := billing.PricingSnapshot{
		Ref:                  billing.VersionRef{ID: "runtime43-pricing", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  1,
		OutputPerMillionNano: 1,
		InputRatePresent:     true,
		OutputRatePresent:    true,
	}
	policy := billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "runtime43-policy", Version: "v1"},
		PricingRef:          pricing.Ref,
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
	}
	operator := billing.OperatorRateSnapshot{
		Ref:                  billing.VersionRef{ID: "runtime43-operator", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  1,
		OutputPerMillionNano: 1,
		InputRatePresent:     true,
		OutputRatePresent:    true,
	}
	require.NoError(t, catalog.PutPricing(pricing))
	require.NoError(t, catalog.PutPolicy(policy))
	require.NoError(t, catalog.PutOperatorRate(operator))
	require.NoError(t, catalog.SetDefaults(pricing.Ref, policy.Ref))
	return catalog
}

func openRuntime43DurableStore(t *testing.T) *billingstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:runtime43-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", runtime43StoreSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "runtime43"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
