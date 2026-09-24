package runtimebundle

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// R5-C2b durable restart proof at the relay seam.
//
// The runtime test proves the crash leaves only the accepted-prefix durable
// journal plus one pending outbox entry per accepted observation. This test
// takes that exact durable state through a real close/reopen, runs the real
// observation economic relay (the same type the process owner starts), and
// asserts what restart can and cannot do with a prefix whose terminal append
// never happened.
//
// The relay is the only restart path that consumes the durable observation
// journal. It must turn the prefix into revision-triggered economic work (the
// intended incremental provider path, requirements 4.2/4.3) and must be
// idempotent on replay (10.6). It must not manufacture a complete-call
// settlement: the complete-call customer path needs a sealed terminal B-leg and
// a frozen call, neither of which the crash produced, so retail/customer
// balance, exposure and pins are untouched.

func r5c2bOpenJournal(t *testing.T, path, storeID string) (*journalstore.DurableStore, *sql.DB) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID})
	require.NoError(t, err)
	return store, sqlDB
}

func r5c2bOpenBilling(t *testing.T, path, storeID string) (*billingstore.DurableStore, *sql.DB) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	require.NoError(t, err)
	return store, sqlDB
}

// r5c2bBridgeObservations drains one validated provider-native media + provider
// money delta observation per revision from the stock provider evidence buffer.
func r5c2bBridgeObservations(storeID string, callID billing.BillingCallID, count int) []metering.Observation {
	buffer := coremetering.NewProviderEvidenceBuffer()
	now := time.Unix(1_700_400_000, 0).UTC()
	buffer.BindEconomicEvidence(coremetering.ObservationIdentity{
		StoreID: storeID, RequestID: "req-r5c2b", CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: "a-r5c2b", BLegID: "b-r5c2b", AttemptID: "attempt-r5c2b", AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	})
	var out []metering.Observation
	for revision := uint64(1); revision <= uint64(count); revision++ {
		media := metering.Decimal{Coefficient: strconv.FormatUint(revision, 10), Scale: 0}
		money := metering.Decimal{Coefficient: strconv.FormatUint(revision*100, 10), Scale: 0}
		buffer.Add(coremetering.ProviderEvidenceDraft{
			SourceEventKey: fmt.Sprintf("provider.r5c2b.%d", revision), StreamID: "provider.r5c2b.v2",
			Semantics: metering.SemanticsDelta,
			Measures: []metering.Measure{{
				Key:   metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "r5c2b.provider.schema"},
				Value: &media, Quality: metering.QualityObserved,
			}},
			Charges: []metering.ReportedCharge{{
				ChargeItemID: fmt.Sprintf("charge:%d", revision), Amount: &money, Currency: "USD", Kind: metering.ChargeKindAggregate,
			}},
		})
		out = append(out, buffer.DrainEconomicObservations()...)
	}
	return out
}

func TestR5C2bDurablePrefixRestartRelaysRevisionWorkWithoutCompleteSettlement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		storeID     = "r5c2b-bridge-store"
		prefixCount = 6
	)

	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)

	dir := t.TempDir()
	journalPath := filepath.Join(dir, "metering.sqlite")
	billingPath := filepath.Join(dir, "billing.sqlite")

	journal, journalSQL := r5c2bOpenJournal(t, journalPath, storeID)
	billingStore, billingSQL := r5c2bOpenBilling(t, billingPath, storeID)

	// Accepted prefix: durable observations and, in one transaction, their
	// economic outbox entries. This is exactly what the runtime checkpoint flush
	// writes before the crash.
	prefix := r5c2bBridgeObservations(storeID, callID, prefixCount)
	require.Len(t, prefix, prefixCount)
	sink := journalstore.NewObservationSinkWithOutbox(journal)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	require.NoError(t, atomicSink.AppendObservations(ctx, prefix))

	// Crash: discard the process-owned runtime and close both durable files.
	require.NoError(t, journal.Close())
	require.NoError(t, journalSQL.Close())
	require.NoError(t, billingStore.Close())
	require.NoError(t, billingSQL.Close())

	// New process composition: reopen the same files.
	journal2, journalSQL2 := r5c2bOpenJournal(t, journalPath, storeID)
	defer func() { _ = journal2.Close(); _ = journalSQL2.Close() }()
	billing2, billingSQL2 := r5c2bOpenBilling(t, billingPath, storeID)
	defer func() { _ = billing2.Close(); _ = billingSQL2.Close() }()

	page, err := journal2.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: "b-r5c2b", Limit: 500,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, prefixCount)
	pending, err := journal2.ListPendingObservationOutbox(ctx, 500)
	require.NoError(t, err)
	require.Len(t, pending, prefixCount, "every accepted prefix observation keeps one durable relay entry")

	// The real relay runs after restart over the durable accepted prefix only.
	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)
	relay := newObservationEconomicRelay(journal2, billing2, builder)
	require.NoError(t, relay.ProcessOnce(ctx))

	pending, err = journal2.ListPendingObservationOutbox(ctx, 500)
	require.NoError(t, err)
	require.Empty(t, pending, "relay must acknowledge the prefix outbox")

	work, err := billing2.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, err)
	require.Len(t, work, 1, "one revision-triggered provider work item per B-leg")
	require.Equal(t, billing.EconomicQueueProvider, work[0].Queue)
	require.Equal(t, uint64(prefixCount), work[0].EvidenceRevision)
	require.Equal(t, "b-r5c2b", work[0].Subject.BLegID)
	require.Len(t, work[0].Input.Observations, prefixCount, "work evidence is exactly the durable prefix")

	// Restart replay is idempotent: re-running the relay neither duplicates the
	// work nor changes it.
	require.NoError(t, relay.ProcessOnce(ctx))
	workAgain, err := billing2.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, err)
	require.Len(t, workAgain, 1)
	require.Equal(t, work[0].Input.InputSetHash, workAgain[0].Input.InputSetHash)

	// No-terminal fence: the crash produced no terminal B-leg and no sealed
	// call, so the complete-call settlement path has nothing to claim. The
	// truncated prefix cannot be selected or posted as a complete call.
	callUsage, err := billing2.ListCallUsage(ctx, "")
	require.NoError(t, err)
	require.Empty(t, callUsage, "no sealed call exists after the crash")
	legs, err := billing2.ListCallLegUsage(ctx, callID)
	require.NoError(t, err)
	require.Empty(t, legs, "no terminal leg exists after the crash")
}
