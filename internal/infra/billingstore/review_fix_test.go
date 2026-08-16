package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestIsUniqueViolationClassifiesDialectCodesNotConstraintText(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "unique-classify", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	_, uniqueErr := store.db.NewRaw(`INSERT INTO billing_accounts(account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, reserved_nano, version, state, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"unique-classify", "USD", string(billing.AccountPrepaid), int64(0), int64(10), int64(10), int64(0), uint64(1), string(billing.AccountReady), "now", "now").Exec(ctx)
	if uniqueErr == nil {
		t.Fatal("expected unique violation")
	}
	if !isUniqueViolation(uniqueErr) {
		t.Fatalf("unique violation not classified: %v", uniqueErr)
	}

	_, checkErr := store.db.NewRaw(`UPDATE billing_accounts SET reserved_nano = -1 WHERE account_id = ?`, "unique-classify").Exec(ctx)
	if checkErr == nil {
		t.Fatal("expected check constraint violation")
	}
	if isUniqueViolation(checkErr) {
		t.Fatalf("CHECK failure misclassified as unique: %v", checkErr)
	}
	if isUniqueViolation(errors.New("constraint failed: generic")) {
		t.Fatal("plain constraint text must not match")
	}
	if isUniqueViolation(nil) {
		t.Fatal("nil must not match")
	}
}

func TestSQLiteReadIntegrityReportMissingOpeningReturnsIssue(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	now := "2026-01-01T00:00:00Z"
	if _, err := store.db.NewRaw(`INSERT INTO billing_accounts(account_id, currency, mode, credit_limit_nano, balance_nano, opening_balance_nano, reserved_nano, version, state, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"report-missing-opening", "USD", string(billing.AccountPrepaid), int64(0), int64(50), int64(50), int64(0), uint64(1), string(billing.AccountReady), now, now).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := store.readIntegrityReport(ctx, "report-missing-opening")
	if err != nil {
		t.Fatalf("readIntegrityReport: %v", err)
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Code == "opening_evidence_missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected opening_evidence_missing, got %+v", report)
	}
	account, err := store.GetAccount(ctx, "report-missing-opening")
	if err != nil {
		t.Fatal(err)
	}
	if account.State != billing.AccountReady {
		t.Fatalf("read-only integrity must not mutate state: %+v", account)
	}
}
