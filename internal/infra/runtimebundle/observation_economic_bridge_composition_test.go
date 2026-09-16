package runtimebundle_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

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
	observation := compositionObservation("stock", 1)
	observation.Subject.StoreID = billingStore.StoreID()
	observation.Correlation.StoreID = billingStore.StoreID()
	// The stock relay polls independently from the receive path. Its first
	// successful pass must create both isolated queue markers without a caller
	// invoking AppendEconomicRevisionWork.
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{observation}))
	provider := eventuallyEconomicWork(t, billingStore, billing.EconomicQueueProvider, 1)
	customer := eventuallyEconomicWork(t, billingStore, billing.EconomicQueueCustomer, 1)
	require.Len(t, provider, 1)
	require.Len(t, customer, 1)

	// Exact terminal/same-input replay does not manufacture a second marker.
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{observation}))
	provider = eventuallyEconomicWork(t, billingStore, billing.EconomicQueueProvider, 1)
	require.Len(t, provider, 1)

	correction := compositionObservation("stock-correction", 2)
	correction.Subject.StoreID = billingStore.StoreID()
	correction.Correlation.StoreID = billingStore.StoreID()
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{correction}))
	provider = eventuallyEconomicWork(t, billingStore, billing.EconomicQueueProvider, 2)
	require.Len(t, provider, 2, "late correction must enqueue a new provider identity")
}

func newCompositionBillingStore(t *testing.T, storeID string) *billingstore.DurableStore {
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

func eventuallyEconomicWork(t *testing.T, store *billingstore.DurableStore, queue billing.EconomicQueue, want int) []billing.EconomicRevisionWork {
	t.Helper()
	var got []billing.EconomicRevisionWork
	var lastErr error
	require.Eventually(t, func() bool {
		got, lastErr = store.ListPendingEconomicRevisionWork(context.Background(), queue, 10)
		return lastErr == nil && len(got) == want
	}, 2*time.Second, 20*time.Millisecond)
	require.NoError(t, lastErr)
	return got
}
