package runtimebundle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	_ "modernc.org/sqlite"
)

const (
	billingHostLoopBackendID    = "backend"
	billingHostLoopModelID      = "model"
	billingHostLoopHoldNano     = int64(1000)
	billingHostLoopOpeningNano  = int64(10000)
	billingHostLoopInputTokens  = 1_000_000
	billingHostLoopOutputTokens = 1_000_000
	billingHostLoopCustomerNano = int64(310) // 100 input + 200 output + 10 fixed
	billingHostLoopOperatorNano = int64(125) // 50 input + 75 output
)

var billingHostLoopMissingOperatorRef = billing.VersionRef{ID: "missing-operator", Version: "v9"}

var billingHostLoopSeq atomic.Uint64

func TestBillingHostLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store := openBillingHostLoopStore(t)
	catalog, pricing, policy, operator := seedBillingHostLoopCatalog(t)
	if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding: %v", err)
	}

	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:    store,
		Catalog:  catalog,
		Identity: nil,
		Currency: "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128000, true, nil
		},
		Strict:              true,
		ConservativeCeiling: &ceiling,
		PostTurnBatchSize:   1,
		PostTurnInterval:    10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      writeBillingHostLoopConfig(t),
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		Production:      prod,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	accountID := fmt.Sprintf("hostloop%d", billingHostLoopSeq.Add(1))
	provisionBillingHostLoopAccount(t, store, accountID)

	executor := hostActiveExecutor(t, host)
	injectBillingHostLoopUsageBackend(t, executor)

	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{
		PrincipalID: scope.Known(accountID),
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Session: lipapi.SessionRef{
			ClientSessionID: "client-hint-must-ignore",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertNoStreamPrices(t, drainBillingHostLoopStream(t, ctx, stream))
	if call.Session.AuthoritativeSessionID == "" || call.Session.ALegID == "" {
		t.Fatalf("secure session did not stamp AuthoritativeSessionID/A-leg: %+v", call.Session)
	}
	if call.Session.AuthoritativeSessionID == "client-hint-must-ignore" {
		t.Fatal("AuthoritativeSessionID must not be the client session hint")
	}

	executor.WaitBillingHandoffRetries()
	processed := waitBillingHostLoopProcessed(t, store, accountID)
	record, err := store.GetUsageRecord(ctx, processed.TURKey)
	if err != nil {
		t.Fatalf("GetUsageRecord: %v", err)
	}
	hold, err := store.GetAuthorization(ctx, accountID, record.Key)
	if err != nil {
		t.Fatalf("GetAuthorization: %v", err)
	}
	assertVersionRefIdentity(t, "hold CustomerPricingRef", hold.PricingRef, pricing.Ref)
	assertVersionRefIdentity(t, "hold ChargePolicyRef", hold.ChargePolicyRef, policy.Ref)
	assertVersionRefIdentity(t, "TUR CustomerPricingRef", record.CustomerPricingRef, pricing.Ref)
	assertVersionRefIdentity(t, "TUR ChargePolicyRef", record.ChargePolicyRef, policy.Ref)
	if len(record.Legs) != 1 {
		t.Fatalf("TUR legs = %+v, want 1", record.Legs)
	}
	assertVersionRefIdentity(t, "leg OperatorRateRef", record.Legs[0].OperatorRateRef, operator.Ref)
	if !record.Legs[0].Evidence.InputTokens.Present || !record.Legs[0].Evidence.OutputTokens.Present {
		t.Fatalf("sealed evidence missing input+output: %+v", record.Legs[0].Evidence)
	}
	if record.Legs[0].Evidence.Cost.Present {
		t.Fatalf("stream price leaked onto LUR: %+v", record.Legs[0].Evidence)
	}

	report, err := store.AccountReport(ctx, accountID, billing.PageRequest{Limit: 100})
	if err != nil {
		t.Fatalf("AccountReport: %v", err)
	}
	wantBalance := billingHostLoopOpeningNano - billingHostLoopCustomerNano
	if report.Account.BalanceNano != wantBalance || report.SpendableNano != wantBalance || report.Account.ReservedNano != 0 {
		t.Fatalf("account report balance=%d spendable=%d reserved=%d, want balance=spendable=%d reserved=0",
			report.Account.BalanceNano, report.SpendableNano, report.Account.ReservedNano, wantBalance)
	}
	explanation, err := store.TurnExplanation(ctx, record.Key)
	if err != nil {
		t.Fatalf("TurnExplanation: %v", err)
	}
	if explanation.Result.CustomerCharge.Nano != billingHostLoopCustomerNano ||
		explanation.Result.ProviderCost.Nano != billingHostLoopOperatorNano {
		t.Fatalf("turn explanation result = %+v, want customer=%d operator=%d (journal, not stream events)",
			explanation.Result, billingHostLoopCustomerNano, billingHostLoopOperatorNano)
	}
	assertVersionRefIdentity(t, "explanation hold pricing", explanation.Authorization.PricingRef, pricing.Ref)
	assertVersionRefIdentity(t, "explanation hold policy", explanation.Authorization.ChargePolicyRef, policy.Ref)
}

func TestBillingHostLoop_MissingCatalogRefs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store := openBillingHostLoopStore(t)
	catalog, pricing, policy, _ := seedBillingHostLoopCatalog(t)
	// Do not bind the published operator-rate body. Stamp a VersionRef that was
	// never Put so SnapshotsFor fails closed after admission still succeeds.

	identity := billingcompose.PrincipalSessionIdentity(billingcompose.SnapshotRefFuncs{
		CustomerPricingRef: catalog.CustomerPricingRef,
		ChargePolicyRef:    catalog.ChargePolicyRef,
		OperatorRateRef: func(context.Context, string, string) billing.VersionRef {
			return billingHostLoopMissingOperatorRef
		},
	})

	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:    store,
		Catalog:  catalog,
		Identity: &identity,
		Currency: "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128000, true, nil
		},
		Strict:              true,
		ConservativeCeiling: &ceiling,
		PostTurnBatchSize:   1,
		PostTurnInterval:    10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      writeBillingHostLoopConfig(t),
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		Production:      prod,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	accountID := fmt.Sprintf("hostloop%d", billingHostLoopSeq.Add(1))
	provisionBillingHostLoopAccount(t, store, accountID)

	executor := hostActiveExecutor(t, host)
	injectBillingHostLoopUsageBackend(t, executor)

	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{
		PrincipalID: scope.Known(accountID),
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Session: lipapi.SessionRef{
			ClientSessionID: "client-hint-must-ignore",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertNoStreamPrices(t, drainBillingHostLoopStream(t, ctx, stream))

	executor.WaitBillingHandoffRetries()
	processing := waitBillingHostLoopFailClosed(t, store, accountID)
	if processing.Status == billing.ProcessingProcessed {
		t.Fatalf("missing catalog refs must not settle as processed: %+v", processing)
	}
	if processing.Status == billing.ProcessingRetryable && processing.SafeErrorCode != "rating_input_unavailable" {
		t.Fatalf("retryable SafeErrorCode = %q, want rating_input_unavailable: %+v", processing.SafeErrorCode, processing)
	}

	record, err := store.GetUsageRecord(ctx, processing.TURKey)
	if err != nil {
		t.Fatalf("GetUsageRecord: %v", err)
	}
	if len(record.Legs) != 1 {
		t.Fatalf("TUR legs = %+v, want 1", record.Legs)
	}
	assertVersionRefIdentity(t, "leg OperatorRateRef", record.Legs[0].OperatorRateRef, billingHostLoopMissingOperatorRef)
	assertVersionRefIdentity(t, "TUR CustomerPricingRef", record.CustomerPricingRef, pricing.Ref)
	assertVersionRefIdentity(t, "TUR ChargePolicyRef", record.ChargePolicyRef, policy.Ref)

	report, err := store.AccountReport(ctx, accountID, billing.PageRequest{Limit: 100})
	if err != nil {
		t.Fatalf("AccountReport: %v", err)
	}
	if report.Account.BalanceNano != billingHostLoopOpeningNano {
		t.Fatalf("account balance=%d, want opening %d (no invented customer charge)",
			report.Account.BalanceNano, billingHostLoopOpeningNano)
	}
	for _, journal := range report.Transactions {
		if journal.OperationKind == "customer_settlement" {
			t.Fatalf("customer_settlement posted despite missing catalog refs: %+v", journal)
		}
	}

	explanation, err := store.TurnExplanation(ctx, record.Key)
	if err != nil {
		t.Fatalf("TurnExplanation: %v", err)
	}
	if explanation.Result.Processed || explanation.Result.Status == billing.ProcessingProcessed {
		t.Fatalf("turn explanation must not show processed catalog-rated settlement: %+v", explanation.Result)
	}
	if explanation.Result.CustomerCharge.Nano == billingHostLoopCustomerNano {
		t.Fatalf("turn explanation fabricated catalog-rated customer charge %d: %+v",
			billingHostLoopCustomerNano, explanation.Result)
	}
}

func openBillingHostLoopStore(t *testing.T) *billingstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:billing-host-loop-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", billingHostLoopSeq.Add(1))
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
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "billing-host-loop"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedBillingHostLoopCatalog(t *testing.T) (*billingcompose.SnapshotCatalog, billing.PricingSnapshot, billing.ChargePolicy, billing.OperatorRateSnapshot) {
	t.Helper()
	catalog := billingcompose.NewSnapshotCatalog()
	pricing := billing.PricingSnapshot{
		Ref:                  billing.VersionRef{ID: "pricing", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  100,
		OutputPerMillionNano: 200,
		InputRatePresent:     true,
		OutputRatePresent:    true,
		FixedCharges:         []billing.ChargeComponent{{Name: "request", Amount: billing.Money{Nano: 10, Currency: "USD"}}},
	}
	policy := billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "policy", Version: "v1"},
		PricingRef:          pricing.Ref,
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}
	operator := billing.OperatorRateSnapshot{
		Ref:                  billing.VersionRef{ID: "operator-rates", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  50,
		OutputPerMillionNano: 75,
		InputRatePresent:     true,
		OutputRatePresent:    true,
	}
	if err := catalog.PutPricing(pricing); err != nil {
		t.Fatal(err)
	}
	if err := catalog.PutPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := catalog.PutOperatorRate(operator); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SetDefaults(pricing.Ref, policy.Ref); err != nil {
		t.Fatal(err)
	}
	return catalog, pricing, policy, operator
}

func provisionBillingHostLoopAccount(t *testing.T, store billing.AccountProvisioner, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{
		ID:       accountID,
		Currency: "USD",
		Mode:     billing.AccountPrepaid,
		State:    billing.AccountReady,
		Version:  1,
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := store.PostFunding(ctx, billing.FundingInput{
		AccountID: accountID,
		Amount:    billing.Money{Nano: billingHostLoopOpeningNano, Currency: "USD"},
		SourceKey: "opening-topup",
		Reason:    "host-loop prepaid funding",
	}); err != nil {
		t.Fatalf("PostFunding: %v", err)
	}
}

func hostActiveExecutor(t *testing.T, host *runtimebundle.Host) *coreruntime.Executor {
	t.Helper()
	if host == nil {
		t.Fatal("nil host")
	}
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("nil active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("active generation missing ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatal("expected *runtime.Executor from active generation")
	}
	return ex
}

func injectBillingHostLoopUsageBackend(t *testing.T, executor *coreruntime.Executor) {
	t.Helper()
	be := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return lipapi.NewFixedEventStream(billingHostLoopUsageEvents()), nil
		},
	}
	if executor.Backends == nil {
		executor.Backends = map[string]execbackend.Backend{}
	}
	executor.Backends[billingHostLoopBackendID] = be
	capFn := func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
		return execbackend.EffectiveCaps(ctx, be, call, cand)
	}
	switch capMap := executor.CapsResolver.(type) {
	case capabilities.MapResolver:
		capMap[billingHostLoopBackendID] = capFn
	case nil:
		executor.CapsResolver = capabilities.MapResolver{billingHostLoopBackendID: capFn}
	default:
		t.Fatalf("CapsResolver type %T cannot accept injected backend caps", executor.CapsResolver)
	}
}

func billingHostLoopUsageEvents() []lipapi.Event {
	return []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hello"},
		{
			Kind:          lipapi.EventUsageDelta,
			InputTokens:   billingHostLoopInputTokens,
			OutputTokens:  billingHostLoopOutputTokens,
			TotalTokens:   billingHostLoopInputTokens + billingHostLoopOutputTokens,
			UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
			Accounting: lipapi.UsageAccountingMetadata{
				Source:    lipapi.UsageSourceProviderReported,
				Authority: lipapi.UsageAuthorityAuthoritative,
			},
		},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	}
}

func drainBillingHostLoopStream(t *testing.T, ctx context.Context, stream lipapi.EventStream) []lipapi.Event {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []lipapi.Event
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
}

func assertNoStreamPrices(t *testing.T, events []lipapi.Event) {
	t.Helper()
	var sawUsage bool
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		sawUsage = true
		if ev.CostPresent || ev.CostNanoUnits != 0 {
			t.Fatalf("stream price enrichment leaked: %+v", ev)
		}
	}
	if !sawUsage {
		t.Fatal("expected provider usage on the client stream")
	}
}

func waitBillingHostLoopProcessed(t *testing.T, store *billingstore.DurableStore, accountID string) billing.UsageRecordProcessing {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := store.QueryProcessing(ctx, billing.ReportFilter{
			AccountID: accountID,
			Status:    billing.ProcessingProcessed,
			Page:      billing.PageRequest{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 {
			return page.Items[0]
		}
		if time.Now().After(deadline) {
			all, qerr := store.QueryProcessing(ctx, billing.ReportFilter{
				AccountID: accountID,
				Page:      billing.PageRequest{Limit: 10},
			})
			t.Fatalf("timed out waiting for processed TUR: items=%+v query_err=%v", all.Items, qerr)
		}
		<-ticker.C
	}
}

func waitBillingHostLoopFailClosed(t *testing.T, store *billingstore.DurableStore, accountID string) billing.UsageRecordProcessing {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := store.QueryProcessing(ctx, billing.ReportFilter{
			AccountID: accountID,
			Page:      billing.PageRequest{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.Status == billing.ProcessingProcessed {
				t.Fatalf("missing catalog refs marked processed: %+v", item)
			}
			switch item.Status {
			case billing.ProcessingRetryable, billing.ProcessingUnreconciledCost, billing.ProcessingTerminalError:
				return item
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for fail-closed processing: items=%+v", page.Items)
		}
		<-ticker.C
	}
}

func assertVersionRefIdentity(t *testing.T, label string, got, want billing.VersionRef) {
	t.Helper()
	if got.ID != want.ID || got.Version != want.Version {
		t.Fatalf("%s = %+v, want %+v", label, got, want)
	}
}

func writeBillingHostLoopConfig(t *testing.T) string {
	t.Helper()
	cfg := `server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 1
  default_route: "backend:model"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: backend
      kind: openai-responses
      enabled: false
      config: {}
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`
	path := filepath.Join(t.TempDir(), "billing-host-loop.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
