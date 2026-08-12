package billing

import (
	"errors"
	"testing"
	"time"
)

func TestTrustedCommandsRejectArbitraryOrNonPositiveOperations(t *testing.T) {
	t.Parallel()
	if err := (FundingInput{AccountID: "a", Amount: Money{Nano: 0, Currency: "USD"}, SourceKey: "s", Reason: "topup"}).Validate(); !errors.Is(err, ErrTrustedCommandInvalid) {
		t.Fatalf("zero funding = %v, want invalid", err)
	}
	if err := (AdjustmentInput{AccountID: "a", Amount: Money{Nano: 1, Currency: "USD"}, Direction: "raw", SourceKey: "s", Reason: "fix"}).Validate(); !errors.Is(err, ErrTrustedCommandInvalid) {
		t.Fatalf("arbitrary adjustment direction = %v, want invalid", err)
	}
	if _, err := FundingJournalIntent(FundingInput{}); !errors.Is(err, ErrTrustedCommandInvalid) {
		t.Fatalf("empty funding intent = %v, want invalid", err)
	}
	if err := (ReleaseAuthorizationInput{AccountID: "a", AuthorizationID: "auth", TURKey: "tur", Reason: ReleaseOperator}).Validate(); !errors.Is(err, ErrTrustedCommandInvalid) {
		t.Fatalf("zero operator release = %v, want invalid", err)
	}
}

func TestTrustedJournalIntentsUseClosedBalancedShapes(t *testing.T) {
	t.Parallel()
	amount := Money{Nano: 25, Currency: "USD"}
	funding := FundingInput{AccountID: "acct", Amount: amount, SourceKey: "bank-1", Reason: "top-up"}
	journal, err := FundingJournalIntent(funding)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Validate(); err != nil {
		t.Fatal(err)
	}
	if journal.SourceKey != ScopedOperationKey("funding", "acct", "bank-1") || journal.ID != journal.SourceKey {
		t.Fatalf("funding identity = id %q source %q, want length-prefixed account-scoped key", journal.ID, journal.SourceKey)
	}
	if journal.Entries[0].LedgerAccount != "cash_payment_clearing" || journal.Entries[1].LedgerAccount != "customer_financial_account" {
		t.Fatalf("funding accounts = %+v", journal.Entries)
	}
	fundingSource := journal.SourceKey
	adjust := AdjustmentInput{AccountID: "acct", Amount: amount, Direction: AdjustmentDebit, SourceKey: "manual-1", Reason: "correction"}
	journal, err = AdjustmentJournalIntent(adjust)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Entries[0].LedgerAccount != "customer_financial_account" || journal.Entries[1].LedgerAccount != "customer_adjustment_clearing" {
		t.Fatalf("debit adjustment accounts = %+v", journal.Entries)
	}

	payment := PaymentInput{AccountID: "acct", Amount: amount, SourceKey: "bank-1", Reason: "top-up"}
	payJournal, err := PaymentJournalIntent(payment)
	if err != nil {
		t.Fatal(err)
	}
	if err := payJournal.Validate(); err != nil {
		t.Fatal(err)
	}
	if payJournal.SourceKey != ScopedOperationKey("payment", "acct", "bank-1") || payJournal.ID != payJournal.SourceKey {
		t.Fatalf("payment identity = id %q source %q, want length-prefixed account-scoped key", payJournal.ID, payJournal.SourceKey)
	}
	if fundingSource == payJournal.SourceKey {
		t.Fatal("payment source key must differ from funding")
	}
	if payJournal.Entries[0].LedgerAccount != "cash_payment_clearing" || payJournal.Entries[1].LedgerAccount != "customer_financial_account" {
		t.Fatalf("payment accounts = %+v", payJournal.Entries)
	}
	fundingFP, err := funding.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	paymentFP, err := payment.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fundingFP == paymentFP {
		t.Fatal("payment fingerprint must differ from funding for the same amount/source")
	}

	credit := AdjustmentInput{AccountID: "acct", Amount: amount, Direction: AdjustmentCredit, SourceKey: "manual-2", Reason: "credit"}
	creditJournal, err := AdjustmentJournalIntent(credit)
	if err != nil {
		t.Fatal(err)
	}
	if creditJournal.Entries[0].LedgerAccount != "customer_adjustment_clearing" || creditJournal.Entries[1].LedgerAccount != "customer_financial_account" {
		t.Fatalf("credit adjustment accounts = %+v", creditJournal.Entries)
	}
}

func TestCreditPolicyRejectsPrepaidLimitAndRequiresReason(t *testing.T) {
	t.Parallel()
	if err := (CreditPolicyInput{AccountID: "a", Mode: AccountPrepaid, Currency: "USD", CreditLimit: 1, SourceKey: "s", Reason: "x"}).Validate(); !errors.Is(err, ErrTrustedCommandInvalid) {
		t.Fatalf("prepaid limit = %v, want invalid", err)
	}
	if err := (CreditPolicyInput{AccountID: "a", Mode: AccountPostpaid, Currency: "USD", CreditLimit: 1, SourceKey: "s"}).Validate(); !errors.Is(err, ErrTrustedCommandInvalid) {
		t.Fatalf("missing reason = %v, want invalid", err)
	}
}

func TestSafeReleaseRequiresInactiveLifetimeAndGrace(t *testing.T) {
	t.Parallel()
	created := time.Unix(100, 0).UTC()
	inactive := created.Add(2 * time.Minute)
	eligibility := SafeReleaseEligibility{AuthorizationCreated: created, AlegInactiveAt: inactive, MaximumExecutionLife: 5 * time.Minute, SafetyGrace: time.Minute, Now: created.Add(7 * time.Minute)}
	if err := eligibility.Validate(); err != nil {
		t.Fatal(err)
	}
	eligibility.Now = created.Add(5 * time.Minute)
	if !errors.Is(eligibility.Validate(), ErrReleaseNotEligible) {
		t.Fatalf("early release = %v", eligibility.Validate())
	}
	if err := (ReleaseAuthorizationInput{AccountID: "a", AuthorizationID: "auth", TURKey: "tur", Amount: Money{Nano: 1, Currency: "USD"}, Reason: ReleaseStaleSafe}).Validate(); !errors.Is(err, ErrReleaseNotEligible) {
		t.Fatalf("missing stale proof = %v", err)
	}
}

func TestProcessingStatusValidationAndConflictIdentity(t *testing.T) {
	t.Parallel()
	state := UsageRecordProcessing{TURKey: "tur", TURFingerprint: "fp", Status: ProcessingProcessing, LeaseOwner: "worker", RetryCount: 1}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	state.Status = "unknown"
	if !errors.Is(state.Validate(), ErrProcessingInvalid) {
		t.Fatalf("unknown processing state = %v", state.Validate())
	}
	if ErrProcessingConflict == nil {
		t.Fatal("conflict sentinel must exist")
	}
	for _, status := range []ProcessingStatus{ProcessingPending, ProcessingProcessing, ProcessingRetryable, ProcessingUnreconciledCost} {
		if !status.BlocksHoldRelease() {
			t.Fatalf("%s should block non-settlement hold release", status)
		}
	}
	for _, status := range []ProcessingStatus{ProcessingProcessed, ProcessingTerminalError} {
		if status.BlocksHoldRelease() {
			t.Fatalf("%s should allow hold release after terminal processing", status)
		}
	}
}

func TestScopedOperationKeyLengthPrefixesAccountAndSource(t *testing.T) {
	t.Parallel()
	left := ScopedOperationKey("funding", "a:b", "c")
	right := ScopedOperationKey("funding", "a", "b:c")
	if left == right {
		t.Fatalf("colon-ambiguous account/source pairs collided: %q", left)
	}
	if left != "funding:v1:3:a:b:1:c" {
		t.Fatalf("left key = %q, want funding:v1:3:a:b:1:c", left)
	}
	if right != "funding:v1:1:a:3:b:c" {
		t.Fatalf("right key = %q, want funding:v1:1:a:3:b:c", right)
	}
}
