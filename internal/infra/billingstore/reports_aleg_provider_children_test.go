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

// Cycle 2R2 report regressions for additive multi-provider-charge
// children. Each child flows through the real production revision
// writer with its own head, posting fence, journals, and snapshots;
// the shared execution gate names the most recently applied child.
// This file stays independent of the aggregate-leg provider tests;
// shared fixtures live in reports_aleg_customer_test.go and
// reports_aleg_provider_test.go.

func c2r2ChildRevisionInput(t *testing.T, accountID string, callID billing.BillingCallID, aLegID, bLegID, chargeID, headKey string, revision uint64, amount int64, payable bool) billing.ProviderCostRevisionInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectProviderCharge, StoreID: "test", AccountID: accountID,
		ALegID: aLegID, BillingCallID: callID.String(), BLegID: bLegID,
		ProviderAccountKey: "provider-acct", ProviderChargeID: chargeID,
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	if !payable {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	}
	legKey, err := billing.CallLegUsageKey(callID, bLegID)
	require.NoError(t, err)
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "c2r2-provider-charge", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: fmt.Sprintf("c2r2-charge-%s-%d", chargeID, revision),
			SourceEventKey: fmt.Sprintf("c2r2-charge-%s-%d", chargeID, revision), Revision: revision,
			StreamID: "c2r2-charge-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID,
				BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
				ProviderAccountKey: subject.ProviderAccountKey, ProviderChargeID: chargeID},
			Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(43, 0).UTC(),
			ReceivedAt: time.Unix(43, 0).UTC(), MappingRef: "c2r2.provider.charge",
			Charges: []metering.ReportedCharge{{ChargeItemID: "charge-" + chargeID, Kind: metering.ChargeKindAggregate,
				Amount: &amountDecimal, Currency: "USD", Payer: payer}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "c2r2-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 payable,
		IncludedLegKeys:         []string{legKey},
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: fmt.Sprintf("%064x-%s", revision, chargeID)[:64],
		ValuationID: fmt.Sprintf("c2r2-valuation-%s-%d", chargeID, revision), Cost: cost,
		Authoritative: payable, Evidence: evidence,
	}
}

func findProviderChild(t *testing.T, leg billing.ALegReportLeg, chargeID string) billing.ALegProviderChild {
	t.Helper()
	for _, child := range leg.ProviderChildren {
		if child.ChargeID == chargeID {
			return child
		}
	}
	t.Fatalf("child %q not in leg lineage", chargeID)
	return billing.ALegProviderChild{}
}

// TestALegReportProviderTwoChildrenKnown proves two additive
// provider-charge children on one B-leg resolve known with the
// checked sum and exact per-child lineage when the second child
// arrives after the first.
func TestALegReportProviderTwoChildrenKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r2-2child", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)

	first := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-charge-a", 1, 30, true))
	second := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-b", "head-charge-b", 1, 20, true))
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(50), resolved.ProviderCost.Nano)
	require.Equal(t, second.Posting.OperationKey, resolved.ProviderOperationKey,
		"aggregate lineage tracks the most recently applied child")
	require.Equal(t, second.Posting.Transaction.ID, resolved.ProviderTransactionID)
	require.Len(t, resolved.ProviderChildren, 2)
	childA := findProviderChild(t, resolved, "charge-a")
	require.Equal(t, billing.ALegProviderKnown, childA.Status)
	require.Equal(t, int64(30), childA.Amount.Nano)
	require.Equal(t, first.Posting.OperationKey, childA.OperationKey)
	require.Equal(t, first.Posting.Transaction.ID, childA.TransactionID)
	childB := findProviderChild(t, resolved, "charge-b")
	require.Equal(t, billing.ALegProviderKnown, childB.Status)
	require.Equal(t, int64(20), childB.Amount.Nano)
	require.Equal(t, second.Posting.OperationKey, childB.OperationKey)
	requireProviderLegTotals(t, report, 50, 1, 0, 0, 0)
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportProviderChildCorrections proves per-child revisions,
// including a same-amount revision and a zero correction, preserve
// independent heads and histories while the shared gate names the
// latest applied child.
func TestALegReportProviderChildCorrections(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r2-childcorr", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-charge-a", 1, 30, true))
	correctedA := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-charge-a", 2, 25, true))
	require.Equal(t, int64(-5), correctedA.Delta.Nano)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-b", "head-charge-b", 1, 20, true))
	sameB := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-b", "head-charge-b", 2, 20, true))
	require.Equal(t, int64(0), sameB.Delta.Nano)
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(45), resolved.ProviderCost.Nano)
	require.Len(t, resolved.ProviderChildren, 2)
	childA := findProviderChild(t, resolved, "charge-a")
	require.Equal(t, int64(25), childA.Amount.Nano)
	require.Equal(t, correctedA.Posting.OperationKey, childA.OperationKey)
	childB := findProviderChild(t, resolved, "charge-b")
	require.Equal(t, int64(20), childB.Amount.Nano)
	require.Equal(t, sameB.Posting.OperationKey, childB.OperationKey,
		"same-amount revisions advance the current operation without new money")
	require.Equal(t, sameB.Posting.OperationKey, resolved.ProviderOperationKey,
		"the shared gate names the latest applied child")
	requireProviderLegTotals(t, report, 45, 1, 0, 0, 0)

	headA, err := store.GetProviderCostHead(context.Background(), accountID, callID, "head-charge-a")
	require.NoError(t, err)
	require.Equal(t, uint64(2), headA.EvidenceRevision)
	headB, err := store.GetProviderCostHead(context.Background(), accountID, callID, "head-charge-b")
	require.NoError(t, err)
	require.Equal(t, uint64(2), headB.EvidenceRevision)
}

// TestALegReportProviderPartialChildPending proves a leg with one
// known and one pending child exposes no partial subtotal: the leg
// stays pending with an explicit issue.
func TestALegReportProviderPartialChildPending(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r2-partial", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-charge-a", 1, 30, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-b", "head-charge-b", 1, 20, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`INSERT INTO billing_economic_revision_work_state(store_id, work_id, work_version, queue, head_key, status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error, fence, completed_at_unix, created_at_unix, updated_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"test", "c2r2-partial-work", 1, "provider", "head-charge-b", "pending", 0, 0, "", 0, "", 0, 0, time.Now().UTC().UnixNano(), time.Now().UTC().UnixNano()).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderPending, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren, "no partial lineage on a pending leg")
	requireALegIssue(t, report, "provider_cost_pending")
	requireProviderLegTotals(t, report, 0, 0, 1, 0, 0)
}

// TestALegReportProviderGateOwnerMissingUnknown proves a shared gate
// naming a head outside the evaluated child set stays unresolved.
func TestALegReportProviderGateOwnerMissingUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r2-gateowner", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-charge-a", 1, 30, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-b", "head-charge-b", 1, 20, true))
	claimC2BLegWork(t, store, accountID, callID, leg)
	legKey, err := billing.CallLegUsageKey(callID, "b-1")
	require.NoError(t, err)
	_, err = store.db.NewRaw(`UPDATE billing_provider_cost_execution_fences SET owner_head_key = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND execution_lineage_key = ?`,
		"head-ghost", "test", accountID, callID.String(), legKey).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderOneZeroChildKnown proves a single child
// reversed to zero reports known_zero with the recorded basis, exact
// current operation, and ZeroLegs incremented while KnownLegs stays
// put and no retail is touched.
func TestALegReportProviderOneZeroChildKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r2-onezero", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-charge-a", 1, 10, true))
	reversed := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-charge-a", 2, 10, false))
	require.Equal(t, int64(-10), reversed.Delta.Nano)
	require.Equal(t, int64(0), reversed.CurrentAmount.Nano)
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnownZero, resolved.ProviderStatus)
	require.Equal(t, billing.ALegProviderZeroAllChildrenZero, resolved.ZeroBasis)
	require.Equal(t, reversed.Posting.OperationKey, resolved.ProviderOperationKey)
	require.Equal(t, reversed.Posting.Transaction.ID, resolved.ProviderTransactionID)
	require.Len(t, resolved.ProviderChildren, 1)
	require.Equal(t, "charge-a", resolved.ProviderChildren[0].ChargeID)
	require.Equal(t, billing.ALegProviderKnownZero, resolved.ProviderChildren[0].Status)
	require.Equal(t, billing.ALegProviderZeroRecorded, resolved.ProviderChildren[0].ZeroBasis,
		"the child entry retains its exact recorded basis")
	requireProviderLegTotals(t, report, 0, 0, 0, 1, 0)
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportProviderMultiZeroChildrenKnownZero proves every
// payable child proven zero aggregates to known_zero with the
// deterministic all-children basis.
func TestALegReportProviderMultiZeroChildrenKnownZero(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r2-multizero", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	for _, chargeID := range []string{"charge-a", "charge-b"} {
		applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", chargeID, "head-"+chargeID, 1, 10, true))
		applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", chargeID, "head-"+chargeID, 2, 10, false))
	}
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnownZero, resolved.ProviderStatus)
	require.Equal(t, billing.ALegProviderZeroAllChildrenZero, resolved.ZeroBasis)
	require.Len(t, resolved.ProviderChildren, 2)
	for _, child := range resolved.ProviderChildren {
		require.Equal(t, billing.ALegProviderKnownZero, child.Status)
		require.Zero(t, child.Amount.Nano)
	}
	requireProviderLegTotals(t, report, 0, 0, 0, 1, 0)
}

// TestALegReportProviderMixedZeroNonzeroKnown proves mixed zero plus
// nonzero children resolve known with the correct sum and
// deterministic lineage.
func TestALegReportProviderMixedZeroNonzeroKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r2-mixed", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-charge-a", 1, 25, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-b", "head-charge-b", 1, 10, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-b", "head-charge-b", 2, 10, false))
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(25), resolved.ProviderCost.Nano)
	require.Len(t, resolved.ProviderChildren, 2)
	require.Equal(t, "charge-a", resolved.ProviderChildren[0].ChargeID)
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderChildren[0].Status)
	require.Equal(t, "charge-b", resolved.ProviderChildren[1].ChargeID)
	require.Equal(t, billing.ALegProviderKnownZero, resolved.ProviderChildren[1].Status)
	requireProviderLegTotals(t, report, 25, 1, 0, 0, 0)
}

// TestALegReportProviderDuplicateChildUnknown proves two heads sharing
// one charge identity stay unresolved instead of attributing either:
// the second head key rotates the lineage while the first head's
// history remains, so latest-wins would silently drop lineage. The
// per-subject loader window detects the overflow first and reports
// the fanout issue; the core duplicate rule covers the same shape
// below the loader bound.
func TestALegReportProviderDuplicateChildUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r2-dupchild", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-one", 1, 30, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-a", "head-two", 2, 30, true))
	claimC2BLegWork(t, store, accountID, callID, leg)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus,
		"rotated head keys for one charge never resolve by latest-wins")
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// TestALegReportProviderChildrenOnLoserLegs proves multiple children
// on failed and loser B-legs contribute independently across legs
// with no surfaced/winner selection.
func TestALegReportProviderChildrenOnLoserLegs(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r2-losers", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	failed := seedC2BLeg(t, store, callID, aLegID, "b-failed", 1, billing.LegOutcomeFailed)
	winner := seedC2BLeg(t, store, callID, aLegID, "b-winner", 2, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-failed", "charge-f1", "head-f1", 1, 3, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-failed", "charge-f2", "head-f2", 1, 5, true))
	claimC2BLegWork(t, store, accountID, callID, failed)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-winner", "charge-w", "head-w", 1, 7, true))
	claimC2BLegWork(t, store, accountID, callID, winner)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolvedFailed := findProviderLeg(t, report, callID, "b-failed")
	require.Equal(t, billing.ALegProviderKnown, resolvedFailed.ProviderStatus)
	require.Equal(t, int64(8), resolvedFailed.ProviderCost.Nano)
	require.Len(t, resolvedFailed.ProviderChildren, 2)
	resolvedWinner := findProviderLeg(t, report, callID, "b-winner")
	require.Equal(t, billing.ALegProviderKnown, resolvedWinner.ProviderStatus)
	require.Equal(t, int64(7), resolvedWinner.ProviderCost.Nano)
	require.Len(t, resolvedWinner.ProviderChildren, 1)
	requireProviderLegTotals(t, report, 15, 2, 0, 0, 0)
}

// TestALegReportProviderChildrenCapUnknown proves child fanout beyond
// the per-leg bound resolves unknown with a loader fanout issue
// rather than a partial sum or a core-level unresolved verdict: nine
// distinct children exceed the eight-child per-leg window, so the leg
// contributes nothing and no derived revision state is consulted for
// the discarded set.
func TestALegReportProviderChildrenCapUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r2-childcap", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	for i := 0; i < 9; i++ {
		chargeID := fmt.Sprintf("charge-%d", i)
		applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", chargeID, "head-"+chargeID, 1, 1, true))
	}
	claimC2BLegWork(t, store, accountID, callID, leg)
	// A pending revision for one discarded head must not pend the leg:
	// over-cap legs load no derived facts.
	_, err := store.db.NewRaw(`INSERT INTO billing_economic_revision_work_state(store_id, work_id, work_version, queue, head_key, status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error, fence, completed_at_unix, created_at_unix, updated_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"test", "c2r2-childcap-work", 1, "provider", "head-charge-0", "pending", 0, 0, "", 0, "", 0, 0, time.Now().UTC().UnixNano(), time.Now().UTC().UnixNano()).Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	for _, issue := range report.Issues {
		require.NotEqual(t, "provider_cost_pending", issue.Code,
			"discarded over-cap heads must not consult derived revision state")
	}
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}

// countHeadsByBLeg groups loaded head rows by their decoded owning
// B-leg. It measures SQL materialization per leg: the per-leg window
// must bound every leg to cap+1 rows before Scan.
func countHeadsByBLeg(t *testing.T, rows []providerCostHeadRow) map[string]int {
	t.Helper()
	out := make(map[string]int)
	for _, row := range rows {
		var subject metering.SubjectRef
		require.NoError(t, json.Unmarshal([]byte(row.SubjectJSON), &subject))
		out[subject.BLegID]++
	}
	return out
}

// countFencesByLeg groups loaded posting-fence rows by the B-leg
// extracted from their lineage (`call:b-leg[:provider-charge:id]`).
// Lineages outside the call scope count under drift and must stay
// bounded the same way.
func countFencesByLeg(callID billing.BillingCallID, rows []providerCostPostingFenceRow) map[string]int {
	out := make(map[string]int)
	prefix := callID.String() + ":"
	for _, row := range rows {
		bleg := "drift"
		if rest, ok := strings.CutPrefix(row.LineageKey, prefix); ok {
			if cut, _, hasChild := strings.Cut(rest, ":"); hasChild {
				bleg = cut
			} else {
				bleg = rest
			}
		}
		out[bleg]++
	}
	return out
}

// TestALegReportProviderLoaderLegBoundFifty proves the SQL per-leg
// bound directly: 50 distinct valid children on one leg materialize
// at most 9 head and fence rows for that leg (8 + probe), while a
// neighbor call reusing charge IDs across legs and mixing subject
// kinds loads completely and resolves known.
func TestALegReportProviderLoaderLegBoundFifty(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r5-legbound", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)

	// Flood call: one leg with 50 distinct valid children.
	floodCall := seedC2BCall(t, store, accountID, aLegID)
	floodLeg := seedC2BLeg(t, store, floodCall, aLegID, "b-flood", 1, billing.LegOutcomeWinner)
	for i := 0; i < 50; i++ {
		chargeID := fmt.Sprintf("charge-%02d", i)
		applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, floodCall, aLegID, "b-flood", chargeID, "head-"+chargeID, 1, 1, true))
	}
	claimC2BLegWork(t, store, accountID, floodCall, floodLeg)

	// Neighbor call: charge IDs reused across legs plus a B-leg-kind
	// aggregate on its own leg.
	multiCall := seedC2BCall(t, store, accountID, aLegID)
	legOne := seedC2BLeg(t, store, multiCall, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	legTwo := seedC2BLeg(t, store, multiCall, aLegID, "b-2", 2, billing.LegOutcomeFailed)
	legAgg := seedC2BLeg(t, store, multiCall, aLegID, "b-3", 3, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, multiCall, aLegID, "b-1", "charge-00", "head-m1-a", 1, 30, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, multiCall, aLegID, "b-1", "charge-01", "head-m1-b", 1, 20, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, multiCall, aLegID, "b-2", "charge-00", "head-m2-a", 1, 7, true))
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, multiCall, aLegID, "b-3", "head-agg", 1, 5, true))
	claimC2BLegWork(t, store, accountID, multiCall, legOne)
	claimC2BLegWork(t, store, accountID, multiCall, legTwo)
	claimC2BLegWork(t, store, accountID, multiCall, legAgg)

	callIDs := []string{floodCall.String(), multiCall.String()}

	// Heads: the flood leg materializes at most cap+1 rows with the
	// probe row present; neighbor legs load completely.
	heads, _, err := loadALegProviderHeadsTx(ctx, store.db, "test", accountID, callIDs)
	require.NoError(t, err)
	floodHeads := countHeadsByBLeg(t, heads[floodCall.String()])
	require.Equal(t, 9, floodHeads["b-flood"],
		"SQL must return exactly cap+1 head rows for the flood leg, got %d", floodHeads["b-flood"])
	multiHeads := countHeadsByBLeg(t, heads[multiCall.String()])
	require.Equal(t, map[string]int{"b-1": 2, "b-2": 1, "b-3": 1}, multiHeads)

	// Posting fences: same per-leg bound, same completeness.
	posting, _, err := loadALegProviderPostingFencesTx(ctx, store.db, "test", accountID, callIDs)
	require.NoError(t, err)
	floodPosting := countFencesByLeg(floodCall, posting[floodCall.String()])
	require.Equal(t, 9, floodPosting["b-flood"],
		"SQL must return exactly cap+1 posting fences for the flood leg, got %d", floodPosting["b-flood"])
	multiPosting := countFencesByLeg(multiCall, posting[multiCall.String()])
	require.Equal(t, map[string]int{"b-1": 2, "b-2": 1, "b-3": 1}, multiPosting)

	// Execution fences stay one per leg; work stays one per leg key.
	execution, _, err := loadALegProviderExecutionFencesTx(ctx, store.db, "test", accountID, callIDs)
	require.NoError(t, err)
	require.Len(t, execution[floodCall.String()], 1)
	require.Len(t, execution[multiCall.String()], 3)
	work, _, err := loadALegProviderWorkTx(ctx, store.db, accountID, callIDs, alegReportMaxPresenceLegs)
	require.NoError(t, err)
	require.Len(t, work, 4)

	// Report verdicts follow the bound with no partials.
	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	flood := findProviderLeg(t, report, floodCall, "b-flood")
	require.Equal(t, billing.ALegProviderUnknown, flood.ProviderStatus)
	require.Empty(t, flood.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	resolvedOne := findProviderLeg(t, report, multiCall, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolvedOne.ProviderStatus)
	require.Equal(t, int64(50), resolvedOne.ProviderCost.Nano)
	resolvedTwo := findProviderLeg(t, report, multiCall, "b-2")
	require.Equal(t, billing.ALegProviderKnown, resolvedTwo.ProviderStatus)
	require.Equal(t, int64(7), resolvedTwo.ProviderCost.Nano)
	resolvedAgg := findProviderLeg(t, report, multiCall, "b-3")
	require.Equal(t, billing.ALegProviderKnown, resolvedAgg.ProviderStatus)
	require.Equal(t, int64(5), resolvedAgg.ProviderCost.Nano)
	requireProviderLegTotals(t, report, 62, 3, 0, 0, 1)
}

// plantProviderHeadRow inserts one durable head row with explicit
// subject columns and payload for adversarial loader/report tests.
// Writer-rejected states (empty, foreign, or mismatched subjects)
// are the point; authority behavior stays on writer-path tests.
func plantProviderHeadRow(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, headKey, kind, id, subjectJSON string) {
	t.Helper()
	_, err := store.db.NewRaw(`INSERT INTO billing_provider_cost_heads(
		store_id, account_id, call_id, head_key, subject_kind, subject_id, subject_json,
		evidence_revision, input_set_hash, valuation_id, amount_nano, currency, head_version, fence,
		last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"test", accountID, callID.String(), headKey, kind, id, subjectJSON,
		1, strings.Repeat("a", 64), "val-adversarial", 10, "USD", 1, 1, "op-adv", "tx-adv", "tx-adv", 1, 1).Exec(context.Background())
	require.NoError(t, err)
}

// plantPostingFenceRow inserts one durable posting-fence row with an
// explicit lineage for adversarial tests.
func plantPostingFenceRow(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, lineage, headKey string) {
	t.Helper()
	_, err := store.db.NewRaw(`INSERT INTO billing_provider_cost_posting_fences(
		store_id, account_id, call_id, lineage_key, authority, head_key, evidence_revision, input_set_hash,
		fingerprint, amount_nano, currency, fence, last_operation_key, original_transaction_id, last_transaction_id,
		created_at_unix, updated_at_unix
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"test", accountID, callID.String(), lineage, "revision", headKey, 1, strings.Repeat("b", 64),
		"fp-adv", 10, "USD", 1, "op-adv", "tx-adv", "tx-adv", 1, 1).Exec(context.Background())
	require.NoError(t, err)
}

// plantExecutionFenceRow inserts one durable execution-fence row with
// an explicit lineage for adversarial tests.
func plantExecutionFenceRow(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, lineage, ownerKind, ownerHeadKey string) {
	t.Helper()
	_, err := store.db.NewRaw(`INSERT INTO billing_provider_cost_execution_fences(
		store_id, account_id, call_id, execution_lineage_key, authority, owner_subject_kind, owner_head_key,
		owner_revision, owner_input_set_hash, owner_fingerprint, fence, last_operation_key, last_transaction_id,
		created_at_unix, updated_at_unix
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"test", accountID, callID.String(), lineage, "revision", ownerKind, ownerHeadKey,
		1, strings.Repeat("c", 64), "fp-adv", 1, "op-adv", "tx-adv", 1, 1).Exec(context.Background())
	require.NoError(t, err)
}

// plantWorkRow inserts one durable work row with an explicit leg key
// for adversarial tests. Timestamps use CURRENT_TIMESTAMP so the
// statement stays portable across SQLite TEXT and PostgreSQL
// TIMESTAMPTZ columns.
func plantWorkRow(t *testing.T, store *DurableStore, callID billing.BillingCallID, legKey string) {
	t.Helper()
	_, err := store.db.NewRaw(`INSERT INTO provider_cost_work(
		usage_leg_key, call_id, status, attempt_count, next_attempt_at, last_error, updated_at
	) VALUES (?, ?, 'pending', 0, CURRENT_TIMESTAMP, '', CURRENT_TIMESTAMP)`,
		legKey, callID.String()).Exec(context.Background())
	require.NoError(t, err)
}

// seedC2R6PoisonCall builds a call with two known legs (b-1 at 30,
// b-2 at 7) plus a neighbor call with one known leg (b-1 at 5) for
// adversarial tests. The poisoned call must resolve fully unknown
// with no partials while the neighbor stays known.
func seedC2R6PoisonCall(t *testing.T, store *DurableStore, accountID, aLegID string) (poisonCall, neighborCall billing.BillingCallID) {
	t.Helper()
	poisonCall = seedC2BCall(t, store, accountID, aLegID)
	legOne := seedC2BLeg(t, store, poisonCall, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	legTwo := seedC2BLeg(t, store, poisonCall, aLegID, "b-2", 2, billing.LegOutcomeFailed)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, poisonCall, aLegID, "b-1", "charge-a", "head-poison-a", 1, 30, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, poisonCall, aLegID, "b-2", "charge-b", "head-poison-b", 1, 7, true))
	claimC2BLegWork(t, store, accountID, poisonCall, legOne)
	claimC2BLegWork(t, store, accountID, poisonCall, legTwo)

	neighborCall = seedC2BCall(t, store, accountID, aLegID)
	legN := seedC2BLeg(t, store, neighborCall, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, neighborCall, aLegID, "b-1", "charge-n", "head-neighbor", 1, 5, true))
	claimC2BLegWork(t, store, accountID, neighborCall, legN)
	return poisonCall, neighborCall
}

// requireC2R6CallPoisoned asserts the poisoned call contributes no
// provider evidence while the neighbor call stays exactly known.
func requireC2R6CallPoisoned(t *testing.T, report billing.ALegReport, poisonCall, neighborCall billing.BillingCallID) {
	t.Helper()
	for _, bleg := range []string{"b-1", "b-2"} {
		resolved := findProviderLeg(t, report, poisonCall, bleg)
		require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus, "poisoned leg %s must stay unknown", bleg)
		require.Empty(t, resolved.ProviderChildren, "poisoned leg %s must expose no partial lineage", bleg)
	}
	requireALegIssue(t, report, "provider_cost_fanout")
	neighbor := findProviderLeg(t, report, neighborCall, "b-1")
	require.Equal(t, billing.ALegProviderKnown, neighbor.ProviderStatus)
	require.Equal(t, int64(5), neighbor.ProviderCost.Nano)
	requireProviderLegTotals(t, report, 5, 1, 0, 0, 2)
}

// TestALegReportProviderEmptySubjectPoisonsCall proves a head with a
// JSON-valid empty subject ({}, no B-leg) poisons every provider leg
// of its call instead of disappearing: unattributable evidence can
// never silently drop while a valid leg stays known.
func TestALegReportProviderEmptySubjectPoisonsCall(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r6-emptysub", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCall, neighborCall := seedC2R6PoisonCall(t, store, accountID, aLegID)
	plantProviderHeadRow(t, store, accountID, poisonCall, "head-empty", "provider_charge", "charge-ghost", "{}")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	requireC2R6CallPoisoned(t, report, poisonCall, neighborCall)
}

// TestALegReportProviderForeignBLegHeadPoisonsCall proves a
// well-formed head naming a B-leg with no enumerated row poisons
// its call instead of disappearing.
func TestALegReportProviderForeignBLegHeadPoisonsCall(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r6-foreignhead", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCall, neighborCall := seedC2R6PoisonCall(t, store, accountID, aLegID)
	subject := metering.SubjectRef{
		Kind: metering.SubjectProviderCharge, StoreID: "test", AccountID: accountID,
		ALegID: aLegID, BillingCallID: poisonCall.String(), BLegID: "b-ghost",
		ProviderAccountKey: "provider-acct", ProviderChargeID: "charge-ghost",
	}
	payload, err := json.Marshal(subject)
	require.NoError(t, err)
	plantProviderHeadRow(t, store, accountID, poisonCall, "head-ghost", "provider_charge", "charge-ghost", string(payload))

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	requireC2R6CallPoisoned(t, report, poisonCall, neighborCall)
}

// TestALegReportProviderMalformedFenceLineagePoisonsLeg proves a
// posting fence naming a valid enumerated B-leg with a malformed
// lineage poisons exactly that leg: the sibling leg stays known
// with its checked sum.
func TestALegReportProviderMalformedFenceLineagePoisonsLeg(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r6-badfence", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCall, _ := seedC2R6PoisonCall(t, store, accountID, aLegID)
	plantPostingFenceRow(t, store, accountID, poisonCall, poisonCall.String()+":b-1:GARBAGE", "head-ghost")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, poisonCall, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	sibling := findProviderLeg(t, report, poisonCall, "b-2")
	require.Equal(t, billing.ALegProviderKnown, sibling.ProviderStatus)
	require.Equal(t, int64(7), sibling.ProviderCost.Nano)
	requireProviderLegTotals(t, report, 12, 2, 0, 0, 1)
}

// TestALegReportProviderForeignFenceLineagePoisonsCall proves a
// well-shaped fence lineage naming a B-leg with no enumerated row
// poisons its whole call.
func TestALegReportProviderForeignFenceLineagePoisonsCall(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r6-foreignfence", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCall, neighborCall := seedC2R6PoisonCall(t, store, accountID, aLegID)
	plantPostingFenceRow(t, store, accountID, poisonCall, poisonCall.String()+":b-ghost", "head-ghost")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	requireC2R6CallPoisoned(t, report, poisonCall, neighborCall)
}

// TestALegReportProviderMalformedExecLineagePoisonsLeg proves an
// execution fence naming a valid enumerated B-leg outside the bare
// `call:b-leg` form poisons exactly that leg.
func TestALegReportProviderMalformedExecLineagePoisonsLeg(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r6-badexec", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCall, _ := seedC2R6PoisonCall(t, store, accountID, aLegID)
	plantExecutionFenceRow(t, store, accountID, poisonCall,
		poisonCall.String()+":b-1:provider-charge:charge-a", "provider_charge", "head-ghost")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, poisonCall, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	sibling := findProviderLeg(t, report, poisonCall, "b-2")
	require.Equal(t, billing.ALegProviderKnown, sibling.ProviderStatus)
	require.Equal(t, int64(7), sibling.ProviderCost.Nano)
	requireProviderLegTotals(t, report, 12, 2, 0, 0, 1)
}

// TestALegReportProviderForeignExecLineagePoisonsCall proves an
// execution fence naming a B-leg with no enumerated row poisons
// its whole call.
func TestALegReportProviderForeignExecLineagePoisonsCall(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r6foreignexec", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCall, neighborCall := seedC2R6PoisonCall(t, store, accountID, aLegID)
	plantExecutionFenceRow(t, store, accountID, poisonCall,
		poisonCall.String()+":b-ghost", "provider_charge", "head-ghost")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	requireC2R6CallPoisoned(t, report, poisonCall, neighborCall)
}

// TestALegReportProviderForeignWorkPoisonsCall proves a work row
// keyed to a B-leg with no enumerated row poisons its whole call.
func TestALegReportProviderForeignWorkPoisonsCall(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r6-foreignwork", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCall, neighborCall := seedC2R6PoisonCall(t, store, accountID, aLegID)
	plantWorkRow(t, store, poisonCall, poisonCall.String()+":b-ghost")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	requireC2R6CallPoisoned(t, report, poisonCall, neighborCall)
}

// TestALegReportProviderCrossCallLineagePoisonsLeg proves a fence
// lineage transplanted from another call poisons the matched leg
// instead of attributing foreign evidence: call IDs share one
// fixed shape, so the extracted B-leg still maps while the lineage
// itself is malformed for this call.
func TestALegReportProviderCrossCallLineagePoisonsLeg(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r6-xcall", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCall, _ := seedC2R6PoisonCall(t, store, accountID, aLegID)
	otherCall := seedC2BCall(t, store, accountID, aLegID)
	plantPostingFenceRow(t, store, accountID, poisonCall, otherCall.String()+":b-1", "head-ghost")

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, poisonCall, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	require.Empty(t, resolved.ProviderChildren)
	requireALegIssue(t, report, "provider_cost_fanout")
	sibling := findProviderLeg(t, report, poisonCall, "b-2")
	require.Equal(t, billing.ALegProviderKnown, sibling.ProviderStatus)
	require.Equal(t, int64(7), sibling.ProviderCost.Nano)
}

// dupBLegSubjectJSON builds a well-formed provider-charge subject
// payload with two top-level b_leg_id keys in the given order for
// duplicate-key adversarial tests. Writer output never duplicates
// keys; only hand-planted drift can.
func dupBLegSubjectJSON(accountID, aLegID, callID, first, second string) string {
	return `{"kind":"provider_charge","store_id":"test","account_id":"` + accountID +
		`","a_leg_id":"` + aLegID + `","billing_call_id":"` + callID +
		`","b_leg_id":"` + first + `","provider_account_key":"provider-acct",` +
		`"provider_charge_id":"charge-dup","b_leg_id":"` + second + `"}`
}

// TestALegReportProviderDuplicateSubjectKeysPoisonCall proves
// top-level b_leg_id key cardinality other than exactly one poisons
// the call: SQLite extraction reads the first duplicate while Go
// decoding reads the last, so any divergence (valid-then-foreign,
// foreign-then-valid, two enumerated legs, or the same value twice)
// must fail closed instead of attributing one side and dropping the
// other. Four poisoned calls plus one valid neighbor share the
// report.
func TestALegReportProviderDuplicateSubjectKeysPoisonsCall(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r7-dupkeys", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	poisonCalls := make([]billing.BillingCallID, 0, 4)
	var neighborCall billing.BillingCallID
	duplicates := [][2]string{{"b-1", "b-ghost"}, {"b-ghost", "b-1"}, {"b-1", "b-2"}, {"b-1", "b-1"}}
	for i, dup := range duplicates {
		poisonCall, neighbor := seedC2R6PoisonCall(t, store, accountID, aLegID)
		poisonCalls = append(poisonCalls, poisonCall)
		if i == 0 {
			neighborCall = neighbor
		}
		plantProviderHeadRow(t, store, accountID, poisonCall, "head-dup",
			"provider_charge", "charge-dup",
			dupBLegSubjectJSON(accountID, aLegID, poisonCall.String(), dup[0], dup[1]))
	}

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	for _, poisonCall := range poisonCalls {
		for _, bleg := range []string{"b-1", "b-2"} {
			resolved := findProviderLeg(t, report, poisonCall, bleg)
			require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus,
				"duplicate-key call %s leg %s must stay unknown", poisonCall.String(), bleg)
			require.Empty(t, resolved.ProviderChildren)
		}
	}
	requireALegIssue(t, report, "provider_cost_fanout")
	neighbor := findProviderLeg(t, report, neighborCall, "b-1")
	require.Equal(t, billing.ALegProviderKnown, neighbor.ProviderStatus)
	require.Equal(t, int64(5), neighbor.ProviderCost.Nano)
	requireProviderLegTotals(t, report, 20, 4, 0, 0, 8)
}

// TestALegReportProviderSameChargeTwoLegsKnown proves one
// provider-charge identity reused on two B-legs resolves per leg:
// head identity includes the owning B-leg, so the same charge ID on
// distinct legs never collides into a false overflow.
func TestALegReportProviderSameChargeTwoLegsKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2r2-sharedcharge", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	legOne := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	legTwo := seedC2BLeg(t, store, callID, aLegID, "b-2", 2, billing.LegOutcomeFailed)
	first := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-1", "charge-shared", "head-shared-1", 1, 30, true))
	second := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callID, aLegID, "b-2", "charge-shared", "head-shared-2", 1, 20, true))
	claimC2BLegWork(t, store, accountID, callID, legOne)
	claimC2BLegWork(t, store, accountID, callID, legTwo)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolvedOne := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolvedOne.ProviderStatus)
	require.Equal(t, int64(30), resolvedOne.ProviderCost.Nano)
	require.Len(t, resolvedOne.ProviderChildren, 1)
	require.Equal(t, "charge-shared", resolvedOne.ProviderChildren[0].ChargeID)
	require.Equal(t, first.Posting.OperationKey, resolvedOne.ProviderChildren[0].OperationKey)
	resolvedTwo := findProviderLeg(t, report, callID, "b-2")
	require.Equal(t, billing.ALegProviderKnown, resolvedTwo.ProviderStatus)
	require.Equal(t, int64(20), resolvedTwo.ProviderCost.Nano)
	require.Len(t, resolvedTwo.ProviderChildren, 1)
	require.Equal(t, "charge-shared", resolvedTwo.ProviderChildren[0].ChargeID)
	require.Equal(t, second.Posting.OperationKey, resolvedTwo.ProviderChildren[0].OperationKey)
	requireProviderLegTotals(t, report, 50, 2, 0, 0, 0)
}

// TestALegReportProviderZeroFenceDriftUnknown proves a first zero
// whose fence fingerprint no longer binds its operation snapshot
// stays unresolved. Operation snapshots are immutable and unique per
// operation, so malformed zero evidence is planted on the mutable
// fence side.
func TestALegReportProviderZeroFenceDriftUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2r2-zerofp", "a-leg-c2b"
	seedALegCustomerAccount(t, store, accountID)
	callID := seedC2BCall(t, store, accountID, aLegID)
	leg := seedC2BLeg(t, store, callID, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	first := applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, callID, aLegID, "b-1", "head-b-1", 1, 0, true))
	require.Equal(t, int64(0), first.CurrentAmount.Nano)
	claimC2BLegWork(t, store, accountID, callID, leg)
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_posting_fences SET fingerprint = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		"fp-drift", "test", accountID, callID.String(), "head-b-1").Exec(ctx)
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	resolved := findProviderLeg(t, report, callID, "b-1")
	require.Equal(t, billing.ALegProviderUnknown, resolved.ProviderStatus)
	requireALegIssue(t, report, "provider_cost_unresolved")
	requireProviderLegTotals(t, report, 0, 0, 0, 0, 1)
}
