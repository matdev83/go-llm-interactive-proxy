package billing

import (
	"fmt"
	"math"
	"sort"
	"strconv"
)

type ReconciliationIssue struct {
	Code     string
	Sequence uint64
	Detail   string
}
type ReconciliationReport struct {
	AccountID             string
	OK                    bool
	Current               AccountSnapshot
	Rebuilt               AccountSnapshot
	FirstMismatchSequence uint64
	Issues                []ReconciliationIssue
}
type BillingReconciliationReport struct {
	Financial ReconciliationReport
	Exposure  ExposureReconciliationReport
	OK        bool
}
type LegacyHoldReconciliationReport struct {
	OpenHolds       int
	BlockedAccounts int
	Ready           bool
}

func ReplayAccount(account Account, openingBalance int64, journals []JournalTransaction) ReconciliationReport {
	report := ReconciliationReport{AccountID: account.ID}
	current, currentErr := snapshotForReplay(account)
	if currentErr != nil {
		report.addIssue("materialized_state_invalid", 0, currentErr.Error())
	} else {
		report.Current = current
	}
	if err := account.Validate(); err != nil {
		report.addIssue("account_invalid", 0, err.Error())
		return report
	}
	// Customer replay has its own ordering domain. Provider COGS rows are
	// balanced reporting facts, not customer-balance mutations; historical
	// provider rows may have a sequence, but must not enter this replay.
	ordered := make([]JournalTransaction, 0, len(journals))
	for _, journal := range journals {
		if journal.OperationKind == "provider_call_cogs" {
			continue
		}
		if journal.AccountSequence == 0 {
			// A zero sequence is not a customer-stream omission. It is malformed
			// persisted evidence and must fail closed rather than disappearing
			// from reconciliation.
			report.addIssue("account_sequence_invalid", 0, journal.ID)
			continue
		}
		ordered = append(ordered, journal)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].AccountSequence < ordered[j].AccountSequence
	})
	byID := make(map[string]JournalTransaction, len(ordered))
	for _, journal := range ordered {
		if _, exists := byID[journal.ID]; exists {
			report.addIssue("duplicate_transaction_id", journal.AccountSequence, journal.ID)
		}
		byID[journal.ID] = journal
	}
	balance := openingBalance
	reversedTargets := make(map[string]struct{}, len(ordered))
	for index, journal := range ordered {
		expectedSequence := uint64(index + 1)
		if journal.AccountSequence != expectedSequence {
			report.addIssue("account_sequence_gap", journal.AccountSequence, "got "+strconv.FormatUint(journal.AccountSequence, 10)+" want "+strconv.FormatUint(expectedSequence, 10))
		}
		if err := journal.Validate(); err != nil {
			report.addIssue("journal_invalid", journal.AccountSequence, err.Error())
			continue
		}
		scopeOK := journal.AccountID == account.ID && journal.Currency == account.Currency
		if !scopeOK {
			report.addIssue("journal_scope_mismatch", journal.AccountSequence, journal.ID)
		}
		if journal.AccountSequence == 0 {
			report.addIssue("account_sequence_invalid", 0, journal.ID)
		}
		fingerprintOK := true
		if expected, err := journal.CanonicalFingerprint(); err != nil {
			report.addIssue("journal_fingerprint_invalid", journal.AccountSequence, err.Error())
			fingerprintOK = false
		} else if journal.SemanticFingerprint != expected {
			report.addIssue("journal_fingerprint_mismatch", journal.AccountSequence, journal.ID)
			fingerprintOK = false
		}
		if scopeOK && fingerprintOK {
			for _, entry := range journal.Entries {
				var delta int64
				switch {
				case journal.Book == JournalBookFinancial && entry.LedgerAccount == "customer_financial_account":
					if entry.Side == JournalCredit {
						delta = entry.Amount.Nano
					} else {
						delta = -entry.Amount.Nano
					}
					var err error
					balance, err = signedAdd(balance, delta)
					if err != nil {
						report.addIssue("balance_overflow", journal.AccountSequence, err.Error())
					}
				}
			}
		}
		if journal.ReversalOf != "" {
			reversedTargets[journal.ReversalOf] = struct{}{}
		}
		if journal.ReversalOf != "" || journal.CorrectsTransactionID != "" {
			targetID := journal.ReversalOf
			if targetID == "" {
				targetID = journal.CorrectsTransactionID
			}
			target, ok := byID[targetID]
			if !ok {
				report.addIssue("correction_target_missing", journal.AccountSequence, targetID)
			} else if target.ID == journal.ID || target.AccountID != journal.AccountID || target.Book != journal.Book || target.Currency != journal.Currency {
				report.addIssue("correction_scope_invalid", journal.AccountSequence, targetID)
			} else {
				targetGroup := target.CorrectionGroupID
				if targetGroup == "" {
					targetGroup = target.ID
				}
				if journal.CorrectionGroupID == "" || journal.CorrectionGroupID != targetGroup {
					report.addIssue("correction_group_invalid", journal.AccountSequence, targetID)
				}
			}
			if journal.CorrectsTransactionID != "" && journal.ReversalOf == "" {
				if _, ok := reversedTargets[journal.CorrectsTransactionID]; !ok {
					report.addIssue("replacement_without_reversal", journal.AccountSequence, journal.CorrectsTransactionID)
				}
			}
		}
	}
	floor := account.CreditFloorNano()
	spendable, err := signedAdd(balance, -floor)
	if err != nil {
		report.addIssue("spendable_overflow", 0, err.Error())
	} else {
		report.Rebuilt = AccountSnapshot{BalanceNano: balance, SpendableNano: spendable, CreditFloorNano: floor, CreditLimitNano: account.CreditLimit, Mode: account.Mode, Currency: account.Currency, Version: account.Version}
	}
	if balance < floor {
		report.addIssue("balance_below_floor", 0, "balance="+strconv.FormatInt(balance, 10)+" floor="+strconv.FormatInt(floor, 10))
	}
	report.OK = len(report.Issues) == 0
	return report
}

func snapshotForReplay(account Account) (AccountSnapshot, error) {
	spendable, err := account.SpendableNano()
	if err != nil {
		return AccountSnapshot{}, err
	}
	return AccountSnapshot{BalanceNano: account.BalanceNano, SpendableNano: spendable, CreditFloorNano: account.CreditFloorNano(), CreditLimitNano: account.CreditLimit, Mode: account.Mode, Currency: account.Currency, Version: account.Version}, nil
}

func (r *ReconciliationReport) addIssue(code string, sequence uint64, detail string) {
	r.AddIssue(code, sequence, detail)
}

func (r *ReconciliationReport) AddIssue(code string, sequence uint64, detail string) {
	r.Issues = append(r.Issues, ReconciliationIssue{Code: code, Sequence: sequence, Detail: detail})
	r.OK = false
	if sequence != 0 && (r.FirstMismatchSequence == 0 || sequence < r.FirstMismatchSequence) {
		r.FirstMismatchSequence = sequence
	}
}

func signedAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("signed arithmetic overflow")
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, fmt.Errorf("signed arithmetic underflow")
	}
	return a + b, nil
}
