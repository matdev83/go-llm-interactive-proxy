package billingstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// retentionResultWithTenant rewrites the tenant consistently across the parent
// subject and every retained monetary valuation subject so the result remains
// internally consistent for a different tenant.
func retentionResultWithTenant(t *testing.T, storeID, bLegID, id, tenant string, revision uint64, createdAt time.Time) billing.ReconciliationRetentionResult {
	t.Helper()
	result := retentionTestResult(t, storeID, bLegID, id, revision, createdAt)
	result.Subject.TenantID = tenant
	result.Monetary.Subject.TenantID = tenant
	for i := range result.Monetary.Valuations {
		result.Monetary.Valuations[i].Valuation.Subject.TenantID = tenant
	}
	return result
}

// seedRetentionLegacyRows inserts bounded decoy rows directly so a
// high-cardinality subject partition exists. The rows carry
// result_schema_version=1 and are excluded from retention latest reads.
func seedRetentionLegacyRows(t *testing.T, ctx context.Context, store *DurableStore, storeID, subjectID, tenantID string, count int, createdAtBase int64) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO billing_reconciliations(store_id, reconciliation_id, reconciliation_version, subject_kind, subject_id, subject_json, tenant_id, scope, basis, result_schema_version, input_set_hash, local_input_hash, provider_input_hash, policy_id, policy_version, result_json, canonical_json, fingerprint, projection_version, created_at_unix) VALUES (?,?,?,?,?,'{}',?,'','',1,'','','','','','{}','','',1,?)`)
	require.NoError(t, err)
	defer func() { _ = statement.Close() }()
	for i := 0; i < count; i++ {
		_, err := statement.ExecContext(ctx, storeID, fmt.Sprintf("legacy-%s-%d", subjectID, i), 1, string(metering.SubjectBLeg), subjectID, tenantID, createdAtBase+int64(i))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

// TestLatestReconciliationRetentionRequiresBoundedSubject proves the latest API
// is subject-scoped and validates every accepted identity before SQL.
func TestLatestReconciliationRetentionRequiresBoundedSubject(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	overlong := strings.Repeat("a", metering.MaxSchemaIDBytes+1)

	cases := []struct {
		name  string
		query ReconciliationRetentionQuery
		want  error
	}{
		{name: "empty query", query: ReconciliationRetentionQuery{}, want: ErrQueryTooBroad},
		{name: "tenant only", query: ReconciliationRetentionQuery{TenantID: "tenant-test"}, want: ErrQueryTooBroad},
		{name: "kind without id", query: ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg}, want: ErrQueryTooBroad},
		{name: "id without kind", query: ReconciliationRetentionQuery{SubjectID: "b-one"}, want: ErrQueryTooBroad},
		{name: "whitespace id", query: ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: " b-one "}, want: ErrReconciliationRetentionQuery},
		{name: "control character id", query: ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b\x01one"}, want: ErrReconciliationRetentionQuery},
		{name: "overlong id", query: ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: overlong}, want: ErrReconciliationRetentionQuery},
		{name: "overlong tenant", query: ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-one", TenantID: overlong}, want: ErrReconciliationRetentionQuery},
		{name: "whitespace tenant", query: ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-one", TenantID: " tenant "}, want: ErrReconciliationRetentionQuery},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := store.LatestReconciliationRetention(ctx, tc.query); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("unknown subject kind", func(t *testing.T) {
		_, _, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: "bogus", SubjectID: "b-one"})
		if !errors.Is(err, ErrReconciliationRetentionQuery) {
			t.Fatalf("error = %v, want ErrReconciliationRetentionQuery", err)
		}
	})

	t.Run("valid subject still works", func(t *testing.T) {
		result := retentionTestResult(t, "test", "b-valid", "retention-valid-subject", 1, time.Unix(1_700_003_100, 0).UTC())
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
		got, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-valid"})
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, result.ID, got.ID)
	})
}

// TestLatestReconciliationRetentionPlanIsIndexBacked proves the supported
// subject-scoped query shapes use the retention index rather than scanning the
// whole table, even with many decoy rows present.
func TestLatestReconciliationRetentionPlanIsIndexBacked(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	seedRetentionLegacyRows(t, ctx, store, "test", "b-plan", "tenant-test", 2000, 1_700_000_000)
	valid := retentionTestResult(t, "test", "b-plan", "retention-plan", 1, time.Unix(1_700_003_200, 0).UTC())
	require.NoError(t, store.AppendReconciliationRetention(ctx, valid))

	for _, shape := range []struct {
		name       string
		sqlText    string
		index      string
		constraint string
	}{
		{name: "subject", sqlText: latestRetentionSubjectSQL, index: billingReconciliationRetentionIndex},
		{name: "subject tenant", sqlText: latestRetentionSubjectTenantSQL, index: billingReconciliationRetentionTenantIndex, constraint: "tenant_id=?"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			rows, err := store.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+shape.sqlText, "test", ReconciliationRecordSchemaRetention, string(metering.SubjectBLeg), "b-plan", "tenant-test")
			require.NoError(t, err)
			defer func() { _ = rows.Close() }()
			usedIndex := false
			usedConstraint := false
			searched := false
			var details []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
				details = append(details, detail)
				if strings.Contains(detail, shape.index) {
					usedIndex = true
				}
				if shape.constraint != "" && strings.Contains(detail, shape.constraint) {
					usedConstraint = true
				}
				if strings.Contains(detail, "SEARCH") {
					searched = true
				}
				if strings.TrimSpace(detail) == "SCAN billing_reconciliations" {
					t.Fatalf("plan performs a full table scan: %s", detail)
				}
			}
			require.NoError(t, rows.Err())
			require.True(t, usedIndex, "plan must use %s: %v", shape.index, details)
			require.True(t, searched, "plan must use an indexed range search, got: %v", details)
			if shape.constraint != "" {
				require.True(t, usedConstraint, "plan must carry %s as an index constraint: %v", shape.constraint, details)
			}
		})
	}

	t.Run("latest ignores decoys", func(t *testing.T) {
		got, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-plan"})
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, valid.ID, got.ID)
	})
}

// TestLatestReconciliationRetentionHighCardinalityStableOrder proves the
// deterministic tie-break (created_at, id, revision, row id) under a large
// subject partition.
func TestLatestReconciliationRetentionHighCardinalityStableOrder(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	seedRetentionLegacyRows(t, ctx, store, "test", "b-high", "tenant-test", 5000, 1_700_000_000)

	createdAt := time.Unix(1_700_003_300, 0).UTC()
	appendResult := func(t *testing.T, id string, revision uint64, at time.Time) {
		t.Helper()
		result := retentionTestResult(t, "test", "b-high", id, revision, at)
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	}
	appendResult(t, "retention-tie-a", 1, createdAt)
	appendResult(t, "retention-tie-b", 1, createdAt)
	appendResult(t, "retention-tie-b", 2, createdAt)

	latest, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-high"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "retention-tie-b", latest.ID, "lexicographically greatest id wins on created_at tie")
	require.Equal(t, uint64(2), latest.ResultRevision, "greatest revision wins within the winning id")

	appendResult(t, "retention-tie-c", 1, createdAt)
	latest, found, err = store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-high"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "retention-tie-c", latest.ID)

	newer := retentionTestResult(t, "test", "b-high", "retention-newer", 1, createdAt.Add(time.Second))
	require.NoError(t, store.AppendReconciliationRetention(ctx, newer))
	latest, found, err = store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-high"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "retention-newer", latest.ID, "newest created_at wins")

	t.Run("tenant refinement isolates rows", func(t *testing.T) {
		_, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-high", TenantID: "tenant-other"})
		require.NoError(t, err)
		require.False(t, found)
	})
}

// TestLatestReconciliationRetentionTenantIsolationHighCardinality proves a
// subject id shared by two tenants returns each tenant's own newest row (and no
// other tenant row) under a large subject partition.
func TestLatestReconciliationRetentionTenantIsolationHighCardinality(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	seedRetentionLegacyRows(t, ctx, store, "test", "b-tenants", "tenant-a", 3000, 1_700_000_000)

	base := time.Unix(1_700_003_400, 0).UTC()
	tenantA := retentionResultWithTenant(t, "test", "b-tenants", "retention-tenant-a", "tenant-a", 1, base)
	tenantB := retentionResultWithTenant(t, "test", "b-tenants", "retention-tenant-b", "tenant-b", 1, base.Add(time.Minute))
	require.NoError(t, store.AppendReconciliationRetention(ctx, tenantA))
	require.NoError(t, store.AppendReconciliationRetention(ctx, tenantB))

	gotA, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-tenants", TenantID: "tenant-a"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, tenantA.ID, gotA.ID, "tenant refinement must not return the newer other-tenant row")

	gotB, found, err := store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-tenants", TenantID: "tenant-b"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, tenantB.ID, gotB.ID)

	_, found, err = store.LatestReconciliationRetention(ctx, ReconciliationRetentionQuery{SubjectKind: metering.SubjectBLeg, SubjectID: "b-tenants", TenantID: "tenant-missing"})
	require.NoError(t, err)
	require.False(t, found)
}
