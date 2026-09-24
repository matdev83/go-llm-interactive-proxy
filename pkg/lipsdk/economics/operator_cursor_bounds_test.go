package economics

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Finding 2 RED contract: every public operator query normalizer must reject a
// raw continuation cursor whose transport bytes exceed MaxOperatorCursorBytes,
// before any operator reader splits, Base64-decodes, MAC-verifies or parses it.
// The cursor transport is ASCII, so the gate counts raw bytes (the allocation
// boundary), never Unicode codepoints. Rejection keeps the stable classified
// non-echoing operator-query error; a supported cursor at the exact bound and a
// normal continuation remain unchanged.

func oversizedCursorPayloadShaped() string {
	return "op2." + strings.Repeat("A", MaxOperatorCursorBytes) + "." + strings.Repeat("B", 43)
}

func oversizedCursorTagShaped() string {
	return "op2." + strings.Repeat("A", 8) + "." + strings.Repeat("B", MaxOperatorCursorBytes)
}

func oversizedCursorUndecodableShaped() string {
	return "op2." + strings.Repeat("!", MaxOperatorCursorBytes+1) + "." + strings.Repeat("!", 8)
}

func TestOperatorQueryNormalizeBoundsIncomingCursor(t *testing.T) {
	t.Parallel()

	baseline := map[string]func(cursor string) error{
		"allowance": func(cursor string) error {
			_, err := AllowanceQuery{
				Scope:              OperatorScope{StoreID: "s", TenantID: "t"},
				ProviderAccountKey: "provider",
				Cursor:             cursor,
			}.Normalize()
			return err
		},
		"discrepancy": func(cursor string) error {
			_, err := DiscrepancyQuery{
				Scope:  OperatorScope{StoreID: "s", TenantID: "t"},
				Cursor: cursor,
			}.Normalize()
			return err
		},
		"statement": func(cursor string) error {
			_, err := StatementLineQuery{
				Scope:              OperatorScope{StoreID: "s", TenantID: "t"},
				ProviderAccountKey: "provider",
				Cursor:             cursor,
			}.Normalize()
			return err
		},
		"adjustment": func(cursor string) error {
			_, err := AdjustmentQuery{
				Scope:  OperatorScope{StoreID: "s", AccountID: "a"},
				Cursor: cursor,
			}.Normalize()
			return err
		},
	}

	oversized := map[string]string{
		"payload segment":  oversizedCursorPayloadShaped(),
		"tag segment":      oversizedCursorTagShaped(),
		"undecodable body": oversizedCursorUndecodableShaped(),
	}

	for kind, normalize := range baseline {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			for name, cursor := range oversized {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					require.Greater(t, len(cursor), MaxOperatorCursorBytes,
						"fixture must exceed the raw transport bound")
					err := normalize(cursor)
					require.ErrorIs(t, err, ErrOperatorQueryInvalid,
						"an oversized raw cursor must fail closed at Normalize")
					require.NotContains(t, err.Error(), cursor,
						"the classified error must not echo caller cursor content")
				})
			}

			t.Run("exact bound supported", func(t *testing.T) {
				t.Parallel()
				require.NoError(t, normalize(strings.Repeat("A", MaxOperatorCursorBytes)),
					"a cursor at the exact raw byte bound must remain accepted")
			})

			t.Run("one over bound rejected", func(t *testing.T) {
				t.Parallel()
				err := normalize(strings.Repeat("A", MaxOperatorCursorBytes+1))
				require.ErrorIs(t, err, ErrOperatorQueryInvalid)
				require.NotContains(t, err.Error(), strings.Repeat("A", 32),
					"the classified error must not echo caller cursor content")
			})

			t.Run("supported continuation unchanged", func(t *testing.T) {
				t.Parallel()
				require.NoError(t, normalize("op2.eyJ2IjoyfQ.AA"),
					"a normal authenticated continuation must remain accepted")
			})
		})
	}
}

// TestOperatorQueryNormalizeCountsBytesNotCodepoints pins the transport
// boundary: a multi-byte cursor is rejected once its raw byte length exceeds
// the bound, even though it carries fewer Unicode codepoints. A codepoint gate
// would accept it, so this fails closed only when raw bytes are counted.
func TestOperatorQueryNormalizeCountsBytesNotCodepoints(t *testing.T) {
	t.Parallel()
	// Euro sign U+20AC is three UTF-8 bytes. Half the bound in codepoints is
	// 1.5x the bound in bytes, so the raw byte count is over while the
	// codepoint count stays under.
	cursor := strings.Repeat("\u20ac", MaxOperatorCursorBytes/2)
	require.Less(t, len([]rune(cursor)), MaxOperatorCursorBytes,
		"the fixture must stay under the bound in codepoints")
	require.Greater(t, len(cursor), MaxOperatorCursorBytes,
		"the fixture must exceed the bound in raw bytes")
	_, err := AllowanceQuery{
		Scope:              OperatorScope{StoreID: "s", TenantID: "t"},
		ProviderAccountKey: "provider",
		Cursor:             cursor,
	}.Normalize()
	require.ErrorIs(t, err, ErrOperatorQueryInvalid)
}
