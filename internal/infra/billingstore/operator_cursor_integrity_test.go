package billingstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/uptrace/bun"
)

// Finding 6 RED contract: operator cursors must be authenticated by a
// server-owned secret, not merely canonical base64 JSON. These tests exercise
// a re-encoded modified position, the legacy unauthenticated op1 format and
// durable reopen/shared-database behavior independently of the codec's
// internal representation.

func openOperatorCursorBun(t *testing.T, dsn string) (*bun.DB, *sql.DB) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	require.NoError(t, err)
	return bunDB, sqlDB
}

func operatorCursorDSN(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), name)))
}

func seedOperatorCursorDiscrepancies(t *testing.T, store *DurableStore, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for i, id := range ids {
		result := orTestRetention(t, store.StoreID(), id, 1)
		result.CreatedAt = time.Unix(1_700_040_000+int64(i), 0).UTC()
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	}
}

// forgeOperatorCursorReencode decodes the cursor payload, applies mutate and
// re-encodes it canonically in the store's advertised format, preserving only
// the original trailing authentication bytes. An unauthenticated format is
// fully re-encoded; an authenticated format carries a stale tag and must be
// rejected because the attacker cannot recompute it without the server key.
func forgeOperatorCursorReencode(t *testing.T, raw string, mutate func(*operatorCursor)) string {
	t.Helper()
	require.True(t, strings.HasPrefix(raw, operatorCursorPrefix), "cursor %q must use the advertised prefix", raw)
	rest := strings.TrimPrefix(raw, operatorCursorPrefix)
	segments := strings.SplitN(rest, ".", 2)
	payload, err := base64.RawURLEncoding.DecodeString(segments[0])
	require.NoError(t, err)
	var cursor operatorCursor
	require.NoError(t, json.Unmarshal(payload, &cursor))
	mutate(&cursor)
	cursor.Version = operatorCursorVersion
	reencoded, err := json.Marshal(cursor)
	require.NoError(t, err)
	forged := operatorCursorPrefix + base64.RawURLEncoding.EncodeToString(reencoded)
	if len(segments) == 2 {
		forged += "." + segments[1]
	}
	return forged
}

func TestOperatorCursorRejectsReencodedDiscrepancyPosition(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	seedOperatorCursorDiscrepancies(t, store, "or-forge-1", "or-forge-2", "or-forge-3")

	scope := economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}
	first, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{Scope: scope, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	forged := forgeOperatorCursorReencode(t, first.NextCursor, func(cursor *operatorCursor) {
		cursor.RowID += 500
	})
	_, err = store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{Scope: scope, Limit: 1, Cursor: forged})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"a recomputed position must not be accepted from cursor contents alone")
}

func TestOperatorCursorRejectsReencodedStatementLinePosition(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	normalized := statementImportNormalized(t, store.StoreID(), "stmt-forge", 1,
		statementImportLineSpec{ID: "line-forge-a", Revision: 1, Amount: "1.00"},
		statementImportLineSpec{ID: "line-forge-b", Revision: 1, Amount: "2.00"},
	)
	require.NoError(t, store.AppendStatementRevision(ctx, normalized))

	base := economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Limit: 1,
	}
	first, err := store.QueryStatementLines(ctx, base)
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	forged := forgeOperatorCursorReencode(t, first.NextCursor, func(cursor *operatorCursor) {
		cursor.LineKey = "line-forge-b"
	})
	_, err = store.QueryStatementLines(ctx, economics.StatementLineQuery{
		Scope:              economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-1"},
		ProviderAccountKey: "provider-account", Limit: 1, Cursor: forged,
	})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"a recomputed line position must not be accepted from cursor contents alone")
}

// TestOperatorCursorRejectsLegacyUnauthenticatedFormat pins the backward
// policy: legacy op1 cursors carried no authenticator and must fail closed.
func TestOperatorCursorRejectsLegacyUnauthenticatedFormat(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	seedOperatorCursorDiscrepancies(t, store, "or-legacy-1", "or-legacy-2")

	filter := struct {
		Store, Tenant, Account, Kind, ID string
	}{store.StoreID(), "tenant-or", "", "", ""}
	legacyPayload, err := json.Marshal(operatorCursor{
		Version: 1, Kind: "discrepancies", StoreID: store.StoreID(), Filter: operatorFilterHash(filter),
		CreatedAt: 1_700_040_000, RecordID: "or-legacy-1", RecordVersion: 1, RowID: 1,
	})
	require.NoError(t, err)
	legacy := "op1." + base64.RawURLEncoding.EncodeToString(legacyPayload)

	_, err = store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 2, Cursor: legacy,
	})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid, "legacy unauthenticated cursors must fail closed")
}

func TestOperatorCursorSurvivesDurableReopen(t *testing.T) {
	ctx := context.Background()
	dsn := operatorCursorDSN(t, "operator-cursor-reopen.db")

	firstBun, firstSQL := openOperatorCursorBun(t, dsn)
	first, err := NewDurableStore(ctx, firstBun, Config{StoreID: "cursor-reopen"})
	require.NoError(t, err)
	seedOperatorCursorDiscrepancies(t, first, "or-reopen-1", "or-reopen-2", "or-reopen-3")
	firstPage, err := first.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: "cursor-reopen", TenantID: "tenant-or"}, Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, firstPage.Items, 2)
	require.NotEmpty(t, firstPage.NextCursor)
	keyBefore := append([]byte(nil), first.cursorKey...)
	require.NoError(t, first.Close())
	_ = firstSQL.Close()

	reopenedBun, reopenedSQL := openOperatorCursorBun(t, dsn)
	reopened, err := NewDurableStore(ctx, reopenedBun, Config{StoreID: "cursor-reopen"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close(); _ = reopenedSQL.Close() })
	require.Equal(t, keyBefore, reopened.cursorKey)

	secondPage, err := reopened.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: "cursor-reopen", TenantID: "tenant-or"}, Limit: 2, Cursor: firstPage.NextCursor,
	})
	require.NoError(t, err, "outstanding cursors must survive a durable reopen with the intended configuration")
	require.Len(t, secondPage.Items, 1)
	require.Empty(t, secondPage.NextCursor)
}

// TestOperatorCursorSharedAcrossInstancesOverSameDatabase proves the explicit
// multi-instance contract: instances over the same durable store accept each
// other's outstanding cursors because the server-owned key is store-scoped
// durable state, while a different store identity is rejected.
func TestOperatorCursorSharedAcrossInstancesOverSameDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := operatorCursorDSN(t, "operator-cursor-shared.db")

	aBun, aSQL := openOperatorCursorBun(t, dsn)
	a, err := NewDurableStore(ctx, aBun, Config{StoreID: "cursor-shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close(); _ = aSQL.Close() })
	seedOperatorCursorDiscrepancies(t, a, "or-shared-1", "or-shared-2")
	page, err := a.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: "cursor-shared", TenantID: "tenant-or"}, Limit: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)

	bBun, bSQL := openOperatorCursorBun(t, dsn)
	b, err := NewDurableStore(ctx, bBun, Config{StoreID: "cursor-shared"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close(); _ = bSQL.Close() })
	continued, err := b.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: "cursor-shared", TenantID: "tenant-or"}, Limit: 1, Cursor: page.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, continued.Items, 1)

	otherBun, otherSQL := openOperatorCursorBun(t, dsn)
	other, err := NewDurableStore(ctx, otherBun, Config{StoreID: "cursor-other"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close(); _ = otherSQL.Close() })
	_, err = other.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: "cursor-other", TenantID: "tenant-or"}, Limit: 1, Cursor: page.NextCursor,
	})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid, "a different store identity must not accept the cursor")
}

// TestOperatorCursorKeyIsPerStoreServerSecret proves the authenticator key is
// high-entropy server-owned material, independent per store identity, and
// reloaded byte-for-byte on reopen rather than derived from public cursor or
// store data.
func TestOperatorCursorKeyIsPerStoreServerSecret(t *testing.T) {
	t.Parallel()
	first := newSQLiteTestStore(t)
	second := newSQLiteTestStore(t)
	require.Len(t, first.cursorKey, operatorCursorKeyBytes)
	require.Len(t, second.cursorKey, operatorCursorKeyBytes)
	require.NotEqual(t, first.cursorKey, second.cursorKey, "each store identity must own independent key material")
	require.NotEqual(t, first.cursorKey, []byte(first.storeID), "key must not be the public store id")
}

func TestOperatorCursorCodecAuthenticatesCompletePayload(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef0123456789abcdef")
	cursor := operatorCursor{Kind: "statement-lines", StoreID: "store-a", Filter: "filter-a", LineKey: "line-a"}
	raw := encodeOperatorCursor(cursor, key)
	require.True(t, strings.HasPrefix(raw, operatorCursorPrefix))

	decoded, err := decodeOperatorCursor(raw, "statement-lines", "store-a", "filter-a", key)
	require.NoError(t, err)
	require.Equal(t, "line-a", decoded.LineKey)

	wrongKey := []byte("fedcba9876543210fedcba9876543210")
	_, err = decodeOperatorCursor(raw, "statement-lines", "store-a", "filter-a", wrongKey)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid, "a different server key must not verify the cursor")

	_, err = decodeOperatorCursor(raw, "adjustments", "store-a", "filter-a", key)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid, "query kind is bound into the authenticator")

	_, err = decodeOperatorCursor(raw, "statement-lines", "store-b", "filter-a", key)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid, "scope is bound into the authenticator")

	segments := strings.SplitN(strings.TrimPrefix(raw, operatorCursorPrefix), ".", 2)
	require.Len(t, segments, 2)
	payload, err := base64.RawURLEncoding.DecodeString(segments[0])
	require.NoError(t, err)
	payload[len(payload)-1] ^= 0x01
	bitFlipped := operatorCursorPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + segments[1]
	_, err = decodeOperatorCursor(bitFlipped, "statement-lines", "store-a", "filter-a", key)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid, "a modified payload with a stale tag must be rejected")

	_, err = decodeOperatorCursor(raw[:len(raw)-3], "statement-lines", "store-a", "filter-a", key)
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid, "a truncated cursor must be rejected")
}

// TestOperatorCursorErrorDoesNotLeakSecrets proves the stable classified error
// never echoes the cursor payload, authenticator or server key.
func TestOperatorCursorErrorDoesNotLeakSecrets(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	seedOperatorCursorDiscrepancies(t, store, "or-leak-1", "or-leak-2")
	scope := economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}
	first, err := store.QueryDiscrepancies(context.Background(), economics.DiscrepancyQuery{Scope: scope, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	forged := forgeOperatorCursorReencode(t, first.NextCursor, func(cursor *operatorCursor) { cursor.RowID += 7 })
	_, err = store.QueryDiscrepancies(context.Background(), economics.DiscrepancyQuery{Scope: scope, Limit: 1, Cursor: forged})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)

	message := err.Error()
	segments := strings.SplitN(strings.TrimPrefix(first.NextCursor, operatorCursorPrefix), ".", 2)
	require.NotContains(t, message, first.NextCursor)
	require.NotContains(t, message, segments[0])
	if len(segments) == 2 {
		require.NotContains(t, message, segments[1])
	}
	require.NotContains(t, message, hex.EncodeToString(store.cursorKey))
	require.NotContains(t, message, base64.RawURLEncoding.EncodeToString(store.cursorKey))
	require.NotContains(t, message, string(store.cursorKey))
}
