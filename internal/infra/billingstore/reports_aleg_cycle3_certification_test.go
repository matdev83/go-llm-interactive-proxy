package billingstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/stretchr/testify/require"
)

// Cycle 3 certification for Task 5.3 (rolling A-leg as_of projections),
// C3R1 revision.
//
// These tests close the certification matrix items that Cycles 1-2 left
// implicit: old-token continuation across appended evidence, real terminal
// lifecycle with full-DTO immutability, content-hash retirement/read-only
// proof, and careful AsOf reconciliation. Adversarial authority, fanout
// bounds, and zero/unknown classification are inherited from the Cycle 1
// customer suite (reports_aleg_customer_test.go) and the Cycle 2
// pass-through/provider suites (reports_aleg_passthrough_test.go,
// reports_aleg_provider_test.go, reports_aleg_provider_children_test.go)
// and are mapped in the Cycle 3 evidence file, not duplicated here.
//
// Production lifecycle paths exercised (no invented writers):
//   - Trusted DONE/terminal closure is the runtime-owned
//     billing.TerminalUsageSink handoff (internal/core/billing/append.go);
//     production wires the DurableStore itself as that sink
//     (internal/infra/runtimebundle/billing_compose_test.go:193), reached
//     at request-terminal via billing_admission.go:426 (calls) and
//     billing_leg.go:538 (legs). Tests below invoke the store through the
//     sink interface, then settle through the two billing seams
//     (AdmitExposure + ApplyCallBillingResult).
//   - A-leg retirement/retention/idle has no billing writer: the sole
//     production retirement observer (internal/infra/runtimebundle/
//     compile_generation.go:208-218) calls only
//     PromptCacheMaintenance.EndSession and ConversationView DeleteALeg.
//     Writer-silence between queries therefore IS the retirement/idle
//     transition from billing's perspective.
//
// Snapshot/token semantics proven here (reports_aleg.go): page cursors are
// keyset positions over live data, not frozen snapshots. Scope-wide totals
// (COUNT(*) with no cursor filter, reports_aleg.go:122-125) and both
// streams (keyset WHERE clauses, reports_aleg.go:146/163) are evaluated
// fresh inside each query's own rollback-only read transaction, whose
// observation timestamp is the output-only AsOf
// (reports_aleg.go:79-93). No requirement demands frozen cross-query
// snapshots; Requirement 3.3 demands resumed calls appear, which live
// continuation satisfies without duplicating or omitting original rows.

// cycle3BillingTables is the allowlisted set of durable billing tables the
// rolling report reads (every FROM/JOIN in reports_aleg.go, plus the
// account scope row). Hash capture verifies each table exists before
// hashing, so the allowlist cannot silently skip a table.
func cycle3BillingTables() []string {
	return []string{
		"usage_call_records",
		"usage_leg_records",
		"journal_transactions",
		"journal_entries",
		"billing_operation_snapshots",
		"billing_cost_pass_through_heads",
		"billing_provider_cost_heads",
		"billing_provider_cost_posting_fences",
		"billing_provider_cost_execution_fences",
		"provider_cost_work",
		"billing_economic_revision_work_state",
		"call_exposures",
		"billing_accounts",
	}
}

func cycle3CountRows(t *testing.T, store *DurableStore, table string) int {
	t.Helper()
	allowed := false
	for _, name := range cycle3BillingTables() {
		if name == table {
			allowed = true
			break
		}
	}
	require.True(t, allowed, "table %q not in Cycle 3 allowlist", table)
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM `+table).Scan(context.Background(), &count))
	return count
}

func cycle3SnapshotCounts(t *testing.T, store *DurableStore) map[string]int {
	t.Helper()
	out := make(map[string]int, len(cycle3BillingTables()))
	for _, table := range cycle3BillingTables() {
		out[table] = cycle3CountRows(t, store, table)
	}
	return out
}

// cycle3SnapshotHashes captures canonical SHA-256 contents (not only row
// counts) for every billing table, so a same-count UPDATE is detectable.
// Rows dump in rowid order; encoding/json sorts map keys, making the hash
// deterministic for identical contents.
func cycle3SnapshotHashes(t *testing.T, store *DurableStore) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := make(map[string]string, len(cycle3BillingTables()))
	for _, table := range cycle3BillingTables() {
		var name string
		require.NoError(t, store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(ctx, &name))
		require.Equal(t, table, name, "billing table %q must exist: hash proof cannot skip it", table)
		var rows []map[string]any
		require.NoError(t, store.db.NewRaw(`SELECT * FROM `+table+` ORDER BY rowid`).Scan(ctx, &rows))
		raw, err := json.Marshal(rows)
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		out[table] = hex.EncodeToString(sum[:])
	}
	return out
}

// cycle3BusinessJSON renders a report deterministically for business-DTO
// equality. AsOf is excluded here ONLY because it is an output observation
// timestamp (asOf := time.Now().UTC() per query, reports_aleg.go:93; core
// documents it as "an output observation timestamp for the returned
// snapshot, not a caller-provided historical cutoff"). AsOf behavior gets
// its own assertions (non-zero, monotonic across queries, refreshed on
// every resumed page) instead of being silently dropped.
func cycle3BusinessJSON(t *testing.T, report billing.ALegReport) []byte {
	t.Helper()
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	delete(decoded, "AsOf")
	out, err := json.Marshal(decoded)
	require.NoError(t, err)
	return out
}

// cycle3RequireAsOfObserved asserts the expected observation-clock
// behavior: every snapshot carries a non-zero AsOf, and later queries
// never observe earlier than earlier ones.
func cycle3RequireAsOfObserved(t *testing.T, earlier, later billing.ALegReport) {
	t.Helper()
	require.False(t, earlier.AsOf.IsZero(), "AsOf must be an output observation timestamp")
	require.False(t, later.AsOf.IsZero(), "AsOf must be an output observation timestamp")
	require.False(t, later.AsOf.Before(earlier.AsOf),
		"observation clock must not run backward: %v -> %v", earlier.AsOf, later.AsOf)
}

// cycle3CanonicalJSON renders a report deterministically for equality
// comparison. AsOf is an output observation timestamp (time.Now at query
// time), so it is removed: logical-snapshot equality is identity of every
// other field. Prefer cycle3BusinessJSON plus cycle3RequireAsOfObserved
// for new proofs; this alias remains for the PostgreSQL mirror.
func cycle3CanonicalJSON(t *testing.T, report billing.ALegReport) []byte {
	t.Helper()
	return cycle3BusinessJSON(t, report)
}

// cycle3CallDTOBytes captures the complete canonical DTO for one call: its
// Calls entry (summary plus page legs, every lineage field) and every
// contribution carrying its lineage. Byte equality across projections
// proves full immutability, not just status/amount/operation-key.
func cycle3CallDTOBytes(t *testing.T, report billing.ALegReport, callID billing.BillingCallID) []byte {
	t.Helper()
	found := false
	var parts []any
	for _, row := range report.Calls {
		if row.CallID == callID {
			parts = append(parts, row)
			found = true
		}
	}
	require.True(t, found, "call %q must be on the page for DTO capture", callID.String())
	for _, c := range report.Contributions {
		if c.Call.CallID == callID {
			parts = append(parts, c)
		}
	}
	raw, err := json.Marshal(parts)
	require.NoError(t, err)
	return raw
}

// cycle3TerminalSettledCall closes one BillingCallID through the production
// terminal path: B-leg then call records via the runtime-owned
// billing.TerminalUsageSink interface (DONE/terminal outcomes), followed by
// the two billing seams (AdmitExposure + ApplyCallBillingResult). It
// returns the allocated BillingCallID; callers resuming the same A-leg get
// a distinct ID with newly allocated B-legs by construction.
//
//nolint:revive // test helper keeps t first per Go testing convention
func cycle3TerminalSettledCall(t *testing.T, ctx context.Context, sink billing.TerminalUsageSink, store *DurableStore, accountID, aLegID, bLegID string, chargeNano int64) billing.BillingCallID {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	require.NoError(t, sink.AppendLeg(ctx, billing.CallLegUsageRecord{
		CallID: callID, ALegID: aLegID, BLegID: bLegID, AttemptSeq: 1,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	}))
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
		ExpectedBLegIDs:    []string{bLegID},
	}
	require.NoError(t, sink.AppendCall(ctx, call))
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:             billing.Money{Nano: 50_000, Currency: "USD"},
		PricingRef:      billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	})
	require.NoError(t, err)
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{
			CallID: callID, Fingerprint: "fp-" + callID.String(),
			CustomerCharge: billing.Money{Nano: chargeNano, Currency: "USD"},
		},
	})
	require.NoError(t, err)
	return callID
}

// cycle3UnionPages walks the opaque cursor to exhaustion with the given
// limit and returns the union of call IDs and (call, B-leg) contribution
// keys, asserting deterministic repeat order across two full walks.
func cycle3UnionPages(t *testing.T, store *DurableStore, accountID, aLegID string, limit int) (calls []string, legs []string) {
	t.Helper()
	walk := func() (callIDs []string, legKeys []string) {
		cursor := ""
		for {
			report := queryALegCustomerReport(t, store, accountID, aLegID, limit, cursor)
			for _, row := range report.Calls {
				callIDs = append(callIDs, row.CallID.String())
			}
			for _, c := range report.Contributions {
				legKeys = append(legKeys, c.Call.CallID.String()+"\x00"+c.BLegID)
			}
			if report.NextCursor == "" {
				break
			}
			cursor = report.NextCursor
		}
		return callIDs, legKeys
	}
	callIDs, legKeys := walk()
	callIDs2, legKeys2 := walk()
	require.Equal(t, callIDs, callIDs2, "paginated call order must be deterministic across walks")
	require.Equal(t, legKeys, legKeys2, "paginated contribution order must be deterministic across walks")
	return callIDs, legKeys
}

func cycle3RequireExactlyOnce(t *testing.T, values []string, scope string) {
	t.Helper()
	seen := make(map[string]int, len(values))
	for _, v := range values {
		seen[v]++
	}
	for v, n := range seen {
		require.Equal(t, 1, n, "%s %q appears %d times, want exactly once", scope, v, n)
	}
}

// TestALegReportCycle3OldTokenContinuationAfterAppend certifies cursor
// snapshot semantics against later appended evidence: fetch a real first
// page and retain its token, append a later BillingCallID/B-leg/economics
// to the same A-leg through the production terminal path, resume with the
// OLD token, then start a fresh later projection.
//
// Proven semantics (keyset positions over live data, not frozen
// snapshots): the old token stays valid; every original call/leg appears
// exactly once across the old pages with no duplicates or omissions; the
// new call appears at most once in the continuation (live tail, never
// duplicated); resumed pages carry LIVE scope-wide totals; every query
// mints a fresh non-decreasing AsOf. The fresh projection then includes
// all calls exactly once with the originals in identical relative order.
func TestALegReportCycle3OldTokenContinuationAfterAppend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c3r1-token", "a-leg-c3r1"
	seedALegCustomerAccount(t, store, accountID)
	var sink billing.TerminalUsageSink = store

	callA := cycle3TerminalSettledCall(t, ctx, sink, store, accountID, aLegID, "b-a", 20)
	callB := cycle3TerminalSettledCall(t, ctx, sink, store, accountID, aLegID, "b-b", 30)

	// Pre-append canonical order over full limit-1 walks.
	preCalls, preLegs := cycle3UnionPages(t, store, accountID, aLegID, 1)
	require.Len(t, preCalls, 2)
	cycle3RequireExactlyOnce(t, preCalls, "pre-append call")

	// First page with its real opaque token.
	page1 := queryALegCustomerReport(t, store, accountID, aLegID, 1, "")
	token := page1.NextCursor
	require.NotEmpty(t, token, "a truncated first page must mint a real page token")
	var page1Calls, page1Legs []string
	for _, row := range page1.Calls {
		page1Calls = append(page1Calls, row.CallID.String())
	}
	for _, c := range page1.Contributions {
		page1Legs = append(page1Legs, c.Call.CallID.String()+"\x00"+c.BLegID)
	}

	// A resumed invocation lands on the same A-leg through the normal
	// allocation path: distinct BillingCallID, new B-leg, settled charge.
	callC := cycle3TerminalSettledCall(t, ctx, sink, store, accountID, aLegID, "b-c", 40)
	require.NotEqual(t, callA.String(), callC.String())
	require.NotEqual(t, callB.String(), callC.String())

	// Resume with the OLD token: it stays valid across appended evidence.
	var resumeCalls, resumeLegs []string
	resumedTotalsLive := false
	cursor := token
	var resumedFirst billing.ALegReport
	resumedPages := 0
	for {
		r := queryALegCustomerReport(t, store, accountID, aLegID, 1, cursor)
		if resumedPages == 0 {
			resumedFirst = r
		}
		resumedPages++
		// Scope-wide totals are live, never frozen at the old token:
		// the continuation already observes the appended call.
		require.Equal(t, 3, r.CallCount, "resumed page %d: CallCount is scope-wide live", resumedPages)
		require.Equal(t, int64(90), r.Retail.KnownSubtotal.Nano, "resumed page %d: totals are live", resumedPages)
		resumedTotalsLive = true
		cycle3RequireAsOfObserved(t, page1, r)
		for _, row := range r.Calls {
			resumeCalls = append(resumeCalls, row.CallID.String())
		}
		for _, c := range r.Contributions {
			resumeLegs = append(resumeLegs, c.Call.CallID.String()+"\x00"+c.BLegID)
		}
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
		require.Less(t, resumedPages, 10, "old-token continuation must terminate")
	}
	require.True(t, resumedTotalsLive)
	require.False(t, resumedFirst.AsOf.Before(page1.AsOf), "resumed pages are freshly observed, not replayed")

	// Originals exactly once across old pages: no duplicate, no omission.
	combinedCalls := append(append([]string(nil), page1Calls...), resumeCalls...)
	combinedLegs := append(append([]string(nil), page1Legs...), resumeLegs...)
	originals := map[string]bool{callA.String(): true, callB.String(): true}
	originalCount := 0
	for _, id := range combinedCalls {
		if originals[id] {
			originalCount++
		}
	}
	require.Equal(t, 2, originalCount, "old-token pages must yield each original call once, got %v", combinedCalls)
	cycle3RequireExactlyOnce(t, combinedCalls, "old-token call")
	originalLegCount := 0
	for _, key := range combinedLegs {
		for orig := range originals {
			if len(key) > len(orig) && key[:len(orig)] == orig {
				originalLegCount++
			}
		}
	}
	require.Equal(t, 2, originalLegCount, "old-token pages must yield each original leg once, got %v", combinedLegs)
	cycle3RequireExactlyOnce(t, combinedLegs, "old-token contribution")
	// The new call rides the live tail at most once: visible fresh
	// evidence is never duplicated by the old token.
	newCallCount := 0
	for _, id := range combinedCalls {
		if id == callC.String() {
			newCallCount++
		}
	}
	require.LessOrEqual(t, newCallCount, 1, "old-token continuation must never duplicate the appended call")

	// Fresh later projection: all three calls and legs exactly once, with
	// the originals in identical relative order to the pre-append walk.
	freshCalls, freshLegs := cycle3UnionPages(t, store, accountID, aLegID, 1)
	require.Len(t, freshCalls, 3)
	cycle3RequireExactlyOnce(t, freshCalls, "fresh call")
	require.Len(t, freshLegs, 3)
	cycle3RequireExactlyOnce(t, freshLegs, "fresh contribution")
	var freshOriginals []string
	for _, id := range freshCalls {
		if originals[id] {
			freshOriginals = append(freshOriginals, id)
		}
	}
	var preOriginals []string
	for _, id := range preCalls {
		if originals[id] {
			preOriginals = append(preOriginals, id)
		}
	}
	require.Equal(t, preOriginals, freshOriginals, "originals keep identical relative order in the later projection")
	require.Len(t, preLegs, 2)
}

// TestALegReportCycle3RealLifecycleFullDTOImmutable certifies the real
// DONE/terminal closure and resume lifecycle: call and B-leg records close
// through the runtime-owned billing.TerminalUsageSink handoff, settle
// through AdmitExposure + ApplyCallBillingResult, the A-leg idles with no
// billing writer invoked (retirement is storage-only by production
// construction, compile_generation.go:208-218), and the same A-leg resumes
// through normal allocation with a distinct BillingCallID and new B-leg.
// The complete canonical call-1 DTO (every lineage field) is byte-identical
// before and after.
func TestALegReportCycle3RealLifecycleFullDTOImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c3r1-life", "a-leg-c3r1"
	seedALegCustomerAccount(t, store, accountID)
	var sink billing.TerminalUsageSink = store

	// Call 1: provider-known leg plus customer settlement, all through the
	// terminal handoff and billing seams.
	call1 := cycle3TerminalSettledCall(t, ctx, sink, store, accountID, aLegID, "b-1", 60)
	leg1, err := billing.CallLegUsageRecord{
		CallID: call1, ALegID: aLegID, BLegID: "b-1", AttemptSeq: 1,
		BackendID: "ok", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	}.Seal()
	require.NoError(t, err)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, call1, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, call1, leg1)

	before := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, 1, before.CallCount)
	call1DTOBefore := cycle3CallDTOBytes(t, before, call1)
	hashesBefore := cycle3SnapshotHashes(t, store)

	// A-leg idle/retirement/retention transition: no billing writer exists
	// for it (compile_generation.go:208-218 ends only prompt-cache and
	// conversation-view state), so writer-silence plus repeated reads is
	// the faithful transition. Every repeat must observe identical
	// business DTO, fresh non-decreasing AsOf, and identical hashes.
	previous := before
	for i := 0; i < 3; i++ {
		idle := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
		require.Equal(t, string(cycle3BusinessJSON(t, before)), string(cycle3BusinessJSON(t, idle)),
			"idle repeat %d: business DTO must be identical", i)
		cycle3RequireAsOfObserved(t, previous, idle)
		require.Equal(t, hashesBefore, cycle3SnapshotHashes(t, store),
			"idle repeat %d: durable contents must be identical", i)
		previous = idle
	}

	// Resume the same A-leg through normal allocation: distinct
	// BillingCallID, newly allocated B-leg, completed terminal outcome.
	call2 := cycle3TerminalSettledCall(t, ctx, sink, store, accountID, aLegID, "b-2", 25)
	require.NotEqual(t, call1.String(), call2.String())

	after := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, 2, after.CallCount)
	require.Equal(t, string(call1DTOBefore), string(cycle3CallDTOBytes(t, after, call1)),
		"complete canonical call-1 DTO (summary, legs, contributions) must survive resume byte-identical")
	require.Equal(t, int64(85), after.Retail.KnownSubtotal.Nano)
	require.Equal(t, 2, after.Retail.SettledCalls)
	requireProviderLegTotals(t, after, 50, 1, 1, 0, 0)
	cycle3RequireAsOfObserved(t, previous, after)
}

// TestALegReportCycle3RetirementAndReadOnlyNoWrites certifies that A-leg
// retention/retirement/idle is a continuity/storage concern producing zero
// new usage, customer charge, pass-through adjustment, provider charge,
// journal transaction, head, fence, snapshot, or work-state mutation. The
// report path is read-only: repeated identical queries produce identical
// business DTOs with identical durable content hashes over every table
// the report reads, including billing_economic_revision_work_state.
// Same-count updates are detectable because full canonical contents are
// hashed, not just row counts.
func TestALegReportCycle3RetirementAndReadOnlyNoWrites(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c3-readonly", "a-leg-c3"
	seedALegCustomerAccount(t, store, accountID)

	// Mixed planes: a pass-through call (posted 60, provider 80 -> -20
	// validated adjustment) and a provider-known leg (50 COGS).
	ptCall, _ := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	applyALegPassThroughRevision(t, store, accountID, ptCall, phase10ProviderCost(2, 80, "USD"))
	provCall := seedC2BCall(t, store, accountID, aLegID)
	provLeg := seedC2BLeg(t, store, provCall, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2bProviderRevisionInput(accountID, provCall, aLegID, "b-1", "head-b-1", 1, 50, true))
	claimC2BLegWork(t, store, accountID, provCall, provLeg)

	baseline := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, int64(60), baseline.Retail.KnownSubtotal.Nano)
	requireProviderLegTotals(t, baseline, 50, 1, 0, 0, 0)
	beforeHashes := cycle3SnapshotHashes(t, store)
	beforeBusiness := cycle3BusinessJSON(t, baseline)

	// Idle/retention transition: no writer calls, only repeated identical
	// report queries. Every repeat must render the identical business DTO
	// with a fresh non-decreasing AsOf and leave every durable content
	// hash untouched — zero new charge/head/fence/snapshot/journal or
	// economic-revision work-state mutation.
	previous := baseline
	for i := 0; i < 3; i++ {
		repeat := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
		require.Equal(t, string(beforeBusiness), string(cycle3BusinessJSON(t, repeat)),
			"repeat %d: identical query must render identical business DTO", i)
		cycle3RequireAsOfObserved(t, previous, repeat)
		require.Equal(t, beforeHashes, cycle3SnapshotHashes(t, store),
			"repeat %d: report query must not mutate durable contents", i)
		require.Equal(t, int64(60), repeat.Retail.KnownSubtotal.Nano)
		requireProviderLegTotals(t, repeat, 50, 1, 0, 0, 0)
		previous = repeat
	}

	// Retirement equality at the same logical snapshot: business DTO,
	// lineage, and every table hash are unchanged by the idle transition.
	final := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, string(beforeBusiness), string(cycle3BusinessJSON(t, final)))
	cycle3RequireAsOfObserved(t, previous, final)
	require.Equal(t, beforeHashes, cycle3SnapshotHashes(t, store))
	ptSummary := findALegCall(t, final, ptCall)
	require.Equal(t, billing.ALegCallKnown, ptSummary.Status)
	require.Len(t, ptSummary.Adjustments, 1, "pass-through adjustment survives idle unchanged")
	resolved := findProviderLeg(t, final, provCall, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolved.ProviderStatus)
	require.Equal(t, int64(50), resolved.ProviderCost.Nano)
}

// TestALegReportCycle3LateCorrectionsAcrossPlanes certifies late
// post-terminal changes updating a later projection without reopening
// execution. A customer reversal nets a settled call 20 -> 12 through the
// production journal writer (fenced, revision-aware, idempotent),
// pass-through posts a positive then a negative correction, and a
// multi-child provider leg corrects 30 -> 25 while a sibling leg records
// zero. Replay of the correction and stale/CAS-loser inputs cause no
// additional durable mutation and the report stays identical. The earlier
// snapshot copy is stable; the latest projection changes exactly once per
// plane; retail carries proven customer charges only and
// provider/pass-through never leak into it.
func TestALegReportCycle3LateCorrectionsAcrossPlanes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c3-late", "a-leg-c3"
	seedALegCustomerAccount(t, store, accountID)

	// Call A: customer settles 20, then a late same-scope partial reversal
	// of 8 nets to 12 through the production journal writer
	// (postJournalTransaction: input validation, correction-linkage
	// fencing, account-sequence allocation, sealed fingerprint). The
	// writer derives the canonical correction group; usage execution rows
	// must not be reopened.
	callA := seedALegCustomerCall(t, store, accountID, aLegID, []string{"b-a"}, 20)
	preCorrection := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, int64(20), findALegCall(t, preCorrection, callA).CustomerCharge.Nano)
	var usageCallsBefore, usageLegsBefore int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM usage_call_records WHERE account_id = ?`, accountID).Scan(ctx, &usageCallsBefore))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM usage_leg_records WHERE call_id = ?`, callA.String()).Scan(ctx, &usageLegsBefore))
	sourceA, err := billing.CustomerSettlementSourceKey(accountID, callA)
	require.NoError(t, err)
	reversalInput := billing.JournalTransaction{
		ID: "tx-c3-reversal", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "src-tx-c3-reversal",
		AccountID: accountID, TurnID: callA.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		ReversalOf: sourceA,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "usage_revenue", Side: billing.JournalDebit, Amount: billing.Money{Nano: 8, Currency: "USD"}},
			{LedgerAccount: "customer_financial_account", Side: billing.JournalCredit, Amount: billing.Money{Nano: 8, Currency: "USD"}},
		},
	}
	reversal, err := store.postJournalTransaction(ctx, reversalInput)
	require.NoError(t, err, "production writer must admit the linked customer reversal")
	require.Equal(t, "tx-c3-reversal", reversal.ID)
	require.Equal(t, sourceA, reversal.ReversalOf)
	require.Equal(t, sourceA, reversal.CorrectionGroupID, "writer derives the canonical correction group")
	midCorrection := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegCallKnown, findALegCall(t, midCorrection, callA).Status)
	require.Equal(t, int64(12), findALegCall(t, midCorrection, callA).CustomerCharge.Nano)
	hashesAfterCorrection := cycle3SnapshotHashes(t, store)

	// Replay of the identical correction is idempotent: same identity and
	// sequence, no new durable row.
	replayed, err := store.postJournalTransaction(ctx, reversalInput)
	require.NoError(t, err)
	require.Equal(t, reversal.ID, replayed.ID)
	require.Equal(t, reversal.AccountSequence, replayed.AccountSequence)
	require.Equal(t, hashesAfterCorrection, cycle3SnapshotHashes(t, store),
		"correction replay must not mutate durable contents")

	// Stale correction (same source key, different amount) is a CAS
	// conflict: rejected with no mutation.
	stale := reversalInput
	stale.ID = "tx-c3-reversal-stale"
	stale.Entries = []billing.JournalEntry{
		{LedgerAccount: "usage_revenue", Side: billing.JournalDebit, Amount: billing.Money{Nano: 9, Currency: "USD"}},
		{LedgerAccount: "customer_financial_account", Side: billing.JournalCredit, Amount: billing.Money{Nano: 9, Currency: "USD"}},
	}
	_, err = store.postJournalTransaction(ctx, stale)
	require.ErrorIs(t, err, ErrIdentityConflict)
	require.Equal(t, hashesAfterCorrection, cycle3SnapshotHashes(t, store),
		"stale correction must not mutate durable contents")

	// A second reversal of the same canonical (race loser) is rejected by
	// the writer fence: the canonical already has a posted reversal.
	loser := reversalInput
	loser.ID = "tx-c3-reversal-loser"
	loser.SourceKey = "src-tx-c3-reversal-loser"
	_, err = store.postJournalTransaction(ctx, loser)
	require.ErrorIs(t, err, ErrCorrectionInvalid)
	require.Equal(t, hashesAfterCorrection, cycle3SnapshotHashes(t, store),
		"CAS-loser correction must not mutate durable contents")

	// The report stays identical across replay/stale/loser attempts.
	steady := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, string(cycle3BusinessJSON(t, midCorrection)), string(cycle3BusinessJSON(t, steady)))
	cycle3RequireAsOfObserved(t, midCorrection, steady)
	var usageCallsAfter, usageLegsAfter int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM usage_call_records WHERE account_id = ?`, accountID).Scan(ctx, &usageCallsAfter))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM usage_leg_records WHERE call_id = ?`, callA.String()).Scan(ctx, &usageLegsAfter))
	require.Equal(t, usageCallsBefore, usageCallsAfter, "late correction must not reopen call execution")
	require.Equal(t, usageLegsBefore, usageLegsAfter, "late correction must not reopen B-leg execution")

	// Call B: pass-through posted 60, positive revision (provider 80 ->
	// adjustment -20), then negative correction (provider 70 -> +10).
	callB, _ := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	up := phase10ProviderCost(2, 80, "USD")
	upSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callB, up)
	require.NoError(t, err)
	applyALegPassThroughRevision(t, store, accountID, callB, up)
	down := phase10ProviderCost(3, 70, "USD")
	downSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callB, down)
	require.NoError(t, err)
	applyALegPassThroughRevision(t, store, accountID, callB, down)

	// Call C: provider leg b-1 with two children (charge-a corrected
	// 30 -> 25, charge-b 20 => 45) plus sibling leg b-2 recorded zero.
	callC := seedC2BCall(t, store, accountID, aLegID)
	legC1 := seedC2BLeg(t, store, callC, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-1", "charge-a", "head-charge-a", 1, 30, true))
	correctedA := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-1", "charge-a", "head-charge-a", 2, 25, true))
	require.Equal(t, int64(-5), correctedA.Delta.Nano)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-1", "charge-b", "head-charge-b", 1, 20, true))
	legC2 := seedC2BLeg(t, store, callC, aLegID, "b-2", 2, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-2", "charge-z", "head-charge-z", 1, 10, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-2", "charge-z", "head-charge-z", 2, 10, false))
	claimC2BLegWork(t, store, accountID, callC, legC1)
	claimC2BLegWork(t, store, accountID, callC, legC2)

	latest := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, 3, latest.CallCount)
	cycle3RequireAsOfObserved(t, preCorrection, latest)

	// Customer plane: A nets 20 -> 12 exactly once; B holds its posted
	// 60 with two validated adjustments; retail sums proven charges only.
	summaryA := findALegCall(t, latest, callA)
	require.Equal(t, billing.ALegCallKnown, summaryA.Status)
	require.True(t, summaryA.CustomerChargeKnown)
	require.Equal(t, int64(12), summaryA.CustomerCharge.Nano)
	require.Equal(t, []string{"b-a"}, summaryA.ExpectedBLegIDs, "selected B-leg lineage is production-real")
	require.Empty(t, summaryA.MissingBLegIDs)
	require.Equal(t, sourceA+":customer_call_settlement", summaryA.CustomerOperationKey,
		"operation anchor is the production settlement source")
	summaryB := findALegCall(t, latest, callB)
	require.Equal(t, billing.ALegCallKnown, summaryB.Status)
	require.Equal(t, int64(60), summaryB.CustomerCharge.Nano)
	require.Len(t, summaryB.Adjustments, 2)
	require.Equal(t, upSource, summaryB.Adjustments[0].TransactionID)
	require.Equal(t, int64(-20), summaryB.Adjustments[0].Amount.Nano)
	require.Equal(t, downSource, summaryB.Adjustments[1].TransactionID)
	require.Equal(t, int64(10), summaryB.Adjustments[1].Amount.Nano)
	for _, ref := range summaryB.Adjustments {
		require.True(t, ref.Validated)
	}
	require.Equal(t, int64(72), latest.Retail.KnownSubtotal.Nano,
		"retail subtotal must equal exactly the proven customer charges 12 + 60")
	require.Equal(t, 2, latest.Retail.SettledCalls)
	require.Equal(t, 1, latest.Retail.PendingCalls, "provider-only call C stays customer-pending")
	require.Equal(t, 0, latest.Retail.UnknownCalls)

	// Provider plane: b-1 known 45 with deterministic charge-ordered
	// children summing exactly; b-2 known_zero; totals reconcile.
	resolvedC1 := findProviderLeg(t, latest, callC, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolvedC1.ProviderStatus)
	require.Equal(t, int64(45), resolvedC1.ProviderCost.Nano)
	require.Len(t, resolvedC1.ProviderChildren, 2)
	require.Equal(t, "charge-a", resolvedC1.ProviderChildren[0].ChargeID)
	require.Equal(t, "charge-b", resolvedC1.ProviderChildren[1].ChargeID)
	var childSum int64
	for _, child := range resolvedC1.ProviderChildren {
		childSum += child.Amount.Nano
	}
	require.Equal(t, resolvedC1.ProviderCost.Nano, childSum, "leg cost must equal the checked child sum")
	require.Equal(t, correctedA.Posting.OperationKey, findProviderChild(t, resolvedC1, "charge-a").OperationKey)
	resolvedC2 := findProviderLeg(t, latest, callC, "b-2")
	require.Equal(t, billing.ALegProviderKnownZero, resolvedC2.ProviderStatus)
	// b-a (call A) never started provider work, so it stays pending:
	// every attributable B-leg in scope carries explicit completeness.
	requireProviderLegTotals(t, latest, 45, 1, 1, 1, 0)

	// Provider COGS and pass-through adjustments never enter retail:
	// retail is exactly the proven customer charges 12 + 60 while the
	// provider plane independently carries 45 COGS.
	require.Equal(t, int64(72), latest.Retail.KnownSubtotal.Nano)
	require.Equal(t, int64(45), latest.Provider.KnownSubtotal.Nano)

	// Earlier snapshot stability: the pre-correction copy still carries
	// the original 20-charge view of call A; the latest projection entries
	// each appear exactly once across a limit-1 paginated walk.
	require.Equal(t, int64(20), findALegCall(t, preCorrection, callA).CustomerCharge.Nano)
	callIDs, legKeys := cycle3UnionPages(t, store, accountID, aLegID, 1)
	require.Len(t, callIDs, 3)
	cycle3RequireExactlyOnce(t, callIDs, "call")
	require.Len(t, legKeys, 3, "b-a, b-1, b-2 (pass-through call B carries no B-legs)")
	cycle3RequireExactlyOnce(t, legKeys, "contribution")
}
