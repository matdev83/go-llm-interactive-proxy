package billingstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func phase4EconomicsObservation(store, id string, revision uint64) metering.Observation {
	now := time.Unix(1_700_000_000+int64(revision), 0).UTC()
	component := metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: "vendor:tokens",
		Unit:      metering.UnitToken,
		SchemaID:  "vendor:meter:v1",
	}
	value := metering.Decimal{Coefficient: "5", Scale: 0}
	return metering.Observation{
		Version: 2, ID: id, SourceEventKey: "source-" + id, Revision: revision,
		StreamID: "stream-" + store, Sequence: revision,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority:   metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: store, TenantID: "tenant-" + store,
			AccountID: "account-" + store, ALegID: "a-" + store, BillingCallID: "call-" + store,
			BLegID: "b-" + store, AttemptID: "attempt-" + store, AttemptSeq: revision,
			ProviderAccountKey: "provider-account-" + store,
		},
		Correlation: metering.CorrelationV2{
			StoreID: store, TenantID: "tenant-" + store, CallID: "call-" + store,
			BillingCallID: "call-" + store, ALegID: "a-" + store, BLegID: "b-" + store,
			AttemptID: "attempt-" + store, AttemptSeq: revision,
			ProviderAccountKey: "provider-account-" + store,
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now,
		MappingRef: "provider:tokens:v1",
		Measures:   []metering.Measure{{Key: component, Value: &value, Quality: metering.QualityObserved}},
	}
}

func phase4EconomicsValuation(t *testing.T, observation metering.Observation, id string, createdAt time.Time, _ string) economics.Valuation {
	t.Helper()
	ref, err := observation.Ref(observation.Subject.StoreID)
	require.NoError(t, err)
	component := observation.Measures[0].Key
	quantity := metering.Decimal{Coefficient: "5", Scale: 0}
	unitPrice := metering.Decimal{Coefficient: "25", Scale: 3}
	amount := metering.Decimal{Coefficient: "125", Scale: 3}
	rounded := economics.Money{NanoUnits: 125_000_000, Currency: "USD", Present: true}
	line := economics.LineItem{
		ID: "line-" + id, RuleID: "rule-v1", ItemID: "item-" + id,
		Component: &component, Quantity: &quantity, Unit: component.Unit,
		UnitPrice: &unitPrice, Amount: &amount, RoundedAmount: &rounded,
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingTowardZero,
		SourceObservationRefs: []metering.ObservationRef{ref},
	}
	totalAmount := metering.Decimal{Coefficient: "125", Scale: 3}
	inputSetHash, err := economics.CanonicalInputSetHash(economics.BasisProviderReported, []metering.ObservationRef{ref})
	require.NoError(t, err)
	return economics.Valuation{
		ID: id, Version: economics.ValuationVersionV2,
		Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", InputObservations: []metering.ObservationRef{ref},
		InputSetHash: inputSetHash, Lines: []economics.LineItem{line},
		Totals:       []economics.CurrencyTotal{{Currency: "USD", Amount: &totalAmount, RoundedAmount: rounded}},
		Completeness: economics.CompletenessComplete, CreatedAt: createdAt,
	}
}

func phase4Reconciliation(t *testing.T, observation metering.Observation, id string, version uint64, createdAt time.Time) ReconciliationRecord {
	t.Helper()
	hash := sha256.Sum256([]byte(id))
	return ReconciliationRecord{
		ID: id, Version: version, Subject: observation.Subject, Scope: "call",
		Basis: economics.BasisProviderReported, InputSetHash: hex.EncodeToString(hash[:]),
		LocalInputHash: strings.Repeat("b", 64), ProviderInputHash: strings.Repeat("c", 64),
		PolicyID: "policy", PolicyVersion: "v1",
		ResultJSON: []byte(`{"z":2,"a":1}`), CreatedAt: createdAt,
	}
}

func TestPhase4ValuationAndReconciliationRoundTripAndReplay(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "obs-economics", 1)
	inputHash := strings.Repeat("a", 64)
	valuation := phase4EconomicsValuation(t, observation, "valuation-1", time.Unix(1_700_000_100, 0).UTC(), inputHash)
	wantValuationJSON, err := valuation.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, store.AppendValuation(ctx, valuation))
	require.NoError(t, store.AppendValuation(ctx, valuation))
	gotValuation, err := store.GetValuation(ctx, valuation.ID, valuation.Version)
	require.NoError(t, err)
	gotValuationJSON, err := gotValuation.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(wantValuationJSON), string(gotValuationJSON))

	var amountCoefficient, roundedCurrency string
	var amountPresent, roundedPresent int
	require.NoError(t, store.db.NewRaw(`SELECT amount_coefficient, amount_present, rounded_currency, rounded_present FROM billing_valuation_lines WHERE store_id = ? AND valuation_id = ?`, "test", valuation.ID).Scan(ctx, &amountCoefficient, &amountPresent, &roundedCurrency, &roundedPresent))
	require.Equal(t, "125", amountCoefficient)
	require.Equal(t, 1, amountPresent)
	require.Equal(t, "USD", roundedCurrency)
	require.Equal(t, 1, roundedPresent)

	changed := valuation.Clone()
	changed.Lines[0].Amount = &metering.Decimal{Coefficient: "126", Scale: 3}
	require.ErrorIs(t, store.AppendValuation(ctx, changed), ErrIdentityConflict)

	reconciliation := phase4Reconciliation(t, observation, "reconciliation-1", 1, time.Unix(1_700_000_101, 0).UTC())
	wantReconciliationJSON, err := reconciliation.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, store.AppendReconciliation(ctx, reconciliation))
	require.NoError(t, store.AppendReconciliation(ctx, reconciliation))
	gotReconciliation, err := store.GetReconciliation(ctx, reconciliation.ID, reconciliation.Version)
	require.NoError(t, err)
	gotReconciliationJSON, err := gotReconciliation.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(wantReconciliationJSON), string(gotReconciliationJSON))
	require.Equal(t, "{\"a\":1,\"z\":2}", string(gotReconciliation.ResultJSON))

	revision := reconciliation
	revision.Version = 2
	revision.ResultJSON = []byte(`{"a":3}`)
	require.NoError(t, store.AppendReconciliation(ctx, revision))
	require.ErrorIs(t, store.AppendReconciliation(ctx, ReconciliationRecord{ID: reconciliation.ID, Version: 1, Subject: reconciliation.Subject, Basis: reconciliation.Basis, ResultJSON: []byte(`{"a":3}`), CreatedAt: reconciliation.CreatedAt}), ErrIdentityConflict)
}

func TestPhase4ValuationBasesRoundTrip(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "obs-all-bases", 1)
	bases := []economics.ValuationBasis{
		economics.BasisLocalExpected,
		economics.BasisProviderQuantityLocal,
		economics.BasisProviderReported,
		economics.BasisStatementReported,
		economics.BasisCustomerPolicy,
	}
	for i, basis := range bases {
		valuation := phase4EconomicsValuation(t, observation, "valuation-"+string(basis), time.Unix(1_700_003_000+int64(i), 0).UTC(), fmt.Sprintf("%064x", i+1))
		valuation.Basis = basis
		inputSetHash, err := economics.CanonicalInputSetHash(valuation.Basis, valuation.InputObservations)
		require.NoError(t, err)
		valuation.InputSetHash = inputSetHash
		if basis == economics.BasisLocalExpected || basis == economics.BasisProviderQuantityLocal || basis == economics.BasisCustomerPolicy {
			valuation.Rater = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater", Version: "v1"}, RaterID: "rater"}
			valuation.RaterContent = &economics.SnapshotContentRef{ContentRef: "store://rater/v1", ContentHash: strings.Repeat("1", 64)}
			valuation.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "store://qualifier/v1", ContentHash: strings.Repeat("2", 64)}
		}
		if basis == economics.BasisLocalExpected || basis == economics.BasisProviderQuantityLocal {
			valuation.Tariff = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff", Version: "v1"}, RaterID: "tariff"}
			valuation.TariffContent = &economics.SnapshotContentRef{ContentRef: "store://tariff/v1", ContentHash: strings.Repeat("3", 64)}
		}
		if basis == economics.BasisCustomerPolicy {
			valuation.Policy = economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v1"}, PolicyID: "policy"}
			valuation.PolicyContent = &economics.SnapshotContentRef{ContentRef: "store://policy/v1", ContentHash: strings.Repeat("4", 64)}
		}
		wantJSON, err := valuation.CanonicalJSON()
		require.NoError(t, err)
		require.NoError(t, store.AppendValuation(ctx, valuation))
		got, err := store.GetValuation(ctx, valuation.ID, valuation.Version)
		require.NoError(t, err)
		gotJSON, err := got.CanonicalJSON()
		require.NoError(t, err)
		require.Equal(t, string(wantJSON), string(gotJSON), basis)
	}
}

func TestPhase4CompositionRollsBackCanonicalAndEconomicRows(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.NoError(t, journalstore.Migrate(ctx, store.db))
	journal, err := journalstore.OpenStore(ctx, store.db, journalstore.DurableConfig{StoreID: "test"})
	require.NoError(t, err)
	observation := phase4EconomicsObservation("test", "obs-composed", 1)
	valuation := phase4EconomicsValuation(t, observation, "valuation-composed", time.Unix(1_700_000_200, 0).UTC(), strings.Repeat("d", 64))
	reconciliation := phase4Reconciliation(t, observation, "reconciliation-composed", 1, time.Unix(1_700_000_201, 0).UTC())
	marker := EconomicWorkMarker{ID: "work-composed", Version: 1, Kind: "valuation", SubjectKind: metering.SubjectBLeg, SubjectID: observation.Subject.BLegID, PayloadJSON: []byte(`{"revision":1}`), CreatedAt: time.Unix(1_700_000_202, 0).UTC()}
	sentinel := errors.New("failpoint")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_observation" {
			return sentinel
		}
		return nil
	})
	require.ErrorIs(t, store.AppendObservationEconomics(ctx, journal, observation, &valuation, &reconciliation, &marker), sentinel)
	for _, table := range []string{"metering_facts", "metering_components", "billing_valuations", "billing_valuation_lines", "billing_reconciliations", "billing_economic_work"} {
		var count int
		require.NoError(t, store.db.NewRaw(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(ctx, &count))
		require.Zero(t, count, table)
	}
	store.SetEconomicFaultHook(nil)
	require.NoError(t, store.AppendObservationEconomics(ctx, journal, observation, &valuation, &reconciliation, &marker))
	for _, table := range []string{"metering_facts", "metering_components", "billing_valuations", "billing_valuation_lines", "billing_reconciliations", "billing_economic_work"} {
		var count int
		require.NoError(t, store.db.NewRaw(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(ctx, &count))
		require.NotZero(t, count, table)
	}
}

func TestPhase4ValuationRebuildAndBoundedPagination(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "obs-rebuild", 1)
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("valuation-page-%d", i)
		inputObservation := phase4EconomicsObservation("test", fmt.Sprintf("obs-rebuild-%d", i), 1)
		valuation := phase4EconomicsValuation(t, inputObservation, id, time.Unix(1_700_001_000+int64(i), 0).UTC(), fmt.Sprintf("%064x", i))
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	query := ValuationQuery{StoreID: "test", SubjectKind: metering.SubjectBLeg, SubjectID: observation.Subject.BLegID, Limit: 1}
	page, err := store.ListValuations(ctx, query)
	require.NoError(t, err)
	require.Len(t, page.Valuations, 1)
	require.NotEmpty(t, page.NextCursor)
	query.Cursor = page.NextCursor
	page, err = store.ListValuations(ctx, query)
	require.NoError(t, err)
	require.Len(t, page.Valuations, 1)
	query.SubjectID = "other"
	_, err = store.ListValuations(ctx, query)
	require.ErrorIs(t, err, ErrInvalidEconomicsCursor)
	query.Cursor = "invalid"
	query.SubjectID = observation.Subject.BLegID
	_, err = store.ListValuations(ctx, query)
	require.ErrorIs(t, err, ErrInvalidEconomicsCursor)
	query.Cursor = ""
	query.Limit = 501
	_, err = store.ListValuations(ctx, query)
	require.ErrorIs(t, err, ErrEconomicsPageSizeExceeded)

	var beforeJSON, beforeFingerprint string
	require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint FROM billing_valuations WHERE valuation_id = ?`, "valuation-page-1").Scan(ctx, &beforeJSON, &beforeFingerprint))
	_, err = store.db.NewRaw(`DELETE FROM billing_valuation_lines WHERE valuation_id = ?`, "valuation-page-1").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`UPDATE billing_valuations SET projection_version = 99 WHERE valuation_id = ?`, "valuation-page-1").Exec(ctx)
	require.NoError(t, err)
	require.NoError(t, store.RebuildValuationProjections(ctx))
	var lineCount, projectionVersion int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_valuation_lines WHERE valuation_id = ?`, "valuation-page-1").Scan(ctx, &lineCount))
	require.Equal(t, 1, lineCount)
	require.NoError(t, store.db.NewRaw(`SELECT projection_version FROM billing_valuations WHERE valuation_id = ?`, "valuation-page-1").Scan(ctx, &projectionVersion))
	require.Equal(t, BillingEconomicsProjectionVersion, projectionVersion)
	var afterJSON, afterFingerprint string
	require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint FROM billing_valuations WHERE valuation_id = ?`, "valuation-page-1").Scan(ctx, &afterJSON, &afterFingerprint))
	require.Equal(t, beforeJSON, afterJSON)
	require.Equal(t, beforeFingerprint, afterFingerprint)
}

func TestPhase4RebuildRepairsStaleReconciliationProjection(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "obs-reconciliation-rebuild", 1)
	reconciliation := phase4Reconciliation(t, observation, "reconciliation-rebuild", 1, time.Unix(1_700_002_000, 0).UTC())
	require.NoError(t, store.AppendReconciliation(ctx, reconciliation))

	var beforeJSON, beforeFingerprint string
	require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint FROM billing_reconciliations WHERE reconciliation_id = ?`, reconciliation.ID).Scan(ctx, &beforeJSON, &beforeFingerprint))
	_, err := store.db.NewRaw(`UPDATE billing_reconciliations SET subject_id = ?, scope = ?, result_json = ?, projection_version = ? WHERE reconciliation_id = ?`, "stale-subject", "stale-scope", `{"stale":true}`, 99, reconciliation.ID).Exec(ctx)
	require.NoError(t, err)

	require.NoError(t, store.RebuildValuationProjections(ctx))
	var subjectID, scope, resultJSON string
	var projectionVersion int
	require.NoError(t, store.db.NewRaw(`SELECT subject_id, scope, result_json, projection_version FROM billing_reconciliations WHERE reconciliation_id = ?`, reconciliation.ID).Scan(ctx, &subjectID, &scope, &resultJSON, &projectionVersion))
	require.Equal(t, reconciliation.Subject.BLegID, subjectID)
	require.Equal(t, reconciliation.Scope, scope)
	require.Equal(t, `{"a":1,"z":2}`, resultJSON)
	require.Equal(t, BillingEconomicsProjectionVersion, projectionVersion)

	var afterJSON, afterFingerprint string
	require.NoError(t, store.db.NewRaw(`SELECT canonical_json, fingerprint FROM billing_reconciliations WHERE reconciliation_id = ?`, reconciliation.ID).Scan(ctx, &afterJSON, &afterFingerprint))
	require.Equal(t, beforeJSON, afterJSON)
	require.Equal(t, beforeFingerprint, afterFingerprint)
}
