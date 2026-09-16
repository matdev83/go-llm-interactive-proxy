package billingstore

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// TestRefinement51ResumableSessionLifecycleAndSettlement exercises the real
// production AuthoritativeBilling store path coordinated with a real deterministic
// B2BUA MemoryStore with TTL retirement to prove:
//  1. Call 1 customer retail settlement closes and applies immediately without
//     waiting for A-leg retirement, idle expiry, or session finality.
//  2. Idle time on the A-leg produces zero new usage records, journal entries, or provider-cost work.
//  3. Call 2 on the same resumed A-leg receives a fresh BillingCallID and disjoint B-leg IDs
//     (coordinated with runtime allocator tests), settling independently without rewriting Call 1.
//  4. Continuity A-leg retirement is purely a storage/continuity concern; it fires the
//     synchronous retirement observer and purges B2BUA session state, while producing
//     ZERO new economic records, journals, or provider-cost work in the durable billing store.
//  5. Completed call economics existed before retirement and remain 100% immutable across retirement.
//     (In-flight stream cancellation semantics are verified via authentic runtime+host integration
//     in TestRefinement51InFlightCancellationAfterBLegStartPostsFixedFeeWithoutPhantomEconomics;
//     cancellation retains the legitimate configured 10-nano fixed request fee and no
//     phantom token/provider economics.)
func TestRefinement51ResumableSessionLifecycleAndSettlement(t *testing.T) {
	t.Parallel()

	// Deterministic virtual clock
	now := time.Unix(1_700_000_000, 0).UTC()
	var nowMu sync.Mutex
	getNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	advanceNow := func(d time.Duration) {
		nowMu.Lock()
		now = now.Add(d)
		nowMu.Unlock()
	}

	// 1. Real B2BUA MemoryStore with 10-minute TTL and synchronous retirement observer
	b2buaStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{
		TTL: 10 * time.Minute,
		Now: getNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	retiredALegs := make(chan string, 10)
	b2buaStore.SetALegRetirementObserver(func(retiredID string) {
		retiredALegs <- retiredID
	})

	// 2. Real durable SQLite billing store
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	account := billing.Account{
		ID:          "acct-refinement51",
		Currency:    "USD",
		Mode:        billing.AccountPrepaid,
		BalanceNano: 1000,
		State:       billing.AccountReady,
		Version:     1,
	}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}

	call1ID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "sess-refinement51-resumable"
	pricingRef := billing.VersionRef{ID: "pricing:v1", Version: "1"}
	policyRef := billing.VersionRef{ID: "policy:v1", Version: "1"}

	// Persist A-leg continuity in B2BUA store via production CreateALeg
	aLegRec, err := b2buaStore.CreateALeg(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	aLegID := aLegRec.ALegID

	// Allocate real B-leg slots in B2BUA store via production NextBLeg
	blegFail, err := b2buaStore.NextBLeg(ctx, aLegID)
	if err != nil {
		t.Fatal(err)
	}
	blegWin, err := b2buaStore.NextBLeg(ctx, aLegID)
	if err != nil {
		t.Fatal(err)
	}
	call1LegIDFail := blegFail.BLegID
	call1LegIDWin := blegWin.BLegID

	// Record B2BUA attempts for Call 1 (failed candidate + winning candidate)
	if err := b2buaStore.RecordAttempt(ctx, lipapi.AttemptRecord{
		ALegID:         aLegID,
		BLegID:         call1LegIDFail,
		Seq:            blegFail.Seq,
		BackendID:      "bad1",
		EffectiveModel: "model",
		Outcome:        lipapi.AttemptSwallowedFailure,
		Reason:         "parallel leg failed before winner",
		StartedAt:      getNow(),
		FinishedAt:     getNow(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := b2buaStore.RecordAttempt(ctx, lipapi.AttemptRecord{
		ALegID:         aLegID,
		BLegID:         call1LegIDWin,
		Seq:            blegWin.Seq,
		BackendID:      "ok",
		EffectiveModel: "model",
		Outcome:        lipapi.AttemptSuccess,
		Reason:         "success",
		StartedAt:      getNow(),
		FinishedAt:     getNow(),
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Call 1: Exposure admission and terminal handoff in durable billing store
	exposure1, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID:       account.ID,
		CallID:          call1ID.String(),
		Max:             billing.Money{Nano: 200, Currency: "USD"},
		PricingRef:      pricingRef,
		ChargePolicyRef: policyRef,
		Now:             getNow(),
	})
	if err != nil {
		t.Fatalf("admit exposure 1: %v", err)
	}
	if !exposure1.IsOpen() {
		t.Fatalf("exposure 1 not open: %+v", exposure1)
	}

	call1LegFail := billing.CallLegUsageRecord{
		CallID:          call1ID,
		ALegID:          aLegID,
		BLegID:          call1LegIDFail,
		AttemptSeq:      blegFail.Seq,
		BackendID:       "bad1",
		ProviderID:      "provider",
		ModelID:         "model",
		StartedAt:       getNow(),
		FinishedAt:      getNow().Add(2 * time.Second),
		Outcome:         billing.LegOutcomeFailed,
		Surfaced:        billing.SurfacedNo,
		OperatorRateRef: pricingRef,
	}
	if err := store.AppendCallLegUsage(ctx, call1LegFail); err != nil {
		t.Fatalf("append call 1 fail leg: %v", err)
	}

	// Call 1 winner leg populated with non-empty nested V2 economic data
	obsTime := getNow()
	measureVal := metering.Decimal{Coefficient: "25", Scale: 0}
	chargeAmt := metering.Decimal{Coefficient: "150", Scale: 2}
	call1Obs := metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-call1-ok",
		SourceEventKey: "src-call1-ok",
		Revision:       1,
		StreamID:       "stream-call1-ok",
		Sequence:       1,
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderResponse,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBLeg,
			StoreID:       "store-test",
			ALegID:        aLegID,
			BLegID:        call1LegIDWin,
			BillingCallID: call1ID.String(),
			AttemptSeq:    uint64(blegWin.Seq),
		},
		Correlation: metering.CorrelationV2{
			StoreID:           "store-test",
			ALegID:            aLegID,
			BLegID:            call1LegIDWin,
			BillingCallID:     call1ID.String(),
			AttemptSeq:        uint64(blegWin.Seq),
			ProviderRequestID: "req-call1-ok",
		},
		Semantics:  metering.SemanticsReplacement,
		ObservedAt: obsTime,
		ReceivedAt: obsTime,
		MappingRef: "provider.test.v1",
		Scope: scope.PrincipalScopeView{
			Roles:        []string{"role-operator", "role-billing"},
			SafeClaims:   map[string]string{"tier": "enterprise", "plan": "growth"},
			PolicyLabels: map[string]string{"env": "staging"},
		},
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction:  metering.DirectionInput,
				Component:  metering.ComponentInputToken,
				Unit:       metering.UnitToken,
				SchemaID:   metering.DefaultInclusionSchemaID,
				Dimensions: []metering.Dimension{{Name: "tier", Value: "std"}},
			},
			Value:   &measureVal,
			Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "item-call1-ok",
			Component: &metering.ComponentKey{
				Direction:  metering.DirectionOutput,
				Component:  metering.ComponentOutputToken,
				Unit:       metering.UnitToken,
				SchemaID:   "provider.tokens.v2",
				Dimensions: []metering.Dimension{{Name: "class", Value: "standard"}},
			},
			Amount:   &chargeAmt,
			Currency: "USD",
			Kind:     metering.ChargeKindComponent,
			Covers: []metering.ChargeCoverageRef{{
				Ref: metering.ChargeRef{
					StoreID:       "store-test",
					ObservationID: "obs-cov-call1",
					Revision:      1,
					ChargeItemID:  "item-cov-call1",
				},
				Relation: metering.CoverageInclusive,
			}},
		}},
		Evidence: []metering.SafeEvidenceField{{
			Path:        "x-request-id",
			Lexeme:      "req-12345",
			Present:     true,
			Acquisition: metering.AcquisitionProviderResponse,
		}},
		Supersedes: []metering.ObservationRef{{
			StoreID:       "store-test",
			ObservationID: "obs-prev-call1",
			Revision:      1,
			PayloadHash:   "hash-prev-call1",
		}},
	}
	call1Disp, err := billing.NewEconomicEvidenceDisposition(call1Obs, billing.EconomicEvidenceCoverageComplete, "canonical report")
	if err != nil {
		t.Fatalf("call1Obs.Validate: %v, construct economic disposition: %v", call1Obs.Validate(), err)
	}

	call1LegWin := billing.CallLegUsageRecord{
		CallID:                  call1ID,
		ALegID:                  aLegID,
		BLegID:                  call1LegIDWin,
		AttemptSeq:              blegWin.Seq,
		BackendID:               "ok",
		ProviderID:              "provider",
		ModelID:                 "model",
		StartedAt:               getNow(),
		FinishedAt:              getNow().Add(5 * time.Second),
		Outcome:                 billing.LegOutcomeWinner,
		Surfaced:                billing.SurfacedYes,
		OperatorRateRef:         pricingRef,
		EvidenceVersion:         billing.EvidenceFormatVersionV2,
		EvidenceProjection:      billing.EvidenceProjectionV1,
		Observations:            []metering.Observation{call1Obs},
		ObservationRefs:         []metering.ObservationRef{{StoreID: "store-test", ObservationID: "ref-call1-ok", Revision: 1, PayloadHash: "hash-call1-ok"}},
		EvidenceConflicts:       []billing.EvidenceConflict{{Identity: "ident-call1", ExistingHash: "hash-a", IncomingHash: "hash-b", ExistingCoverage: billing.EconomicEvidenceCoverageComplete, ExistingCoverageReason: "orig", IncomingCoverage: billing.EconomicEvidenceCoveragePartial, IncomingCoverageReason: "disp"}},
		EconomicEvidenceVersion: billing.EconomicEvidenceDispositionVersionV1,
		EconomicDispositions:    []billing.EconomicEvidenceDisposition{call1Disp},
	}
	if err := store.AppendCallLegUsage(ctx, call1LegWin); err != nil {
		t.Fatalf("append call 1 win leg: %v", err)
	}

	call1Closure := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             call1ID,
		AccountID:          account.ID,
		ALegID:             aLegID,
		SessionID:          sessionID,
		StartedAt:          getNow(),
		FinishedAt:         getNow().Add(5 * time.Second),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricingRef,
		ChargePolicyRef:    policyRef,
		ExpectedBLegIDs:    []string{call1LegIDFail, call1LegIDWin},
	}
	if err := store.AppendCallUsage(ctx, call1Closure); err != nil {
		t.Fatalf("append call 1 closure: %v", err)
	}

	call1Rating := billing.CallRatingResult{
		CallID:         call1ID,
		CustomerCharge: billing.Money{Nano: 75, Currency: "USD"},
		Fingerprint:    "rating-fp-call1",
	}

	// Requirement 3.1, 4.3: Customer retail settlement applies immediately
	// upon durable local call closure. It does NOT gate on A-leg retirement or session finality.
	settlement1, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call:     call1Closure,
		Exposure: exposure1,
		Result:   call1Rating,
	})
	if err != nil {
		t.Fatalf("settlement 1 before retirement: %v", err)
	}
	if settlement1.Replayed || settlement1.Customer.Transaction.ID == "" {
		t.Fatalf("expected fresh customer settlement: %+v", settlement1)
	}

	acctAfterCall1, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if acctAfterCall1.BalanceNano != 925 || acctAfterCall1.Version != 2 {
		t.Fatalf("account after call 1 settlement: balance=%d version=%d, want 925/2", acctAfterCall1.BalanceNano, acctAfterCall1.Version)
	}

	// Snapshot all relevant durable rows before idle/retirement:
	// journal transactions, call legs, call usage closures, and pending provider cost work.
	txsBeforeIdle, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txsBeforeIdle) != 1 {
		t.Fatalf("transactions after call 1 settlement = %d, want 1", len(txsBeforeIdle))
	}

	call1LegsBeforeIdle, err := store.ListCallLegUsage(ctx, call1ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(call1LegsBeforeIdle) != 2 {
		t.Fatalf("call 1 legs before idle = %d, want 2", len(call1LegsBeforeIdle))
	}
	for _, leg := range call1LegsBeforeIdle {
		switch leg.BLegID {
		case call1LegIDFail:
			if leg.AttemptSeq != blegFail.Seq {
				t.Fatalf("call 1 fail leg AttemptSeq = %d, want blegFail.Seq %d", leg.AttemptSeq, blegFail.Seq)
			}
		case call1LegIDWin:
			if leg.AttemptSeq != blegWin.Seq {
				t.Fatalf("call 1 win leg AttemptSeq = %d, want blegWin.Seq %d", leg.AttemptSeq, blegWin.Seq)
			}
		default:
			t.Fatalf("unexpected call 1 leg in store: %q", leg.BLegID)
		}
	}

	callUsageBeforeIdle, err := store.ListCallUsage(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(callUsageBeforeIdle) != 1 {
		t.Fatalf("call closures before idle = %d, want 1", len(callUsageBeforeIdle))
	}

	providerCostWorkBeforeIdle, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	custWorkBeforeIdle, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueCustomer, 10)
	if err != nil {
		t.Fatal(err)
	}
	opWorkBeforeIdle, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Idle lifecycle phase: 2 minutes elapse (within 10m TTL)
	advanceNow(2 * time.Minute)

	// B2BUA session is active and queryable during idle
	attemptsDuringIdle, err := b2buaStore.LoadAttempts(ctx, aLegID)
	if err != nil {
		t.Fatalf("LoadAttempts during idle: %v", err)
	}
	if len(attemptsDuringIdle) != 2 {
		t.Fatalf("attempts during idle = %d, want 2", len(attemptsDuringIdle))
	}

	// Verify that idle time creates zero new records, zero new journals, zero new work, and no balance changes
	txsAfterIdle, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txsAfterIdle) != len(txsBeforeIdle) {
		t.Fatalf("idle created journal transactions: before=%d after=%d", len(txsBeforeIdle), len(txsAfterIdle))
	}
	callUsageAfterIdle, err := store.ListCallUsage(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(callUsageAfterIdle, callUsageBeforeIdle) {
		t.Fatalf("idle altered call usage rows: before=%#v after=%#v", callUsageBeforeIdle, callUsageAfterIdle)
	}
	callLegsAfterIdle, err := store.ListCallLegUsage(ctx, call1ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(callLegsAfterIdle, call1LegsBeforeIdle) {
		t.Fatalf("idle altered call leg rows: before=%#v after=%#v", call1LegsBeforeIdle, callLegsAfterIdle)
	}
	providerCostWorkAfterIdle, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(providerCostWorkAfterIdle, providerCostWorkBeforeIdle) {
		t.Fatalf("idle altered provider cost work: before=%#v after=%#v", providerCostWorkBeforeIdle, providerCostWorkAfterIdle)
	}
	custWorkAfterIdle, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueCustomer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(custWorkAfterIdle, custWorkBeforeIdle) {
		t.Fatalf("idle altered customer valuation work: before=%#v after=%#v", custWorkBeforeIdle, custWorkAfterIdle)
	}
	opWorkAfterIdle, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opWorkAfterIdle, opWorkBeforeIdle) {
		t.Fatalf("idle altered operator valuation work: before=%#v after=%#v", opWorkBeforeIdle, opWorkAfterIdle)
	}
	acctAfterIdle, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if acctAfterIdle.BalanceNano != acctAfterCall1.BalanceNano || acctAfterIdle.Version != acctAfterCall1.Version {
		t.Fatalf("idle altered balance/version: before=%d/%d after=%d/%d", acctAfterCall1.BalanceNano, acctAfterCall1.Version, acctAfterIdle.BalanceNano, acctAfterIdle.Version)
	}

	// 3. Call 2: Resumed invocation on same A-leg (during idle window, before TTL retirement)
	// Requirement 3.3: Allocate a real B-leg slot from the active B2BUA store via NextBLeg
	// and prove independent exposure admission, append, and settlement apply cleanly without
	// rewriting Call 1. Note that runtime allocator dynamic B-leg allocation through Executor
	// is verified in TestRefinement51SequentialCallsKeepLocalClosureAndALegContinuity.
	call2LegSlot, err := b2buaStore.NextBLeg(ctx, aLegID)
	if err != nil {
		t.Fatalf("allocate call 2 B-leg from B2BUA store: %v", err)
	}
	if call2LegSlot.Seq != 3 {
		t.Fatalf("call 2 B-leg seq = %d, want 3 (following blegFail(1) and blegWin(2))", call2LegSlot.Seq)
	}
	call2LegID := call2LegSlot.BLegID
	if call2LegID == call1LegIDFail || call2LegID == call1LegIDWin {
		t.Fatalf("call 2 B-leg ID %q reuses an existing B-leg ID", call2LegID)
	}
	if err := b2buaStore.RecordAttempt(ctx, lipapi.AttemptRecord{
		ALegID:         aLegID,
		BLegID:         call2LegID,
		Seq:            call2LegSlot.Seq,
		BackendID:      "ok",
		EffectiveModel: "model",
		Outcome:        lipapi.AttemptSuccess,
		Reason:         "success",
		StartedAt:      getNow(),
		FinishedAt:     getNow(),
	}); err != nil {
		t.Fatal(err)
	}

	call2ID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if call2ID == call1ID {
		t.Fatal("call 2 BillingCallID must differ from call 1")
	}

	exposure2, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID:       account.ID,
		CallID:          call2ID.String(),
		Max:             billing.Money{Nano: 200, Currency: "USD"},
		PricingRef:      pricingRef,
		ChargePolicyRef: policyRef,
		Now:             getNow(),
	})
	if err != nil {
		t.Fatalf("admit exposure 2: %v", err)
	}

	call2Leg := billing.CallLegUsageRecord{
		CallID:          call2ID,
		ALegID:          aLegID,
		BLegID:          call2LegID,
		AttemptSeq:      call2LegSlot.Seq,
		BackendID:       "ok",
		ProviderID:      "provider",
		ModelID:         "model",
		StartedAt:       getNow(),
		FinishedAt:      getNow().Add(5 * time.Second),
		Outcome:         billing.LegOutcomeWinner,
		Surfaced:        billing.SurfacedYes,
		OperatorRateRef: pricingRef,
	}
	if err := store.AppendCallLegUsage(ctx, call2Leg); err != nil {
		t.Fatalf("append call 2 leg: %v", err)
	}

	call2Closure := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             call2ID,
		AccountID:          account.ID,
		ALegID:             aLegID,
		SessionID:          sessionID,
		StartedAt:          getNow(),
		FinishedAt:         getNow().Add(5 * time.Second),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricingRef,
		ChargePolicyRef:    policyRef,
		ExpectedBLegIDs:    []string{call2LegID},
	}
	if err := store.AppendCallUsage(ctx, call2Closure); err != nil {
		t.Fatalf("append call 2 closure: %v", err)
	}

	call2Rating := billing.CallRatingResult{
		CallID:         call2ID,
		CustomerCharge: billing.Money{Nano: 50, Currency: "USD"},
		Fingerprint:    "rating-fp-call2",
	}
	settlement2, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call:     call2Closure,
		Exposure: exposure2,
		Result:   call2Rating,
	})
	if err != nil {
		t.Fatalf("settlement 2: %v", err)
	}
	if settlement2.Replayed || settlement2.Customer.Transaction.ID == "" {
		t.Fatalf("expected fresh call 2 settlement: %+v", settlement2)
	}

	acctAfterCall2, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if acctAfterCall2.BalanceNano != 875 || acctAfterCall2.Version != 3 {
		t.Fatalf("account after call 2: balance=%d version=%d, want 875/3", acctAfterCall2.BalanceNano, acctAfterCall2.Version)
	}

	txsAfterCall2, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txsAfterCall2) != 2 {
		t.Fatalf("transactions after call 2 = %d, want 2", len(txsAfterCall2))
	}

	// Deep verify Call 1 leg immutability after Call 2
	call1LegsFinal, err := store.ListCallLegUsage(ctx, call1ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(call1LegsFinal, call1LegsBeforeIdle) {
		t.Fatalf("call 1 legs mutated after call 2:\n before=%#v\n after=%#v", call1LegsBeforeIdle, call1LegsFinal)
	}

	// Deep verify Call 1 closure immutability after Call 2
	call1ClosureFinal, err := store.GetCallUsage(ctx, call1ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(call1ClosureFinal, callUsageBeforeIdle[0]) {
		t.Fatalf("call 1 closure mutated after call 2:\n before=%#v\n after=%#v", callUsageBeforeIdle[0], call1ClosureFinal)
	}

	// Verify disjointness and sequence correlation in store
	call2LegsFinal, err := store.ListCallLegUsage(ctx, call2ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(call2LegsFinal) != 1 {
		t.Fatalf("call 2 legs = %d, want 1", len(call2LegsFinal))
	}
	if call2LegsFinal[0].AttemptSeq != call2LegSlot.Seq {
		t.Fatalf("call 2 leg AttemptSeq in store = %d, want %d", call2LegsFinal[0].AttemptSeq, call2LegSlot.Seq)
	}
	for _, c1Leg := range call1LegsFinal {
		if call2LegsFinal[0].BLegID == c1Leg.BLegID {
			t.Fatalf("call 2 B-leg %q aliases call 1 B-leg %q in store", call2LegsFinal[0].BLegID, c1Leg.BLegID)
		}
	}
	// Confirm strict attempt sequence progression across all attempts on this A-leg
	if !(blegFail.Seq < blegWin.Seq && blegWin.Seq < call2LegSlot.Seq) {
		t.Fatalf("attempt sequences do not progress strictly monotonically: %d < %d < %d",
			blegFail.Seq, blegWin.Seq, call2LegSlot.Seq)
	}

	// 4. Real deterministic MemoryStore retirement phase:
	// Snapshot ALL relevant durable SQLite tables before retirement.
	txsBeforeRetirement, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txsBeforeRetirement) != 2 {
		t.Fatalf("transactions before retirement = %d, want 2", len(txsBeforeRetirement))
	}
	callUsageBeforeRetirement, err := store.ListCallUsage(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(callUsageBeforeRetirement) != 2 {
		t.Fatalf("call usage before retirement = %d, want 2", len(callUsageBeforeRetirement))
	}
	call1LegsBeforeRetirement, err := store.ListCallLegUsage(ctx, call1ID)
	if err != nil {
		t.Fatal(err)
	}
	call2LegsBeforeRetirement, err := store.ListCallLegUsage(ctx, call2ID)
	if err != nil {
		t.Fatal(err)
	}
	providerCostWorkBeforeRetirement, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	custWorkBeforeRetirement, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueCustomer, 10)
	if err != nil {
		t.Fatal(err)
	}
	opWorkBeforeRetirement, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	acctBeforeRetirement, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Advance virtual clock past 10m TTL (+15m total)
	advanceNow(15 * time.Minute)

	// Trigger lazy sweep / eviction via LoadAttempts on the A-leg in B2BUA MemoryStore
	_, err = b2buaStore.LoadAttempts(ctx, aLegID)
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("expected ErrALegNotFound on expired session in B2BUA store, got %v", err)
	}

	// Verify retirement observer fired synchronously for aLegID (deterministic check without sleeps)
	select {
	case retiredID := <-retiredALegs:
		if retiredID != aLegID {
			t.Fatalf("retired A-leg = %q, want %q", retiredID, aLegID)
		}
	default:
		t.Fatal("retirement observer did not fire synchronously on B2BUA LoadAttempts eviction")
	}

	// Requirement 3.6: Retention or retirement of an A-leg is a continuity/storage
	// concern and shall NOT create previously unrecognized usage, supplier cost, or journal entries.
	// Snapshot and verify all durable SQLite billing tables immediately following retirement:
	// - Completed call economics existed BEFORE retirement and remain completely intact.
	// - Zero new journal transactions are emitted (count remains exactly 2).
	// - Call 1 & Call 2 leg records remain byte-for-byte / deep identical across retirement.
	// - Call 1 & Call 2 usage closures remain identical across retirement.
	// - Pending provider-cost work remains identical across retirement.
	// - Pending economic revision work remains identical across retirement.
	// - Account balance remains unchanged across retirement.
	txsAfterRetirement, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txsAfterRetirement) != len(txsBeforeRetirement) {
		t.Fatalf("transactions after retirement = %d, want exactly %d (no retirement journals)", len(txsAfterRetirement), len(txsBeforeRetirement))
	}
	if !reflect.DeepEqual(txsAfterRetirement, txsBeforeRetirement) {
		t.Fatalf("transactions changed across retirement: before=%#v after=%#v", txsBeforeRetirement, txsAfterRetirement)
	}

	call1LegsAfterRetirement, err := store.ListCallLegUsage(ctx, call1ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(call1LegsAfterRetirement, call1LegsBeforeRetirement) {
		t.Fatalf("call 1 legs changed across retirement:\n before=%#v\n after=%#v", call1LegsBeforeRetirement, call1LegsAfterRetirement)
	}

	call2LegsAfterRetirement, err := store.ListCallLegUsage(ctx, call2ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(call2LegsAfterRetirement, call2LegsBeforeRetirement) {
		t.Fatalf("call 2 legs changed across retirement:\n before=%#v\n after=%#v", call2LegsBeforeRetirement, call2LegsAfterRetirement)
	}

	callUsageAfterRetirement, err := store.ListCallUsage(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(callUsageAfterRetirement, callUsageBeforeRetirement) {
		t.Fatalf("call closures changed across retirement:\n before=%#v\n after=%#v", callUsageBeforeRetirement, callUsageAfterRetirement)
	}

	providerCostWorkAfterRetirement, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(providerCostWorkAfterRetirement, providerCostWorkBeforeRetirement) {
		t.Fatalf("provider cost work changed across retirement: before=%#v after=%#v", providerCostWorkBeforeRetirement, providerCostWorkAfterRetirement)
	}

	custWorkAfterRetirement, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueCustomer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(custWorkAfterRetirement, custWorkBeforeRetirement) {
		t.Fatalf("customer valuation work changed across retirement: before=%#v after=%#v", custWorkBeforeRetirement, custWorkAfterRetirement)
	}

	opWorkAfterRetirement, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opWorkAfterRetirement, opWorkBeforeRetirement) {
		t.Fatalf("operator valuation work changed across retirement: before=%#v after=%#v", opWorkBeforeRetirement, opWorkAfterRetirement)
	}

	acctAfterRetirement, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(acctAfterRetirement, acctBeforeRetirement) {
		t.Fatalf("account altered by retirement: before=%#v after=%#v", acctBeforeRetirement, acctAfterRetirement)
	}
}
