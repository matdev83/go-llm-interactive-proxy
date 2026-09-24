package billing

import (
	"math/big"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.3B RED contract: pure low-cardinality economics health summary.
// Queue names, statuses and retry reasons map through explicit allowlists
// with unknown values collapsing to "other"; discrepancy math stays exact
// per native currency with no FX; ages never go negative and empty input
// yields zeros rather than panics.

func healthTestTime() time.Time { return time.Unix(1_700_050_000, 0).UTC() }

func TestSummarizeQueuesBucketsAndAges(t *testing.T) {
	t.Parallel()
	now := healthTestTime()
	got, err := SummarizeEconomicHealth(EconomicHealthInput{
		Queues: []EconomicQueueRow{
			{Queue: "customer", Status: "pending", Count: 3, OldestCreatedUnix: now.Add(-90 * time.Second).Unix(), MaxAttempts: 2},
			{Queue: "customer", Status: "failed", Count: 1, OldestCreatedUnix: now.Add(-10 * time.Second).Unix(), MaxAttempts: 5},
			{Queue: "provider", Status: "processing", Count: 2, OldestCreatedUnix: now.Add(-30 * time.Second).Unix(), MaxAttempts: 1},
			{Queue: "hostile-queue-bc_1", Status: "pending", Count: 7, OldestCreatedUnix: now.Add(-5 * time.Second).Unix()},
		},
	}, now)
	requireNoError(t, err)
	byQueue := map[string]EconomicQueueHealth{}
	for _, queue := range got.Queues {
		byQueue[queue.Queue] = queue
	}
	customer := byQueue["customer"]
	if customer.Pending != 3 || customer.Failed != 1 {
		t.Fatalf("customer = %+v, want pending=3 failed=1", customer)
	}
	if customer.OldestAgeSec < 89 || customer.OldestAgeSec > 91 {
		t.Fatalf("customer oldest age = %v, want ~90s", customer.OldestAgeSec)
	}
	if customer.MaxAttempts != 5 {
		t.Fatalf("customer max attempts = %d, want 5", customer.MaxAttempts)
	}
	if _, ok := byQueue["hostile-queue-bc_1"]; ok {
		t.Fatal("hostile queue identity must collapse to other")
	}
	other := byQueue["other"]
	if other.Pending != 7 {
		t.Fatalf("other = %+v, want pending=7", other)
	}
	if len(got.Queues) != 3 {
		t.Fatalf("queues = %d, want 3 bounded buckets", len(got.Queues))
	}
}

func TestSummarizeRetryReasonsMapUnknownToOther(t *testing.T) {
	t.Parallel()
	got, err := SummarizeEconomicHealth(EconomicHealthInput{
		Retries: []EconomicRetryRow{
			{Queue: "customer", Reason: string(EconomicWorkReasonTransientFailure), Count: 2, Attempts: 2},
			{Queue: "customer", Reason: "weird hostile reason with bc_1", Count: 4, Attempts: 3},
			{Queue: "customer", Reason: "", Count: 9, Attempts: 0},
			{Queue: "customer", Reason: "", Count: 1, Attempts: 1},
		},
	}, healthTestTime())
	requireNoError(t, err)
	if len(got.Queues) != 1 {
		t.Fatalf("queues = %+v, want one customer bucket", got.Queues)
	}
	reasons := map[string]int{}
	for _, retry := range got.Queues[0].Retries {
		reasons[retry.Reason] = retry.Count
	}
	if reasons[string(EconomicWorkReasonTransientFailure)] != 2 {
		t.Fatalf("reasons = %+v, want transient_failure=2", reasons)
	}
	if reasons["other"] != 4 {
		t.Fatalf("reasons = %+v, want hostile reason mapped to other=4", reasons)
	}
	if reasons["unclassified"] != 1 {
		t.Fatalf("reasons = %+v, want empty reason with attempts mapped to unclassified=1", reasons)
	}
	if len(reasons) != 3 {
		t.Fatalf("reasons = %+v, want exactly 3 bounded buckets", reasons)
	}
}

func TestSummarizeDiscrepancyExactMultiCurrency(t *testing.T) {
	t.Parallel()
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store", BLegID: "b-1"}
	exact := func(currency, amount string) MonetaryExactAmount {
		t.Helper()
		decimal, err := metering.ParseDecimal(amount)
		if err != nil {
			t.Fatal(err)
		}
		rat, err := decimal.ToRat()
		if err != nil {
			t.Fatal(err)
		}
		value, err := newMonetaryExactAmount(currency, rat)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	term := func(value MonetaryExactAmount) MonetaryDiscrepancyTerm {
		amount := value
		return MonetaryDiscrepancyTerm{Status: MonetaryTermComplete, Amount: &amount}
	}
	got, err := SummarizeEconomicHealth(EconomicHealthInput{
		Retentions: []ReconciliationRetentionResult{
			{
				SchemaVersion: ReconciliationRetentionSchemaVersionV1, ID: "r1", ResultRevision: 1,
				Subject: subject, Policy: VersionRef{ID: "p", Version: "v1"}, CreatedAt: healthTestTime(),
				Quantity: &ComponentQuantityComparison{Status: ReconciliationStatusDiscrepant, Complete: true},
				Monetary: &MonetaryDiscrepancyComparison{
					Status: MonetaryDiscrepancyComplete, Subject: subject,
					Rows: []MonetaryDiscrepancyRow{
						{
							Currency:              "USD",
							MeteringCostEffect:    term(exact("USD", "0.10")),
							ReportedPriceResidual: term(exact("USD", "0.22")),
							EndToEndCostDelta:     term(exact("USD", "0.32")),
						},
						{
							Currency:              "EUR",
							MeteringCostEffect:    term(exact("EUR", "1.00")),
							ReportedPriceResidual: term(exact("EUR", "0.50")),
							EndToEndCostDelta:     term(exact("EUR", "1.50")),
						},
					},
				},
			},
			{
				SchemaVersion: ReconciliationRetentionSchemaVersionV1, ID: "r2", ResultRevision: 1,
				Subject: subject, Policy: VersionRef{ID: "p", Version: "v1"}, CreatedAt: healthTestTime(),
				Quantity: &ComponentQuantityComparison{Status: ReconciliationStatusIncomparable, Reason: ReconciliationReasonTokenizerMismatch},
				Monetary: &MonetaryDiscrepancyComparison{
					Status: MonetaryDiscrepancyIncomparable, Reason: MonetaryReasonQuantityEvidenceIncomparable, Subject: subject,
					Rows: []MonetaryDiscrepancyRow{{
						Currency:              "EUR",
						MeteringCostEffect:    MonetaryDiscrepancyTerm{Status: MonetaryTermMissing, Reason: MonetaryReasonMissingQ},
						ReportedPriceResidual: MonetaryDiscrepancyTerm{Status: MonetaryTermIncomparable, Reason: MonetaryReasonContextMismatch},
						EndToEndCostDelta:     MonetaryDiscrepancyTerm{Status: MonetaryTermIncomparable, Reason: MonetaryReasonContextMismatch},
					}},
				},
			},
		},
	}, healthTestTime())
	requireNoError(t, err)
	if got.WindowRows != 2 {
		t.Fatalf("window rows = %d, want 2", got.WindowRows)
	}
	byQty := map[string]int{}
	for _, row := range got.Discrepancies.ByQuantity {
		byQty[row.Status] = row.Count
	}
	if byQty["discrepant"] != 1 || byQty["incomparable"] != 1 {
		t.Fatalf("quantity buckets = %+v", byQty)
	}
	if got.Discrepancies.Incomparable != 2 {
		t.Fatalf("incomparable = %d, want quantity + monetary rows", got.Discrepancies.Incomparable)
	}
	gross := map[string]*big.Rat{}
	for _, total := range got.Discrepancies.Gross {
		rat, ok := new(big.Rat).SetString(total.Amount)
		if !ok {
			t.Fatalf("gross %q amount %q is not exact", total.Currency, total.Amount)
		}
		gross[total.Currency] = rat
	}
	// Exact per-currency sums with no float drift and no FX mixing:
	// USD |0.10|+|0.22| = 0.32, EUR |1.00|+|0.50| = 1.50.
	if gross["USD"] == nil || gross["USD"].Cmp(big.NewRat(8, 25)) != 0 {
		t.Fatalf("USD gross = %v, want exact 0.32", gross["USD"])
	}
	if gross["EUR"] == nil || gross["EUR"].Cmp(big.NewRat(3, 2)) != 0 {
		t.Fatalf("EUR gross = %v, want exact 1.50", gross["EUR"])
	}
	if len(gross) != 2 {
		t.Fatalf("gross = %+v, want exactly two native currencies", gross)
	}
}

func TestSummarizeEmptyAndSkewedClock(t *testing.T) {
	t.Parallel()
	now := healthTestTime()
	empty, err := SummarizeEconomicHealth(EconomicHealthInput{}, now)
	requireNoError(t, err)
	if len(empty.Queues) != 0 || empty.WindowRows != 0 {
		t.Fatalf("empty input must yield zeros: %+v", empty)
	}
	if !empty.TakenAt.Equal(now) {
		t.Fatalf("taken at = %v, want %v", empty.TakenAt, now)
	}
	future, err := SummarizeEconomicHealth(EconomicHealthInput{
		Queues: []EconomicQueueRow{{Queue: "customer", Status: "pending", Count: 1, OldestCreatedUnix: now.Add(time.Hour).Unix()}},
	}, now)
	requireNoError(t, err)
	if future.Queues[0].OldestAgeSec != 0 {
		t.Fatalf("future timestamp must clamp age to 0, got %v", future.Queues[0].OldestAgeSec)
	}
	if _, err := SummarizeEconomicHealth(EconomicHealthInput{}, time.Time{}); err == nil {
		t.Fatal("zero clock must fail closed, never panic on age math")
	}
}

func TestSummarizeRejectsNegativeCounts(t *testing.T) {
	t.Parallel()
	if _, err := SummarizeEconomicHealth(EconomicHealthInput{
		Queues: []EconomicQueueRow{{Queue: "customer", Status: "pending", Count: -1}},
	}, healthTestTime()); err == nil {
		t.Fatal("negative queue count must fail closed")
	}
}

// Finding 4 RED contract: backlog age and backlog presence describe only
// outstanding work. Completed, processed and failed rows are retained history;
// they must keep their per-status counts but never contribute an age, dominate
// a newer outstanding row, or fabricate a backlog for a terminal-only queue.
func healthQueuesByBucket(got EconomicHealthSnapshot) map[string]EconomicQueueHealth {
	byQueue := map[string]EconomicQueueHealth{}
	for _, queue := range got.Queues {
		byQueue[queue.Queue] = queue
	}
	return byQueue
}

func TestSummarizeTerminalQueuesHaveNoBacklogAge(t *testing.T) {
	t.Parallel()
	now := healthTestTime()
	got, err := SummarizeEconomicHealth(EconomicHealthInput{
		Queues: []EconomicQueueRow{
			{Queue: string(EconomicWorkKindCustomerRating), Status: string(EconomicWorkStatusCompleted), Count: 4, OldestCreatedUnix: now.Add(-10 * time.Minute).Unix()},
			{Queue: string(EconomicWorkKindProviderRating), Status: string(EconomicWorkStatusFailed), Count: 2, OldestCreatedUnix: now.Add(-30 * time.Minute).Unix()},
			{Queue: string(EconomicWorkKindReconciliation), Status: "processed", Count: 5, OldestCreatedUnix: now.Add(-time.Hour).Unix()},
			{Queue: EconomicHealthQueueProviderLegacy, Status: "processed", Count: 3, OldestCreatedUnix: now.Add(-2 * time.Hour).Unix()},
		},
	}, now)
	requireNoError(t, err)
	byQueue := healthQueuesByBucket(got)
	customer, ok := byQueue[string(EconomicWorkKindCustomerRating)]
	if !ok {
		t.Fatalf("customer_rating bucket missing: %+v", got.Queues)
	}
	if customer.Completed != 4 || customer.Pending != 0 || customer.Processing != 0 {
		t.Fatalf("customer_rating = %+v, want completed=4 with zero outstanding", customer)
	}
	if customer.OldestAgeSec != 0 {
		t.Fatalf("completed-only backlog age = %v, want 0", customer.OldestAgeSec)
	}
	provider, ok := byQueue[string(EconomicWorkKindProviderRating)]
	if !ok || provider.Failed != 2 || provider.OldestAgeSec != 0 {
		t.Fatalf("provider_rating = %+v, want failed=2 with zero backlog age", provider)
	}
	reconciliation, ok := byQueue[string(EconomicWorkKindReconciliation)]
	if !ok || reconciliation.Processed != 5 || reconciliation.OldestAgeSec != 0 {
		t.Fatalf("reconciliation = %+v, want processed=5 with zero backlog age", reconciliation)
	}
	legacy, ok := byQueue[EconomicHealthQueueProviderLegacy]
	if !ok || legacy.Processed != 3 || legacy.OldestAgeSec != 0 {
		t.Fatalf("provider_legacy = %+v, want processed=3 with zero backlog age", legacy)
	}
}

func TestSummarizeBacklogAgeIgnoresTerminalHistory(t *testing.T) {
	t.Parallel()
	now := healthTestTime()
	got, err := SummarizeEconomicHealth(EconomicHealthInput{
		Queues: []EconomicQueueRow{
			{Queue: string(EconomicWorkKindCustomerRating), Status: string(EconomicWorkStatusCompleted), Count: 4, OldestCreatedUnix: now.Add(-10 * time.Minute).Unix()},
			{Queue: string(EconomicWorkKindCustomerRating), Status: string(EconomicWorkStatusPending), Count: 1, OldestCreatedUnix: now.Add(-30 * time.Second).Unix()},
			{Queue: string(EconomicWorkKindProviderRating), Status: string(EconomicWorkStatusFailed), Count: 2, OldestCreatedUnix: now.Add(-time.Hour).Unix()},
			{Queue: string(EconomicWorkKindProviderRating), Status: string(EconomicWorkStatusProcessing), Count: 3, OldestCreatedUnix: now.Add(-2 * time.Minute).Unix()},
			{Queue: string(EconomicWorkKindReconciliation), Status: "processed", Count: 5, OldestCreatedUnix: now.Add(-3 * time.Hour).Unix()},
			{Queue: string(EconomicWorkKindReconciliation), Status: string(EconomicWorkStatusPending), Count: 2, OldestCreatedUnix: now.Add(-45 * time.Second).Unix()},
			{Queue: EconomicHealthQueueProviderLegacy, Status: "processed", Count: 6, OldestCreatedUnix: now.Add(-4 * time.Hour).Unix()},
			{Queue: EconomicHealthQueueProviderLegacy, Status: string(EconomicWorkStatusPending), Count: 1, OldestCreatedUnix: now.Add(-12 * time.Second).Unix()},
		},
	}, now)
	requireNoError(t, err)
	byQueue := healthQueuesByBucket(got)
	for _, want := range []struct {
		bucket string
		minAge float64
		maxAge float64
	}{
		{string(EconomicWorkKindCustomerRating), 29, 31},
		{string(EconomicWorkKindProviderRating), 119, 121},
		{string(EconomicWorkKindReconciliation), 44, 46},
		{EconomicHealthQueueProviderLegacy, 11, 13},
	} {
		queue, ok := byQueue[want.bucket]
		if !ok {
			t.Fatalf("bucket %q missing: %+v", want.bucket, got.Queues)
		}
		if queue.OldestAgeSec < want.minAge || queue.OldestAgeSec > want.maxAge {
			t.Fatalf("bucket %q backlog age = %v, want ~[%v,%v] from outstanding work", want.bucket, queue.OldestAgeSec, want.minAge, want.maxAge)
		}
	}
	customer := byQueue[string(EconomicWorkKindCustomerRating)]
	if customer.Completed != 4 || customer.Pending != 1 {
		t.Fatalf("customer_rating = %+v, want completed=4 pending=1", customer)
	}
	provider := byQueue[string(EconomicWorkKindProviderRating)]
	if provider.Failed != 2 || provider.Processing != 3 {
		t.Fatalf("provider_rating = %+v, want failed=2 processing=3", provider)
	}
	reconciliation := byQueue[string(EconomicWorkKindReconciliation)]
	if reconciliation.Processed != 5 || reconciliation.Pending != 2 {
		t.Fatalf("reconciliation = %+v, want processed=5 pending=2", reconciliation)
	}
	legacy := byQueue[EconomicHealthQueueProviderLegacy]
	if legacy.Processed != 6 || legacy.Pending != 1 {
		t.Fatalf("provider_legacy = %+v, want processed=6 pending=1", legacy)
	}
}

func TestSummarizeBacklogAgeUsesOldestOutstanding(t *testing.T) {
	t.Parallel()
	now := healthTestTime()
	got, err := SummarizeEconomicHealth(EconomicHealthInput{
		Queues: []EconomicQueueRow{
			{Queue: string(EconomicWorkKindCustomerRating), Status: string(EconomicWorkStatusCompleted), Count: 1, OldestCreatedUnix: now.Add(-time.Hour).Unix()},
			{Queue: string(EconomicWorkKindCustomerRating), Status: string(EconomicWorkStatusFailed), Count: 4, OldestCreatedUnix: now.Add(-10 * time.Minute).Unix()},
			{Queue: string(EconomicWorkKindCustomerRating), Status: string(EconomicWorkStatusPending), Count: 2, OldestCreatedUnix: now.Add(-30 * time.Second).Unix()},
			{Queue: string(EconomicWorkKindCustomerRating), Status: string(EconomicWorkStatusProcessing), Count: 3, OldestCreatedUnix: now.Add(-5 * time.Minute).Unix()},
		},
	}, now)
	requireNoError(t, err)
	customer := healthQueuesByBucket(got)[string(EconomicWorkKindCustomerRating)]
	if customer.OldestAgeSec < 299 || customer.OldestAgeSec > 301 {
		t.Fatalf("backlog age = %v, want ~300s from oldest outstanding row", customer.OldestAgeSec)
	}
	if customer.Completed != 1 || customer.Failed != 4 || customer.Pending != 2 || customer.Processing != 3 {
		t.Fatalf("per-status counts must be preserved: %+v", customer)
	}
}

func TestSummarizeUnknownStatusNeverInflatesBacklog(t *testing.T) {
	t.Parallel()
	now := healthTestTime()
	got, err := SummarizeEconomicHealth(EconomicHealthInput{
		Queues: []EconomicQueueRow{
			{Queue: string(EconomicWorkKindCustomerRating), Status: "future_status", Count: 9, OldestCreatedUnix: now.Add(-2 * time.Hour).Unix()},
			{Queue: string(EconomicWorkKindCustomerRating), Status: string(EconomicWorkStatusPending), Count: 1, OldestCreatedUnix: now.Add(-25 * time.Second).Unix()},
			{Queue: string(EconomicWorkKindProviderRating), Status: "future_status", Count: 7, OldestCreatedUnix: now.Add(-2 * time.Hour).Unix()},
		},
	}, now)
	requireNoError(t, err)
	byQueue := healthQueuesByBucket(got)
	customer := byQueue[string(EconomicWorkKindCustomerRating)]
	if customer.Other != 9 || customer.Pending != 1 {
		t.Fatalf("customer_rating = %+v, want other=9 pending=1", customer)
	}
	if customer.OldestAgeSec < 24 || customer.OldestAgeSec > 26 {
		t.Fatalf("unknown status must not inflate backlog age, got %v want ~25s", customer.OldestAgeSec)
	}
	provider := byQueue[string(EconomicWorkKindProviderRating)]
	if provider.Other != 7 || provider.Pending != 0 || provider.Processing != 0 {
		t.Fatalf("provider_rating = %+v, want other=7 with zero outstanding", provider)
	}
	if provider.OldestAgeSec != 0 {
		t.Fatalf("unknown-only backlog age = %v, want 0", provider.OldestAgeSec)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
