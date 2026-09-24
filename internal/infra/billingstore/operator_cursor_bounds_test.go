package billingstore

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Finding 2 RED contract: the shared operator-cursor decoder must reject a raw
// cursor whose transport bytes exceed MaxOperatorCursorBytes as its very first
// step, before splitting on the delimiter, Base64-decoding either segment,
// computing the HMAC or parsing the inner payload. A validly signed oversized
// cursor is the decisive probe: pre-remediation it decodes and is accepted, so
// rejecting it proves the raw-byte gate, not a MAC mismatch, is what fails
// closed. The exact bound and a normal continuation must stay accepted.

func operatorCursorTestKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

// buildSignedOperatorCursorExactly grows a validly signed statement-lines
// cursor until its raw transport length is exactly target bytes. Every
// reachable op2 length is controllable through the free-form line key.
func buildSignedOperatorCursorExactly(t *testing.T, key []byte, target int) string {
	t.Helper()
	for pad := 0; pad <= 2*target; pad++ {
		raw := encodeOperatorCursor(operatorCursor{
			Kind: "statement-lines", StoreID: "store-a", Filter: "filter-a",
			LineKey: strings.Repeat("L", pad),
		}, key)
		if len(raw) == target {
			return raw
		}
		if len(raw) > target {
			break
		}
	}
	t.Fatalf("unable to build a signed operator cursor of exactly %d bytes", target)
	return ""
}

// buildSignedOperatorCursorAtLeast grows a validly signed cursor until it first
// reaches or exceeds minLen; because op2 lengths skip some byte counts, the
// caller must assert the resulting length itself.
func buildSignedOperatorCursorAtLeast(t *testing.T, key []byte, minLen int) string {
	t.Helper()
	for pad := 0; pad <= 2*minLen; pad++ {
		raw := encodeOperatorCursor(operatorCursor{
			Kind: "statement-lines", StoreID: "store-a", Filter: "filter-a",
			LineKey: strings.Repeat("L", pad),
		}, key)
		if len(raw) >= minLen {
			return raw
		}
	}
	t.Fatalf("unable to build a signed operator cursor of at least %d bytes", minLen)
	return ""
}

func TestDecodeOperatorCursorRejectsOversizedSignedPayload(t *testing.T) {
	t.Parallel()
	key := operatorCursorTestKey()
	oversized := encodeOperatorCursor(operatorCursor{
		Kind: "statement-lines", StoreID: "store-a", Filter: "filter-a",
		LineKey: strings.Repeat("L", economics.MaxOperatorCursorBytes),
	}, key)
	require.Greater(t, len(oversized), economics.MaxOperatorCursorBytes)

	decoded, err := decodeOperatorCursor(oversized, "statement-lines", "store-a", "filter-a", key)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"an oversized raw cursor must be rejected even though its MAC is valid")
	require.Empty(t, decoded.LineKey)
	require.NotContains(t, err.Error(), oversized,
		"the classified error must not echo caller cursor content")
}

func TestDecodeOperatorCursorExactBoundControl(t *testing.T) {
	t.Parallel()
	key := operatorCursorTestKey()

	atBound := buildSignedOperatorCursorExactly(t, key, economics.MaxOperatorCursorBytes)
	require.Len(t, atBound, economics.MaxOperatorCursorBytes)
	decoded, err := decodeOperatorCursor(atBound, "statement-lines", "store-a", "filter-a", key)
	require.NoError(t, err, "a validly signed cursor at the exact bound must remain accepted")
	require.Equal(t, "statement-lines", decoded.Kind)

	overBound := buildSignedOperatorCursorAtLeast(t, key, economics.MaxOperatorCursorBytes+1)
	require.Greater(t, len(overBound), economics.MaxOperatorCursorBytes)
	_, err = decodeOperatorCursor(overBound, "statement-lines", "store-a", "filter-a", key)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"a validly signed cursor one byte over the bound must fail closed")
}

func TestDecodeOperatorCursorRejectsOversizedTag(t *testing.T) {
	t.Parallel()
	key := operatorCursorTestKey()
	valid := encodeOperatorCursor(operatorCursor{
		Kind: "statement-lines", StoreID: "store-a", Filter: "filter-a", LineKey: "line-a",
	}, key)
	segments := strings.SplitN(strings.TrimPrefix(valid, operatorCursorPrefix), ".", 2)
	require.Len(t, segments, 2)
	tag, err := base64.RawURLEncoding.DecodeString(segments[1])
	require.NoError(t, err)

	hugeTag := append(bytes.Repeat([]byte{0}, economics.MaxOperatorCursorBytes), tag...)
	raw := operatorCursorPrefix + segments[0] + "." + base64.RawURLEncoding.EncodeToString(hugeTag)
	require.Greater(t, len(raw), economics.MaxOperatorCursorBytes)

	_, err = decodeOperatorCursor(raw, "statement-lines", "store-a", "filter-a", key)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)
	require.NotContains(t, err.Error(), raw,
		"the classified error must not echo caller cursor content")
}

// TestDecodeAllowanceCursorRejectsOversizedSignedCursor exercises the public
// allowance authority: a caller with a validly signed but oversized token must
// be rejected at the shared decoder entry rather than allocated and hashed
// proportionally to its size.
func TestDecodeAllowanceCursorRejectsOversizedSignedCursor(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	raw := store.EncodeAllowanceCursor(store.StoreID(), "filter-a",
		strings.Repeat("S", economics.MaxOperatorCursorBytes))
	require.Greater(t, len(raw), economics.MaxOperatorCursorBytes)

	_, err := store.DecodeAllowanceCursor(raw, store.StoreID(), "filter-a")
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"an oversized authenticated allowance cursor must fail closed before decode/hash")
	require.NotContains(t, err.Error(), raw,
		"the classified error must not echo caller cursor content")
}
