package runtimebundle_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	coreRuntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type authoritativeWorkerStore struct {
	billing.AuthoritativeBilling
	claims atomic.Int32
}

func (s *authoritativeWorkerStore) AppendUsageRecord(context.Context, billing.TurnUsageRecord) error {
	return nil
}

func (s *authoritativeWorkerStore) ClaimPending(context.Context, int) ([]billing.TurnUsageRecord, error) {
	s.claims.Add(1)
	return nil, nil
}

func (s *authoritativeWorkerStore) MarkProcessingRetryable(context.Context, string, string, string) error {
	return nil
}

func (s *authoritativeWorkerStore) MarkProcessingTerminal(context.Context, string, string, string) error {
	return nil
}

func (s *authoritativeWorkerStore) MarkProcessingUnreconciledCost(context.Context, string, string, string) error {
	return nil
}

func (s *authoritativeWorkerStore) MarkProcessingProcessed(context.Context, string, string, string) error {
	return nil
}

func (s *authoritativeWorkerStore) MarkProcessingInvariantFailure(context.Context, billing.TurnUsageRecord, string) error {
	return nil
}

func (s *authoritativeWorkerStore) ApplyBillingResult(context.Context, billing.ApplyBillingInput) (billing.Settlement, error) {
	return billing.Settlement{}, nil
}

type authoritativeWorkerAdmission struct{}

func (authoritativeWorkerAdmission) Authorize(context.Context, coreRuntime.BillingAdmissionInput) (billing.Authorization, error) {
	return billing.Authorization{}, nil
}

type authoritativeWorkerResolver struct{}

func (authoritativeWorkerResolver) ResolveRating(context.Context, billing.TurnUsageRecord) (billing.RatingInput, error) {
	return billing.RatingInput{}, errors.New("no records should reach resolver in lifecycle test")
}

func billingWorkerConfig() *config.Config {
	return &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
	}
}

func billingWorkerBuildOpts(store *authoritativeWorkerStore) *runtimebundle.BuildOptions {
	return &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Production: runtimebundle.ProductionOptions{
			BillingAuthoritative: true,
			BillingStore:         store,
			BillingAdmission:     authoritativeWorkerAdmission{},
			BillingIdentity: coreRuntime.BillingIdentity{
				AccountID:       func(context.Context, lipapi.Call) string { return "account" },
				AuthorizationID: func(context.Context, lipapi.Call, string) string { return "authorization" },
			},
			BillingRatingResolver: authoritativeWorkerResolver{},
		},
	}
}

func stubBillingCompose(context.Context, *config.Config, *slog.Logger, stdhttp.StandardHTTPInput) (http.Handler, error) {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
}

func assertNoWorkerClaims(t *testing.T, store *authoritativeWorkerStore, wait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if n := store.claims.Load(); n != 0 {
			t.Fatalf("unpublished generation claimed %d post-turn records", n)
		}
		time.Sleep(time.Millisecond)
	}
	if n := store.claims.Load(); n != 0 {
		t.Fatalf("unpublished generation claimed %d post-turn records", n)
	}
}

func newBillingProcess(t *testing.T, store *authoritativeWorkerStore) *runtimebundle.ProcessServices {
	t.Helper()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: billingWorkerConfig(), Log: testkit.DiscardLogger(), Opts: billingWorkerBuildOpts(store),
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func TestBuildAuthoritativeBillingStartsAndQuiescesPostTurnWorker(t *testing.T) {
	store := &authoritativeWorkerStore{}
	ps := newBillingProcess(t, store)
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		Compose: stubBillingCompose,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gen.Close() })
	assertNoWorkerClaims(t, store, 50*time.Millisecond)

	mgr := runtimehost.NewManager(runtimebundle.DefaultMaxRetainedGenerations, nil)
	t.Cleanup(func() { _ = mgr.ShutdownDetached(context.Background()) })
	published := mgr.PrepareRequestPlane("startup", gen)
	if err := mgr.Publish(published); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for store.claims.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.claims.Load() == 0 {
		t.Fatal("authoritative post-turn worker did not start and poll after Publish")
	}
	if err := gen.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := store.claims.Load()
	time.Sleep(50 * time.Millisecond)
	if store.claims.Load() != n {
		t.Fatal("post-turn worker continued polling after Quiesce")
	}
}

func TestBuildAuthoritativeBillingDoesNotStartWorkerBeforePublish(t *testing.T) {
	t.Run("compile_candidate", func(t *testing.T) {
		store := &authoritativeWorkerStore{}
		ps := newBillingProcess(t, store)
		cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
			Process: ps,
			Bus:     hooks.New(hooks.Config{}),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cand.Close() })
		assertNoWorkerClaims(t, store, 50*time.Millisecond)
	})
	t.Run("compile_candidate_activate_fault", func(t *testing.T) {
		store := &authoritativeWorkerStore{}
		ps := newBillingProcess(t, store)
		_, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
			Process: ps,
			Bus:     hooks.New(hooks.Config{}),
			FaultInject: runtimebundle.CandidateFaultInject{
				After: "activate",
			},
		})
		if !errors.Is(err, runtimebundle.ErrCandidateFaultInjected) {
			t.Fatalf("compile = %v, want injected activate fault", err)
		}
		assertNoWorkerClaims(t, store, 50*time.Millisecond)
	})
	t.Run("compile_generation", func(t *testing.T) {
		store := &authoritativeWorkerStore{}
		ps := newBillingProcess(t, store)
		gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process: ps,
			Bus:     hooks.New(hooks.Config{}),
			Compose: stubBillingCompose,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = gen.Close() })
		assertNoWorkerClaims(t, store, 50*time.Millisecond)
	})

	postActivateFaults := []string{"handler", "composer-clone", "ledger-transfer"}
	for _, after := range postActivateFaults {
		t.Run("compile_generation_"+after+"_fault", func(t *testing.T) {
			store := &authoritativeWorkerStore{}
			ps := newBillingProcess(t, store)
			_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
				Process: ps,
				Bus:     hooks.New(hooks.Config{}),
				Compose: stubBillingCompose,
				FaultInject: runtimebundle.CandidateFaultInject{
					After: after,
				},
			})
			if err == nil {
				t.Fatalf("compile succeeded, want %s fault", after)
			}
			if after != "ledger-transfer" && !errors.Is(err, runtimebundle.ErrCandidateFaultInjected) {
				t.Fatalf("compile = %v, want injected %s fault", err, after)
			}
			assertNoWorkerClaims(t, store, 50*time.Millisecond)
		})
	}
}

func TestBuildConfigAuthoritativeBillingRequiresInjectedStore(t *testing.T) {
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Accounting: config.AccountingConfig{Billing: config.AccountingBillingConfig{Authoritative: true, ReportsPath: "/admin/billing"}},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	if !errors.Is(err, runtimebundle.ErrAuthoritativeBillingRequired) {
		t.Fatalf("config cutover without store = %v, want ErrAuthoritativeBillingRequired", err)
	}
}

func TestBuildConfigAuthoritativeBillingEnablesCutoverWhenStoreInjected(t *testing.T) {
	store := &authoritativeWorkerStore{}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Accounting: config.AccountingConfig{Billing: config.AccountingBillingConfig{Authoritative: true, ReportsPath: "/admin/billing-cutover"}},
	}
	_, bundle := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Production: runtimebundle.ProductionOptions{
			BillingStore:     store,
			BillingAdmission: authoritativeWorkerAdmission{},
			BillingIdentity: coreRuntime.BillingIdentity{
				AccountID:       func(context.Context, lipapi.Call) string { return "account" },
				AuthorizationID: func(context.Context, lipapi.Call, string) string { return "authorization" },
			},
			BillingRatingResolver: authoritativeWorkerResolver{},
		},
	})
	if bundle.Executor() == nil || !bundle.Executor().BillingAuthoritative {
		t.Fatal("config authoritative did not cut monetary runtime path over")
	}
	httpIn := bundle.StandardHTTPInput(cfg, nil, "")
	if httpIn.Operations.BillingReportsPath != "/admin/billing-cutover" {
		t.Fatalf("reports path = %q", httpIn.Operations.BillingReportsPath)
	}
	if httpIn.Operations.BillingReports == nil {
		t.Fatal("authoritative reports were not mounted from BillingStore")
	}
	if httpIn.Operations.BillingProvisioner != nil {
		t.Fatal("store without AccountProvisioner must leave BillingProvisioner nil")
	}
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = bundle.Close()
}

type provisionerAuthoritativeStore struct {
	authoritativeWorkerStore
}

func (s *provisionerAuthoritativeStore) CreateAccount(context.Context, billing.Account) error {
	return nil
}

func (s *provisionerAuthoritativeStore) PostFunding(context.Context, billing.FundingInput) (billing.Posting, error) {
	return billing.Posting{}, nil
}

func (s *provisionerAuthoritativeStore) ChangeCreditPolicy(context.Context, billing.CreditPolicyInput) (billing.PolicyChange, error) {
	return billing.PolicyChange{}, nil
}

func TestBuildConfigAuthoritativeBillingCopiesProvisionerWhenStoreImplementsIt(t *testing.T) {
	store := &provisionerAuthoritativeStore{}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Accounting: config.AccountingConfig{Billing: config.AccountingBillingConfig{Authoritative: true, ReportsPath: "/admin/billing-cutover"}},
	}
	_, bundle := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Production: runtimebundle.ProductionOptions{
			BillingStore:     store,
			BillingAdmission: authoritativeWorkerAdmission{},
			BillingIdentity: coreRuntime.BillingIdentity{
				AccountID:       func(context.Context, lipapi.Call) string { return "account" },
				AuthorizationID: func(context.Context, lipapi.Call, string) string { return "authorization" },
			},
			BillingRatingResolver: authoritativeWorkerResolver{},
		},
	})
	httpIn := bundle.StandardHTTPInput(cfg, nil, "")
	if httpIn.Operations.BillingReports == nil {
		t.Fatal("authoritative reports were not mounted from BillingStore")
	}
	got, ok := httpIn.Operations.BillingProvisioner.(*provisionerAuthoritativeStore)
	if !ok || got != store {
		t.Fatalf("BillingProvisioner = %T (%v), want injected store %T", httpIn.Operations.BillingProvisioner, httpIn.Operations.BillingProvisioner, store)
	}
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = bundle.Close()
}
