package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Rolling A-leg snapshots, Cycle 1 (refinement Task 5.3).
//
// One rollback-only read transaction defines AsOf for calls, legs,
// operation snapshots, and journals. Customer authority is proven per call
// by the single core evaluator (billing.EvaluateALegCallAuthority) from
// the exact canonical settlement marker plus the matching canonical
// journal (or a proven zero with no settlement journal); provider COGS and
// exclusions stay pending and are never faked as known zero. Pass-through
// journals ride a distinct adjustment plane; their production-writer join is
// deferred to Cycle 2. Unknown is never zero: stray, mismatched, or missing
// evidence classifies the call pending/unknown with an issue instead of a
// charge.
//
// Operation kinds and financial issue codes are owned by core billing.
// This shell owns SQL, pagination, presence/journal retention bounds, and
// snapshot assembly only.

// alegReportScopeChunkSize bounds per-query fact materialization for one
// rolling snapshot: every scope ID/marker/journal IN-list carries at most
// this many call IDs, and totals stream scope-wide in chunks of this size.
// It is independent of total A-leg size; only page output additionally
// honors ALegReportMaxLimit. Tests override it for small-scope proofs.
var alegReportScopeChunkSize = 200

// alegReportMaxPresenceLegs caps retained B-leg presence rows per call,
// independent of durable fan-out. Tests override it for small-scope proofs.
var alegReportMaxPresenceLegs = 100

// alegReportMaxJournalsPerCall caps retained journals per call, independent
// of durable per-call density. Production chains hold one settlement plus a
// few linked corrections; beyond the cap the call is unresolved, never
// partially netted. Tests override it for small-scope proofs.
var alegReportMaxJournalsPerCall = 16

// alegReportMaxEntriesPerTx caps retained entries per journal transaction,
// independent of durable per-transaction density. Writer pairs carry two
// entries; beyond the cap the owning call is unresolved, never partially
// netted. Tests override it for small-scope proofs.
var alegReportMaxEntriesPerTx = 8

// alegReportMaxIssues bounds retained report issues independent of scope
// size; beyond the cap a single truncation issue is recorded instead.
const alegReportMaxIssues = 128

// alegReportMaxSnapshotsPerSource caps retained operation snapshots per
// adjustment source identity, independent of durable density. The writer
// posts exactly one snapshot per applied revision; more than one row per
// source is already ambiguous and resolves the owning call unknown, never
// partially. The window keeps ambiguity detectable while bounding memory.
const alegReportMaxSnapshotsPerSource = 4

// Cycle 1 shell-owned bound issue codes. Financial proof codes live in
// core billing (billing.ALegIssue*); every code carries the call ID in
// Detail and the marker/journal sequence where durable.
const (
	alegIssueLegFanout      = "customer_leg_fanout"
	alegIssueJournalFanout  = "customer_journal_fanout"
	alegIssueEntryFanout    = "customer_entry_fanout"
	alegIssueProviderFanout = "provider_cost_fanout"
)

func (s *DurableStore) QueryALegReport(ctx context.Context, query billing.ALegReportQuery) (billing.ALegReport, error) {
	normalized, err := query.Normalize()
	if err != nil {
		return billing.ALegReport{}, err
	}
	var txOpts *sql.TxOptions
	if s.db.Dialect().Name() == dialect.PG {
		txOpts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, err := s.db.BeginTx(ctx, txOpts)
	if err != nil {
		return billing.ALegReport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	asOf := time.Now().UTC()

	account, err := getAccountTx(ctx, tx, normalized.AccountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return billing.ALegReport{}, billing.ErrReportNotFound
		}
		return billing.ALegReport{}, err
	}
	currency := account.Currency
	afterLegCall, afterLegBLeg, afterCall, err := billing.DecodeALegReportCursor(normalized.Cursor, s.storeID, account.ID, normalized.ALegID)
	if err != nil {
		return billing.ALegReport{}, err
	}
	// A structurally valid leg position must exist in this scope. Without
	// this check the position degrades into an arbitrary keyset boundary
	// and silently skips or restarts the leg stream.
	if afterLegCall != "" {
		var legExists int
		if err := tx.NewRaw(`SELECT 1 FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id WHERE l.call_id = ? AND l.b_leg_id = ? AND l.a_leg_id = ? AND c.account_id = ? LIMIT 1`, afterLegCall, afterLegBLeg, normalized.ALegID, account.ID).Scan(ctx, &legExists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return billing.ALegReport{}, fmt.Errorf("%w: unknown A-leg report leg position", billing.ErrReportInvalid)
			}
			return billing.ALegReport{}, fmt.Errorf("billingstore: validate A-leg leg position: %w", err)
		}
	}

	// Scope-wide counts stay stable on every page without materializing
	// rows: two aggregate reads, no fact loading.
	var callCount int
	if err := tx.NewRaw(`SELECT COUNT(*) FROM usage_call_records WHERE account_id = ? AND a_leg_id = ?`, account.ID, normalized.ALegID).Scan(ctx, &callCount); err != nil {
		return billing.ALegReport{}, fmt.Errorf("billingstore: count A-leg calls: %w", err)
	}

	// Independent bounded call page: keyset over (sealed_at, call_id) with
	// limit+1. The cursor carries the call ID; its sealed_at resolves here
	// so unknown positions fail closed instead of slicing wrong.
	var afterSealed string
	if afterCall != "" {
		// The call position resolves inside this exact scope: a real call
		// from another A-leg or account must fail closed instead of
		// silently offsetting pagination.
		if err := tx.NewRaw(`SELECT sealed_at FROM usage_call_records WHERE call_id = ? AND account_id = ? AND a_leg_id = ?`, afterCall, account.ID, normalized.ALegID).Scan(ctx, &afterSealed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return billing.ALegReport{}, fmt.Errorf("%w: unknown A-leg report call position", billing.ErrReportInvalid)
			}
			return billing.ALegReport{}, fmt.Errorf("billingstore: resolve A-leg call position: %w", err)
		}
	}
	type callPageRow struct {
		CallID string `bun:"call_id"`
	}
	var callPageRows []callPageRow
	if err := tx.NewRaw(`SELECT call_id FROM usage_call_records WHERE account_id = ? AND a_leg_id = ? AND (sealed_at > ? OR (sealed_at = ? AND call_id > ?)) ORDER BY sealed_at, call_id LIMIT ?`,
		account.ID, normalized.ALegID, afterSealed, afterSealed, afterCall, normalized.Limit+1).Scan(ctx, &callPageRows); err != nil {
		return billing.ALegReport{}, fmt.Errorf("billingstore: query A-leg call page: %w", err)
	}
	callPage := make([]string, 0, len(callPageRows))
	for _, row := range callPageRows {
		callPage = append(callPage, row.CallID)
	}
	hasMoreCalls := len(callPage) > normalized.Limit
	if hasMoreCalls {
		callPage = callPage[:normalized.Limit]
	}

	// Independent bounded leg page: account scope joins through the call
	// table while the (a_leg_id, call_id, b_leg_id) index drives the
	// keyset order; limit+1 bounds the page. Identity columns ride along
	// so every surfaced row proves against its immutable SQL row.
	legQuery := `SELECT l.call_id, l.b_leg_id, l.usage_leg_key, l.fingerprint, l.a_leg_id, c.account_id, l.payload_json FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id WHERE c.account_id = ? AND l.a_leg_id = ? AND (l.call_id > ? OR (l.call_id = ? AND l.b_leg_id > ?)) ORDER BY l.call_id, l.b_leg_id LIMIT ?`
	var legRows []legPayloadRow
	if err := tx.NewRaw(legQuery, account.ID, normalized.ALegID, afterLegCall, afterLegCall, afterLegBLeg, normalized.Limit+1).Scan(ctx, &legRows); err != nil {
		return billing.ALegReport{}, fmt.Errorf("billingstore: query A-leg legs: %w", err)
	}
	hasMoreLegs := len(legRows) > normalized.Limit
	if hasMoreLegs {
		legRows = legRows[:normalized.Limit]
	}
	// Every surfaced leg row proves row/payload lineage exactly once here;
	// both assembly loops below consume validated records keyed by durable
	// row identity, never re-decoded payloads.
	legRecords := make(map[string]billing.CallLegUsageRecord, len(legRows))
	for _, row := range legRows {
		var record billing.CallLegUsageRecord
		if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
			return billing.ALegReport{}, fmt.Errorf("billingstore: decode A-leg leg: %w", err)
		}
		if err := checkALegLegLineage(account.ID, normalized.ALegID, row, record); err != nil {
			return billing.ALegReport{}, err
		}
		legRecords[row.CallID+"\x00"+row.BLegID] = record
	}

	// Page union for closures, presence, and retained classes. Everything
	// below stays bounded by twice the page limit plus one.
	pageSet := make(map[string]bool, 2*(normalized.Limit+1))
	for _, callID := range callPage {
		pageSet[callID] = true
	}
	for _, row := range legRows {
		pageSet[row.CallID] = true
	}
	pageIDs := make([]string, 0, len(pageSet))
	for callID := range pageSet {
		pageIDs = append(pageIDs, callID)
	}
	presentLegs := make(map[string]map[string]bool, len(pageIDs))
	closures := make(map[string]billing.CallUsageRecord, len(pageIDs))
	overCap := make(map[string]bool)
	if len(pageIDs) > 0 {
		pairArgs := make([]any, 0, len(pageIDs))
		for _, callID := range pageIDs {
			pairArgs = append(pairArgs, callID)
		}
		var pairs []struct {
			CallID string `bun:"call_id"`
			BLegID string `bun:"b_leg_id"`
		}
		// Presence is scope-bound like the leg page itself, and capped per
		// call independent of durable fan-out: at most cap+1 rows per call
		// load, so the extra row deterministically proves overflow. A leg
		// row sharing a call_id but carrying a foreign account/A-leg never
		// satisfies expected B-leg presence for the queried scope.
		presenceArgs := append([]any{account.ID, normalized.ALegID}, pairArgs...)
		presenceArgs = append(presenceArgs, alegReportMaxPresenceLegs+1)
		if err := tx.NewRaw(`SELECT call_id, b_leg_id FROM (SELECT l.call_id, l.b_leg_id, ROW_NUMBER() OVER (PARTITION BY l.call_id ORDER BY l.b_leg_id) AS rn FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id WHERE c.account_id = ? AND l.a_leg_id = ? AND l.call_id IN (`+sqlPlaceholders(len(pairArgs))+`)) WHERE rn <= ?`, presenceArgs...).Scan(ctx, &pairs); err != nil {
			return billing.ALegReport{}, fmt.Errorf("billingstore: query A-leg leg presence: %w", err)
		}
		legCounts := make(map[string]int, len(pageIDs))
		for _, pair := range pairs {
			legCounts[pair.CallID]++
			if legCounts[pair.CallID] > alegReportMaxPresenceLegs {
				overCap[pair.CallID] = true
				continue
			}
			set, ok := presentLegs[pair.CallID]
			if !ok {
				set = make(map[string]bool)
				presentLegs[pair.CallID] = set
			}
			set[pair.BLegID] = true
		}
		var payloads []alegClosureRow
		if err := tx.NewRaw(`SELECT call_id, account_id, a_leg_id, usage_call_key, fingerprint, payload_json FROM usage_call_records WHERE call_id IN (`+sqlPlaceholders(len(pairArgs))+`)`, pairArgs...).Scan(ctx, &payloads); err != nil {
			return billing.ALegReport{}, fmt.Errorf("billingstore: query A-leg closures: %w", err)
		}
		for _, row := range payloads {
			var record billing.CallUsageRecord
			if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
				return billing.ALegReport{}, fmt.Errorf("billingstore: decode A-leg closure: %w", err)
			}
			if err := checkALegClosureLineage(account.ID, normalized.ALegID, row, record); err != nil {
				return billing.ALegReport{}, err
			}
			closures[row.CallID] = record
		}
	}

	// Scope-wide totals stream in fixed-size chunks: per-call authority
	// consumes only the current chunk, and only page calls retain classes.
	// Memory and placeholder counts stay bounded by alegReportScopeChunkSize
	// independent of total A-leg size. Per-call journal fan-out stays
	// writer-bounded (one settlement plus linked corrections); chunking
	// bounds scope scaling, not single-call density.
	classes := make(map[string]billing.ALegCallVerdict)
	var issues []billing.ReconciliationIssue
	droppedNonPage, droppedPage := 0, 0
	addIssue := func(issue billing.ReconciliationIssue) {
		if len(issues) < alegReportMaxIssues {
			issues = append(issues, issue)
			return
		}
		// At cap, preserve page-call context: evict the oldest
		// non-page issue for page calls, else drop and count.
		// Order of survivors never changes, so output stays
		// deterministic.
		call := issue.Detail
		if !pageSet[call] {
			droppedNonPage++
			return
		}
		for i, existing := range issues {
			if existing.Code == "customer_issues_truncated" || pageSet[existing.Detail] {
				continue
			}
			issues = append(issues[:i:i], issues[i+1:]...)
			issues = append(issues, issue)
			droppedNonPage++
			return
		}
		droppedPage++
	}
	subtotal := int64(0)
	settled, pending, unknown := 0, 0, 0
	// Provider plane accumulators cover every attributable scope leg,
	// including failed, retry, and loser attempts: only known legs
	// contribute to the COGS subtotal. Page legs additionally retain
	// verdicts for lineage mapping; counts stay scope-wide.
	providerSubtotal := int64(0)
	providerKnown, providerPending, providerZero, providerUnknown := 0, 0, 0, 0
	providerVerdicts := make(map[string]billing.ALegProviderLegVerdict)
	chunkAfterSealed, chunkAfterCall := "", ""
	for {
		var chunkIDs []string
		{
			var chunkRows []callPageRow
			if err := tx.NewRaw(`SELECT call_id FROM usage_call_records WHERE account_id = ? AND a_leg_id = ? AND (sealed_at > ? OR (sealed_at = ? AND call_id > ?)) ORDER BY sealed_at, call_id LIMIT ?`,
				account.ID, normalized.ALegID, chunkAfterSealed, chunkAfterSealed, chunkAfterCall, alegReportScopeChunkSize+1).Scan(ctx, &chunkRows); err != nil {
				return billing.ALegReport{}, fmt.Errorf("billingstore: stream A-leg calls: %w", err)
			}
			for _, row := range chunkRows {
				chunkIDs = append(chunkIDs, row.CallID)
			}
		}
		if len(chunkIDs) == 0 {
			break
		}
		more := len(chunkIDs) > alegReportScopeChunkSize
		if more {
			chunkIDs = chunkIDs[:alegReportScopeChunkSize]
		}
		markers, err := loadALegMarkersTx(ctx, tx, account.ID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		journals, journalFanout, err := loadALegCallJournalsTx(ctx, tx, account.ID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		// Trusted pass-through facts stream with the same chunk bounds:
		// one head query per chunk, adjustment snapshots in
		// chunk-bounded source batches, and exposure policies only for
		// headed calls. No per-call statements anywhere.
		heads, err := loadALegPassThroughHeadsTx(ctx, tx, account.ID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		snapshots, err := loadALegPassThroughSnapshotsTx(ctx, tx, account.ID, passThroughSources(chunkIDs, journals))
		if err != nil {
			return billing.ALegReport{}, err
		}
		policies, err := loadALegPassThroughPoliciesTx(ctx, tx, account.ID, headedPassThroughCalls(chunkIDs, heads))
		if err != nil {
			return billing.ALegReport{}, err
		}
		// Trusted provider facts stream with the same chunk bounds: leg
		// enumeration, heads, both fence kinds, work states, revision
		// states, and journal-bound snapshots. No per-call and no
		// per-leg statements anywhere. The leg cap covers the page
		// limit so every surfaced page leg always has a verdict.
		perCallCap := alegReportMaxPresenceLegs
		if normalized.Limit > perCallCap {
			perCallCap = normalized.Limit
		}
		providerLegs, providerOverCap, err := loadALegProviderLegsTx(ctx, tx, account.ID, normalized.ALegID, chunkIDs, perCallCap)
		if err != nil {
			return billing.ALegReport{}, err
		}
		// Head, fence, and work facts stream through deterministic
		// per-identity cap+1 windows; any overflow unions into the
		// same per-call over-cap set as leg enumeration, so an
		// over-cap leg resolves unknown with a fanout issue and never
		// contributes a partial subtotal.
		providerHeads, headsOverCap, err := loadALegProviderHeadsTx(ctx, tx, s.storeID, account.ID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		postingFences, postingOverCap, err := loadALegProviderPostingFencesTx(ctx, tx, s.storeID, account.ID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		executionFences, executionOverCap, err := loadALegProviderExecutionFencesTx(ctx, tx, s.storeID, account.ID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		workStates, workOverCap, err := loadALegProviderWorkTx(ctx, tx, account.ID, chunkIDs, perCallCap)
		if err != nil {
			return billing.ALegReport{}, err
		}
		for callID := range headsOverCap {
			providerOverCap[callID] = true
		}
		for callID := range postingOverCap {
			providerOverCap[callID] = true
		}
		for callID := range executionOverCap {
			providerOverCap[callID] = true
		}
		for callID := range workOverCap {
			providerOverCap[callID] = true
		}
		// Fail-closed anomaly markers run before any derived loads:
		// evidence that cannot be attributed to an enumerated leg
		// poisons its whole call (one DISTINCT row per call bounds
		// every marker no matter how many pseudo-leg identities
		// flood the tables), while attributable evidence with a
		// malformed lineage poisons exactly that leg. Poisoned
		// calls join the over-cap set so derivation skips them;
		// poisoned legs join the leg over-cap set below.
		headAnomalies, err := loadALegProviderHeadAnomaliesTx(ctx, tx, s.storeID, account.ID, normalized.ALegID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		postingCallAnomalies, postingLegAnomalies, err := loadALegProviderPostingAnomaliesTx(ctx, tx, s.storeID, account.ID, normalized.ALegID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		executionCallAnomalies, executionLegAnomalies, err := loadALegProviderExecutionAnomaliesTx(ctx, tx, s.storeID, account.ID, normalized.ALegID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		workAnomalies, err := loadALegProviderWorkAnomaliesTx(ctx, tx, account.ID, normalized.ALegID, chunkIDs)
		if err != nil {
			return billing.ALegReport{}, err
		}
		for callID := range headAnomalies {
			providerOverCap[callID] = true
		}
		for callID := range postingCallAnomalies {
			providerOverCap[callID] = true
		}
		for callID := range executionCallAnomalies {
			providerOverCap[callID] = true
		}
		for callID := range workAnomalies {
			providerOverCap[callID] = true
		}
		// The total per-B-leg head bound applies before any derived
		// loads: heads partition by exact identity (call, owning
		// B-leg, kind, charge), and only surviving legs contribute
		// revision-state keys and snapshot sources below.
		headsByLeg, legOverCap, err := groupALegProviderHeads(providerHeads)
		if err != nil {
			return billing.ALegReport{}, err
		}
		for legKey := range postingLegAnomalies {
			legOverCap[legKey] = true
		}
		for legKey := range executionLegAnomalies {
			legOverCap[legKey] = true
		}
		okHeads, okFences, okJournals, err := retainALegProviderFacts(chunkIDs, providerLegs, headsByLeg, postingFences, journals, providerOverCap, legOverCap)
		if err != nil {
			return billing.ALegReport{}, err
		}
		revisionPending, err := loadALegProviderRevisionStateTx(ctx, tx, s.storeID, providerHeadKeys(okHeads, okFences))
		if err != nil {
			return billing.ALegReport{}, err
		}
		providerSnapshots, err := loadALegProviderSnapshotsTx(ctx, tx, account.ID, providerSnapshotSources(chunkIDs, okJournals, okHeads))
		if err != nil {
			return billing.ALegReport{}, err
		}
		for _, callID := range chunkIDs {
			if code, ok := journalFanout[callID]; ok {
				class := billing.ALegCallVerdict{Status: billing.ALegCallUnknown, Charge: billing.Money{Currency: currency}}
				addIssue(billing.ReconciliationIssue{Code: code, Sequence: 0, Detail: callID})
				if pageSet[callID] {
					classes[callID] = class
				}
				unknown++
				continue
			}
			scope := billing.ALegAuthorityScope{StoreID: s.storeID, AccountID: account.ID, ALegID: normalized.ALegID, CallID: callID, Currency: currency}
			coreMarkers := alegCoreMarkers(markers[callID])
			class, classIssues, err := billing.EvaluateALegCallAuthority(scope, coreMarkers, stripALegAdjustmentJournals(journals[callID]))
			if err != nil {
				return billing.ALegReport{}, err
			}
			if class.Status == billing.ALegCallKnown {
				// Pass-through authority layers only onto proven retail:
				// a known call with complete validated adjustment
				// lineage stays known with that lineage attached, while
				// incomplete or conflicting evidence demotes it to
				// pending or unknown. Retail verdicts other than known
				// pass through untouched, preserving every Cycle 1
				// behavior bit-for-bit.
				head, hasHead := heads[callID]
				class, classIssues, err = composeALegPassThrough(scope, class, classIssues, coreMarkers, head, hasHead, policies, snapshots, journals[callID])
				if err != nil {
					return billing.ALegReport{}, err
				}
			}
			if overCap[callID] {
				// Fan-out beyond the presence cap: lineage completeness
				// can never be known for this call, regardless of its
				// customer authority verdict.
				class = billing.ALegCallVerdict{Status: billing.ALegCallUnknown, Charge: billing.Money{Currency: currency}, Adjustments: class.Adjustments}
				classIssues = append(classIssues, billing.ReconciliationIssue{Code: alegIssueLegFanout, Detail: callID})
			}
			for _, classIssue := range classIssues {
				addIssue(classIssue)
			}
			if pageSet[callID] {
				classes[callID] = class
			}
			switch class.Status {
			case billing.ALegCallKnown:
				settled++
				subtotal, err = billing.AddReportAmount(subtotal, class.Charge, currency)
				if err != nil {
					return billing.ALegReport{}, fmt.Errorf("%w: A-leg retail subtotal: %v", billing.ErrMoneyOverflow, err)
				}
			case billing.ALegCallPending:
				pending++
			default:
				unknown++
			}
		}
		// Provider authority is per B-leg and independent of retail:
		// every enumerated scope leg is proven here, including legs of
		// calls whose retail verdict is not known.
		providerFanoutIssued := make(map[string]bool)
		for _, callID := range chunkIDs {
			if providerOverCap[callID] {
				// Beyond the leg retention cap the scope population is
				// no longer exact: retained legs resolve unknown with
				// a fanout issue instead of contributing partial
				// totals, mirroring the retail presence cap.
				addIssue(billing.ReconciliationIssue{Code: alegIssueProviderFanout, Detail: callID})
				providerFanoutIssued[callID] = true
				for _, leg := range providerLegs[callID] {
					providerUnknown++
					if pageSet[callID] {
						providerVerdicts[callID+"\x00"+leg.BLegID] = billing.ALegProviderLegVerdict{Status: billing.ALegProviderUnknown}
					}
				}
				continue
			}
			for _, leg := range providerLegs[callID] {
				if legOverCap[callID+"\x00"+leg.BLegID] {
					// Beyond the per-leg head bound, or poisoned by
					// malformed attributable evidence, the leg
					// population is no longer exact: unknown with
					// a fanout issue instead of a partial total.
					// One issue per call; every over-cap leg of
					// the call still counts unknown.
					if !providerFanoutIssued[callID] {
						addIssue(billing.ReconciliationIssue{Code: alegIssueProviderFanout, Detail: callID})
						providerFanoutIssued[callID] = true
					}
					providerUnknown++
					if pageSet[callID] {
						providerVerdicts[callID+"\x00"+leg.BLegID] = billing.ALegProviderLegVerdict{Status: billing.ALegProviderUnknown}
					}
					continue
				}
				verdict, verdictIssues, err := evaluateALegProviderLegTx(
					billing.ALegAuthorityScope{StoreID: s.storeID, AccountID: account.ID, ALegID: normalized.ALegID, CallID: callID, Currency: currency},
					leg, okJournals[callID], okHeads[callID], okFences[callID], executionFences[callID],
					workStates, revisionPending, providerSnapshots)
				if err != nil {
					return billing.ALegReport{}, err
				}
				for _, verdictIssue := range verdictIssues {
					addIssue(verdictIssue)
				}
				switch verdict.Status {
				case billing.ALegProviderKnown:
					providerKnown++
					providerSubtotal, err = billing.AddReportAmount(providerSubtotal, verdict.Cost, currency)
					if err != nil {
						return billing.ALegReport{}, fmt.Errorf("%w: A-leg provider subtotal: %v", billing.ErrMoneyOverflow, err)
					}
				case billing.ALegProviderKnownZero:
					providerZero++
				case billing.ALegProviderPending:
					providerPending++
				default:
					providerUnknown++
				}
				if pageSet[callID] {
					providerVerdicts[callID+"\x00"+leg.BLegID] = verdict
				}
			}
		}
		if !more {
			break
		}
		lastChunkID := chunkIDs[len(chunkIDs)-1]
		if err := tx.NewRaw(`SELECT sealed_at FROM usage_call_records WHERE call_id = ?`, lastChunkID).Scan(ctx, &chunkAfterSealed); err != nil {
			return billing.ALegReport{}, fmt.Errorf("billingstore: advance A-leg stream: %w", err)
		}
		chunkAfterCall = lastChunkID
	}
	if droppedNonPage+droppedPage > 0 {
		issues = append(issues,
			billing.ReconciliationIssue{Code: "customer_issues_truncated", Detail: account.ID})
	}

	report := billing.ALegReport{
		StoreID: s.storeID, AccountID: account.ID, ALegID: normalized.ALegID,
		Currency: currency, AsOf: asOf,
		Retail: billing.ALegRetailTotals{Currency: currency,
			KnownSubtotal: billing.Money{Nano: subtotal, Currency: currency},
			SettledCalls:  settled, PendingCalls: pending, UnknownCalls: unknown},
		Provider: billing.ALegProviderTotals{Currency: currency,
			KnownSubtotal: billing.Money{Nano: providerSubtotal, Currency: currency},
			KnownLegs:     providerKnown, PendingLegs: providerPending, ZeroLegs: providerZero, UnknownLegs: providerUnknown},
		Issues:    issues,
		CallCount: callCount,
	}
	buildSummary := func(callID string) (billing.ALegReportCallSummary, error) {
		closure, ok := closures[callID]
		if !ok {
			return billing.ALegReportCallSummary{}, fmt.Errorf("%w: A-leg call %q vanished mid-snapshot", billing.ErrReportInvalid, callID)
		}
		class, ok := classes[callID]
		if !ok {
			return billing.ALegReportCallSummary{}, fmt.Errorf("%w: A-leg call %q left the page snapshot", billing.ErrReportInvalid, callID)
		}
		summary := billing.ALegReportCallSummary{
			SessionID: closure.SessionID, Outcome: closure.Outcome,
			ExpectedBLegIDs: append([]string(nil), closure.ExpectedBLegIDs...),
			Status:          class.Status, CustomerCharge: class.Charge,
			CustomerChargeKnown:  class.Known,
			CustomerOperationKey: class.OpKey, Adjustments: class.Adjustments,
		}
		if parsed, err := billing.ParseBillingCallID(callID); err == nil {
			summary.CallID = parsed
		}
		// Missing lineage is computed only under the presence cap: an
		// over-cap call keeps unknown status with its fanout issue
		// instead of a completeness claim from truncated presence.
		if !overCap[callID] {
			for _, expected := range closure.ExpectedBLegIDs {
				if !presentLegs[callID][expected] {
					summary.MissingBLegIDs = append(summary.MissingBLegIDs, expected)
				}
			}
		}
		return summary, nil
	}
	for _, callID := range callPage {
		summary, err := buildSummary(callID)
		if err != nil {
			return billing.ALegReport{}, err
		}
		var legs []billing.ALegReportLeg
		for _, row := range legRows {
			if row.CallID != callID {
				continue
			}
			record := legRecords[row.CallID+"\x00"+row.BLegID]
			provider, ok := providerVerdicts[row.CallID+"\x00"+row.BLegID]
			if !ok {
				return billing.ALegReport{}, fmt.Errorf("%w: A-leg leg %q left the page snapshot", billing.ErrReportInvalid, row.CallID+"\x00"+row.BLegID)
			}
			legs = append(legs, billing.ALegReportLeg{
				BLegID: row.BLegID, Outcome: record.Outcome, Surfaced: record.Surfaced,
				BackendID: record.BackendID, ProviderID: record.ProviderID, ModelID: record.ModelID,
				Fingerprint: record.Fingerprint, ProviderStatus: provider.Status, ZeroBasis: provider.ZeroBasis,
				ProviderCost: provider.Cost, ProviderOperationKey: provider.OperationKey, ProviderTransactionID: provider.TransactionID,
				ProviderChildren: provider.Children,
			})
		}
		report.Calls = append(report.Calls, billing.ALegReportCall{ALegReportCallSummary: summary, BLegs: legs})
	}
	for _, row := range legRows {
		record := legRecords[row.CallID+"\x00"+row.BLegID]
		summary, err := buildSummary(row.CallID)
		if err != nil {
			return billing.ALegReport{}, err
		}
		provider, ok := providerVerdicts[row.CallID+"\x00"+row.BLegID]
		if !ok {
			return billing.ALegReport{}, fmt.Errorf("%w: A-leg leg %q left the page snapshot", billing.ErrReportInvalid, row.CallID+"\x00"+row.BLegID)
		}
		report.Contributions = append(report.Contributions, billing.ALegReportContribution{
			Call: summary, LegKey: row.Key,
			BLegID: row.BLegID, Outcome: record.Outcome, Surfaced: record.Surfaced,
			BackendID: record.BackendID, ProviderID: record.ProviderID, ModelID: record.ModelID,
			Fingerprint: record.Fingerprint, ProviderStatus: provider.Status, ZeroBasis: provider.ZeroBasis,
			ProviderCost: provider.Cost, ProviderOperationKey: provider.OperationKey, ProviderTransactionID: provider.TransactionID,
			ProviderChildren: provider.Children,
		})
	}

	if !hasMoreCalls && !hasMoreLegs {
		return report, nil
	}
	callPos := afterCall
	if len(callPage) > 0 {
		callPos = callPage[len(callPage)-1]
	}
	// The exhausted leg position carries forward as the validated terminal:
	// clearing it would restart the leg query from the beginning and
	// duplicate the stream on the next page.
	legCallPos, legBLegPos := afterLegCall, afterLegBLeg
	if len(legRows) > 0 {
		legCallPos, legBLegPos = legRows[len(legRows)-1].CallID, legRows[len(legRows)-1].BLegID
	}
	report.NextCursor = billing.EncodeALegReportCursor(s.storeID, account.ID, normalized.ALegID, legCallPos, legBLegPos, callPos)
	return report, nil
}

// legPayloadRow carries a leg payload together with the immutable SQL row
// columns that proof it, including the joined account scope.
type legPayloadRow struct {
	CallID      string `bun:"call_id"`
	BLegID      string `bun:"b_leg_id"`
	Key         string `bun:"usage_leg_key"`
	Fingerprint string `bun:"fingerprint"`
	ALegID      string `bun:"a_leg_id"`
	AccountID   string `bun:"account_id"`
	Payload     string `bun:"payload_json"`
}

// alegClosureRow carries a closure payload together with the immutable
// SQL row columns that proof it.
type alegClosureRow struct {
	CallID      string `bun:"call_id"`
	AccountID   string `bun:"account_id"`
	ALegID      string `bun:"a_leg_id"`
	Key         string `bun:"usage_call_key"`
	Fingerprint string `bun:"fingerprint"`
	Payload     string `bun:"payload_json"`
}

// checkALegClosureLineage binds a decoded closure payload to its immutable
// SQL row: a row-anchored expected record replays against the decoded
// payload through the existing replay primitive, and row scope columns
// name this scope exactly. No self-comparison can establish this.
func checkALegClosureLineage(accountID, aLegID string, row alegClosureRow, record billing.CallUsageRecord) error {
	callID, err := billing.ParseBillingCallID(row.CallID)
	if err != nil {
		return fmt.Errorf("%w: A-leg closure row identity: %v", billing.ErrReportInvalid, err)
	}
	expected := record
	expected.Key = row.Key
	expected.Fingerprint = row.Fingerprint
	expected.CallID = callID
	expected.AccountID = row.AccountID
	expected.ALegID = row.ALegID
	if err := billing.CheckCallUsageReplay(expected, record); err != nil {
		return fmt.Errorf("%w: A-leg closure lineage: %v", billing.ErrReportInvalid, err)
	}
	if row.AccountID != accountID || row.ALegID != aLegID || row.CallID != record.CallID.String() {
		return fmt.Errorf("%w: A-leg closure scope mismatch", billing.ErrReportInvalid)
	}
	return nil
}

// checkALegLegLineage binds a decoded leg payload to its immutable SQL row
// the same way: row-anchored expected record through the existing leg
// replay primitive plus exact scope and B-leg identity.
func checkALegLegLineage(accountID, aLegID string, row legPayloadRow, record billing.CallLegUsageRecord) error {
	callID, err := billing.ParseBillingCallID(row.CallID)
	if err != nil {
		return fmt.Errorf("%w: A-leg leg row identity: %v", billing.ErrReportInvalid, err)
	}
	expected := record
	expected.Key = row.Key
	expected.Fingerprint = row.Fingerprint
	expected.CallID = callID
	expected.ALegID = row.ALegID
	expected.BLegID = row.BLegID
	if err := billing.CheckCallLegUsageReplay(expected, record); err != nil {
		return fmt.Errorf("%w: A-leg leg lineage: %v", billing.ErrReportInvalid, err)
	}
	if row.AccountID != accountID || row.ALegID != aLegID ||
		row.CallID != record.CallID.String() || row.BLegID != record.BLegID {
		return fmt.Errorf("%w: A-leg leg scope mismatch", billing.ErrReportInvalid)
	}
	return nil
}

func loadALegMarkersTx(ctx context.Context, q bun.IDB, accountID string, callIDs []string) (map[string][]operationSnapshotRow, error) {
	out := make(map[string][]operationSnapshotRow, len(callIDs))
	if len(callIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(callIDs)+3)
	args = append(args, accountID, billing.ALegSettlementKind, billing.ALegRepairKind)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	var rows []operationSnapshotRow
	if err := q.NewRaw(`SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind IN (?,?) AND source_key IN (`+sqlPlaceholders(len(callIDs))+`)`, args...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: query A-leg markers: %w", err)
	}
	for _, row := range rows {
		out[row.SourceKey] = append(out[row.SourceKey], row)
	}
	return out, nil
}

// alegCoreMarkers adapts durable snapshot rows to the single core
// customer-authority input. The shell owns SQL; all proof lives in core.
func alegCoreMarkers(rows []operationSnapshotRow) []billing.ALegMarker {
	out := make([]billing.ALegMarker, 0, len(rows))
	for _, row := range rows {
		out = append(out, billing.ALegMarker{
			OperationKey: row.OperationKey, AccountID: row.AccountID,
			OperationKind: row.OperationKind, SourceKey: row.SourceKey,
			Fingerprint: row.Fingerprint, IntegrityFingerprint: row.IntegrityFingerprint,
			Currency: row.Currency, Mode: row.Mode,
			Before: billing.AccountSnapshot{BalanceNano: row.BalanceBefore, SpendableNano: row.SpendableBefore,
				CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit,
				Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionBefore},
			After: billing.AccountSnapshot{BalanceNano: row.BalanceAfter, SpendableNano: row.SpendableAfter,
				CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit,
				Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionAfter},
			SequenceStart: row.SequenceStart, SequenceEnd: row.SequenceEnd,
		})
	}
	return out
}

// stripALegAdjustmentJournals removes the distinct pass-through adjustment
// plane from the retail authority input. Retail proof never nets or
// defers on adjustments; the pass-through plane composes separately via
// composeALegPassThrough. The input slice is never mutated.
func stripALegAdjustmentJournals(journals []billing.JournalTransaction) []billing.JournalTransaction {
	out := make([]billing.JournalTransaction, 0, len(journals))
	for _, journal := range journals {
		if journal.OperationKind == billing.CostPassThroughAdjustmentOperationKind {
			continue
		}
		out = append(out, journal)
	}
	return out
}

// passThroughSources collects adjustment source identities across one
// chunk in chunk order, deduplicated. Snapshot loading batches these in
// chunk-bounded statements instead of one scope-wide list.
func passThroughSources(chunkIDs []string, journals map[string][]billing.JournalTransaction) []string {
	var out []string
	seen := make(map[string]bool)
	for _, callID := range chunkIDs {
		for _, journal := range journals[callID] {
			if journal.OperationKind != billing.CostPassThroughAdjustmentOperationKind || seen[journal.SourceKey] {
				continue
			}
			seen[journal.SourceKey] = true
			out = append(out, journal.SourceKey)
		}
	}
	return out
}

// headedPassThroughCalls returns the chunk calls carrying a trusted head,
// in chunk order for deterministic fact loading.
func headedPassThroughCalls(chunkIDs []string, heads map[string]costPassThroughHeadRow) []string {
	var out []string
	for _, callID := range chunkIDs {
		if _, ok := heads[callID]; ok {
			out = append(out, callID)
		}
	}
	return out
}

// loadALegPassThroughHeadsTx loads trusted head rows for one chunk: one
// bounded statement, keyed by call.
func loadALegPassThroughHeadsTx(ctx context.Context, q bun.IDB, accountID string, callIDs []string) (map[string]costPassThroughHeadRow, error) {
	out := make(map[string]costPassThroughHeadRow, len(callIDs))
	if len(callIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(callIDs)+1)
	args = append(args, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	var rows []costPassThroughHeadRow
	if err := q.NewRaw(`SELECT head_key, account_id, call_id, settlement_operation_key, a_leg_id, original_transaction_id,
	policy_id, policy_version, missing_cost, safe_bound_nano, currency, allow_late_adjustment,
	status, posted_amount_nano, provider_lur_key, provider_valuation_id, provider_revision,
	provider_input_hash, settlement_fingerprint, head_version, fence, created_at, updated_at
	FROM billing_cost_pass_through_heads WHERE account_id = ? AND call_id IN (`+sqlPlaceholders(len(callIDs))+`)`, args...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: query A-leg pass-through heads: %w", err)
	}
	for _, row := range rows {
		out[row.CallID] = row
	}
	return out, nil
}

// loadALegPassThroughSnapshotsTx loads canonical adjustment operation
// snapshots for deduplicated source identities in chunk-bounded batches:
// no scope-wide IN list, no per-call statements. Rows stream
// deterministically ordered; per-source retention stays capped so
// ambiguity is always detectable downstream, never truncated clean.
func loadALegPassThroughSnapshotsTx(ctx context.Context, q bun.IDB, accountID string, sourceKeys []string) (map[string][]operationSnapshotRow, error) {
	out := make(map[string][]operationSnapshotRow)
	if len(sourceKeys) == 0 {
		return out, nil
	}
	for start := 0; start < len(sourceKeys); {
		end := start + alegReportScopeChunkSize
		if end > len(sourceKeys) {
			end = len(sourceKeys)
		}
		batch := sourceKeys[start:end]
		args := make([]any, 0, len(batch)+2)
		args = append(args, accountID, billing.CostPassThroughAdjustmentOperationKind)
		for _, key := range batch {
			args = append(args, key)
		}
		args = append(args, alegReportMaxSnapshotsPerSource)
		var rows []alegPassThroughSnapshotChunkRow
		if err := q.NewRaw(`SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM (SELECT s.*, ROW_NUMBER() OVER (PARTITION BY s.source_key ORDER BY s.operation_key) AS rn FROM billing_operation_snapshots s WHERE s.account_id = ? AND s.operation_kind = ? AND s.source_key IN (`+sqlPlaceholders(len(batch))+`)) WHERE rn <= ?`, args...).Scan(ctx, &rows); err != nil {
			return nil, fmt.Errorf("billingstore: query A-leg pass-through snapshots: %w", err)
		}
		for _, row := range rows {
			out[row.SourceKey] = append(out[row.SourceKey], row.operationSnapshotRow)
		}
		start = end
	}
	return out, nil
}

// loadALegPassThroughPoliciesTx loads expected charge policies from call
// exposures for headed chunk calls: one bounded statement. A missing
// exposure row fails the pass-through proof closed downstream; malformed
// durable policy JSON fails the query like any undecodable fact.
func loadALegPassThroughPoliciesTx(ctx context.Context, q bun.IDB, accountID string, callIDs []string) (map[string]billing.VersionRef, error) {
	out := make(map[string]billing.VersionRef, len(callIDs))
	if len(callIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(callIDs)+1)
	args = append(args, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	var rows []struct {
		CallID          string `bun:"call_id"`
		ChargePolicyRef string `bun:"charge_policy_ref"`
	}
	if err := q.NewRaw(`SELECT call_id, charge_policy_ref FROM call_exposures WHERE account_id = ? AND call_id IN (`+sqlPlaceholders(len(callIDs))+`)`, args...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: query A-leg pass-through policies: %w", err)
	}
	for _, row := range rows {
		var policy billing.VersionRef
		if err := json.Unmarshal([]byte(row.ChargePolicyRef), &policy); err != nil {
			return nil, fmt.Errorf("billingstore: decode A-leg pass-through policy: %w", err)
		}
		out[row.CallID] = policy
	}
	return out, nil
}

// alegPassThroughSnapshotChunkRow mirrors operationSnapshotRow plus the
// per-source window position for bounded retention.
type alegPassThroughSnapshotChunkRow struct {
	operationSnapshotRow
	Rn int `bun:"rn"`
}

// alegCorePassThroughHead adapts one durable head row to the single core
// pass-through authority input. The shell owns SQL; all proof lives in core.
func alegCorePassThroughHead(row costPassThroughHeadRow) billing.ALegPassThroughHead {
	return billing.ALegPassThroughHead{
		AccountID: row.AccountID, CallID: row.CallID, ALegID: row.ALegID,
		SettlementOperationKey: row.SettlementOperationKey, OriginalTransactionID: row.OriginalTransactionID,
		PolicyRef:           billing.VersionRef{ID: row.PolicyID, Version: row.PolicyVersion},
		MissingCost:         billing.CostPassThroughMissingCostPolicy(row.MissingCost),
		SafeBound:           billing.Money{Nano: row.SafeBoundNano, Currency: row.Currency},
		AllowLateAdjustment: row.AllowLateAdjustment,
		Status:              billing.CostPassThroughSettlementStatus(row.Status),
		PostedAmount:        billing.Money{Nano: row.PostedAmountNano, Currency: row.Currency},
		ProviderLURKey:      row.ProviderLURKey, ProviderValuationID: row.ProviderValuationID,
		ProviderRevision: uint64(max(row.ProviderRevision, 0)), ProviderInputHash: row.ProviderInputHash,
		HeadVersion: row.HeadVersion, Fence: row.Fence, SettlementFingerprint: row.SettlementFingerprint,
	}
}

// alegCorePassThroughSnapshot adapts one durable snapshot row to the core
// pass-through authority input.
func alegCorePassThroughSnapshot(row operationSnapshotRow) billing.ALegPassThroughSnapshot {
	return billing.ALegPassThroughSnapshot{
		OperationKey: row.OperationKey, SourceKey: row.SourceKey,
		Fingerprint: row.Fingerprint, IntegrityFingerprint: row.IntegrityFingerprint,
		Currency: row.Currency, Mode: row.Mode,
		Before: billing.AccountSnapshot{BalanceNano: row.BalanceBefore, SpendableNano: row.SpendableBefore,
			CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit,
			Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionBefore},
		After: billing.AccountSnapshot{BalanceNano: row.BalanceAfter, SpendableNano: row.SpendableAfter,
			CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit,
			Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionAfter},
		SequenceStart: row.SequenceStart, SequenceEnd: row.SequenceEnd,
	}
}

// composeALegPassThrough layers trusted pass-through authority onto one
// retail-known verdict. A retail-known call with no pass-through
// involvement returns unchanged; complete validated lineage keeps it
// known with that lineage attached; incomplete evidence demotes it to
// pending and conflicting evidence to unknown. Validated lineage never
// touches retail totals: the caller adds only the retail charge.
func composeALegPassThrough(scope billing.ALegAuthorityScope, retail billing.ALegCallVerdict, retailIssues []billing.ReconciliationIssue, markers []billing.ALegMarker, head costPassThroughHeadRow, hasHead bool, policies map[string]billing.VersionRef, snapshots map[string][]operationSnapshotRow, journals []billing.JournalTransaction) (billing.ALegCallVerdict, []billing.ReconciliationIssue, error) {
	// Retail known implies exactly one selected marker carrying the
	// settlement fingerprint and telescoping base. Anything else is a
	// defensive no-op: the retail verdict stands on its own proof.
	if len(markers) != 1 {
		return retail, retailIssues, nil
	}
	var coreHead *billing.ALegPassThroughHead
	var policy billing.VersionRef
	if hasHead {
		adapted := alegCorePassThroughHead(head)
		coreHead = &adapted
		policy = policies[scope.CallID]
	}
	seen := make(map[string]bool)
	var coreSnapshots []billing.ALegPassThroughSnapshot
	for _, journal := range journals {
		if journal.OperationKind != billing.CostPassThroughAdjustmentOperationKind || journal.TurnID != scope.CallID || seen[journal.SourceKey] {
			continue
		}
		seen[journal.SourceKey] = true
		for _, row := range snapshots[journal.SourceKey] {
			coreSnapshots = append(coreSnapshots, alegCorePassThroughSnapshot(row))
		}
	}
	verdict, issues, err := billing.EvaluateALegPassThroughAuthority(scope, billing.ALegPassThroughFacts{
		Head: coreHead, Policy: policy, Marker: markers[0], HasMarker: true,
		Snapshots: coreSnapshots, Journals: journals,
	})
	if err != nil {
		return billing.ALegCallVerdict{}, nil, err
	}
	switch verdict.Status {
	case billing.ALegPassThroughNone:
		return retail, retailIssues, nil
	case billing.ALegPassThroughKnown:
		retail.Adjustments = verdict.Adjustments
		return retail, retailIssues, nil
	case billing.ALegPassThroughPending:
		return billing.ALegCallVerdict{Status: billing.ALegCallPending, Charge: billing.Money{Currency: scope.Currency}, Adjustments: verdict.Adjustments}, issues, nil
	default:
		return billing.ALegCallVerdict{Status: billing.ALegCallUnknown, Charge: billing.Money{Currency: scope.Currency}, Adjustments: verdict.Adjustments}, issues, nil
	}
}

// alegProviderLegRow carries one scope leg identity with its durable
// outcome for provider enumeration. Outcomes ride the row so no
// payload decode is needed for the provider plane.
type alegProviderLegRow struct {
	CallID  string `bun:"call_id"`
	BLegID  string `bun:"b_leg_id"`
	Outcome string `bun:"outcome"`
}

// alegProviderLegChunkRow mirrors alegProviderLegRow plus the per-call
// window position for bounded retention.
type alegProviderLegChunkRow struct {
	alegProviderLegRow
	Rn int `bun:"rn"`
}

// loadALegProviderLegsTx enumerates scope legs for one chunk with the
// same scope predicates as the leg page, capped per call so fact
// loading stays bounded independent of durable fan-out. Calls beyond
// the cap resolve provider-unknown with a fanout issue instead of
// contributing partial totals. perCallCap must cover the page limit so
// every surfaced page leg always has a verdict.
func loadALegProviderLegsTx(ctx context.Context, q bun.IDB, accountID, aLegID string, callIDs []string, perCallCap int) (map[string][]alegProviderLegRow, map[string]bool, error) {
	out := make(map[string][]alegProviderLegRow, len(callIDs))
	overCap := make(map[string]bool)
	if len(callIDs) == 0 {
		return out, overCap, nil
	}
	args := make([]any, 0, len(callIDs)+3)
	args = append(args, accountID, aLegID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	args = append(args, perCallCap+1)
	var rows []alegProviderLegChunkRow
	if err := q.NewRaw(`SELECT call_id, b_leg_id, outcome FROM (SELECT l.call_id, l.b_leg_id, l.outcome, ROW_NUMBER() OVER (PARTITION BY l.call_id ORDER BY l.b_leg_id) AS rn FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id WHERE c.account_id = ? AND l.a_leg_id = ? AND l.call_id IN (`+sqlPlaceholders(len(callIDs))+`)) WHERE rn <= ?`, args...).Scan(ctx, &rows); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider legs: %w", err)
	}
	counts := make(map[string]int, len(callIDs))
	for _, row := range rows {
		counts[row.CallID]++
		if counts[row.CallID] > perCallCap {
			overCap[row.CallID] = true
			continue
		}
		out[row.CallID] = append(out[row.CallID], row.alegProviderLegRow)
	}
	return out, overCap, nil
}

// providerHeadOverCap and providerFenceOverCap bound durable rows per
// head/fence identity: at most one head per subject and one fence per
// lineage is resolvable, so cap+1 rows per identity prove overflow
// deterministically. The values stay in sync with the core duplicate
// and ambiguity rules they protect.
const (
	providerHeadOverCap  = 1
	providerFenceOverCap = 1
)

// providerLegTotalCap bounds durable rows per B-leg across the
// provider fact tables: at most this many provider-charge children
// per leg are resolvable (in sync with the core child cap it
// protects), so every provider loader returns at most cap+1 rows
// per exact (call, B-leg) scope before Scan. The ninth row is the
// explicit overflow probe: its presence marks the leg unknown with
// a fanout issue, never a partial subtotal.
const providerLegTotalCap = 8

// providerWorkLegOverCap bounds work rows per leg key: exactly one
// work row exists per leg (UNIQUE usage_leg_key), so cap+1 rows
// prove drift deterministically.
const providerWorkLegOverCap = 1

// alegProviderHeadChunkRow mirrors providerCostHeadRow plus the
// per-leg window position for bounded retention. LegRn is the
// explicit overflow probe: the SQL leg window numbers rows 1..N per
// (call, B-leg), so LegRn == providerLegTotalCap+1 proves more than
// a full leg exists without materializing it.
type alegProviderHeadChunkRow struct {
	providerCostHeadRow
	LegRn int `bun:"leg_rn"`
}

// alegProviderPostingFenceChunkRow mirrors providerCostPostingFenceRow
// plus the per-leg window position for bounded retention.
type alegProviderPostingFenceChunkRow struct {
	providerCostPostingFenceRow
	LegRn int `bun:"leg_rn"`
}

// alegProviderExecutionFenceChunkRow mirrors
// providerCostExecutionFenceRow plus the per-leg window position
// for bounded retention.
type alegProviderExecutionFenceChunkRow struct {
	providerCostExecutionFenceRow
	LegRn int `bun:"leg_rn"`
}

// providerHeadBLEGExpr extracts the owning B-leg ID from a stored
// provider subject payload for the per-leg SQL window. SQLite reads
// the JSON document directly; PostgreSQL casts the TEXT column
// (writer JSON is always valid; a cast failure fails the scope
// query closed, exactly like an undecodable subject in Go).
// Missing keys coalesce to empty and group into one bounded
// pseudo-leg that downstream decoding fails closed. Core stays
// untouched: this is shell SQL only.
func providerHeadBLEGExpr(pg bool, alias string) string {
	if pg {
		return `COALESCE(` + alias + `.subject_json::jsonb ->> 'b_leg_id', '')`
	}
	return `COALESCE(json_extract(` + alias + `.subject_json, '$.b_leg_id'), '')`
}

// providerLineageBLEGExpr extracts the owning B-leg ID from a
// `call:b-leg[:provider-charge:id]` lineage for the per-leg SQL
// window. B-leg IDs never contain ':', and neither do call IDs, so
// the B-leg is the segment after `call_id + ':'` up to the next ':'
// (or end of key). substr/length are portable; only the
// first-segment split differs per dialect. Lineages outside the
// call scope yield garbage segments that group into bounded
// pseudo-legs which the retained-leg filter drops.
func providerLineageBLEGExpr(pg bool, alias, lineageCol, callCol string) string {
	suffix := `substr(` + alias + `.` + lineageCol + `, length(` + alias + `.` + callCol + `) + 2)`
	if pg {
		return `COALESCE(split_part(` + suffix + `, ':', 1), '')`
	}
	return `COALESCE(substr(` + suffix + `, 1, instr(` + suffix + ` || ':', ':') - 1), '')`
}

// providerSubjectKeyCountExpr counts top-level b_leg_id keys in a
// stored provider subject payload for duplicate-key detection.
// SQLite json_extract reads the first duplicate while Go decoding
// reads the last, so cardinality other than exactly one is
// malformed regardless of which side extraction would attribute.
// PostgreSQL json_each walks the preserved json document (jsonb
// would hide duplicates; json_each on the json cast emits every
// top-level pair, proven by the duplicate-order tests), so both
// dialects agree. Malformed JSON fails the scope query closed
// here exactly as in the main loaders.
func providerSubjectKeyCountExpr(pg bool, alias string) string {
	if pg {
		return `(SELECT COUNT(*) FROM json_each(` + alias + `.subject_json::json) WHERE key = 'b_leg_id')`
	}
	return `(SELECT COUNT(*) FROM json_each(` + alias + `.subject_json) WHERE key = 'b_leg_id')`
}

// providerLegEnumerationExists returns an EXISTS predicate proving
// the extracted B-leg names an enumerated leg of the exact report
// scope (same call, account, and A-leg). It powers the fail-closed
// anomaly markers below: evidence that cannot be attributed to an
// enumerated leg can never be proven, so its call resolves unknown.
func providerLegEnumerationExists(callRef, blegRef string) string {
	return `EXISTS (SELECT 1 FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id` +
		` WHERE l.call_id = ` + callRef + ` AND c.account_id = ? AND l.a_leg_id = ? AND l.b_leg_id = ` + blegRef + `)`
}

// alegProviderCallAnomaly carries one poisoned call identity from an
// anomaly marker query.
type alegProviderCallAnomaly struct {
	CallID string `bun:"call_id"`
}

// alegProviderLegAnomaly carries one poisoned leg identity (call plus
// enumerated B-leg) from an anomaly marker query.
type alegProviderLegAnomaly struct {
	CallID string `bun:"call_id"`
	BLegID string `bun:"bleg"`
}

// loadALegProviderHeadAnomaliesTx marks calls with head rows that
// cannot be attributed to an enumerated B-leg of the exact scope
// (missing, malformed, or foreign B-leg in the stored subject) or
// whose top-level b_leg_id key cardinality is not exactly one.
// The cardinality rule closes the duplicate-key divergence:
// SQLite extraction reads the first duplicate while Go decoding
// reads the last, so a valid-then-foreign subject would evade
// attribution and decode foreign. Attributable single-key heads
// are always evaluated, where the core fails them closed per
// unit; only unattributable or ambiguous heads are silent, so
// only they poison. One DISTINCT row per call bounds the result
// no matter how many pseudo-leg identities flood the table.
func loadALegProviderHeadAnomaliesTx(ctx context.Context, q bun.IDB, storeID, accountID, aLegID string, callIDs []string) (map[string]bool, error) {
	out := make(map[string]bool)
	if len(callIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(callIDs)+4)
	args = append(args, storeID, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	args = append(args, accountID, aLegID)
	pg := q.Dialect().Name() == dialect.PG
	bleg := providerHeadBLEGExpr(pg, "h")
	keyCount := providerSubjectKeyCountExpr(pg, "h")
	var rows []alegProviderCallAnomaly
	if err := q.NewRaw(`SELECT DISTINCT h.call_id FROM billing_provider_cost_heads h WHERE h.store_id = ? AND h.account_id = ? AND h.call_id IN (`+sqlPlaceholders(len(callIDs))+`) AND (NOT (`+providerLegEnumerationExists("h.call_id", bleg)+`) OR `+keyCount+` <> 1)`, args...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: query A-leg provider head anomalies: %w", err)
	}
	for _, row := range rows {
		out[row.CallID] = true
	}
	return out, nil
}

// loadALegProviderPostingAnomaliesTx marks unattributable posting
// fences (whole-call poison, as above) plus attributable fences
// with malformed lineages (exact-leg poison). A well-formed posting
// lineage is exactly the `call:b-leg` base or that base plus
// `:provider-charge:` with a non-empty charge; anything else naming
// a real leg is drift that retained-leg filtering would silently
// drop. Leg results name enumerated legs only, so flooding
// pseudo-leg identities cannot widen the result beyond the bounded
// enumerated leg population.
func loadALegProviderPostingAnomaliesTx(ctx context.Context, q bun.IDB, storeID, accountID, aLegID string, callIDs []string) (map[string]bool, map[string]bool, error) {
	callPoison := make(map[string]bool)
	legPoison := make(map[string]bool)
	if len(callIDs) == 0 {
		return callPoison, legPoison, nil
	}
	pg := q.Dialect().Name() == dialect.PG
	scopeArgs := make([]any, 0, len(callIDs)+2)
	scopeArgs = append(scopeArgs, storeID, accountID)
	for _, callID := range callIDs {
		scopeArgs = append(scopeArgs, callID)
	}
	var unattributed []alegProviderCallAnomaly
	bleg := providerLineageBLEGExpr(pg, "f", "lineage_key", "call_id")
	if err := q.NewRaw(`SELECT DISTINCT f.call_id FROM billing_provider_cost_posting_fences f WHERE f.store_id = ? AND f.account_id = ? AND f.call_id IN (`+sqlPlaceholders(len(callIDs))+`) AND NOT (`+providerLegEnumerationExists("f.call_id", bleg)+`)`, append(scopeArgs, accountID, aLegID)...).Scan(ctx, &unattributed); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider posting-fence call anomalies: %w", err)
	}
	for _, row := range unattributed {
		callPoison[row.CallID] = true
	}
	var malformed []alegProviderLegAnomaly
	if err := q.NewRaw(`SELECT DISTINCT scope.call_id, scope.bleg FROM (SELECT f.call_id AS call_id, `+bleg+` AS bleg, f.lineage_key AS lineage_key FROM billing_provider_cost_posting_fences f WHERE f.store_id = ? AND f.account_id = ? AND f.call_id IN (`+sqlPlaceholders(len(callIDs))+`)) AS scope WHERE `+providerLegEnumerationExists("scope.call_id", "scope.bleg")+` AND scope.lineage_key <> scope.call_id || ':' || scope.bleg AND (substr(scope.lineage_key, 1, length(scope.call_id || ':' || scope.bleg) + 17) <> (scope.call_id || ':' || scope.bleg || ':provider-charge:') OR length(scope.lineage_key) = length(scope.call_id || ':' || scope.bleg) + 17)`, append(scopeArgs, accountID, aLegID)...).Scan(ctx, &malformed); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider posting-fence leg anomalies: %w", err)
	}
	for _, row := range malformed {
		legPoison[row.CallID+"\x00"+row.BLegID] = true
	}
	return callPoison, legPoison, nil
}

// loadALegProviderExecutionAnomaliesTx marks unattributable
// execution fences (whole-call poison) plus attributable fences
// outside the bare `call:b-leg` form (exact-leg poison). The
// execution gate is always exactly the base key; a child-suffixed
// or otherwise decorated lineage naming a real leg is drift the
// core would ignore while retained-leg filtering drops.
func loadALegProviderExecutionAnomaliesTx(ctx context.Context, q bun.IDB, storeID, accountID, aLegID string, callIDs []string) (map[string]bool, map[string]bool, error) {
	callPoison := make(map[string]bool)
	legPoison := make(map[string]bool)
	if len(callIDs) == 0 {
		return callPoison, legPoison, nil
	}
	pg := q.Dialect().Name() == dialect.PG
	scopeArgs := make([]any, 0, len(callIDs)+2)
	scopeArgs = append(scopeArgs, storeID, accountID)
	for _, callID := range callIDs {
		scopeArgs = append(scopeArgs, callID)
	}
	var unattributed []alegProviderCallAnomaly
	bleg := providerLineageBLEGExpr(pg, "f", "execution_lineage_key", "call_id")
	if err := q.NewRaw(`SELECT DISTINCT f.call_id FROM billing_provider_cost_execution_fences f WHERE f.store_id = ? AND f.account_id = ? AND f.call_id IN (`+sqlPlaceholders(len(callIDs))+`) AND NOT (`+providerLegEnumerationExists("f.call_id", bleg)+`)`, append(scopeArgs, accountID, aLegID)...).Scan(ctx, &unattributed); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider execution-fence call anomalies: %w", err)
	}
	for _, row := range unattributed {
		callPoison[row.CallID] = true
	}
	var malformed []alegProviderLegAnomaly
	if err := q.NewRaw(`SELECT DISTINCT scope.call_id, scope.bleg FROM (SELECT f.call_id AS call_id, `+bleg+` AS bleg, f.execution_lineage_key AS lineage_key FROM billing_provider_cost_execution_fences f WHERE f.store_id = ? AND f.account_id = ? AND f.call_id IN (`+sqlPlaceholders(len(callIDs))+`)) AS scope WHERE `+providerLegEnumerationExists("scope.call_id", "scope.bleg")+` AND scope.lineage_key <> scope.call_id || ':' || scope.bleg`, append(scopeArgs, accountID, aLegID)...).Scan(ctx, &malformed); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider execution-fence leg anomalies: %w", err)
	}
	for _, row := range malformed {
		legPoison[row.CallID+"\x00"+row.BLegID] = true
	}
	return callPoison, legPoison, nil
}

// loadALegProviderWorkAnomaliesTx marks calls with work rows keyed
// outside the enumerated leg population. Work keys are exact
// `call:b-leg` usage keys (UNIQUE), so attribution is an exact key
// match: anything else is drift no evaluated leg can consume. One
// DISTINCT row per call bounds the result.
func loadALegProviderWorkAnomaliesTx(ctx context.Context, q bun.IDB, accountID, aLegID string, callIDs []string) (map[string]bool, error) {
	out := make(map[string]bool)
	if len(callIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, 2*len(callIDs)+3)
	args = append(args, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	args = append(args, accountID, aLegID)
	var rows []alegProviderCallAnomaly
	if err := q.NewRaw(`SELECT DISTINCT w.call_id FROM provider_cost_work w WHERE w.call_id IN (SELECT call_id FROM usage_call_records WHERE account_id = ? AND call_id IN (`+sqlPlaceholders(len(callIDs))+`)) AND NOT EXISTS (SELECT 1 FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id WHERE l.call_id = w.call_id AND c.account_id = ? AND l.a_leg_id = ? AND l.call_id || ':' || l.b_leg_id = w.usage_leg_key)`, args...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: query A-leg provider work anomalies: %w", err)
	}
	for _, row := range rows {
		out[row.CallID] = true
	}
	return out, nil
}

// loadALegProviderHeadsTx loads current selected-cost heads for one
// chunk with two deterministic pre-Scan windows computed over the
// full scope in a single pass: a per-identity cap+1 window (at most
// one head per exact subject identity is resolvable) for ambiguity
// probing, alongside a total per-(call, B-leg) cap+1 window that
// bounds every leg to providerLegTotalCap+1 rows before Scan. The
// ninth row of a leg is the explicit overflow probe. Rows return as
// stored; the core evaluator fails them closed.
func loadALegProviderHeadsTx(ctx context.Context, q bun.IDB, storeID, accountID string, callIDs []string) (map[string][]providerCostHeadRow, map[string]bool, error) {
	out := make(map[string][]providerCostHeadRow, len(callIDs))
	overCap := make(map[string]bool)
	if len(callIDs) == 0 {
		return out, overCap, nil
	}
	args := make([]any, 0, len(callIDs)+4)
	args = append(args, storeID, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	args = append(args, providerHeadOverCap+1, providerLegTotalCap+1)
	bleg := providerHeadBLEGExpr(q.Dialect().Name() == dialect.PG, "h")
	var rows []alegProviderHeadChunkRow
	if err := q.NewRaw(`SELECT id, store_id, account_id, call_id, head_key, subject_kind, subject_id, subject_json, evidence_revision, input_set_hash, valuation_id, amount_nano, currency, head_version, fence, last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix, leg_rn FROM (SELECT h.*, ROW_NUMBER() OVER (PARTITION BY h.call_id, h.subject_kind, h.subject_id, h.subject_json ORDER BY h.head_key) AS identity_rn, ROW_NUMBER() OVER (PARTITION BY h.call_id, `+bleg+` ORDER BY h.head_key) AS leg_rn FROM billing_provider_cost_heads h WHERE h.store_id = ? AND h.account_id = ? AND h.call_id IN (`+sqlPlaceholders(len(callIDs))+`)) WHERE identity_rn <= ? AND leg_rn <= ?`, args...).Scan(ctx, &rows); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider heads: %w", err)
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		if row.LegRn < 1 || row.LegRn > providerLegTotalCap+1 {
			return nil, nil, fmt.Errorf("billingstore: A-leg provider head leg window violated")
		}
		// The Go partition mirrors the SQL identity window
		// exactly: the stored subject JSON carries the owning
		// B-leg, so the same charge ID on distinct legs never
		// collides here. The total per-leg bound already held in
		// SQL (at most cap+1 rows per leg reach this loop);
		// groupALegProviderHeads below re-derives it from decoded
		// subjects as a dialect-proof backstop.
		partition := row.CallID + "\x00" + row.SubjectKind + "\x00" + row.SubjectID + "\x00" + row.SubjectJSON
		counts[partition]++
		if counts[partition] > providerHeadOverCap {
			overCap[row.CallID] = true
			continue
		}
		out[row.CallID] = append(out[row.CallID], row.providerCostHeadRow)
	}
	return out, overCap, nil
}

// groupALegProviderHeads partitions chunk-loaded heads by exact
// identity (call, owning B-leg, subject kind, subject charge) decoded
// from the stored subject. The SQL leg window already bounds every
// leg to providerLegTotalCap+1 rows, so nine materialized rows for a
// leg prove overflow exactly (the ninth is the probe); this layer
// re-derives the same bound from decoded subjects as a dialect-proof
// backstop (e.g. if B-leg extraction ever degraded, counts still
// fail the right leg closed). Any leg beyond the bound, or with a
// duplicated exact identity, marks head-over-cap and keeps no rows
// downstream, so over-cap legs resolve unknown with a fanout issue
// and contribute no derived facts. Legs within the bound keep every
// head. Undecodable subjects fail the query like any undecodable
// durable fact.
func groupALegProviderHeads(heads map[string][]providerCostHeadRow) (map[string]map[string][]providerCostHeadRow, map[string]bool, error) {
	byLeg := make(map[string]map[string][]providerCostHeadRow, len(heads))
	overCap := make(map[string]bool)
	for callID, rows := range heads {
		type decodedHead struct {
			row    providerCostHeadRow
			legKey string
		}
		var decoded []decodedHead
		perLeg := make(map[string]int)
		perIdentity := make(map[string]int)
		for _, row := range rows {
			var subject metering.SubjectRef
			if err := json.Unmarshal([]byte(row.SubjectJSON), &subject); err != nil {
				return nil, nil, fmt.Errorf("billingstore: decode A-leg provider head subject: %w", err)
			}
			legKey := callID + "\x00" + subject.BLegID
			identity := legKey + "\x00" + string(subject.Kind) + "\x00" + subject.ProviderChargeID
			decoded = append(decoded, decodedHead{row: row, legKey: legKey})
			perLeg[legKey]++
			perIdentity[identity]++
			if perLeg[legKey] > providerLegTotalCap || perIdentity[identity] > 1 {
				overCap[legKey] = true
			}
		}
		for _, head := range decoded {
			if overCap[head.legKey] {
				continue
			}
			legs, ok := byLeg[callID]
			if !ok {
				legs = make(map[string][]providerCostHeadRow)
				byLeg[callID] = legs
			}
			legs[head.legKey] = append(legs[head.legKey], head.row)
		}
	}
	return byLeg, overCap, nil
}

// loadALegProviderPostingFencesTx loads posting fences for one chunk
// with two deterministic pre-Scan windows: a per-lineage cap+1
// window (at most one fence per lineage is resolvable) for ambiguity
// probing, alongside a total per-(call, B-leg) cap+1 window bounding
// every leg to providerLegTotalCap+1 rows before Scan. The B-leg
// comes from the `call:b-leg[:provider-charge:id]` lineage, so
// cross-leg same IDs never collide while cross-kind ambiguity still
// probes per lineage. Fence verdicts ride the head bound; this
// window bounds materialization and its probe rows never reach
// evaluation for over-cap legs.
func loadALegProviderPostingFencesTx(ctx context.Context, q bun.IDB, storeID, accountID string, callIDs []string) (map[string][]providerCostPostingFenceRow, map[string]bool, error) {
	out := make(map[string][]providerCostPostingFenceRow, len(callIDs))
	overCap := make(map[string]bool)
	if len(callIDs) == 0 {
		return out, overCap, nil
	}
	args := make([]any, 0, len(callIDs)+4)
	args = append(args, storeID, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	args = append(args, providerFenceOverCap+1, providerLegTotalCap+1)
	bleg := providerLineageBLEGExpr(q.Dialect().Name() == dialect.PG, "f", "lineage_key", "call_id")
	var rows []alegProviderPostingFenceChunkRow
	if err := q.NewRaw(`SELECT id, store_id, account_id, call_id, lineage_key, authority, head_key, evidence_revision, input_set_hash, fingerprint, amount_nano, currency, fence, last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix, leg_rn FROM (SELECT f.*, ROW_NUMBER() OVER (PARTITION BY f.call_id, f.lineage_key ORDER BY f.head_key) AS identity_rn, ROW_NUMBER() OVER (PARTITION BY f.call_id, `+bleg+` ORDER BY f.head_key) AS leg_rn FROM billing_provider_cost_posting_fences f WHERE f.store_id = ? AND f.account_id = ? AND f.call_id IN (`+sqlPlaceholders(len(callIDs))+`)) WHERE identity_rn <= ? AND leg_rn <= ?`, args...).Scan(ctx, &rows); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider posting fences: %w", err)
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		if row.LegRn < 1 || row.LegRn > providerLegTotalCap+1 {
			return nil, nil, fmt.Errorf("billingstore: A-leg provider posting fence leg window violated")
		}
		partition := row.CallID + "\x00" + row.LineageKey
		counts[partition]++
		if counts[partition] > providerFenceOverCap {
			overCap[row.CallID] = true
			continue
		}
		out[row.CallID] = append(out[row.CallID], row.providerCostPostingFenceRow)
	}
	return out, overCap, nil
}

// loadALegProviderExecutionFencesTx loads execution fences for one
// chunk with two deterministic pre-Scan windows mirroring the
// posting-fence bound: per-lineage ambiguity probing alongside a
// total per-(call, B-leg) cap+1 window. The execution lineage is the
// bare `call:b-leg` key, so the B-leg extracts with portable
// substr/length through the shared lineage helper.
func loadALegProviderExecutionFencesTx(ctx context.Context, q bun.IDB, storeID, accountID string, callIDs []string) (map[string][]providerCostExecutionFenceRow, map[string]bool, error) {
	out := make(map[string][]providerCostExecutionFenceRow, len(callIDs))
	overCap := make(map[string]bool)
	if len(callIDs) == 0 {
		return out, overCap, nil
	}
	args := make([]any, 0, len(callIDs)+4)
	args = append(args, storeID, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	args = append(args, providerFenceOverCap+1, providerLegTotalCap+1)
	bleg := providerLineageBLEGExpr(q.Dialect().Name() == dialect.PG, "f", "execution_lineage_key", "call_id")
	var rows []alegProviderExecutionFenceChunkRow
	if err := q.NewRaw(`SELECT id, store_id, account_id, call_id, execution_lineage_key, authority, owner_subject_kind, owner_head_key, owner_revision, owner_input_set_hash, owner_fingerprint, fence, last_operation_key, last_transaction_id, created_at_unix, updated_at_unix, leg_rn FROM (SELECT f.*, ROW_NUMBER() OVER (PARTITION BY f.call_id, f.execution_lineage_key ORDER BY f.owner_head_key) AS identity_rn, ROW_NUMBER() OVER (PARTITION BY f.call_id, `+bleg+` ORDER BY f.owner_head_key) AS leg_rn FROM billing_provider_cost_execution_fences f WHERE f.store_id = ? AND f.account_id = ? AND f.call_id IN (`+sqlPlaceholders(len(callIDs))+`)) WHERE identity_rn <= ? AND leg_rn <= ?`, args...).Scan(ctx, &rows); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider execution fences: %w", err)
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		if row.LegRn < 1 || row.LegRn > providerLegTotalCap+1 {
			return nil, nil, fmt.Errorf("billingstore: A-leg provider execution fence leg window violated")
		}
		partition := row.CallID + "\x00" + row.ExecutionLineage
		counts[partition]++
		if counts[partition] > providerFenceOverCap {
			overCap[row.CallID] = true
			continue
		}
		out[row.CallID] = append(out[row.CallID], row.providerCostExecutionFenceRow)
	}
	return out, overCap, nil
}

type alegProviderWorkState struct {
	Status   string
	Attempts int
}

// loadALegProviderWorkTx loads provider-cost work states for one chunk
// with two deterministic pre-Scan windows: the existing per-call
// cap+1 window over the call's legs, plus a per-leg-key cap+1 window
// (exactly one work row exists per leg key by UNIQUE usage_leg_key,
// so a second row proves drift). The evaluator treats any
// non-processed state as outstanding evidence.
func loadALegProviderWorkTx(ctx context.Context, q bun.IDB, accountID string, callIDs []string, perCallCap int) (map[string]alegProviderWorkState, map[string]bool, error) {
	out := make(map[string]alegProviderWorkState)
	overCap := make(map[string]bool)
	if len(callIDs) == 0 {
		return out, overCap, nil
	}
	args := make([]any, 0, len(callIDs)+3)
	args = append(args, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	args = append(args, perCallCap+1, providerWorkLegOverCap+1)
	var rows []struct {
		CallID   string `bun:"call_id"`
		LegKey   string `bun:"usage_leg_key"`
		Status   string `bun:"status"`
		Attempts int    `bun:"attempt_count"`
		LegRn    int    `bun:"leg_rn"`
	}
	if err := q.NewRaw(`SELECT call_id, usage_leg_key, status, attempt_count, leg_rn FROM (SELECT w.call_id, w.usage_leg_key, w.status, w.attempt_count, ROW_NUMBER() OVER (PARTITION BY w.call_id ORDER BY w.usage_leg_key) AS rn, ROW_NUMBER() OVER (PARTITION BY w.call_id, w.usage_leg_key ORDER BY w.usage_leg_key) AS leg_rn FROM provider_cost_work w WHERE w.call_id IN (SELECT call_id FROM usage_call_records WHERE account_id = ? AND call_id IN (`+sqlPlaceholders(len(callIDs))+`))) WHERE rn <= ? AND leg_rn <= ?`, args...).Scan(ctx, &rows); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg provider work: %w", err)
	}
	counts := make(map[string]int, len(callIDs))
	legCounts := make(map[string]int, len(rows))
	for _, row := range rows {
		if row.LegRn < 1 || row.LegRn > providerWorkLegOverCap+1 {
			return nil, nil, fmt.Errorf("billingstore: A-leg provider work leg window violated")
		}
		counts[row.CallID]++
		if counts[row.CallID] > perCallCap {
			overCap[row.CallID] = true
			continue
		}
		legPartition := row.CallID + "\x00" + row.LegKey
		legCounts[legPartition]++
		if legCounts[legPartition] > providerWorkLegOverCap {
			overCap[row.CallID] = true
			continue
		}
		out[row.LegKey] = alegProviderWorkState{Status: row.Status, Attempts: row.Attempts}
	}
	return out, overCap, nil
}

// loadALegProviderRevisionStateTx loads pending V2 revision work for
// chunk head keys: one bounded statement per key batch. Any
// non-completed state means a newer revision is in flight for that
// lineage, so the current head is not yet provable.
func loadALegProviderRevisionStateTx(ctx context.Context, q bun.IDB, storeID string, headKeys []string) (map[string]bool, error) {
	out := make(map[string]bool)
	if len(headKeys) == 0 {
		return out, nil
	}
	seen := make(map[string]bool, len(headKeys))
	var keys []string
	for _, key := range headKeys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	for start := 0; start < len(keys); {
		end := start + alegReportScopeChunkSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[start:end]
		args := make([]any, 0, len(batch)+2)
		args = append(args, storeID, string(billing.EconomicQueueProvider))
		for _, key := range batch {
			args = append(args, key)
		}
		var rows []struct {
			HeadKey string `bun:"head_key"`
		}
		if err := q.NewRaw(`SELECT head_key FROM billing_economic_revision_work_state WHERE store_id = ? AND queue = ? AND status <> 'completed' AND head_key IN (`+sqlPlaceholders(len(batch))+`)`, args...).Scan(ctx, &rows); err != nil {
			return nil, fmt.Errorf("billingstore: query A-leg provider revision state: %w", err)
		}
		for _, row := range rows {
			out[row.HeadKey] = true
		}
		start = end
	}
	return out, nil
}

// loadALegProviderSnapshotsTx loads canonical provider operation
// snapshots for chunk journal identities in chunk-bounded batches with
// per-operation retention caps, so ambiguity stays detectable and
// memory stays bounded.
func loadALegProviderSnapshotsTx(ctx context.Context, q bun.IDB, accountID string, operationKeys []string) (map[string][]operationSnapshotRow, error) {
	out := make(map[string][]operationSnapshotRow)
	if len(operationKeys) == 0 {
		return out, nil
	}
	seen := make(map[string]bool, len(operationKeys))
	var keys []string
	for _, key := range operationKeys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	for start := 0; start < len(keys); {
		end := start + alegReportScopeChunkSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[start:end]
		args := make([]any, 0, len(batch)+3)
		args = append(args, accountID, "provider_call_cogs")
		for _, key := range batch {
			args = append(args, key)
		}
		args = append(args, alegReportMaxSnapshotsPerSource)
		var rows []alegPassThroughSnapshotChunkRow
		if err := q.NewRaw(`SELECT operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at FROM (SELECT s.*, ROW_NUMBER() OVER (PARTITION BY s.operation_key ORDER BY s.operation_key) AS rn FROM billing_operation_snapshots s WHERE s.account_id = ? AND s.operation_kind = ? AND s.operation_key IN (`+sqlPlaceholders(len(batch))+`)) WHERE rn <= ?`, args...).Scan(ctx, &rows); err != nil {
			return nil, fmt.Errorf("billingstore: query A-leg provider snapshots: %w", err)
		}
		for _, row := range rows {
			out[row.OperationKey] = append(out[row.OperationKey], row.operationSnapshotRow)
		}
		start = end
	}
	return out, nil
}

// retainALegProviderFacts narrows chunk-loaded provider facts to legs
// that still resolve: calls already over cap drop entirely, and only
// enumerated legs surviving the head bound keep heads, posting
// fences, and journals. Journal rows without a B-leg are call-scoped
// evidence and stay. Execution fences and work states are already
// per-lineage/per-call capped and consulted per leg only, so
// discarded legs never touch them either. Derived snapshot and
// revision loads below consume only these retained maps, so no facts
// are derived for discarded rows.
func retainALegProviderFacts(chunkIDs []string, legs map[string][]alegProviderLegRow, headsByLeg map[string]map[string][]providerCostHeadRow, fences map[string][]providerCostPostingFenceRow, journals map[string][]billing.JournalTransaction, callOverCap, legOverCap map[string]bool) (map[string][]providerCostHeadRow, map[string][]providerCostPostingFenceRow, map[string][]billing.JournalTransaction, error) {
	okHeads := make(map[string][]providerCostHeadRow)
	okFences := make(map[string][]providerCostPostingFenceRow)
	okJournals := make(map[string][]billing.JournalTransaction)
	for _, callID := range chunkIDs {
		if callOverCap[callID] {
			continue
		}
		parsed, err := billing.ParseBillingCallID(callID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("billing: A-leg provider call identity: %w", err)
		}
		retainedLegs := make(map[string]bool)
		legKeys := make(map[string]bool)
		for _, leg := range legs[callID] {
			if legOverCap[callID+"\x00"+leg.BLegID] {
				continue
			}
			retainedLegs[leg.BLegID] = true
			usageKey, err := billing.CallLegUsageKey(parsed, leg.BLegID)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("billing: A-leg provider leg identity: %w", err)
			}
			legKeys[usageKey] = true
			// Legs without heads (fence-only exclusions,
			// never-started legs) keep no heads but still
			// resolve from fences, journals, and work state.
			okHeads[callID] = append(okHeads[callID], headsByLeg[callID][callID+"\x00"+leg.BLegID]...)
		}
		for _, row := range fences[callID] {
			keep := false
			for key := range legKeys {
				if row.LineageKey == key || strings.HasPrefix(row.LineageKey, key+":provider-charge:") {
					keep = true
					break
				}
			}
			if keep {
				okFences[callID] = append(okFences[callID], row)
			}
		}
		for _, journal := range journals[callID] {
			if journal.BLegID != "" && !retainedLegs[journal.BLegID] {
				continue
			}
			okJournals[callID] = append(okJournals[callID], journal)
		}
	}
	return okHeads, okFences, okJournals, nil
}

// providerHeadKeys collects revision-state join keys for one chunk:
// head keys from heads plus head keys from posting fences, so
// in-flight revisions pend excluded lineages too.
func providerHeadKeys(heads map[string][]providerCostHeadRow, fences map[string][]providerCostPostingFenceRow) []string {
	var out []string
	for _, rows := range heads {
		for _, row := range rows {
			out = append(out, row.HeadKey)
		}
	}
	for _, rows := range fences {
		for _, row := range rows {
			out = append(out, row.HeadKey)
		}
	}
	return out
}

// providerSnapshotSources collects provider snapshot identities across
// one chunk in chunk order for snapshot batching: journal identities
// plus head current operations, so journal-less zero-delta revisions
// load their immutable operation snapshots too.
func providerSnapshotSources(chunkIDs []string, journals map[string][]billing.JournalTransaction, heads map[string][]providerCostHeadRow) []string {
	var out []string
	for _, callID := range chunkIDs {
		for _, journal := range journals[callID] {
			if journal.OperationKind != "provider_call_cogs" {
				continue
			}
			out = append(out, journal.ID)
		}
		for _, head := range heads[callID] {
			out = append(out, head.LastOperationKey)
		}
	}
	return out
}

// alegCoreProviderHead adapts one durable head row to the single core
// provider-authority input. Undecodable subjects fail the query like
// any undecodable durable fact; kind/ID agreement with the decoded
// subject plus full ancestry are core proof.
func alegCoreProviderHead(row providerCostHeadRow) (billing.ALegProviderHead, error) {
	var subject metering.SubjectRef
	if err := json.Unmarshal([]byte(row.SubjectJSON), &subject); err != nil {
		return billing.ALegProviderHead{}, fmt.Errorf("billingstore: decode A-leg provider head subject: %w", err)
	}
	return billing.ALegProviderHead{
		StoreID:   row.StoreID,
		AccountID: row.AccountID, CallID: row.CallID, HeadKey: row.HeadKey,
		SubjectKind: row.SubjectKind, SubjectID: row.SubjectID, Subject: subject,
		EvidenceRevision: uint64(maxInt64ToZero(row.EvidenceRevision)), InputSetHash: row.InputSetHash,
		ValuationID: row.ValuationID, CurrentAmount: billing.Money{Nano: row.AmountNano, Currency: row.Currency},
		HeadVersion: uint64(maxInt64ToZero(row.HeadVersion)), Fence: uint64(maxInt64ToZero(row.Fence)),
		LastOperationKey:      row.LastOperationKey,
		OriginalTransactionID: providerCostOriginalTransactionID(row.OriginalTransactionID, row.LastTransactionID),
		LastTransactionID:     row.LastTransactionID,
	}, nil
}

// alegCoreProviderFence adapts one durable posting-fence row. Negative
// counters clamp to zero so wraparound can never fake validity; the
// core evaluator fails clamped rows closed.
func alegCoreProviderFence(row providerCostPostingFenceRow) billing.ALegProviderFence {
	return billing.ALegProviderFence{
		StoreID:    row.StoreID,
		LineageKey: row.LineageKey, Authority: row.Authority, HeadKey: row.HeadKey,
		EvidenceRevision: uint64(maxInt64ToZero(row.EvidenceRevision)), InputSetHash: row.InputSetHash,
		Fingerprint: row.Fingerprint, Amount: billing.Money{Nano: row.AmountNano, Currency: row.Currency},
		Fence:                 uint64(maxInt64ToZero(row.Fence)),
		LastOperationKey:      row.LastOperationKey,
		OriginalTransactionID: providerCostOriginalTransactionID(row.OriginalTransactionID, row.LastTransactionID),
		LastTransactionID:     row.LastTransactionID,
	}
}

// alegCoreProviderExecutionFence adapts one durable execution-fence row
// with its full persisted owner envelope. The evaluator compares every
// field against the current head, posting fence, and leg scope; the
// fence counter itself is writer-positive and CAS-monotonic per
// lineage, with no cross-counter equality to head-local counters
// (execution-gate events and head-local advances count different
// domains: fresh inserts and cutover recoveries start each counter at
// one independently).
func alegCoreProviderExecutionFence(row providerCostExecutionFenceRow) billing.ALegProviderExecutionFence {
	return billing.ALegProviderExecutionFence{
		StoreID:    row.StoreID,
		LineageKey: row.ExecutionLineage, Authority: row.Authority,
		OwnerSubjectKind: row.OwnerSubjectKind, OwnerHeadKey: row.OwnerHeadKey,
		OwnerRevision: uint64(maxInt64ToZero(row.OwnerRevision)), OwnerInputSetHash: row.OwnerInputSetHash,
		OwnerFingerprint: row.OwnerFingerprint, Fence: uint64(maxInt64ToZero(row.Fence)),
		LastOperationKey: row.LastOperationKey, LastTransactionID: row.LastTransactionID,
	}
}

// alegCoreProviderSnapshot adapts one durable snapshot row to the core
// provider-authority input.
func alegCoreProviderSnapshot(row operationSnapshotRow) billing.ALegProviderSnapshot {
	return billing.ALegProviderSnapshot{
		AccountID: row.AccountID, OperationKind: row.OperationKind,
		OperationKey: row.OperationKey, SourceKey: row.SourceKey,
		Fingerprint: row.Fingerprint, IntegrityFingerprint: row.IntegrityFingerprint,
		Currency: row.Currency, Mode: row.Mode,
		Before: billing.AccountSnapshot{BalanceNano: row.BalanceBefore, SpendableNano: row.SpendableBefore,
			CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit,
			Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionBefore},
		After: billing.AccountSnapshot{BalanceNano: row.BalanceAfter, SpendableNano: row.SpendableAfter,
			CreditFloorNano: row.CreditFloor, CreditLimitNano: row.CreditLimit,
			Mode: billing.AccountMode(row.Mode), Currency: row.Currency, Version: row.VersionAfter},
		SequenceStart: row.SequenceStart, SequenceEnd: row.SequenceEnd,
	}
}

// evaluateALegProviderLegTx proves one B-leg inside the snapshot: it
// assembles the leg's facts from chunk-loaded rows and delegates all
// proof to the single core evaluator. Provider verdicts never touch
// retail totals.
func evaluateALegProviderLegTx(scope billing.ALegAuthorityScope, leg alegProviderLegRow, journals []billing.JournalTransaction,
	heads []providerCostHeadRow, postingFences []providerCostPostingFenceRow, executionFences []providerCostExecutionFenceRow,
	work map[string]alegProviderWorkState, revisionPending map[string]bool,
	snapshots map[string][]operationSnapshotRow) (billing.ALegProviderLegVerdict, []billing.ReconciliationIssue, error) {
	callID, err := billing.ParseBillingCallID(scope.CallID)
	if err != nil {
		return billing.ALegProviderLegVerdict{}, nil, fmt.Errorf("billing: A-leg provider call identity: %w", err)
	}
	legKey, err := billing.CallLegUsageKey(callID, leg.BLegID)
	if err != nil {
		return billing.ALegProviderLegVerdict{}, nil, fmt.Errorf("billing: A-leg provider leg identity: %w", err)
	}
	facts := billing.ALegProviderLegFacts{BLegID: leg.BLegID, Outcome: billing.LegOutcome(leg.Outcome)}
	if state, ok := work[legKey]; ok && state.Status != "processed" {
		facts.WorkPending = true
	}
	// Heads and fences narrow to this leg before joining revision
	// state: another leg's in-flight revision must never pend this
	// leg. Lineages cover the base key plus this leg's child keys.
	lineages := map[string]bool{legKey: true}
	var keys []string
	for _, row := range heads {
		head, err := alegCoreProviderHead(row)
		if err != nil {
			return billing.ALegProviderLegVerdict{}, nil, err
		}
		if head.Subject.BLegID != leg.BLegID {
			continue
		}
		facts.Heads = append(facts.Heads, head)
		keys = append(keys, head.HeadKey)
		if head.Subject.Kind == metering.SubjectProviderCharge && head.Subject.ProviderChargeID != "" {
			lineages[legKey+":provider-charge:"+head.Subject.ProviderChargeID] = true
		}
	}
	for _, row := range postingFences {
		fence := alegCoreProviderFence(row)
		// State-join keys cover every fence of this leg's lineages,
		// including fence-only child exclusions without heads, so
		// in-flight revisions pend the leg. Only evaluated lineages
		// enter the proof facts below.
		if fence.LineageKey == legKey || strings.HasPrefix(fence.LineageKey, legKey+":provider-charge:") {
			keys = append(keys, fence.HeadKey)
		}
		if !lineages[fence.LineageKey] {
			continue
		}
		facts.PostingFences = append(facts.PostingFences, fence)
	}
	for _, pending := range keys {
		if revisionPending[pending] {
			facts.RevisionPending = true
			break
		}
	}
	for _, row := range executionFences {
		facts.ExecutionFences = append(facts.ExecutionFences, alegCoreProviderExecutionFence(row))
	}
	// Snapshots bind by operation key: journal identities plus head
	// current operations, so journal-less zero-delta revisions load
	// their immutable operation snapshots too. Collection dedupes by
	// operation key; genuine duplicate rows for one key stay visible
	// so the evaluator can fail them closed as ambiguous.
	addedSnapshots := make(map[string]bool)
	addSnapshots := func(operationKey string) {
		if addedSnapshots[operationKey] {
			return
		}
		addedSnapshots[operationKey] = true
		for _, row := range snapshots[operationKey] {
			facts.Snapshots = append(facts.Snapshots, alegCoreProviderSnapshot(row))
		}
	}
	for _, journal := range journals {
		addSnapshots(journal.ID)
	}
	for _, row := range heads {
		addSnapshots(row.LastOperationKey)
	}
	facts.Journals = journals
	return billing.EvaluateALegProviderLeg(scope, facts)
}

func loadALegCallJournalsTx(ctx context.Context, q bun.IDB, accountID string, callIDs []string) (map[string][]billing.JournalTransaction, map[string]string, error) {
	out := make(map[string][]billing.JournalTransaction, len(callIDs))
	fanout := make(map[string]string)
	if len(callIDs) == 0 {
		return out, fanout, nil
	}
	args := make([]any, 0, len(callIDs)+2)
	args = append(args, accountID)
	for _, callID := range callIDs {
		args = append(args, callID)
	}
	args = append(args, alegReportMaxJournalsPerCall+1)
	var rows []alegJournalChunkRow
	// Per-call capped journal rows across all books: cross-book drift
	// detection needs nonfinancial rows too, so no book predicate here.
	// cap+1 rows per call prove overflow deterministically.
	if err := q.NewRaw(`SELECT transaction_id, account_id, book, currency, source_key, semantic_fingerprint, turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id, correction_group_id, operation_kind, balance_before_nano, balance_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at FROM (SELECT j.*, ROW_NUMBER() OVER (PARTITION BY j.turn_id ORDER BY j.account_sequence, j.transaction_id) AS rn FROM journal_transactions j WHERE j.account_id = ? AND j.turn_id IN (`+sqlPlaceholders(len(callIDs))+`)) WHERE rn <= ?`, args...).Scan(ctx, &rows); err != nil {
		return nil, nil, fmt.Errorf("billingstore: query A-leg journals: %w", err)
	}
	counts := make(map[string]int, len(callIDs))
	txCalls := make(map[string]string)
	var txIDs []string
	for _, row := range rows {
		counts[row.TurnID]++
		if counts[row.TurnID] > alegReportMaxJournalsPerCall {
			fanout[row.TurnID] = alegIssueJournalFanout
			continue
		}
		txIDs = append(txIDs, row.ID)
		txCalls[row.ID] = row.TurnID
	}
	// Per-transaction capped entries for the retained journals only.
	entryCounts := make(map[string]int)
	byTx := make(map[string][]billing.JournalEntry)
	if len(txIDs) > 0 {
		entryArgs := make([]any, 0, len(txIDs)+1)
		for _, id := range txIDs {
			entryArgs = append(entryArgs, id)
		}
		entryArgs = append(entryArgs, alegReportMaxEntriesPerTx+1)
		var entries []alegJournalEntryChunkRow
		if err := q.NewRaw(`SELECT transaction_id, ordinal, ledger_account, side, currency, amount_nano FROM (SELECT e.*, ROW_NUMBER() OVER (PARTITION BY e.transaction_id ORDER BY e.ordinal) AS rn FROM journal_entries e WHERE e.transaction_id IN (`+sqlPlaceholders(len(txIDs))+`)) WHERE rn <= ?`, entryArgs...).Scan(ctx, &entries); err != nil {
			return nil, nil, fmt.Errorf("billingstore: query A-leg journal entries: %w", err)
		}
		for _, entry := range entries {
			entryCounts[entry.TransactionID]++
			if entryCounts[entry.TransactionID] > alegReportMaxEntriesPerTx {
				if callID, ok := txCalls[entry.TransactionID]; ok {
					if _, exists := fanout[callID]; !exists {
						fanout[callID] = alegIssueEntryFanout
					}
				}
				continue
			}
			byTx[entry.TransactionID] = append(byTx[entry.TransactionID], billing.JournalEntry{
				LedgerAccount: entry.LedgerAccount,
				Side:          billing.JournalSide(entry.Side),
				Amount:        billing.Money{Nano: entry.AmountNano, Currency: entry.Currency},
			})
		}
	}
	for _, row := range rows {
		if _, over := fanout[row.TurnID]; over {
			continue
		}
		out[row.TurnID] = append(out[row.TurnID], journalFromRow(row.journalTransactionRow, byTx[row.ID]))
	}
	return out, fanout, nil
}

// alegJournalChunkRow mirrors journalTransactionRow plus the per-call
// window position for bounded retention.
type alegJournalChunkRow struct {
	journalTransactionRow
	Rn int `bun:"rn"`
}

// alegJournalEntryChunkRow mirrors journalEntryRow plus the
// per-transaction window position for bounded retention.
type alegJournalEntryChunkRow struct {
	journalEntryRow
	Rn int `bun:"rn"`
}
