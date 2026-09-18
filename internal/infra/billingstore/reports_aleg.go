package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
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

// Cycle 1 shell-owned bound issue codes. Financial proof codes live in
// core billing (billing.ALegIssue*); every code carries the call ID in
// Detail and the marker/journal sequence where durable.
const (
	alegIssueLegFanout     = "customer_leg_fanout"
	alegIssueJournalFanout = "customer_journal_fanout"
	alegIssueEntryFanout   = "customer_entry_fanout"
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
	// Cycle 1 defers all provider economics: every leg counts pending,
	// including never-started and rejected outcomes. Lineage and outcome
	// facts are preserved on the rows; no known-zero is emitted.
	var pendingLegs int
	if err := tx.NewRaw(`SELECT COUNT(*) FROM usage_leg_records l JOIN usage_call_records c ON c.call_id = l.call_id WHERE c.account_id = ? AND l.a_leg_id = ?`, account.ID, normalized.ALegID).Scan(ctx, &pendingLegs); err != nil {
		return billing.ALegReport{}, fmt.Errorf("billingstore: count A-leg legs: %w", err)
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
			class, classIssues, err := billing.EvaluateALegCallAuthority(billing.ALegAuthorityScope{AccountID: account.ID, ALegID: normalized.ALegID, CallID: callID, Currency: currency}, alegCoreMarkers(markers[callID]), journals[callID])
			if err != nil {
				return billing.ALegReport{}, err
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
		Provider:  billing.ALegProviderTotals{Currency: currency, PendingLegs: pendingLegs},
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
			legs = append(legs, billing.ALegReportLeg{
				BLegID: row.BLegID, Outcome: record.Outcome, Surfaced: record.Surfaced,
				BackendID: record.BackendID, ProviderID: record.ProviderID, ModelID: record.ModelID,
				Fingerprint: record.Fingerprint, ProviderStatus: billing.ALegProviderPending,
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
		report.Contributions = append(report.Contributions, billing.ALegReportContribution{
			Call: summary, LegKey: row.Key,
			BLegID: row.BLegID, Outcome: record.Outcome, Surfaced: record.Surfaced,
			BackendID: record.BackendID, ProviderID: record.ProviderID, ModelID: record.ModelID,
			Fingerprint: record.Fingerprint, ProviderStatus: billing.ALegProviderPending,
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
