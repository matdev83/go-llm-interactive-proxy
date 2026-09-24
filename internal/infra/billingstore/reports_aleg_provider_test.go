package billingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// Cycle 2B (Task 5.3 C2B) report regressions for trusted provider COGS
// authority. B-legs, revisions, corrections, and exclusions flow through
// the real production writers; drift fixtures use raw INSERT/UPDATE only
// for writer-rejected states. Provider economics never enters retail
// totals. This file stays independent of the customer/pass-through
// report tests; shared fixtures live in reports_aleg_customer_test.go.

func c2bProviderRevisionInput(accountID string, callID billing.BillingCallID, aLegID, bLegID, headKey string, revision uint64, amount int64, payable bool) billing.ProviderCostRevisionInput {
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", AccountID: accountID,
		ALegID: aLegID, BillingCallID: callID.String(), BLegID: bLegID,
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	if !payable {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	}
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "c2b-provider-cost", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: fmt.Sprintf("c2b-provider-charge-%s-%d", bLegID, revision),
			SourceEventKey: fmt.Sprintf("c2b-provider-charge-%s-%d", bLegID, revision), Revision: revision,
			StreamID: "c2b-provider-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{
				StoreID: subject.StoreID, ALegID: subject.ALegID,
				BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
			},
			Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(43, 0).UTC(),
			ReceivedAt: time.Unix(43, 0).UTC(), MappingRef: "c2b.provider.cost",
			Charges: []metering.ReportedCharge{{
				ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate,
				Amount: &amountDecimal, Currency: "USD", Payer: payer,
			}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "c2b-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 payable,
		IncludedLegKeys:         []string{bLegID},
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: fmt.Sprintf("%064x", revision),
		ValuationID: fmt.Sprintf("c2b-valuation-%s-%d", bLegID, revision), Cost: cost,
		Authoritative: payable, Evidence: evidence,
	}
}

func seedC2BCall(t *testing.T, store *DurableStore, accountID, aLegID string) billing.BillingCallID {
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

func seedC2BLeg(t *testing.T, store *DurableStore, callID billing.BillingCallID, aLegID, bLegID string, seq int, outcome billing.LegOutcome) billing.CallLegUsageRecord {
	t.Helper()
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: aLegID, BLegID: bLegID, AttemptSeq: seq,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: outcome, Surfaced: billing.SurfacedYes,
	}
	require.NoError(t, store.AppendCallLegUsage(context.Background(), leg))
	sealed, err := leg.Seal()
	require.NoError(t, err)
	return sealed
}

func applyC2BRevision(t *testing.T, store *DurableStore, input billing.ProviderCostRevisionInput) billing.ProviderCostRevisionResult {
	t.Helper()
	result, err := store.ApplyProviderCostRevision(context.Background(), input)
	require.NoError(t, err)
	require.True(t, result.Applied, "revision %+v must apply", result)
	return result
}

func claimC2BLegWork(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord) {
	t.Helper()
	claimed, err := store.ClaimProviderCostWorkForRevision(context.Background(), billing.ProviderCostWork{
		AccountID: accountID, CallID: callID, Leg: leg,
	})
	require.NoError(t, err)
	require.True(t, claimed, "revision-owned work must claim")
}

func findProviderLeg(t *testing.T, report billing.ALegReport, callID billing.BillingCallID, bLegID string) billing.ALegReportLeg {
	t.Helper()
	for _, row := range report.Calls {
		if row.CallID != callID {
			continue
		}
		for _, leg := range row.BLegs {
			if leg.BLegID == bLegID {
				return leg
			}
		}
	}
	t.Fatalf("leg %q of call %q not on page", bLegID, callID.String())
	return billing.ALegReportLeg{}
}

func requireProviderLegTotals(t *testing.T, report billing.ALegReport, subtotal int64, known, pending, zero, unknown int) {
	t.Helper()
	require.Equal(t, int64(subtotal), report.Provider.KnownSubtotal.Nano)
	require.Equal(t, "USD", report.Provider.KnownSubtotal.Currency)
	require.Equal(t, known, report.Provider.KnownLegs)
	require.Equal(t, pending, report.Provider.PendingLegs)
	require.Equal(t, zero, report.Provider.ZeroLegs)
	require.Equal(t, unknown, report.Provider.UnknownLegs)
}

// TestALegReportProviderNonzeroKnown proves the production
// writer-to-report join: a current nonzero provider head with its
// journal chain and fences resolves the leg known with exact
// cost/operation lineage, while retail carries only the settlement.
func TestALegReportProviderNonzeroKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-nz", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)

	pending := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderPending, findProviderLeg(t, pending, callID, "b-1").ProviderStatus)
	requireProviderLegTotals(t, pending, 0, 0, 1, 0, 0)

	input := c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true)
	posted := applyC2BRevision(t, store, input)
	require.Equal(t, int64(50), posted.CurrentAmount.Nano)
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(50), resolved.ProviderCost.Nano)
	require.Equal(t, "USD", resolved.ProviderCost.Currency)
	require.Equal(t, posted.Posting.OperationKey, resolved.ProviderOperationKey)
	require.Equal(t, posted.Posting.Transaction.ID, resolved.ProviderTransactionID)
	require.NotEmpty(t, resolved.ProviderOperationKey)
	require.NotEmpty(t, resolved.ProviderTransactionID)
	require.Empty(t, resolved.ZeroBasis)
	requireProviderLegTotals(t, report, 50, 1, 0, 0, 0)
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano,
		"provider COGS must never enter retail totals")

	// Contributions mirror the leg lineage exactly.
	var contribution *billing.ALegReportContribution
	for i := range report.Contributions {
		if report.Contributions[i].BLegID == "b-1" {
			contribution = &report.Contributions[i]
		}
	}
	require.NotNil(t, contribution)
	require.Equal(t, billing.ALegProviderKnown, contribution.ProviderStatus)
	require.Equal(t, resolved.ProviderCost, contribution.ProviderCost)
	require.Equal(t, resolved.ProviderOperationKey, contribution.ProviderOperationKey)
	require.Equal(t, resolved.ProviderTransactionID, contribution.ProviderTransactionID)

	var storedWork string
	require.NoError(t, store.db.NewRaw(`SELECT status FROM provider_cost_work WHERE usage_leg_key = ?`, leg.Key).Scan(ctx, &storedWork))
	require.Equal(t, "processed", storedWork)
}

// TestALegReportProviderCorrectionAdvancesCurrent proves a later
// provider revision changes current COGS in a later query naturally:
// earlier immutable rows remain while the head, fences, and totals
// advance with no A-leg finality trigger.
func TestALegReportProviderCorrectionAdvancesCurrent(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2b-corr", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)

	first := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, int64(50), findProviderLeg(t, first, callID, "b-1").ProviderCost.Nano)
	requireProviderLegTotals(t, first, 50, 1, 0, 0, 0)

	corrected := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 2, 40, true))
	require.Equal(t, int64(-10), corrected.Delta.Nano)

	second := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, second, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(40), resolved.ProviderCost.Nano)
	require.Equal(t, corrected.Posting.OperationKey, resolved.ProviderOperationKey)
	require.Equal(t, corrected.Posting.Transaction.ID, resolved.ProviderTransactionID)
	requireProviderLegTotals(t, second, 40, 1, 0, 0, 0)

	var journals int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM journal_transactions WHERE account_id = ? AND operation_kind = 'provider_call_cogs'`, accountID).Scan(context.Background(), &journals))
	require.Equal(t, 2, journals, "earlier immutable rows must remain")
	head, err := store.GetProviderCostHead(context.Background(), accountID, callID, "head-b-1")
	require.NoError(t, err)
	require.Equal(t, uint64(2), head.EvidenceRevision)
	require.Equal(t, uint64(2), head.HeadVersion)
	require.Equal(t, uint64(2), head.Fence)
}

// TestALegReportProviderAllAttemptsContribute proves operator COGS
// covers every attributable B-leg — failed, retry, and loser — never
// just the surfaced winner.
func TestALegReportProviderAllAttemptsContribute(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2b-all", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	legs := map[string]billing.CallLegUsageRecord{
		"b-failed": seedC2BLeg(t, store, callID, aLegID, "b-failed", 1, billing.LegOutcomeFailed),
		"b-retry":  seedC2BLeg(t, store, callID, aLegID, "b-retry", 2, billing.LegOutcomeLoser),
		"b-winner": seedC2BLeg(t, store, callID, aLegID, "b-winner", 3, billing.LegOutcomeWinner),
	}
	amounts := map[string]int64{"b-failed": 3, "b-retry": 5, "b-winner": 7}
	for bLegID, amount := range amounts {
		applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, bLegID, "head-"+bLegID, 1, amount, true))
		claimC2BLegWork(t, store, accountID, callID, legs[bLegID])
	}
	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	for bLegID, amount := range amounts {
		resolved := findProviderLeg(t, report, callID, bLegID)
		require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus, "leg %q must contribute", bLegID)
		require.Equal(t, amount, resolved.ProviderCost.Nano)
	}
	requireProviderLegTotals(t, report, 15, 3, 0, 0, 0)
}

// TestALegReportProviderRecordedZero proves a payer correction that
// reverses prior COGS to zero reports known_zero with the exact
// recorded basis instead of pending or a faked nonzero.
func TestALegReportProviderRecordedZero(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2b-zero", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 10, true))
	reversed := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 2, 10, false))
	require.Equal(t, int64(-10), reversed.Delta.Nano)
	require.Equal(t, int64(0), reversed.CurrentAmount.Nano)
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnownZero, resolved.ProviderStatus)
	require.Equal(t, billing.ALegProviderZeroRecorded, resolved.ZeroBasis)
	requireProviderLegTotals(t, report, 0, 0, 0, 1, 0)
}

// TestALegReportProviderExcludedZero proves a recorded non-payable
// exclusion reports known_zero — but only after the pending work
// precedence clears: with the work still pending the leg stays pending.
func TestALegReportProviderExcludedZero(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2b-excl", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	excluded, err := store.ApplyProviderCostRevision(context.Background(),
		c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 12, false))
	require.NoError(t, err)
	require.True(t, excluded.Ignored)

	early := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderPending, findProviderLeg(t, early, callID, "b-1").ProviderStatus,
		"pending work takes precedence even over a recorded exclusion")
	requireProviderLegTotals(t, early, 0, 0, 1, 0, 0)

	claimC2BLegWork(t, store, accountID, callID, leg)
	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnownZero, resolved.ProviderStatus)
	require.Equal(t, billing.ALegProviderZeroExcluded, resolved.ZeroBasis)
	requireProviderLegTotals(t, report, 0, 0, 0, 1, 0)
}

// TestALegReportProviderNeverStartedZero proves explicit zero only for
// a truly evidence-free non-payable leg: an appended never-started leg
// still carries pending work and stays pending, while a work-free
// never-started row with no provider evidence reports known_zero with
// the exact basis.
func TestALegReportProviderNeverStartedZero(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-ns", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	seedC2BLeg(t, store, callID, aLegID, "b-appended", 1, billing.LegOutcomeNeverStarted)

	sealed, err := billing.CallLegUsageRecord{
		CallID: callID, ALegID: aLegID, BLegID: "b-quiet", AttemptSeq: 2,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeNeverStarted, Surfaced: billing.SurfacedNo,
	}.Seal()
	require.NoError(t, err)
	payload, err := json.Marshal(sealed)
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = store.db.NewRaw(`INSERT INTO usage_leg_records(usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sealed.Key, sealed.Fingerprint, callID.String(), aLegID, "b-quiet",
		"ok", "provider-a", "model-a", now, now, string(billing.LegOutcomeNeverStarted), string(billing.SurfacedNo), string(payload), now).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderPending, findProviderLeg(t, report, callID, "b-appended").ProviderStatus,
		"pending work takes precedence over the never-started basis")
	quiet := findProviderLeg(t, report, callID, "b-quiet")
	require.Equal(t, billing.ALegProviderKnownZero, quiet.ProviderStatus)
	require.Equal(t, billing.ALegProviderZeroNeverStarted, quiet.ZeroBasis)
	requireProviderLegTotals(t, report, 0, 0, 1, 1, 0)
}

// TestALegReportProviderPendingRevisionOverHead proves an in-flight
// newer revision keeps the leg pending even though an older head is
// already recorded.
func TestALegReportProviderPendingRevisionOverHead(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-revpend", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	settled := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderKnown, findProviderLeg(t, settled, callID, "b-1").ProviderStatus)

	_, err := store.db.NewRaw(`INSERT INTO billing_economic_revision_work_state(store_id, work_id, work_version, queue, head_key, status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error, fence, completed_at_unix, created_at_unix, updated_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"test", "c2b-pending-work", 1, "provider", "head-b-1", "pending", 0, 0, "", 0, "", 0, 0, time.Now().UTC().UnixNano(), time.Now().UTC().UnixNano()).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderPending, findProviderLeg(t, report, callID, "b-1").ProviderStatus,
		"an in-flight revision takes precedence over the older head")
	requireALegIssue(t, report, "provider_cost_pending")
	requireProviderLegTotals(t, report, 0, 0, 1, 0, 0)
}

// plantC2BProviderSnapshot inserts one canonical provider operation
// snapshot with recomputed integrity over self-consistent balances.
func plantC2BProviderSnapshot(t *testing.T, store *DurableStore, accountID, operationKey, fp string, balBefore, balAfter int64, seq uint64) {
	t.Helper()
	before := billing.AccountSnapshot{BalanceNano: balBefore, SpendableNano: balBefore, Mode: billing.AccountPrepaid, Currency: "USD", Version: 3}
	after := billing.AccountSnapshot{BalanceNano: balAfter, SpendableNano: balAfter, Mode: billing.AccountPrepaid, Currency: "USD", Version: 3}
	integrity := snapshotIntegrity(operationKey, accountID, "provider_call_cogs", operationKey, fp, before, after, seq, seq)
	_, err := store.db.NewRaw(`INSERT INTO billing_operation_snapshots(operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		operationKey, accountID, "provider_call_cogs", operationKey, fp, integrity,
		"USD", "prepaid", balBefore, balAfter, 0, 0, balBefore, balAfter, 0, 0, 3, 3, seq, seq, time.Now().UTC()).Exec(context.Background())
	require.NoError(t, err)
}

// TestALegReportProviderStrayJournalUnknown proves a provider journal
// without a head stays unresolved instead of contributing.
func TestALegReportProviderStrayJournalUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-stray", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	sealed, err := billing.JournalTransaction{
		ID: "tx-stray-pv", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "tx-stray-pv",
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, BLegID: "b-1",
		OperationKind: "provider_call_cogs", CorrectionGroupID: "head-ghost",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "inference_provider_cogs", Side: billing.JournalDebit, Amount: billing.Money{Nano: 9, Currency: "USD"}},
			{LedgerAccount: "provider_payable_clearing", Side: billing.JournalCredit, Amount: billing.Money{Nano: 9, Currency: "USD"}},
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
		"tx-stray-pv", accountID, "financial", "USD", "tx-stray-pv", sealed.SemanticFingerprint,
		callID.String(), aLegID, "b-1", 7, "", "", "head-ghost", "provider_call_cogs",
		0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-stray-pv", 0, "inference_provider_cogs", "debit", "USD", 9).Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-stray-pv", 1, "provider_payable_clearing", "credit", "USD", 9).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderHeadAmountMismatchUnknown proves a head whose
// amount disagrees with its journal chain stays unresolved.
func TestALegReportProviderHeadAmountMismatchUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-mismatch", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_heads SET amount_nano = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		51, "test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderStaleFenceUnknown proves a posting fence
// behind the head revision stays unresolved.
func TestALegReportProviderStaleFenceUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-fence", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 2, 40, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_posting_fences SET evidence_revision = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		1, "test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderForeignExecutionOwnerUnknown proves an
// execution fence owned by another head stays unresolved.
func TestALegReportProviderForeignExecutionOwnerUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-exec", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	legKey, err := billing.CallLegUsageKey(callID, "b-1")
	require.NoError(t, err)
	_, err = store.db.NewRaw(`UPDATE billing_provider_cost_execution_fences SET owner_head_key = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND execution_lineage_key = ?`,
		"head-other", "test", accountID, callID.String(), legKey).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderBranchedChainUnknown proves two journals
// correcting the same prior transaction are ambiguous and stay
// unresolved even when each validates alone.
func TestALegReportProviderBranchedChainUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-branch", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	posted := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	priorTx := posted.Posting.Transaction.ID
	require.NotEmpty(t, priorTx)
	branch, err := billing.JournalTransaction{
		ID: "tx-branch-pv", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "tx-branch-pv",
		AccountID: accountID, TurnID: callID.String(), ALegID: aLegID, BLegID: "b-1",
		AccountSequence: 9, ReversalOf: priorTx, CorrectsTransactionID: priorTx,
		OperationKind: "provider_call_cogs",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "inference_provider_cogs", Side: billing.JournalDebit, Amount: billing.Money{Nano: 5, Currency: "USD"}},
			{LedgerAccount: "provider_payable_clearing", Side: billing.JournalCredit, Amount: billing.Money{Nano: 5, Currency: "USD"}},
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
		"tx-branch-pv", accountID, "financial", "USD", "tx-branch-pv", branch.SemanticFingerprint,
		callID.String(), aLegID, "b-1", 9, priorTx, priorTx, "", "provider_call_cogs",
		0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-branch-pv", 0, "inference_provider_cogs", "debit", "USD", 5).Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		"tx-branch-pv", 1, "provider_payable_clearing", "credit", "USD", 5).Exec(ctx)
	require.NoError(t, err)
	plantC2BProviderSnapshot(t, store, accountID, "tx-branch-pv", "fp-branch", 0, 0, 9)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderBookConflictUnknown proves a provider journal
// outside the financial book stays unresolved. Journal rows are
// immutable, so the conflicting journal is planted raw the way writers
// reject it: the evaluator fails it closed on the book before any
// fingerprint or snapshot rule.
func TestALegReportProviderBookConflictUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-book", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	posted := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	priorTx := posted.Posting.Transaction.ID
	require.NotEmpty(t, priorTx)
	_, err := store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"tx-book-pv", accountID, "authorization", "USD", "tx-book-pv", "fp-bogus",
		callID.String(), aLegID, "b-1", 9, priorTx, priorTx, "head-b-1", "provider_call_cogs",
		0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderSubjectCallMismatchUnknown proves a head whose
// subject names another call stays unresolved.
func TestALegReportProviderSubjectCallMismatchUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2b-subject", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_heads SET subject_json = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		`{"kind":"b_leg","store_id":"test","account_id":"`+accountID+`","a_leg_id":"`+aLegID+`","billing_call_id":"bc_00000000000000000000000000000099","b_leg_id":"b-1"}`,
		"test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderSameAmountRevisionKnown proves a same-amount
// later revision resolves known with the new current operation and the
// last monetary transaction separately: no journal is fabricated for
// the zero delta while the immutable snapshot proves the new operation.
func TestALegReportProviderSameAmountRevisionKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r1-same", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	first := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	second := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 2, 50, true))
	require.Equal(t, int64(0), second.Delta.Nano)
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(50), resolved.ProviderCost.Nano)
	require.Equal(t, second.Posting.OperationKey, resolved.ProviderOperationKey)
	require.Equal(t, first.Posting.Transaction.ID, resolved.ProviderTransactionID,
		"the last monetary transaction stays put across a zero delta")
	require.NotEqual(t, first.Posting.OperationKey, resolved.ProviderOperationKey)
	requireProviderLegTotals(t, report, 50, 1, 0, 0, 0)
}

// TestALegReportProviderSubjectChildDriftUnknown proves durable head
// columns naming a provider-charge child while the decoded subject
// stays a B-leg stay unresolved: columns and JSON must agree exactly.
func TestALegReportProviderSubjectChildDriftUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r1-childdrift", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_heads SET subject_kind = ?, subject_id = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		"provider_charge", "charge-7", "test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderStaleExecutionOwnerUnknown proves a stale
// execution owner revision stays unresolved even when head, fence, and
// chain agree.
func TestALegReportProviderStaleExecutionOwnerUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r1-staleowner", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 2, 40, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	legKey, err := billing.CallLegUsageKey(callID, "b-1")
	require.NoError(t, err)
	_, err = store.db.NewRaw(`UPDATE billing_provider_cost_execution_fences SET owner_revision = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND execution_lineage_key = ?`,
		1, "test", accountID, callID.String(), legKey).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderSubjectAncestryUnknown proves a head whose
// subject omits A-leg ancestry stays unresolved.
func TestALegReportProviderSubjectAncestryUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r1-ancestry", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_heads SET subject_json = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		`{"kind":"b_leg","store_id":"test","account_id":"`+accountID+`","billing_call_id":"`+callID.String()+`","b_leg_id":"b-1"}`,
		"test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderSubjectKindDriftUnknown proves durable head
// columns disagreeing with the decoded subject stay unresolved.
func TestALegReportProviderSubjectKindDriftUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r1-kinddrift", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_heads SET subject_kind = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		"provider_charge", "test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderFirstZeroKnown proves a first writer-recorded
// payable zero — head, fences, and operation snapshot with no monetary
// journal — reports known_zero with the recorded basis.
func TestALegReportProviderFirstZeroKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r1-firstzero", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	first := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 0, true))
	require.Equal(t, int64(0), first.CurrentAmount.Nano)
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnownZero, resolved.ProviderStatus)
	require.Equal(t, billing.ALegProviderZeroRecorded, resolved.ZeroBasis)
	require.Equal(t, first.Posting.OperationKey, resolved.ProviderOperationKey)
	require.Empty(t, resolved.ProviderTransactionID, "no transaction is fabricated for a no-journal revision")
	requireProviderLegTotals(t, report, 0, 0, 0, 1, 0)
}

// TestALegReportProviderRootMismatchUnknown proves a head whose
// original transaction is not the canonical root stays unresolved.
func TestALegReportProviderRootMismatchUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r1-root", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_heads SET original_transaction_id = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		"tx-other", "test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderLastTransactionMismatchUnknown proves a head
// whose last transaction is not the chain terminal stays unresolved,
// even when the posting fence is drifted to agree with the head.
func TestALegReportProviderLastTransactionMismatchUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r1-lasttx", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_heads SET last_transaction_id = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		"tx-other", "test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`UPDATE billing_provider_cost_posting_fences SET last_transaction_id = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		"tx-other", "test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegProviderUnknown, findProviderLeg(t, report, callID, "b-1").ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderExclusionPromotionKnown proves a payable
// promotion after a recorded exclusion resolves known with the new
// current operation bound to the promotion journal.
func TestALegReportProviderExclusionPromotionKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r1-promotion", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	excluded, err := store.ApplyProviderCostRevision(context.Background(),
		c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 12, false))
	require.NoError(t, err)
	require.True(t, excluded.Ignored)
	claimC2BLegWork(t, store, accountID, callID, leg)
	promoted := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 2, 5, true))
	require.Equal(t, int64(5), promoted.CurrentAmount.Nano)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(5), resolved.ProviderCost.Nano)
	require.Equal(t, promoted.Posting.OperationKey, resolved.ProviderOperationKey)
	require.Equal(t, promoted.Posting.Transaction.ID, resolved.ProviderTransactionID)
	requireProviderLegTotals(t, report, 5, 1, 0, 0, 0)
}

// TestALegReportProviderBoundedFactLoading proves provider fact loads
// (legs, heads, fences, work, revision state, snapshots) stream in
// chunk-bounded statements with no scope-wide IN list and no N+1 while
// every leg still resolves.
func TestALegReportProviderBoundedFactLoading(t *testing.T) {
	// No t.Parallel: this test mutates the package chunk size; sequential
	// execution keeps the override and its Cleanup restore deterministic.
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2b-bound", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	for i := range 3 {
		callID := seedC2BCall(t, store, accountID, aLegID)
		for j, bLegID := range []string{"b-1", "b-2"} {
			leg := seedC2BLeg(t, store, callID, aLegID, bLegID, j+1, billing.LegOutcomeWinner)
			applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, bLegID, "head-"+bLegID, 1, int64(10*(i+1)), true))
			claimC2BLegWork(t, store, accountID, callID, leg)
		}
	}
	recorder := &alegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	const chunk = 2
	oldChunk := alegReportScopeChunkSize
	alegReportScopeChunkSize = chunk
	t.Cleanup(func() { alegReportScopeChunkSize = oldChunk })

	report := queryALegCustomerReport(t, store, accountID, aLegID, 10, "")
	require.Equal(t, 3, report.CallCount)
	require.Equal(t, 6, report.Provider.KnownLegs)
	require.Equal(t, int64(120), report.Provider.KnownSubtotal.Nano)
	for _, row := range report.Calls {
		for _, leg := range row.BLegs {
			require.Equal(t, billing.ALegProviderKnown, leg.ProviderStatus)
		}
	}
	require.LessOrEqual(t, recorder.maxInListLenMatching("billing_provider_cost_heads"), chunk,
		"head loads must stay chunk-bounded")
	require.LessOrEqual(t, recorder.maxInListLenMatching("billing_provider_cost_posting_fences"), chunk,
		"posting fence loads must stay chunk-bounded")
	require.LessOrEqual(t, recorder.maxInListLenMatching("billing_provider_cost_execution_fences"), chunk,
		"execution fence loads must stay chunk-bounded")
	require.LessOrEqual(t, recorder.maxInListLenMatching("provider_cost_work"), chunk,
		"work loads must stay chunk-bounded")
	require.LessOrEqual(t, recorder.maxInListLenMatching("billing_economic_revision_work_state"), chunk,
		"revision state loads must stay chunk-bounded")
	require.LessOrEqual(t, recorder.maxInListLenMatching("l.outcome"), chunk,
		"leg enumeration must stay chunk-bounded")
	require.GreaterOrEqual(t, recorder.countMatching("billing_provider_cost_heads"), 2,
		"head loads must stream in chunks, got %d head queries", recorder.countMatching("billing_provider_cost_heads"))
}

// TestALegProviderHeadLoaderBoundedProves the per-subject cap+1
// window directly: three heads for one subject retain exactly the
// first by head key with the call flagged over-cap, and a single
// head loads cleanly. Raw rows exercise loader mechanics;
// authority behavior stays covered by writer-path tests.
func TestALegProviderHeadLoaderBounded(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID = "aleg-loader-bound"
	seedALegCustomerAccount(t, store, accountID)
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	plantProviderHeadRow := func(headKey string) {
		t.Helper()
		_, err := store.db.NewRaw(`INSERT INTO billing_provider_cost_heads(
			store_id, account_id, call_id, head_key, subject_kind, subject_id, subject_json,
			evidence_revision, input_set_hash, valuation_id, amount_nano, currency, head_version, fence,
			last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"test", accountID, callID.String(), headKey, "b_leg", "b-1", "{}",
			1, strings.Repeat("a", 64), "val-1", 10, "USD", 1, 1, "op-1", "tx-1", "tx-1", 1, 1).Exec(ctx)
		require.NoError(t, err)
	}
	plantProviderHeadRow("head-c")
	plantProviderHeadRow("head-a")
	plantProviderHeadRow("head-b")

	heads, overCap, err := loadALegProviderHeadsTx(ctx, store.db, "test", accountID, []string{callID.String()})
	require.NoError(t, err)
	require.True(t, overCap[callID.String()], "three same-subject heads must flag overflow")
	require.Len(t, heads[callID.String()], 1, "only cap rows may be retained")
	require.Equal(t, "head-a", heads[callID.String()][0].HeadKey,
		"retention must follow deterministic head-key order")

	fences, fenceOverCap, err := loadALegProviderPostingFencesTx(ctx, store.db, "test", accountID, []string{callID.String()})
	require.NoError(t, err)
	require.Empty(t, fences[callID.String()])
	require.Empty(t, fenceOverCap)
}
