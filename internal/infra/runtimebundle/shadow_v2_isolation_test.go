package runtimebundle_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var (
	errC4NonIntegerQuantity = errors.New("c4: non-integer quantity")
	errC4QuantityOverflow   = errors.New("c4: quantity overflow")
)

func openC4FileStore(t *testing.T, path string) (*billingstore.DurableStore, *sql.DB) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "test"})
	require.NoError(t, err)
	return store, sqlDB
}

func openC4FileJournal(t *testing.T, path string) (*journalstore.DurableStore, *sql.DB) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	journal, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "test"})
	require.NoError(t, err)
	return journal, sqlDB
}

//nolint:revive // test helper keeps t first per Go testing convention
func c4JournalObservations(t *testing.T, ctx context.Context, journal *journalstore.DurableStore, bLegID string) []metering.Observation {
	t.Helper()
	page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: "test", SubjectKind: metering.SubjectBLeg, SubjectID: bLegID, Limit: 100,
	})
	require.NoError(t, err)
	require.Empty(t, page.NextCursor)
	return page.Observations
}

type c4QuantityRater struct {
	rateNano int64
	currency string
}

func (r *c4QuantityRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	var totalTokens int64
	for _, observation := range input.Observations {
		for _, measure := range observation.Measures {
			if measure.Value == nil {
				continue
			}
			if measure.Value.Scale != 0 {
				return economics.Valuation{}, errC4NonIntegerQuantity
			}
			qty, err := strconv.ParseInt(measure.Value.Coefficient, 10, 64)
			if err != nil || qty < 0 {
				return economics.Valuation{}, errC4NonIntegerQuantity
			}
			if totalTokens > math.MaxInt64-qty {
				return economics.Valuation{}, errC4QuantityOverflow
			}
			totalTokens += qty
		}
	}
	if totalTokens > 0 && r.rateNano > math.MaxInt64/totalTokens {
		return economics.Valuation{}, errC4QuantityOverflow
	}
	amountNano := totalTokens * r.rateNano
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		refs = append([]metering.ObservationRef(nil), input.ObservationRefs...)
	}
	amountDecimal := metering.Decimal{Coefficient: strconv.FormatInt(amountNano, 10), Scale: 9}
	currency := r.currency
	if currency == "" {
		currency = "USD"
	}
	// The valuation carries one line per rated measure so reconciliation
	// compares real component quantities rather than empty envelopes.
	lines := make([]economics.LineItem, 0, len(input.Observations))
	for _, observation := range input.Observations {
		obsRef, refErr := observation.Ref(input.Subject.StoreID)
		if refErr != nil {
			return economics.Valuation{}, refErr
		}
		for _, measure := range observation.Measures {
			if measure.Value == nil || measure.Key.Component == "" || measure.Value.Scale != 0 {
				continue
			}
			qty, err := strconv.ParseInt(measure.Value.Coefficient, 10, 64)
			if err != nil || qty < 0 || (qty > 0 && r.rateNano > math.MaxInt64/qty) {
				return economics.Valuation{}, errC4QuantityOverflow
			}
			lineNano := qty * r.rateNano
			lineAmount := metering.Decimal{Coefficient: strconv.FormatInt(lineNano, 10), Scale: 9}
			linePrice := metering.Decimal{Coefficient: strconv.FormatInt(r.rateNano, 10), Scale: 9}
			key := measure.Key.Clone()
			lineQty := *measure.Value
			lines = append(lines, economics.LineItem{
				ID: "line-c4-" + observation.ID, RuleID: "rule-c4", ItemID: "item-c4-" + observation.ID,
				Component: &key, Quantity: &lineQty, Unit: key.Unit,
				UnitPrice:     &linePrice,
				Amount:        &lineAmount,
				RoundedAmount: &economics.Money{NanoUnits: lineNano, Currency: currency, Present: true},
				RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfAwayFromZero,
				Status:                economics.RatingLineRated,
				SourceObservationRefs: []metering.ObservationRef{obsRef},
			})
		}
	}
	return economics.Valuation{
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs,
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), input.AllocationCoverageRefs...),
		Payer:                  input.Payer,
		Lines:                  lines,
		Totals: []economics.CurrencyTotal{{
			Currency:      currency,
			Amount:        &amountDecimal,
			RoundedAmount: economics.Money{NanoUnits: amountNano, Currency: currency, Present: true},
		}},
		Completeness: economics.CompletenessPartial,
	}, nil
}

type c4ProviderCostResolver struct{}

func (c4ProviderCostResolver) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	return billing.RateProviderCost(leg, billing.OperatorRateSet{}, "USD")
}

const (
	c4AccountID = "shadow-c4-acct"
	c4ALeg      = "a-c4"
	c4StoreID   = "test"
)

func c4Observation(t *testing.T, callID billing.BillingCallID, bLegID, obsID string, revision uint64) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_190_000+int64(revision), 0).UTC()
	component := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: "vendor:tokens",
		Unit:      metering.UnitToken,
		SchemaID:  "vendor:meter:v1",
	}
	value := metering.Decimal{Coefficient: "5", Scale: 0}
	return metering.Observation{
		Version: 2, ID: obsID, SourceEventKey: "source-" + obsID, Revision: revision,
		StreamID: "stream-" + bLegID, Sequence: revision,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: c4StoreID, TenantID: "tenant-c4",
			AccountID: c4AccountID, ALegID: c4ALeg, BillingCallID: callID.String(),
			CallID: callID.String(), BLegID: bLegID, AttemptID: "attempt-c4", AttemptSeq: revision,
			ProviderAccountKey: "provider-account-c4",
		},
		Correlation: metering.CorrelationV2{
			StoreID: c4StoreID, TenantID: "tenant-c4", CallID: callID.String(),
			BillingCallID: callID.String(), ALegID: c4ALeg, BLegID: bLegID,
			AttemptID: "attempt-c4", AttemptSeq: revision,
			ProviderAccountKey: "provider-account-c4",
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now,
		MappingRef: "c4:v1",
		Measures:   []metering.Measure{{Key: component, Value: &value, Quality: metering.QualityObserved}},
	}
}

func c4V1Leg(t *testing.T, callID billing.BillingCallID, bLegID string) billing.CallLegUsageRecord {
	t.Helper()
	return billing.CallLegUsageRecord{
		CallID: callID, ALegID: c4ALeg, BLegID: bLegID, AttemptSeq: 1,
		BackendID: "backend-c4", ProviderID: "provider-c4", ModelID: "model-c4",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 7, Present: true},
			OutputTokens: billing.Quantity{Value: 3, Present: true},
			Cost:         billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
			DedupeKey:    "provider-charge-c4-" + bLegID,
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v4"},
	}
}

func c4V2Leg(t *testing.T, callID billing.BillingCallID, bLegID, obsID string, revision uint64) billing.CallLegUsageRecord {
	t.Helper()
	leg := c4V1Leg(t, callID, bLegID)
	leg.AttemptSeq = int(revision)
	leg.EvidenceVersion = billing.EvidenceFormatVersionV2
	leg.EvidenceProjection = billing.EvidenceProjectionV1
	observation := c4Observation(t, callID, bLegID, obsID, revision)
	observation.Subject.AttemptSeq = revision
	observation.Correlation.AttemptSeq = revision
	leg.Observations = []metering.Observation{observation}
	return leg
}

func c4Call(t *testing.T, callID billing.BillingCallID, aLeg string, bLegIDs ...string) billing.CallUsageRecord {
	t.Helper()
	return billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: c4AccountID, ALegID: aLeg, SessionID: "session-c4",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    bLegIDs,
	}
}

func c4RatingWork(t *testing.T, observation metering.Observation, headKey string, revision uint64) billing.EconomicRevisionWork {
	t.Helper()
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey,
		Subject: observation.Subject, EvidenceRevision: revision, Input: input,
		CreatedAt: time.Unix(1_700_191_000+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

func c4ReconWork(t *testing.T, rating billing.EconomicRevisionWork) billing.EconomicRevisionWork {
	t.Helper()
	normalized, err := rating.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	callID, err := billing.ParseBillingCallID(normalized.Subject.BillingCallID)
	require.NoError(t, err)
	observation := c4Observation(t, callID, normalized.Subject.BLegID, "c4-recon-evidence", 32)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, Kind: billing.EconomicWorkKindReconciliation,
		HeadKey: "c4-recon-head", Subject: observation.Subject,
		EvidenceRevision: 32, Input: input, Dependencies: []billing.EconomicJobDependency{dependency},
		CreatedAt: time.Unix(1_700_191_500, 0).UTC(),
	}
	normalizedRecon, err := work.Normalize()
	require.NoError(t, err)
	return normalizedRecon
}

type c4FinancialSnapshot struct {
	Balance        int64
	AccountVersion uint64
	JournalIDs     []string
	CogsCount      int
	OpenExposures  []string
	SelectedHead   billing.SelectedCostHead
	UnitAfter      billing.CustomerUnitBalance
}

//nolint:revive // test helper keeps t first per Go testing convention
func c4CaptureFinancial(t *testing.T, ctx context.Context, store *billingstore.DurableStore, selectedCallID billing.BillingCallID, unitKey billing.CustomerUnitKey) c4FinancialSnapshot {
	t.Helper()
	account, err := store.GetAccount(ctx, c4AccountID)
	require.NoError(t, err)
	journals, err := store.JournalTransactions(ctx, c4AccountID)
	require.NoError(t, err)
	ids := make([]string, 0, len(journals))
	cogs := 0
	for _, journal := range journals {
		ids = append(ids, journal.ID)
		if journal.OperationKind == "provider_call_cogs" {
			cogs++
		}
	}
	page, err := store.QueryOpenExposures(ctx, c4AccountID, billing.PageRequest{Limit: 100})
	require.NoError(t, err)
	open := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		open = append(open, item.CallID)
	}
	head, err := store.GetSelectedCostHead(ctx, c4AccountID, selectedCallID, "c4-selected-head")
	require.NoError(t, err)
	unitAfter, err := store.CustomerUnitBalance(ctx, unitKey)
	require.NoError(t, err)
	return c4FinancialSnapshot{
		Balance: account.BalanceNano, AccountVersion: account.Version,
		JournalIDs: ids, CogsCount: cogs, OpenExposures: open,
		SelectedHead: head, UnitAfter: unitAfter,
	}
}

//nolint:revive // test helper keeps t first per Go testing convention
func c4JournalByID(t *testing.T, ctx context.Context, store *billingstore.DurableStore, id string) billing.JournalTransaction {
	t.Helper()
	journals, err := store.JournalTransactions(ctx, c4AccountID)
	require.NoError(t, err)
	for _, journal := range journals {
		if journal.ID == id {
			return journal
		}
	}
	t.Fatalf("journal %s not found", id)
	return billing.JournalTransaction{}
}

func TestPhase172Cluster4ComposedNoPostLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c4.sqlite")
	journalPath := filepath.Join(t.TempDir(), "c4-journal.sqlite")
	store, sqlDB := openC4FileStore(t, path)
	journal, journalDB := openC4FileJournal(t, journalPath)
	ctx := context.Background()

	account := billing.Account{ID: c4AccountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	_, err := store.PostFunding(ctx, billing.FundingInput{AccountID: c4AccountID, Amount: billing.Money{Nano: 500_000, Currency: "USD"}, SourceKey: "c4-funding-1", Reason: "c4 baseline funding"})
	require.NoError(t, err)

	unitKey := billing.CustomerUnitKey{
		AccountID: c4AccountID, PoolID: "c4-pool", PeriodID: "c4-period",
		Component: metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentCredit, Unit: metering.UnitCredit},
	}
	grant := billing.CustomerUnitOperation{
		Version: billing.CustomerUnitOperationVersionV1, OperationID: "c4-unit-grant-1",
		Key: unitKey, Kind: billing.CustomerUnitOperationGrant, Source: billing.CustomerUnitOperationSourceCustomerProvisioning,
		Quantity: metering.Decimal{Coefficient: "5"}, ExpectedVersion: 0, Fence: 1,
	}
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	require.NoError(t, err)
	require.False(t, grantResult.Replayed)

	v1CallID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	v1Leg := c4V1Leg(t, v1CallID, "b-c4-v1")
	require.NoError(t, store.AppendCallLegUsage(ctx, v1Leg))
	sealedV1Leg, err := v1Leg.Seal()
	require.NoError(t, err)
	v1ProviderInput := billing.ApplyProviderCostInput{
		AccountID: c4AccountID, CallID: v1CallID, Leg: v1Leg,
		Result: billing.OperatorCostResult{LURKey: sealedV1Leg.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
	}
	v1Posting, err := store.ApplyProviderCost(ctx, v1ProviderInput)
	require.NoError(t, err)
	require.False(t, v1Posting.Replayed)

	v1Call := c4Call(t, v1CallID, c4ALeg, "b-c4-v1")
	require.NoError(t, store.AppendCallUsage(ctx, v1Call))
	v1Exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: c4AccountID, CallID: v1CallID.String(),
		Max: billing.Money{Nano: 100_000, Currency: "USD"}, PricingRef: v1Call.CustomerPricingRef, ChargePolicyRef: v1Call.ChargePolicyRef,
	})
	require.NoError(t, err)
	v1SettlementInput := billing.ApplyCallBillingInput{
		Call: v1Call, Exposure: v1Exposure,
		Result: billing.CallRatingResult{CallID: v1CallID, CustomerCharge: billing.Money{Nano: 25_000, Currency: "USD"}, Fingerprint: "c4-v1-result"},
	}
	v1Settlement, err := store.ApplyCallBillingResult(ctx, v1SettlementInput)
	require.NoError(t, err)
	require.False(t, v1Settlement.Replayed)

	v1OpenCallID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	require.NoError(t, store.AppendCallUsage(ctx, c4Call(t, v1OpenCallID, c4ALeg)))
	_, err = store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: c4AccountID, CallID: v1OpenCallID.String(),
		Max:             billing.Money{Nano: 60_000, Currency: "USD"},
		PricingRef:      billing.VersionRef{ID: "prices", Version: "v1"},
		ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
	})
	require.NoError(t, err)

	selectedCallID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	selectedSubject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: c4StoreID, AccountID: c4AccountID,
		ALegID: c4ALeg, BillingCallID: selectedCallID.String(), BLegID: "b-c4-selected",
	}
	selectedDecimal := metering.DecimalFromNanoUnits(10_000_000_000)
	selectedValuation, err := billing.NewSelectedCostValuation(
		billing.SelectedCostValuationRef{ValuationID: "c4-sel-v1", Revision: 1, InputSetHash: "0000000000000000000000000000000000000000000000000000000000000001"},
		billing.OperatorCostSelectionResult{
			Status: billing.OperatorCostSelectionStatusFinal, Provenance: billing.OperatorCostProvenanceAttempted,
			Currency: "USD", Amount: &billing.MonetaryExactAmount{Currency: "USD", Decimal: &selectedDecimal},
		})
	require.NoError(t, err)
	selectedInput := billing.SelectedCostAdjustmentInput{
		AccountID: c4AccountID,
		CallID:    selectedCallID, HeadKey: "c4-selected-head", Subject: selectedSubject,
		Expected: billing.SelectedCostHeadExpectation{}, Selected: selectedValuation,
	}
	_, err = store.ApplySelectedCostAdjustment(ctx, selectedInput)
	require.NoError(t, err)

	baseline := c4CaptureFinancial(t, ctx, store, selectedCallID, unitKey)
	require.Equal(t, int64(1_475_000), baseline.Balance)
	require.Equal(t, "5/0", baseline.UnitAfter.Available.CanonicalString(), "seeded unit baseline must be nonzero and current")
	require.Equal(t, 2, baseline.CogsCount)
	require.Equal(t, []string{v1OpenCallID.String()}, baseline.OpenExposures)
	pending, err := store.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, pending)

	handle, err := runtimebundle.ComposeShadowV2Capture(runtimebundle.ShadowV2CaptureInput{
		StoreID: c4StoreID, MaxObservations: 16,
		EvidenceSink: journal, WorkAppender: store, ResultStore: store, ReconciliationStore: store,
		Rater: &c4QuantityRater{rateNano: 1_000_000, currency: "USD"}, V1Settlement: store,
	})
	require.NoError(t, err)

	shadowCallID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	// Shadow observes a distinct execution purely through V2 observations:
	// no ordinary V1 leg/call rows or claim state may be created.
	shadowObservation := c4Observation(t, shadowCallID, "b-c4-shadow", "c4-shadow-obs", 31)
	require.NoError(t, handle.CaptureObservations(ctx, []metering.Observation{shadowObservation}))
	require.NoError(t, handle.CaptureObservations(ctx, []metering.Observation{shadowObservation}))
	require.Len(t, c4JournalObservations(t, ctx, journal, "b-c4-shadow"), 1, "shadow V2 observation must be queryable without V1 rows")
	_, err = store.GetCallUsage(ctx, shadowCallID)
	require.Error(t, err, "shadow-only identity must own no ordinary V1 call row")
	ratingWork := c4RatingWork(t, shadowObservation, "c4-shadow-head", 31)
	require.NoError(t, handle.AppendWork(ctx, ratingWork))
	require.NoError(t, handle.RateAndPersist(ctx, ratingWork))
	ratingIdentity, err := ratingWork.Identity()
	require.NoError(t, err)
	persistedValuation, err := store.GetValuation(ctx, ratingIdentity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, ratingIdentity.ValuationKey(), persistedValuation.ID)
	require.Len(t, persistedValuation.Totals, 1)
	require.Equal(t, int64(5_000_000), persistedValuation.Totals[0].RoundedAmount.NanoUnits)

	reconWork := c4ReconWork(t, ratingWork)
	require.NoError(t, handle.AppendWork(ctx, reconWork))

	// Reconciliation runs through the existing pure job-runner seam with the
	// production comparison reconciler: the envelope below is computed from
	// the persisted dependency valuation, never hand-authored.
	reconRunner, err := billing.NewEconomicJobRunner(billing.EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater:      &c4QuantityRater{rateNano: 1_000_000, currency: "USD"},
		Reconciler: billing.ComponentComparisonReconciler{},
		Owner:      "c4-recon", Batch: 8,
	})
	require.NoError(t, err)
	reconSummary, err := reconRunner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 2, reconSummary.Completed)
	require.Equal(t, 0, reconSummary.Failed+reconSummary.Retried)

	reconIdentity, err := reconWork.Identity()
	require.NoError(t, err)
	record, err := store.GetReconciliation(ctx, reconIdentity.ReconciliationKey(), reconIdentity.EvidenceRevision)
	require.NoError(t, err)
	require.Equal(t, reconIdentity.InputSetHash, record.InputSetHash)
	var decoded struct {
		Status string `json:"status"`
		Items  []struct {
			Component        string  `json:"component"`
			Status           string  `json:"status"`
			ProviderQuantity *string `json:"provider_quantity"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(record.ResultJSON, &decoded))
	require.Equal(t, "partial", decoded.Status, "one-sided evidence stays partial, never a match")
	require.Len(t, decoded.Items, 1)
	require.Equal(t, "missing_local", decoded.Items[0].Status)
	require.Equal(t, "5", c4StringValue(t, decoded.Items[0].ProviderQuantity))
	probed, err := store.HasEconomicRevisionReconciliation(ctx, mustC4ReconIdentity(t, reconWork))
	require.NoError(t, err)
	require.True(t, probed)

	claimed, err := store.ClaimCompleteCalls(ctx, 10)
	require.NoError(t, err)
	for _, complete := range claimed {
		require.NotEqual(t, shadowCallID, complete.Closure.CallID, "shadow observation must never become claimable settlement work")
		require.NotEqual(t, v1CallID, complete.Closure.CallID, "settled V1 call must not be reclaimed")
	}
	_, err = store.GetCallExposure(ctx, shadowCallID)
	require.Error(t, err, "shadow call has no exposure, so V1 settlement is unreachable for it")

	// Shadow observes an existing V1 execution through source-separated V2
	// observations: no separate claimable billing call or ordinary leg row may
	// be fabricated for it.
	attachedObservation := c4Observation(t, v1CallID, "b-c4-attached", "c4-attached-obs", 33)
	require.NoError(t, handle.CaptureObservations(ctx, []metering.Observation{attachedObservation}))
	require.Len(t, c4JournalObservations(t, ctx, journal, "b-c4-attached"), 1, "attached shadow evidence must stay queryable under source-separated storage")
	reclaimed, err := store.ClaimCompleteCalls(ctx, 10)
	require.NoError(t, err)
	for _, complete := range reclaimed {
		require.NotEqual(t, v1CallID, complete.Closure.CallID, "attaching shadow evidence must not fabricate claimable work")
	}

	pending, err = store.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, pending, "shadow capture must enqueue no provider-cost work, attached or otherwise")

	// One legitimate V1 leg through the ordinary terminal path proves the V1
	// workers are live: only its expected source identity may change state.
	v1LegB := c4V1Leg(t, v1CallID, "b-c4-v1b")
	v1LegB.AttemptSeq = 2
	require.NoError(t, store.AppendCallLegUsage(ctx, v1LegB))
	sealedV1LegB, err := v1LegB.Seal()
	require.NoError(t, err)
	pending, err = store.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1, "only the legitimate V1 leg may be pending")
	require.Equal(t, sealedV1LegB.Key, pending[0].Leg.Key)
	providerWorker, err := billing.NewCallProviderCostWorker(store, store, c4ProviderCostResolver{}, 4)
	require.NoError(t, err)
	require.NoError(t, providerWorker.ProcessOnce(ctx))

	expectedProviderKey, err := billing.ProviderCostSourceKey(sealedV1LegB.Key)
	require.NoError(t, err)
	afterWorker := c4CaptureFinancial(t, ctx, store, selectedCallID, unitKey)
	require.Equal(t, baseline.Balance, afterWorker.Balance)
	require.Equal(t, baseline.AccountVersion, afterWorker.AccountVersion)
	require.Equal(t, baseline.CogsCount+1, afterWorker.CogsCount)
	require.Len(t, afterWorker.JournalIDs, len(baseline.JournalIDs)+1)
	newJournal := c4JournalByID(t, ctx, store, expectedProviderKey)
	require.Equal(t, "provider_call_cogs", newJournal.OperationKind)
	require.Equal(t, v1CallID.String(), newJournal.TurnID)
	require.Equal(t, "b-c4-v1b", newJournal.BLegID)
	require.Equal(t, baseline.OpenExposures, afterWorker.OpenExposures)
	require.Equal(t, baseline.SelectedHead, afterWorker.SelectedHead)
	require.Equal(t, baseline.UnitAfter, afterWorker.UnitAfter)
	workState, err := store.GetProviderCostWorkState(ctx, sealedV1LegB.Key)
	require.NoError(t, err)
	require.Equal(t, "processed", workState.Status)
	// Shadow observations never own provider-cost work rows: the only pending
	// item above was the legitimate V1 leg, now processed.
	for _, id := range afterWorker.JournalIDs {
		journalTx := c4JournalByID(t, ctx, store, id)
		require.NotContains(t, journalTx.ID, ratingIdentity.ValuationKey())
		require.NotEqual(t, shadowCallID.String(), journalTx.TurnID, "no journal may be rooted at a shadow call")
	}
	pending, err = store.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, pending)

	replayedSettlement, err := store.ApplyCallBillingResult(ctx, v1SettlementInput)
	require.NoError(t, err)
	require.True(t, replayedSettlement.Replayed, "V1 settlement writer stays authoritative and idempotent")
	replayedProvider, err := store.ApplyProviderCost(ctx, v1ProviderInput)
	require.NoError(t, err)
	require.True(t, replayedProvider.Replayed)
	_, err = store.ApplySelectedCostAdjustment(ctx, selectedInput)
	require.NoError(t, err)
	require.Equal(t, afterWorker.SelectedHead, mustC4SelectedHead(t, ctx, store, selectedCallID))

	require.NoError(t, store.Close())
	require.NoError(t, sqlDB.Close())
	require.NoError(t, journal.Close())
	require.NoError(t, journalDB.Close())
	reopened, reopenedDB := openC4FileStore(t, path)
	reopenedJournal, reopenedJournalDB := openC4FileJournal(t, journalPath)
	t.Cleanup(func() {
		_ = reopened.Close()
		_ = reopenedDB.Close()
		_ = reopenedJournal.Close()
		_ = reopenedJournalDB.Close()
	})
	reopenedHandle, err := runtimebundle.ComposeShadowV2Capture(runtimebundle.ShadowV2CaptureInput{
		StoreID: c4StoreID, MaxObservations: 16,
		EvidenceSink: reopenedJournal, WorkAppender: reopened, ResultStore: reopened, ReconciliationStore: reopened,
		Rater: &c4QuantityRater{rateNano: 1_000_000, currency: "USD"}, V1Settlement: reopened,
	})
	require.NoError(t, err)
	require.NoError(t, reopenedHandle.CaptureObservations(ctx, []metering.Observation{shadowObservation}))
	require.NoError(t, reopenedHandle.CaptureObservations(ctx, []metering.Observation{attachedObservation}))
	require.NoError(t, reopenedHandle.AppendWork(ctx, ratingWork))
	require.NoError(t, reopenedHandle.RateAndPersist(ctx, ratingWork))
	require.NoError(t, reopenedHandle.AppendWork(ctx, reconWork))
	reopenedRunner, err := billing.NewEconomicJobRunner(billing.EconomicJobRunnerConfig{
		Queue: reopened, Backlog: reopened, Results: reopened, Dependencies: reopened, Reconciliations: reopened,
		Rater:      &c4QuantityRater{rateNano: 1_000_000, currency: "USD"},
		Reconciler: billing.ComponentComparisonReconciler{},
		Owner:      "c4-recon-reopen", Batch: 8,
	})
	require.NoError(t, err)
	reopenedSummary, err := reopenedRunner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 0, reopenedSummary.Claimed, "replay after reopen must find no pending shadow work")

	rereadValuation, err := reopened.GetValuation(ctx, ratingIdentity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, persistedValuation.Totals, rereadValuation.Totals)
	reconIdentity = mustC4ReconIdentity(t, reconWork)
	rereadRecord, err := reopened.GetReconciliation(ctx, reconIdentity.ReconciliationKey(), reconIdentity.EvidenceRevision)
	require.NoError(t, err)
	require.JSONEq(t, string(record.ResultJSON), string(rereadRecord.ResultJSON))
	require.Len(t, c4JournalObservations(t, ctx, reopenedJournal, "b-c4-shadow"), 1)
	require.Len(t, c4JournalObservations(t, ctx, reopenedJournal, "b-c4-attached"), 1)

	afterReopen := c4CaptureFinancial(t, ctx, reopened, selectedCallID, unitKey)
	require.Equal(t, afterWorker.JournalIDs, afterReopen.JournalIDs)
	require.Equal(t, afterWorker.Balance, afterReopen.Balance)
	require.Equal(t, afterWorker.SelectedHead, afterReopen.SelectedHead)
	require.Equal(t, afterWorker.OpenExposures, afterReopen.OpenExposures)
	require.Equal(t, afterWorker.UnitAfter, afterReopen.UnitAfter)
}

func c4StringValue(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}

func mustC4ReconIdentity(t *testing.T, work billing.EconomicRevisionWork) billing.EconomicRevisionIdentity {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	return identity
}

//nolint:revive // test helper keeps t first per Go testing convention
func mustC4SelectedHead(t *testing.T, ctx context.Context, store *billingstore.DurableStore, callID billing.BillingCallID) billing.SelectedCostHead {
	t.Helper()
	head, err := store.GetSelectedCostHead(ctx, c4AccountID, callID, "c4-selected-head")
	require.NoError(t, err)
	return head
}
