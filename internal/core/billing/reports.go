package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrReportInvalid  = errors.New("billing: invalid report query")
	ErrReportNotFound = errors.New("billing: report subject not found")
)

// ReportingStore is the read-side contract used by control-plane/reporting
// callers. Implementations return only domain values, never Bun/database types.
type ReportingStore interface {
	AccountReport(context.Context, string, PageRequest) (AccountReport, error)
	TurnExplanation(context.Context, string) (TurnExplanation, error)
	OperatorCostReport(context.Context, ReportFilter) (OperatorCostReport, error)
	TrialBalanceReport(context.Context, ReportFilter) (TrialBalanceReport, error)
	SessionReport(context.Context, string, string, PageRequest) (SessionReport, error)
	QueryProcessing(context.Context, ReportFilter) (ProcessingPage, error)
	QueryOpenHolds(context.Context, string, PageRequest) (HoldPage, error)
	QueryReconcileRequired(context.Context, PageRequest) (AccountStatePage, error)
}

// AuthoritativeBilling is the Phase 7 cutover boundary: monetary settlement
// and all billing reports share one durable implementation. Legacy telemetry
// stores may remain elsewhere, but cannot satisfy this financial contract.
type AuthoritativeBilling interface {
	SettlementStore
	ProcessingStore
	ReportingStore
}

// PageRequest is the bounded read-side pagination contract. AfterSequence is an
// opaque-to-callers journal cursor: zero starts at the beginning.
type PageRequest struct {
	AfterSequence uint64
	AfterKey      string
	Limit         int
}

func (p PageRequest) normalized() (PageRequest, error) {
	if p.Limit == 0 {
		p.Limit = 100
	}
	if p.Limit < 1 || p.Limit > 1000 {
		return PageRequest{}, fmt.Errorf("%w: page limit must be between 1 and 1000", ErrReportInvalid)
	}
	return p, nil
}

// ReportFilter bounds journal-backed report reads. The date interval is
// half-open: From <= recorded_at < To when either endpoint is present.
type ReportFilter struct {
	AccountID string
	Currency  string
	Book      JournalBook
	Status    ProcessingStatus
	AfterKey  string
	From      time.Time
	To        time.Time
	Page      PageRequest
}

// Normalize validates and applies the default page bound.
func (p PageRequest) Normalize() (PageRequest, error) { return p.normalized() }

func (f ReportFilter) normalized() (ReportFilter, error) {
	f.AccountID = strings.TrimSpace(f.AccountID)
	f.Currency = strings.TrimSpace(f.Currency)
	f.AfterKey = strings.TrimSpace(f.AfterKey)
	if f.Book != "" && f.Book != JournalBookFinancial && f.Book != JournalBookAuthorization {
		return ReportFilter{}, fmt.Errorf("%w: unsupported journal book %q", ErrReportInvalid, f.Book)
	}
	if f.Status != "" && !f.Status.Valid() {
		return ReportFilter{}, fmt.Errorf("%w: unsupported processing status %q", ErrReportInvalid, f.Status)
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return ReportFilter{}, fmt.Errorf("%w: report end precedes start", ErrReportInvalid)
	}
	page, err := f.Page.normalized()
	if err != nil {
		return ReportFilter{}, err
	}
	f.Page = page
	return f, nil
}

// Normalize validates a report filter and applies the default page bound.
func (f ReportFilter) Normalize() (ReportFilter, error) { return f.normalized() }

// SummarizeJournalForReport exposes the bounded read-side projection helper.
func SummarizeJournalForReport(transactions []JournalTransaction, currency string) (int64, int64, []ReconciliationIssue) {
	return summarizeJournal(transactions, currency)
}

// ReportMargin computes customer revenue minus provider cost using checked math.
func ReportMargin(currency string, revenue, cost int64) (Money, error) {
	return reportMoneyDifference(currency, revenue, cost)
}

// AddReportAmount performs checked arithmetic for read-side totals.
func AddReportAmount(total int64, amount Money, currency string) (int64, error) {
	return addReportAmount(total, amount, currency)
}

// ReportDifference performs checked subtraction for read-side totals.
func ReportDifference(currency string, left, right int64) (Money, error) {
	return reportMoneyDifference(currency, left, right)
}

// OperationSnapshot is safe redundant evidence for one customer-affecting
// operation. It contains no prompts, completions, credentials, or wire data.
type OperationSnapshot struct {
	OperationKey  string
	OperationKind string
	SourceKey     string
	Fingerprint   string
	Currency      string
	Mode          AccountMode
	Before        AccountSnapshot
	After         AccountSnapshot
	SequenceStart uint64
	SequenceEnd   uint64
	CreatedAt     time.Time
}

// AuthorizationReport is the redacted read-side representation of a hold.
type AuthorizationReport struct {
	ID              string
	AccountID       string
	TURKey          string
	Amount          Money
	Status          string
	PricingRef      VersionRef
	ChargePolicyRef VersionRef
	Before          AccountSnapshot
	After           AccountSnapshot
	ClosedReason    ReleaseReason
	ReleasedAmount  Money
	CreatedAt       time.Time
	ClosedAt        time.Time
}

// AccountReport is a journal-backed account view. Transactions are immutable
// copies and are returned in AccountSequence order. SpendableNano/CreditFloorNano
// are derived read-side projections so control-plane JSON does not require
// callers to reconstruct floors themselves.
type AccountReport struct {
	Account         Account
	SpendableNano   int64
	CreditFloorNano int64
	Transactions    []JournalTransaction
	NextCursor      uint64
}

// ProcessingPage is a bounded operator view of mutable post-turn work.
type ProcessingPage struct {
	Items      []UsageRecordProcessing
	NextCursor string
}

// HoldReport is a safe operator view of one authorization hold.
type HoldReport struct {
	ID             string
	AccountID      string
	TURKey         string
	Amount         Money
	Status         string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	ClosedReason   ReleaseReason
	ReleasedAmount Money
}

// HoldPage is a bounded operator view of authorization exposure.
type HoldPage struct {
	Items      []HoldReport
	NextCursor string
}

// AccountStatePage lists only materialized safety state for reconciliation
// operations; it never exposes database handles or raw payloads.
type AccountStatePage struct {
	Items      []Account
	NextCursor string
}

// TurnResultSummary links the read-side financial result without pretending that
// raw stream usage is a billing result.
type TurnResultSummary struct {
	CustomerCharge Money
	ProviderCost   Money
	GrossMargin    Money
	Processed      bool
	Status         ProcessingStatus
}

// TurnExplanation links all durable evidence needed to explain one A-leg.
type TurnExplanation struct {
	Record         TurnUsageRecord
	Processing     UsageRecordProcessing
	Authorization  AuthorizationReport
	Result         TurnResultSummary
	Transactions   []JournalTransaction
	Snapshots      []OperationSnapshot
	Reconciliation *ReconciliationReport
}

// OperatorCostRow preserves B-leg attribution for operator reporting.
type OperatorCostRow struct {
	TURKey      string
	TurnID      string
	ALegID      string
	BLegID      string
	ProviderID  string
	ModelID     string
	Transaction JournalTransaction
	Amount      Money
}

// OperatorCostReport keeps customer revenue and provider COGS as separate
// perspectives. Rows are one B-leg page; CustomerRevenue, ProviderCost, and
// GrossMargin are netted totals for the selected account/date range.
type OperatorCostReport struct {
	Rows            []OperatorCostRow
	CustomerRevenue Money
	ProviderCost    Money
	GrossMargin     Money
	NextCursor      uint64
	// NextKey is the lur_key cursor for the next B-leg page. Headline
	// CustomerRevenue/ProviderCost/GrossMargin are filter-range totals and do
	// not change across pages.
	NextKey string
	Issues  []ReconciliationIssue
}

// SessionReportRow is one A-leg settlement in a session aggregation.
type SessionReportRow struct {
	TURKey         string
	TurnID         string
	ALegID         string
	CustomerCharge Money
	ProviderCost   Money
	GrossMargin    Money
}

// SessionReport aggregates A-leg settlements that share one proxy-owned
// session identity. Headline totals cover the whole session; Rows are one
// tur_key page. There is no mutable session balance.
type SessionReport struct {
	AccountID       string
	SessionID       string
	Rows            []SessionReportRow
	CustomerRevenue Money
	ProviderCost    Money
	GrossMargin     Money
	NextKey         string
	Issues          []ReconciliationIssue
}

// TrialBalanceReport proves debit/credit equality for the selected journal
// range. Failures are safe bounded diagnostics rather than raw database errors.
type TrialBalanceTotals struct {
	Debit     Money
	Credit    Money
	Imbalance Money
	// Valid is false when bounded arithmetic or entry validation prevented
	// trustworthy per-book totals. A zero imbalance alone is not sufficient
	// evidence that an invalid/overflowed book balanced.
	Valid bool
}

type TrialBalanceReport struct {
	AccountID string
	Currency  string
	From      time.Time
	To        time.Time
	Debit     Money
	Credit    Money
	Imbalance Money
	ByBook    map[JournalBook]TrialBalanceTotals
	// PageBalanced is true when the returned journal page's debit/credit totals
	// and per-book checks succeed. It does not imply the whole account is sound.
	PageBalanced bool
	// Balanced is true only when the page is balanced, the page is the complete
	// selected range (NextCursor == 0), and account-wide Integrity.OK.
	Balanced     bool
	Transactions int
	NextCursor   uint64
	Issues       []ReconciliationIssue

	// Integrity is the account-wide, read-only diagnostic snapshot. It is kept
	// separate from Issues/Balanced because journal totals are bounded by the
	// requested page/range while materialized-state replay covers the account.
	Integrity *ReconciliationReport
}

func money(currency string, nano int64) Money { return Money{Nano: nano, Currency: currency} }

func addReportAmount(total int64, amount Money, currency string) (int64, error) {
	if amount.Currency != currency {
		return 0, ErrMoneyCurrencyMismatch
	}
	return checkedAdd(total, amount.Nano)
}

// summarizeJournal derives the financial perspectives solely from journal
// entries. Customer revenue is net credits minus debits to usage_revenue;
// provider cost is net debits minus credits to inference_provider_cogs so
// reversals and replacements do not double-count.
func summarizeJournal(transactions []JournalTransaction, currency string) (revenue, cost int64, issues []ReconciliationIssue) {
	for _, tx := range transactions {
		if tx.Currency != currency {
			issues = append(issues, ReconciliationIssue{Code: "currency_mismatch", Sequence: tx.AccountSequence, Detail: tx.ID})
			continue
		}
		for _, entry := range tx.Entries {
			switch entry.LedgerAccount {
			case "usage_revenue":
				var err error
				revenue, err = applySignedReportAmount(revenue, entry, currency, JournalCredit)
				if err != nil {
					issues = append(issues, ReconciliationIssue{Code: "revenue_overflow", Sequence: tx.AccountSequence, Detail: tx.ID})
				}
			case "inference_provider_cogs":
				var err error
				cost, err = applySignedReportAmount(cost, entry, currency, JournalDebit)
				if err != nil {
					issues = append(issues, ReconciliationIssue{Code: "cost_overflow", Sequence: tx.AccountSequence, Detail: tx.ID})
				}
			}
		}
	}
	return revenue, cost, issues
}

// applySignedReportAmount adds amount when entry.Side matches positiveSide and
// subtracts it otherwise, so a reversing posting nets out of the running total.
func applySignedReportAmount(total int64, entry JournalEntry, currency string, positiveSide JournalSide) (int64, error) {
	if entry.Side != JournalDebit && entry.Side != JournalCredit {
		return total, nil
	}
	if entry.Side == positiveSide {
		return addReportAmount(total, entry.Amount, currency)
	}
	if entry.Amount.Currency != currency {
		return 0, ErrMoneyCurrencyMismatch
	}
	return checkedSub(total, entry.Amount.Nano)
}

func reportMoneyDifference(currency string, revenue, cost int64) (Money, error) {
	margin, err := checkedSub(revenue, cost)
	if err != nil {
		return Money{}, err
	}
	return money(currency, margin), nil
}
