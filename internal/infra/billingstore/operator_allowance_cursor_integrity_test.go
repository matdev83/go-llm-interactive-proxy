package billingstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Finding 6 RED contract: the allowance operator endpoint must not forward the
// unauthenticated account-window journal cursor (aw1, base64 JSON with a public
// filter hash and no MAC) to callers. A caller who can read the public cursor
// encoding could otherwise re-encode an altered ordering position and have it
// accepted. The operator boundary must authenticate the continuation with the
// durable op2 HMAC before trusting or parsing the inner source position, bind
// kind=allowance, full authorized scope, filters/order and the opaque source
// position, and return op2 cursors only.

const (
	testAllowanceJournalStoreID = "allowance-cursor-journal"
	testAllowanceBillingStoreID = "allowance-cursor-billing"
	testAllowanceTenantID       = "tenant-or"
	testAllowanceProvider       = "provider-or"
	testAllowanceOrder          = "account-window-history-v1"
	testAllowanceKind           = "allowance"
)

type allowanceCursorFixture struct {
	ctx     context.Context
	billing *DurableStore
	journal *journalstore.DurableStore
	storeID string
	scope   economics.OperatorScope
}

func openAllowanceCursorBilling(t *testing.T, dsn, storeID string) (*DurableStore, *sql.DB) {
	t.Helper()
	bunDB, sqlDB := openOperatorCursorBun(t, dsn)
	seedTestSchemaIfEmpty(t, bunDB)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	require.NoError(t, err)
	return store, sqlDB
}

func openAllowanceCursorJournal(t *testing.T, dsn, storeID string) (*journalstore.DurableStore, *sql.DB) {
	t.Helper()
	bunDB, sqlDB := openOperatorCursorBun(t, dsn)
	journal, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID})
	require.NoError(t, err)
	return journal, sqlDB
}

func seedAllowanceGauges(t *testing.T, journal *journalstore.DurableStore, storeID string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, journal.AppendAccountWindowObservation(context.Background(), orTestGauge(t, storeID, id, "12.5")))
	}
}

// allowanceReaderUnderTest wires the journal source to the billing store's
// durable op2 key authority.
func allowanceReaderUnderTest(t *testing.T, billing *DurableStore, journal *journalstore.DurableStore) AllowanceReader {
	t.Helper()
	return AllowanceReader{Source: journal, Authority: billing}
}

func newAllowanceCursorFixture(t *testing.T) *allowanceCursorFixture {
	t.Helper()
	ctx := context.Background()
	billingStore, billingSQL := openAllowanceCursorBilling(t, operatorCursorDSN(t, "allowance-billing.db"), testAllowanceBillingStoreID)
	journal, journalSQL := openAllowanceCursorJournal(t, operatorCursorDSN(t, "allowance-journal.db"), testAllowanceJournalStoreID)
	t.Cleanup(func() {
		_ = billingStore.Close()
		_ = billingSQL.Close()
		_ = journal.Close()
		_ = journalSQL.Close()
	})
	seedAllowanceGauges(t, journal, testAllowanceJournalStoreID, "obs-aw-1", "obs-aw-2", "obs-aw-3")
	return &allowanceCursorFixture{
		ctx:     ctx,
		billing: billingStore,
		journal: journal,
		storeID: testAllowanceJournalStoreID,
		scope:   economics.OperatorScope{StoreID: testAllowanceJournalStoreID, TenantID: testAllowanceTenantID},
	}
}

func allowanceQuery(scope economics.OperatorScope, limit int, cursor string) economics.AllowanceQuery {
	return economics.AllowanceQuery{
		Scope:              scope,
		ProviderAccountKey: testAllowanceProvider,
		Limit:              limit,
		Cursor:             cursor,
	}
}

func testAllowanceFilter(scope economics.OperatorScope, provider, pool, window string) string {
	return operatorFilterHash(struct {
		Store, Tenant, Provider, Pool, Window string
	}{scope.StoreID, scope.TenantID, provider, pool, window})
}

// allowanceSourceCursor returns the opaque inner journal cursor carried by an
// operator cursor. Before the fix the operator endpoint exposes the raw aw1
// journal cursor directly, so this returns it unchanged; afterwards it unwraps
// the authenticated op2 envelope.
func allowanceSourceCursor(t *testing.T, raw string) string {
	t.Helper()
	if !strings.HasPrefix(raw, operatorCursorPrefix) {
		return raw
	}
	segments := strings.SplitN(strings.TrimPrefix(raw, operatorCursorPrefix), ".", 2)
	payload, err := base64.RawURLEncoding.DecodeString(segments[0])
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(payload, &envelope))
	source, ok := envelope["source"].(string)
	require.True(t, ok, "operator cursor must carry an opaque source position: %v", envelope)
	return source
}

func forgeAccountWindowCursorPosition(t *testing.T, raw string, mutate func(map[string]any)) string {
	t.Helper()
	require.True(t, strings.HasPrefix(raw, "aw1."), "expected account-window journal cursor, got %q", raw)
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, "aw1."))
	require.NoError(t, err)
	var position map[string]any
	require.NoError(t, json.Unmarshal(payload, &position))
	mutate(position)
	reencoded, err := json.Marshal(position)
	require.NoError(t, err)
	return "aw1." + base64.RawURLEncoding.EncodeToString(reencoded)
}

// forgeAllowanceSourceCursor re-encodes an altered ordering position inside the
// public cursor encoding, preserving whatever authenticator bytes the caller
// can observe. Before the fix this yields a valid aw1 cursor; afterwards it
// yields an op2 envelope whose stale tag cannot be recomputed without the
// server key.
func forgeAllowanceSourceCursor(t *testing.T, raw string, mutate func(map[string]any)) string {
	t.Helper()
	if !strings.HasPrefix(raw, operatorCursorPrefix) {
		return forgeAccountWindowCursorPosition(t, raw, mutate)
	}
	segments := strings.SplitN(strings.TrimPrefix(raw, operatorCursorPrefix), ".", 2)
	payload, err := base64.RawURLEncoding.DecodeString(segments[0])
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(payload, &envelope))
	source, ok := envelope["source"].(string)
	require.True(t, ok, "operator cursor must carry an opaque source position: %v", envelope)
	envelope["source"] = forgeAccountWindowCursorPosition(t, source, mutate)
	repacked, err := json.Marshal(envelope)
	require.NoError(t, err)
	forged := operatorCursorPrefix + base64.RawURLEncoding.EncodeToString(repacked)
	if len(segments) == 2 {
		forged += "." + segments[1]
	}
	return forged
}

func TestQueryAllowancesReturnsAuthenticatedOperatorCursor(t *testing.T) {
	t.Parallel()
	fixture := newAllowanceCursorFixture(t)
	reader := allowanceReaderUnderTest(t, fixture.billing, fixture.journal)

	page, err := reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, ""))
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.True(t, strings.HasPrefix(page.NextCursor, operatorCursorPrefix),
		"allowance continuation must be an authenticated operator cursor, got %q", page.NextCursor)

	decoded, err := decodeOperatorCursor(page.NextCursor, testAllowanceKind, fixture.storeID,
		testAllowanceFilter(fixture.scope, testAllowanceProvider, "", ""), fixture.billing.cursorKey)
	require.NoError(t, err)
	require.Equal(t, testAllowanceKind, decoded.Kind)
	require.Equal(t, testAllowanceOrder, decoded.Order)
	require.NotEmpty(t, allowanceSourceCursor(t, page.NextCursor),
		"the opaque inner journal position must be bound into the operator cursor")
}

func TestQueryAllowancesRejectsReencodedSourcePosition(t *testing.T) {
	t.Parallel()
	fixture := newAllowanceCursorFixture(t)
	reader := allowanceReaderUnderTest(t, fixture.billing, fixture.journal)

	first, err := reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, ""))
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	forged := forgeAllowanceSourceCursor(t, first.NextCursor, func(position map[string]any) {
		rowID, ok := position["row_id"].(float64)
		require.True(t, ok, "journal cursor must carry an ordering position: %v", position)
		position["row_id"] = rowID + 1_000
	})
	_, err = reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, forged))
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"a re-encoded source position must not be accepted from public cursor contents alone")
}

func TestQueryAllowancesRejectsLegacyAccountWindowCursor(t *testing.T) {
	t.Parallel()
	fixture := newAllowanceCursorFixture(t)
	reader := allowanceReaderUnderTest(t, fixture.billing, fixture.journal)

	first, err := reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, ""))
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	legacy := allowanceSourceCursor(t, first.NextCursor)
	_, err = reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, legacy))
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"raw aw1 journal cursors must fail closed at the operator boundary")
}

func TestQueryAllowancesRejectsCrossScopeReplay(t *testing.T) {
	t.Parallel()
	fixture := newAllowanceCursorFixture(t)
	reader := allowanceReaderUnderTest(t, fixture.billing, fixture.journal)

	first, err := reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, ""))
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	cases := map[string]economics.AllowanceQuery{
		"provider account": {Scope: fixture.scope, ProviderAccountKey: "provider-other", Limit: 1, Cursor: first.NextCursor},
		"tenant": {
			Scope:              economics.OperatorScope{StoreID: fixture.storeID, TenantID: "tenant-other"},
			ProviderAccountKey: testAllowanceProvider, Limit: 1, Cursor: first.NextCursor,
		},
		"pool": {
			Scope: fixture.scope, ProviderAccountKey: testAllowanceProvider,
			PoolID: "secondary", Limit: 1, Cursor: first.NextCursor,
		},
		"window": {
			Scope: fixture.scope, ProviderAccountKey: testAllowanceProvider,
			WindowID: "hourly", Limit: 1, Cursor: first.NextCursor,
		},
		"store": {
			Scope:              economics.OperatorScope{StoreID: "different-store", TenantID: testAllowanceTenantID},
			ProviderAccountKey: testAllowanceProvider, Limit: 1, Cursor: first.NextCursor,
		},
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := reader.QueryAllowances(fixture.ctx, query)
			require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
				"a cursor bound to another scope/filter must not be replayed")
		})
	}
}

func TestQueryAllowancesRejectsCrossKindReplay(t *testing.T) {
	t.Parallel()
	fixture := newAllowanceCursorFixture(t)
	filter := testAllowanceFilter(fixture.scope, testAllowanceProvider, "", "")
	foreign := encodeOperatorCursor(operatorCursor{
		Kind: "discrepancies", StoreID: fixture.storeID, Filter: filter,
		CreatedAt: 1_700_000_000, RecordID: "foreign", RecordVersion: 1, RowID: 1,
	}, fixture.billing.cursorKey)

	_, err := allowanceReaderUnderTest(t, fixture.billing, fixture.journal).
		QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, foreign))
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"a cursor minted for another operator kind must not be replayed as an allowance cursor")
}

func TestQueryAllowancesRejectsCrossStoreReplay(t *testing.T) {
	t.Parallel()
	fixture := newAllowanceCursorFixture(t)
	filter := testAllowanceFilter(fixture.scope, testAllowanceProvider, "", "")
	foreign := encodeOperatorCursor(operatorCursor{
		Kind: testAllowanceKind, StoreID: "different-store", Filter: filter,
		Order: testAllowanceOrder,
	}, fixture.billing.cursorKey)

	_, err := allowanceReaderUnderTest(t, fixture.billing, fixture.journal).
		QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, foreign))
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"a cursor bound to another store identity must not be replayed")
}

func TestQueryAllowancesRejectsAccountScopedContinuation(t *testing.T) {
	t.Parallel()
	fixture := newAllowanceCursorFixture(t)
	reader := allowanceReaderUnderTest(t, fixture.billing, fixture.journal)

	first, err := reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, ""))
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	_, err = reader.QueryAllowances(fixture.ctx, economics.AllowanceQuery{
		Scope: economics.OperatorScope{
			StoreID: fixture.storeID, TenantID: testAllowanceTenantID, AccountID: "customer-account",
		},
		ProviderAccountKey: testAllowanceProvider, Limit: 1, Cursor: first.NextCursor,
	})
	require.ErrorIs(t, err, economics.ErrOperatorQueryInvalid,
		"a customer account scope must never authorize provider allowance history")
}

func TestQueryAllowancesCursorSurvivesDurableReopen(t *testing.T) {
	ctx := context.Background()
	billingDSN := operatorCursorDSN(t, "allowance-reopen-billing.db")
	journalDSN := operatorCursorDSN(t, "allowance-reopen-journal.db")
	scope := economics.OperatorScope{StoreID: testAllowanceJournalStoreID, TenantID: testAllowanceTenantID}

	firstBilling, firstBillingSQL := openAllowanceCursorBilling(t, billingDSN, testAllowanceBillingStoreID)
	firstJournal, firstJournalSQL := openAllowanceCursorJournal(t, journalDSN, testAllowanceJournalStoreID)
	seedAllowanceGauges(t, firstJournal, testAllowanceJournalStoreID, "obs-reopen-1", "obs-reopen-2", "obs-reopen-3")
	first, err := allowanceReaderUnderTest(t, firstBilling, firstJournal).
		QueryAllowances(ctx, allowanceQuery(scope, 2, ""))
	require.NoError(t, err)
	require.Len(t, first.Observations, 2)
	require.NotEmpty(t, first.NextCursor)
	keyBefore := append([]byte(nil), firstBilling.cursorKey...)
	require.NoError(t, firstJournal.Close())
	_ = firstJournalSQL.Close()
	require.NoError(t, firstBilling.Close())
	_ = firstBillingSQL.Close()

	reopenedBilling, reopenedBillingSQL := openAllowanceCursorBilling(t, billingDSN, testAllowanceBillingStoreID)
	reopenedJournal, reopenedJournalSQL := openAllowanceCursorJournal(t, journalDSN, testAllowanceJournalStoreID)
	t.Cleanup(func() {
		_ = reopenedBilling.Close()
		_ = reopenedBillingSQL.Close()
		_ = reopenedJournal.Close()
		_ = reopenedJournalSQL.Close()
	})
	require.Equal(t, keyBefore, reopenedBilling.cursorKey, "the durable key must reload byte-for-byte")

	second, err := allowanceReaderUnderTest(t, reopenedBilling, reopenedJournal).
		QueryAllowances(ctx, allowanceQuery(scope, 2, first.NextCursor))
	require.NoError(t, err, "an outstanding allowance cursor must survive a durable reopen")
	require.Len(t, second.Observations, 1)
	require.Empty(t, second.NextCursor)
}

func TestQueryAllowancesCursorSharedAcrossInstancesOverSameDatabase(t *testing.T) {
	ctx := context.Background()
	billingDSN := operatorCursorDSN(t, "allowance-shared-billing.db")
	journalDSN := operatorCursorDSN(t, "allowance-shared-journal.db")
	scope := economics.OperatorScope{StoreID: testAllowanceJournalStoreID, TenantID: testAllowanceTenantID}

	aBilling, aBillingSQL := openAllowanceCursorBilling(t, billingDSN, testAllowanceBillingStoreID)
	aJournal, aJournalSQL := openAllowanceCursorJournal(t, journalDSN, testAllowanceJournalStoreID)
	t.Cleanup(func() { _ = aBilling.Close(); _ = aBillingSQL.Close(); _ = aJournal.Close(); _ = aJournalSQL.Close() })
	seedAllowanceGauges(t, aJournal, testAllowanceJournalStoreID, "obs-shared-1", "obs-shared-2")
	page, err := allowanceReaderUnderTest(t, aBilling, aJournal).QueryAllowances(ctx, allowanceQuery(scope, 1, ""))
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)

	bBilling, bBillingSQL := openAllowanceCursorBilling(t, billingDSN, testAllowanceBillingStoreID)
	bJournal, bJournalSQL := openAllowanceCursorJournal(t, journalDSN, testAllowanceJournalStoreID)
	t.Cleanup(func() { _ = bBilling.Close(); _ = bBillingSQL.Close(); _ = bJournal.Close(); _ = bJournalSQL.Close() })
	require.Equal(t, aBilling.cursorKey, bBilling.cursorKey, "instances over one database share the store key")

	continued, err := allowanceReaderUnderTest(t, bBilling, bJournal).QueryAllowances(ctx, allowanceQuery(scope, 1, page.NextCursor))
	require.NoError(t, err)
	require.Len(t, continued.Observations, 1)

	otherBilling, otherBillingSQL := openAllowanceCursorBilling(t, operatorCursorDSN(t, "allowance-other-billing.db"), "allowance-other-billing")
	otherJournal, otherJournalSQL := openAllowanceCursorJournal(t, operatorCursorDSN(t, "allowance-other-journal.db"), testAllowanceJournalStoreID)
	t.Cleanup(func() {
		_ = otherBilling.Close()
		_ = otherBillingSQL.Close()
		_ = otherJournal.Close()
		_ = otherJournalSQL.Close()
	})
	_, err = allowanceReaderUnderTest(t, otherBilling, otherJournal).QueryAllowances(ctx, allowanceQuery(scope, 1, page.NextCursor))
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid,
		"a different store identity owns a different key and must not accept the cursor")
}

func TestQueryAllowancesCursorErrorDoesNotLeakSecrets(t *testing.T) {
	t.Parallel()
	fixture := newAllowanceCursorFixture(t)
	reader := allowanceReaderUnderTest(t, fixture.billing, fixture.journal)

	first, err := reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, ""))
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	forged := forgeAllowanceSourceCursor(t, first.NextCursor, func(position map[string]any) {
		position["row_id"] = float64(9_999_999)
	})
	_, err = reader.QueryAllowances(fixture.ctx, allowanceQuery(fixture.scope, 1, forged))
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)

	message := err.Error()
	require.NotContains(t, message, first.NextCursor)
	require.NotContains(t, message, base64.RawURLEncoding.EncodeToString(fixture.billing.cursorKey))
	require.NotContains(t, message, string(fixture.billing.cursorKey))
}
