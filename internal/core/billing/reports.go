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

type ReportingStore interface {
	AccountReport(context.Context, string, PageRequest) (AccountReport, error)
	CallExplanation(context.Context, string) (CallExplanation, error)
	OperatorCostReport(context.Context, ReportFilter) (OperatorCostReport, error)
	TrialBalanceReport(context.Context, ReportFilter) (TrialBalanceReport, error)
	QueryOpenExposures(context.Context, string, PageRequest) (ExposurePage, error)
	QueryReconcileRequired(context.Context, PageRequest) (AccountStatePage, error)
}
type AuthoritativeBilling interface {
	CallSettlementStore
	ReportingStore
}
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

type ReportFilter struct {
	AccountID string
	Currency  string
	Book      JournalBook
	AfterKey  string
	From      time.Time
	To        time.Time
	Page      PageRequest
}

func (p PageRequest) Normalize() (PageRequest, error) { return p.normalized() }
func (f ReportFilter) normalized() (ReportFilter, error) {
	f.AccountID = strings.TrimSpace(f.AccountID)
	f.Currency = strings.TrimSpace(f.Currency)
	f.AfterKey = strings.TrimSpace(f.AfterKey)
	if f.Book != "" && f.Book != JournalBookFinancial {
		return ReportFilter{}, fmt.Errorf("%w: unsupported current journal book %q", ErrReportInvalid, f.Book)
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
func (f ReportFilter) Normalize() (ReportFilter, error) { return f.normalized() }
func SummarizeJournalForReport(transactions []JournalTransaction, currency string) (int64, int64, []ReconciliationIssue) {
	return summarizeJournal(transactions, currency)
}

func ReportMargin(currency string, revenue, cost int64) (Money, error) {
	return reportMoneyDifference(currency, revenue, cost)
}

func AddReportAmount(total int64, amount Money, currency string) (int64, error) {
	return addReportAmount(total, amount, currency)
}

func ReportDifference(currency string, left, right int64) (Money, error) {
	return reportMoneyDifference(currency, left, right)
}

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
type AccountReport struct {
	Account         Account
	SpendableNano   int64
	CreditFloorNano int64
	OpenExposure    Money
	Transactions    []JournalTransaction
	NextCursor      uint64
}
type AccountStatePage struct {
	Items      []Account
	NextCursor string
}
type TurnResultSummary struct {
	CustomerCharge Money
	ProviderCost   Money
	GrossMargin    Money
	Processed      bool
}
type ExposureReport struct {
	AccountID       string
	CallID          string
	Status          ExposureStatus
	Max             Money
	PricingRef      VersionRef
	ChargePolicyRef VersionRef
	Fingerprint     string
	CreatedAt       time.Time
	ClosedAt        time.Time
	Basis           ExposureBasis
	ALegID          string
	SessionID       string
}
type ExposurePage struct {
	Items      []ExposureReport
	NextCursor string
}
type CallExplanation struct {
	CallID                 string
	Exposure               ExposureReport
	Closure                CallUsageRecord
	Legs                   []CallLegUsageRecord
	CustomerOperations     []OperationSnapshot
	ProviderCostOperations []OperationSnapshot
	Transactions           []JournalTransaction
	Result                 TurnResultSummary
	Reconciliation         *ReconciliationReport
}
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
type OperatorCostReport struct {
	Rows              []OperatorCostRow
	CustomerRevenue   Money
	ProviderCost      Money
	GrossMargin       Money
	UnreconciledCosts int
	NextCursor        uint64
	NextKey           string
	Issues            []ReconciliationIssue
}
type TrialBalanceTotals struct {
	Debit     Money
	Credit    Money
	Imbalance Money
	Valid     bool
}
type TrialBalanceReport struct {
	AccountID    string
	Currency     string
	From         time.Time
	To           time.Time
	Debit        Money
	Credit       Money
	Imbalance    Money
	ByBook       map[JournalBook]TrialBalanceTotals
	PageBalanced bool
	Balanced     bool
	Transactions int
	NextCursor   uint64
	Issues       []ReconciliationIssue
	Integrity    *ReconciliationReport
}

func money(currency string, nano int64) Money { return Money{Nano: nano, Currency: currency} }
func addReportAmount(total int64, amount Money, currency string) (int64, error) {
	if amount.Currency != currency {
		return 0, ErrMoneyCurrencyMismatch
	}
	return checkedAdd(total, amount.Nano)
}

func summarizeJournal(transactions []JournalTransaction, currency string) (revenue, cost int64, issues []ReconciliationIssue) {
	for _, tx := range transactions {
		if tx.Book != "" && tx.Book != JournalBookFinancial {
			issues = append(issues, ReconciliationIssue{Code: "current_book_unsupported", Sequence: tx.AccountSequence, Detail: tx.ID})
			continue
		}
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
