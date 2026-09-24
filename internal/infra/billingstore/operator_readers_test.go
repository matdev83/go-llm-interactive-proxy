package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.2A RED contract: durable bounded operator readers over existing
// tables. Discrepancy views read retention rows; statement views read
// statement lines; adjustment views read correction rows; allowance views
// project journal gauge history through a narrow port. Every query is
// scope-bound, paged with tamper-evident cursors and dual-dialect SQL.

func orTestRetentionSubject(storeID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: "tenant-or",
		ALegID: "a-or", BillingCallID: "call-or", BLegID: "b-or",
	}
}

// orTestRetentionSubjectWithAccount is the account-owned variant used by
// account-scope regression tests.
func orTestRetentionSubjectWithAccount(storeID, accountID string) metering.SubjectRef {
	subject := orTestRetentionSubject(storeID)
	subject.AccountID = accountID
	return subject
}

func orTestObservation(t *testing.T, id, origin, storeID, value string) metering.Observation {
	t.Helper()
	return orTestObservationForSubject(t, id, origin, value, orTestRetentionSubject(storeID))
}

func orTestObservationForSubject(t *testing.T, id, origin, value string, subject metering.SubjectRef) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionLocalTokenizer
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	decimal, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	now := time.Unix(1_700_030_000, 0).UTC()
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
		MappingRef: "operator-readers.test.v1",
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
				Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
			},
			Value: &decimal, Quality: metering.QualityObserved, MethodRef: "or-tokenizer-v1",
		}},
	}
	require.NoError(t, observation.Validate())
	return observation
}

func orTestValuation(t *testing.T, storeID, id string, basis economics.ValuationBasis, amount string) economics.Valuation {
	t.Helper()
	return orTestValuationForSubject(t, storeID, id, basis, amount, orTestRetentionSubject(storeID))
}

func orTestValuationForSubject(t *testing.T, storeID, id string, basis economics.ValuationBasis, amount string, subject metering.SubjectRef) economics.Valuation {
	t.Helper()
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
		InputSetHash:         strings.Repeat("d", 64),
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://or/qualifiers/v1", ContentHash: strings.Repeat("a", 64)},
		Completeness:         economics.CompletenessComplete,
		CreatedAt:            time.Unix(1_700_030_100, 0).UTC(),
		Totals: []economics.CurrencyTotal{{
			Currency: "USD", Amount: &decimal,
			RoundedAmount: economics.Money{NanoUnits: nanos, Currency: "USD", Present: true},
		}},
	}
	if basis != economics.BasisProviderReported {
		valuation.Tariff = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-or", Version: "v1"}}
		valuation.TariffContent = &economics.SnapshotContentRef{ContentRef: "catalog://or/tariff/v1", ContentHash: strings.Repeat("f", 64)}
		valuation.Rater = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-or", Version: "v1"}, RaterID: "reference-rater"}
		valuation.RaterContent = &economics.SnapshotContentRef{ContentRef: "catalog://or/rater/v1", ContentHash: strings.Repeat("e", 64)}
	}
	require.NoError(t, valuation.Validate())
	return valuation
}

func orTestRetention(t *testing.T, storeID, id string, revision uint64) billing.ReconciliationRetentionResult {
	t.Helper()
	return orTestRetentionForSubject(t, storeID, id, revision, orTestRetentionSubject(storeID))
}

func orTestRetentionForSubject(t *testing.T, storeID, id string, revision uint64, subject metering.SubjectRef) billing.ReconciliationRetentionResult {
	t.Helper()
	local := orTestObservationForSubject(t, id+"-local", metering.OriginLocal, "100", subject)
	provider := orTestObservationForSubject(t, id+"-provider", metering.OriginProvider, "110", subject)
	quantity, err := billing.CompareComponentQuantities(
		billing.ReconciliationEvidenceSet{Subject: subject, Tokenizer: "or-tokenizer-v1", Observations: []metering.Observation{local}},
		billing.ReconciliationEvidenceSet{Subject: subject, Tokenizer: "or-tokenizer-v1", Observations: []metering.Observation{provider}},
	)
	require.NoError(t, err)
	monetary, err := billing.DecomposeMonetaryDiscrepancies(billing.MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
		orTestValuationForSubject(t, storeID, id+"-e", economics.BasisLocalExpected, "1.00", subject),
		orTestValuationForSubject(t, storeID, id+"-q", economics.BasisProviderQuantityLocal, "1.10", subject),
		orTestValuationForSubject(t, storeID, id+"-p", economics.BasisProviderReported, "1.32", subject),
	}})
	require.NoError(t, err)
	localRef, err := local.Ref(storeID)
	require.NoError(t, err)
	providerRef, err := provider.Ref(storeID)
	require.NoError(t, err)
	return billing.ReconciliationRetentionResult{
		SchemaVersion: billing.ReconciliationRetentionSchemaVersionV1,
		ID:            id, ResultRevision: revision, Subject: subject, Scope: "call:or",
		Policy:          billing.VersionRef{ID: "policy-or", Version: "v1"},
		InputSetHash:    strings.Repeat("a", 64),
		ValuationIDs:    []string{id + "-e", id + "-q", id + "-p"},
		ObservationRefs: []metering.ObservationRef{localRef, providerRef},
		Quantity:        &quantity, Monetary: &monetary,
		Diagnostics: []billing.ReconciliationRetentionDiagnostic{{Code: "metering_difference", Reason: billing.ReconciliationReasonNone}},
		CreatedAt:   time.Unix(1_700_030_200, 0).UTC(),
	}
}

func TestQueryDiscrepanciesPagesBoundedHistory(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for i, id := range []string{"or-recon-1", "or-recon-2", "or-recon-3"} {
		result := orTestRetention(t, store.StoreID(), id, 1)
		result.CreatedAt = time.Unix(1_700_030_200+int64(i), 0).UTC()
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	}

	query := economics.DiscrepancyQuery{Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 2}
	first, err := store.QueryDiscrepancies(ctx, query)
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)
	require.NoError(t, first.Validate())

	second, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 2, Cursor: first.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.Empty(t, second.NextCursor)

	seen := map[string]bool{}
	for _, item := range append(append([]economics.DiscrepancyView{}, first.Items...), second.Items...) {
		require.False(t, seen[item.ID], "item %q must appear exactly once", item.ID)
		seen[item.ID] = true
		require.Equal(t, economics.DiscrepancyDiscrepant, item.QuantityStatus)
		require.True(t, item.QuantityComplete)
		require.Equal(t, economics.MonetaryComplete, item.MonetaryState)
		require.Equal(t, "policy-or", item.PolicyID)
	}
	require.Len(t, seen, 3)
}

func TestQueryDiscrepanciesRejectsTamperedCursor(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.AppendReconciliationRetention(ctx, orTestRetention(t, store.StoreID(), "or-tamper", 1)))
	secondRetention := orTestRetention(t, store.StoreID(), "or-tamper-2", 1)
	secondRetention.CreatedAt = time.Unix(1_700_030_201, 0).UTC()
	require.NoError(t, store.AppendReconciliationRetention(ctx, secondRetention))

	first, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	tampered := first.NextCursor[:len(first.NextCursor)-2] + "xx"
	_, err = store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 1, Cursor: tampered,
	})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)

	_, err = store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-other"}, Limit: 1, Cursor: first.NextCursor,
	})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid, "cross-scope cursor replay must fail closed")
}

func TestQueryDiscrepanciesTenantIsolation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.AppendReconciliationRetention(ctx, orTestRetention(t, store.StoreID(), "or-iso", 1)))

	got, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-unrelated"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, got.Items)
	require.Empty(t, got.NextCursor)
}

func TestQueryDiscrepanciesSubjectFilter(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.AppendReconciliationRetention(ctx, orTestRetention(t, store.StoreID(), "or-subject", 1)))

	got, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope:       economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"},
		SubjectKind: metering.SubjectBLeg, SubjectID: "b-or", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, "or-subject", got.Items[0].ID)

	empty, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope:       economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"},
		SubjectKind: metering.SubjectBLeg, SubjectID: "b-absent", Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, empty.Items)
}

// TestQueryDiscrepanciesAccountScopeRejectsAccountlessRows proves an
// account-only scope never surfaces retained reconciliation evidence whose
// authoritative subject carries no account ownership. Missing ownership is
// an absence, not an implicit match.
func TestQueryDiscrepanciesAccountScopeRejectsAccountlessRows(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.AppendReconciliationRetention(ctx, orTestRetention(t, store.StoreID(), "or-accountless", 1)))

	got, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: "or-account-a"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, got.Items)
	require.Empty(t, got.NextCursor)
}

// TestQueryDiscrepanciesAccountScopeFiltersBeforePagination proves foreign
// and account-less rows are excluded in SQL before LIMIT, so they neither leak
// nor deny or distort pagination for the matching account's rows. It also
// traverses the cursor to prove stable, duplicate-free continuation.
func TestQueryDiscrepanciesAccountScopeFiltersBeforePagination(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	matchingSubject := orTestRetentionSubjectWithAccount(store.StoreID(), "or-account-a")
	foreignSubject := orTestRetentionSubjectWithAccount(store.StoreID(), "or-account-b")
	specs := []struct {
		id      string
		subject *metering.SubjectRef
		offset  int64
	}{
		{id: "or-acct-mix-1", subject: &matchingSubject, offset: 0},
		{id: "or-acct-mix-2", subject: &foreignSubject, offset: 1},
		{id: "or-acct-mix-3", subject: nil, offset: 2},
		{id: "or-acct-mix-4", subject: &matchingSubject, offset: 3},
		{id: "or-acct-mix-5", subject: &foreignSubject, offset: 4},
		{id: "or-acct-mix-6", subject: &matchingSubject, offset: 5},
	}
	for _, spec := range specs {
		subject := orTestRetentionSubject(store.StoreID())
		if spec.subject != nil {
			subject = *spec.subject
		}
		result := orTestRetentionForSubject(t, store.StoreID(), spec.id, 1, subject)
		result.CreatedAt = time.Unix(1_700_030_200+spec.offset, 0).UTC()
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	}

	query := economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: "or-account-a"}, Limit: 2,
	}
	var pages int
	var seen []string
	for {
		page, err := store.QueryDiscrepancies(ctx, query)
		require.NoError(t, err, "a foreign or account-less row must not deny the matching page")
		require.LessOrEqual(t, len(page.Items), 2)
		for _, item := range page.Items {
			require.Equal(t, "or-account-a", item.Subject.AccountID, "only the requested account may be returned")
			seen = append(seen, item.ID)
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
	}
	require.Equal(t, []string{"or-acct-mix-1", "or-acct-mix-4", "or-acct-mix-6"}, seen)
	require.Equal(t, 2, pages, "matching rows must page deterministically without foreign-row distortion")
}

// TestQueryDiscrepanciesTenantScopeStaysAccountAgnostic pins the existing
// tenant-only contract: an explicit tenant scope returns every retained row in
// the tenant regardless of account ownership. The account predicate must only
// apply when an authoritative account is requested.
func TestQueryDiscrepanciesTenantScopeStaysAccountAgnostic(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for i, subject := range []metering.SubjectRef{
		orTestRetentionSubjectWithAccount(store.StoreID(), "or-tenant-account-a"),
		orTestRetentionSubjectWithAccount(store.StoreID(), "or-tenant-account-b"),
		orTestRetentionSubject(store.StoreID()),
	} {
		result := orTestRetentionForSubject(t, store.StoreID(), fmt.Sprintf("or-tenant-agnostic-%d", i), 1, subject)
		result.CreatedAt = time.Unix(1_700_030_200+int64(i), 0).UTC()
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	}

	got, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, got.Items, 3, "tenant-only scope must stay account-agnostic")
}

func TestQueryStatementLinesUnmatchedAggregate(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, store.StoreID(), "stmt-or", 1,
		statementImportLineSpec{ID: "line-m", Revision: 1, Amount: "2.50"},
		statementImportLineSpec{ID: "line-u", Revision: 1, Unmatched: true, UnmatchedReason: "account-period aggregate"},
	)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))

	unmatched, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Outcome: economics.StatementLineUnmatched, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, unmatched.Lines, 1)
	line := unmatched.Lines[0]
	require.Equal(t, "line-u", line.LineID)
	require.Equal(t, economics.StatementLineUnmatched, line.Outcome)
	require.Equal(t, "account-period aggregate", line.UnmatchedReason)
	require.Equal(t, "stmt-or", line.StatementID)
	require.Equal(t, "period-1", line.PeriodID)
	require.Empty(t, line.ChargeItemID, "unmatched aggregate must not gain a charge linkage")

	matched, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Outcome: economics.StatementLineMatched, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, matched.Lines, 1)
	require.Equal(t, "line-m", matched.Lines[0].LineID)
	require.Equal(t, "charge-line-m", matched.Lines[0].ChargeItemID)
}

// Finding 5B: the retained statement reader must resolve one exact
// statement-line identity so a detail resolver can read the durable outcome
// without scanning the whole statement.
func TestQueryStatementLinesLineIDFilter(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, store.StoreID(), "stmt-or-line", 1,
		statementImportLineSpec{ID: "line-target", Revision: 1, Amount: "1.00"},
		statementImportLineSpec{ID: "line-other", Revision: 1, Amount: "2.00"},
	)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))

	got, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", StatementID: "stmt-or-line", LineID: "line-target", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, got.Lines, 1)
	require.Equal(t, "line-target", got.Lines[0].LineID)

	otherLine, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", StatementID: "stmt-or-line", LineID: "line-other", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, otherLine.Lines, 1)
	require.Equal(t, "line-other", otherLine.Lines[0].LineID)
}

func TestQueryStatementLinesPaginationAndIsolation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, store.StoreID(), "stmt-or-page", 1,
		statementImportLineSpec{ID: "line-a", Revision: 1, Amount: "1.00"},
		statementImportLineSpec{ID: "line-b", Revision: 1, Amount: "2.00"},
		statementImportLineSpec{ID: "line-c", Revision: 1, Unmatched: true, UnmatchedReason: "aggregate"},
	)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))

	base := economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Limit: 2,
	}
	first, err := store.QueryStatementLines(ctx, base)
	require.NoError(t, err)
	require.Len(t, first.Lines, 2)
	require.NotEmpty(t, first.NextCursor)

	second, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Limit: 2, Cursor: first.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, second.Lines, 1)
	require.Empty(t, second.NextCursor)
	seen := map[string]bool{}
	for _, line := range append(append([]economics.StatementLineView{}, first.Lines...), second.Lines...) {
		require.False(t, seen[line.LineID])
		seen[line.LineID] = true
	}
	require.Len(t, seen, 3)

	other, err := store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-other"},
		ProviderAccountKey: "provider-account", Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, other.Lines)
}

func orTestAdjustmentHead(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, headKey string, currency string, nanos int64) {
	t.Helper()
	decimal := metering.DecimalFromNanoUnits(nanos)
	selected, err := billing.NewSelectedCostValuation(
		billing.SelectedCostValuationRef{ValuationID: "val-" + headKey, Revision: 1, InputSetHash: fmt.Sprintf("%064x", nanos)},
		billing.OperatorCostSelectionResult{
			Status: billing.OperatorCostSelectionStatusFinal, Provenance: billing.OperatorCostProvenanceAttempted,
			Currency: currency, Amount: &billing.MonetaryExactAmount{Currency: currency, Decimal: &decimal},
		},
	)
	require.NoError(t, err)
	_, err = store.ApplySelectedCostAdjustment(context.Background(), billing.SelectedCostAdjustmentInput{
		AccountID: accountID, CallID: callID, HeadKey: headKey,
		Subject:  metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: store.StoreID(), AccountID: accountID, ALegID: "a-or-adj", BillingCallID: callID.String(), BLegID: "b-or-adj"},
		Expected: billing.SelectedCostHeadExpectation{}, Selected: selected,
	})
	require.NoError(t, err)
}

func TestQueryAdjustmentsNegativeDeltaAndReplay(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "or-adj", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	orTestAdjustmentHead(t, store, account.ID, callID, "head-or", "USD", 10_000_000_000)

	// Downward correction 10 -> 8 USD posts a signed -2 USD delta once.
	decimal := metering.DecimalFromNanoUnits(8_000_000_000)
	selected, err := billing.NewSelectedCostValuation(
		billing.SelectedCostValuationRef{ValuationID: "val-head-or", Revision: 2, InputSetHash: strings.Repeat("e", 64)},
		billing.OperatorCostSelectionResult{
			Status: billing.OperatorCostSelectionStatusFinal, Provenance: billing.OperatorCostProvenanceAttempted,
			Currency: "USD", Amount: &billing.MonetaryExactAmount{Currency: "USD", Decimal: &decimal},
		},
	)
	require.NoError(t, err)
	head, err := store.GetSelectedCostHead(ctx, account.ID, callID, "head-or")
	require.NoError(t, err)
	_, err = store.ApplySelectedCostAdjustment(ctx, billing.SelectedCostAdjustmentInput{
		AccountID: account.ID, CallID: callID, HeadKey: "head-or",
		Subject: head.Subject, Expected: billing.SelectedCostHeadExpectation{Version: head.Version, Previous: head.Selected},
		Selected: selected,
	})
	require.NoError(t, err)

	// Exact replay of the correction is idempotent: still two rows.
	_, err = store.ApplySelectedCostAdjustment(ctx, billing.SelectedCostAdjustmentInput{
		AccountID: account.ID, CallID: callID, HeadKey: "head-or",
		Subject: head.Subject, Expected: billing.SelectedCostHeadExpectation{Version: head.Version, Previous: head.Selected},
		Selected: selected,
	})
	require.NoError(t, err)

	page, err := store.QueryAdjustments(ctx, economics.AdjustmentQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: account.ID}, CallID: callID.String(), Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Adjustments, 2)

	var correction *economics.AdjustmentView
	for i := range page.Adjustments {
		if page.Adjustments[i].Current.Revision == 2 {
			correction = &page.Adjustments[i]
		}
	}
	require.NotNil(t, correction)
	require.Equal(t, economics.AdjustmentApplied, correction.Status)
	require.Equal(t, economics.AdjustmentComparisonComparable, correction.Comparison)
	require.Equal(t, economics.AdjustmentPostingApplied, correction.Posting)
	require.Equal(t, economics.AdjustmentSelectionFinal, correction.SelectionStatus)
	require.NotNil(t, correction.Delta)
	require.Equal(t, "USD", correction.Delta.Currency)
	require.NotNil(t, correction.Delta.Decimal)
	require.Equal(t, "-2", correction.Delta.Decimal.Coefficient)
	require.Equal(t, uint8(0), correction.Delta.Decimal.Scale)
	require.NoError(t, page.Validate())

	// Planes are sourced from their own durable columns: the initial
	// posting carries not_evaluated comparison while the correction carries
	// comparable, and every view field matches its own column.
	var initial *economics.AdjustmentView
	for i := range page.Adjustments {
		if page.Adjustments[i].Current.Revision == 1 {
			initial = &page.Adjustments[i]
		}
	}
	require.NotNil(t, initial)
	require.Equal(t, economics.AdjustmentComparisonNotEvaluated, initial.Comparison)
	type rawAdjustmentRow struct {
		Status     string `bun:"status"`
		Comparison string `bun:"comparison"`
		Posting    string `bun:"posting"`
	}
	for _, item := range page.Adjustments {
		var raw rawAdjustmentRow
		require.NoError(t, store.db.NewRaw(`SELECT status, comparison, posting FROM billing_selected_cost_adjustments WHERE store_id = ? AND operation_key = ?`,
			store.StoreID(), item.OperationKey).Scan(ctx, &raw))
		require.Equal(t, raw.Status, string(item.Status))
		require.Equal(t, raw.Comparison, string(item.Comparison))
		require.Equal(t, raw.Posting, string(item.Posting))
	}
}

func TestQueryAdjustmentsIsolationAndPaging(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for _, accountID := range []string{"or-adj-a", "or-adj-b"} {
		account := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
		require.NoError(t, store.CreateAccount(ctx, account))
		callID, err := billing.NewBillingCallID()
		require.NoError(t, err)
		orTestAdjustmentHead(t, store, accountID, callID, "head-"+accountID, "USD", 5_000_000_000)
	}

	page, err := store.QueryAdjustments(ctx, economics.AdjustmentQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: "or-adj-a"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Adjustments, 1)
	require.Equal(t, "or-adj-a", page.Adjustments[0].AccountID)

	paged, err := store.QueryAdjustments(ctx, economics.AdjustmentQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: "or-adj-b"}, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, paged.Adjustments, 1)
}

type orStubAllowanceSource struct {
	page coremetering.AccountWindowObservationPage
	err  error
}

func (s orStubAllowanceSource) ListAccountWindowObservations(context.Context, coremetering.AccountWindowQuery) (coremetering.AccountWindowObservationPage, error) {
	return s.page, s.err
}

func orTestGauge(t *testing.T, storeID, id string, value string) metering.Observation {
	t.Helper()
	decimal, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	now := time.Unix(1_700_030_300, 0).UTC()
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: "stream-or-aw", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority:   metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectAccountWindow, StoreID: storeID, TenantID: "tenant-or",
			ProviderAccountKey: "provider-or", PoolID: "primary", WindowID: "minute",
			ResetAt: now.Add(time.Minute),
		},
		Correlation: metering.CorrelationV2{StoreID: storeID, TenantID: "tenant-or", ProviderAccountKey: "provider-or"},
		Semantics:   metering.SemanticsGauge, ObservedAt: now, ReceivedAt: now,
		MappingRef: "operator-readers.test.v1",
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction: metering.DirectionNone, Component: "provider:account_utilization",
				Unit: metering.UnitPercent, SchemaID: "provider:account:v1",
			},
			Value: &decimal, Quality: metering.QualityObserved,
		}},
	}
	require.NoError(t, observation.Validate())
	return observation
}

func TestAllowanceReaderMapsJournalHistory(t *testing.T) {
	t.Parallel()
	authority := newSQLiteTestStore(t)
	first := orTestGauge(t, "test", "obs-or-aw-1", "12.5")
	second := orTestGauge(t, "test", "obs-or-aw-2", "13.0")
	reader := AllowanceReader{Source: orStubAllowanceSource{
		page: coremetering.AccountWindowObservationPage{Observations: []metering.Observation{first, second}, NextCursor: "journal-cursor"},
	}, Authority: authority}

	scope := economics.OperatorScope{StoreID: "test", TenantID: "tenant-or"}
	got, err := reader.QueryAllowances(context.Background(), economics.AllowanceQuery{
		Scope:              scope,
		ProviderAccountKey: "provider-or", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, got.Observations, 2)
	require.True(t, strings.HasPrefix(got.NextCursor, operatorCursorPrefix),
		"the journal continuation must be re-sealed as an operator cursor")
	decoded, err := decodeOperatorCursor(got.NextCursor, operatorCursorKindAllowance, "test",
		testAllowanceFilter(scope, "provider-or", "", ""), authority.cursorKey)
	require.NoError(t, err)
	require.Equal(t, "journal-cursor", decoded.Source)
	require.Equal(t, allowanceCursorOrder, decoded.Order)
	require.NoError(t, got.Validate())
	// Gauges stay independent: no summation, no inferred debit.
	firstRat, err := got.Observations[0].Measures[0].Value.ToRat()
	require.NoError(t, err)
	require.Equal(t, "25/2", firstRat.RatString())
	secondRat, err := got.Observations[1].Measures[0].Value.ToRat()
	require.NoError(t, err)
	require.Equal(t, "13", secondRat.RatString())
}

func TestAllowanceReaderRejectsAccountScope(t *testing.T) {
	t.Parallel()
	reader := AllowanceReader{Source: orStubAllowanceSource{}}
	_, err := reader.QueryAllowances(context.Background(), economics.AllowanceQuery{
		Scope:              economics.OperatorScope{StoreID: "test", TenantID: "tenant-or", AccountID: "customer-acct"},
		ProviderAccountKey: "provider-or",
	})
	require.ErrorIs(t, err, economics.ErrOperatorQueryInvalid)
}

func TestAllowanceReaderJournalDurable(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", fmt.Sprintf("file:allowance-journal-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", time.Now().UnixNano()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	require.NoError(t, err)
	journal, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = journal.Close() })

	require.NoError(t, journal.AppendAccountWindowObservation(ctx, orTestGauge(t, "test", "obs-or-j-1", "12.5")))
	require.NoError(t, journal.AppendAccountWindowObservation(ctx, orTestGauge(t, "test", "obs-or-j-2", "13.0")))

	reader := AllowanceReader{Source: journal}
	got, err := reader.QueryAllowances(ctx, economics.AllowanceQuery{
		Scope:              economics.OperatorScope{StoreID: "test", TenantID: "tenant-or"},
		ProviderAccountKey: "provider-or", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, got.Observations, 2)
	require.Empty(t, got.NextCursor)
	require.NoError(t, got.Validate())

	other, err := reader.QueryAllowances(ctx, economics.AllowanceQuery{
		Scope:              economics.OperatorScope{StoreID: "test", TenantID: "tenant-other"},
		ProviderAccountKey: "provider-or", Limit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, other.Observations)
}
