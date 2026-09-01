package runtimebundle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
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
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store := openBillingHostLoopStore(t)
	catalog, pricing, policy, operator := seedBillingHostLoopCatalog(t)
	if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding: %v", err)
	}

	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:             store,
		TerminalUsageSink: store,
		Catalog:           catalog,
		Identity:          nil,
		Currency:          "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128000, true, nil
		},
		Strict:              true,
		ConservativeCeiling: &ceiling,
		PostTurnBatchSize:   1,
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
	if executor.BillingExposureAdmission == nil {
		t.Fatal("exposure generation must expose operational admission")
	}
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

	callRecord, exposure, complete := waitBillingHostLoopCall(t, store, accountID)
	assertVersionRefIdentity(t, "call CustomerPricingRef", callRecord.CustomerPricingRef, pricing.Ref)
	assertVersionRefIdentity(t, "call ChargePolicyRef", callRecord.ChargePolicyRef, policy.Ref)
	if exposure.IsOpen() {
		t.Fatalf("customer settlement left exposure open: %+v", exposure)
	}
	if len(complete.Legs) != 1 {
		t.Fatalf("call legs = %+v, want 1", complete.Legs)
	}
	assertVersionRefIdentity(t, "leg OperatorRateRef", complete.Legs[0].OperatorRateRef, operator.Ref)
	if !complete.Legs[0].Evidence.InputTokens.Present || !complete.Legs[0].Evidence.OutputTokens.Present {
		t.Fatalf("durable leg missing input+output: %+v", complete.Legs[0].Evidence)
	}
	if complete.Legs[0].Evidence.Cost.Present {
		t.Fatalf("stream price leaked onto independent LUR: %+v", complete.Legs[0].Evidence)
	}

	report := waitBillingHostLoopProviderCost(t, store, accountID)
	wantBalance := billingHostLoopOpeningNano - billingHostLoopCustomerNano
	if report.Account.BalanceNano != wantBalance || report.SpendableNano != wantBalance {
		t.Fatalf("account report balance=%d spendable=%d, want balance=spendable=%d",
			report.Account.BalanceNano, report.SpendableNano, wantBalance)
	}
	var sawCustomerSettlement, sawProviderCost bool
	for _, transaction := range report.Transactions {
		switch transaction.OperationKind {
		case "customer_call_settlement":
			sawCustomerSettlement = true
			if transaction.Entries[0].Amount.Nano != billingHostLoopCustomerNano {
				t.Fatalf("customer settlement amount = %+v, want %d", transaction.Entries, billingHostLoopCustomerNano)
			}
		case "provider_call_cogs":
			sawProviderCost = true
			if transaction.Entries[0].Amount.Nano != billingHostLoopOperatorNano {
				t.Fatalf("provider COGS amount = %+v, want %d", transaction.Entries, billingHostLoopOperatorNano)
			}
		}
	}
	if !sawCustomerSettlement || !sawProviderCost {
		t.Fatalf("account report missing independent customer/provider operations: %+v", report.Transactions)
	}
	operatorReport, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 100}})
	if err != nil {
		t.Fatalf("OperatorCostReport: %v", err)
	}
	if operatorReport.ProviderCost.Nano != billingHostLoopOperatorNano || len(operatorReport.Rows) != 1 || operatorReport.Rows[0].Amount.Nano != billingHostLoopOperatorNano {
		t.Fatalf("operator report = %+v, want one independent B-leg cost=%d", operatorReport, billingHostLoopOperatorNano)
	}
}

func TestBillingHostLoop_FailoverOpenFailureClaimsAndSettles(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store := openBillingHostLoopStore(t)
	catalog, pricing, policy, operator := seedBillingHostLoopCatalog(t)
	for _, backendID := range []string{"bad", "good"} {
		if err := catalog.SetOperatorRateBinding(backendID, billingHostLoopModelID, operator.Ref); err != nil {
			t.Fatalf("SetOperatorRateBinding(%s): %v", backendID, err)
		}
	}

	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:             store,
		TerminalUsageSink: store,
		Catalog:           catalog,
		Identity:          nil,
		Currency:          "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128000, true, nil
		},
		Strict:              true,
		ConservativeCeiling: &ceiling,
		PostTurnBatchSize:   1,
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

	accountID := fmt.Sprintf("hostloop-failover%d", billingHostLoopSeq.Add(1))
	provisionBillingHostLoopAccount(t, store, accountID)
	executor := hostActiveExecutor(t, host)
	opens := injectBillingHostLoopFailoverBackends(t, executor)

	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)})
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "bad:" + billingHostLoopModelID + "|good:" + billingHostLoopModelID},
		Session:  lipapi.SessionRef{ClientSessionID: "client-failover-hint"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	stream, err := executor.Execute(execCtx, call)
	if err != nil {
		t.Fatalf("Execute failover call: %v", err)
	}
	assertNoStreamPrices(t, drainBillingHostLoopStream(t, ctx, stream))
	if got := opens.Load(); got != 2 {
		t.Fatalf("backend opens = %d, want failed bad open plus successful good open", got)
	}

	callRecord, exposure, complete := waitBillingHostLoopCall(t, store, accountID)
	assertVersionRefIdentity(t, "call CustomerPricingRef", callRecord.CustomerPricingRef, pricing.Ref)
	assertVersionRefIdentity(t, "call ChargePolicyRef", callRecord.ChargePolicyRef, policy.Ref)
	if exposure.IsOpen() {
		t.Fatalf("failover settlement left exposure open: %+v", exposure)
	}
	if len(complete.Legs) != 2 {
		t.Fatalf("complete call legs = %+v, want never-started failure plus winner", complete.Legs)
	}
	var sawNeverStarted, sawWinner bool
	for _, leg := range complete.Legs {
		assertVersionRefIdentity(t, "leg OperatorRateRef", leg.OperatorRateRef, operator.Ref)
		switch leg.Outcome {
		case billing.LegOutcomeNeverStarted:
			sawNeverStarted = true
		case billing.LegOutcomeWinner:
			sawWinner = true
			if leg.BackendID != "good" {
				t.Fatalf("winner backend = %q, want good", leg.BackendID)
			}
		}
	}
	if !sawNeverStarted || !sawWinner {
		t.Fatalf("joined legs missing never-started/winner outcomes: %+v", complete.Legs)
	}

	report := waitBillingHostLoopProviderCost(t, store, accountID)
	wantBalance := billingHostLoopOpeningNano - billingHostLoopCustomerNano
	if report.Account.BalanceNano != wantBalance {
		t.Fatalf("settled account balance=%d, want balance=%d", report.Account.BalanceNano, wantBalance)
	}
	var customerSettlements int
	for _, transaction := range report.Transactions {
		if transaction.OperationKind == "customer_call_settlement" {
			customerSettlements++
			if transaction.Entries[0].Amount.Nano != billingHostLoopCustomerNano {
				t.Fatalf("customer settlement entries = %+v, want %d", transaction.Entries, billingHostLoopCustomerNano)
			}
		}
	}
	if customerSettlements != 1 {
		t.Fatalf("customer settlement transactions = %d, want exactly one", customerSettlements)
	}
}

func TestBillingHostLoop_MissingCatalogRefs(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store := openBillingHostLoopStore(t)
	catalog, pricing, policy, _ := seedBillingHostLoopCatalog(t)
	// Do not bind the published operator-rate body. Stamp a VersionRef that was
	// never Put: the customer path must settle while provider-cost resolution
	// stays unreconciled for the same leg.

	identity := billingcompose.PrincipalSessionIdentity(billingcompose.SnapshotRefFuncs{
		CustomerPricingRef: catalog.CustomerPricingRef,
		ChargePolicyRef:    catalog.ChargePolicyRef,
		OperatorRateRef: func(context.Context, string, string) billing.VersionRef {
			return billingHostLoopMissingOperatorRef
		},
	})

	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:             store,
		TerminalUsageSink: store,
		Catalog:           catalog,
		Identity:          &identity,
		Currency:          "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128000, true, nil
		},
		Strict:              true,
		ConservativeCeiling: &ceiling,
		PostTurnBatchSize:   1,
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
	if executor.BillingExposureAdmission == nil {
		t.Fatal("exposure generation must expose operational admission")
	}
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

	// The customer path no longer resolves operator-rate snapshots: this call
	// settles and closes exposure even though the persisted OperatorRateRef has
	// no published rate. The same leg's provider-cost work stays
	// pending/unreconciled and never posts COGS or alters the customer posting.
	callRecord, exposure, complete := waitBillingHostLoopCall(t, store, accountID)
	assertVersionRefIdentity(t, "call CustomerPricingRef", callRecord.CustomerPricingRef, pricing.Ref)
	assertVersionRefIdentity(t, "call ChargePolicyRef", callRecord.ChargePolicyRef, policy.Ref)
	if exposure.IsOpen() {
		t.Fatalf("missing operator rate must not keep customer exposure open: %+v", exposure)
	}
	if len(complete.Legs) != 1 {
		t.Fatalf("complete call legs = %+v, want 1", complete.Legs)
	}
	assertVersionRefIdentity(t, "leg OperatorRateRef", complete.Legs[0].OperatorRateRef, billingHostLoopMissingOperatorRef)

	report, err := store.AccountReport(ctx, accountID, billing.PageRequest{Limit: 100})
	if err != nil {
		t.Fatalf("AccountReport: %v", err)
	}
	wantBalance := billingHostLoopOpeningNano - billingHostLoopCustomerNano
	if report.Account.BalanceNano != wantBalance {
		t.Fatalf("settled balance = %d, want %d (customer settlement proceeds without operator rate)", report.Account.BalanceNano, wantBalance)
	}
	var customerSettlements int
	for _, journal := range report.Transactions {
		switch journal.OperationKind {
		case "customer_call_settlement":
			customerSettlements++
			if journal.Entries[0].Amount.Nano != billingHostLoopCustomerNano {
				t.Fatalf("customer settlement entries = %+v, want %d", journal.Entries, billingHostLoopCustomerNano)
			}
		case "provider_call_cogs":
			t.Fatalf("provider COGS posted despite missing operator rate: %+v", journal)
		}
	}
	if customerSettlements != 1 {
		t.Fatalf("customer settlement transactions = %d, want exactly one", customerSettlements)
	}
	// Read the durable row directly instead of waiting for a retry to become
	// due. The worker's first failed attempt records pending state, attempt
	// metadata, and an unreconciled marker; the next retry may be arbitrarily
	// later because backoff is deliberately durable.
	sealedLeg, err := complete.Legs[0].Seal()
	if err != nil {
		t.Fatalf("seal complete leg: %v", err)
	}
	var workState billingstore.ProviderCostWorkState
	var providerReport billing.OperatorCostReport
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, stateErr := store.GetProviderCostWorkState(ctx, sealedLeg.Key)
		if stateErr != nil && !errors.Is(stateErr, billingstore.ErrUsageRecordNotFound) {
			t.Fatalf("GetProviderCostWorkState: %v", stateErr)
		}
		providerReport, err = store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 100}})
		if err != nil {
			t.Fatalf("OperatorCostReport after provider failure: %v", err)
		}
		if stateErr == nil && state.Status == "pending" && state.AttemptCount >= 1 && strings.Contains(state.LastError, "exact_operator_rate_unavailable") && providerReport.UnreconciledCosts == 1 {
			workState = state
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for durable provider-cost failure state: state=%+v report=%+v", state, providerReport)
		case <-ticker.C:
		}
	}
	if workState.Status != "pending" || workState.AttemptCount < 1 || !strings.Contains(workState.LastError, "exact_operator_rate_unavailable") {
		t.Fatalf("provider-cost retry state = %+v, want pending with recorded unavailable-rate failure", workState)
	}
	if providerReport.UnreconciledCosts != 1 {
		t.Fatalf("unreconciled provider costs = %d, want 1", providerReport.UnreconciledCosts)
	}

	// Re-read customer state after provider failure handling. The diagnostic
	// marker and deferred work must not mutate the completed customer posting or
	// reopen its exposure.
	afterReport, err := store.AccountReport(ctx, accountID, billing.PageRequest{Limit: 100})
	if err != nil {
		t.Fatalf("AccountReport after provider failure: %v", err)
	}
	for _, journal := range afterReport.Transactions {
		if journal.OperationKind == "provider_call_cogs" {
			t.Fatalf("provider COGS posted after missing operator rate: %+v", journal)
		}
	}
	if afterReport.Account.BalanceNano != report.Account.BalanceNano || afterReport.Account.Version != report.Account.Version {
		t.Fatalf("provider failure changed customer report: before=%+v after=%+v", report.Account, afterReport.Account)
	}
	afterAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("GetAccount after provider failure: %v", err)
	}
	if afterAccount.BalanceNano != report.Account.BalanceNano || afterAccount.Version != report.Account.Version {
		t.Fatalf("provider failure mutated customer account: before=%+v after=%+v", report.Account, afterAccount)
	}
	afterExposure, err := store.GetCallExposure(ctx, complete.Closure.CallID)
	if err != nil {
		t.Fatalf("GetCallExposure after provider failure: %v", err)
	}
	if afterExposure != exposure || afterExposure.IsOpen() {
		t.Fatalf("provider failure mutated/reopened customer exposure: before=%+v after=%+v", exposure, afterExposure)
	}
}

func TestBillingHostLoop_AdmissionDeny(t *testing.T) {
	t.Parallel()
	t.Run("custom mapping returns empty identity", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		store := openBillingHostLoopStore(t)
		catalog, _, _, operator := seedBillingHostLoopCatalog(t)
		if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
			t.Fatalf("SetOperatorRateBinding: %v", err)
		}

		identity := billingcompose.PrincipalSessionIdentity(billingcompose.SnapshotRefFuncs{
			CustomerPricingRef: catalog.CustomerPricingRef,
			ChargePolicyRef:    catalog.ChargePolicyRef,
			OperatorRateRef:    catalog.OperatorRateRef,
		})
		identity.AccountID = func(context.Context, lipapi.Call) string { return "" }

		executor, opens := startBillingHostLoopHost(
			t,
			ctx,
			store,
			catalog,
			&identity,
		)

		accountID := fmt.Sprintf("hostloop%d", billingHostLoopSeq.Add(1))
		provisionBillingHostLoopAccount(t, store, accountID)
		if !billingHostLoopAccountExists(t, store, accountID) {
			t.Fatal("provisioned account missing before Execute")
		}

		stream, err := executeBillingHostLoopCall(ctx, executor, accountID)
		assertBillingHostLoopAdmissionDenied(
			t,
			stream,
			err,
			opens,
			billing.ErrCreditScreenInvalid,
			coreruntime.ErrBillingCreditScreenDenied,
		)
		if !billingHostLoopAccountExists(t, store, accountID) {
			t.Fatal("empty identity mapping must not delete the provisioned account")
		}
		if billingHostLoopAccountExists(t, store, "") {
			t.Fatal("empty identity mapping must not CreateAccount as a request side effect")
		}
	})

	t.Run("stock mapping principal has no billing account", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		store := openBillingHostLoopStore(t)
		catalog, _, _, operator := seedBillingHostLoopCatalog(t)
		if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
			t.Fatalf("SetOperatorRateBinding: %v", err)
		}

		executor, opens := startBillingHostLoopHost(
			t,
			ctx,
			store,
			catalog,
			nil,
		)

		principalID := fmt.Sprintf("missing-hostloop%d", billingHostLoopSeq.Add(1))
		if billingHostLoopAccountExists(t, store, principalID) {
			t.Fatal("test setup must not CreateAccount for this principal")
		}

		stream, err := executeBillingHostLoopCall(ctx, executor, principalID)
		assertBillingHostLoopAdmissionDenied(
			t,
			stream,
			err,
			opens,
			billingstore.ErrAccountNotFound,
			billing.ErrAccountNotFound,
			billing.ErrAccountNotReady,
			billing.ErrInsufficientSpendable,
		)
		if billingHostLoopAccountExists(t, store, principalID) {
			t.Fatal("missing-account deny must not CreateAccount on the request path")
		}
	})
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

func injectBillingHostLoopUsageBackend(t *testing.T, executor *coreruntime.Executor) *atomic.Int32 {
	t.Helper()
	var opens atomic.Int32
	be := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
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
	return &opens
}

func injectBillingHostLoopFailoverBackends(t *testing.T, executor *coreruntime.Executor) *atomic.Int32 {
	t.Helper()
	var opens atomic.Int32
	bad := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return nil, lipapi.RecoverablePreOutputError(errors.New("bad backend unavailable before output"))
		},
	}
	good := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return lipapi.NewFixedEventStream(billingHostLoopUsageEvents()), nil
		},
	}
	if executor.Backends == nil {
		executor.Backends = map[string]execbackend.Backend{}
	}
	executor.Backends["bad"] = bad
	executor.Backends["good"] = good
	capMap := capabilities.MapResolver{}
	for backendID, backend := range map[string]execbackend.Backend{"bad": bad, "good": good} {
		be := backend
		capMap[backendID] = func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
			return execbackend.EffectiveCaps(ctx, be, call, cand)
		}
	}
	switch existing := executor.CapsResolver.(type) {
	case capabilities.MapResolver:
		maps.Copy(existing, capMap)
	case nil:
		executor.CapsResolver = capMap
	default:
		t.Fatalf("CapsResolver type %T cannot accept failover backend caps", executor.CapsResolver)
	}
	return &opens
}

//nolint:revive // test helper takes *testing.T as first argument
func startBillingHostLoopHost(
	t *testing.T,
	ctx context.Context,
	store *billingstore.DurableStore,
	catalog *billingcompose.SnapshotCatalog,
	identity *coreruntime.BillingIdentity,
) (*coreruntime.Executor, *atomic.Int32) {
	t.Helper()
	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:             store,
		TerminalUsageSink: store,
		Catalog:           catalog,
		Identity:          identity,
		Currency:          "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128000, true, nil
		},
		Strict:              true,
		ConservativeCeiling: &ceiling,
		PostTurnBatchSize:   1,
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
	executor := hostActiveExecutor(t, host)
	opens := injectBillingHostLoopUsageBackend(t, executor)
	return executor, opens
}

func executeBillingHostLoopCall(ctx context.Context, executor *coreruntime.Executor, principalID string) (lipapi.EventStream, error) {
	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{
		PrincipalID: scope.Known(principalID),
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
	return executor.Execute(execCtx, call)
}

func assertBillingHostLoopAdmissionDenied(
	t *testing.T,
	stream lipapi.EventStream,
	err error,
	opens *atomic.Int32,
	sentinels ...error,
) {
	t.Helper()
	if stream != nil {
		_ = stream.Close()
		t.Fatal("Execute returned a stream; admission must deny before upstream")
	}
	if err == nil {
		t.Fatal("Execute succeeded; want admission deny")
	}
	if !errors.Is(err, coreruntime.ErrBillingAdmissionDenied) {
		t.Fatalf("Execute = %v, want %v", err, coreruntime.ErrBillingAdmissionDenied)
	}
	matched := false
	for _, sentinel := range sentinels {
		if errors.Is(err, sentinel) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("Execute = %v, want wrapping one of %v", err, sentinels)
	}
	if n := opens.Load(); n != 0 {
		t.Fatalf("backend Open = %d, want 0", n)
	}
}

func billingHostLoopAccountExists(t *testing.T, store *billingstore.DurableStore, accountID string) bool {
	t.Helper()
	_, err := store.GetAccount(context.Background(), accountID)
	if err == nil {
		return true
	}
	if errors.Is(err, billingstore.ErrAccountNotFound) {
		return false
	}
	t.Fatalf("GetAccount(%q): %v", accountID, err)
	return false
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

//nolint:revive // test helper takes *testing.T as first argument
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

func waitBillingHostLoopCall(t *testing.T, store *billingstore.DurableStore, accountID string) (billing.CallUsageRecord, billing.CallExposure, billing.CompleteCall) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := store.ListCallUsage(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 {
			exposure, exposureErr := store.GetCallExposure(ctx, records[0].CallID)
			if exposureErr != nil {
				t.Fatal(exposureErr)
			}
			if !exposure.IsOpen() {
				complete, completeErr := store.ClaimCompleteCall(ctx, records[0].CallID)
				if completeErr != nil {
					t.Fatal(completeErr)
				}
				return records[0], exposure, complete
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for settled call: records=%+v", records)
		}
		<-ticker.C
	}
}

func waitBillingHostLoopProviderCost(t *testing.T, store *billingstore.DurableStore, accountID string) billing.AccountReport {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		report, err := store.AccountReport(ctx, accountID, billing.PageRequest{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, transaction := range report.Transactions {
			if transaction.OperationKind == "provider_call_cogs" {
				return report
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for provider COGS: %+v", report.Transactions)
		}
		<-ticker.C
	}
}

func waitBillingHostLoopCallRecords(t *testing.T, store *billingstore.DurableStore, accountID string) []billing.CallUsageRecord {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := store.ListCallUsage(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 {
			return records
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for call closure: records=%+v", records)
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
  max_attempts: 3
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
    - id: bad
      kind: openai-responses
      enabled: false
      config: {}
    - id: good
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
