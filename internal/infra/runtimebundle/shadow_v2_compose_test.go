package runtimebundle_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var shadowComposeTestSequence atomic.Int64

type shadowStubRater struct{}

func (shadowStubRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		refs = append([]metering.ObservationRef(nil), input.ObservationRefs...)
	}
	return economics.Valuation{
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs,
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), input.AllocationCoverageRefs...),
		Payer:                  input.Payer,
		Completeness:           economics.CompletenessPartial,
	}, nil
}

func newShadowBillingStore(t *testing.T) *billingstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:shadow-compose-172-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", shadowComposeTestSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
		_ = sqlDB.Close()
	})
	return store
}

func newShadowRater(_ *testing.T) billing.PostUsageRater {
	return shadowStubRater{}
}

func newShadowJournal(t *testing.T) *journalstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:shadow-compose-journal-172-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", shadowComposeTestSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	journal, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = journal.Close()
	})
	return journal
}

func TestPhase172ComposeShadowV2RequiresExplicitPortsAndKeepsV1Writer(t *testing.T) {
	t.Parallel()
	store := newShadowBillingStore(t)
	journal := newShadowJournal(t)
	rater := newShadowRater(t)

	valid := runtimebundle.ShadowV2CaptureInput{
		StoreID:             "test",
		MaxObservations:     16,
		EvidenceSink:        journal,
		WorkAppender:        store,
		ResultStore:         store,
		ReconciliationStore: store,
		Rater:               rater,
		V1Settlement:        store,
	}
	handle, err := runtimebundle.ComposeShadowV2Capture(valid)
	require.NoError(t, err)
	_ = handle

	// V1 remains the sole monetary writer: the composed production path still
	// exposes settlement, while the shadow handle exposes no monetary ledger.
	// Composed financial isolation itself is proven by
	// TestPhase172Cluster4ComposedNoPostLifecycle with nonzero seeded money,
	// unit, payable, and adjustment state.
	var _ billing.CallSettlementStore = store

	missing := valid
	missing.Rater = nil
	if _, err := runtimebundle.ComposeShadowV2Capture(missing); err == nil {
		t.Fatal("nil rater accepted")
	}
	missing = valid
	missing.V1Settlement = nil
	if _, err := runtimebundle.ComposeShadowV2Capture(missing); err == nil {
		t.Fatal("missing V1 settlement accepted; shadow must coexist with the authoritative V1 writer")
	}
	missing = valid
	missing.StoreID = ""
	if _, err := runtimebundle.ComposeShadowV2Capture(missing); err == nil {
		t.Fatal("empty store scope accepted")
	}
}
