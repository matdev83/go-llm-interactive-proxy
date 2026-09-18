package billingstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

// TestRefinement83 integrated cross-authority proof (subpass C): domain
// attribution and retail rating computed through the production seams are
// persisted through the production store seams on one file-backed store,
// then explained and restarted. Multimodal contribution lineage travels on
// the durable B-leg rows. No A-leg/session-finality dependency exists.

const ref83intStoreID = "ref83"

func ref83intStore(t *testing.T) (*DurableStore, string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "billing.sqlite")
	store, closeStore := openRefinement82FileBillingStore(t, path, ref83intStoreID)
	return store, path, closeStore
}

func ref83intKey(direction metering.FlowDirection, component, unit string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: unit, SchemaID: "refinement83.cert.v1"}
}

func ref83intDecimal(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &value
}

func ref83intObservation(t *testing.T, callID billing.BillingCallID, bLegID, id, charge string, measures ...struct {
	key      metering.ComponentKey
	quantity string
}) metering.Observation {
	t.Helper()
	items := make([]metering.Measure, 0, len(measures))
	for _, item := range measures {
		items = append(items, metering.Measure{Key: item.key, Value: ref83intDecimal(t, item.quantity), Quality: metering.QualityObserved})
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	return metering.Observation{
		Version: 2, ID: id, SourceEventKey: id + "-source", Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: ref83intStoreID, ALegID: "a-83", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: ref83intStoreID, CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-83", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "refinement83.cert.v1",
		Measures: items,
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "cogs-" + id, Kind: metering.ChargeKindAggregate,
			Payer:    metering.PaymentParty{Kind: metering.PaymentPartyOperator},
			Amount:   ref83intDecimal(t, charge),
			Currency: "USD",
		}},
	}
}

func ref83intLeg(t *testing.T, callID billing.BillingCallID, id string, seq int, outcome billing.LegOutcome, surfaced billing.SurfacedState, observation *metering.Observation) billing.CallLegUsageRecord {
	t.Helper()
	leg := testIndependentCallLegFor(callID, id)
	leg.ALegID = "a-83"
	leg.AttemptSeq = seq
	leg.Outcome = outcome
	leg.Surfaced = surfaced
	leg.EvidenceVersion = billing.EvidenceFormatVersionV2
	leg.EvidenceProjection = billing.EvidenceProjectionV1
	if observation != nil {
		leg.Observations = []metering.Observation{*observation}
	}
	return leg
}

func ref83intTariff(t *testing.T) economics.TariffSnapshot {
	t.Helper()
	rule := func(id string, key metering.ComponentKey, price string) economics.RatingRule {
		return economics.RatingRule{ID: id, Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: ref83intDecimal(t, price)}
	}
	imageIn := ref83intKey(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	imageOut := ref83intKey(metering.DirectionOutput, metering.ComponentImage, metering.UnitImage)
	audioIn := ref83intKey(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	audioOut := ref83intKey(metering.DirectionOutput, metering.ComponentAudio, metering.UnitSecond)
	docIn := ref83intKey(metering.DirectionInput, metering.ComponentDocument, metering.UnitPage)
	tariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing-83", Version: "v3"}, RaterID: "reference"},
		"USD",
		[]economics.RatingRule{
			rule("image-input", imageIn, "0.10"),
			rule("image-output", imageOut, "0.40"),
			rule("audio-input", audioIn, "0.02"),
			rule("audio-output", audioOut, "0.05"),
			rule("document-input", docIn, "0.01"),
			{ID: "call-fee", Kind: economics.RatingRuleFixed, Currency: "USD", FixedAmount: ref83intDecimal(t, "1"), FixedScope: economics.FixedFeeScopeCall},
		})
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func ref83intWinnerPolicy() billing.ChargePolicy {
	return billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "retail-policy-83", Version: "v10"},
		PricingRef:          billing.VersionRef{ID: "retail-pricing-83", Version: "v3"},
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeFixedCharges: true,
		Retail:              &billing.RetailSelectionPolicy{Mode: billing.RetailSelectionSurfacedWinner, Basis: billing.RetailBasisIndependent},
	}
}

func ref83intCall(t *testing.T, callID billing.BillingCallID, accountID string, policy billing.ChargePolicy, legs ...string) billing.CallUsageRecord {
	t.Helper()
	return billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-83", SessionID: "sess-83",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted, SubmissionID: "submission-83",
		CustomerPricingRef: policy.PricingRef, ChargePolicyRef: policy.Ref,
		ExpectedBLegIDs: append([]string(nil), legs...),
	}
}

func ref83intSealedKey(t *testing.T, leg billing.CallLegUsageRecord) string {
	t.Helper()
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	return sealed.Key
}

var errRef83NoOperatorCharge = errors.New("refinement83: leg carries no operator-payable charge")

// ref83intLegByKey resolves one COGS-included leg key to its durable leg row.
func ref83intLegByKey(t *testing.T, legs []billing.CallLegUsageRecord, key string) billing.CallLegUsageRecord {
	t.Helper()
	for _, leg := range legs {
		sealed, err := leg.Seal()
		if err != nil {
			t.Fatal(err)
		}
		if sealed.Key == key {
			return leg
		}
	}
	t.Fatalf("no leg for COGS key %q", key)
	return billing.CallLegUsageRecord{}
}

// ref83intOperatorChargeOrError extracts the exact operator-payable amount
// and immutable observation ref from one leg. It fails closed unless the leg
// carries exactly one operator-payer charge, so shell and BYOK legs can
// never produce a postable operator contribution.
func ref83intOperatorChargeOrError(leg billing.CallLegUsageRecord) (billing.Money, metering.ObservationRef, error) {
	count := 0
	var amount billing.Money
	var ref metering.ObservationRef
	for _, observation := range leg.Observations {
		for _, charge := range observation.Charges {
			if charge.Payer.Kind != metering.PaymentPartyOperator || charge.Amount == nil {
				continue
			}
			count++
			nano, err := charge.Amount.ToNanoUnits()
			if err != nil {
				return billing.Money{}, metering.ObservationRef{}, err
			}
			amount = billing.Money{Nano: nano, Currency: charge.Currency}
			ref, err = observation.Ref(observation.Subject.StoreID)
			if err != nil {
				return billing.Money{}, metering.ObservationRef{}, err
			}
		}
	}
	if count != 1 {
		return billing.Money{}, metering.ObservationRef{}, fmt.Errorf("%w: leg %q has %d operator charges", errRef83NoOperatorCharge, leg.BLegID, count)
	}
	return amount, ref, nil
}

func ref83intOperatorCharge(t *testing.T, leg billing.CallLegUsageRecord) (billing.Money, metering.ObservationRef) {
	t.Helper()
	amount, ref, err := ref83intOperatorChargeOrError(leg)
	if err != nil {
		t.Fatalf("operator contribution for leg %q: %v", leg.BLegID, err)
	}
	return amount, ref
}

func ref83intPassThroughPolicy() billing.ChargePolicy {
	return billing.ChargePolicy{
		Ref:                 billing.VersionRef{ID: "ref83-pt-policy", Version: "v1"},
		PricingRef:          billing.VersionRef{ID: "pt-prices-83", Version: "v1"},
		Scope:               billing.ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeFixedCharges: true,
		Retail: &billing.RetailSelectionPolicy{
			Mode:  billing.RetailSelectionSurfacedWinner,
			Basis: billing.RetailBasisCostPassThrough,
			CostPassThrough: &billing.CostPassThroughPolicy{
				MissingCost:         billing.CostPassThroughMissingCostPending,
				SafeBound:           &billing.Money{Nano: 10_000_000_000, Currency: "USD"},
				AllowLateAdjustment: true,
			},
		},
	}
}

func ref83intProviderCost(revision uint64, nanos int64) billing.CostPassThroughProviderCost {
	return billing.CostPassThroughProviderCost{
		LURKey: "lur-83-pt", ValuationID: "valuation-83-pt", Revision: revision,
		InputHash: strings.Repeat("e", 64), Amount: billing.Money{Nano: nanos, Currency: "USD"},
		AmountPresent: true, Reconciled: true, Authoritative: true,
	}
}

func TestRefinement83IntegratedPassThroughHeadRevisionAndConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path, closeStore := ref83intStore(t)
	defer closeStore()
	account := billing.Account{ID: "ref83-pt", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10_000_000_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	policy := ref83intPassThroughPolicy()
	obs := ref83intObservation(t, callID, "b-pt-winner", "obs-b-pt-winner", "0.50",
		struct {
			key      metering.ComponentKey
			quantity string
		}{key: ref83intKey(metering.DirectionInput, metering.ComponentImage, metering.UnitImage), quantity: "1"})
	leg := ref83intLeg(t, callID, "b-pt-winner", 1, billing.LegOutcomeWinner, billing.SurfacedYes, &obs)
	call := ref83intCall(t, callID, account.ID, policy, "b-pt-winner")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	// An unrelated resource allocation exists durably on this store, yet the
	// pass-through settlement below must not absorb a nano of it.
	if err := store.AppendAllocation(ctx, ref83intAllocation(t, callID)); err != nil {
		t.Fatalf("AppendAllocation: %v", err)
	}

	// The initial head comes from the production RateCall result, not a
	// hand-built settlement: authoritative provider cost settles finally.
	provider := ref83intProviderCost(1, 2_000_000_000)
	rated, err := billing.RateCall(billing.CallRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg},
		MaxCustomerCharge: billing.Money{Nano: 10_000_000_000, Currency: "USD"},
		CustomerPolicy:    policy, ProviderCost: &provider,
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}
	if rated.CustomerCharge != (billing.Money{Nano: 2_000_000_000, Currency: "USD"}) ||
		rated.CostPassThrough == nil || rated.CostPassThrough.Status != billing.CostPassThroughSettlementFinal {
		t.Fatalf("pass-through rating = %+v, want final 2.00 USD", rated)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 10_000_000_000, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: rated})
	if err != nil {
		t.Fatalf("pass-through settlement: %v", err)
	}
	if settled.CostPassThrough == nil || settled.CostPassThrough.ProviderCost == nil ||
		*settled.CostPassThrough.ProviderCost != provider {
		t.Fatalf("settled head lost provider lineage: %+v", settled.CostPassThrough)
	}
	head := settled.CostPassThrough
	if head.PolicyRef != policy.Ref || head.Policy.MissingCost != billing.CostPassThroughMissingCostPending ||
		!head.Policy.AllowLateAdjustment {
		t.Fatalf("settled head lost policy lineage: %+v", head)
	}
	if head.Status != billing.CostPassThroughSettlementFinal ||
		head.SafeBound != (billing.Money{Nano: 10_000_000_000, Currency: "USD"}) ||
		head.PostedAmount != (billing.Money{Nano: 2_000_000_000, Currency: "USD"}) {
		t.Fatalf("settled head state = %+v, want final 2.00 under the 10.00 bound", head)
	}
	if providerFP, err := head.ProviderCost.SemanticFingerprint(); err != nil {
		t.Fatal(err)
	} else if expectedFP, err := provider.SemanticFingerprint(); err != nil {
		t.Fatal(err)
	} else if providerFP != expectedFP {
		t.Fatal("settled head provider fingerprint drifted from the accepted cost")
	}
	if head.ProviderCost.LURKey != "lur-83-pt" || head.ProviderCost.ValuationID != "valuation-83-pt" ||
		head.ProviderCost.Revision != 1 || head.ProviderCost.InputHash != strings.Repeat("e", 64) {
		t.Fatalf("settled head lost LUR/valuation/revision/hash lineage: %+v", head.ProviderCost)
	}
	if head.OriginalTransactionID == "" || head.OriginalTransactionID != settled.Customer.Transaction.ID {
		t.Fatalf("head linkage = %+v, want the durable settlement transaction identity", head)
	}
	settlementReplay, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: rated})
	if err != nil {
		t.Fatalf("settlement replay: %v", err)
	}
	if !settlementReplay.Replayed {
		t.Fatalf("settlement replay = %+v, want Replayed without duplicate posting", settlementReplay)
	}
	// The durable explanation exposes the pass-through settlement as the
	// customer operation snapshot: source key, exact fingerprint binding the
	// RateCall result to the head, kind, and currency.
	wantHeadFP, err := head.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	wantOpFingerprint := rated.Fingerprint + ":cost-pass-through:" + wantHeadFP
	explained, err := store.CallExplanation(ctx, callID.String())
	if err != nil {
		t.Fatalf("CallExplanation: %v", err)
	}
	if len(explained.CustomerOperations) != 1 {
		t.Fatalf("customer operations = %+v, want exactly the pass-through settlement", explained.CustomerOperations)
	}
	customerOp := explained.CustomerOperations[0]
	if customerOp.SourceKey != callID.String() || customerOp.Fingerprint != wantOpFingerprint ||
		customerOp.OperationKind != "customer_call_settlement" || customerOp.Currency != "USD" {
		t.Fatalf("pass-through customer operation = %+v, want exact head-bound snapshot", customerOp)
	}
	if !explained.Result.Processed || explained.Result.CustomerCharge.Nano != 2_000_000_000 {
		t.Fatalf("explanation result = %+v, want processed 2.00 charge", explained.Result)
	}

	// Identical revision replays without applying; a superseding revision
	// posts exactly its delta; an older revision is stale.
	replay, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{
		AccountID: account.ID, CallID: callID, ProviderCost: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Applied {
		t.Fatalf("identical revision = %+v, want replay without application", replay)
	}
	secondCost := ref83intProviderCost(2, 1_500_000_000)
	second, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{
		AccountID: account.ID, CallID: callID, ProviderCost: secondCost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Applied || second.Delta != (billing.Money{Nano: -500_000_000, Currency: "USD"}) ||
		second.CurrentAmount != (billing.Money{Nano: 1_500_000_000, Currency: "USD"}) {
		t.Fatalf("superseding revision = %+v, want -0.50 delta to 1.50", second)
	}
	if second.ProviderCost != secondCost {
		t.Fatalf("revision result lost provider lineage: %+v", second.ProviderCost)
	}
	wantAdjustmentKey, err := billing.CostPassThroughAdjustmentSourceKey(account.ID, callID, secondCost)
	if err != nil {
		t.Fatal(err)
	}
	if second.Posting.Transaction.SourceKey != wantAdjustmentKey ||
		second.Posting.Transaction.OperationKind != billing.CostPassThroughAdjustmentOperationKind {
		t.Fatalf("revision journal = %+v, want exact %q lineage", second.Posting.Transaction, wantAdjustmentKey)
	}
	stale, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{
		AccountID: account.ID, CallID: callID, ProviderCost: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale || stale.Applied {
		t.Fatalf("older revision = %+v, want stale no-op", stale)
	}
	conflict := ref83intProviderCost(2, 1_600_000_000)
	if _, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{
		AccountID: account.ID, CallID: callID, ProviderCost: conflict,
	}); !errors.Is(err, billing.ErrCostPassThroughRevisionConflict) {
		t.Fatalf("conflicting revision = %v, want ErrCostPassThroughRevisionConflict", err)
	}
	gotAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 10.00 - 2.00 settlement, then -0.50 revision credit back: 8.50.
	if gotAccount.BalanceNano != 8_500_000_000 {
		t.Fatalf("account balance = %+v, want 8.50", gotAccount)
	}
	if got := ref83intJournalCount(t, ctx, store, account.ID, billing.CostPassThroughAdjustmentOperationKind); got != 1 {
		t.Fatalf("pass-through adjustment journals = %d, want exactly one", got)
	}
	if got := ref83intJournalCount(t, ctx, store, account.ID, "customer_call_settlement"); got != 1 {
		t.Fatalf("customer settlement journals = %d, want exactly one", got)
	}

	// Restart: the head, operation snapshot, journals, and balance survive
	// with identical refs, fingerprints, lineage, and amounts. The full
	// journal content is compared transaction by transaction, and an exact
	// replay afterwards must leave that set unchanged.
	beforeJournals := ref83intJournalSnapshot(t, ctx, store, account.ID)
	if len(beforeJournals) != 2 {
		t.Fatalf("pre-restart journals = %d, want settlement plus one adjustment", len(beforeJournals))
	}
	closeStore()
	store, closeStore = openRefinement82FileBillingStore(t, path, ref83intStoreID)
	t.Cleanup(func() { closeStore() })
	restartedExplanation, err := store.CallExplanation(ctx, callID.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(restartedExplanation.CustomerOperations) != 1 ||
		restartedExplanation.CustomerOperations[0] != customerOp {
		t.Fatalf("restart changed pass-through operation: before=%+v after=%+v",
			customerOp, restartedExplanation.CustomerOperations)
	}
	if restartedExplanation.Result != explained.Result {
		t.Fatalf("restart changed explanation result: before=%+v after=%+v",
			explained.Result, restartedExplanation.Result)
	}
	ref83intRequireSameTransactions(t, beforeJournals, ref83intJournalSnapshot(t, ctx, store, account.ID))
	restartedReplay, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{
		AccountID: account.ID, CallID: callID, ProviderCost: ref83intProviderCost(2, 1_500_000_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !restartedReplay.Replayed || restartedReplay.Applied {
		t.Fatalf("post-restart identical revision = %+v, want replay without application", restartedReplay)
	}
	ref83intRequireSameTransactions(t, beforeJournals, ref83intJournalSnapshot(t, ctx, store, account.ID))
	restartedAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restartedAccount.BalanceNano != gotAccount.BalanceNano || restartedAccount.Version != gotAccount.Version {
		t.Fatalf("restart changed account: before=%+v after=%+v", gotAccount, restartedAccount)
	}
}

func ref83intAllocation(t *testing.T, callID billing.BillingCallID) economics.AllocationRecord {
	t.Helper()
	amount, err := metering.ParseDecimal("12")
	if err != nil {
		t.Fatal(err)
	}
	target := func(id, bLegID, numerator string) economics.AllocationTarget {
		return economics.AllocationTarget{
			TargetID: id, Weight: economics.AllocationFraction{Numerator: numerator, Denominator: "4"},
			Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: ref83intStoreID, TenantID: "tenant-83",
				BillingCallID: callID.String(), ALegID: "a-83", BLegID: bLegID},
		}
	}
	return economics.AllocationRecord{
		ID: "ref83-cache-tier-cost", Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: ref83intStoreID, TenantID: "tenant-83", AccountID: "supplier-account-83",
			ResourceID: "ref83-cache-tier", PeriodID: "2026-09",
			StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC(),
		},
		SourceBasis: economics.BasisStatementReported, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "ref83-cache-weighted", Version: "v1", Hash: strings.Repeat("d", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			target("b-winner-share", "b-winner", "2"),
			target("b-retry-share", "b-retry", "1"),
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "4"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
}

func TestRefinement83IntegratedAllocationPersistsWithoutSyntheticLegs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, closeStore := ref83intStore(t)
	defer closeStore()
	account := billing.Account{ID: "ref83-alloc", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10_000_000_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	policy := ref83intWinnerPolicy()
	imageIn := ref83intKey(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	retryObs := ref83intObservation(t, callID, "b-retry", "obs-b-retry", "1.25",
		struct {
			key      metering.ComponentKey
			quantity string
		}{key: imageIn, quantity: "3"})
	winnerObs := ref83intObservation(t, callID, "b-winner", "obs-b-winner", "3.50",
		struct {
			key      metering.ComponentKey
			quantity string
		}{key: imageIn, quantity: "1"})
	legs := []billing.CallLegUsageRecord{
		ref83intLeg(t, callID, "b-retry", 1, billing.LegOutcomeFailed, billing.SurfacedNo, &retryObs),
		ref83intLeg(t, callID, "b-winner", 2, billing.LegOutcomeWinner, billing.SurfacedYes, &winnerObs),
	}
	call := ref83intCall(t, callID, account.ID, policy, "b-retry", "b-winner")
	for _, leg := range legs {
		if err := store.AppendCallLegUsage(ctx, leg); err != nil {
			t.Fatalf("append leg %q: %v", leg.BLegID, err)
		}
	}

	record := ref83intAllocation(t, callID)
	if err := store.AppendAllocation(ctx, record); err != nil {
		t.Fatalf("AppendAllocation: %v", err)
	}
	if err := store.AppendAllocation(ctx, record); err != nil {
		t.Fatalf("allocation replay: %v", err)
	}
	got, err := store.GetAllocation(ctx, record.ID, record.Version)
	if err != nil {
		t.Fatalf("GetAllocation: %v", err)
	}
	wantJSON, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := got.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("allocation round-trip drifted:\nwant %s\ngot  %s", wantJSON, gotJSON)
	}
	wantShares := map[string]economics.AllocationFraction{
		"b-winner-share": {Numerator: "1", Denominator: "2"},
		"b-retry-share":  {Numerator: "1", Denominator: "4"},
		"unallocated":    {Numerator: "1", Denominator: "4"},
	}
	if len(got.Targets) != len(wantShares) {
		t.Fatalf("persisted targets = %+v, want two attributions plus remainder", got.Targets)
	}
	for _, target := range got.Targets {
		if wantShares[target.TargetID] != target.Share {
			t.Fatalf("persisted target %q share = %+v, want %+v", target.TargetID, target.Share, wantShares[target.TargetID])
		}
	}
	if got.SourceSubject.ResourceID != "ref83-cache-tier" || got.Policy.Method != "ref83-cache-weighted" || got.Policy.Version != "v1" {
		t.Fatalf("persisted allocation lost source/policy identity: %+v", got)
	}
	page, err := store.ListAllocations(ctx, economics.AllocationQuery{StoreID: ref83intStoreID, SourceSubject: &record.SourceSubject, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Allocations) != 1 || page.Allocations[0].ID != record.ID {
		t.Fatalf("allocation listing = %+v, want the persisted record", page.Allocations)
	}

	// No synthetic B-leg was created for the allocation targets.
	stored, err := store.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("durable legs = %d, want exactly the two executed B-legs", len(stored))
	}

	// Retail inference selection over the persisted legs still selects only
	// the winner: the allocation never becomes provider inference usage.
	selection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: stored, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.SelectedBLegs) != 1 || selection.SelectedBLegs[0].BLegID != "b-winner" {
		t.Fatalf("selection over persisted legs = %+v, want winner only", selection.SelectedBLegs)
	}
}

func ref83intJournalCount(t *testing.T, ctx context.Context, store *DurableStore, accountID, kind string) int {
	t.Helper()
	count := 0
	for _, transaction := range ref83intJournalSnapshot(t, ctx, store, accountID) {
		if transaction.OperationKind == kind {
			count++
		}
	}
	return count
}

// ref83intJournalSnapshot reads every journal transaction for the account
// through the production durable reader, sorted deterministically by source
// key then transaction ID. JournalTransactions does not promise row order,
// so the sort is test-side normalization only; every compared field comes
// from the durable row.
func ref83intJournalSnapshot(t *testing.T, ctx context.Context, store *DurableStore, accountID string) []billing.JournalTransaction {
	t.Helper()
	transactions, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	out := append([]billing.JournalTransaction(nil), transactions...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceKey != out[j].SourceKey {
			return out[i].SourceKey < out[j].SourceKey
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ref83intRequireSameTransactions compares full durable transaction content:
// IDs, source keys, operation kinds, turn/B-leg/correction lineage, currency,
// account sequence, and complete ordered entries/amounts. RecordedAt is
// assigned by the database and excluded from semantic identity, exactly as
// production declares it.
func ref83intRequireSameTransactions(t *testing.T, want, got []billing.JournalTransaction) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("journal transaction count = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		a, b := want[i], got[i]
		if a.ID != b.ID || a.SourceKey != b.SourceKey || a.OperationKind != b.OperationKind ||
			a.TurnID != b.TurnID || a.BLegID != b.BLegID || a.CorrectionGroupID != b.CorrectionGroupID ||
			a.Currency != b.Currency || a.AccountSequence != b.AccountSequence {
			t.Fatalf("journal transaction %d differs:\n got %+v\nwant %+v", i, b, a)
		}
		if len(a.Entries) != len(b.Entries) {
			t.Fatalf("journal transaction %q entries = %+v, want %+v", a.SourceKey, b.Entries, a.Entries)
		}
		for j := range a.Entries {
			if a.Entries[j] != b.Entries[j] {
				t.Fatalf("journal transaction %q entry %d = %+v, want %+v", a.SourceKey, j, b.Entries[j], a.Entries[j])
			}
		}
	}
}

func TestRefinement83IntegratedCrossAuthorityPersistsAndExplains(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, path, closeStore := ref83intStore(t)
	account := billing.Account{ID: "ref83-integrated", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10_000_000_000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	policy := ref83intWinnerPolicy()
	measure := func(key metering.ComponentKey, quantity string) struct {
		key      metering.ComponentKey
		quantity string
	} {
		return struct {
			key      metering.ComponentKey
			quantity string
		}{key: key, quantity: quantity}
	}
	imageIn := ref83intKey(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	imageOut := ref83intKey(metering.DirectionOutput, metering.ComponentImage, metering.UnitImage)
	audioIn := ref83intKey(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	audioOut := ref83intKey(metering.DirectionOutput, metering.ComponentAudio, metering.UnitSecond)
	docIn := ref83intKey(metering.DirectionInput, metering.ComponentDocument, metering.UnitPage)
	retryObs := ref83intObservation(t, callID, "b-retry", "obs-b-retry", "1.25", measure(imageIn, "3"))
	winnerObs := ref83intObservation(t, callID, "b-winner", "obs-b-winner", "3.50",
		measure(imageIn, "1"), measure(imageOut, "1"), measure(audioIn, "10"), measure(audioOut, "5"), measure(docIn, "2"))
	byokObs := ref83intObservation(t, callID, "b-byok", "obs-b-byok", "5.00", measure(imageIn, "1"))
	byokObs.Charges[0].Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer}
	legs := []billing.CallLegUsageRecord{
		ref83intLeg(t, callID, "b-retry", 1, billing.LegOutcomeFailed, billing.SurfacedNo, &retryObs),
		ref83intLeg(t, callID, "b-winner", 2, billing.LegOutcomeWinner, billing.SurfacedYes, &winnerObs),
		ref83intLeg(t, callID, "b-shell", 3, billing.LegOutcomeNeverStarted, billing.SurfacedNo, nil),
		ref83intLeg(t, callID, "b-byok", 4, billing.LegOutcomeFailed, billing.SurfacedNo, &byokObs),
	}
	call := ref83intCall(t, callID, account.ID, policy, "b-retry", "b-winner", "b-shell", "b-byok")

	// Domain authorities computed through production seams: all-payable COGS
	// across both executed attempts, winner-only retail selection, exact
	// settlement rating, and a strictly larger retry-inclusive rating.
	cogs, err := billing.AttributeOperatorCOGS(legs, nil, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if cogs.KnownSubtotal != (billing.Money{Nano: 4_750_000_000, Currency: "USD"}) || !cogs.Payable {
		t.Fatalf("operator COGS = %+v, want known payable 4.75 USD", cogs)
	}
	wantExcluded := []string{
		callID.String() + ":b-byok",
		callID.String() + ":b-shell",
	}
	if len(cogs.ExcludedLegKeys) != len(wantExcluded) {
		t.Fatalf("COGS excluded = %v, want BYOK plus shell %v", cogs.ExcludedLegKeys, wantExcluded)
	}
	for i, want := range wantExcluded {
		if cogs.ExcludedLegKeys[i] != want {
			t.Fatalf("COGS excluded = %v, want %v", cogs.ExcludedLegKeys, wantExcluded)
		}
	}
	selection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: legs, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.SelectedBLegs) != 1 || selection.SelectedBLegs[0].BLegID != "b-winner" {
		t.Fatalf("retail selection = %+v, want winner only", selection.SelectedBLegs)
	}
	tariff := ref83intTariff(t)
	rated, err := billing.RateCall(billing.CallRatingInput{
		Call: call, Legs: legs, MaxCustomerCharge: billing.Money{Nano: 100_000_000_000, Currency: "USD"},
		CustomerPricing: billing.PricingSnapshot{Ref: policy.PricingRef, Currency: "USD"},
		CustomerPolicy:  policy, CustomerTariff: tariff,
	})
	if err != nil {
		t.Fatalf("RateCall: %v", err)
	}
	if rated.CustomerCharge != (billing.Money{Nano: 1_970_000_000, Currency: "USD"}) {
		t.Fatalf("customer charge = %+v, want 1.97 USD", rated.CustomerCharge)
	}
	inclusivePolicy := policy
	inclusiveRetail := *policy.Retail
	inclusiveRetail.Mode = billing.RetailSelectionAllAttributable
	inclusivePolicy.Retail = &inclusiveRetail
	inclusiveSelection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: legs, Policy: inclusivePolicy})
	if err != nil {
		t.Fatal(err)
	}
	if len(inclusiveSelection.SelectedBLegs) != 3 {
		t.Fatalf("retry-inclusive selection = %+v, want retry plus winner plus byok", inclusiveSelection.SelectedBLegs)
	}

	wantIncluded := []string{
		callID.String() + ":b-retry",
		callID.String() + ":b-winner",
	}
	if len(cogs.IncludedLegKeys) != len(wantIncluded) {
		t.Fatalf("COGS included = %v, want %v", cogs.IncludedLegKeys, wantIncluded)
	}
	for i, want := range wantIncluded {
		if cogs.IncludedLegKeys[i] != want {
			t.Fatalf("COGS included = %v, want %v", cogs.IncludedLegKeys, wantIncluded)
		}
	}

	// Durable operator costs: every posting is derived from the production
	// COGS result. One posting per included leg key, with the amount taken
	// from that leg's own immutable operator charge; the shell and BYOK legs
	// carry no operator-payable charge, so the derivation fails closed for
	// them and they can never reach the posting seam through this path.
	postedResults := make(map[string]billing.OperatorCostResult)
	expectedRefs := make(map[string]metering.ObservationRef)
	var postedTotal int64
	for _, leg := range legs {
		if err := store.AppendCallLegUsage(ctx, leg); err != nil {
			t.Fatalf("append leg %q: %v", leg.BLegID, err)
		}
	}
	for _, key := range cogs.IncludedLegKeys {
		leg := ref83intLegByKey(t, legs, key)
		amount, ref := ref83intOperatorCharge(t, leg)
		result := billing.OperatorCostResult{
			LURKey: key, Amount: amount,
			AmountPresent: true, Reconciled: true, Authoritative: true,
		}
		posting, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
			AccountID: account.ID, CallID: callID, Leg: leg, Result: result,
		})
		if err != nil {
			t.Fatalf("provider cost %q: %v", leg.BLegID, err)
		}
		wantOpKey, err := billing.ProviderCostSourceKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if posting.Replayed {
			t.Fatalf("first provider posting for %q replayed: %+v", key, posting)
		}
		if posting.OperationKey != wantOpKey {
			t.Fatalf("provider operation key = %q, want %q", posting.OperationKey, wantOpKey)
		}
		transaction := posting.Transaction
		if transaction.OperationKind != "provider_call_cogs" || transaction.SourceKey != wantOpKey ||
			transaction.ID != wantOpKey || transaction.TurnID != callID.String() ||
			transaction.BLegID != leg.BLegID || transaction.CorrectionGroupID != key ||
			transaction.Currency != "USD" {
			t.Fatalf("provider journal = %+v, want exact %q lineage", transaction, key)
		}
		if len(transaction.Entries) != 2 ||
			transaction.Entries[0] != (billing.JournalEntry{LedgerAccount: "inference_provider_cogs", Side: billing.JournalDebit, Amount: amount}) ||
			transaction.Entries[1] != (billing.JournalEntry{LedgerAccount: "provider_payable_clearing", Side: billing.JournalCredit, Amount: amount}) {
			t.Fatalf("provider entries = %+v, want exact COGS debit/credit of %+v", transaction.Entries, amount)
		}
		if ref.ObservationID == "" || ref.Revision == 0 || ref.PayloadHash == "" || ref.StoreID == "" {
			t.Fatalf("contribution ref for %q is not immutable: %+v", key, ref)
		}
		postedResults[key] = result
		expectedRefs[key] = ref
		postedTotal += amount.Nano
	}
	if postedTotal != cogs.KnownSubtotal.Nano {
		t.Fatalf("posted provider total = %d, want COGS subtotal %d", postedTotal, cogs.KnownSubtotal.Nano)
	}
	// The shell carries no reconciled amount: even claiming authority, the
	// store's monetary guard rejects it; the BYOK leg carries no
	// operator-payable charge, so the contribution derivation above fails
	// closed for it. Neither can post.
	shellKey := callID.String() + ":b-shell"
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
		AccountID: account.ID, CallID: callID, Leg: ref83intLegByKey(t, legs, shellKey),
		Result: billing.OperatorCostResult{LURKey: shellKey, Authoritative: true, AmountPresent: false},
	}); !errors.Is(err, billing.ErrUnreconciledCost) {
		t.Fatalf("shell posting = %v, want %v", err, billing.ErrUnreconciledCost)
	}
	byokKey := callID.String() + ":b-byok"
	if _, _, err := ref83intOperatorChargeOrError(ref83intLegByKey(t, legs, byokKey)); err == nil {
		t.Fatal("BYOK leg unexpectedly carries an operator-payable charge")
	}
	winnerKey := callID.String() + ":b-winner"
	winnerAmount, _ := ref83intOperatorCharge(t, ref83intLegByKey(t, legs, winnerKey))
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
		AccountID: account.ID, CallID: callID, Leg: ref83intLegByKey(t, legs, winnerKey),
		Result: billing.OperatorCostResult{
			LURKey: winnerKey, Amount: winnerAmount,
			AmountPresent: true, Reconciled: true, Authoritative: true,
		},
	}); err != nil {
		t.Fatalf("identical provider replay: %v", err)
	}
	conflictAmount := winnerAmount
	conflictAmount.Nano++
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
		AccountID: account.ID, CallID: callID, Leg: ref83intLegByKey(t, legs, winnerKey),
		Result: billing.OperatorCostResult{
			LURKey: winnerKey, Amount: conflictAmount,
			AmountPresent: true, Reconciled: true, Authoritative: true,
		},
	}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conflicting provider payload = %v, want ErrOperationConflict", err)
	}

	// Durable customer settlement for the winner-only charge, then replay.
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 10_000_000_000, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: rated})
	if err != nil {
		t.Fatalf("customer settlement: %v", err)
	}
	if settled.Replayed || settled.Customer.Transaction.ID == "" {
		t.Fatalf("first settlement = %+v, want one customer posting", settled)
	}
	wantSettlementKey, err := billing.CustomerSettlementSourceKey(account.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	settlementTx := settled.Customer.Transaction
	if settlementTx.SourceKey != wantSettlementKey || settlementTx.ID != wantSettlementKey ||
		settlementTx.OperationKind != "customer_call_settlement" || settlementTx.TurnID != callID.String() ||
		settlementTx.Currency != "USD" {
		t.Fatalf("settlement journal = %+v, want exact %q lineage", settlementTx, wantSettlementKey)
	}
	if len(settlementTx.Entries) != 2 ||
		settlementTx.Entries[0] != (billing.JournalEntry{LedgerAccount: "customer_financial_account", Side: billing.JournalDebit, Amount: rated.CustomerCharge}) ||
		settlementTx.Entries[1] != (billing.JournalEntry{LedgerAccount: "usage_revenue", Side: billing.JournalCredit, Amount: rated.CustomerCharge}) {
		t.Fatalf("settlement entries = %+v, want exact 1.97 USD debit/credit", settlementTx.Entries)
	}
	replay, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: rated})
	if err != nil {
		t.Fatalf("settlement replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("settlement replay = %+v, want Replayed without duplicate posting", replay)
	}
	gotAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAccount.BalanceNano != 8_030_000_000 {
		t.Fatalf("account balance = %+v, want 8.03 after the 1.97 settlement", gotAccount)
	}
	if got := ref83intJournalCount(t, ctx, store, account.ID, "customer_call_settlement"); got != 1 {
		t.Fatalf("customer settlement journals = %d, want exactly one", got)
	}
	if got := ref83intJournalCount(t, ctx, store, account.ID, "provider_call_cogs"); got != 2 {
		t.Fatalf("provider COGS journals = %d, want one per payable attempt", got)
	}

	// Every included leg's exact immutable contribution ref survives the
	// durable leg row: the read-back observation resolves the identical
	// store/observation/revision/hash tuple the posting was derived from.
	for _, key := range cogs.IncludedLegKeys {
		readLeg, err := store.GetCallLegUsage(ctx, key)
		if err != nil {
			t.Fatalf("GetCallLegUsage(%q): %v", key, err)
		}
		if len(readLeg.Observations) != 1 {
			t.Fatalf("durable leg %q observations = %d, want the single contribution observation", key, len(readLeg.Observations))
		}
		readRef, err := readLeg.Observations[0].Ref(readLeg.Observations[0].Subject.StoreID)
		if err != nil {
			t.Fatalf("durable leg %q ref: %v", key, err)
		}
		if readRef != expectedRefs[key] {
			t.Fatalf("durable leg %q ref = %+v, want %+v", key, readRef, expectedRefs[key])
		}
	}

	// Durable explanation carries the multimodal contribution lineage.
	explained, err := store.CallExplanation(ctx, callID.String())
	if err != nil {
		t.Fatalf("CallExplanation: %v", err)
	}
	if len(explained.Legs) != 4 {
		t.Fatalf("explanation legs = %d, want all four durable legs", len(explained.Legs))
	}
	var winner *billing.CallLegUsageRecord
	explainedByID := make(map[string]*billing.CallLegUsageRecord)
	for i := range explained.Legs {
		if explained.Legs[i].BLegID == "b-winner" {
			winner = &explained.Legs[i]
		}
		explainedByID[explained.Legs[i].BLegID] = &explained.Legs[i]
	}
	if winner == nil || len(winner.Observations) != 1 {
		t.Fatalf("explanation has no winner observation lineage: %+v", explained.Legs)
	}
	lineage := map[string]string{}
	for _, measure := range winner.Observations[0].Measures {
		lineage[string(measure.Key.Direction)+"/"+measure.Key.Component+"/"+measure.Key.Unit] = measure.Value.Coefficient
	}
	for key, quantity := range map[string]string{
		"input/image/image": "1", "output/image/image": "1",
		"input/audio/second": "10", "output/audio/second": "5", "input/document/page": "2",
	} {
		if lineage[key] != quantity {
			t.Fatalf("durable multimodal lineage = %v, want %q=%q", lineage, key, quantity)
		}
	}
	// Every payable leg's explanation row carries the exact contribution ref
	// the posting was derived from; the never-started shell contributes no
	// observation lineage at all.
	for _, key := range cogs.IncludedLegKeys {
		var bLegID string
		for _, leg := range legs {
			sealed, err := leg.Seal()
			if err != nil {
				t.Fatal(err)
			}
			if sealed.Key == key {
				bLegID = leg.BLegID
			}
		}
		explainedLeg, ok := explainedByID[bLegID]
		if !ok || len(explainedLeg.Observations) != 1 {
			t.Fatalf("explanation lacks exactly one contribution observation for %q", key)
		}
		explainedRef, err := explainedLeg.Observations[0].Ref(explainedLeg.Observations[0].Subject.StoreID)
		if err != nil {
			t.Fatalf("explanation leg %q ref: %v", key, err)
		}
		if explainedRef != expectedRefs[key] {
			t.Fatalf("explanation leg %q ref = %+v, want %+v", key, explainedRef, expectedRefs[key])
		}
		if explainedRef.StoreID != ref83intStoreID || explainedRef.Revision != 1 || explainedRef.PayloadHash == "" {
			t.Fatalf("explanation leg %q ref tuple incomplete: %+v", key, explainedRef)
		}
	}
	if shell, ok := explainedByID["b-shell"]; !ok || len(shell.Observations) != 0 {
		t.Fatalf("shell explanation leg must carry no observation lineage: %+v", shell)
	}
	if len(explained.CustomerOperations) != 1 {
		t.Fatalf("customer operations = %+v, want exactly the winner-only settlement", explained.CustomerOperations)
	}
	customerOp := explained.CustomerOperations[0]
	if customerOp.SourceKey != callID.String() || customerOp.Fingerprint != rated.Fingerprint ||
		customerOp.OperationKind != "customer_call_settlement" || customerOp.Currency != "USD" {
		t.Fatalf("customer operation = %+v, want the exact RateCall result identity", customerOp)
	}
	if len(explained.ProviderCostOperations) != len(cogs.IncludedLegKeys) {
		t.Fatalf("provider operations = %+v, want exactly the payable COGS contribution set", explained.ProviderCostOperations)
	}
	providerOps := make(map[string]billing.OperationSnapshot)
	for _, op := range explained.ProviderCostOperations {
		providerOps[op.SourceKey] = op
	}
	for _, key := range cogs.IncludedLegKeys {
		op, ok := providerOps[key]
		if !ok {
			t.Fatalf("provider operations lack payable leg %q: %+v", key, explained.ProviderCostOperations)
		}
		wantFP, err := postedResults[key].SemanticFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if op.Fingerprint != wantFP || op.OperationKind != "provider_call_cogs" || op.Currency != "USD" {
			t.Fatalf("provider operation for %q = %+v, want the exact posted contribution identity", key, op)
		}
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	journalKeys := make(map[string]int)
	for _, transaction := range transactions {
		journalKeys[transaction.SourceKey]++
		if transaction.OperationKind == "provider_call_cogs" {
			key := transaction.CorrectionGroupID
			wantOpKey, err := billing.ProviderCostSourceKey(key)
			if err != nil {
				t.Fatal(err)
			}
			if transaction.SourceKey != wantOpKey || transaction.TurnID != callID.String() {
				t.Fatalf("provider journal = %+v, want exact %q lineage", transaction, key)
			}
		}
	}
	for _, key := range cogs.IncludedLegKeys {
		wantOpKey, err := billing.ProviderCostSourceKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if journalKeys[wantOpKey] != 1 {
			t.Fatalf("journal postings for %q = %d, want exactly one", key, journalKeys[wantOpKey])
		}
	}
	if journalKeys[wantSettlementKey] != 1 {
		t.Fatalf("journal postings for settlement %q = %d, want exactly one", wantSettlementKey, journalKeys[wantSettlementKey])
	}
	if !explained.Result.Processed || explained.Result.CustomerCharge.Nano != 1_970_000_000 ||
		explained.Result.ProviderCost.Nano != 4_750_000_000 {
		t.Fatalf("explanation result = %+v, want customer 1.97 and provider 4.75", explained.Result)
	}

	// Restart: close and reopen the same files; every durable fact survives
	// with identical identities, operation fingerprints, journal keys,
	// balances, journals, and explanation.
	beforeCustomerFP := customerOp.Fingerprint
	beforeProviderFP := make(map[string]string)
	for key, result := range postedResults {
		fingerprint, err := result.SemanticFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		beforeProviderFP[key] = fingerprint
	}
	beforeJournals := ref83intJournalSnapshot(t, ctx, store, account.ID)
	if len(beforeJournals) != 1+len(cogs.IncludedLegKeys) {
		t.Fatalf("pre-restart journals = %d, want one settlement plus one per payable leg", len(beforeJournals))
	}
	closeStore()
	store, closeStore = openRefinement82FileBillingStore(t, path, ref83intStoreID)
	t.Cleanup(func() { closeStore() })
	restartedAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restartedAccount.BalanceNano != gotAccount.BalanceNano || restartedAccount.Version != gotAccount.Version {
		t.Fatalf("restart changed account: before=%+v after=%+v", gotAccount, restartedAccount)
	}
	winnerBefore, err := store.GetCallLegUsage(ctx, ref83intSealedKey(t, legs[1]))
	if err != nil {
		t.Fatal(err)
	}
	restartedWinner, err := store.GetCallLegUsage(ctx, ref83intSealedKey(t, legs[1]))
	if err != nil {
		t.Fatal(err)
	}
	if restartedWinner.Fingerprint != winnerBefore.Fingerprint {
		t.Fatalf("restart changed winner fingerprint: before=%q after=%q", winnerBefore.Fingerprint, restartedWinner.Fingerprint)
	}
	// The complete per-leg ref tuple survives restart for every payable leg.
	for _, key := range cogs.IncludedLegKeys {
		restartedLeg, err := store.GetCallLegUsage(ctx, key)
		if err != nil {
			t.Fatalf("restarted GetCallLegUsage(%q): %v", key, err)
		}
		if len(restartedLeg.Observations) != 1 {
			t.Fatalf("restarted leg %q observations = %d, want one", key, len(restartedLeg.Observations))
		}
		restartedRef, err := restartedLeg.Observations[0].Ref(restartedLeg.Observations[0].Subject.StoreID)
		if err != nil {
			t.Fatalf("restarted leg %q ref: %v", key, err)
		}
		if restartedRef != expectedRefs[key] {
			t.Fatalf("restarted leg %q ref = %+v, want %+v", key, restartedRef, expectedRefs[key])
		}
	}
	restartedLegs, err := store.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restartedLegs) != 4 {
		t.Fatalf("restarted legs = %d, want four durable legs", len(restartedLegs))
	}
	restartedExplanation, err := store.CallExplanation(ctx, callID.String())
	if err != nil {
		t.Fatal(err)
	}
	if restartedExplanation.Result != explained.Result {
		t.Fatalf("restart changed explanation result: before=%+v after=%+v", explained.Result, restartedExplanation.Result)
	}
	if len(restartedExplanation.CustomerOperations) != 1 ||
		restartedExplanation.CustomerOperations[0].Fingerprint != beforeCustomerFP ||
		restartedExplanation.CustomerOperations[0].SourceKey != callID.String() {
		t.Fatalf("restart changed customer operation: before=%+v after=%+v", customerOp, restartedExplanation.CustomerOperations)
	}
	restartedProviderOps := make(map[string]string)
	for _, op := range restartedExplanation.ProviderCostOperations {
		restartedProviderOps[op.SourceKey] = op.Fingerprint
	}
	if len(restartedProviderOps) != len(beforeProviderFP) {
		t.Fatalf("restart changed provider operation set: before=%v after=%v", beforeProviderFP, restartedProviderOps)
	}
	for key, want := range beforeProviderFP {
		if restartedProviderOps[key] != want {
			t.Fatalf("restart changed provider operation for %q: before=%q after=%q", key, want, restartedProviderOps[key])
		}
	}
	restartedByID := make(map[string]billing.CallLegUsageRecord)
	for _, leg := range restartedExplanation.Legs {
		restartedByID[leg.BLegID] = leg
	}
	for _, key := range cogs.IncludedLegKeys {
		var bLegID string
		for _, leg := range legs {
			sealed, err := leg.Seal()
			if err != nil {
				t.Fatal(err)
			}
			if sealed.Key == key {
				bLegID = leg.BLegID
			}
		}
		restartedLeg, ok := restartedByID[bLegID]
		if !ok || len(restartedLeg.Observations) != 1 {
			t.Fatalf("restarted explanation lacks the contribution observation for %q", key)
		}
		restartedRef, err := restartedLeg.Observations[0].Ref(restartedLeg.Observations[0].Subject.StoreID)
		if err != nil {
			t.Fatalf("restarted explanation leg %q ref: %v", key, err)
		}
		if restartedRef != expectedRefs[key] {
			t.Fatalf("restarted explanation leg %q ref = %+v, want %+v", key, restartedRef, expectedRefs[key])
		}
	}
	// The full durable journal content survives restart: IDs, source keys,
	// operation kinds, turn/B-leg/correction lineage, currencies, sequences,
	// and complete ordered entries/amounts are byte-identical.
	ref83intRequireSameTransactions(t, beforeJournals, ref83intJournalSnapshot(t, ctx, store, account.ID))
}
