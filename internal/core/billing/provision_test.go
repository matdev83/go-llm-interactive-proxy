package billing

import (
	"context"
	"testing"
)

// admissionOnlyBilling implements AuthoritativeBilling by embedding its
// constituent ports and must compile without AccountProvisioner methods.
type admissionOnlyBilling struct {
	SettlementStore
	ProcessingStore
	ReportingStore
}

var _ AuthoritativeBilling = (*admissionOnlyBilling)(nil)

func TestAccountProvisioner_CreateFundAndChangePolicyWithoutStoreImport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	provisioner := newMemoryAccountProvisioner()
	account := Account{
		ID:       "acct-port",
		Currency: "USD",
		Mode:     AccountPrepaid,
		State:    AccountReady,
		Version:  1,
	}
	funding := FundingInput{
		AccountID: account.ID,
		Amount:    Money{Nano: 50, Currency: "USD"},
		SourceKey: "bank-1",
		Reason:    "opening top-up",
	}
	policy := CreditPolicyInput{
		AccountID:   account.ID,
		Mode:        AccountPostpaid,
		Currency:    "USD",
		CreditLimit: 100,
		SourceKey:   "policy-1",
		Reason:      "enable credit",
	}

	posting, change, err := provisionAccountThroughPort(ctx, provisioner, account, funding, policy)
	if err != nil {
		t.Fatal(err)
	}
	if posting.After.BalanceNano != 50 || posting.After.Currency != "USD" || posting.After.Mode != AccountPrepaid {
		t.Fatalf("funding snapshot = %+v, want prepaid USD balance 50", posting.After)
	}
	if posting.After.SpendableNano != 50 {
		t.Fatalf("prepaid spendable after funding = %d, want 50", posting.After.SpendableNano)
	}
	if change.After.Mode != AccountPostpaid || change.After.CreditLimitNano != 100 {
		t.Fatalf("policy snapshot = %+v, want postpaid credit limit 100", change.After)
	}
	got, ok := provisioner.account(account.ID)
	if !ok {
		t.Fatal("provisioned account missing from helper")
	}
	if got.Mode != AccountPostpaid || got.Currency != "USD" || got.CreditLimit != 100 || got.BalanceNano != 50 {
		t.Fatalf("account = %+v, want postpaid USD limit 100 balance 50", got)
	}
}

func TestAuthoritativeBillingDoesNotIncludeAccountProvisioner(t *testing.T) {
	t.Parallel()
	var store AuthoritativeBilling = (*admissionOnlyBilling)(nil)
	if _, ok := store.(AccountProvisioner); ok {
		t.Fatal("AuthoritativeBilling must not require AccountProvisioner")
	}
}

func provisionAccountThroughPort(ctx context.Context, p AccountProvisioner, account Account, funding FundingInput, policy CreditPolicyInput) (Posting, PolicyChange, error) {
	if err := p.CreateAccount(ctx, account); err != nil {
		return Posting{}, PolicyChange{}, err
	}
	posting, err := p.PostFunding(ctx, funding)
	if err != nil {
		return Posting{}, PolicyChange{}, err
	}
	change, err := p.ChangeCreditPolicy(ctx, policy)
	return posting, change, err
}

type memoryAccountProvisioner struct {
	accounts map[string]Account
}

func newMemoryAccountProvisioner() *memoryAccountProvisioner {
	return &memoryAccountProvisioner{accounts: map[string]Account{}}
}

func (m *memoryAccountProvisioner) account(id string) (Account, bool) {
	got, ok := m.accounts[id]
	return got, ok
}

func (m *memoryAccountProvisioner) CreateAccount(_ context.Context, account Account) error {
	if err := account.Validate(); err != nil {
		return err
	}
	m.accounts[account.ID] = account
	return nil
}

func (m *memoryAccountProvisioner) PostFunding(_ context.Context, input FundingInput) (Posting, error) {
	if err := input.Validate(); err != nil {
		return Posting{}, err
	}
	account, ok := m.accounts[input.AccountID]
	if !ok {
		return Posting{}, ErrAccountInvalid
	}
	before, err := snapshotFromAccount(account)
	if err != nil {
		return Posting{}, err
	}
	afterAccount, err := account.ApplyBalanceDelta(input.Amount)
	if err != nil {
		return Posting{}, err
	}
	afterAccount.Version = account.Version + 1
	after, err := snapshotFromAccount(afterAccount)
	if err != nil {
		return Posting{}, err
	}
	m.accounts[input.AccountID] = afterAccount
	return Posting{Before: before, After: after}, nil
}

func (m *memoryAccountProvisioner) ChangeCreditPolicy(_ context.Context, input CreditPolicyInput) (PolicyChange, error) {
	if err := input.Validate(); err != nil {
		return PolicyChange{}, err
	}
	account, ok := m.accounts[input.AccountID]
	if !ok {
		return PolicyChange{}, ErrAccountInvalid
	}
	before, err := snapshotFromAccount(account)
	if err != nil {
		return PolicyChange{}, err
	}
	afterAccount := account
	afterAccount.Mode = input.Mode
	afterAccount.CreditLimit = input.CreditLimit
	afterAccount.Version = account.Version + 1
	after, err := snapshotFromAccount(afterAccount)
	if err != nil {
		return PolicyChange{}, err
	}
	m.accounts[input.AccountID] = afterAccount
	return PolicyChange{Before: before, After: after}, nil
}

func snapshotFromAccount(account Account) (AccountSnapshot, error) {
	spendable, err := account.SpendableNano()
	if err != nil {
		return AccountSnapshot{}, err
	}
	return AccountSnapshot{
		BalanceNano:     account.BalanceNano,
		ReservedNano:    account.ReservedNano,
		SpendableNano:   spendable,
		CreditFloorNano: account.CreditFloorNano(),
		CreditLimitNano: account.CreditLimit,
		Mode:            account.Mode,
		Currency:        account.Currency,
		Version:         account.Version,
	}, nil
}
