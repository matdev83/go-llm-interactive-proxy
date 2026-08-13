package billingstore

import (
	"context"
	"errors"
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

func TestDurableStore_AccountProvisionerPort_WrapsDomainSentinels(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{
		ID: "provisioner-wrap", Currency: "USD", Mode: billing.AccountPrepaid,
		State: billing.AccountReady, Version: 1,
	}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}

	createConflict := store.CreateAccount(ctx, account)
	if !errors.Is(createConflict, ErrIdentityConflict) {
		t.Fatalf("create conflict store sentinel = %v", createConflict)
	}
	if !errors.Is(createConflict, billing.ErrAccountConflict) {
		t.Fatalf("create conflict domain sentinel = %v, want billing.ErrAccountConflict", createConflict)
	}

	_, missingFund := store.PostFunding(ctx, billing.FundingInput{
		AccountID: "missing-account", Amount: billing.Money{Nano: 1, Currency: "USD"},
		SourceKey: "bank-missing", Reason: "topup",
	})
	if !errors.Is(missingFund, ErrAccountNotFound) {
		t.Fatalf("funding missing store sentinel = %v", missingFund)
	}
	if !errors.Is(missingFund, billing.ErrAccountNotFound) {
		t.Fatalf("funding missing domain sentinel = %v, want billing.ErrAccountNotFound", missingFund)
	}

	_, missingPolicy := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{
		AccountID: "missing-account", Mode: billing.AccountPrepaid, Currency: "USD",
		CreditLimit: 0, SourceKey: "policy-missing", Reason: "none",
	})
	if !errors.Is(missingPolicy, ErrAccountNotFound) {
		t.Fatalf("policy missing store sentinel = %v", missingPolicy)
	}
	if !errors.Is(missingPolicy, billing.ErrAccountNotFound) {
		t.Fatalf("policy missing domain sentinel = %v, want billing.ErrAccountNotFound", missingPolicy)
	}

	if _, err := store.PostFunding(ctx, billing.FundingInput{
		AccountID: account.ID, Amount: billing.Money{Nano: 10, Currency: "USD"},
		SourceKey: "bank-1", Reason: "topup",
	}); err != nil {
		t.Fatal(err)
	}
	_, fundConflict := store.PostFunding(ctx, billing.FundingInput{
		AccountID: account.ID, Amount: billing.Money{Nano: 11, Currency: "USD"},
		SourceKey: "bank-1", Reason: "topup",
	})
	if !errors.Is(fundConflict, ErrOperationConflict) {
		t.Fatalf("funding conflict store sentinel = %v", fundConflict)
	}
	if !errors.Is(fundConflict, billing.ErrAccountConflict) {
		t.Fatalf("funding conflict domain sentinel = %v, want billing.ErrAccountConflict", fundConflict)
	}

	if _, err := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{
		AccountID: account.ID, Mode: billing.AccountPostpaid, Currency: "USD",
		CreditLimit: 100, SourceKey: "policy-1", Reason: "enable credit",
	}); err != nil {
		t.Fatal(err)
	}
	_, policyConflict := store.ChangeCreditPolicy(ctx, billing.CreditPolicyInput{
		AccountID: account.ID, Mode: billing.AccountPostpaid, Currency: "USD",
		CreditLimit: 101, SourceKey: "policy-1", Reason: "enable credit",
	})
	if !errors.Is(policyConflict, ErrOperationConflict) {
		t.Fatalf("policy conflict store sentinel = %v", policyConflict)
	}
	if !errors.Is(policyConflict, billing.ErrAccountConflict) {
		t.Fatalf("policy conflict domain sentinel = %v, want billing.ErrAccountConflict", policyConflict)
	}
}
