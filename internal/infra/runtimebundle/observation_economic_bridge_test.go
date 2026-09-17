package runtimebundle

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestObservationEconomicRelayEnqueuesProviderWorkAndAcksOutbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	meteringStore := newBridgeMeteringStore(t, "bridge-store")
	billingStore := newBridgeBillingStore(t, "bridge-store")
	observation := bridgeRuntimeObservation("relay", 1)
	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{observation}))

	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)
	relay := newObservationEconomicRelay(meteringStore, billingStore, builder)
	require.NoError(t, relay.ProcessOnce(ctx))

	pending, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	work, err := billingStore.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, err)
	require.Len(t, work, 1)
	require.Equal(t, observation.Subject.BLegID, work[0].Subject.BLegID)
}

func TestObservationEconomicRelayRecoversExpiredLeaseAfterStaleOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newDeterministicBridgeClock(time.Unix(1_800_000_000, 0).UTC())
	meteringStore := newBridgeMeteringStoreWithClock(t, "bridge-lease-recovery", clock.Now)
	billingStore := newBridgeBillingStore(t, "bridge-lease-recovery")
	observation := bridgeRuntimeObservation("lease-recovery", 1)
	observation.Subject.StoreID = "bridge-lease-recovery"
	observation.Correlation.StoreID = "bridge-lease-recovery"
	require.NoError(t, observation.Validate())
	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{observation}))
	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)
	blocking := newBlockingEconomicAppender(billingStore)
	relayA := newObservationEconomicRelay(meteringStore, blocking, builder)
	relayA.lease = 10 * time.Second
	aDone := make(chan error, 1)
	go func() { aDone <- relayA.ProcessOnce(ctx) }()
	<-blocking.committed

	claimed, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, "processing", claimed[0].Status)
	require.Equal(t, relayA.owner, claimed[0].LeaseOwner)
	require.Equal(t, 1, claimed[0].AttemptCount)

	// A has committed the real economic work but is stale before its ack. The
	// deterministic clock makes the durable lease expiry observable without
	// sleeping or polling.
	clock.Advance(relayA.lease + time.Nanosecond)
	relayB := newObservationEconomicRelay(meteringStore, billingStore, builder)
	relayB.lease = relayA.lease
	require.NoError(t, relayB.ProcessOnce(ctx))
	close(blocking.release)
	require.ErrorIs(t, <-aDone, journalstore.ErrObservationOutboxClaimLost)

	var status string
	var attempts int
	require.NoError(t, meteringStore.DB().NewRaw(`
SELECT status, attempt_count
FROM metering_observation_economic_outbox
WHERE store_id = ? AND observation_id = ? AND observation_revision = ?`,
		"bridge-lease-recovery", observation.ID, int64(observation.Revision)).Scan(ctx, &status, &attempts))
	require.Equal(t, "delivered", status)
	require.Equal(t, 2, attempts, "A claim plus one B lease recovery")
	pending, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	work, err := billingStore.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, err)
	require.Len(t, work, 1, "stale A and recovered B must leave one logical work item")
	require.NoError(t, relayB.ProcessOnce(ctx))
	workAgain, err := billingStore.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, err)
	require.Len(t, workAgain, 1)
	_, err = meteringStore.GetObservation(ctx, observation.ID, observation.Revision)
	require.NoError(t, err, "durable observation must survive stale-owner recovery")
}

func TestObservationEconomicRelayRetainsUnlinkedStatementOutbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	meteringStore := newBridgeMeteringStore(t, "bridge-store")
	billingStore := newBridgeBillingStore(t, "bridge-store")
	statement := bridgeRuntimeObservation("unlinked-statement", 1)
	statement.Origin = metering.OriginStatement
	statement.Acquisition = metering.AcquisitionStatementImporter
	statement.Authority = metering.AuthorityVerifiedStatement
	statement.Subject = metering.SubjectRef{
		Kind: metering.SubjectStatementLine, StoreID: "bridge-store", ProviderAccountKey: "provider",
		StatementID: "statement-1", StatementLineID: "line-1",
	}
	statement.Correlation = metering.CorrelationV2{StoreID: "bridge-store", ProviderAccountKey: "provider"}
	require.NoError(t, statement.Validate())
	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink := sink.(metering.AtomicObservationSink)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{statement}))

	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)
	relay := newObservationEconomicRelay(meteringStore, billingStore, builder)
	require.Error(t, relay.ProcessOnce(ctx), "unlinked statement must not be acknowledged")
	pending, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	work, err := billingStore.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, err)
	require.Empty(t, work)
}

func TestObservationEconomicRelayFailureAndLeaseExpiryRemainRetryable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	meteringStore := newBridgeMeteringStore(t, "bridge-store")
	billingStore := newBridgeBillingStore(t, "bridge-store")
	observation := bridgeRuntimeObservation("retry", 1)
	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink := sink.(metering.AtomicObservationSink)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{observation}))
	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)
	relay := newObservationEconomicRelay(meteringStore, failingEconomicAppender{}, builder)
	relay.lease = time.Nanosecond
	require.Error(t, relay.ProcessOnce(ctx))
	pending, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "pending", pending[0].Status)

	// Leave a claimed row unacknowledged to model a process crash between
	// billing append and outbox acknowledgement. The next owner must reclaim it
	// after the finite lease expires, while the billing identity fence absorbs
	// any duplicate append from a crash after the queue commit.
	time.Sleep(120 * time.Millisecond)
	claimed, err := meteringStore.ClaimObservationOutbox(ctx, "crashed-owner", 1, time.Nanosecond)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	time.Sleep(2 * time.Millisecond)
	retryRelay := newObservationEconomicRelay(meteringStore, billingStore, builder)
	require.NoError(t, retryRelay.ProcessOnce(ctx))
	pending, err = meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

type failingEconomicAppender struct {
}

func (a failingEconomicAppender) AppendEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork) error {
	return errors.New("injected economic work append failure")
}

type deterministicBridgeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newDeterministicBridgeClock(now time.Time) *deterministicBridgeClock {
	return &deterministicBridgeClock{now: now.UTC()}
}

func (c *deterministicBridgeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *deterministicBridgeClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

type blockingEconomicAppender struct {
	delegate  billing.EconomicRevisionWorkAppender
	committed chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newBlockingEconomicAppender(delegate billing.EconomicRevisionWorkAppender) *blockingEconomicAppender {
	return &blockingEconomicAppender{
		delegate:  delegate,
		committed: make(chan struct{}),
		release:   make(chan struct{}),
	}
}

func (a *blockingEconomicAppender) AppendEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork) error {
	if err := a.delegate.AppendEconomicRevisionWork(ctx, work); err != nil {
		return err
	}
	a.once.Do(func() { close(a.committed) })
	<-a.release
	return nil
}

func newBridgeMeteringStore(t *testing.T, storeID string) *journalstore.DurableStore {
	return newBridgeMeteringStoreWithClock(t, storeID, time.Now)
}

func newBridgeMeteringStoreWithClock(t *testing.T, storeID string, now func() time.Time) *journalstore.DurableStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "metering.sqlite")) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID, Now: now})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newBridgeBillingStore(t *testing.T, storeID string) *billingstore.DurableStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "billing.sqlite")) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func bridgeRuntimeObservation(id string, revision uint64) metering.Observation {
	now := time.Unix(1_700_001_000+int64(revision), 0).UTC()
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:tokens", Unit: metering.UnitToken, SchemaID: "bridge:v1"}
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	amount := metering.Decimal{Coefficient: "3", Scale: 0}
	return metering.Observation{
		Version: 2, ID: "bridge-" + id, SourceEventKey: "bridge-source-" + id, Revision: revision,
		StreamID: "bridge-stream", Sequence: revision, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "bridge-store", TenantID: "tenant", AccountID: "account", ALegID: "a", BillingCallID: "call", BLegID: "b", AttemptID: "attempt", ProviderAccountKey: "provider"},
		Correlation: metering.CorrelationV2{StoreID: "bridge-store", TenantID: "tenant", CallID: "call", BillingCallID: "call", ALegID: "a", BLegID: "b", AttemptID: "attempt", ProviderAccountKey: "provider"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "bridge:v1",
		Measures: []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
		Charges:  []metering.ReportedCharge{{ChargeItemID: "charge-" + id, Component: &key, Amount: &amount, Currency: "USD", Kind: metering.ChargeKindComponent}},
	}
}
