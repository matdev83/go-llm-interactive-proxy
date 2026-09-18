//go:build integration

package billingstore

import (
	"context"
	"testing"
	"time"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// refinement82PostgresSharedHarness runs the shared revision scenario on
// isolated PostgreSQL schemas using only existing repository infrastructure.
// Every generation opens FRESH handles: the isolated-schema helpers create
// (and finally drop) the schemas, while each generation opens its own Bun
// pools bound to those schemas, pings them, and constructs stores on top.
// Session Close releases exactly its own generation's stores and pools, so
// closing one generation can never poison the next. No new DB
// infrastructure, no watered-down assertions — the SAME
// runRefinement82SharedRevisionScenario runs here.
func refinement82PostgresSharedHarness(t *testing.T, dsn string) refinement82SharedHarness {
	t.Helper()
	type schemas struct{ billing, journal string }
	byStoreID := make(map[string]schemas)
	var opens int64
	openGeneration := func(t *testing.T, storeID string, s schemas) refinement82SharedSession {
		t.Helper()
		billingDSN, _ := postgresDSNWithSearchPath(dsn, s.billing)
		journalDSN, _ := postgresDSNWithSearchPath(dsn, s.journal)
		billingBun := testkit.OpenPostgresBunForTest(t, billingDSN, 8)
		journalBun := testkit.OpenPostgresBunForTest(t, journalDSN, 4)
		opens += 2
		closeHandles := func() {
			_ = billingBun.Close()
			_ = journalBun.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := billingBun.PingContext(ctx); err != nil {
			closeHandles()
			t.Fatalf("ping fresh billing handle: %v", err)
		}
		if err := journalBun.PingContext(ctx); err != nil {
			closeHandles()
			t.Fatalf("ping fresh journal handle: %v", err)
		}
		billingStore, err := NewDurableStore(ctx, billingBun, Config{StoreID: storeID})
		if err != nil {
			closeHandles()
			t.Fatalf("open billing store: %v", err)
		}
		journalStore, err := journalstore.NewDurableStore(ctx, journalBun, journalstore.DurableConfig{StoreID: storeID})
		if err != nil {
			_ = billingStore.Close()
			closeHandles()
			t.Fatalf("open journal store: %v", err)
		}
		return refinement82SharedSession{
			Billing: billingStore,
			Journal: journalStore,
			Close: func() {
				_ = billingStore.Close()
				_ = journalStore.Close()
				closeHandles()
			},
		}
	}
	return refinement82SharedHarness{
		Open: func(t *testing.T, storeID string) refinement82SharedSession {
			t.Helper()
			billingBun, billingSchema := openIsolatedPostgresBun(t, dsn, 8)
			journalBun, journalSchema := openIsolatedPostgresBun(t, dsn, 4)
			// The helper cleanups own schema drops at test end; handles below
			// are per-generation, so release the helper pools immediately to
			// keep exactly one ownership rule: a session closes what it opened.
			_ = billingBun.Close()
			_ = journalBun.Close()
			byStoreID[storeID] = schemas{billing: billingSchema, journal: journalSchema}
			return openGeneration(t, storeID, byStoreID[storeID])
		},
		Reopen: func(t *testing.T, storeID string, prev refinement82SharedSession) refinement82SharedSession {
			t.Helper()
			prev.Close()
			s, ok := byStoreID[storeID]
			if !ok {
				t.Fatalf("unknown shared store identity %q", storeID)
			}
			return openGeneration(t, storeID, s)
		},
		Opens: func() int64 { return opens },
	}
}

// TestRefinement82SharedRevisionScenarioPostgresDirect executes the SAME
// shared revision/restart/concurrency scenario as the SQLite wrapper against
// PostgreSQL-direct. Without a DSN it compiles and SKIPs explicitly via the
// repository convention.
func TestRefinement82SharedRevisionScenarioPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	runRefinement82SharedRevisionScenario(t, ctx, refinement82PostgresSharedHarness(t, dsn))
}

// TestRefinement82PostgresReopenWithoutOutbox proves the restart factory
// itself on PostgreSQL using only paths already proven on PG (leg writes,
// plain observation appends, reads, pings): after a full close, reopen
// returns fresh usable handles on the same isolated schemas and all
// pre-restart rows read back stable. Outbox-dependent stages are excluded
// here — they are covered by the shared scenario, currently blocked on PG
// by the journalstore JSONB payload-comparison defect documented in the
// execution evidence.
func TestRefinement82PostgresReopenWithoutOutbox(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	harness := refinement82PostgresSharedHarness(t, dsn)
	storeID := testkit.UniquePostgresStoreID("refinement82-pg-reopen")
	sess := harness.Open(t, storeID)
	if got := harness.Opens(); got != 2 {
		t.Fatalf("opens after Open = %d, want 2", got)
	}
	callID, err := corebilling.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := refinement52ClosedLeg(storeID, callID)
	if err := sess.Billing.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("pre-restart leg append: %v", err)
	}
	o1 := refinement52ProviderObservation(storeID, callID, 1, "pg-reopen-o1", metering.AcquisitionProviderResponse, metering.OriginProvider, metering.AuthorityObservedClaim)
	if err := sess.Journal.AppendObservations(ctx, []metering.Observation{o1}); err != nil {
		t.Fatalf("pre-restart observation append: %v", err)
	}
	sess = harness.Reopen(t, storeID, sess)
	defer sess.Close()
	if got := harness.Opens(); got != 4 {
		t.Fatalf("opens after Reopen = %d, want 4 (fresh handles per generation)", got)
	}
	if err := sess.Billing.CheckReadiness(ctx); err != nil {
		t.Fatalf("reopened billing handle not usable: %v", err)
	}
	if err := sess.Journal.CheckReadiness(ctx); err != nil {
		t.Fatalf("reopened journal handle not usable: %v", err)
	}
	legKey, err := corebilling.CallLegUsageKey(callID, leg.BLegID)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sess.Billing.GetCallLegUsage(ctx, legKey)
	if err != nil {
		t.Fatalf("post-restart leg read: %v", err)
	}
	if sealed.Fingerprint == "" {
		t.Fatal("post-restart leg has no fingerprint")
	}
	if _, err := sess.Journal.GetObservation(ctx, o1.ID, o1.Revision); err != nil {
		t.Fatalf("post-restart observation read: %v", err)
	}
	o2 := refinement52ProviderObservation(storeID, callID, 2, "pg-reopen-o2", metering.AcquisitionProviderFinalizer, metering.OriginProvider, metering.AuthorityObservedClaim)
	if err := sess.Journal.AppendObservations(ctx, []metering.Observation{o2}); err != nil {
		t.Fatalf("post-restart observation write: %v", err)
	}
	page, err := sess.Journal.ListObservations(ctx, journalstore.ObservationQuery{StoreID: storeID, SubjectKind: metering.SubjectBLeg, SubjectID: "b-leg", Limit: 100})
	if err != nil {
		t.Fatalf("post-restart observation list: %v", err)
	}
	if len(page.Observations) != 2 {
		t.Fatalf("post-restart observations = %d, want o1 + o2", len(page.Observations))
	}
}
