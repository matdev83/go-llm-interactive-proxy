package billingstore

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// memoryExposureLedger is a test double for task 0.2. It serializes admit,
// settle, and credit adjustments through one account lock, stores immutable
// exposure rows, and never posts journals or mutates reserved_nano on admit.
// Open exposure is always recomputed by summing open rows (no aggregate counter).
type memoryExposureLedger struct {
	mu        sync.Mutex
	account   billing.Account
	exposures []billing.CallExposure
	journals  []billing.JournalTransaction
}

func newMemoryExposureLedger(account billing.Account) *memoryExposureLedger {
	return &memoryExposureLedger{account: account, exposures: []billing.CallExposure{}, journals: []billing.JournalTransaction{}}
}

func (m *memoryExposureLedger) Admit(in billing.AdmitExposureInput) (billing.CallExposure, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := append([]billing.CallExposure(nil), m.exposures...)
	got, err := billing.EvaluateAdmit(m.account, snapshot, in)
	if err != nil {
		return billing.CallExposure{}, err
	}
	for i, existing := range m.exposures {
		if existing.CallID == in.CallID {
			m.exposures[i] = got
			return got, nil
		}
	}
	m.exposures = append(m.exposures, got)
	return got, nil
}

func (m *memoryExposureLedger) Settle(in billing.SettleExposureInput) (billing.SettleExposureResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := append([]billing.CallExposure(nil), m.exposures...)
	result, err := billing.EvaluateSettle(m.account, snapshot, in)
	if err != nil {
		return billing.SettleExposureResult{}, err
	}
	m.account = result.Account
	replaced := false
	for i, existing := range m.exposures {
		if existing.CallID == in.CallID {
			m.exposures[i] = result.Exposure
			replaced = true
			break
		}
	}
	if !replaced {
		return billing.SettleExposureResult{}, fmt.Errorf("%w: call %q missing after settle", billing.ErrExposureNotFound, in.CallID)
	}
	return result, nil
}

func (m *memoryExposureLedger) ApplyCredit(delta billing.Money) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	updated, err := m.account.ApplyBalanceDelta(delta)
	if err != nil {
		return err
	}
	m.account = updated
	return nil
}

func (m *memoryExposureLedger) snapshot() (billing.Account, []billing.CallExposure, []billing.JournalTransaction) {
	m.mu.Lock()
	defer m.mu.Unlock()
	exps := append([]billing.CallExposure(nil), m.exposures...)
	journals := append([]billing.JournalTransaction(nil), m.journals...)
	return m.account, exps, journals
}

func (m *memoryExposureLedger) openSum(t *testing.T) billing.Money {
	t.Helper()
	_, exposures, _ := m.snapshot()
	got, err := billing.OpenExposure(m.account.Currency, exposures)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func ledgerAdmit(callID string, max int64) billing.AdmitExposureInput {
	return billing.AdmitExposureInput{
		AccountID:       "acct",
		CallID:          callID,
		Max:             billing.Money{Nano: max, Currency: "USD"},
		PricingRef:      billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
		Now:             time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
	}
}

func TestMemoryExposureAdmitDoesNotMutateBalanceJournalOrReserved(t *testing.T) {
	t.Parallel()
	account := billing.Account{
		ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady,
		BalanceNano: 100, ReservedNano: 0, Version: 3,
	}
	ledger := newMemoryExposureLedger(account)
	got, err := ledger.Admit(ledgerAdmit("call-1", 40))
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsOpen() || got.Max.Nano != 40 {
		t.Fatalf("admitted = %+v", got)
	}
	after, exposures, journals := ledger.snapshot()
	if after.BalanceNano != 100 || after.ReservedNano != 0 || after.Version != 3 {
		t.Fatalf("admit mutated financial account: %+v", after)
	}
	if len(journals) != 0 {
		t.Fatalf("admit posted journal entries: %+v", journals)
	}
	for _, journal := range journals {
		if journal.Book == billing.JournalBookLegacyAuthorization {
			t.Fatal("admit must not require an authorization-book journal")
		}
	}
	if len(exposures) != 1 || exposures[0].Max.Nano != 40 {
		t.Fatalf("exposures = %+v", exposures)
	}
	if ledger.openSum(t).Nano != 40 {
		t.Fatalf("open exposure must be summed from rows, got %d", ledger.openSum(t).Nano)
	}
}

func TestMemoryExposureAdmitIgnoresReservedNanoForSafetyMargin(t *testing.T) {
	t.Parallel()
	account := billing.Account{
		ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReconcileRequired,
		BalanceNano: 50, ReservedNano: 999, Version: 1,
	}
	margin, err := billing.SafetyMargin(account, nil)
	if err != nil {
		t.Fatalf("SafetyMargin path must ignore reserved_nano: %v", err)
	}
	if margin.Nano != 50 {
		t.Fatalf("SafetyMargin = %d, want 50", margin.Nano)
	}
	ready := account
	ready.State = billing.AccountReady
	if _, err := billing.EvaluateAdmit(ready, nil, ledgerAdmit("call-1", 50)); !errors.Is(err, billing.ErrAccountInvalid) {
		t.Fatalf("ready+reserved admit = %v, want ErrAccountInvalid", err)
	}
}

func TestMemoryExposureCloseDoesNotPostAuthorizationJournal(t *testing.T) {
	t.Parallel()
	account := billing.Account{
		ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady,
		BalanceNano: 100, Version: 1,
	}
	ledger := newMemoryExposureLedger(account)
	if _, err := ledger.Admit(ledgerAdmit("call-1", 40)); err != nil {
		t.Fatal(err)
	}
	result, err := ledger.Settle(billing.SettleExposureInput{
		CallID: "call-1", Actual: billing.Money{Nano: 0, Currency: "USD"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Exposure.IsOpen() || result.Exposure.Max.Nano != 40 {
		t.Fatalf("closed exposure = %+v", result.Exposure)
	}
	after, exposures, journals := ledger.snapshot()
	if after.BalanceNano != 100 {
		t.Fatalf("zero-actual close mutated balance: %+v", after)
	}
	if len(journals) != 0 {
		t.Fatalf("closing exposure posted journals: %+v", journals)
	}
	if ledger.openSum(t).Nano != 0 {
		t.Fatalf("closed row still counted in open sum: %+v", exposures)
	}
}

func TestMemoryExposureRebuildsBalanceWithoutConsultingOpenRows(t *testing.T) {
	t.Parallel()
	account := billing.Account{
		ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady,
		BalanceNano: 70, Version: 1,
	}
	ledger := newMemoryExposureLedger(account)
	if _, err := ledger.Admit(ledgerAdmit("call-1", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ledgerAdmit("call-2", 30)); err != nil {
		t.Fatal(err)
	}
	after, exposures, journals := ledger.snapshot()
	headroom, err := billing.SettledHeadroom(after)
	if err != nil {
		t.Fatal(err)
	}
	if headroom.Nano != 70 {
		t.Fatalf("settled headroom consulted exposure: %d", headroom.Nano)
	}
	open, err := billing.OpenExposure("USD", exposures)
	if err != nil {
		t.Fatal(err)
	}
	if open.Nano != 70 {
		t.Fatalf("open exposure = %d, want 70 from rows", open.Nano)
	}
	margin, err := billing.SafetyMargin(after, exposures)
	if err != nil {
		t.Fatal(err)
	}
	if margin.Nano != 0 {
		t.Fatalf("SafetyMargin = %d, want 0", margin.Nano)
	}
	if len(journals) != 0 {
		t.Fatalf("financial rebuild input must stay journal-only; got %+v", journals)
	}
	rebuilt := int64(70)
	for _, journal := range journals {
		if journal.Book != billing.JournalBookFinancial {
			continue
		}
		t.Fatalf("unexpected financial journal during exposure-only admit: %+v", journal)
	}
	if rebuilt != after.BalanceNano {
		t.Fatalf("journal-only rebuild %d != materialized balance %d", rebuilt, after.BalanceNano)
	}
}

func TestMemoryExposureSerializedInterleavingsKeepBalanceAtOrAboveFloor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		acct  billing.Account
		floor int64
	}{
		{
			name: "prepaid",
			acct: billing.Account{
				ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady,
				BalanceNano: 90, Version: 1,
			},
			floor: 0,
		},
		{
			name: "postpaid",
			acct: billing.Account{
				ID: "acct", Currency: "USD", Mode: billing.AccountPostpaid, State: billing.AccountReady,
				CreditLimit: 60, BalanceNano: -10, Version: 1,
			},
			floor: -60,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ledger := newMemoryExposureLedger(tt.acct)
			const workers = 12
			var wg sync.WaitGroup
			wg.Add(workers)
			for i := 0; i < workers; i++ {
				i := i
				go func() {
					defer wg.Done()
					callID := fmt.Sprintf("call-%d", i)
					max := int64(15)
					actual := int64(i % 16)
					if actual > max {
						actual = max
					}
					if _, err := ledger.Admit(ledgerAdmit(callID, max)); err != nil {
						return
					}
					_, _ = ledger.Settle(billing.SettleExposureInput{
						CallID: callID, Actual: billing.Money{Nano: actual, Currency: "USD"},
					})
				}()
			}
			wg.Wait()
			after, exposures, journals := ledger.snapshot()
			if after.BalanceNano < tt.floor {
				t.Fatalf("balance %d below credit floor %d", after.BalanceNano, tt.floor)
			}
			if len(journals) != 0 {
				t.Fatalf("serialized admit/settle posted journals: %+v", journals)
			}
			margin, err := billing.SafetyMargin(after, exposures)
			if err != nil {
				t.Fatal(err)
			}
			if margin.Nano < 0 {
				t.Fatalf("SafetyMargin %d is negative", margin.Nano)
			}
			for _, e := range exposures {
				if e.IsOpen() && e.Max.Nano != 15 {
					t.Fatalf("open exposure amount mutated: %+v", e)
				}
			}
		})
	}
}

func TestMemoryExposureTopUpSharesAccountLockWithAdmit(t *testing.T) {
	t.Parallel()
	ledger := newMemoryExposureLedger(billing.Account{
		ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady,
		BalanceNano: 10, Version: 1,
	})
	if _, err := ledger.Admit(ledgerAdmit("too-big", 40)); !errors.Is(err, billing.ErrExposureInsufficient) {
		t.Fatalf("expected insufficient before top-up, got %v", err)
	}
	if err := ledger.ApplyCredit(billing.Money{Nano: 30, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(ledgerAdmit("after-topup", 40)); err != nil {
		t.Fatalf("admit after serialized top-up: %v", err)
	}
	after, _, journals := ledger.snapshot()
	if after.BalanceNano != 40 {
		t.Fatalf("balance after top-up+admit = %d, want 40", after.BalanceNano)
	}
	if len(journals) != 0 {
		t.Fatalf("top-up helper in this double must not invent journals: %+v", journals)
	}
}

func TestMemoryExposureReplayDoesNotDuplicateRowsOrMoney(t *testing.T) {
	t.Parallel()
	ledger := newMemoryExposureLedger(billing.Account{
		ID: "acct", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady,
		BalanceNano: 100, Version: 1,
	})
	first, err := ledger.Admit(ledgerAdmit("call-1", 25))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ledger.Admit(ledgerAdmit("call-1", 25))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Fingerprint != first.Fingerprint {
		t.Fatalf("replay fingerprint %q != %q", replay.Fingerprint, first.Fingerprint)
	}
	if _, err := ledger.Admit(ledgerAdmit("call-1", 26)); !errors.Is(err, billing.ErrExposureConflict) {
		t.Fatalf("conflict = %v, want ErrExposureConflict", err)
	}
	after, exposures, journals := ledger.snapshot()
	if after.BalanceNano != 100 || len(exposures) != 1 || len(journals) != 0 {
		t.Fatalf("replay mutated money or rows: account=%+v exposures=%+v journals=%+v", after, exposures, journals)
	}
}
