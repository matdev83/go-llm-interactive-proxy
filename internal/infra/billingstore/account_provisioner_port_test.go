package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestDurableStore_AccountProvisionerPort(t *testing.T) {
	store := newSQLiteTestStore(t)
	var provisioner billing.AccountProvisioner = store
	ctx := context.Background()

	account := billing.Account{
		ID:       "provisioner-port",
		Currency: "USD",
		Mode:     billing.AccountPrepaid,
		State:    billing.AccountReady,
		Version:  1,
	}
	if err := provisioner.CreateAccount(ctx, account); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	posting, err := provisioner.PostFunding(ctx, billing.FundingInput{
		AccountID: account.ID,
		Amount:    billing.Money{Nano: 50, Currency: "USD"},
		SourceKey: "bank-1",
		Reason:    "opening top-up",
	})
	if err != nil {
		t.Fatalf("PostFunding: %v", err)
	}
	if posting.After.BalanceNano != 50 || posting.After.SpendableNano != 50 || posting.After.Mode != billing.AccountPrepaid {
		t.Fatalf("funding snapshot = %+v, want prepaid spendable 50", posting.After)
	}

	change, err := provisioner.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{
		AccountID:   account.ID,
		Mode:        billing.AccountPostpaid,
		Currency:    "USD",
		CreditLimit: 100,
		SourceKey:   "policy-1",
		Reason:      "enable credit",
	})
	if err != nil {
		t.Fatalf("ChangeCreditPolicy: %v", err)
	}
	if change.After.Mode != billing.AccountPostpaid || change.After.CreditLimitNano != 100 {
		t.Fatalf("policy snapshot = %+v, want postpaid credit limit 100", change.After)
	}

	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.Mode != billing.AccountPostpaid || got.Currency != "USD" || got.CreditLimit != 100 || got.BalanceNano != 50 {
		t.Fatalf("account = %+v, want postpaid USD limit 100 balance 50", got)
	}
	spendable, err := got.SpendableNano()
	if err != nil {
		t.Fatalf("SpendableNano: %v", err)
	}
	if spendable != 150 {
		t.Fatalf("spendable = %d, want 150 (balance 50 minus postpaid floor -100)", spendable)
	}
}
