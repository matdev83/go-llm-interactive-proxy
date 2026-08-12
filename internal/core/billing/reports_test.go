package billing

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestSummarizeJournalNetsReversalsAndReplacements(t *testing.T) {
	t.Parallel()
	usd := func(nano int64) Money { return Money{Nano: nano, Currency: "USD"} }
	entry := func(account string, side JournalSide, nano int64) JournalEntry {
		return JournalEntry{LedgerAccount: account, Side: side, Amount: usd(nano)}
	}
	cases := []struct {
		name           string
		transactions   []JournalTransaction
		wantRevenue    int64
		wantCost       int64
		wantIssueCodes []string
	}{
		{
			name: "original-only",
			transactions: []JournalTransaction{
				{ID: "t1", Currency: "USD", AccountSequence: 1, Entries: []JournalEntry{
					entry("customer_financial_account", JournalDebit, 8),
					entry("usage_revenue", JournalCredit, 8),
				}},
				{ID: "c1", Currency: "USD", AccountSequence: 2, Entries: []JournalEntry{
					entry("inference_provider_cogs", JournalDebit, 2),
					entry("provider_payable_clearing", JournalCredit, 2),
				}},
			},
			wantRevenue: 8,
			wantCost:    2,
		},
		{
			name: "reversal-and-replacement",
			transactions: []JournalTransaction{
				{ID: "t1", Currency: "USD", AccountSequence: 1, Entries: []JournalEntry{
					entry("customer_financial_account", JournalDebit, 8),
					entry("usage_revenue", JournalCredit, 8),
				}},
				{ID: "t1-rev", Currency: "USD", AccountSequence: 2, ReversalOf: "t1", Entries: []JournalEntry{
					entry("customer_financial_account", JournalCredit, 8),
					entry("usage_revenue", JournalDebit, 8),
				}},
				{ID: "t1-rep", Currency: "USD", AccountSequence: 3, CorrectsTransactionID: "t1", Entries: []JournalEntry{
					entry("customer_financial_account", JournalDebit, 5),
					entry("usage_revenue", JournalCredit, 5),
				}},
				{ID: "c1", Currency: "USD", AccountSequence: 4, Entries: []JournalEntry{
					entry("inference_provider_cogs", JournalDebit, 2),
					entry("provider_payable_clearing", JournalCredit, 2),
				}},
				{ID: "c1-rev", Currency: "USD", AccountSequence: 5, ReversalOf: "c1", Entries: []JournalEntry{
					entry("inference_provider_cogs", JournalCredit, 2),
					entry("provider_payable_clearing", JournalDebit, 2),
				}},
				{ID: "c1-rep", Currency: "USD", AccountSequence: 6, CorrectsTransactionID: "c1", Entries: []JournalEntry{
					entry("inference_provider_cogs", JournalDebit, 1),
					entry("provider_payable_clearing", JournalCredit, 1),
				}},
			},
			wantRevenue: 5,
			wantCost:    1,
		},
		{
			name: "currency-mismatch-skipped",
			transactions: []JournalTransaction{
				{ID: "eur", Currency: "EUR", AccountSequence: 1, Entries: []JournalEntry{
					entry("usage_revenue", JournalCredit, 9),
				}},
			},
			wantIssueCodes: []string{"currency_mismatch"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			revenue, cost, issues := SummarizeJournalForReport(tc.transactions, "USD")
			if revenue != tc.wantRevenue || cost != tc.wantCost {
				t.Fatalf("revenue/cost = %d/%d, want %d/%d issues=%v", revenue, cost, tc.wantRevenue, tc.wantCost, issues)
			}
			if len(issues) != len(tc.wantIssueCodes) {
				t.Fatalf("issues = %+v, want codes %v", issues, tc.wantIssueCodes)
			}
			for i, code := range tc.wantIssueCodes {
				if issues[i].Code != code {
					t.Fatalf("issue[%d] = %q, want %q", i, issues[i].Code, code)
				}
			}
		})
	}
}

func TestReportArithmeticCheckedMath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		run     func() (Money, int64, error)
		want    int64
		wantErr error
	}{
		{
			name: "margin revenue minus cost",
			run: func() (Money, int64, error) {
				got, err := ReportMargin("USD", 12, 5)
				return got, got.Nano, err
			},
			want: 7,
		},
		{
			name: "difference alias matches margin",
			run: func() (Money, int64, error) {
				got, err := ReportDifference("USD", 12, 5)
				return got, got.Nano, err
			},
			want: 7,
		},
		{
			name: "add report amount",
			run: func() (Money, int64, error) {
				total, err := AddReportAmount(10, Money{Nano: 3, Currency: "USD"}, "USD")
				return Money{}, total, err
			},
			want: 13,
		},
		{
			name: "add currency mismatch",
			run: func() (Money, int64, error) {
				total, err := AddReportAmount(10, Money{Nano: 3, Currency: "EUR"}, "USD")
				return Money{}, total, err
			},
			wantErr: ErrMoneyCurrencyMismatch,
		},
		{
			name: "add overflow",
			run: func() (Money, int64, error) {
				total, err := AddReportAmount(math.MaxInt64, Money{Nano: 1, Currency: "USD"}, "USD")
				return Money{}, total, err
			},
			wantErr: ErrMoneyOverflow,
		},
		{
			name: "margin overflow",
			run: func() (Money, int64, error) {
				got, err := ReportMargin("USD", math.MinInt64, 1)
				return got, got.Nano, err
			},
			wantErr: ErrMoneyOverflow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, nano, err := tt.run()
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if nano != tt.want {
				t.Fatalf("got nano=%d money=%+v, want %d", nano, got, tt.want)
			}
		})
	}
}

func TestPageRequestAndReportFilterNormalize(t *testing.T) {
	t.Parallel()
	t.Run("default page limit is 100", func(t *testing.T) {
		t.Parallel()
		got, err := (PageRequest{}).Normalize()
		if err != nil || got.Limit != 100 {
			t.Fatalf("default page = %+v err=%v, want limit 100", got, err)
		}
	})
	for _, tt := range []struct {
		name string
		page PageRequest
	}{
		{name: "limit below one", page: PageRequest{Limit: -1}},
		{name: "limit above 1000", page: PageRequest{Limit: 1001}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tt.page.Normalize(); !errors.Is(err, ErrReportInvalid) {
				t.Fatalf("error = %v, want ErrReportInvalid", err)
			}
		})
	}

	from := time.Unix(100, 0).UTC()
	to := time.Unix(200, 0).UTC()
	ok, err := (ReportFilter{AccountID: " acct ", Currency: " USD ", Book: JournalBookFinancial, Status: ProcessingPending, From: from, To: to}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if ok.AccountID != "acct" || ok.Currency != "USD" || ok.Page.Limit != 100 {
		t.Fatalf("normalized filter = %+v", ok)
	}

	for _, tt := range []struct {
		name   string
		filter ReportFilter
	}{
		{name: "unsupported book", filter: ReportFilter{Book: "tax"}},
		{name: "unsupported status", filter: ReportFilter{Status: "not-a-status"}},
		{name: "end precedes start", filter: ReportFilter{From: to, To: from}},
		{name: "invalid page limit", filter: ReportFilter{Page: PageRequest{Limit: 1001}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tt.filter.Normalize(); !errors.Is(err, ErrReportInvalid) {
				t.Fatalf("error = %v, want ErrReportInvalid", err)
			}
		})
	}
}
