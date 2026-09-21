package billingstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Finding 5A RED contract: the durable economic-detail reader must expose a
// real stable continuation so every in-scope observation is reachable. The
// cursor is authenticated by the Finding 6 per-store secret and binds the query
// scope/filter, the observation ordering definition and the cursor kind.

// edCursorStore seeds one call with a deterministic multi-stream observation
// set and returns the expected canonical observation ID order.
func edCursorStore(t *testing.T) (*DurableStore, billing.EconomicDetailQuery, []string) {
	t.Helper()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-cursor", "USD")
	callID := edTestCallID(t)
	var observations []metering.Observation
	var ids []string
	for _, spec := range []struct {
		stream string
		count  int
	}{{"stream-a", 3}, {"stream-b", 3}, {"stream-c", 1}} {
		for seq := 1; seq <= spec.count; seq++ {
			id := spec.stream + "-" + string(rune('0'+seq))
			subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-cursor", callID.String(), "b-ed-cursor")
			observations = append(observations, edTestObservation(t, id, metering.OriginLocal, spec.stream, uint64(seq), subject,
				[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil))
			ids = append(ids, id)
		}
	}
	edSetupCall(t, store, account.ID, callID, "a-ed-cursor", edTestLeg(t, "b-ed-cursor", observations...))
	query := billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-cursor",
	}
	return store, query, ids
}

func TestQueryEconomicDetailCursorTraversalExactlyOnce(t *testing.T) {
	t.Parallel()
	store, query, expected := edCursorStore(t)
	ctx := context.Background()

	var collected []string
	var cursor string
	pages := 0
	for {
		page := query
		page.Limit = 2
		page.Cursor = cursor
		got, err := store.QueryEconomicDetail(ctx, page)
		require.NoError(t, err)
		require.Equal(t, got.NextCursor != "", got.Truncated, "Truncated must agree with NextCursor")
		for _, observation := range got.Observations {
			collected = append(collected, observation.ID)
		}
		if got.NextCursor == "" {
			break
		}
		cursor = got.NextCursor
		pages++
		require.Less(t, pages, 20, "pagination must terminate instead of repeating the first page")
	}
	require.Equal(t, expected, collected, "every in-scope observation must be traversed exactly once in stable order")
}

func TestQueryEconomicDetailCursorKeepsFullScopeFacts(t *testing.T) {
	t.Parallel()
	store, query, _ := edCursorStore(t)
	ctx := context.Background()

	first := query
	first.Limit = 2
	firstPage, err := store.QueryEconomicDetail(ctx, first)
	require.NoError(t, err)
	require.NotEmpty(t, firstPage.NextCursor)

	second := query
	second.Limit = 2
	second.Cursor = firstPage.NextCursor
	secondPage, err := store.QueryEconomicDetail(ctx, second)
	require.NoError(t, err)

	require.NotEqual(t, firstPage.Observations, secondPage.Observations)
	require.Equal(t, firstPage.Scope, secondPage.Scope, "the authoritative scope snapshot must be identical on every page")
	require.Equal(t, firstPage.Valuations, secondPage.Valuations)
	require.Equal(t, firstPage.Totals, secondPage.Totals)
	require.Equal(t, firstPage.Margin, secondPage.Margin)
}

func TestQueryEconomicDetailCursorRejectsTamperedPosition(t *testing.T) {
	t.Parallel()
	store, query, _ := edCursorStore(t)
	ctx := context.Background()

	first := query
	first.Limit = 2
	page, err := store.QueryEconomicDetail(ctx, first)
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)

	forged := forgeOperatorCursorReencode(t, page.NextCursor, func(cursor *operatorCursor) {
		require.NotNil(t, cursor.Detail)
		cursor.Detail.ObservationID = "obs-forged"
	})
	tampered := query
	tampered.Limit = 2
	tampered.Cursor = forged
	_, err = store.QueryEconomicDetail(ctx, tampered)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"a recomputed position must not be accepted from cursor contents alone")
}

func TestQueryEconomicDetailCursorRejectsScopeFilterKindOrder(t *testing.T) {
	t.Parallel()
	store, query, _ := edCursorStore(t)
	ctx := context.Background()

	first := query
	first.Limit = 2
	page, err := store.QueryEconomicDetail(ctx, first)
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)
	filter := economicDetailCursorFilter(query)

	t.Run("foreign call scope", func(t *testing.T) {
		t.Parallel()
		// A cursor issued for one call must not be replayable against another.
		other := query
		other.BillingCallID = edTestCallID(t).String()
		other.Limit = 2
		other.Cursor = page.NextCursor
		_, err := store.QueryEconomicDetail(ctx, other)
		require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)
	})

	t.Run("kind mismatch", func(t *testing.T) {
		t.Parallel()
		wrongKind := encodeOperatorCursor(operatorCursor{
			Kind: "discrepancies", StoreID: store.StoreID(), Filter: filter,
			Order:  billing.EconomicDetailObservationOrder,
			Detail: &economicDetailCursorPosition{StreamID: "stream-a", ObservationID: "stream-a-1", Revision: 1},
		}, store.cursorKey)
		q := query
		q.Limit = 2
		q.Cursor = wrongKind
		_, err := store.QueryEconomicDetail(ctx, q)
		require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)
	})

	t.Run("order definition mismatch", func(t *testing.T) {
		t.Parallel()
		wrongOrder := encodeOperatorCursor(operatorCursor{
			Kind: "economic-detail", StoreID: store.StoreID(), Filter: filter,
			Order:  "economic-detail-observation-v0",
			Detail: &economicDetailCursorPosition{StreamID: "stream-a", ObservationID: "stream-a-1", Revision: 1},
		}, store.cursorKey)
		q := query
		q.Limit = 2
		q.Cursor = wrongOrder
		_, err := store.QueryEconomicDetail(ctx, q)
		require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)
	})

	t.Run("missing position payload", func(t *testing.T) {
		t.Parallel()
		missing := encodeOperatorCursor(operatorCursor{
			Kind: "economic-detail", StoreID: store.StoreID(), Filter: filter,
			Order: billing.EconomicDetailObservationOrder,
		}, store.cursorKey)
		q := query
		q.Limit = 2
		q.Cursor = missing
		_, err := store.QueryEconomicDetail(ctx, q)
		require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)
	})
}

func TestQueryEconomicDetailCursorSurvivesDurableReopen(t *testing.T) {
	ctx := context.Background()
	dsn := operatorCursorDSN(t, "economic-detail-cursor-reopen.db")

	firstBun, firstSQL := openOperatorCursorBun(t, dsn)
	first, err := NewDurableStore(ctx, firstBun, Config{StoreID: "ed-cursor-reopen"})
	require.NoError(t, err)
	account := edTestAccount(t, first, "ed-cursor-reopen", "USD")
	callID := edTestCallID(t)
	var observations []metering.Observation
	for i := 0; i < 3; i++ {
		subject := edTestBLegSubject(first.StoreID(), "tenant-ed", account.ID, "a-ed-reopen", callID.String(), "b-ed-reopen")
		observations = append(observations, edTestObservation(t, "obs-reopen-"+string(rune('a'+i)), metering.OriginLocal, "stream-reopen", uint64(i+1), subject,
			[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil))
	}
	edSetupCall(t, first, account.ID, callID, "a-ed-reopen", edTestLeg(t, "b-ed-reopen", observations...))

	firstQuery := billing.EconomicDetailQuery{
		StoreID: first.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-reopen", Limit: 1,
	}
	firstPage, err := first.QueryEconomicDetail(ctx, firstQuery)
	require.NoError(t, err)
	require.Len(t, firstPage.Observations, 1)
	require.NotEmpty(t, firstPage.NextCursor)
	require.NoError(t, first.Close())
	_ = firstSQL.Close()

	reopenedBun, reopenedSQL := openOperatorCursorBun(t, dsn)
	reopened, err := NewDurableStore(ctx, reopenedBun, Config{StoreID: "ed-cursor-reopen"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close(); _ = reopenedSQL.Close() })

	secondQuery := firstQuery
	secondQuery.Cursor = firstPage.NextCursor
	secondPage, err := reopened.QueryEconomicDetail(ctx, secondQuery)
	require.NoError(t, err, "outstanding detail cursors must survive a durable reopen")
	require.Len(t, secondPage.Observations, 1)
	require.NotEqual(t, firstPage.Observations[0].ID, secondPage.Observations[0].ID)
}
