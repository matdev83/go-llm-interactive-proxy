package billingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.3B RED contract: bounded durable economics health snapshot.
// Queue reads use store-scoped aggregates only and never take customer
// balance locks; retention windows stay capped; secrets in free-text
// columns never reach the snapshot; 16.3A sanitizer markers stay stable.

var ehSeedSequence atomic.Int64

func ehSeedRevisionRow(t *testing.T, store *DurableStore, queue, status, reason string, attempts int, created time.Time) {
	t.Helper()
	workKind := string(billing.EconomicWorkKindProviderRating)
	if queue == "customer" {
		workKind = string(billing.EconomicWorkKindCustomerRating)
	}
	workID := fmt.Sprintf("work-%s-%d", queue, ehSeedSequence.Add(1))
	_, err := store.db.NewRaw(`INSERT INTO billing_economic_revision_work_state(store_id, work_id, work_version, queue, head_key, work_kind, status, attempt_count, retry_reason, created_at_unix, updated_at_unix) VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		store.StoreID(), workID, 1, queue, "head-eh", workKind, status, attempts, reason, created.UnixNano(), created.UnixNano()).Exec(context.Background())
	require.NoError(t, err)
}

func TestEconomicHealthSnapshotQueues(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ehSeedRevisionRow(t, store, "customer", "pending", "", 0, now.Add(-90*time.Second))
	ehSeedRevisionRow(t, store, "customer", "pending", "", 0, now.Add(-80*time.Second))
	ehSeedRevisionRow(t, store, "customer", "failed", "transient_failure", 3, now.Add(-10*time.Second))
	ehSeedRevisionRow(t, store, "customer", "failed", "hostile reason bc_1 SECRET_X", 2, now.Add(-5*time.Second))
	ehSeedRevisionRow(t, store, "provider", "processing", "", 1, now.Add(-30*time.Second))

	account := edTestAccount(t, store, "eh-acct", "USD")
	callID := edTestCallID(t)
	leg := edTestLeg(t, "b-eh")
	leg.CallID = callID
	leg.ALegID = "a-eh"
	edSetupCall(t, store, account.ID, callID, "a-eh", leg)

	got, err := store.EconomicHealthSnapshot(ctx)
	require.NoError(t, err)
	byQueue := map[string]billing.EconomicQueueHealth{}
	for _, queue := range got.Queues {
		byQueue[queue.Queue] = queue
	}
	customer, ok := byQueue["customer_rating"]
	require.True(t, ok, "customer-rating bucket must be present, got %+v", got.Queues)
	require.Equal(t, 2, customer.Pending)
	require.Equal(t, 2, customer.Failed)
	require.GreaterOrEqual(t, customer.OldestAgeSec, 89.0)
	require.Equal(t, 3, customer.MaxAttempts)
	reasons := map[string]int{}
	for _, retry := range customer.Retries {
		reasons[retry.Reason] = retry.Count
	}
	require.Equal(t, 1, reasons["transient_failure"])
	require.Equal(t, 1, reasons["other"])
	require.NotContains(t, keysOf(reasons), "hostile reason bc_1 SECRET_X")
	provider, ok := byQueue["provider_rating"]
	require.True(t, ok)
	require.Equal(t, 1, provider.Processing)
	require.GreaterOrEqual(t, provider.OldestAgeSec, 29.0)

	payload, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "SECRET_X")
	require.NotContains(t, string(payload), "bc_1")
}

// Finding 4 regression: the durable reader emits completed/processed status
// groups, but backlog age must derive only from outstanding rows. A
// completed-only bucket reports zero backlog age while retaining its completed
// count, and an old completed row must not dominate a newer outstanding one.
func TestEconomicHealthSnapshotBacklogAgeExcludesTerminalHistory(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ehSeedRevisionRow(t, store, "customer", "completed", "", 0, now.Add(-10*time.Minute))
	ehSeedRevisionRow(t, store, "customer", "completed", "", 0, now.Add(-9*time.Minute))
	ehSeedRevisionRow(t, store, "customer", "pending", "", 0, now.Add(-30*time.Second))
	ehSeedRevisionRow(t, store, "provider", "completed", "", 0, now.Add(-2*time.Hour))

	got, err := store.EconomicHealthSnapshot(ctx)
	require.NoError(t, err)
	byQueue := map[string]billing.EconomicQueueHealth{}
	for _, queue := range got.Queues {
		byQueue[queue.Queue] = queue
	}
	customer, ok := byQueue["customer_rating"]
	require.True(t, ok, "customer_rating bucket missing: %+v", got.Queues)
	require.Equal(t, 2, customer.Completed)
	require.Equal(t, 1, customer.Pending)
	require.GreaterOrEqual(t, customer.OldestAgeSec, 28.0)
	require.Less(t, customer.OldestAgeSec, 60.0,
		"backlog age must come from the newer pending row, not terminal history")
	provider, ok := byQueue["provider_rating"]
	require.True(t, ok, "provider_rating bucket missing: %+v", got.Queues)
	require.Equal(t, 1, provider.Completed)
	require.Zero(t, provider.OldestAgeSec, "completed-only queue must report zero backlog age")
}

func keysOf(values map[string]int) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

func TestEconomicHealthSnapshotLockFreedom(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("economic_health.go")
	require.NoError(t, err)
	for _, forbidden := range []string{"lockAccount", "getAccountTx", "billing_accounts", "withAccountTx"} {
		require.NotContains(t, string(source), forbidden,
			"health snapshot must not take customer balance locks or touch account tables")
	}
}

func TestEconomicHealthSnapshotStatementsAndDiscrepancies(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, store.StoreID(), "stmt-eh", 1)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))
	require.NoError(t, store.AppendReconciliationRetention(ctx, orTestRetention(t, store.StoreID(), "or-eh", 1)))

	got, err := store.EconomicHealthSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, got.Statements.Matched)
	require.Equal(t, 1, got.Statements.Unmatched)
	require.Equal(t, 1, got.WindowRows)
	require.Equal(t, 1, got.Discrepancies.ByQuantity[0].Count)
	require.NotEmpty(t, got.Discrepancies.Gross)
	for _, total := range got.Discrepancies.Gross {
		require.Equal(t, "USD", total.Currency)
	}
}

func TestEconomicHealthCertifiesNoSecrets(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	secret := "sk-ant-cert-secret-value"

	// Secret-bearing evidence cannot persist: the 16.3A boundary rejects it.
	badCall := edTestCallID(t)
	badSubject := edTestBLegSubject(store.StoreID(), "tenant-cert", "cert-acct", "a-cert", badCall.String(), "b-cert")
	bad := edTestObservation(t, "obs-cert", metering.OriginProvider, "stream-cert", 1, badSubject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil)
	bad.Evidence = []metering.SafeEvidenceField{{
		Path: "$.usage.input_tokens", Lexeme: secret, Present: true,
		Acquisition: metering.AcquisitionProviderResponse,
	}}
	require.Error(t, bad.Validate())
	require.Error(t, store.AppendCallLegUsage(ctx, edTestLeg(t, "b-cert", bad)))

	// Sanitizer markers stay stable across the durable round-trip.
	now := time.Unix(1_700_050_000, 0).UTC()
	markedCall := edTestCallID(t)
	markedSubject := edTestBLegSubject(store.StoreID(), "tenant-cert", "cert-acct", "a-cert", markedCall.String(), "b-marked")
	marked := edTestObservation(t, "obs-marked", metering.OriginProvider, "stream-marked", 1, markedSubject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil)
	marked.ObservedAt, marked.ReceivedAt = now, now
	marked.Evidence = []metering.SafeEvidenceField{{
		Path: "$.usage.input_tokens", Lexeme: "10", Present: true,
		Acquisition: metering.AcquisitionProviderResponse, Sanitizer: "lip-normalizer/v1",
	}}
	before := marked.Fingerprint()
	edSetupCall(t, store, "cert-acct", markedCall, "a-cert", edTestLeg(t, "b-marked", marked))
	stored, err := store.ListCallLegUsage(ctx, markedCall)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Len(t, stored[0].Observations, 1)
	require.Equal(t, "lip-normalizer/v1", stored[0].Observations[0].Evidence[0].Sanitizer)
	require.Equal(t, before, stored[0].Observations[0].Fingerprint(),
		"sanitizer marker and hash must be stable across persistence")

	got, err := store.EconomicHealthSnapshot(ctx)
	require.NoError(t, err)
	payload, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(payload), secret)
}
