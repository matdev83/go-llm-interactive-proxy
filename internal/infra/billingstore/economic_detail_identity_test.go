package billingstore

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Fresh Phase 16 Finding 2 boundary contract: reconciliation latest-revision
// selection and valuation stream identity must use the complete authoritative
// SubjectRef identity. A coarse kind/primary-id (or partition that omits an
// accepted subject field) silently merges independent economic subjects.

// edAppendRetentionSubject persists one full-result reconciliation retention for
// an explicit authoritative SubjectRef, so a test can vary lineages that share
// one primary subject id (for example one BLegID with different provider-charge
// or tenant lineage).
func edAppendRetentionSubject(t *testing.T, store *DurableStore, id string, subject metering.SubjectRef, localValue, providerValue string, revision uint64, createdAt time.Time) billing.ReconciliationRetentionResult {
	t.Helper()
	ctx := context.Background()
	local := edTestObservation(t, "obs-"+id+"-local", metering.OriginLocal, "stream-"+id+"-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, localValue)}, nil)
	provider := edTestObservation(t, "obs-"+id+"-provider", metering.OriginProvider, "stream-"+id+"-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, providerValue)}, nil)
	quantity, err := billing.CompareComponentQuantities(
		billing.ReconciliationEvidenceSet{Subject: subject, Tokenizer: "ed-tokenizer-v1", Observations: []metering.Observation{local}},
		billing.ReconciliationEvidenceSet{Subject: subject, Tokenizer: "ed-tokenizer-v1", Observations: []metering.Observation{provider}},
	)
	require.NoError(t, err)
	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
	}
	monetary, err := billing.DecomposeMonetaryDiscrepancies(billing.MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
		edTestValuation(t, id+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, id+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuation(t, id+"-p", economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", "1.32")),
	}})
	require.NoError(t, err)
	result := billing.ReconciliationRetentionResult{
		SchemaVersion: billing.ReconciliationRetentionSchemaVersionV1,
		ID:            id, ResultRevision: revision, Subject: subject, Scope: "call:" + subject.BillingCallID,
		Policy:       billing.VersionRef{ID: "policy-ed", Version: "v1"},
		InputSetHash: strings.Repeat("a", 64),
		ValuationIDs: []string{id + "-e", id + "-q", id + "-p"},
		Quantity:     &quantity, Monetary: &monetary,
		CreatedAt: createdAt,
	}
	require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	return result
}

func TestQueryEconomicDetailReconciliationSameBLegDistinctLineageSurvives(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-ident-lineage", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-ident-lineage", edTestLeg(t, "b-ident-lineage"))

	base := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-ident-lineage", callID.String(), "b-ident-lineage")
	chargeA := base
	chargeA.ProviderAccountKey = "prov-acct-a"
	chargeA.ProviderChargeID = "charge-a"
	chargeA.AttemptID = "attempt-a"
	chargeA.AttemptSeq = 1
	chargeB := base
	chargeB.ProviderAccountKey = "prov-acct-a"
	chargeB.ProviderChargeID = "charge-b"
	chargeB.AttemptID = "attempt-b"
	chargeB.AttemptSeq = 1

	edAppendRetentionSubject(t, store, "recon-lineage-a", chargeA, "100", "110", 1, time.Unix(1_700_020_200, 0).UTC())
	edAppendRetentionSubject(t, store, "recon-lineage-b", chargeB, "100", "120", 1, time.Unix(1_700_020_300, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-ident-lineage",
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 2, "distinct full SubjectRef identities sharing one BLegID must not collapse")
	seen := map[string]bool{}
	for _, entry := range got.Reconciliations {
		require.Equal(t, "b-ident-lineage", entry.Subject.BLegID)
		seen[entry.Subject.ProviderChargeID] = true
	}
	require.True(t, seen["charge-a"], "the first lineage identity must survive")
	require.True(t, seen["charge-b"], "the second lineage identity must survive")
}

func TestQueryEconomicDetailReconciliationRevisionWithinExactIdentity(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-ident-revision", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-ident-revision", edTestLeg(t, "b-ident-revision"))

	base := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-ident-revision", callID.String(), "b-ident-revision")
	identityA := base
	identityA.ProviderChargeID = "charge-a"
	identityB := base
	identityB.ProviderChargeID = "charge-b"

	// Two revisions of one exact identity: the later matched revision wins.
	edAppendRetentionSubject(t, store, "recon-ident-rev-a", identityA, "100", "110", 1, time.Unix(1_700_020_200, 0).UTC())
	edAppendRetentionSubject(t, store, "recon-ident-rev-a", identityA, "100", "100", 2, time.Unix(1_700_020_300, 0).UTC())
	// A distinct identity sharing the same BLegID is independent, not a revision.
	edAppendRetentionSubject(t, store, "recon-ident-rev-b", identityB, "100", "120", 1, time.Unix(1_700_020_400, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-ident-revision",
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 2, "one latest revision per exact identity, not one row per primary id")
	byCharge := map[string]billing.EconomicDetailReconciliation{}
	for _, entry := range got.Reconciliations {
		byCharge[entry.Subject.ProviderChargeID] = entry
	}
	require.Contains(t, byCharge, "charge-a")
	require.Contains(t, byCharge, "charge-b")
	require.NotNil(t, byCharge["charge-a"].Quantity)
	require.Equal(t, billing.ReconciliationStatusMatched, byCharge["charge-a"].Quantity.Status, "the latest revision must win within one exact identity")
}

func TestQueryEconomicDetailReconciliationTenantIdentityIsolation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-ident-tenant", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-ident-tenant", edTestLeg(t, "b-ident-tenant"))

	base := edTestBLegSubject(store.StoreID(), "", account.ID, "a-ed-ident-tenant", callID.String(), "b-ident-tenant")
	tenantA := base
	tenantA.TenantID = "tenant-a"
	tenantB := base
	tenantB.TenantID = "tenant-b"

	edAppendRetentionSubject(t, store, "recon-tenant-a", tenantA, "100", "110", 1, time.Unix(1_700_020_200, 0).UTC())
	// The foreign tenant is newer, so a coarse LIMIT 1 would hide the other.
	edAppendRetentionSubject(t, store, "recon-tenant-b", tenantB, "100", "120", 1, time.Unix(1_700_020_300, 0).UTC())

	unscoped, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-ident-tenant",
	})
	require.NoError(t, err)
	require.Len(t, unscoped.Reconciliations, 2, "distinct tenant identities must both survive when no tenant scope is requested")

	scoped, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-a", AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-ident-tenant",
	})
	require.NoError(t, err)
	require.Len(t, scoped.Reconciliations, 1, "tenant scope must isolate one tenant identity")
	require.Equal(t, "tenant-a", scoped.Reconciliations[0].Subject.TenantID)
}

func TestQueryEconomicDetailValuationTemporalIdentityStreamsSurvive(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-ident-temporal", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-ident-temporal", edTestLeg(t, "b-ident-temporal"))

	base := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-ident-temporal", callID.String(), "b-ident-temporal")
	first := base
	first.StartAt = time.Unix(1_700_020_000, 0).UTC()
	first.EndAt = time.Unix(1_700_020_100, 0).UTC()
	second := base
	second.StartAt = time.Unix(1_700_030_000, 0).UTC()
	second.EndAt = time.Unix(1_700_030_100, 0).UTC()

	observation := edTestObservation(t, "obs-ed-temporal-local", metering.OriginLocal, "stream-ed-temporal-local", 1, base,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	refs := []metering.ObservationRef{edObservationRef(t, store.StoreID(), observation)}
	require.NoError(t, store.AppendValuation(ctx, edTestValuation(t, "val-ed-temporal-1", economics.BasisLocalExpected, first, refs, edTestCurrencyTotal(t, "USD", "1.00"))))
	require.NoError(t, store.AppendValuation(ctx, edTestValuation(t, "val-ed-temporal-2", economics.BasisLocalExpected, second, refs, edTestCurrencyTotal(t, "USD", "2.00"))))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-ident-temporal",
	})
	require.NoError(t, err)
	require.Len(t, got.Valuations, 2, "valuation streams that differ only by an accepted temporal subject field must not collapse")
	seen := map[time.Time]bool{}
	for _, valuation := range got.Valuations {
		seen[valuation.Subject.StartAt] = true
	}
	require.True(t, seen[first.StartAt], "the first temporal identity must survive")
	require.True(t, seen[second.StartAt], "the second temporal identity must survive")
}

// TestEconomicDetailValuationStreamSubjectFieldsCoverSubjectRef guards the SQL
// partition list against drift: every persisted canonical SubjectRef field must
// participate in the database-side latest-revision partition, or two accepted
// identities silently merge before Go sees them.
func TestEconomicDetailValuationStreamSubjectFieldsCoverSubjectRef(t *testing.T) {
	t.Parallel()
	covered := make(map[string]struct{}, len(economicDetailValuationStreamSubjectFields))
	for _, field := range economicDetailValuationStreamSubjectFields {
		covered[field] = struct{}{}
	}
	typ := reflect.TypeFor[metering.SubjectRef]()
	for field := range typ.Fields() {
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		_, ok := covered[tag]
		require.True(t, ok, "SubjectRef json field %q must participate in the valuation stream partition", tag)
	}
}
