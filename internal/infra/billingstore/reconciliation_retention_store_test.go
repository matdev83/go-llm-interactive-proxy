package billingstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func retentionTestSubject(storeID, bLegID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: "tenant-" + storeID,
		ALegID: "a-" + bLegID, BillingCallID: "call-" + bLegID, BLegID: bLegID,
	}
}

func retentionTestObservation(t *testing.T, id, storeID, bLegID, origin, value string) metering.Observation {
	t.Helper()
	subject := retentionTestSubject(storeID, bLegID)
	acquisition := metering.AcquisitionLocalTokenizer
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	now := time.Unix(1_700_002_100, 0).UTC()
	decimal, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, TenantID: subject.TenantID,
			ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now,
		MappingRef: "retention.test.v1",
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
				Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
			},
			Value: &decimal, Quality: metering.QualityObserved, MethodRef: "retention-tokenizer-v1",
		}},
	}
	require.NoError(t, observation.Validate())
	return observation
}

func retentionTestValuation(t *testing.T, storeID, bLegID, id string, basis economics.ValuationBasis, amount string) economics.Valuation {
	t.Helper()
	subject := retentionTestSubject(storeID, bLegID)
	decimal, err := metering.ParseDecimal(amount)
	require.NoError(t, err)
	nanos, err := decimal.ToNanoUnits()
	require.NoError(t, err)
	valuation := economics.Valuation{
		ID: id, Version: economics.ValuationVersionV2,
		Perspective: metering.PerspectiveOperator, Basis: basis, Subject: subject,
		InputObservations: []metering.ObservationRef{{
			StoreID: storeID, ObservationID: id + "-observation", Revision: 1, PayloadHash: strings.Repeat("c", 64),
		}},
		InputSetHash: strings.Repeat("d", 64),
		Completeness: economics.CompletenessComplete,
		CreatedAt:    time.Unix(1_700_002_200, 0).UTC(),
		Totals: []economics.CurrencyTotal{{
			Currency: "USD", Amount: &decimal,
			RoundedAmount: economics.Money{NanoUnits: nanos, Currency: "USD", Present: true},
		}},
	}
	if basis != economics.BasisProviderReported {
		valuation.Tariff = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-retention", Version: "v1"}}
		valuation.TariffContent = &economics.SnapshotContentRef{ContentRef: "catalog://retention/tariff/v1", ContentHash: strings.Repeat("f", 64)}
		valuation.Rater = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-retention", Version: "v1"}, RaterID: "reference-rater"}
		valuation.RaterContent = &economics.SnapshotContentRef{ContentRef: "catalog://retention/rater/v1", ContentHash: strings.Repeat("e", 64)}
		valuation.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "catalog://retention/qualifiers/v1", ContentHash: strings.Repeat("a", 64)}
	}
	require.NoError(t, valuation.Validate())
	return valuation
}

// retentionTestResult composes the full 12.1/12.2/12.3 result using the real
// pure producers.
func retentionTestResult(t *testing.T, storeID, bLegID, id string, revision uint64, createdAt time.Time) billing.ReconciliationRetentionResult {
	t.Helper()
	subject := retentionTestSubject(storeID, bLegID)
	local := retentionTestObservation(t, id+"-local", storeID, bLegID, metering.OriginLocal, "100")
	provider := retentionTestObservation(t, id+"-provider", storeID, bLegID, metering.OriginProvider, "110")
	quantity, err := billing.CompareComponentQuantities(
		billing.ReconciliationEvidenceSet{Subject: subject, Tokenizer: "retention-tokenizer-v1", Observations: []metering.Observation{local}},
		billing.ReconciliationEvidenceSet{Subject: subject, Tokenizer: "retention-tokenizer-v1", Observations: []metering.Observation{provider}},
	)
	require.NoError(t, err)

	monetary, err := billing.DecomposeMonetaryDiscrepancies(billing.MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
		retentionTestValuation(t, storeID, bLegID, id+"-e", economics.BasisLocalExpected, "1.00"),
		retentionTestValuation(t, storeID, bLegID, id+"-q", economics.BasisProviderQuantityLocal, "1.10"),
		retentionTestValuation(t, storeID, bLegID, id+"-p", economics.BasisProviderReported, "1.32"),
	}})
	require.NoError(t, err)

	limit := metering.Decimal{Coefficient: "4", Scale: 1}
	policy := billing.ReconciliationTolerancePolicy{
		Version: billing.ReconciliationTolerancePolicyV1,
		Ref:     billing.VersionRef{ID: "tolerance-policy", Version: "v1"},
		Rules: []billing.ReconciliationToleranceRule{{
			ID: "usd", Scope: billing.ReconciliationToleranceScope{Currency: "USD"}, AbsoluteLimit: &limit,
		}},
	}
	findings, err := billing.ReconciliationFindingsFromMonetaryComparison("call:"+bLegID, monetary)
	require.NoError(t, err)
	aggregate, err := billing.AggregateReconciliationFindings(policy, findings)
	require.NoError(t, err)

	localRef, err := local.Ref(storeID)
	require.NoError(t, err)
	providerRef, err := provider.Ref(storeID)
	require.NoError(t, err)
	return billing.ReconciliationRetentionResult{
		SchemaVersion: billing.ReconciliationRetentionSchemaVersionV1,
		ID:            id, ResultRevision: revision,
		Subject: subject, Scope: "call:" + bLegID,
		Policy:            billing.VersionRef{ID: policy.Ref.ID, Version: policy.Ref.Version},
		InputSetHash:      strings.Repeat("a", 64),
		LocalInputHash:    strings.Repeat("b", 64),
		ProviderInputHash: strings.Repeat("c", 64),
		ValuationIDs:      []string{id + "-e", id + "-q", id + "-p"},
		ObservationRefs:   []metering.ObservationRef{localRef, providerRef},
		Quantity:          &quantity,
		Monetary:          &monetary,
		Aggregate:         &aggregate,
		Diagnostics:       []billing.ReconciliationRetentionDiagnostic{{Code: "suspected_pricing_difference", Detail: "residual without provider rate detail"}},
		CreatedAt:         createdAt,
	}
}

// TestReconciliationRetentionRejectsForeignEvidenceAndCrossStoreLeak proves
// foreign-store nested evidence never reaches the durable table and that reads
// stay scoped to the owning store.
func TestReconciliationRetentionRejectsForeignEvidenceAndCrossStoreLeak(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "retention-scope.db")))
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	defer func() { _ = bunDB.Close() }()
	storeA, err := NewDurableStore(ctx, bunDB, Config{StoreID: "store-a"})
	require.NoError(t, err)
	storeB, err := NewDurableStore(ctx, bunDB, Config{StoreID: "store-b"})
	require.NoError(t, err)

	valid := retentionTestResult(t, "store-a", "b-a", "retention-scope-a", 1, time.Unix(1_700_002_900, 0).UTC())
	require.NoError(t, storeA.AppendReconciliationRetention(ctx, valid))

	_, err = storeB.GetReconciliationRetention(ctx, valid.ID, valid.ResultRevision)
	require.ErrorIs(t, err, sql.ErrNoRows, "another store must not read the result")
	_, found, err := storeB.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-a"})
	require.NoError(t, err)
	require.False(t, found, "another store must not discover the result")

	foreign := retentionTestResult(t, "store-a", "b-a", "retention-foreign-nested", 1, time.Unix(1_700_002_901, 0).UTC())
	foreign.Quantity.Items[0].Local[0].Observation.StoreID = "store-b"
	require.ErrorIs(t, storeA.AppendReconciliationRetention(ctx, foreign), billing.ErrInvalidReconciliationRetention)

	var count int
	require.NoError(t, storeA.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ?`, "store-a").Scan(ctx, &count))
	require.Equal(t, 1, count, "foreign evidence must not be persisted")
}

// TestReconciliationRetentionGenericAppendRejectsPoisonSchema2 proves the
// generic reconciliation append cannot persist a schema-2 payload that the
// specialized retention reader would reject.
func TestReconciliationRetentionGenericAppendRejectsPoisonSchema2(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	poison := ReconciliationRecord{
		ID: "retention-poison", Version: 1,
		Subject:             retentionTestSubject("test", "b-poison"),
		ResultSchemaVersion: ReconciliationRecordSchemaRetention,
		ResultJSON:          json.RawMessage(`{"not":"retention"}`),
		CreatedAt:           time.Unix(1_700_005_000, 0).UTC(),
	}
	err := store.AppendReconciliation(ctx, poison)
	require.ErrorIs(t, err, ErrInvalidReconciliation)
	require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)

	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = 'test' AND reconciliation_id = ?`, poison.ID).Scan(ctx, &count))
	require.Zero(t, count, "poison schema-2 payload must not be persisted")

	valid, err := ReconciliationRecordFromRetention(retentionTestResult(t, "test", "b-poison", "retention-poison-valid", 1, poison.CreatedAt.Add(time.Second)))
	require.NoError(t, err)
	require.NoError(t, store.AppendReconciliation(ctx, valid), "specialized retention path must still persist")
}

// cloneRetentionJSONObject deep-copies one decoded canonical JSON object while
// preserving json.Number lexemes, so a forged payload stays byte-canonical.
func cloneRetentionJSONObject(t *testing.T, in map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	require.NoError(t, decoder.Decode(&out))
	return out
}

// forgeRetentionCanonical decodes one valid canonical retention payload,
// applies a JSON-level mutation and re-encodes it, so forged bytes stay
// canonical and only semantic revalidation can reject them.
func forgeRetentionCanonical(t *testing.T, result billing.ReconciliationRetentionResult, mutate func(document map[string]any)) []byte {
	t.Helper()
	canonical, err := result.CanonicalJSON()
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var document map[string]any
	require.NoError(t, decoder.Decode(&document))
	mutate(document)
	forged, err := json.Marshal(document)
	require.NoError(t, err)
	return forged
}

// forgeRetentionQuantityCanonical applies a byte-canonical JSON mutation to the
// one comparable quantity item of a valid retention payload.
func forgeRetentionQuantityCanonical(t *testing.T, result billing.ReconciliationRetentionResult, mutate func(map[string]any)) []byte {
	t.Helper()
	return forgeRetentionCanonical(t, result, func(document map[string]any) {
		quantity, ok := document["quantity"].(map[string]any)
		require.True(t, ok, "canonical payload must carry the quantity block")
		items, ok := quantity["items"].([]any)
		require.True(t, ok)
		require.Len(t, items, 1)
		item, ok := items[0].(map[string]any)
		require.True(t, ok, "quantity item must be a JSON object")
		mutate(item)
	})
}

// TestReconciliationRetentionRejectsForgedQuantityDeltasDurably proves both the
// specialized and the raw generic schema-2 append paths refuse canonical
// payloads whose comparable quantity deltas or source shape were forged, and
// that no forged row reaches the table.
func TestReconciliationRetentionRejectsForgedQuantityDeltasDurably(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	createdAt := time.Unix(1_700_005_100, 0).UTC()

	base := func(t *testing.T, id string) billing.ReconciliationRetentionResult {
		t.Helper()
		return retentionTestResult(t, "test", "b-forged", id, 1, createdAt)
	}

	secondEvidence := func(evidence billing.ReconciliationQuantityEvidence, id string) billing.ReconciliationQuantityEvidence {
		duplicate := evidence
		duplicate.Key = evidence.Key.Clone()
		duplicate.Observation.ObservationID = id
		duplicate.Observation.PayloadHash = strings.Repeat("f", 64)
		return duplicate
	}

	cases := []struct {
		name   string
		mutate func(*billing.ReconciliationRetentionResult)
		forge  func(map[string]any)
	}{
		{
			name: "wrong signed delta",
			mutate: func(result *billing.ReconciliationRetentionResult) {
				result.Quantity.Items[0].SignedDelta = &metering.Decimal{Coefficient: "11"}
			},
			forge: func(item map[string]any) {
				signedDelta, ok := item["signed_delta"].(map[string]any)
				require.True(t, ok, "signed_delta must be a JSON object")
				signedDelta["coefficient"] = "11"
			},
		},
		{
			name: "second provider source",
			mutate: func(result *billing.ReconciliationRetentionResult) {
				item := &result.Quantity.Items[0]
				item.Provider = append(item.Provider, secondEvidence(item.Provider[0], "retention-forged-second-provider"))
			},
			forge: func(item map[string]any) {
				provider, ok := item["provider"].([]any)
				require.True(t, ok, "provider must be a JSON array")
				firstProvider, ok := provider[0].(map[string]any)
				require.True(t, ok, "provider entry must be a JSON object")
				duplicate := cloneRetentionJSONObject(t, firstProvider)
				observation, ok := duplicate["observation"].(map[string]any)
				require.True(t, ok, "observation must be a JSON object")
				observation["observation_id"] = "retention-forged-second-provider"
				observation["payload_hash"] = strings.Repeat("f", 64)
				item["provider"] = append(provider, duplicate)
			},
		},
	}

	for _, tc := range cases {
		slug := strings.ReplaceAll(tc.name, " ", "-")
		t.Run(tc.name+" specialized append", func(t *testing.T) {
			result := base(t, "retention-forged-specialized-"+slug)
			tc.mutate(&result)
			err := store.AppendReconciliationRetention(ctx, result)
			require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		})
		t.Run(tc.name+" generic schema-2 append", func(t *testing.T) {
			result := base(t, "retention-forged-generic-"+slug)
			raw := forgeRetentionQuantityCanonical(t, result, tc.forge)
			poison := ReconciliationRecord{
				ID: result.ID, Version: result.ResultRevision,
				Subject:             result.Subject,
				ResultSchemaVersion: ReconciliationRecordSchemaRetention,
				ResultJSON:          raw,
				CreatedAt:           result.CreatedAt,
			}
			err := store.AppendReconciliation(ctx, poison)
			require.ErrorIs(t, err, ErrInvalidReconciliation)
			require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		})
	}

	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ?`, "test").Scan(ctx, &count))
	require.Zero(t, count, "no forged row may be persisted")

	valid := retentionTestResult(t, "test", "b-forged", "retention-forged-valid", 1, createdAt.Add(time.Minute))
	require.NoError(t, store.AppendReconciliationRetention(ctx, valid), "a valid retention result must still persist")
}

// TestReconciliationRetentionRejectsForgedMonetaryTermsDurably proves both the
// specialized and the raw generic schema-2 append paths refuse canonical
// payloads whose monetary terms were forged or detached from the retained
// E/Q/P valuations, and that no forged row reaches the table.
func TestReconciliationRetentionRejectsForgedMonetaryTermsDurably(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	createdAt := time.Unix(1_700_005_200, 0).UTC()

	monetaryRowJSON := func(t *testing.T, document map[string]any) map[string]any {
		t.Helper()
		monetary, ok := document["monetary"].(map[string]any)
		require.True(t, ok, "canonical payload must carry the monetary block")
		rows, ok := monetary["rows"].([]any)
		require.True(t, ok)
		require.Len(t, rows, 1)
		row, ok := rows[0].(map[string]any)
		require.True(t, ok)
		return row
	}

	cases := []struct {
		name   string
		mutate func(t *testing.T, result *billing.ReconciliationRetentionResult)
		forge  func(t *testing.T, document map[string]any)
	}{
		{
			name: "wrong complete metering amount",
			mutate: func(t *testing.T, result *billing.ReconciliationRetentionResult) {
				result.Monetary.Rows[0].MeteringCostEffect.Amount = &billing.MonetaryExactAmount{
					Currency: "USD", Decimal: &metering.Decimal{Coefficient: "2", Scale: 1},
				}
			},
			forge: func(t *testing.T, document map[string]any) {
				row := monetaryRowJSON(t, document)
				term, ok := row["metering_cost_effect"].(map[string]any)
				require.True(t, ok)
				amount, ok := term["amount"].(map[string]any)
				require.True(t, ok)
				decimal, ok := amount["decimal"].(map[string]any)
				require.True(t, ok)
				decimal["coefficient"] = "2"
			},
		},
		{
			name: "missing term without a reason",
			mutate: func(t *testing.T, result *billing.ReconciliationRetentionResult) {
				result.Monetary.Rows[0].MeteringCostEffect = billing.MonetaryDiscrepancyTerm{Status: billing.MonetaryTermMissing}
			},
			forge: func(t *testing.T, document map[string]any) {
				monetaryRowJSON(t, document)["metering_cost_effect"] = map[string]any{"status": "missing"}
			},
		},
		{
			name: "incomparable term with an unknown reason",
			mutate: func(t *testing.T, result *billing.ReconciliationRetentionResult) {
				result.Monetary.Rows[0].EndToEndCostDelta = billing.MonetaryDiscrepancyTerm{
					Status: billing.MonetaryTermIncomparable, Reason: "bogus",
				}
			},
			forge: func(t *testing.T, document map[string]any) {
				monetaryRowJSON(t, document)["end_to_end_cost_delta"] = map[string]any{"status": "incomparable", "reason": "bogus"}
			},
		},
	}

	for _, tc := range cases {
		slug := strings.ReplaceAll(tc.name, " ", "-")
		t.Run(tc.name+" specialized append", func(t *testing.T) {
			result := retentionTestResult(t, "test", "b-monetary-forged", "retention-monetary-specialized-"+slug, 1, createdAt)
			tc.mutate(t, &result)
			err := store.AppendReconciliationRetention(ctx, result)
			require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		})
		t.Run(tc.name+" generic schema-2 append", func(t *testing.T) {
			result := retentionTestResult(t, "test", "b-monetary-forged", "retention-monetary-generic-"+slug, 1, createdAt)
			forged := forgeRetentionCanonical(t, result, func(document map[string]any) { tc.forge(t, document) })
			poison := ReconciliationRecord{
				ID: result.ID, Version: result.ResultRevision,
				Subject:             result.Subject,
				ResultSchemaVersion: ReconciliationRecordSchemaRetention,
				ResultJSON:          forged,
				CreatedAt:           result.CreatedAt,
			}
			err := store.AppendReconciliation(ctx, poison)
			require.ErrorIs(t, err, ErrInvalidReconciliation)
			require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		})
	}

	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ?`, "test").Scan(ctx, &count))
	require.Zero(t, count, "no forged monetary row may be persisted")

	valid := retentionTestResult(t, "test", "b-monetary-forged", "retention-monetary-valid", 1, createdAt.Add(time.Minute))
	require.NoError(t, store.AppendReconciliationRetention(ctx, valid), "a valid retention result must still persist")
}

func TestReconciliationRetentionAppendReplayConflictAndRevisions(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	result := retentionTestResult(t, "test", "b-retention", "reconciliation-retention-1", 1, time.Unix(1_700_002_300, 0).UTC())
	canonical, err := result.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	require.NoError(t, store.AppendReconciliationRetention(ctx, result), "exact replay must be idempotent")

	got, err := store.GetReconciliationRetention(ctx, result.ID, result.ResultRevision)
	require.NoError(t, err)
	gotCanonical, err := got.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(canonical), string(gotCanonical), "stored bytes must round-trip exactly")

	payload := string(gotCanonical)
	for _, fragment := range []string{
		`"result_revision":1`,
		`"gross_absolute_discrepancy"`,
		`"coefficient":"132","scale":2`,
		`"signed_delta"`,
		`"observation_refs"`,
		`"valuation_ids"`,
	} {
		require.Contains(t, payload, fragment)
	}

	conflict := result
	conflict.CreatedAt = result.CreatedAt.Add(time.Second)
	require.ErrorIs(t, store.AppendReconciliationRetention(ctx, conflict), ErrIdentityConflict)

	second := retentionTestResult(t, "test", "b-retention", result.ID, 2, result.CreatedAt.Add(time.Minute))
	secondCanonical, err := second.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, store.AppendReconciliationRetention(ctx, second))
	first, err := store.GetReconciliationRetention(ctx, result.ID, 1)
	require.NoError(t, err)
	firstCanonical, err := first.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(canonical), string(firstCanonical), "revision append must not rewrite revision 1")
	latest, err := store.GetReconciliationRetention(ctx, result.ID, 2)
	require.NoError(t, err)
	latestCanonical, err := latest.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(secondCanonical), string(latestCanonical))

	_, err = store.GetReconciliationRetention(ctx, result.ID, 3)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestReconciliationRetentionScopeIsolationAndLatest(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	base := time.Unix(1_700_002_400, 0).UTC()
	first := retentionTestResult(t, "test", "b-one", "retention-scope-1", 1, base)
	revision := retentionTestResult(t, "test", "b-one", "retention-scope-1", 2, base.Add(time.Minute))
	other := retentionTestResult(t, "test", "b-two", "retention-scope-2", 1, base.Add(2*time.Minute))
	for _, result := range []billing.ReconciliationRetentionResult{first, revision, other} {
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	}

	latest, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-one"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "retention-scope-1", latest.ID)
	require.Equal(t, uint64(2), latest.ResultRevision, "latest must be the newest revision")

	latestOther, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-two"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "retention-scope-2", latestOther.ID)

	_, found, err = store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-missing"})
	require.NoError(t, err)
	require.False(t, found)

	_, _, err = store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{TenantID: "tenant-test"})
	require.ErrorIs(t, err, ErrQueryTooBroad)

	foreign := retentionTestResult(t, "other", "b-one", "retention-foreign", 1, base)
	require.ErrorIs(t, store.AppendReconciliationRetention(ctx, foreign), ErrEconomicsOutOfScope)
}

func TestReconciliationRetentionRestartAndLegacyIsolation(t *testing.T) {
	ctx := context.Background()
	storeID := "restart-retention"
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "retention.db")))
	open := func() *DurableStore {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(4)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		store, err := NewDurableStore(ctx, bunDB, Config{StoreID: storeID})
		require.NoError(t, err)
		return store
	}

	first := open()
	retention := retentionTestResult(t, storeID, "b-restart", "retention-restart", 1, time.Unix(1_700_002_500, 0).UTC())
	retentionCanonical, err := retention.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, first.AppendReconciliationRetention(ctx, retention))

	observation := phase4EconomicsObservation(storeID, "obs-restart-retention", 1)
	legacy := phase4Reconciliation(t, observation, "legacy-restart", 1, time.Unix(1_700_002_501, 0).UTC())
	require.NoError(t, first.AppendReconciliation(ctx, legacy))
	require.NoError(t, first.Close())

	second := open()
	defer func() { _ = second.Close() }()
	reopened, err := second.GetReconciliationRetention(ctx, retention.ID, retention.ResultRevision)
	require.NoError(t, err)
	reopenedCanonical, err := reopened.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(retentionCanonical), string(reopenedCanonical))

	legacyRecord, err := second.GetReconciliation(ctx, legacy.ID, legacy.Version)
	require.NoError(t, err)
	require.Equal(t, ReconciliationRecordSchemaLegacy, legacyRecord.ResultSchemaVersion)
	require.Equal(t, legacy.Basis, legacyRecord.Basis)

	_, err = second.GetReconciliationRetention(ctx, legacy.ID, legacy.Version)
	require.ErrorIs(t, err, ErrReconciliationRetentionMismatch)

	envelope, err := second.GetReconciliation(ctx, retention.ID, retention.ResultRevision)
	require.NoError(t, err)
	require.Equal(t, ReconciliationRecordSchemaRetention, envelope.ResultSchemaVersion)

	page, err := second.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-restart"})
	require.NoError(t, err)
	require.Len(t, page.Reconciliations, 1)
	require.Equal(t, retention.ID, page.Reconciliations[0].ID)
	page, err = second.ListReconciliations(ctx, ReconciliationQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-restart-retention"})
	require.NoError(t, err)
	require.Len(t, page.Reconciliations, 1)
	require.Equal(t, legacy.ID, page.Reconciliations[0].ID)
	require.Equal(t, ReconciliationRecordSchemaLegacy, page.Reconciliations[0].ResultSchemaVersion)
}

func TestReconciliationRetentionBoundsAndMalformed(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	valid := retentionTestResult(t, "test", "b-bounds", "retention-bounds", 1, time.Unix(1_700_002_600, 0).UTC())

	missingRevision := valid
	missingRevision.ResultRevision = 0
	require.ErrorIs(t, store.AppendReconciliationRetention(ctx, missingRevision), billing.ErrInvalidReconciliationRetention)

	missingID := valid
	missingID.ID = ""
	require.ErrorIs(t, store.AppendReconciliationRetention(ctx, missingID), billing.ErrInvalidReconciliationRetention)

	missingEvidence := valid
	missingEvidence.Quantity, missingEvidence.Monetary, missingEvidence.Aggregate = nil, nil, nil
	require.ErrorIs(t, store.AppendReconciliationRetention(ctx, missingEvidence), billing.ErrInvalidReconciliationRetention)

	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ?`, "test").Scan(ctx, &count))
	require.Zero(t, count, "malformed results must not reach the database")

	require.NoError(t, store.AppendReconciliationRetention(ctx, valid))
	_, err := store.GetReconciliationRetention(ctx, "", 1)
	require.Error(t, err)
	_, err = store.GetReconciliationRetention(ctx, valid.ID, 0)
	require.Error(t, err)
}

// retentionAggregateResultForStore builds an aggregate-only retention result
// whose comparable P-E finding carries a nested tolerance evaluation, so
// durable aggregate/evaluation mutations have a complete producer-derived
// target.
func retentionAggregateResultForStore(t *testing.T, storeID, bLegID, id string, revision uint64, createdAt time.Time) billing.ReconciliationRetentionResult {
	t.Helper()
	expected := retentionTestValuation(t, storeID, bLegID, id+"-e", economics.BasisLocalExpected, "1.00")
	reported := retentionTestValuation(t, storeID, bLegID, id+"-p", economics.BasisProviderReported, "1.32")
	reported.QualifierSnapshotRef = &economics.SnapshotContentRef{ContentRef: "catalog://retention/qualifiers/v1", ContentHash: strings.Repeat("a", 64)}
	require.NoError(t, reported.Validate())
	monetary, err := billing.DecomposeMonetaryDiscrepancies(billing.MonetaryDiscrepancyInput{Valuations: []economics.Valuation{expected, reported}})
	require.NoError(t, err)
	findings, err := billing.ReconciliationFindingsFromMonetaryComparison("call:"+bLegID, monetary)
	require.NoError(t, err)
	limit := metering.Decimal{Coefficient: "4", Scale: 1}
	policy := billing.ReconciliationTolerancePolicy{
		Version: billing.ReconciliationTolerancePolicyV1,
		Ref:     billing.VersionRef{ID: "tolerance-policy", Version: "v1"},
		Rules: []billing.ReconciliationToleranceRule{{
			ID: "usd", Scope: billing.ReconciliationToleranceScope{Currency: "USD"}, AbsoluteLimit: &limit,
		}},
	}
	aggregate, err := billing.AggregateReconciliationFindings(policy, findings)
	require.NoError(t, err)
	result := retentionTestResult(t, storeID, bLegID, id, revision, createdAt)
	result.Policy = billing.VersionRef{ID: policy.Ref.ID, Version: policy.Ref.Version}
	// The retained monetary comparison stays the authoritative source for the
	// aggregate findings; an aggregate without its evidence is unbound.
	result.Monetary = &monetary
	result.Aggregate = &aggregate
	return result
}

// TestReconciliationRetentionRejectsForgedAggregateTermsDurably proves both the
// specialized and the raw generic schema-2 append paths refuse canonical
// payloads whose aggregate classification, nested tolerance evaluation or row
// totals were forged, and that no forged row reaches the table.
func TestReconciliationRetentionRejectsForgedAggregateTermsDurably(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	createdAt := time.Unix(1_700_005_300, 0).UTC()

	aggregateFindingJSON := func(t *testing.T, document map[string]any) map[string]any {
		t.Helper()
		aggregate, ok := document["aggregate"].(map[string]any)
		require.True(t, ok, "canonical payload must carry the aggregate block")
		findings, ok := aggregate["findings"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, findings)
		finding, ok := findings[0].(map[string]any)
		require.True(t, ok)
		return finding
	}

	cases := []struct {
		name   string
		build  func(t *testing.T, id string) billing.ReconciliationRetentionResult
		mutate func(t *testing.T, result *billing.ReconciliationRetentionResult)
		forge  func(t *testing.T, document map[string]any)
	}{
		{
			name: "forged evaluation signed delta",
			build: func(t *testing.T, id string) billing.ReconciliationRetentionResult {
				return retentionAggregateResultForStore(t, "test", "b-aggregate-forged", id, 1, createdAt)
			},
			mutate: func(t *testing.T, result *billing.ReconciliationRetentionResult) {
				result.Aggregate.Findings[0].Evaluation.SignedDelta = &billing.MonetaryExactAmount{
					Currency: "USD", Decimal: &metering.Decimal{Coefficient: "31", Scale: 2},
				}
			},
			forge: func(t *testing.T, document map[string]any) {
				finding := aggregateFindingJSON(t, document)
				evaluation, ok := finding["evaluation"].(map[string]any)
				require.True(t, ok)
				amount, ok := evaluation["signed_delta"].(map[string]any)
				require.True(t, ok)
				decimal, ok := amount["decimal"].(map[string]any)
				require.True(t, ok)
				decimal["coefficient"] = "31"
			},
		},
		{
			name: "unknown evaluated status",
			build: func(t *testing.T, id string) billing.ReconciliationRetentionResult {
				return retentionTestResult(t, "test", "b-aggregate-forged", id, 1, createdAt)
			},
			mutate: func(t *testing.T, result *billing.ReconciliationRetentionResult) {
				result.Aggregate.Findings[0].EvaluatedStatus = "bogus"
			},
			forge: func(t *testing.T, document map[string]any) {
				aggregateFindingJSON(t, document)["evaluated_status"] = "bogus"
			},
		},
		{
			name: "forged row gross absolute total",
			build: func(t *testing.T, id string) billing.ReconciliationRetentionResult {
				return retentionTestResult(t, "test", "b-aggregate-forged", id, 1, createdAt)
			},
			mutate: func(t *testing.T, result *billing.ReconciliationRetentionResult) {
				result.Aggregate.Rows[0].GrossAbsoluteDiscrepancy = &billing.MonetaryExactAmount{
					Currency: "USD", Decimal: &metering.Decimal{Coefficient: "1"},
				}
			},
			forge: func(t *testing.T, document map[string]any) {
				aggregate, ok := document["aggregate"].(map[string]any)
				require.True(t, ok, "aggregate must be a JSON object")
				rows, ok := aggregate["rows"].([]any)
				require.True(t, ok)
				require.NotEmpty(t, rows)
				row, ok := rows[0].(map[string]any)
				require.True(t, ok)
				amount, ok := row["gross_absolute_discrepancy"].(map[string]any)
				require.True(t, ok)
				decimal, ok := amount["decimal"].(map[string]any)
				require.True(t, ok)
				decimal["coefficient"] = "1"
			},
		},
	}

	for _, tc := range cases {
		slug := strings.ReplaceAll(tc.name, " ", "-")
		t.Run(tc.name+" specialized append", func(t *testing.T) {
			result := tc.build(t, "retention-aggregate-specialized-"+slug)
			tc.mutate(t, &result)
			err := store.AppendReconciliationRetention(ctx, result)
			require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		})
		t.Run(tc.name+" generic schema-2 append", func(t *testing.T) {
			result := tc.build(t, "retention-aggregate-generic-"+slug)
			forged := forgeRetentionCanonical(t, result, func(document map[string]any) { tc.forge(t, document) })
			poison := ReconciliationRecord{
				ID: result.ID, Version: result.ResultRevision,
				Subject:             result.Subject,
				ResultSchemaVersion: ReconciliationRecordSchemaRetention,
				ResultJSON:          forged,
				CreatedAt:           result.CreatedAt,
			}
			err := store.AppendReconciliation(ctx, poison)
			require.ErrorIs(t, err, ErrInvalidReconciliation)
			require.ErrorIs(t, err, billing.ErrInvalidReconciliationRetention)
		})
	}

	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ?`, "test").Scan(ctx, &count))
	require.Zero(t, count, "no forged aggregate row may be persisted")

	valid := retentionAggregateResultForStore(t, "test", "b-aggregate-forged", "retention-aggregate-valid", 1, createdAt.Add(time.Minute))
	require.NoError(t, store.AppendReconciliationRetention(ctx, valid), "a valid aggregate retention result must still persist")
}

func TestReconciliationRetentionSchemaAddsColumnIndexAndMigration(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.NoError(t, VerifySchema(ctx, store.db))

	var columnCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_reconciliations') WHERE name = 'result_schema_version'`).Scan(ctx, &columnCount))
	require.Equal(t, 1, columnCount)

	var indexName string
	require.NoError(t, store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, billingReconciliationRetentionIndex).Scan(ctx, &indexName))
	require.Equal(t, billingReconciliationRetentionIndex, indexName)

	var tenantIndexName string
	require.NoError(t, store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, billingReconciliationRetentionTenantIndex).Scan(ctx, &tenantIndexName))
	require.Equal(t, billingReconciliationRetentionTenantIndex, tenantIndexName)

	require.Contains(t, RequiredMigrationNames, BillingReconciliationRetentionMigrationName)
	require.Contains(t, RequiredMigrationNames, BillingReconciliationRetentionTenantIndexMigrationName)

	observation := phase4EconomicsObservation("test", "obs-legacy-default", 1)
	legacy := phase4Reconciliation(t, observation, "legacy-default", 1, time.Unix(1_700_002_700, 0).UTC())
	require.NoError(t, store.AppendReconciliation(ctx, legacy))
	var storedVersion int
	require.NoError(t, store.db.NewRaw(`SELECT result_schema_version FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ?`, "test", legacy.ID).Scan(ctx, &storedVersion))
	require.Equal(t, int(ReconciliationRecordSchemaLegacy), storedVersion)
}
