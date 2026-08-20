package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLitePhase7TrialBalanceReportsMaterializedMismatchWithoutRepairing(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "report-integrity"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PostFunding(ctx, billing.FundingInput{AccountID: accountID, Amount: billing.Money{Nano: 5, Currency: "USD"}, SourceKey: "fund", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE billing_accounts SET balance_nano = 999 WHERE account_id = ?`, accountID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := store.TrialBalanceReport(ctx, billing.ReportFilter{AccountID: accountID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	if report.Integrity != nil {
		for _, issue := range report.Integrity.Issues {
			if issue.Code == "materialized_state_mismatch" {
				found = true
			}
		}
	}
	if !found || report.Integrity == nil || report.Integrity.OK || !report.PageBalanced || report.Balanced {
		t.Fatalf("integrity report = %+v integrity=%+v", report, report.Integrity)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 999 {
		t.Fatalf("read-only report repaired account: %+v", account)
	}
}
