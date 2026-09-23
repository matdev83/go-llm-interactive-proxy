package billingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// Cycle 1 customer-authority regressions for Task 5.3. Each test seeds
// durable facts through production writers (or raw INSERT drift that the
// validated writers reject) and asserts the rolling snapshot classifies the
// call exactly once: known positive, known zero, pending, or unknown with an
// issue. Unknown is never a zero amount.

func seedALegCustomerAccount(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	require.NoError(t, store.CreateAccount(context.Background(), billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 10_000_000, State: billing.AccountReady, Version: 1,
	}))
}

func seedALegCustomerCall(t *testing.T, store *DurableStore, accountID, aLegID string, bLegs []string, chargeNano int64) billing.BillingCallID {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
		ExpectedBLegIDs:    append([]string(nil), bLegs...),
	}
	require.NoError(t, store.AppendCallUsage(ctx, call))
	for i, bLegID := range bLegs {
		require.NoError(t, store.AppendCallLegUsage(ctx, billing.CallLegUsageRecord{
			CallID: callID, ALegID: aLegID, BLegID: bLegID, AttemptSeq: i + 1,
			BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		}))
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:             billing.Money{Nano: 50_000, Currency: "USD"},
		PricingRef:      billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	})
	require.NoError(t, err)
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{
			CallID: callID, Fingerprint: "fp-" + callID.String(),
			CustomerCharge: billing.Money{Nano: chargeNano, Currency: "USD"},
		},
	})
	require.NoError(t, err)
	return callID
}

func seedALegCustomerClosure(t *testing.T, store *DurableStore, accountID, aLegID string) billing.BillingCallID {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	require.NoError(t, store.AppendCallUsage(context.Background(), billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
	}))
	return callID
}

// plantALegSettlementMarker inserts one operation snapshot row directly;
// writers reject everything but the canonical contract, so this simulates
// storage drift.
func plantALegSettlementMarker(t *testing.T, store *DurableStore, opKey, accountID, kind, source, fp, integrity string) {
	t.Helper()
	_, err := store.db.NewRaw(`INSERT INTO billing_operation_snapshots(operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		opKey, accountID, kind, source, fp, integrity, "USD", "prepaid", 100, 100, 0, 0, 100, 100, 0, 0, 1, 1, 0, 0, time.Now().UTC()).Exec(context.Background())
	require.NoError(t, err)
}

// plantALegSettlementJournal inserts one sealed journal row directly with
// full linkage control for correction-chain regressions.
func plantALegSettlementJournal(t *testing.T, store *DurableStore, id, accountID, turnID, aLegID, kind, debitLedger, creditLedger string, amount int64, seq uint64, reversalOf, corrects, group string) {
	t.Helper()
	sealed, err := billing.JournalTransaction{
		ID: id, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "src-" + id,
		AccountID: accountID, TurnID: turnID, ALegID: aLegID, OperationKind: kind,
		ReversalOf: reversalOf, CorrectsTransactionID: corrects, CorrectionGroupID: group,
		Entries: []billing.JournalEntry{
			{LedgerAccount: debitLedger, Side: billing.JournalDebit, Amount: billing.Money{Nano: amount, Currency: "USD"}},
			{LedgerAccount: creditLedger, Side: billing.JournalCredit, Amount: billing.Money{Nano: amount, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, accountID, "financial", "USD", "src-"+id, sealed.SemanticFingerprint,
		turnID, aLegID, "", seq, reversalOf, corrects, group, kind, 0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(context.Background())
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		id, 0, debitLedger, "debit", "USD", amount).Exec(context.Background())
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		id, 1, creditLedger, "credit", "USD", amount).Exec(context.Background())
	require.NoError(t, err)
}

func queryALegCustomerReport(t *testing.T, store *DurableStore, accountID, aLegID string, limit int, cursor string) billing.ALegReport {
	t.Helper()
	report, err := store.QueryALegReport(context.Background(), billing.ALegReportQuery{
		AccountID: accountID, ALegID: aLegID, Limit: limit, Cursor: cursor,
	})
	require.NoError(t, err)
	return report
}

func findALegCall(t *testing.T, report billing.ALegReport, callID billing.BillingCallID) billing.ALegReportCallSummary {
	t.Helper()
	for _, row := range report.Calls {
		if row.CallID == callID {
			return row.ALegReportCallSummary
		}
	}
	t.Fatalf("call %q not on page", callID.String())
	return billing.ALegReportCallSummary{}
}

func requireALegIssue(t *testing.T, report billing.ALegReport, code string) {
	t.Helper()
	for _, issue := range report.Issues {
		if issue.Code == code {
			return
		}
	}
	t.Fatalf("issue %q not reported, issues = %+v", code, report.Issues)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

// alegQueryRecorder is a deterministic bun hook recording every executed
// query's interpolated SQL. It proves bounded materialization without
// wall-clock or heap thresholds: IN-list lengths stay chunk-bounded and
// fact loads stream in chunks instead of one scope-wide statement.
type alegQueryRecorder struct {
	queries []alegRecordedQuery
}

type alegRecordedQuery struct {
	sql string
}

func (h *alegQueryRecorder) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *alegQueryRecorder) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	h.queries = append(h.queries, alegRecordedQuery{sql: event.Query})
}

// maxInListLen returns the largest top-level IN-list length across recorded
// queries. Fixture IDs never contain commas or parentheses, so splitting the
// balanced IN segment on commas is exact for these shapes.
func (h *alegQueryRecorder) maxInListLen() int {
	max := 0
	for _, q := range h.queries {
		rest := q.sql
		for {
			idx := strings.Index(rest, "IN (")
			if idx < 0 {
				break
			}
			segment := rest[idx+4:]
			depth := 1
			end := 0
			for end < len(segment) && depth > 0 {
				switch segment[end] {
				case '(':
					depth++
				case ')':
					depth--
				}
				end++
			}
			items := 0
			if strings.TrimSpace(segment[:end-1]) != "" {
				items = strings.Count(segment[:end-1], ",") + 1
			}
			if items > max {
				max = items
			}
			rest = segment[end:]
		}
	}
	return max
}

func (h *alegQueryRecorder) countMatching(substr string) int {
	count := 0
	for _, q := range h.queries {
		if strings.Contains(q.sql, substr) {
			count++
		}
	}
	return count
}

// maxInListLenMatching bounds the largest IN-list among recorded queries
// naming the given SQL fragment (table or column shape), so slices with
// different density constants prove their own bound independently.
func (h *alegQueryRecorder) maxInListLenMatching(substr string) int {
	probe := &alegQueryRecorder{}
	for _, q := range h.queries {
		if strings.Contains(q.sql, substr) {
			probe.queries = append(probe.queries, q)
		}
	}
	return probe.maxInListLen()
}

// TestALegReportBoundedFactLoading proves a small page over a large scope
// never materializes unbounded ID/fact sets: every query carries at most a
// chunk-bounded placeholder list, and marker loads stream in chunks rather
// than one scope-wide IN list.
func TestALegReportBoundedFactLoading(t *testing.T) {
	// No t.Parallel: this test mutates the package chunk size; sequential
	// execution keeps the override and its Cleanup restore deterministic.
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-bound", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	for i := 0; i < 10; i++ {
		seedALegCustomerCall(t, store, accountID, aLegID, nil, 10)
	}
	recorder := &alegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	const chunk = 3
	oldChunk := alegReportScopeChunkSize
	alegReportScopeChunkSize = chunk
	t.Cleanup(func() { alegReportScopeChunkSize = oldChunk })

	report := queryALegCustomerReport(t, store, accountID, aLegID, 2, "")
	require.Equal(t, 10, report.CallCount)
	require.Equal(t, int64(100), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 10, report.Retail.SettledCalls)
	require.LessOrEqual(t, recorder.maxInListLen(), chunk,
		"every IN-list must stay chunk-bounded")
	require.GreaterOrEqual(t, recorder.countMatching("billing_operation_snapshots"), 3,
		"marker loads must stream in chunks, got %d marker queries", recorder.countMatching("billing_operation_snapshots"))
}

func TestALegReportCanonicalPositive(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-pos", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, []string{"b-1"}, 75)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.False(t, report.AsOf.IsZero(), "AsOf must be an output observation timestamp")
	require.Equal(t, 1, report.CallCount)
	require.Len(t, report.Calls, 1)
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallKnown, summary.Status)
	require.True(t, summary.CustomerChargeKnown)
	require.Equal(t, int64(75), summary.CustomerCharge.Nano)
	require.Equal(t, "USD", summary.CustomerCharge.Currency)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	require.Equal(t, source+":customer_call_settlement", summary.CustomerOperationKey)
	require.Equal(t, []string{"b-1"}, summary.ExpectedBLegIDs)
	require.Empty(t, summary.MissingBLegIDs)
	require.Len(t, report.Contributions, 1)
	require.Equal(t, "b-1", report.Contributions[0].BLegID)
	require.Equal(t, callID, report.Contributions[0].Call.CallID)
	require.Equal(t, billing.ALegProviderPending, report.Contributions[0].ProviderStatus)
	require.Equal(t, int64(75), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.SettledCalls)
	require.Equal(t, 0, report.Retail.PendingCalls)
	require.Equal(t, 0, report.Retail.UnknownCalls)
	require.Empty(t, report.NextCursor)
}

func TestALegReportCanonicalZero(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-zero", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 0)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallKnown, summary.Status)
	require.True(t, summary.CustomerChargeKnown, "proven zero is known, not pending")
	require.Equal(t, int64(0), summary.CustomerCharge.Nano)
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.SettledCalls)
}

func TestALegReportZeroMarkerWithStraySettlementUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-stray", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 0)
	// A valid sealed settlement journal that is not the canonical writer
	// journal is stray evidence: the zero marker cannot vouch for it.
	plantALegSettlementJournal(t, store, "tx-stray", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 5, uint64(9), "", "", "")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown, "unknown is never a zero amount")
	require.Equal(t, int64(0), summary.CustomerCharge.Nano)
	requireALegIssue(t, report, "customer_stray_journal")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 0, report.Retail.SettledCalls)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

func TestALegReportBogusMarkerIntegrityUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-bogus", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	plantALegSettlementMarker(t, store, source+":customer_call_settlement", accountID,
		"customer_call_settlement", callID.String(), "fp-bogus", "bogus-integrity")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_marker_integrity")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportUnrelatedCorrectionComponentUnknown proves a reversal of
// an unrelated same-scope root cannot move the canonical subtotal: the
// chain is not rooted at the canonical settlement journal.
func TestALegReportUnrelatedCorrectionComponentUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-f1unrel", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	plantALegSettlementJournal(t, store, "tx-f1-target", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 20, uint64(9), "", "", "")
	plantALegSettlementJournal(t, store, "tx-f1-rev", accountID, callID.String(), aLegID,
		"customer_call_settlement", "usage_revenue", "customer_financial_account", 5, uint64(10),
		"tx-f1-target", "", "tx-f1-target")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportCompetingReplacementsUnknown proves two distinct
// replacement claims for the same canonical target leave the call
// unresolved: the correction head is ambiguous.
func TestALegReportCompetingReplacementsUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-f1comp", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	plantALegSettlementJournal(t, store, "tx-f1-rev", accountID, callID.String(), aLegID,
		"customer_call_settlement", "usage_revenue", "customer_financial_account", 8, uint64(10),
		source, "", source)
	plantALegSettlementJournal(t, store, "tx-f1-rep-one", accountID, callID.String(), aLegID,
		"customer_call_settlement", "usage_revenue", "customer_financial_account", 3, uint64(11),
		"", source, source)
	plantALegSettlementJournal(t, store, "tx-f1-rep-two", accountID, callID.String(), aLegID,
		"customer_call_settlement", "usage_revenue", "customer_financial_account", 4, uint64(12),
		"", source, source)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportReversalCycleUnknown proves a reversal cycle detached
// from the canonical root cannot alter the net: unreachable components
// never establish economics.
func TestALegReportReversalCycleUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-f1cyc", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	plantALegSettlementJournal(t, store, "tx-f1-cyc-one", accountID, callID.String(), aLegID,
		"customer_call_settlement", "usage_revenue", "customer_financial_account", 5, uint64(10),
		"tx-f1-cyc-two", "", "g-f1-cyc")
	plantALegSettlementJournal(t, store, "tx-f1-cyc-two", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 2, uint64(11),
		"tx-f1-cyc-one", "", "g-f1-cyc")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportMalformedCorrectionShapeUnknown proves a balanced
// correction with an arbitrary clearing account contributes nothing:
// only the exact writer pair nets as known economics.
func TestALegReportMalformedCorrectionShapeUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-f1shape", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	plantALegSettlementJournal(t, store, "tx-f1-mal", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "arbitrary_clearing", 5, uint64(10),
		source, "", source)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportValidReplacementChainKnown proves a valid same-scope
// reversal-plus-replacement chain nets the replacement: the canonical
// 20 is fully reversed and 12 is re-asserted by a CorrectsTransactionID-only
// claim, so the call is known 12 with no correction issue. Rows are
// planted in deliberately non-account-sequence insertion order (the
// replacement row first) so evaluator sorting and deterministic graph
// traversal are exercised, not insertion order.
func TestALegReportValidReplacementChainKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-f1repok", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	plantALegSettlementJournal(t, store, "tx-f1-rep", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 12, uint64(12),
		"", source, source)
	plantALegSettlementJournal(t, store, "tx-f1-rev", accountID, callID.String(), aLegID,
		"customer_call_settlement", "usage_revenue", "customer_financial_account", 20, uint64(10),
		source, "", source)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallKnown, summary.Status)
	require.True(t, summary.CustomerChargeKnown)
	require.Equal(t, int64(12), summary.CustomerCharge.Nano)
	require.Equal(t, "USD", summary.CustomerCharge.Currency)
	require.Equal(t, source+":customer_call_settlement", summary.CustomerOperationKey)
	require.Equal(t, callID, summary.CallID)
	require.Empty(t, summary.Adjustments)
	require.Empty(t, report.Issues)
	require.Equal(t, int64(12), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.SettledCalls)
	require.Equal(t, 0, report.Retail.UnknownCalls)
}

// plantALegJournalTx seals and inserts one journal row with caller-built
// entries for fan-out/drift regressions that fixed-shape planters cannot
// express.
func plantALegJournalTx(t *testing.T, store *DurableStore, tx billing.JournalTransaction, seq uint64) {
	t.Helper()
	sealed, err := tx.Seal()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sealed.ID, sealed.AccountID, string(sealed.Book), sealed.Currency, sealed.SourceKey, sealed.SemanticFingerprint,
		sealed.TurnID, sealed.ALegID, sealed.BLegID, seq, sealed.ReversalOf, sealed.CorrectsTransactionID,
		sealed.CorrectionGroupID, sealed.OperationKind, sealed.BalanceBefore, sealed.BalanceAfter, 0, 0,
		sealed.SpendableBefore, sealed.SpendableAfter, sealed.CreditFloor, sealed.CreditLimit,
		sealed.Mode, sealed.SnapshotVersionBefore, sealed.SnapshotVersionAfter, "2020-01-01T00:00:00Z").Exec(context.Background())
	require.NoError(t, err)
	for ordinal, entry := range sealed.Entries {
		_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
			sealed.ID, ordinal, entry.LedgerAccount, string(entry.Side), entry.Amount.Currency, entry.Amount.Nano).Exec(context.Background())
		require.NoError(t, err)
	}
}

// TestALegReportJournalFanoutBounded proves per-call journal retention is
// capped independent of durable density: one settled call buried under
// unlinked settlement journals resolves unresolved with an explicit
// fanout issue instead of materializing or stray-matching them all.
func TestALegReportJournalFanoutBounded(t *testing.T) {
	// No t.Parallel: this test mutates the package journal cap, and
	// parallel tests sharing the process would observe the override window.
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-jfan", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	for i := 0; i < 6; i++ {
		plantALegSettlementJournal(t, store, fmt.Sprintf("tx-jfan-%d", i), accountID, callID.String(), aLegID,
			"customer_call_settlement", "customer_financial_account", "usage_revenue", 5, uint64(20+i), "", "", "")
	}

	oldCap := alegReportMaxJournalsPerCall
	alegReportMaxJournalsPerCall = 3
	t.Cleanup(func() { alegReportMaxJournalsPerCall = oldCap })

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_journal_fanout")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportEntryFanoutBounded proves per-transaction entry retention
// is capped: a balanced six-entry correction claim nets in full today,
// but must stay unresolved without netting a partial journal.
func TestALegReportEntryFanoutBounded(t *testing.T) {
	// No t.Parallel: this test mutates the package entry cap, and
	// parallel tests sharing the process would observe the override window.
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-efan", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	entries := []billing.JournalEntry{
		{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
	}
	for i := 0; i < 5; i++ {
		entries = append(entries, billing.JournalEntry{
			LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 4, Currency: "USD"},
		})
	}
	plantALegJournalTx(t, store, billing.JournalTransaction{
		ID: "tx-efan-claim", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "src-tx-efan-claim",
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		ReversalOf: source, CorrectsTransactionID: source, CorrectionGroupID: source,
		Entries: entries,
	}, uint64(10))

	oldCap := alegReportMaxEntriesPerTx
	alegReportMaxEntriesPerTx = 3
	t.Cleanup(func() { alegReportMaxEntriesPerTx = oldCap })

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_entry_fanout")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportUnsupportedBookConflict proves a same-call customer
// settlement-shaped journal in a nonfinancial book is a visible
// conflict: the financial canonical path must not consume it, and the
// call must not resolve known or known zero.
func TestALegReportUnsupportedBookConflict(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-book", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	_, err := store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"tx-op-book", accountID, "authorization", "USD", "src-tx-op-book", "fp-op-book",
		callID.String(), aLegID, "", uint64(30), "", "", "", "customer_call_settlement", 0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-op-book", 0, "customer_financial_account", "debit", "USD", 5).Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-op-book", 1, "usage_revenue", "credit", "USD", 5).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_book_conflict")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportPresenceFanoutBounded proves per-call B-leg presence is
// capped independent of durable fan-out: one settled call with more legs
// than the presence cap cannot mark completeness known, even though every
// leg row is individually valid.
func TestALegReportPresenceFanoutBounded(t *testing.T) {
	// No t.Parallel: this test mutates the package presence cap, and
	// parallel tests sharing the process would observe the override.
	// Sequential tests run exclusive of the parallel batch, so the
	// Cleanup restore keeps the suite deterministic.
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-fanout", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	expected := []string{"b-1", "b-2", "b-3", "b-4", "b-5", "b-6", "b-7", "b-8"}
	require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
		ExpectedBLegIDs:    expected,
	}))
	for i, bLegID := range expected {
		require.NoError(t, store.AppendCallLegUsage(ctx, billing.CallLegUsageRecord{
			CallID: callID, ALegID: aLegID, BLegID: bLegID, AttemptSeq: i + 1,
			BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		}))
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:             billing.Money{Nano: 50_000, Currency: "USD"},
		PricingRef:      billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	})
	require.NoError(t, err)
	closure, err := store.GetCallUsage(ctx, callID)
	require.NoError(t, err)
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: closure, Exposure: exposure,
		Result: billing.CallRatingResult{
			CallID: callID, Fingerprint: "fp-" + callID.String(),
			CustomerCharge: billing.Money{Nano: 80, Currency: "USD"},
		},
	})
	require.NoError(t, err)

	oldCap := alegReportMaxPresenceLegs
	alegReportMaxPresenceLegs = 3
	t.Cleanup(func() { alegReportMaxPresenceLegs = oldCap })

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status,
		"fan-out beyond the presence cap cannot mark completeness known")
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_leg_fanout")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportIssuesBoundedDuringCollection proves issue retention is
// bounded while collecting with page-call context preserved: with far
// more unresolved calls than the cap, every call on the requested page
// still carries its own issue plus the deterministic global marker.
func TestALegReportIssuesBoundedDuringCollection(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-issuecap", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	for i := 0; i < 140; i++ {
		seedALegCustomerClosure(t, store, accountID, aLegID)
	}
	first := queryALegCustomerReport(t, store, accountID, aLegID, 70, "")
	require.NotEmpty(t, first.NextCursor, "second page must exist")
	second := queryALegCustomerReport(t, store, accountID, aLegID, 70, first.NextCursor)
	require.Len(t, second.Calls, 70)
	for _, summary := range second.Calls {
		found := false
		for _, issue := range second.Issues {
			if issue.Code == "customer_marker_missing" && issue.Detail == summary.CallID.String() {
				found = true
				break
			}
		}
		require.True(t, found, "page call %q lost its issue context", summary.CallID.String())
	}
	requireALegIssue(t, second, "customer_issues_truncated")
	require.LessOrEqual(t, len(second.Issues), 128+1+70,
		"retained issues stay bounded by cap plus page context")
}

// TestALegReportAdjustmentOverflowPending proves adjustment negation is
// checked: financial-side entries accumulate to MinInt64 through checked
// steps (a debit of MaxInt64, credits of MaxInt64, MaxInt64, and 1), and
// negating that net is unrepresentable. No wrapped adjustment amount may
// be emitted; the call stays pending with its adjustment issue.
func TestALegReportAdjustmentOverflowPending(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-adjovf", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 0)
	const maxNano = int64(1<<63 - 1)
	_, err := store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"tx-adj-ovf", accountID, "financial", "USD", "src-tx-adj-ovf", "fp-overflow",
		callID.String(), aLegID, "", uint64(9), "", "", "", billing.CostPassThroughAdjustmentOperationKind, 0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	for ordinal, entry := range []struct {
		ledger, side string
		amount       int64
	}{
		{"customer_financial_account", "debit", maxNano},
		{"customer_financial_account", "credit", maxNano},
		{"customer_financial_account", "credit", maxNano},
		{"customer_financial_account", "credit", 1},
	} {
		_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
			"tx-adj-ovf", ordinal, entry.ledger, entry.side, "USD", entry.amount).Exec(ctx)
		require.NoError(t, err)
	}

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallPending, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_adjustment_pending")
	for _, ref := range summary.Adjustments {
		require.NotEqual(t, int64(-1<<63), ref.Amount.Nano,
			"wrapped negation must never surface as an adjustment amount")
	}
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportForeignALegCallCursorRejected proves a structurally valid
// cursor naming a real call from another A-leg in the same account is
// rejected: the call position must resolve inside the queried scope, or
// pagination silently skips scope calls.
func TestALegReportForeignALegCallCursorRejected(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-fcall", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	seedALegCustomerCall(t, store, accountID, aLegID, nil, 10)
	foreignCall := seedALegCustomerCall(t, store, accountID, "a-leg-other", nil, 10)

	cursor := billing.EncodeALegReportCursor("test", accountID, aLegID, "", "", foreignCall.String())
	require.NotEmpty(t, cursor)
	_, err := store.QueryALegReport(context.Background(), billing.ALegReportQuery{
		AccountID: accountID, ALegID: aLegID, Limit: 5, Cursor: cursor,
	})
	require.ErrorIs(t, err, billing.ErrReportInvalid,
		"existing call from another A-leg must not position this scope")
}

// TestALegReportForeignAccountCallCursorRejected proves the same boundary
// across accounts: a real call from another account never positions this
// scope's call stream.
func TestALegReportForeignAccountCallCursorRejected(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-facct", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	seedALegCustomerCall(t, store, accountID, aLegID, nil, 10)
	seedALegCustomerAccount(t, store, "other-acct")
	foreignCall := seedALegCustomerCall(t, store, "other-acct", "a-leg-other", nil, 10)

	cursor := billing.EncodeALegReportCursor("test", accountID, aLegID, "", "", foreignCall.String())
	require.NotEmpty(t, cursor)
	_, err := store.QueryALegReport(context.Background(), billing.ALegReportQuery{
		AccountID: accountID, ALegID: aLegID, Limit: 5, Cursor: cursor,
	})
	require.ErrorIs(t, err, billing.ErrReportInvalid,
		"existing call from another account must not position this scope")
}

// TestALegReportForeignLegPresenceExcluded proves a usage-leg SQL row
// sharing a call_id but carrying a foreign A-leg never satisfies expected
// B-leg presence for the queried A-leg, and never surfaces in lineage.
func TestALegReportForeignLegPresenceExcluded(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-fpres", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	require.NoError(t, store.AppendCallUsage(ctx, billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
		ExpectedBLegIDs:    []string{"b-x"},
	}))
	now := time.Now().UTC()
	_, err = store.db.NewRaw(`INSERT INTO usage_leg_records(usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"legkey-foreign-1", "fp-foreign", callID.String(), "a-leg-other", "b-x",
		"ok", "provider-a", "model-a", now, now, "completed", "yes", "{}", now).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, []string{"b-x"}, summary.MissingBLegIDs,
		"foreign-A-leg leg row must not satisfy expected presence")
	require.Empty(t, report.Contributions,
		"foreign-A-leg leg row must never surface in lineage")
}

// TestALegReportClosurePayloadMismatchErrors proves a decoded call-closure
// payload disagreeing with its immutable SQL row columns fails the query
// instead of contributing trusted lineage: a fully valid sealed payload
// naming another account must not read as this scope's call.
func TestALegReportClosurePayloadMismatchErrors(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-cmis", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	foreign, err := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: "other-acct", ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
	}.Seal()
	require.NoError(t, err)
	payload, err := json.Marshal(foreign)
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = store.db.NewRaw(`INSERT INTO usage_call_records(usage_call_key, fingerprint, call_id, account_id, a_leg_id, session_id, started_at, finished_at, outcome, expected_b_leg_ids, payload_json, sealed_at, next_claim_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		foreign.Key, foreign.Fingerprint, callID.String(), accountID, aLegID, "sess-"+aLegID,
		now, now, "completed", "[]", string(payload), now, now).Exec(ctx)
	require.NoError(t, err)

	_, err = store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.ErrorIs(t, err, billing.ErrReportInvalid,
		"closure payload disagreeing with its row must fail closed")
}

// TestALegReportLegPayloadMismatchErrors proves a decoded leg payload
// disagreeing with its immutable SQL row columns fails the query instead
// of surfacing under payload identity: a B-leg valid in its own payload
// but absent from its row must never appear in lineage.
func TestALegReportLegPayloadMismatchErrors(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-lmis", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	sealed, err := billing.CallLegUsageRecord{
		CallID: callID, ALegID: aLegID, BLegID: "b-other", AttemptSeq: 1,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	}.Seal()
	require.NoError(t, err)
	payload, err := json.Marshal(sealed)
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = store.db.NewRaw(`INSERT INTO usage_leg_records(usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sealed.Key, sealed.Fingerprint, callID.String(), aLegID, "b-drift",
		"ok", "provider-a", "model-a", now, now, "completed", "yes", string(payload), now).Exec(ctx)
	require.NoError(t, err)

	_, err = store.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: accountID, ALegID: aLegID})
	require.ErrorIs(t, err, billing.ErrReportInvalid,
		"leg payload disagreeing with its row must fail closed, never surface as b-other")
}

// TestALegReportCrossOperationClaimUnknown proves a repair-plane
// target/claim presented beside a settlement canonical and settlement
// marker is never netted: every correction participant must match the
// selected marker operation kind.
func TestALegReportCrossOperationClaimUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-xop", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	plantALegSettlementJournal(t, store, "tx-xop-target", accountID, callID.String(), aLegID,
		"customer_no_charge_repair", "customer_financial_account", "usage_revenue", 20, uint64(9), "", "", "")
	plantALegSettlementJournal(t, store, "tx-xop-claim", accountID, callID.String(), aLegID,
		"customer_no_charge_repair", "customer_financial_account", "usage_revenue", 20, uint64(10),
		"tx-xop-target", "tx-xop-target", "tx-xop-target")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportCanonicalCorrectionMetadataUnknown proves a selected
// canonical settlement row carrying correction linkage is noncanonical:
// the root of a correction chain is never itself a correction.
func TestALegReportCanonicalCorrectionMetadataUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-cmeta", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-meta",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-meta", "USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0),
		"USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		ReversalOf: "tx-other",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}, uint64(9), 10000, 9980, 10000, 9980, 0, 0, "prepaid", 1, 2)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_journal_mismatch")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportEmptyMarkerModeUnknown proves marker semantic invariants
// extend beyond digest integrity: an empty mode is never a coherent
// account snapshot transition, even with a matching digest and journal.
func TestALegReportEmptyMarkerModeUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-mode", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-mode",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-mode", "USD", "", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0),
		"USD", "", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}, uint64(9), 10000, 9980, 10000, 9980, 0, 0, "", 1, 2)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_marker_invalid")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportReversedMarkerSequenceUnknown proves sequence ranges run
// forward: SequenceStart above SequenceEnd is incoherent even when the
// digest covers those exact values.
func TestALegReportReversedMarkerSequenceUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-seq", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-seq",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-seq", "USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 5, 3),
		"USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 5, 3)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}, uint64(9), 10000, 9980, 10000, 9980, 0, 0, "prepaid", 1, 2)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_marker_invalid")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportBackwardMarkerVersionUnknown proves versions transition
// forward with the snapshots: a version that runs backward while the
// balance moves forward is incoherent.
func TestALegReportBackwardMarkerVersionUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-ver", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-ver",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-ver", "USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 5, 3, 0, 0),
		"USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 5, 3, 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}, uint64(9), 10000, 9980, 10000, 9980, 0, 0, "prepaid", 5, 3)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_marker_invalid")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportCorrectionOnlyPositiveUnknown proves a fully linked
// same-scope correction chain cannot independently establish economics:
// with no canonical settlement journal, a positive-looking net stays
// unresolved, never known.
func TestALegReportCorrectionOnlyPositiveUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-conly", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 0)
	plantALegSettlementJournal(t, store, "tx-conly-target", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 20, uint64(9), "", "", "")
	plantALegSettlementJournal(t, store, "tx-conly-claim", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 20, uint64(10),
		"tx-conly-target", "tx-conly-target", "tx-conly-target")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportCorrectionOnlyNegativeUnknown proves the same boundary
// for a negative net under an unchanged zero marker: no canonical
// journal, no known charge, and no negative amount leaks.
func TestALegReportCorrectionOnlyNegativeUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-coneg", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 0)
	plantALegSettlementJournal(t, store, "tx-coneg-target", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 20, uint64(9), "", "", "")
	plantALegSettlementJournal(t, store, "tx-coneg-claim", accountID, callID.String(), aLegID,
		"customer_call_settlement", "usage_revenue", "customer_financial_account", 20, uint64(10),
		"tx-coneg-target", "tx-coneg-target", "tx-coneg-target")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	require.Equal(t, billing.Money{Currency: "USD"}, summary.CustomerCharge)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

func TestALegReportMissingMarkerPending(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-pend", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallPending, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_marker_missing")
	require.Equal(t, 1, report.Retail.PendingCalls)
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

func TestALegReportPaddedCorrectionUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-pad", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	// Padded linkage never resolves to the canonical journal: raw identity
	// is required, so the correction is unresolved.
	plantALegSettlementJournal(t, store, "tx-pad-claim", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 20, uint64(10),
		" "+source+" ", " "+source+" ", source)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, 1, report.Retail.UnknownCalls)
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

func TestALegReportCrossScopeCorrectionUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-xscope", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	// Target shares the call but lives in another A-leg: same-scope chain
	// proof must reject it.
	plantALegSettlementJournal(t, store, "tx-foreign-target", accountID, callID.String(), "a-leg-other",
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 20, uint64(9), "", "", "")
	plantALegSettlementJournal(t, store, "tx-foreign-claim", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 20, uint64(10),
		"tx-foreign-target", "tx-foreign-target", "")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

func TestALegReportCanonicalCorrectionNets(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-chain", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	// A same-scope partial reversal nets against the canonical charge: the
	// validated immutable chain is included, not hidden. The reversal
	// carries exactly one link kind (ReversalOf); a claim carrying both
	// link kinds is ambiguous and unresolved (F1).
	plantALegSettlementJournal(t, store, "tx-reversal", accountID, callID.String(), aLegID,
		"customer_call_settlement", "usage_revenue", "customer_financial_account", 8, uint64(10),
		source, "", source)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallKnown, summary.Status)
	require.True(t, summary.CustomerChargeKnown)
	require.Equal(t, int64(12), summary.CustomerCharge.Nano)
	require.Equal(t, int64(12), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.SettledCalls)
}

func TestALegReportSnapshotStableAndRolling(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-roll", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	pendingID := seedALegCustomerClosure(t, store, accountID, aLegID)

	first := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegCallPending, findALegCall(t, first, pendingID).Status)
	require.Equal(t, int64(0), first.Retail.KnownSubtotal.Nano)
	// One snapshot is self-consistent: the subtotal equals the sum of the
	// known call charges on this page.
	var sum int64
	for _, summary := range first.Calls {
		if summary.CustomerChargeKnown {
			sum += summary.CustomerCharge.Nano
		}
	}
	require.Equal(t, sum, first.Retail.KnownSubtotal.Nano)

	// The pending call settles through the production writer; the next
	// projection reflects it with no finality flag anywhere.
	closure, err := store.GetCallUsage(ctx, pendingID)
	require.NoError(t, err)
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: pendingID.String(),
		Max:             billing.Money{Nano: 50_000, Currency: "USD"},
		PricingRef:      billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	})
	require.NoError(t, err)
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: closure, Exposure: exposure,
		Result: billing.CallRatingResult{
			CallID: pendingID, Fingerprint: "fp-" + pendingID.String(),
			CustomerCharge: billing.Money{Nano: 40, Currency: "USD"},
		},
	})
	require.NoError(t, err)
	second := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	secondSummary := findALegCall(t, second, pendingID)
	require.Equal(t, billing.ALegCallKnown, secondSummary.Status)
	require.Equal(t, int64(40), secondSummary.CustomerCharge.Nano)
	require.Equal(t, int64(40), second.Retail.KnownSubtotal.Nano)
	require.False(t, second.AsOf.Before(first.AsOf), "rolling snapshots move forward")

	// A resumed call appears naturally in a later projection.
	resumedID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 5)
	third := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, 2, third.CallCount)
	require.Equal(t, int64(45), third.Retail.KnownSubtotal.Nano)
	findALegCall(t, third, resumedID)
}

func TestALegReportUnknownNeverZero(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-unz", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	// Conflicting settlement and repair markers for one call: ambiguous
	// proof that must stay unknown, never a zero.
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	plantALegSettlementMarker(t, store, source+":customer_call_settlement", accountID,
		"customer_call_settlement", callID.String(), "fp-a", "integrity-a")
	plantALegSettlementMarker(t, store, source+":customer_no_charge_repair", accountID,
		"customer_no_charge_repair", callID.String(), "fp-b", "integrity-b")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	require.Equal(t, billing.Money{Currency: "USD"}, summary.CustomerCharge,
		"unknown carries no amount, not even zero")
	requireALegIssue(t, report, "customer_markers_conflicting")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
	require.Equal(t, 0, report.Retail.SettledCalls)
}

func TestALegReportCrossCurrencyCorrectionUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-xcur", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	// Same-lineage target in a foreign currency: no implicit FX, the chain
	// stays unresolved.
	sealed, err := billing.JournalTransaction{
		ID: "tx-eur-target", Book: billing.JournalBookFinancial, Currency: "EUR", SourceKey: "src-tx-eur-target",
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "EUR"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "EUR"}},
		},
	}.Seal()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"tx-eur-target", accountID, "financial", "EUR", "src-tx-eur-target", sealed.SemanticFingerprint,
		callID.String(), aLegID, "", uint64(9), "", "", "", "customer_call_settlement", 0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-eur-target", 0, "customer_financial_account", "debit", "EUR", 20).Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-eur-target", 1, "usage_revenue", "credit", "EUR", 20).Exec(ctx)
	require.NoError(t, err)
	plantALegSettlementJournal(t, store, "tx-eur-claim", accountID, callID.String(), aLegID,
		"customer_call_settlement", "customer_financial_account", "usage_revenue", 20, uint64(10),
		"tx-eur-target", "tx-eur-target", "")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

func TestALegReportBoundedIndependentCursors(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-page", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	seedALegCustomerCall(t, store, accountID, aLegID, []string{"b-1", "b-2"}, 30)
	seedALegCustomerCall(t, store, accountID, aLegID, []string{"b-3"}, 20)
	seedALegCustomerCall(t, store, accountID, aLegID, nil, 0)

	var legKeys, callIDs []string
	var firstTotals billing.ALegRetailTotals
	cursor := ""
	pages := 0
	for {
		report := queryALegCustomerReport(t, store, accountID, aLegID, 2, cursor)
		if pages == 0 {
			firstTotals = report.Retail
		}
		require.LessOrEqual(t, len(report.Contributions), 2, "leg stream must obey the page bound")
		require.LessOrEqual(t, len(report.Calls), 2, "call stream must obey the page bound")
		require.Equal(t, firstTotals, report.Retail, "scope-wide totals stay stable across pages")
		require.Equal(t, 3, report.CallCount)
		for _, c := range report.Contributions {
			legKeys = append(legKeys, c.Call.CallID.String()+"\x00"+c.BLegID)
		}
		for _, summary := range report.Calls {
			callIDs = append(callIDs, summary.CallID.String())
		}
		pages++
		if report.NextCursor == "" {
			break
		}
		cursor = report.NextCursor
		require.Less(t, pages, 10, "pagination must terminate")
	}
	require.Len(t, legKeys, 3, "every leg traversed exactly once")
	require.Len(t, callIDs, 3, "every call traversed exactly once")
	for i := 1; i < len(legKeys); i++ {
		require.Greater(t, legKeys[i], legKeys[i-1], "legs traverse in (call, b-leg) order")
	}
	require.Len(t, uniqueStrings(legKeys), 3, "no duplicate legs")
	require.Len(t, uniqueStrings(callIDs), 3, "no duplicate calls")
	require.Equal(t, int64(50), firstTotals.KnownSubtotal.Nano)

	// Determinism: a second full traversal observes the identical order.
	var legKeysAgain, callIDsAgain []string
	cursor = ""
	for {
		report := queryALegCustomerReport(t, store, accountID, aLegID, 2, cursor)
		for _, c := range report.Contributions {
			legKeysAgain = append(legKeysAgain, c.Call.CallID.String()+"\x00"+c.BLegID)
		}
		for _, summary := range report.Calls {
			callIDsAgain = append(callIDsAgain, summary.CallID.String())
		}
		if report.NextCursor == "" {
			break
		}
		cursor = report.NextCursor
	}
	require.Equal(t, legKeys, legKeysAgain, "leg traversal must be deterministic")
	require.Equal(t, callIDs, callIDsAgain, "call traversal must be deterministic")

	// A cursor minted for another A-leg is rejected here.
	otherCursor := queryALegCustomerReport(t, store, accountID, aLegID, 1, "").NextCursor
	require.NotEmpty(t, otherCursor, "bounded traversal must produce a cursor")
	_, err := store.QueryALegReport(context.Background(), billing.ALegReportQuery{
		AccountID: accountID, ALegID: "a-leg-other", Limit: 1, Cursor: otherCursor,
	})
	require.ErrorIs(t, err, billing.ErrReportInvalid)
}

func TestALegReportZeroMarkerWithAdjustmentPending(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-adj", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 0)
	// A valid pass-through journal keeps the call explicitly pending: the
	// adjustment plane is lineage only in Cycle 1 and must never yield a
	// known customer result, not even known zero.
	sealed, err := billing.JournalTransaction{
		ID: "tx-adj-1", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "src-tx-adj-1",
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: billing.CostPassThroughAdjustmentOperationKind,
		BalanceBefore: 10000, BalanceAfter: 9500, SpendableBefore: 10000, SpendableAfter: 9500,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 500, Currency: "USD"}},
			{LedgerAccount: "customer_adjustment_clearing", Side: billing.JournalCredit, Amount: billing.Money{Nano: 500, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"tx-adj-1", accountID, "financial", "USD", "src-tx-adj-1", sealed.SemanticFingerprint,
		callID.String(), aLegID, "", uint64(9), "", "", "", billing.CostPassThroughAdjustmentOperationKind, 10000, 9500, 0, 0, 10000, 9500, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-adj-1", 0, "customer_financial_account", "debit", "USD", 500).Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-adj-1", 1, "customer_adjustment_clearing", "credit", "USD", 500).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallPending, summary.Status)
	require.False(t, summary.CustomerChargeKnown, "deferred adjustment plane is never known, not even zero")
	requireALegIssue(t, report, "customer_adjustment_pending")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.PendingCalls)
	require.Len(t, summary.Adjustments, 1, "adjustment stays lineage only")
	require.Equal(t, "tx-adj-1", summary.Adjustments[0].TransactionID)
}

func TestALegReportPositiveMarkerWithAdjustmentPending(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-adjpos", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, nil, 20)
	sealed, err := billing.JournalTransaction{
		ID: "tx-adj-pos", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "src-tx-adj-pos",
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: billing.CostPassThroughAdjustmentOperationKind,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 100, Currency: "USD"}},
			{LedgerAccount: "customer_adjustment_clearing", Side: billing.JournalCredit, Amount: billing.Money{Nano: 100, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"tx-adj-pos", accountID, "financial", "USD", "src-tx-adj-pos", sealed.SemanticFingerprint,
		callID.String(), aLegID, "", uint64(10), "", "", "", billing.CostPassThroughAdjustmentOperationKind, 0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-adj-pos", 0, "customer_financial_account", "debit", "USD", 100).Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-adj-pos", 1, "customer_adjustment_clearing", "credit", "USD", 100).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallPending, summary.Status,
		"proven settlement plus deferred adjustment stays pending, never known")
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_adjustment_pending")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 0, report.Retail.SettledCalls)
	require.Equal(t, 1, report.Retail.PendingCalls)
}

func TestALegReportProviderNeverStartedStaysPending(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-prov", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
	}
	require.NoError(t, store.AppendCallUsage(ctx, call))
	for i, leg := range []struct {
		id      string
		outcome billing.LegOutcome
	}{
		{id: "b-idle", outcome: billing.LegOutcomeNeverStarted},
		{id: "b-no", outcome: billing.LegOutcomeRejected},
		{id: "b-win", outcome: billing.LegOutcomeWinner},
	} {
		require.NoError(t, store.AppendCallLegUsage(ctx, billing.CallLegUsageRecord{
			CallID: callID, ALegID: aLegID, BLegID: leg.id, AttemptSeq: i + 1,
			BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
			StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
			Outcome: leg.outcome, Surfaced: billing.SurfacedNo,
		}))
	}

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Len(t, report.Contributions, 3)
	for _, c := range report.Contributions {
		require.Equal(t, billing.ALegProviderPending, c.ProviderStatus,
			"Cycle 1 provider economics stay pending for every outcome including %q", c.Outcome)
		require.Empty(t, c.ZeroBasis, "no zero basis without provider authority")
	}
	require.Equal(t, 0, report.Provider.ZeroLegs, "no known-zero count without provider authority")
	require.Equal(t, 3, report.Provider.PendingLegs)
}

func TestALegReportExhaustedLegStreamExactOnce(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c1-exh", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	c1 := seedALegCustomerClosure(t, store, accountID, aLegID)
	require.NoError(t, store.AppendCallLegUsage(ctx, billing.CallLegUsageRecord{
		CallID: c1, ALegID: aLegID, BLegID: "b-1", AttemptSeq: 1,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	}))
	seedALegCustomerClosure(t, store, accountID, aLegID)
	seedALegCustomerClosure(t, store, accountID, aLegID)
	// Call order follows writer sealed_at creation order (nanosecond
	// production timestamps), so c1 pages first and its leg exhausts
	// while calls continue.

	// Three calls, one B-leg, limit 1: the leg stream exhausts on the first
	// page while calls continue. The exhausted position must carry forward;
	// clearing it restarts the leg query and duplicates the leg.
	var legKeys, callIDs []string
	cursor := ""
	pages := 0
	for {
		report := queryALegCustomerReport(t, store, accountID, aLegID, 1, cursor)
		require.LessOrEqual(t, len(report.Contributions), 1)
		require.LessOrEqual(t, len(report.Calls), 1)
		for _, c := range report.Contributions {
			legKeys = append(legKeys, c.Call.CallID.String()+"\x00"+c.BLegID)
		}
		for _, summary := range report.Calls {
			callIDs = append(callIDs, summary.CallID.String())
		}
		pages++
		if report.NextCursor == "" {
			break
		}
		cursor = report.NextCursor
		require.Less(t, pages, 10, "pagination must terminate")
	}
	require.Len(t, callIDs, 3, "every call traversed exactly once")
	require.Len(t, legKeys, 1, "the exhausted leg must appear exactly once, never restart")
	require.Equal(t, c1.String()+"\x00b-1", legKeys[0])
}

func TestALegReportUnknownLegCursorRejected(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-unkleg", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerCall(t, store, accountID, aLegID, []string{"b-1"}, 10)

	// Structurally valid cursor naming a leg absent from this scope: the
	// store must reject it, not treat it as a keyset boundary.
	unknownLeg := billing.EncodeALegReportCursor("test", accountID, aLegID, callID.String(), "b-nope", callID.String())
	require.NotEmpty(t, unknownLeg)
	_, err := store.QueryALegReport(context.Background(), billing.ALegReportQuery{
		AccountID: accountID, ALegID: aLegID, Limit: 5, Cursor: unknownLeg,
	})
	require.ErrorIs(t, err, billing.ErrReportInvalid)

	// Unknown call positions stay rejected as well.
	unknownCall := billing.EncodeALegReportCursor("test", accountID, aLegID, "", "", "bc_nonexistent")
	_, err = store.QueryALegReport(context.Background(), billing.ALegReportQuery{
		AccountID: accountID, ALegID: aLegID, Limit: 5, Cursor: unknownCall,
	})
	require.ErrorIs(t, err, billing.ErrReportInvalid)
}

// plantALegMarkerFull inserts one operation snapshot row with fully
// controlled snapshots for authority-boundary regressions. Callers compute
// integrity over the exact planted values via snapshotIntegrity.
func plantALegMarkerFull(t *testing.T, store *DurableStore, opKey, accountID, kind, source, fp, integrity, currency, mode string, balBefore, balAfter, spendBefore, spendAfter, floor, limit int64, verBefore, verAfter uint64, seqStart, seqEnd uint64) {
	t.Helper()
	_, err := store.db.NewRaw(`INSERT INTO billing_operation_snapshots(operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		opKey, accountID, kind, source, fp, integrity, currency, mode, balBefore, balAfter, 0, 0, spendBefore, spendAfter, floor, limit, verBefore, verAfter, seqStart, seqEnd, time.Now().UTC()).Exec(context.Background())
	require.NoError(t, err)
}

// plantALegJournalFull seals and inserts one journal row with fully
// controlled identity, snapshots, and entries for authority-boundary
// regressions.
func plantALegJournalFull(t *testing.T, store *DurableStore, tx billing.JournalTransaction, seq uint64, balBefore, balAfter, spendBefore, spendAfter, floor, limit int64, mode string, verBefore, verAfter uint64) {
	t.Helper()
	ctx := context.Background()
	tx.BalanceBefore, tx.BalanceAfter = balBefore, balAfter
	tx.SpendableBefore, tx.SpendableAfter = spendBefore, spendAfter
	tx.CreditFloor, tx.CreditLimit = floor, limit
	tx.Mode, tx.SnapshotVersionBefore, tx.SnapshotVersionAfter = mode, verBefore, verAfter
	sealed, err := tx.Seal()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sealed.ID, sealed.AccountID, string(sealed.Book), sealed.Currency, sealed.SourceKey, sealed.SemanticFingerprint,
		sealed.TurnID, sealed.ALegID, sealed.BLegID, seq, sealed.ReversalOf, sealed.CorrectsTransactionID,
		sealed.CorrectionGroupID, sealed.OperationKind, balBefore, balAfter, 0, 0, spendBefore, spendAfter,
		floor, limit, mode, verBefore, verAfter, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	for ordinal, entry := range sealed.Entries {
		_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
			sealed.ID, ordinal, entry.LedgerAccount, string(entry.Side), entry.Amount.Currency, entry.Amount.Nano).Exec(ctx)
		require.NoError(t, err)
	}
}

func alegMarkerIntegrity(opKey, accountID, kind, source, fp, currency, mode string, balBefore, balAfter, spendBefore, spendAfter, floor, limit int64, verBefore, verAfter uint64, seqStart, seqEnd uint64) string {
	before := billing.AccountSnapshot{
		BalanceNano: balBefore, SpendableNano: spendBefore,
		CreditFloorNano: floor, CreditLimitNano: limit,
		Mode: billing.AccountMode(mode), Currency: currency, Version: verBefore,
	}
	after := billing.AccountSnapshot{
		BalanceNano: balAfter, SpendableNano: spendAfter,
		CreditFloorNano: floor, CreditLimitNano: limit,
		Mode: billing.AccountMode(mode), Currency: currency, Version: verAfter,
	}
	return snapshotIntegrity(opKey, accountID, kind, source, fp, before, after, seqStart, seqEnd)
}

// TestALegReportForeignALegCanonicalUnknown proves a canonical-looking
// same-call settlement journal from a different A-leg cannot make the
// queried A-leg known: the canonical proof is anchored to the report scope.
func TestALegReportForeignALegCanonicalUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-falg", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-foreign",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-foreign", "USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0),
		"USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: "a-leg-other", OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}, uint64(9), 10000, 9980, 10000, 9980, 0, 0, "prepaid", 1, 2)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_journal_mismatch")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportCanonicalLedgerShapeUnknown proves the canonical journal
// must carry exactly the writer pair: a financial debit plus a usage
// revenue credit. A balanced pair on the wrong plane never resolves.
func TestALegReportCanonicalLedgerShapeUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-shape", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-shape",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-shape", "USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0),
		"USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "customer_adjustment_clearing", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}, uint64(9), 10000, 9980, 10000, 9980, 0, 0, "prepaid", 1, 2)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_journal_mismatch")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportDuplicateSettlementMarkersUnknown proves duplicate
// settlement markers of one kind classify as conflicting, never known.
// Same-kind duplicates are schema-unreachable in storage, so the core
// evaluator is exercised directly as a unit.
func TestALegReportDuplicateSettlementMarkersUnknown(t *testing.T) {
	t.Parallel()
	const accountID = "aleg-c1-dup"
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	marker := func(opKey string) billing.ALegMarker {
		snap := billing.AccountSnapshot{
			BalanceNano: 100, SpendableNano: 100,
			Mode: billing.AccountPrepaid, Currency: "USD", Version: 1,
		}
		return billing.ALegMarker{
			OperationKey: opKey, AccountID: accountID, OperationKind: "customer_call_settlement",
			SourceKey: callID.String(), Fingerprint: "fp-dup",
			IntegrityFingerprint: alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-dup", "USD", "prepaid", 100, 100, 100, 100, 0, 0, 1, 1, 0, 0),
			Currency:             "USD", Mode: "prepaid",
			Before: snap, After: snap,
		}
	}
	verdict, issues, err := billing.EvaluateALegCallAuthority(billing.ALegAuthorityScope{AccountID: accountID, ALegID: "a-leg-c1", CallID: callID.String(), Currency: "USD"},
		[]billing.ALegMarker{marker(source + ":customer_call_settlement"), marker(source + ":customer_call_settlement")}, nil)
	require.NoError(t, err)
	require.Equal(t, billing.ALegCallUnknown, verdict.Status)
	require.Len(t, issues, 1)
	require.Equal(t, "customer_markers_conflicting", issues[0].Code)
}

// TestALegReportSnapshotOverflowUnknown proves snapshot delta arithmetic
// is checked: a MinInt64 to MaxInt64 transition wraps raw subtraction to
// a positive delta, but must stay unresolved, never a wrapped known charge.
func TestALegReportSnapshotOverflowUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-ovf", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	const lo = int64(-1 << 63)
	const hi = int64(1<<63 - 1)
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-wrap",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-wrap", "USD", "prepaid", lo, hi, lo, hi, 0, 0, 1, 1, 0, 0),
		"USD", "prepaid", lo, hi, lo, hi, 0, 0, 1, 1, 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 1, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 1, Currency: "USD"}},
		},
	}, uint64(9), lo, hi, lo, hi, 0, 0, "prepaid", 1, 1)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_journal_mismatch")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportCrossALegChainUnknown proves correction chains stay
// anchored to the queried A-leg: a claim and target agreeing with each
// other in another A-leg cannot establish known economics here.
func TestALegReportCrossALegChainUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-xchain", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-zero",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-zero", "USD", "prepaid", 100, 100, 100, 100, 0, 0, 1, 1, 0, 0),
		"USD", "prepaid", 100, 100, 100, 100, 0, 0, 1, 1, 0, 0)
	pair := []billing.JournalEntry{
		{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
	}
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: "tx-xa-target", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "src-tx-xa-target",
		AccountID: accountID, TurnID: callID.String(), ALegID: "a-leg-other", OperationKind: "customer_call_settlement",
		Entries: pair,
	}, uint64(9), 0, 0, 0, 0, 0, 0, "prepaid", 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: "tx-xa-claim", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "src-tx-xa-claim",
		AccountID: accountID, TurnID: callID.String(), ALegID: "a-leg-other", OperationKind: "customer_call_settlement",
		ReversalOf: "tx-xa-target", CorrectsTransactionID: "tx-xa-target", CorrectionGroupID: "tx-xa-target",
		Entries: pair,
	}, uint64(10), 0, 0, 0, 0, 0, 0, "prepaid", 0, 0)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportForeignCurrencyChainUnknown proves chain currency is
// anchored to the report scope, not just agreed between claim and target.
func TestALegReportForeignCurrencyChainUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-xcur2", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-zero",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-zero", "USD", "prepaid", 100, 100, 100, 100, 0, 0, 1, 1, 0, 0),
		"USD", "prepaid", 100, 100, 100, 100, 0, 0, 1, 1, 0, 0)
	pair := []billing.JournalEntry{
		{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "EUR"}},
		{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "EUR"}},
	}
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: "tx-xc-target", Book: billing.JournalBookFinancial, Currency: "EUR", SourceKey: "src-tx-xc-target",
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		Entries: pair,
	}, uint64(9), 0, 0, 0, 0, 0, 0, "prepaid", 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: "tx-xc-claim", Book: billing.JournalBookFinancial, Currency: "EUR", SourceKey: "src-tx-xc-claim",
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		ReversalOf: "tx-xc-target", CorrectsTransactionID: "tx-xc-target", CorrectionGroupID: "tx-xc-target",
		Entries: pair,
	}, uint64(10), 0, 0, 0, 0, 0, 0, "prepaid", 0, 0)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_correction_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportCanonicalBLegDriftUnknown proves canonical settlement
// journals carry an empty B-leg: writer settlement postings never
// attribute a B-leg, so a B-legged canonical row is drift.
func TestALegReportCanonicalBLegDriftUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-cbleg", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-bleg",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-bleg", "USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0),
		"USD", "prepaid", 10000, 9980, 10000, 9980, 0, 0, 1, 2, 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, BLegID: "b-9", OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}, uint64(9), 10000, 9980, 10000, 9980, 0, 0, "prepaid", 1, 2)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_journal_mismatch")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportMarkerJournalSnapshotMismatchUnknown pins the existing
// enforcement that a strict-digest marker over forged snapshots cannot
// borrow a disagreeing canonical journal. Guard: passes before and after.
func TestALegReportMarkerJournalSnapshotMismatchUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c1-msnap", "a-leg-c1"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedALegCustomerClosure(t, store, accountID, aLegID)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	opKey := source + ":customer_call_settlement"
	plantALegMarkerFull(t, store, opKey, accountID, "customer_call_settlement", callID.String(), "fp-five",
		alegMarkerIntegrity(opKey, accountID, "customer_call_settlement", callID.String(), "fp-five", "USD", "prepaid", 100, 95, 100, 95, 0, 0, 1, 2, 0, 0),
		"USD", "prepaid", 100, 95, 100, 95, 0, 0, 1, 2, 0, 0)
	plantALegJournalFull(t, store, billing.JournalTransaction{
		ID: source, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: billing.Money{Nano: 7, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 7, Currency: "USD"}},
		},
	}, uint64(9), 100, 93, 100, 93, 0, 0, "prepaid", 1, 2)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, report, "customer_journal_mismatch")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}
