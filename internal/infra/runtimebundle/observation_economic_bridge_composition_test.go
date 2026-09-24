package runtimebundle_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// compositionRelayWaitBudget is the hard upper bound on the relay waits in
// TestStockCompositionObservationEconomicBridgeQueuesWithoutManualSeeding. The
// stall-sensitive durable fingerprint is the real fail-fast detector; this
// bound only guarantees the test cannot run until the global go test timeout if
// a relay makes nonproductive retry/read-error churn look like progress.
const compositionRelayWaitBudget = 60 * time.Second

func TestStockCompositionObservationEconomicBridgeQueuesWithoutManualSeeding(t *testing.T) {
	ctx := context.Background()
	billingStore := newCompositionBillingStore(t, "billing-host-loop")
	catalog := billingcompose.NewSnapshotCatalog()
	pricing := billing.PricingSnapshot{
		Ref: billing.VersionRef{ID: "pricing", Version: "v1"}, Currency: "USD",
		InputPerMillionNano: 100, OutputPerMillionNano: 200, InputRatePresent: true, OutputRatePresent: true,
	}
	policy := billing.ChargePolicy{Ref: billing.VersionRef{ID: "policy", Version: "v1"}, PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true}
	require.NoError(t, catalog.PutPricing(pricing))
	require.NoError(t, catalog.PutPolicy(policy))
	require.NoError(t, catalog.SetDefaults(pricing.Ref, policy.Ref))
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store: billingStore, TerminalUsageSink: billingStore, Catalog: catalog, Currency: "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) { return 1024, true, nil }, PostTurnBatchSize: 1,
	})
	require.NoError(t, err)
	baseConfig := writeBillingHostLoopConfig(t)
	base, err := os.ReadFile(baseConfig)
	require.NoError(t, err)
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	configText := strings.Replace(string(base), "continuity:\n  in_memory: true\n  store: memory\n", "continuity:\n  in_memory: true\n  store: memory\nmetering:\n  enabled: true\n  journal:\n    store: sqlite\n    sqlite_path: \""+filepath.ToSlash(journalPath)+"\"\n", 1)
	configPath := filepath.Join(t.TempDir(), "bridge-host.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configText), 0o600))
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath: configPath, Mandatory: lipsdk.StandardDistributionRequirements(), LogWriter: io.Discard,
		HandlerComposer: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NewServeMux(), nil
		}, Production: prod,
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)
	executor := hostActiveExecutor(t, host)
	atomicSink, ok := executor.MeteringObservationSink.(metering.AtomicObservationSink)
	require.True(t, ok, "stock production composition must expose atomic observation sink")
	journal, ok := executor.MeteringRecorder.(*journalstore.DurableStore)
	require.True(t, ok, "stock production composition must expose the durable observation journal")
	// Bound the relay waits so nonproductive retry/read-error churn can never
	// run until the global go test timeout; the durable-progress fingerprint
	// remains the fast stall detector.
	relayCtx, cancelRelay := context.WithTimeout(ctx, compositionRelayWaitBudget)
	defer cancelRelay()
	progress := func(probeCtx context.Context) (string, error) {
		return observationRelayProgress(probeCtx, billingStore, journal)
	}
	// Durable work identities are immutable markers; reading them (rather than
	// the mutable pending view) proves the relay enqueued each identity without
	// racing the background revision worker, which may move a marker out of the
	// pending page while it retries.
	readProvider := func(readCtx context.Context) ([]string, error) {
		return listEconomicWorkIDs(readCtx, billingStore, billing.EconomicQueueProvider)
	}
	readCustomer := func(readCtx context.Context) ([]string, error) {
		return listEconomicWorkIDs(readCtx, billingStore, billing.EconomicQueueCustomer)
	}
	observation := compositionObservation("stock", 1)
	observation.Subject.StoreID = billingStore.StoreID()
	observation.Correlation.StoreID = billingStore.StoreID()
	// The stock relay polls independently from the receive path. Its first
	// successful pass must create both isolated queue markers without a caller
	// invoking AppendEconomicRevisionWork. The wait synchronizes on the relay's
	// durable outbox completion rather than a fixed wall clock, so a relay that
	// advances slower than a fixed budget under load is not mistaken for a stall.
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{observation}))
	provider := waitEconomicWorkQueued(t, relayCtx, billing.EconomicQueueProvider, progress, readProvider)
	customer := waitEconomicWorkQueued(t, relayCtx, billing.EconomicQueueCustomer, progress, readCustomer)
	require.Len(t, provider, 1)
	require.Len(t, customer, 1)

	// Exact terminal/same-input replay does not manufacture a second marker.
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{observation}))
	provider = waitEconomicWorkQueued(t, relayCtx, billing.EconomicQueueProvider, progress, readProvider)
	require.Len(t, provider, 1)

	correction := compositionObservation("stock-correction", 2)
	correction.Subject.StoreID = billingStore.StoreID()
	correction.Correlation.StoreID = billingStore.StoreID()
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{correction}))
	provider = waitEconomicWorkQueued(t, relayCtx, billing.EconomicQueueProvider, progress, readProvider)
	require.Len(t, provider, 2, "late correction must enqueue a new provider identity")
}

func newCompositionBillingStore(t *testing.T, storeID string) *billingstore.DurableStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "billing.sqlite")) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
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

func compositionObservation(id string, revision uint64) metering.Observation {
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

// waitEconomicWorkQueued blocks until the stock relay reports durable outbox
// completion (the progress probe reports drained) and then reads the immutable
// work identities for queue. It replaces the previous fixed 2s wall-clock
// Eventually: the probe observes the relay's durable progress, so a slow but
// advancing relay under load is tolerated while a frozen or retry-churning one
// still fails fast at the stall bound. Callers assert the expected count.
//
//nolint:revive // test helper keeps t first per Go testing convention
func waitEconomicWorkQueued(t *testing.T, parent context.Context, queue billing.EconomicQueue, probe func(context.Context) (string, error), read func(context.Context) ([]string, error)) []string {
	t.Helper()
	return waitEconomicWorkQueuedWithin(t, parent, outboxDrainStallWindow, outboxDrainTick, queue, probe, read)
}

// waitEconomicWorkQueuedWithin is the configurable-stall-window seam used by the
// composition wait (production constants) and by the progress-sensitivity
// regression, which uses a short window so total progress can exceed it without
// materially increasing suite runtime.
//
//nolint:revive // test helper keeps t first per Go testing convention
func waitEconomicWorkQueuedWithin(t *testing.T, parent context.Context, stallWindow, tick time.Duration, queue billing.EconomicQueue, probe func(context.Context) (string, error), read func(context.Context) ([]string, error)) []string {
	t.Helper()
	if err := pollObservationOutboxDrained(parent, stallWindow, tick, probe); err != nil {
		t.Fatalf("timed out waiting for the observation outbox relay to complete before reading %s work: %v", queue, err)
	}
	got, err := read(parent)
	require.NoError(t, err)
	return got
}

// TestStockCompositionEconomicWorkWaitObservesRelayProgressNotWallClock is the
// deterministic regression for the load-sensitive composition flake. A fixed
// total deadline (the removed 2s Eventually, or a fixed stall-window length)
// cannot distinguish a slow but steadily advancing relay from a stalled one.
// The controlled relay here advances durable completion on every observation,
// so total progress in wall time exceeds the stall window while each individual
// advance lands well inside it: a fixed stall-window timeout expires first, the
// progress-sensitive wait succeeds.
func TestStockCompositionEconomicWorkWaitObservesRelayProgressNotWallClock(t *testing.T) {
	t.Parallel()
	const (
		stallWindow = 200 * time.Millisecond
		pollTick    = 5 * time.Millisecond
		advances    = 100 // advances*pollTick = 500ms total progress > stallWindow
	)
	if time.Duration(advances)*pollTick <= stallWindow {
		t.Fatalf("total productive progress %s must exceed the stall window %s",
			time.Duration(advances)*pollTick, stallWindow)
	}
	if pollTick >= stallWindow {
		t.Fatalf("each productive advance %s must land inside the stall window %s", pollTick, stallWindow)
	}
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The controlled relay advances the durable fingerprint on each poll, so
	// scheduler delay can never be mistaken for a stall; completion is bounded
	// only by the ticker and the total progress still exceeds the stall window.
	var step atomic.Int32
	probe := func(context.Context) (string, error) {
		if step.Add(1) > advances {
			return "", nil
		}
		return fmt.Sprintf("durable-step-%d", step.Load()), nil
	}
	read := func(context.Context) ([]string, error) {
		if step.Load() > advances {
			return []string{"work-1"}, nil
		}
		return nil, nil
	}

	start := time.Now()
	if got := waitEconomicWorkQueuedWithin(t, parent, stallWindow, pollTick, billing.EconomicQueueProvider, probe, read); len(got) != 1 {
		t.Fatalf("progress-sensitive wait returned %d rows, want 1", len(got))
	}
	if elapsed := time.Since(start); elapsed <= stallWindow {
		t.Fatalf("wait completed in %s, want total progress beyond the %s stall window", elapsed, stallWindow)
	}
}
