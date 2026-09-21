package billingstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Finding 5B1 RED contract: leg-record SQL and embedded-observation
// materialization are bounded before growth. A scope above the finite leg bound
// or above the full-scope observation budget fails closed with
// billing.ErrEconomicDetailBoundExceeded instead of decoding/holding unbounded
// rows, and the leg query carries a database-side LIMIT.

// edLegQueryRecorder is a deterministic bun hook recording every executed
// query's formatted SQL so the leg query can be proven to carry a
// database-side bound.
type edLegQueryRecorder struct {
	queries []string
}

func (h *edLegQueryRecorder) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *edLegQueryRecorder) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	h.queries = append(h.queries, event.Query)
}

func edTestLegObservations(t *testing.T, storeID, accountID, aLegID, callID, bLegID, prefix string, count int) []metering.Observation {
	t.Helper()
	subject := edTestBLegSubject(storeID, "tenant-ed", accountID, aLegID, callID, bLegID)
	measure := edTestMeasure(t, metering.ComponentInputToken, "10")
	out := make([]metering.Observation, 0, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%s-%04d", prefix, i)
		out = append(out, edTestObservation(t, id, metering.OriginLocal, "stream-"+prefix, uint64(i+1), subject,
			[]metering.Measure{measure}, nil))
	}
	return out
}

// TestEconomicDetailLegQueryCarriesDatabaseSideLimit proves the leg SQL itself
// is bounded database-side; an IN-list bound is not a row bound.
func TestEconomicDetailLegQueryCarriesDatabaseSideLimit(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-leg-limit", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-leg-limit", edTestLeg(t, "b-ed-leg-limit"))

	recorder := &edLegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-leg-limit",
	})
	require.NoError(t, err)

	bounded := false
	for _, query := range recorder.queries {
		if strings.Contains(query, "usage_leg_records") && strings.Contains(query, "LIMIT") {
			bounded = true
		}
	}
	require.True(t, bounded, "leg SQL must carry a database-side LIMIT; recorded queries: %v", recorder.queries)
}

// TestEconomicDetailLegRecordsBoundFailsClosed proves a scope with more than
// the finite leg bound fails closed at load time instead of being materialized.
func TestEconomicDetailLegRecordsBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-leg-over", "USD")
	callID := edTestCallID(t)
	legs := make([]billing.CallLegUsageRecord, 0, economicDetailMaxLegRecords+1)
	for i := 0; i < economicDetailMaxLegRecords+1; i++ {
		leg := edTestLeg(t, fmt.Sprintf("b-ed-leg-over-%04d", i))
		leg.AttemptSeq = i + 1
		legs = append(legs, leg)
	}
	edSetupCall(t, store, account.ID, callID, "a-ed-leg-over", legs...)

	got, err := store.detailLegsByCalls(ctx, []string{callID.String()})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, got, "an over-bound leg set must never be retained")
}

// TestEconomicDetailLegRecordsBoundaryPasses proves exactly the finite bound is
// still accepted; the bound fails over, not at, the maximum.
func TestEconomicDetailLegRecordsBoundaryPasses(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-leg-max", "USD")
	callID := edTestCallID(t)
	legs := make([]billing.CallLegUsageRecord, 0, economicDetailMaxLegRecords)
	for i := 0; i < economicDetailMaxLegRecords; i++ {
		leg := edTestLeg(t, fmt.Sprintf("b-ed-leg-max-%04d", i))
		leg.AttemptSeq = i + 1
		legs = append(legs, leg)
	}
	edSetupCall(t, store, account.ID, callID, "a-ed-leg-max", legs...)

	got, err := store.detailLegsByCalls(ctx, []string{callID.String()})
	require.NoError(t, err)
	require.Len(t, got, economicDetailMaxLegRecords)
}

// TestEconomicDetailLegBoundIsGlobalAcrossChunks proves chunking enforces one
// global bound, not the bound per chunk: each chunk stays within the bound but
// their cumulative total must still fail closed.
func TestEconomicDetailLegBoundIsGlobalAcrossChunks(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-leg-chunk", "USD")
	first := edTestCallID(t)
	second := edTestCallID(t)
	perCall := economicDetailMaxLegRecords - 28 // two chunks stay individually under the bound
	for callIndex, callID := range []billing.BillingCallID{first, second} {
		legs := make([]billing.CallLegUsageRecord, 0, perCall)
		for i := 0; i < perCall; i++ {
			leg := edTestLeg(t, fmt.Sprintf("b-ed-leg-chunk-%d-%04d", callIndex, i))
			leg.AttemptSeq = i + 1
			legs = append(legs, leg)
		}
		edSetupCall(t, store, account.ID, callID, fmt.Sprintf("a-ed-leg-chunk-%d", callIndex), legs...)
	}

	// Pad the ID list so the two real calls land in different SQL chunks.
	padded := make([]string, 0, economicDetailChunkSize+2)
	padded = append(padded, first.String())
	for i := 0; i < economicDetailChunkSize; i++ {
		padded = append(padded, fmt.Sprintf("call-pad-%04d", i))
	}
	padded = append(padded, second.String())

	got, err := store.detailLegsByCalls(ctx, padded)
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, got)
}

// TestEconomicDetailEmbeddedObservationBoundFailsClosed proves a small number
// of leg rows can still carry more embedded observations than the full-scope
// budget, and that case fails closed before the over-bound set is retained.
func TestEconomicDetailEmbeddedObservationBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-obs-over", "USD")
	callID := edTestCallID(t)
	full := edTestLeg(t, "b-ed-obs-over",
		edTestLegObservations(t, store.StoreID(), account.ID, "a-ed-obs-over", callID.String(), "b-ed-obs-over", "obs-ed-obs-over", billing.MaxEconomicDetailObservations)...)
	extra := edTestLeg(t, "b-ed-obs-extra",
		edTestLegObservations(t, store.StoreID(), account.ID, "a-ed-obs-over", callID.String(), "b-ed-obs-extra", "obs-ed-obs-extra", 1)...)
	extra.AttemptSeq = 2
	edSetupCall(t, store, account.ID, callID, "a-ed-obs-over", full, extra)

	got, err := store.detailLegsByCalls(ctx, []string{callID.String()})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, got, "a scope over the observation budget must not retain leg records")
}

// TestEconomicDetailCallLegObservationsBoundFailsClosed proves the observation
// projection itself refuses to grow past the full-scope budget.
func TestEconomicDetailCallLegObservationsBoundFailsClosed(t *testing.T) {
	t.Parallel()
	legs := []billing.CallLegUsageRecord{
		{Observations: make([]metering.Observation, billing.MaxEconomicDetailObservations)},
		{Observations: make([]metering.Observation, 1)},
	}
	got, err := callLegObservations(legs)
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Nil(t, got)
}

// TestEconomicDetailCorruptLegPayloadFailsClosedSecretSafe proves a corrupt
// durable leg payload fails closed without echoing raw bytes.
func TestEconomicDetailCorruptLegPayloadFailsClosedSecretSafe(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	secret := "sk-live-SUPER-SECRET-LEG-VALUE"
	callID := edTestCallID(t)
	now := time.Now().UTC()
	_, err := store.db.NewRaw(
		`INSERT INTO usage_leg_records( usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"corrupt-leg-key", "corrupt-leg-fingerprint", callID.String(), "a-ed-corrupt", "b-ed-corrupt",
		"backend-ed", "provider-ed", "model-ed", now, now, "winner", "yes",
		`{"secret":"`+secret, now).Exec(ctx)
	require.NoError(t, err)

	_, err = store.detailLegsByCalls(ctx, []string{callID.String()})
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret, "corrupt leg errors must never echo raw payload bytes")
}
