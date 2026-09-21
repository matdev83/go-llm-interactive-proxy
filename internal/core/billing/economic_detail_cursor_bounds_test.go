package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Finding 2 RED contract: the economic-detail normalizer is a public reader
// entry point too, so it must enforce the same raw MaxOperatorCursorBytes bound
// as the operator query normalizers. Direct SDK/adapter callers that bypass the
// operator reader normalizers must not be able to hand an unbounded cursor to
// the durable decoder.
func TestEconomicDetailQueryNormalizeBoundsIncomingCursor(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, callID := detailTestScope()
	base := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, BillingCallID: callID, ALegID: aLegID}

	oversized := map[string]string{
		"payload segment": "op2." + strings.Repeat("A", economics.MaxOperatorCursorBytes) + "." + strings.Repeat("B", 43),
		"tag segment":     "op2." + strings.Repeat("A", 8) + "." + strings.Repeat("B", economics.MaxOperatorCursorBytes),
	}
	for name, cursor := range oversized {
		name, cursor := name, cursor
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Greater(t, len(cursor), economics.MaxOperatorCursorBytes)
			query := base
			query.Cursor = cursor
			_, err := query.Normalize()
			require.ErrorIs(t, err, ErrEconomicDetailInvalid,
				"an oversized raw cursor must fail closed at Normalize")
			require.NotContains(t, err.Error(), cursor,
				"the classified error must not echo caller cursor content")
		})
	}

	t.Run("exact bound supported", func(t *testing.T) {
		t.Parallel()
		query := base
		query.Cursor = strings.Repeat("A", economics.MaxOperatorCursorBytes)
		got, err := query.Normalize()
		require.NoError(t, err, "a cursor at the exact raw byte bound must remain accepted")
		require.Equal(t, query.Cursor, got.Cursor)
	})

	t.Run("one over bound rejected", func(t *testing.T) {
		t.Parallel()
		query := base
		query.Cursor = strings.Repeat("A", economics.MaxOperatorCursorBytes+1)
		_, err := query.Normalize()
		require.ErrorIs(t, err, ErrEconomicDetailInvalid)
	})

	t.Run("supported continuation unchanged", func(t *testing.T) {
		t.Parallel()
		query := base
		query.Cursor = "op2.eyJ2IjoyfQ.AA"
		got, err := query.Normalize()
		require.NoError(t, err, "a normal authenticated continuation must remain accepted")
		require.Equal(t, query.Cursor, got.Cursor)
	})
}
