package billingstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLitePhase7ReportsJoinJournalUsageAndSnapshots(t *testing.T) {
	runPhase7Reports(t, newSQLiteTestStore(t), "phase7-reports")
}

func TestSQLitePhase7OperatorCostPagesProviderCogsOnly(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "phase7-cogs-page"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		turn := fmt.Sprintf("cogs-turn-%d", i)
		authInput := authorizationInput(accountID, turn, "auth-"+turn, 40)
		auth, err := store.Authorize(ctx, authInput)
		if err != nil {
			t.Fatal(err)
		}
		record := testTUR(accountID)
		record.TurnID = turn
		record.ALegID = turn
		record.AuthorizationID = auth.ID
		record.Legs[0].ALegID = turn
		record.Legs[0].BLegID = fmt.Sprintf("b-%d", i)
		if err := store.AppendUsageRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
		sealed, err := record.Seal()
		if err != nil {
			t.Fatal(err)
		}
		result := billing.Result{
			TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"},
			OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 2, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}},
		}
		if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 1 || first.NextKey == "" || first.CustomerRevenue.Nano != 24 || first.ProviderCost.Nano != 6 {
		t.Fatalf("first cogs page = %+v", first)
	}
	second, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{AfterKey: first.NextKey, Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Rows) != 1 || second.Rows[0].BLegID == first.Rows[0].BLegID || second.CustomerRevenue.Nano != 24 {
		t.Fatalf("second cogs page = %+v", second)
	}
}

func TestSQLiteOperatorCostMultiLegPagesKeepRangeTotals(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "phase7-multileg-page"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(400, 0).UTC()
	record := billing.TurnUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, AccountID: accountID, TurnID: "turn", ALegID: "a-1", AuthorizationID: auth.ID,
		StartedAt: now, FinishedAt: now.Add(2 * time.Second), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
		Legs: []billing.LegUsageRecord{
			{ALegID: "a-1", BLegID: "b-fail", Seq: 1, BackendID: "backend-a", ProviderID: "provider-a", ModelID: "model-a", StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo, Evidence: billing.FinalBillingEvidence{Cost: billing.MoneyEvidence{NanoUnits: 2, Currency: "USD", Present: true}}},
			{ALegID: "a-1", BLegID: "b-win", Seq: 2, BackendID: "backend-b", ProviderID: "provider-b", ModelID: "model-b", StartedAt: now, FinishedAt: now.Add(2 * time.Second), Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes, Evidence: billing.FinalBillingEvidence{Cost: billing.MoneyEvidence{NanoUnits: 3, Currency: "USD", Present: true}}},
		},
	}
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.Result{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{
		{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 2, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
		{LURKey: sealed.Legs[1].Key, Amount: billing.Money{Nano: 3, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
	}}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
		t.Fatal(err)
	}
	first, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{AfterKey: first.NextKey, Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 1 || len(second.Rows) != 1 || first.Rows[0].BLegID == second.Rows[0].BLegID {
		t.Fatalf("pages = %+v / %+v", first.Rows, second.Rows)
	}
	if first.CustomerRevenue.Nano != 8 || second.CustomerRevenue.Nano != 8 || first.ProviderCost.Nano != 5 || second.ProviderCost.Nano != 5 {
		t.Fatalf("range totals drifted across pages first=%+v second=%+v", first, second)
	}
	if first.Rows[0].Amount.Nano+second.Rows[0].Amount.Nano != 5 {
		t.Fatalf("page row amounts = %d+%d, want 5", first.Rows[0].Amount.Nano, second.Rows[0].Amount.Nano)
	}
}

func TestSQLiteOperatorCostIncludesAuthoritativeZeroLeg(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "phase7-zero-leg"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "tur-turn", "tur-auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := testTUR(accountID)
	record.Legs[0].Evidence.Cost = billing.MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true}
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.Result{
		TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"},
		OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 0, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}},
	}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
		t.Fatal(err)
	}
	operator, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(operator.Rows) != 1 || operator.Rows[0].BLegID != sealed.Legs[0].BLegID || operator.Rows[0].Amount.Nano != 0 || operator.Rows[0].ProviderID != "provider" {
		t.Fatalf("zero-cost row = %+v", operator)
	}
	if operator.CustomerRevenue.Nano != 8 || operator.ProviderCost.Nano != 0 {
		t.Fatalf("zero-cost totals = %+v", operator)
	}
}

func TestSQLiteOperatorCostFailsClosedOnStoreError(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "phase7-closed", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: "phase7-closed", Page: billing.PageRequest{Limit: 10}}); err == nil {
		t.Fatal("closed store unexpectedly succeeded")
	}
}

func TestSQLitePhase7ReportQueriesRejectUnboundedOrInvalidRequests(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "phase7-validation", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AccountReport(ctx, "phase7-validation", billing.PageRequest{Limit: 1001}); err == nil {
		t.Fatal("oversized page unexpectedly accepted")
	}
	if _, err := store.TrialBalanceReport(ctx, billing.ReportFilter{AccountID: "phase7-validation", Book: billing.JournalBook("unsupported")}); err == nil {
		t.Fatal("unsupported book unexpectedly accepted")
	}
	if _, err := store.AccountReport(ctx, "missing-account", billing.PageRequest{Limit: 10}); !errors.Is(err, billing.ErrReportNotFound) {
		t.Fatalf("missing account = %v, want ErrReportNotFound", err)
	}
}

func TestSQLitePhase7ShadowRatingMatchesJournalOutcomes(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "phase7-shadow-journal"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "tur-turn", "tur-auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := testTUR(accountID)
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.Result{
		TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"},
		OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 2, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}},
	}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
		t.Fatal(err)
	}
	explanation, err := store.TurnExplanation(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if explanation.Result.CustomerCharge != result.CustomerCharge || explanation.Result.ProviderCost.Nano != 2 || explanation.Result.GrossMargin.Nano != 6 {
		t.Fatalf("journal explanation diverged from settled result: explanation=%+v result=%+v", explanation.Result, result)
	}
	operator, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if operator.CustomerRevenue != result.CustomerCharge || operator.ProviderCost.Nano != 2 || operator.GrossMargin.Nano != 6 {
		t.Fatalf("operator report diverged from settled result: %+v", operator)
	}
}

func TestSQLiteReportsNetReversalAndReplacement(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "phase7-correction-net"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "tur-turn", "tur-auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := testTUR(accountID)
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.Result{
		TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"},
		OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 2, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}},
	}
	settlement, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	if err := postCorrectionPair(t, store, settlement.Customer.Transaction, 5); err != nil {
		t.Fatal(err)
	}
	if err := postCorrectionPair(t, store, settlement.ProviderCosts[0].Transaction, 1); err != nil {
		t.Fatal(err)
	}
	explanation, err := store.TurnExplanation(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if explanation.Result.CustomerCharge.Nano != 5 || explanation.Result.ProviderCost.Nano != 1 || explanation.Result.GrossMargin.Nano != 4 {
		t.Fatalf("corrected explanation = %+v, want revenue 5 cost 1 margin 4", explanation.Result)
	}
	operator, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if operator.CustomerRevenue.Nano != 5 || operator.ProviderCost.Nano != 1 || operator.GrossMargin.Nano != 4 {
		t.Fatalf("corrected operator totals = %+v, want revenue 5 cost 1 margin 4", operator)
	}
}

func TestSQLiteTurnExplanationPartialWhenProcessingAndHoldMissing(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "partial-explain"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	record := testTUR(accountID)
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`DELETE FROM usage_record_processing WHERE tur_key = ?`, sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	explanation, err := store.TurnExplanation(ctx, sealed.Key)
	if err != nil {
		t.Fatalf("TurnExplanation: %v", err)
	}
	if explanation.Record.Key != sealed.Key {
		t.Fatalf("record = %+v, want key %s", explanation.Record, sealed.Key)
	}
	if explanation.Processing != (billing.UsageRecordProcessing{}) {
		t.Fatalf("invented processing = %+v", explanation.Processing)
	}
	if explanation.Authorization != (billing.AuthorizationReport{}) {
		t.Fatalf("invented hold = %+v", explanation.Authorization)
	}
	if explanation.Result.Processed || explanation.Result.Status != "" {
		t.Fatalf("invented result status = %+v", explanation.Result)
	}
}

func TestSQLiteTurnExplanationMissingTURIsNotFound(t *testing.T) {
	store := newSQLiteTestStore(t)
	_, err := store.TurnExplanation(context.Background(), "missing:tur")
	if !errors.Is(err, billing.ErrReportNotFound) {
		t.Fatalf("err = %v, want ErrReportNotFound", err)
	}
}

func TestSQLiteSessionReportAggregatesAuthoritativeSession(t *testing.T) {
	runSessionReportAggregatesAuthoritativeSession(t, newSQLiteTestStore(t), "session-report-acct")
}

func TestSQLiteSessionReportRejectsEmptySessionID(t *testing.T) {
	store := newSQLiteTestStore(t)
	_, err := store.SessionReport(context.Background(), "acct", "", billing.PageRequest{Limit: 10})
	if !errors.Is(err, billing.ErrReportInvalid) {
		t.Fatalf("err = %v, want ErrReportInvalid", err)
	}
}

func runPhase7Reports(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	authInput := authorizationInput(accountID, "tur-turn", "tur-auth", 40)
	auth, err := store.Authorize(ctx, authInput)
	if err != nil {
		t.Fatal(err)
	}
	record := testTUR(accountID)
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.Result{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 8, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 2, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}}}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
		t.Fatal(err)
	}

	accountReport, err := store.AccountReport(ctx, accountID, billing.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if accountReport.Account.BalanceNano != 92 || accountReport.Account.ReservedNano != 0 || accountReport.SpendableNano != 92 || accountReport.CreditFloorNano != 0 || len(accountReport.Transactions) != 2 || accountReport.NextCursor == 0 {
		t.Fatalf("account report = %+v", accountReport)
	}
	secondPage, err := store.AccountReport(ctx, accountID, billing.PageRequest{AfterSequence: accountReport.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Transactions) == 0 || secondPage.Transactions[0].AccountSequence <= accountReport.NextCursor {
		t.Fatalf("account report second page = %+v", secondPage)
	}

	explanation, err := store.TurnExplanation(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if explanation.Record.Key != sealed.Key || explanation.Authorization.ID != auth.ID || explanation.Processing.Status != billing.ProcessingProcessed || explanation.Result.CustomerCharge.Nano != 8 || explanation.Result.ProviderCost.Nano != 2 || explanation.Result.GrossMargin.Nano != 6 {
		t.Fatalf("turn explanation = %+v", explanation)
	}
	if len(explanation.Snapshots) < 2 || len(explanation.Transactions) < 3 {
		t.Fatalf("turn links incomplete = snapshots=%d transactions=%d", len(explanation.Snapshots), len(explanation.Transactions))
	}
	if explanation.Record.Fingerprint == "" || explanation.Record.Legs[0].Fingerprint == "" {
		t.Fatal("explanation lost sealed identities")
	}

	operator, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if len(operator.Rows) != 1 || operator.Rows[0].BLegID != sealed.Legs[0].BLegID || operator.Rows[0].ProviderID != "provider" || operator.Rows[0].ModelID != "model" || operator.ProviderCost.Nano != 2 || operator.CustomerRevenue.Nano != 8 || operator.GrossMargin.Nano != 6 {
		t.Fatalf("operator report = %+v", operator)
	}

	trial, err := store.TrialBalanceReport(ctx, billing.ReportFilter{AccountID: accountID, Page: billing.PageRequest{Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if !trial.PageBalanced || !trial.Balanced || trial.Debit.Nano != trial.Credit.Nano || trial.ByBook[billing.JournalBookFinancial].Imbalance.Nano != 0 || trial.ByBook[billing.JournalBookAuthorization].Imbalance.Nano != 0 {
		t.Fatalf("trial balance = %+v", trial)
	}
}

func runSessionReportAggregatesAuthoritativeSession(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 200, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	first := settleSessionTurn(t, store, accountID, "turn-a", "a-1", "auth-a", "sess-shared", 8, 2)
	second := settleSessionTurn(t, store, accountID, "turn-b", "a-2", "auth-b", "sess-shared", 5, 1)
	other := settleSessionTurn(t, store, accountID, "turn-c", "a-3", "auth-c", "sess-other", 3, 1)

	page1, err := store.SessionReport(ctx, accountID, "sess-shared", billing.PageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page1.AccountID != accountID || page1.SessionID != "sess-shared" {
		t.Fatalf("identity = %+v", page1)
	}
	if page1.CustomerRevenue.Nano != 13 || page1.ProviderCost.Nano != 3 || page1.GrossMargin.Nano != 10 {
		t.Fatalf("shared session totals = %+v, want revenue 13 cost 3 margin 10", page1)
	}
	if len(page1.Rows) != 1 || page1.NextKey == "" {
		t.Fatalf("first page = %+v", page1)
	}
	page2, err := store.SessionReport(ctx, accountID, "sess-shared", billing.PageRequest{Limit: 1, AfterKey: page1.NextKey})
	if err != nil {
		t.Fatal(err)
	}
	if page2.CustomerRevenue.Nano != page1.CustomerRevenue.Nano || page2.ProviderCost.Nano != page1.ProviderCost.Nano {
		t.Fatalf("headline totals changed across pages: page1=%+v page2=%+v", page1, page2)
	}
	if len(page2.Rows) != 1 || page2.Rows[0].TURKey == page1.Rows[0].TURKey {
		t.Fatalf("second page = %+v first=%+v", page2, page1)
	}
	seen := map[string]struct{}{page1.Rows[0].TURKey: {}, page2.Rows[0].TURKey: {}}
	if _, ok := seen[first.Key]; !ok {
		t.Fatalf("missing first TUR %s in %+v", first.Key, seen)
	}
	if _, ok := seen[second.Key]; !ok {
		t.Fatalf("missing second TUR %s in %+v", second.Key, seen)
	}

	otherPage, err := store.SessionReport(ctx, accountID, "sess-other", billing.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherPage.Rows) != 1 || otherPage.Rows[0].TURKey != other.Key || otherPage.CustomerRevenue.Nano != 3 {
		t.Fatalf("other session = %+v, want only %s revenue 3", otherPage, other.Key)
	}
}

func settleSessionTurn(t *testing.T, store *DurableStore, accountID, turnID, aLegID, authID, sessionID string, charge, cost int64) billing.TurnUsageRecord {
	t.Helper()
	ctx := context.Background()
	auth, err := store.Authorize(ctx, authorizationInput(accountID, turnID, authID, 40))
	if err != nil {
		t.Fatal(err)
	}
	record := testTUR(accountID)
	record.TurnID = turnID
	record.ALegID = aLegID
	record.AuthorizationID = authID
	record.SessionID = sessionID
	record.Legs[0].ALegID = aLegID
	record.Legs[0].BLegID = "b-" + turnID
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.Result{
		TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: charge, Currency: "USD"},
		OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: cost, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}},
	}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err != nil {
		t.Fatal(err)
	}
	return sealed
}

func postCorrectionPair(t *testing.T, store *DurableStore, original billing.JournalTransaction, replacementNano int64) error {
	t.Helper()
	if original.ID == "" {
		return fmt.Errorf("missing original journal")
	}
	reversal := original
	reversal.ID = original.ID + "-rev"
	reversal.SourceKey = original.SourceKey + "-rev"
	reversal.SemanticFingerprint = ""
	reversal.AccountSequence = 0
	reversal.ReversalOf = original.ID
	reversal.CorrectsTransactionID = ""
	reversal.CorrectionGroupID = ""
	reversal.Entries = swapJournalSides(original.Entries)
	if _, err := store.postJournalTransaction(context.Background(), reversal); err != nil {
		return err
	}
	replacement := original
	replacement.ID = original.ID + "-rep"
	replacement.SourceKey = original.SourceKey + "-rep"
	replacement.SemanticFingerprint = ""
	replacement.AccountSequence = 0
	replacement.ReversalOf = ""
	replacement.CorrectsTransactionID = original.ID
	replacement.CorrectionGroupID = ""
	replacement.Entries = scaleJournalAmounts(original.Entries, replacementNano)
	_, err := store.postJournalTransaction(context.Background(), replacement)
	return err
}

func swapJournalSides(entries []billing.JournalEntry) []billing.JournalEntry {
	out := append([]billing.JournalEntry(nil), entries...)
	for i := range out {
		if out[i].Side == billing.JournalDebit {
			out[i].Side = billing.JournalCredit
		} else {
			out[i].Side = billing.JournalDebit
		}
	}
	return out
}

func scaleJournalAmounts(entries []billing.JournalEntry, nano int64) []billing.JournalEntry {
	out := append([]billing.JournalEntry(nil), entries...)
	for i := range out {
		out[i].Amount.Nano = nano
	}
	return out
}
