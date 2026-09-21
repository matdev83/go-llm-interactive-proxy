package billing

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

// Task 16.3B pure low-cardinality economics health summary.
//
// SummarizeEconomicHealth projects bounded store rows into queue backlogs,
// retry buckets, statement outcomes and exact per-currency discrepancy
// exposure. Queue names, statuses and retry reasons pass through explicit
// allowlists; unknown values collapse to "other" so no call, leg, account,
// tenant, statement-line, external identity or raw error string can become
// a diagnostic dimension. Discrepancy math stays exact per native currency
// with no FX conversion. Backlog ages derive only from outstanding (non-
// terminal) rows, using persisted timestamps with negative results clamped to
// zero; terminal completed/processed/failed history keeps its per-status
// counts but cannot inflate backlog age. Empty input yields zeros, never a
// panic. The function performs no I/O, takes no locks and retains no
// raw content.

var (
	// ErrEconomicHealthInput identifies malformed snapshot input: negative
	// counts, unknown clocks or inexact arithmetic outside the bounded
	// exact contract.
	ErrEconomicHealthInput = errors.New("billing: invalid economic health input")
)

// EconomicHealthReader is the durable query port for the economics health
// snapshot. Implementations perform bounded store-scoped reads without
// taking customer balance locks.
type EconomicHealthReader interface {
	EconomicHealthSnapshot(context.Context) (EconomicHealthSnapshot, error)
}

// EconomicQueueRow is one store-projected (queue, status) work aggregate.
// Queue carries the worker queue or work kind; Status carries the durable
// work state. Both are re-mapped through allowlists during summarization.
type EconomicQueueRow struct {
	Queue             string
	Status            string
	Count             int
	OldestCreatedUnix int64
	MaxAttempts       int
}

// EconomicRetryRow is one store-projected (queue, retry reason) aggregate.
// Attempts carries the maximum attempt count in the bucket and classifies
// empty reasons: attempts without a recorded reason are unclassified.
type EconomicRetryRow struct {
	Queue    string
	Reason   string
	Count    int
	Attempts int
}

// EconomicStatementRow is one store-projected statement outcome aggregate.
type EconomicStatementRow struct {
	Outcome string
	Count   int
}

// EconomicHealthInput is the frozen snapshot input. Retentions arrive as
// parsed immutable results in durable order; at most the bounded window
// supplied by the caller is summarized.
type EconomicHealthInput struct {
	Queues     []EconomicQueueRow
	Retries    []EconomicRetryRow
	Statements []EconomicStatementRow
	Retentions []ReconciliationRetentionResult
}

// EconomicQueueHealth is one bounded queue bucket. Only documented counters
// exist; per-request or per-account identities never appear here.
type EconomicQueueHealth struct {
	Queue        string               `json:"queue"`
	Pending      int                  `json:"pending"`
	Processing   int                  `json:"processing"`
	Completed    int                  `json:"completed"`
	Processed    int                  `json:"processed"`
	Failed       int                  `json:"failed"`
	Other        int                  `json:"other"`
	OldestAgeSec float64              `json:"oldest_age_seconds"`
	MaxAttempts  int                  `json:"max_attempts"`
	Retries      []EconomicRetryCount `json:"retries,omitempty"`
}

// EconomicRetryCount is one bounded retry-reason bucket.
type EconomicRetryCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// EconomicStatementHealth counts retained statement lines by outcome.
type EconomicStatementHealth struct {
	Matched   int `json:"matched"`
	Unmatched int `json:"unmatched"`
	Other     int `json:"other"`
}

// EconomicStatusCount is one bounded comparison-state bucket.
type EconomicStatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// EconomicCurrencyAmount is one exact native-currency total rendered as an
// exact rational string (integers without a denominator). Currencies are
// never converted into each other.
type EconomicCurrencyAmount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// EconomicDiscrepancyHealth summarizes one bounded retention window:
// per-plane status buckets, exact per-currency gross absolute exposure and
// explicit partial/incomparable/conflict rollups.
type EconomicDiscrepancyHealth struct {
	ByQuantity   []EconomicStatusCount    `json:"by_quantity,omitempty"`
	ByMonetary   []EconomicStatusCount    `json:"by_monetary,omitempty"`
	Gross        []EconomicCurrencyAmount `json:"gross_absolute,omitempty"`
	Partial      int                      `json:"partial"`
	Incomparable int                      `json:"incomparable"`
	Conflict     int                      `json:"conflict"`
}

// EconomicHealthSnapshot is one deterministic point-in-time rollup. Queues
// sort by name; every other list sorts by its stable key.
type EconomicHealthSnapshot struct {
	Queues        []EconomicQueueHealth     `json:"queues,omitempty"`
	Statements    EconomicStatementHealth   `json:"statements"`
	Discrepancies EconomicDiscrepancyHealth `json:"discrepancies"`
	WindowRows    int                       `json:"window_rows"`
	WindowCapped  bool                      `json:"window_capped"`
	TakenAt       time.Time                 `json:"taken_at"`
}

// Economic health queue vocabulary. Worker queues, legacy provider costing
// and the three documented economic work kinds are the only values that
// survive summarization; anything else becomes "other".
const (
	EconomicHealthQueueOther          = "other"
	EconomicHealthQueueProviderLegacy = "provider_legacy"
	EconomicHealthReasonOther         = "other"
	EconomicHealthReasonUnclassified  = "unclassified"
	EconomicHealthStatementOther      = "other"
	EconomicHealthStatusOther         = "other"
	EconomicHealthMaxWindowRows       = 256
	EconomicHealthMaxReasonBuckets    = 16
)

func economicHealthQueueBucket(queue string) string {
	switch queue {
	case string(EconomicQueueCustomer), string(EconomicQueueProvider),
		EconomicHealthQueueProviderLegacy,
		string(EconomicWorkKindCustomerRating), string(EconomicWorkKindProviderRating),
		string(EconomicWorkKindReconciliation):
		return queue
	default:
		return EconomicHealthQueueOther
	}
}

func economicHealthStatusBucket(status string) string {
	switch status {
	case "pending", "processing", "completed", "processed", "failed":
		return status
	default:
		return EconomicHealthStatusOther
	}
}

// economicHealthStatusOutstanding reports whether one durable work status
// still counts as unfinished backlog for its queue bucket. Backlog age must
// describe outstanding work only: terminal completed/processed/failed history
// is retained but never contributes an age, never dominates a newer
// outstanding row and never fabricates a backlog for a terminal-only queue.
//
// The mapping is explicit per queue family and never inferred from
// suffixes or substrings:
//   - economic revision work (the customer/provider queues and the three
//     documented work kinds) is outstanding while pending or processing;
//   - the legacy provider-costing queue is outstanding while pending, since
//     processed is its only terminal state.
//
// Any unrecognized status, including a future status introduced without a
// mapping update, is explicitly non-outstanding and therefore fails closed:
// it is still counted per-status but cannot inflate backlog age.
func economicHealthStatusOutstanding(queueBucket, status string) bool {
	if queueBucket == EconomicHealthQueueProviderLegacy {
		return status == "pending"
	}
	switch EconomicWorkStatus(status) {
	case EconomicWorkStatusPending, EconomicWorkStatusProcessing:
		return true
	default:
		return false
	}
}

func economicHealthReasonBucket(reason string, attempts int) (string, bool) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		if attempts <= 0 {
			return "", false
		}
		return EconomicHealthReasonUnclassified, true
	}
	switch EconomicWorkReason(reason) {
	case EconomicWorkReasonUnclassified, EconomicWorkReasonTransientFailure,
		EconomicWorkReasonRaterFailure, EconomicWorkReasonReconcilerFailure,
		EconomicWorkReasonPersistenceFailure, EconomicWorkReasonDependencyPending,
		EconomicWorkReasonLeaseExpired, EconomicWorkReasonPermanentFailure:
		return reason, true
	default:
		return EconomicHealthReasonOther, true
	}
}

func economicHealthStatementBucket(outcome string) string {
	switch outcome {
	case "matched", "unmatched":
		return outcome
	default:
		return EconomicHealthStatementOther
	}
}

// SummarizeEconomicHealth folds bounded store rows into a deterministic
// snapshot. Unknown queue, status and reason values collapse to "other";
// exact discrepancy math uses big rationals with no float fallback.
func SummarizeEconomicHealth(input EconomicHealthInput, now time.Time) (EconomicHealthSnapshot, error) {
	if now.IsZero() {
		return EconomicHealthSnapshot{}, fmt.Errorf("%w: clock required", ErrEconomicHealthInput)
	}
	nowUnix := now.Unix()
	queues := map[string]*EconomicQueueHealth{}
	ensureQueue := func(bucket string) *EconomicQueueHealth {
		if existing, ok := queues[bucket]; ok {
			return existing
		}
		health := &EconomicQueueHealth{Queue: bucket}
		queues[bucket] = health
		return health
	}
	oldest := map[string]int64{}
	for _, row := range input.Queues {
		if row.Count < 0 || row.MaxAttempts < 0 {
			return EconomicHealthSnapshot{}, fmt.Errorf("%w: negative queue count", ErrEconomicHealthInput)
		}
		health := ensureQueue(economicHealthQueueBucket(row.Queue))
		switch economicHealthStatusBucket(row.Status) {
		case "pending":
			health.Pending += row.Count
		case "processing":
			health.Processing += row.Count
		case "completed":
			health.Completed += row.Count
		case "processed":
			health.Processed += row.Count
		case "failed":
			health.Failed += row.Count
		default:
			health.Other += row.Count
		}
		if row.MaxAttempts > health.MaxAttempts {
			health.MaxAttempts = row.MaxAttempts
		}
		if row.Count > 0 && row.OldestCreatedUnix > 0 && economicHealthStatusOutstanding(health.Queue, row.Status) {
			if prior, ok := oldest[health.Queue]; !ok || row.OldestCreatedUnix < prior {
				oldest[health.Queue] = row.OldestCreatedUnix
			}
		}
	}
	retryCounts := map[string]map[string]int{}
	for _, row := range input.Retries {
		if row.Count < 0 || row.Attempts < 0 {
			return EconomicHealthSnapshot{}, fmt.Errorf("%w: negative retry count", ErrEconomicHealthInput)
		}
		bucket, ok := economicHealthReasonBucket(row.Reason, row.Attempts)
		if !ok {
			continue
		}
		queueBucket := economicHealthQueueBucket(row.Queue)
		ensureQueue(queueBucket)
		if retryCounts[queueBucket] == nil {
			retryCounts[queueBucket] = map[string]int{}
		}
		retryCounts[queueBucket][bucket] += row.Count
		if len(retryCounts[queueBucket]) > EconomicHealthMaxReasonBuckets {
			return EconomicHealthSnapshot{}, fmt.Errorf("%w: retry bucket overflow", ErrEconomicHealthInput)
		}
	}
	var statements EconomicStatementHealth
	for _, row := range input.Statements {
		if row.Count < 0 {
			return EconomicHealthSnapshot{}, fmt.Errorf("%w: negative statement count", ErrEconomicHealthInput)
		}
		switch economicHealthStatementBucket(row.Outcome) {
		case "matched":
			statements.Matched += row.Count
		case "unmatched":
			statements.Unmatched += row.Count
		default:
			statements.Other += row.Count
		}
	}
	discrepancies, err := summarizeDiscrepancyWindow(input.Retentions)
	if err != nil {
		return EconomicHealthSnapshot{}, err
	}
	out := EconomicHealthSnapshot{
		Statements: statements, Discrepancies: discrepancies,
		WindowRows: len(input.Retentions), TakenAt: now.UTC(),
	}
	for queue, health := range queues {
		if created, ok := oldest[queue]; ok {
			age := float64(nowUnix - created)
			if age < 0 {
				age = 0
			}
			health.OldestAgeSec = age
		}
		if counts, ok := retryCounts[queue]; ok {
			for reason, count := range counts {
				health.Retries = append(health.Retries, EconomicRetryCount{Reason: reason, Count: count})
			}
			sort.SliceStable(health.Retries, func(i, j int) bool { return health.Retries[i].Reason < health.Retries[j].Reason })
		}
		out.Queues = append(out.Queues, *health)
	}
	sort.SliceStable(out.Queues, func(i, j int) bool { return out.Queues[i].Queue < out.Queues[j].Queue })
	sort.SliceStable(out.Discrepancies.ByQuantity, func(i, j int) bool {
		return out.Discrepancies.ByQuantity[i].Status < out.Discrepancies.ByQuantity[j].Status
	})
	sort.SliceStable(out.Discrepancies.ByMonetary, func(i, j int) bool {
		return out.Discrepancies.ByMonetary[i].Status < out.Discrepancies.ByMonetary[j].Status
	})
	sort.SliceStable(out.Discrepancies.Gross, func(i, j int) bool { return out.Discrepancies.Gross[i].Currency < out.Discrepancies.Gross[j].Currency })
	return out, nil
}

func summarizeDiscrepancyWindow(retentions []ReconciliationRetentionResult) (EconomicDiscrepancyHealth, error) {
	var out EconomicDiscrepancyHealth
	byQuantity := map[string]int{}
	byMonetary := map[string]int{}
	gross := map[string]*big.Rat{}
	for _, result := range retentions {
		if result.Quantity != nil {
			status := string(result.Quantity.Status)
			if !reconciliationFindingStatusKnown(result.Quantity.Status) {
				status = string(ReconciliationStatusIncomparable)
			}
			byQuantity[status]++
			switch result.Quantity.Status {
			case ReconciliationStatusPartial, ReconciliationStatusMissingLocal, ReconciliationStatusMissingProvider:
				out.Partial++
			case ReconciliationStatusIncomparable:
				out.Incomparable++
			case ReconciliationStatusConflict:
				out.Conflict++
			}
		}
		if result.Monetary != nil {
			state := string(result.Monetary.Status)
			switch MonetaryDiscrepancyStatus(state) {
			case MonetaryDiscrepancyComplete, MonetaryDiscrepancyPartial, MonetaryDiscrepancyIncomparable, MonetaryDiscrepancyConflict:
			default:
				state = string(MonetaryDiscrepancyIncomparable)
			}
			byMonetary[state]++
			switch result.Monetary.Status {
			case MonetaryDiscrepancyPartial:
				out.Partial++
			case MonetaryDiscrepancyIncomparable:
				out.Incomparable++
			case MonetaryDiscrepancyConflict:
				out.Conflict++
			}
		}
		if err := accumulateRetentionGross(gross, result); err != nil {
			return EconomicDiscrepancyHealth{}, err
		}
	}
	for status, count := range byQuantity {
		out.ByQuantity = append(out.ByQuantity, EconomicStatusCount{Status: status, Count: count})
	}
	for state, count := range byMonetary {
		out.ByMonetary = append(out.ByMonetary, EconomicStatusCount{Status: state, Count: count})
	}
	for currency, total := range gross {
		if total.Sign() == 0 {
			continue
		}
		out.Gross = append(out.Gross, EconomicCurrencyAmount{Currency: currency, Amount: total.RatString()})
	}
	return out, nil
}

// accumulateRetentionGross folds one retention into per-currency gross
// absolute exposure. The retained aggregate projection wins when present;
// otherwise exact complete monetary terms contribute their magnitudes.
// End-to-end deltas never add separately: they equal metering effect plus
// residual, and adding them would double count the same difference.
func accumulateRetentionGross(gross map[string]*big.Rat, result ReconciliationRetentionResult) error {
	if result.Aggregate != nil {
		for _, row := range result.Aggregate.Rows {
			if row.GrossAbsoluteDiscrepancy == nil {
				continue
			}
			rat, err := row.GrossAbsoluteDiscrepancy.Rat()
			if err != nil {
				return fmt.Errorf("%w: aggregate gross: %v", ErrEconomicHealthInput, err)
			}
			amount := new(big.Rat).Abs(rat)
			currency := row.GrossAbsoluteDiscrepancy.Currency
			if gross[currency] == nil {
				gross[currency] = new(big.Rat)
			}
			gross[currency].Add(gross[currency], amount)
		}
		return nil
	}
	if result.Monetary == nil {
		return nil
	}
	for _, row := range result.Monetary.Rows {
		for _, term := range []MonetaryDiscrepancyTerm{row.MeteringCostEffect, row.ReportedPriceResidual} {
			if term.Status != MonetaryTermComplete || term.Amount == nil {
				continue
			}
			rat, err := term.Amount.Rat()
			if err != nil {
				return fmt.Errorf("%w: monetary term: %v", ErrEconomicHealthInput, err)
			}
			amount := new(big.Rat).Abs(rat)
			if gross[term.Amount.Currency] == nil {
				gross[term.Amount.Currency] = new(big.Rat)
			}
			gross[term.Amount.Currency].Add(gross[term.Amount.Currency], amount)
		}
	}
	return nil
}
