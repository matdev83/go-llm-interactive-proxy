package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/uptrace/bun/dialect"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.1B durable read adapter for the approved 16.1A economic-detail
// model. QueryEconomicDetail loads persisted sources and assembles them
// through the pure billing.AssembleEconomicDetail contract.
//
// Scope is enforced in SQL (store handle, account/call/A-leg predicates,
// tenant predicate where the table carries it) and again in domain assembly.
// Reads are a small fixed number of bounded queries: no JOIN across the
// billing/metering families, no N+1 over scope size. Multi-row loads use
// chunked IN queries ordered deterministically; the 16.1A Limit applies at
// assembly projection while totals, missing evidence and coverage always
// cover the full loaded scope (Truncated signals a paged observation list).
// A-leg gathers every attributable call/B-leg without double-counting: each
// durable row contributes once, keyed by its immutable identity. Durable
// reconciliation retentions are gathered per authoritative in-scope subject
// (primary call/A-leg plus its calls/B-legs) as a bounded, deterministically
// ordered set; a later child subject is never dropped silently, revision
// selection stays within one true subject identity, and an over-bound subject
// scope fails closed with billing.ErrEconomicDetailBoundExceeded.
//
// Reuses existing tables and indexes only; no migration. Links without a
// durable row surface as explicit missing/partial status through 16.1A,
// never as fabricated evidence. The full operator-selection candidate set is
// not retained as a unit in any table, so the adapter loads the authoritative
// selected/posted heads and 16.1A exposes them separately: one unambiguous
// persisted head is projected as the truthful candidate-less singular
// selection and selected subtotal, while multiple independent heads stay an
// explicit ambiguous scope that is never summed or FX-converted. Posted heads
// carry the frozen outcome and its transaction/operation lineage.
// Statement-origin full observations live in the metering journal family, so
// S planes show valuation totals with refs while their statement
// observations remain explicitly missing until a composed journal read joins
// them. Existing summary/explanation responses are untouched.
//
// Continuation (Finding 5A): the observation page is a strict keyset page over
// the canonical order (stream, sequence, source event, observation identity,
// revision). An authenticated per-store cursor binds the normalized
// store/tenant/account/call-or-A-leg scope, the ordering definition and the
// cursor kind, and carries the last returned position; page N+1 continues
// strictly after page N so every in-scope observation is reachable exactly
// once.
//
// Continuation (Finding 7): page one additionally binds a deterministic,
// bounded fingerprint of the full pre-pagination observation membership and
// every repeated full-scope fact (valuations, heads, reconciliations,
// allocations, summaries). Before serving a continuation, the adapter
// recomputes that fingerprint under the same bounded reads and rejects the
// cursor with economics.ErrOperatorCursorStale when anything changed, so a
// resumed A-leg call or a late correction can never be silently omitted while
// newer totals appear. The snapshot boundary is explicit invalidation rather
// than a cross-table as-of: the durable sources carry no single monotonic
// revision, and a true as-of would require a broad migration. Limit and the
// cursor are not part of the bound filter, so a caller may change page size and
// still continue. All cursor rejections leak no payload or key material.
//
// Bounds (Finding 5B1): every leg query carries a database-side LIMIT against
// one global finite leg-record budget, chunking enforces that one budget
// rather than a per-chunk budget, leg payloads decode through the canonical
// sealed record contract, and embedded observations are counted with checked
// arithmetic before any growth. A scope above either bound fails closed with
// billing.ErrEconomicDetailBoundExceeded and is never truncated silently. A
// cursor page still traverses the same bounded observation set exactly once.
//
// Bounds (Finding 5B2): valuation selection is pushed to the database boundary
// as a latest-revision-per-logical-stream window query over the persisted
// authoritative subject identity plus perspective and basis, so a large
// revision history is never decoded in full. Selected heads carry a
// database-side LIMIT and one global finite budget across chunks. Both budgets
// fail closed with billing.ErrEconomicDetailBoundExceeded rather than
// truncating, and both preserve the true stream/head identity established by
// Finding 3A/4A.
//
// Provider-charge subjects (Finding 1): a request-scoped provider charge is
// owned by its B-leg, so the scope's attributable provider-charge subjects are
// discovered from the authoritative in-scope B-leg identities rather than from
// the caller or the subject's self-declared lineage. Discovery is a bounded
// database DISTINCT over the store/tenant/account/B-leg predicates, and each
// discovered subject keeps its charge id, owning B-leg and trusted account so
// the valuation read is an exact canonical ownership match. Distinct provider
// accounts of one charge id remain independent streams through the full
// canonical partition; a foreign account is never discovered or selected, and a
// supplied tenant scope likewise excludes foreign tenants.

const (
	// economicDetailMaxCalls bounds one A-leg call list. Beyond the cap the
	// query fails closed instead of paging silently inside the adapter;
	// page tokens belong to the 16.2 control surface.
	economicDetailMaxCalls = 500
	// economicDetailChunkSize bounds one IN-list for chunked multi-row loads,
	// mirroring the A-leg report scope chunk discipline.
	economicDetailChunkSize = 200
	// economicDetailMaxAllocationRecords bounds distinct allocation envelopes
	// joined for one detail query. The bound is one global budget across
	// target-ID chunks and is enforced database-side with LIMIT remaining+1, so
	// chunking cannot multiply it and an over-bound scope fails closed instead
	// of materializing the excess discovery set.
	economicDetailMaxAllocationRecords = 64
	// economicDetailMaxLegRecords bounds the leg records one detail query may
	// materialize. One leg record is at most one B-leg subject of the frozen
	// 16.1A assembly, whose independent in-scope subject set is itself bounded
	// by billing.MaxEconomicDetailReconciliations, so no scope that can
	// assemble carries more leg records than that finite maximum. The bound is
	// enforced database-side with LIMIT and re-checked per chunk so chunking
	// cannot multiply it; overflow fails closed instead of truncating.
	economicDetailMaxLegRecords = billing.MaxEconomicDetailReconciliations
	// economicDetailMaxValuations bounds the distinct logical valuation streams
	// one detail query may retain. Latest-revision selection per true stream
	// identity and the global budget are both enforced at the database boundary
	// with a window query and LIMIT remaining+1, so a small number of streams
	// with a large revision history is decoded without materializing that
	// history. Overflow fails closed instead of truncating.
	economicDetailMaxValuations = billing.MaxEconomicDetailValuations
	// economicDetailMaxChargeSubjects bounds the distinct provider-charge
	// subjects one detail query may discover from the scope's authoritative
	// B-legs. Discovery only feeds detailValuations, whose own global budget
	// bounds the retained streams, but discovery itself must be finite before
	// any Go growth: the DISTINCT discovery query carries LIMIT remaining+1
	// against this one global budget, so a scope with an unbounded
	// provider-charge population fails closed instead of materializing it.
	economicDetailMaxChargeSubjects = billing.MaxEconomicDetailValuations
	// economicDetailMaxHeads bounds the selected/posted heads one detail query
	// may materialize. Every head query carries a database-side LIMIT and one
	// global budget spans all chunks, so chunking cannot multiply it.
	economicDetailMaxHeads = billing.MaxEconomicDetailHeads
	// economicDetailMaxJournalTransactions bounds the financial journal rows
	// one call's settlement summary may materialize before the secondary
	// journal-entry load. The read carries a deterministic ORDER BY plus a
	// database-side LIMIT of budget+1; over-budget correction history fails
	// closed with billing.ErrEconomicDetailBoundExceeded instead of decoding or
	// truncating it.
	economicDetailMaxJournalTransactions = billing.MaxEconomicDetailJournalTransactions
	// economicDetailMaxOperationSnapshots bounds the settlement operation
	// snapshots one call's settlement summary may materialize. It carries the
	// same deterministic ORDER BY plus database-side LIMIT budget+1 discipline
	// and fails closed on overflow.
	economicDetailMaxOperationSnapshots = billing.MaxEconomicDetailOperationSnapshots
)

// QueryEconomicDetail returns the scoped economic detail for one billing call
// or one A-leg. Unknown calls/legs report billing.ErrReportNotFound;
// out-of-scope evidence reports billing.ErrEconomicDetailScopeMismatch;
// over-bound scopes report billing.ErrEconomicDetailBoundExceeded. A
// malformed, tampered, cross-scope/cross-kind or wrong-ordering cursor reports
// economics.ErrOperatorCursorInvalid before any trusted read. A continuation
// whose full-scope snapshot fingerprint changed (for example a resumed call
// inserted ahead of the position, or a late valuation/head/reconciliation/
// allocation correction) reports economics.ErrOperatorCursorStale and requires
// restarting pagination. Storage failures carry no SQL text and no raw payload
// bytes.
func (s *DurableStore) QueryEconomicDetail(ctx context.Context, query billing.EconomicDetailQuery) (billing.EconomicDetail, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.EconomicDetail{}, err
	}
	normalized, err := query.Normalize()
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	if normalized.StoreID != s.storeID {
		return billing.EconomicDetail{}, fmt.Errorf("%w: detail store scope", billing.ErrEconomicDetailScopeMismatch)
	}
	after, cursorSnapshot, cursorExtSnapshot, err := s.decodeEconomicDetailCursor(normalized)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	var detail billing.EconomicDetail
	if normalized.IsCallScope() {
		detail, err = s.queryCallEconomicDetail(ctx, normalized, after)
	} else {
		detail, err = s.queryALegEconomicDetail(ctx, normalized, after)
	}
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	// A continuation is only valid while the full-scope snapshot it was issued
	// against is unchanged. Recomputing the fingerprint from the same bounded
	// reads and comparing it fails closed instead of returning a page that
	// silently mixes two snapshots (a resumed call or correction can sort
	// before the issued position while changing the totals).
	if cursorSnapshot != "" && cursorSnapshot != detail.SnapshotFingerprint {
		return billing.EconomicDetail{}, fmt.Errorf("%w: restart pagination", economics.ErrOperatorCursorStale)
	}
	detail.PreviousSnapshotFingerprint = cursorExtSnapshot
	if detail.NextPosition != nil {
		detail.NextCursor = s.encodeEconomicDetailCursor(normalized, *detail.NextPosition, detail.SnapshotFingerprint, cursorExtSnapshot)
	}
	// The opaque continuation is transport state, not scope identity: the
	// authoritative scope snapshot is identical on every page and carries no
	// used cursor.
	detail.Scope.Cursor = ""
	return detail, nil
}

// EncodeEconomicDetailSnapshotContinuation re-encodes an economic-detail
// continuation preserving the durable base snapshot fingerprint and binding an
// outer fingerprint computed by a reader composed above this adapter. It
// authenticates nothing new: the caller has already authenticated the incoming
// cursor through QueryEconomicDetail, and this only produces the next opaque
// token. An invalid position or foreign store yields an empty token so the
// caller keeps its existing (already authenticated) cursor rather than
// emitting a token that cannot verify.
func (s *DurableStore) EncodeEconomicDetailSnapshotContinuation(query billing.EconomicDetailQuery, position billing.EconomicObservationPosition, baseFingerprint, outerFingerprint string) string {
	normalized, err := query.Normalize()
	if err != nil {
		return ""
	}
	if normalized.StoreID != s.storeID {
		return ""
	}
	if err := position.Validate(); err != nil {
		return ""
	}
	return s.encodeEconomicDetailCursor(normalized, position, baseFingerprint, outerFingerprint)
}

// economicDetailCursorFilter binds the complete normalized scope of one
// economic-detail query plus the canonical ordering definition. Limit and the
// opaque cursor are deliberately excluded, so a caller may change the page size
// and still continue with an outstanding cursor, while a different store,
// tenant, account, call/A-leg identity or ordering definition is rejected.
func economicDetailCursorFilter(query billing.EconomicDetailQuery) string {
	return operatorFilterHash(struct {
		Store, Tenant, Account, Call, ALeg, Order string
	}{query.StoreID, query.TenantID, query.AccountID, query.BillingCallID, query.ALegID, billing.EconomicDetailObservationOrder})
}

// decodeEconomicDetailCursor authenticates the opaque continuation token with
// the per-store server key and converts it to the core keyset position plus the
// base and composed snapshot fingerprints it binds. A missing cursor is the
// first page; every rejection is the single classified invalid-cursor error and
// leaks no payload or key material.
func (s *DurableStore) decodeEconomicDetailCursor(query billing.EconomicDetailQuery) (*billing.EconomicObservationPosition, string, string, error) {
	if query.Cursor == "" {
		return nil, "", "", nil
	}
	cursor, err := decodeOperatorCursor(query.Cursor, "economic-detail", s.storeID, economicDetailCursorFilter(query), s.cursorKey)
	if err != nil {
		return nil, "", "", err
	}
	position := &billing.EconomicObservationPosition{
		StreamID: cursor.Detail.StreamID, Sequence: cursor.Detail.Sequence,
		SourceEventKey: cursor.Detail.SourceEventKey, ObservationID: cursor.Detail.ObservationID,
		Revision: cursor.Detail.Revision,
	}
	if err := position.Validate(); err != nil {
		return nil, "", "", invalidOperatorCursor()
	}
	return position, cursor.Snapshot, cursor.ExtSnapshot, nil
}

func (s *DurableStore) encodeEconomicDetailCursor(query billing.EconomicDetailQuery, position billing.EconomicObservationPosition, baseFingerprint, outerFingerprint string) string {
	return encodeOperatorCursor(operatorCursor{
		Kind: "economic-detail", StoreID: s.storeID, Filter: economicDetailCursorFilter(query),
		Order:       billing.EconomicDetailObservationOrder,
		Snapshot:    baseFingerprint,
		ExtSnapshot: outerFingerprint,
		Detail: &economicDetailCursorPosition{
			StreamID: position.StreamID, Sequence: position.Sequence,
			SourceEventKey: position.SourceEventKey, ObservationID: position.ObservationID,
			Revision: position.Revision,
		},
	}, s.cursorKey)
}

func (s *DurableStore) queryCallEconomicDetail(ctx context.Context, query billing.EconomicDetailQuery, after *billing.EconomicObservationPosition) (billing.EconomicDetail, error) {
	callID, err := billing.ParseBillingCallID(query.BillingCallID)
	if err != nil {
		return billing.EconomicDetail{}, fmt.Errorf("%w: %v", billing.ErrEconomicDetailInvalid, err)
	}
	closure, err := s.loadCallUsage(ctx, s.db, callID)
	if err != nil {
		if errors.Is(err, ErrUsageRecordNotFound) {
			return billing.EconomicDetail{}, billing.ErrReportNotFound
		}
		return billing.EconomicDetail{}, fmt.Errorf("billingstore: economic detail call load: %w", err)
	}
	if closure.AccountID != query.AccountID {
		// Cross-account existence is not revealed; the scoped row is absent.
		return billing.EconomicDetail{}, billing.ErrReportNotFound
	}
	if query.ALegID != "" && closure.ALegID != query.ALegID {
		return billing.EconomicDetail{}, fmt.Errorf("%w: call A-leg mismatch", billing.ErrEconomicDetailScopeMismatch)
	}
	legs, err := s.detailLegsByCalls(ctx, []string{callID.String()})
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	sort.SliceStable(legs, func(i, j int) bool {
		if legs[i].BLegID != legs[j].BLegID {
			return legs[i].BLegID < legs[j].BLegID
		}
		return legs[i].AttemptSeq < legs[j].AttemptSeq
	})

	callIDs := []string{callID.String()}
	subjects := economicDetailScopeSubjects("", callIDs, legs)
	bLegIDs := detailBLegIDs(legs)
	charges, err := s.economicDetailProviderChargeSubjects(ctx, query.TenantID, query.AccountID, bLegIDs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	observations, err := callLegObservations(legs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	executionCoverage := callLegExecutionCoverage(query.StoreID, query.TenantID, query.AccountID, legs)
	heads, err := s.detailHeads(ctx, query.AccountID, callIDs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	valuations, err := s.detailValuations(ctx, query.TenantID, appendEconomicDetailSubjects(subjects, charges))
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	selectedValuations, err := s.detailSelectedValuations(ctx, query.TenantID, economicDetailSelectedValuationRefs(heads))
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	reconciliationSubjects, err := s.economicDetailProviderChargeReconciliationSubjects(ctx, query.TenantID, query.AccountID, bLegIDs, billing.MaxEconomicDetailReconciliations-len(subjects))
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	reconciliations, err := s.detailReconciliations(ctx, query.TenantID, appendEconomicDetailSubjects(subjects, reconciliationSubjects))
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	turn, err := s.detailTurnSummary(ctx, query.AccountID, callID, legs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	allocationResult, err := s.detailAllocationResult(ctx, query, callIDs, bLegIDs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	return billing.AssembleEconomicDetail(billing.EconomicDetailInput{
		Query: query, After: after, Observations: observations, Valuations: valuations,
		SelectedValuations: selectedValuations,
		Reconciliations:    reconciliations, Heads: heads,
		TurnSummary: turn, Allocations: allocationResult.Lines, AllocationState: allocationResult.State,
		ExecutionCoverage: executionCoverage,
	})
}

func (s *DurableStore) queryALegEconomicDetail(ctx context.Context, query billing.EconomicDetailQuery, after *billing.EconomicObservationPosition) (billing.EconomicDetail, error) {
	callIDs, err := s.detailALegCallIDs(ctx, query.AccountID, query.ALegID)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	legs, err := s.detailLegsByCalls(ctx, callIDs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	subjects := economicDetailScopeSubjects(query.ALegID, callIDs, legs)
	bLegIDs := detailBLegIDs(legs)
	charges, err := s.economicDetailProviderChargeSubjects(ctx, query.TenantID, query.AccountID, bLegIDs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	observations, err := callLegObservations(legs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	executionCoverage := callLegExecutionCoverage(query.StoreID, query.TenantID, query.AccountID, legs)
	heads, err := s.detailHeads(ctx, query.AccountID, callIDs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	valuations, err := s.detailValuations(ctx, query.TenantID, appendEconomicDetailSubjects(subjects, charges))
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	selectedValuations, err := s.detailSelectedValuations(ctx, query.TenantID, economicDetailSelectedValuationRefs(heads))
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	reconciliationSubjects, err := s.economicDetailProviderChargeReconciliationSubjects(ctx, query.TenantID, query.AccountID, bLegIDs, billing.MaxEconomicDetailReconciliations-len(subjects))
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	reconciliations, err := s.detailReconciliations(ctx, query.TenantID, appendEconomicDetailSubjects(subjects, reconciliationSubjects))
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	allocationResult, err := s.detailAllocationResult(ctx, query, callIDs, bLegIDs)
	if err != nil {
		return billing.EconomicDetail{}, err
	}
	input := billing.EconomicDetailInput{
		Query: query, After: after, Observations: observations, Valuations: valuations,
		SelectedValuations: selectedValuations,
		Reconciliations:    reconciliations, Heads: heads,
		Allocations: allocationResult.Lines, AllocationState: allocationResult.State,
		ExecutionCoverage: executionCoverage,
	}
	// Retail/provider totals reuse the existing rolling A-leg projection so
	// the detail summary stays byte/semantic compatible with it. A missing
	// account row leaves the summaries explicitly absent instead of failing
	// the evidence detail.
	report, err := s.QueryALegReport(ctx, billing.ALegReportQuery{AccountID: query.AccountID, ALegID: query.ALegID, Limit: 1})
	if err != nil {
		if !errors.Is(err, billing.ErrReportNotFound) {
			return billing.EconomicDetail{}, fmt.Errorf("billingstore: economic detail A-leg summary: %w", err)
		}
	} else {
		retail, provider := report.Retail, report.Provider
		input.RetailTotals, input.ProviderTotals = &retail, &provider
	}
	return billing.AssembleEconomicDetail(input)
}

// detailALegCallIDs lists every attributable call of one A-leg in stable
// sealed order. The list is bounded; overflow fails closed.
func (s *DurableStore) detailALegCallIDs(ctx context.Context, accountID, aLegID string) ([]string, error) {
	var callIDs []string
	if err := s.db.NewRaw(`SELECT call_id FROM usage_call_records WHERE account_id = ? AND a_leg_id = ? ORDER BY sealed_at, call_id LIMIT ?`,
		accountID, aLegID, economicDetailMaxCalls+1).Scan(ctx, &callIDs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, billing.ErrReportNotFound
		}
		return nil, fmt.Errorf("billingstore: economic detail A-leg calls: %w", err)
	}
	if len(callIDs) == 0 {
		return nil, billing.ErrReportNotFound
	}
	if len(callIDs) > economicDetailMaxCalls {
		return nil, fmt.Errorf("%w: A-leg calls exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxCalls)
	}
	return callIDs, nil
}

// detailLegsByCalls loads legs for many calls in bounded chunks with
// deterministic total order. Every query carries LIMIT remaining+1 so the row
// count of each chunk is bounded by what is left of one global budget;
// chunking therefore cannot multiply the bound. Each payload decodes and
// validates through the canonical sealed record contract; corrupt rows fail
// closed without raw bytes. Overflow is explicit
// (billing.ErrEconomicDetailBoundExceeded) and never truncates silently.
// Embedded observations are counted with checked arithmetic as each leg is
// decoded, so a scope whose observations exceed the full-scope budget fails
// closed before the excess is retained.
func (s *DurableStore) detailLegsByCalls(ctx context.Context, callIDs []string) ([]billing.CallLegUsageRecord, error) {
	out := make([]billing.CallLegUsageRecord, 0)
	observationTotal := 0
	for _, chunk := range chunkStrings(callIDs, economicDetailChunkSize) {
		remaining := max(economicDetailMaxLegRecords-len(out), 0)
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, remaining+1)
		var payloads []string
		if err := s.db.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE call_id IN (`+placeholders+`) ORDER BY call_id, b_leg_id LIMIT ?`,
			args...).Scan(ctx, &payloads); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("billingstore: economic detail legs load: %w", err)
			}
			continue
		}
		if len(payloads) > remaining {
			return nil, fmt.Errorf("%w: leg records exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxLegRecords)
		}
		for _, payload := range payloads {
			var record billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(payload), &record); err != nil {
				return nil, fmt.Errorf("billingstore: economic detail leg record mismatch")
			}
			if err := billing.CheckCallLegUsageReplay(record, record); err != nil {
				return nil, fmt.Errorf("billingstore: economic detail leg record mismatch")
			}
			if err := checkObservationBudget(observationTotal, len(record.Observations)); err != nil {
				return nil, err
			}
			observationTotal += len(record.Observations)
			out = append(out, record)
		}
	}
	return out, nil
}

// checkObservationBudget returns the fail-closed bound error when retaining
// count more embedded observations would exceed the full-scope observation
// budget. Subtracting from the remaining budget keeps the comparison
// overflow-free for hostile lengths and never grows past the bound.
func checkObservationBudget(total, count int) error {
	if count <= 0 {
		return nil
	}
	if count > billing.MaxEconomicDetailObservations-total {
		return fmt.Errorf("%w: embedded observations exceed %d", billing.ErrEconomicDetailBoundExceeded, billing.MaxEconomicDetailObservations)
	}
	return nil
}

// callLegObservations carries leg-embedded V2 observations verbatim into the
// detail input. Legacy V1 legs contribute no observations; nothing is
// synthesized for them. The full-scope observation budget is enforced before
// each append, so the projection never materializes an over-bound slice.
func callLegObservations(legs []billing.CallLegUsageRecord) ([]metering.Observation, error) {
	var out []metering.Observation
	total := 0
	for _, leg := range legs {
		if err := checkObservationBudget(total, len(leg.Observations)); err != nil {
			return nil, err
		}
		out = append(out, leg.Observations...)
		total += len(leg.Observations)
	}
	return out, nil
}

// callLegExecutionCoverage carries one bounded provider-neutral execution fact
// per already-loaded authoritative leg. It preserves a leg that has no (or only
// unusable) embedded observations so it still participates in cost completeness,
// and exposes no measurement or amount. The leg-record budget already bounds the
// slice before it reaches assembly.
func callLegExecutionCoverage(storeID, tenantID, accountID string, legs []billing.CallLegUsageRecord) []billing.EconomicDetailExecutionLeg {
	if len(legs) == 0 {
		return nil
	}
	out := make([]billing.EconomicDetailExecutionLeg, 0, len(legs))
	for _, leg := range legs {
		subject := metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: tenantID, AccountID: accountID,
			ALegID: leg.ALegID, BillingCallID: leg.CallID.String(), BLegID: leg.BLegID,
		}
		if leg.AttemptSeq > 0 {
			subject.AttemptSeq = uint64(leg.AttemptSeq)
		}
		out = append(out, billing.NewEconomicDetailExecutionLeg(subject, leg.AttemptSeq, leg.Outcome, leg.Surfaced, leg.Evidence))
	}
	return out
}

func detailBLegIDs(legs []billing.CallLegUsageRecord) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, leg := range legs {
		if leg.BLegID == "" {
			continue
		}
		if _, exists := seen[leg.BLegID]; exists {
			continue
		}
		seen[leg.BLegID] = struct{}{}
		out = append(out, leg.BLegID)
	}
	sort.Strings(out)
	return out
}

type economicDetailSubject struct {
	kind metering.SubjectKind
	id   string
	// providerBLegID and providerAccountScope carry the authoritative ownership
	// of a discovered provider-charge subject (its owning B-leg and trusted
	// account). When present they turn valuation selection into an exact
	// canonical ownership match instead of the coarser kind/id pair, so a charge
	// id reused by another B-leg or account can never be merged into this scope.
	// A directly constructed subject leaves them empty and keeps the exact
	// kind/id contract used by the unit-level valuation reader.
	providerBLegID       string
	providerAccountScope string
}

// appendEconomicDetailSubjects returns the base scope subjects followed by the
// discovered provider-charge subjects without mutating the base slice. Both
// consumers (latest-revision valuation streams and latest-revision
// reconciliation retentions) add only the provider-charge identities their own
// discovery proved attributable to an owned B-leg; the base call/A-leg/B-leg
// subjects stay authoritative.
func appendEconomicDetailSubjects(base, extra []economicDetailSubject) []economicDetailSubject {
	if len(extra) == 0 {
		return base
	}
	out := make([]economicDetailSubject, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)
	return out
}

// economicDetailChargeSubjectRow is one distinct attributable provider-charge
// subject identity: the persisted charge id (subject_id) plus its authoritative
// owning B-leg extracted from canonical JSON.
type economicDetailChargeSubjectRow struct {
	SubjectID string `bun:"subject_id"`
	BLegID    string `bun:"b_leg_id"`
}

// economicDetailProviderChargeSubjects discovers every distinct provider-charge
// subject attributable to the requested scope's authoritative B-legs. A
// request-scoped provider charge is owned by its B-leg (Requirement 6.2), so
// discovery is driven by the in-scope B-leg identities and the trusted account,
// never by a self-declared subject or caller claim. Each returned subject keeps
// the charge id, owning B-leg and account so the later valuation read is an
// exact ownership match that still preserves distinct provider-account streams
// through the full canonical partition.
//
// The read is bounded before any Go growth: the DISTINCT query carries a
// database-side LIMIT of what is left of one global economicDetailMaxChargeSubjects
// budget, and the retained set is deduplicated as rows are scanned. The
// in-scope leg-record bound keeps the B-leg list finite, so a scope whose
// attributable provider-charge population exceeds the budget fails closed with
// billing.ErrEconomicDetailBoundExceeded and is never truncated. A foreign
// account is excluded by the account predicate even when it reuses an in-scope
// B-leg id; a supplied tenant scope additionally excludes foreign tenants.
func (s *DurableStore) economicDetailProviderChargeSubjects(ctx context.Context, tenantID, accountID string, bLegIDs []string) ([]economicDetailSubject, error) {
	if len(bLegIDs) == 0 {
		return nil, nil
	}
	dialectName := s.db.Dialect().Name()
	bLegExpr, err := economicDetailSubjectFieldExpr(dialectName, "b_leg_id")
	if err != nil {
		return nil, err
	}
	accountExpr, err := economicDetailSubjectFieldExpr(dialectName, "account_id")
	if err != nil {
		return nil, err
	}
	out := make([]economicDetailSubject, 0)
	seen := make(map[string]struct{})
	for _, chunk := range chunkStrings(bLegIDs, economicDetailChunkSize) {
		remaining := max(economicDetailMaxChargeSubjects-len(out), 0)
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		where := `store_id = ? AND subject_kind = ?`
		args := []any{s.storeID, string(metering.SubjectProviderCharge)}
		if tenantID != "" {
			where += ` AND tenant_id = ?`
			args = append(args, tenantID)
		}
		where += ` AND ` + bLegExpr + ` IN (` + placeholders + `)`
		for _, id := range chunk {
			args = append(args, id)
		}
		where += ` AND (` + accountExpr + ` = ? OR ` + accountExpr + ` IS NULL OR ` + accountExpr + ` = '')`
		args = append(args, accountID)
		args = append(args, remaining+1)
		var rows []economicDetailChargeSubjectRow
		if err := s.db.NewRaw(`SELECT DISTINCT subject_id, `+bLegExpr+` AS b_leg_id FROM billing_valuations WHERE `+where+` ORDER BY subject_id, b_leg_id LIMIT ?`,
			args...).Scan(ctx, &rows); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("billingstore: economic detail provider charges: %w", err)
			}
			continue
		}
		for _, row := range rows {
			key := row.SubjectID + "\x00" + row.BLegID
			if _, exists := seen[key]; exists {
				continue
			}
			if len(out) >= economicDetailMaxChargeSubjects {
				return nil, fmt.Errorf("%w: provider-charge subjects exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxChargeSubjects)
			}
			seen[key] = struct{}{}
			out = append(out, economicDetailSubject{
				kind: metering.SubjectProviderCharge, id: row.SubjectID,
				providerBLegID: row.BLegID, providerAccountScope: accountID,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].id != out[j].id {
			return out[i].id < out[j].id
		}
		return out[i].providerBLegID < out[j].providerBLegID
	})
	return out, nil
}

// economicDetailScopeSubjects names every authoritative subject of the scope in
// deterministic order: the A-leg itself (when the query is A-leg scoped), each
// attributable call and each B-leg. Different subjects are independent economic
// streams; they are never collapsed, summed or merged.
func economicDetailScopeSubjects(aLegID string, callIDs []string, legs []billing.CallLegUsageRecord) []economicDetailSubject {
	var subjects []economicDetailSubject
	if aLegID != "" {
		subjects = append(subjects, economicDetailSubject{kind: metering.SubjectALeg, id: aLegID})
	}
	for _, callID := range callIDs {
		subjects = append(subjects, economicDetailSubject{kind: metering.SubjectBillingCall, id: callID})
	}
	for _, bLegID := range detailBLegIDs(legs) {
		subjects = append(subjects, economicDetailSubject{kind: metering.SubjectBLeg, id: bLegID})
	}
	return subjects
}

// detailValuations loads canonical valuation envelopes for the scope
// subjects in bounded chunks over the existing store/subject index. Both
// dialects share the same placeholder SQL through Bun.
//
// Latest-revision selection happens at the database boundary: a window
// function partitions on the complete canonical SubjectRef identity extracted
// from canonical_json (every field billing.EconomicValuationStreamKey folds
// in) plus perspective and basis, and keeps only stream_rank = 1 for each true
// logical valuation stream. The persisted subject_id column is deliberately
// NOT used as the identity: it is a single primary key per subject kind and
// distinct true streams can share it (for example B-leg attempt lineage or a
// provider charge's provider account key), so a subject_id partition would
// approximate identity and silently merge independent streams. Independent
// contributions that merely share a basis or a subject_id are never collapsed,
// merged or FX-converted; only a repeated revision of one exact stream
// identity is superseded.
//
// One global economicDetailMaxValuations budget spans all chunks: every query
// carries LIMIT remaining+1, so a large revision history for a small number of
// streams returns only the latest streams without decoding that history, and a
// scope above the bound fails closed with billing.ErrEconomicDetailBoundExceeded.
// Each selected row is verified against the persisted identity columns that
// selected it before use, and after decode the selected set must contain one
// row per true billing.EconomicValuationStreamKey; any mismatch fails closed
// rather than merging an identity the partition did not distinguish. Legacy
// rows are handled without migration: the extraction reads whatever canonical
// subject JSON is persisted, and a legacy row missing a lineage field is
// treated as the same empty value the decoded stream key uses. Output order is
// deterministic by stream identity.
func (s *DurableStore) detailValuations(ctx context.Context, tenantID string, subjects []economicDetailSubject) ([]economics.Valuation, error) {
	dialectName := s.db.Dialect().Name()
	partition, err := economicDetailValuationPartitionSQL(dialectName)
	if err != nil {
		return nil, err
	}
	bLegExpr, err := economicDetailSubjectFieldExpr(dialectName, "b_leg_id")
	if err != nil {
		return nil, err
	}
	accountExpr, err := economicDetailSubjectFieldExpr(dialectName, "account_id")
	if err != nil {
		return nil, err
	}
	type latestValuation struct {
		valuation economics.Valuation
		createdAt int64
		version   int64
	}
	byStream := map[string]latestValuation{}
	loaded := 0
	for _, chunk := range chunkEconomicDetailSubjects(subjects, economicDetailChunkSize) {
		remaining := max(economicDetailMaxValuations-loaded, 0)
		var clauses []string
		args := []any{s.storeID}
		where := `store_id = ?`
		if tenantID != "" {
			where += ` AND tenant_id = ?`
			args = append(args, tenantID)
		}
		for _, subject := range chunk {
			clause := `(subject_kind = ? AND subject_id = ?)`
			args = append(args, string(subject.kind), subject.id)
			// A discovered provider-charge subject additionally pins its
			// authoritative B-leg ownership and trusted account. The charge id
			// alone is not a stream identity (provider accounts differ), and a
			// reused id must not pull in another B-leg or account. An account-less
			// legacy row stays selectable, exactly as the canonical subject
			// validator treats an absent account as in-scope.
			if subject.kind == metering.SubjectProviderCharge {
				if subject.providerBLegID != "" {
					clause += ` AND COALESCE(` + bLegExpr + `, '') = ?`
					args = append(args, subject.providerBLegID)
				}
				if subject.providerAccountScope != "" {
					clause += ` AND (` + accountExpr + ` = ? OR ` + accountExpr + ` IS NULL OR ` + accountExpr + ` = '')`
					args = append(args, subject.providerAccountScope)
				}
			}
			clauses = append(clauses, clause)
		}
		where += ` AND (` + strings.Join(clauses, ` OR `) + `)`
		args = append(args, remaining+1)
		var rows []economicDetailValuationRow
		if err := s.db.NewRaw(`SELECT subject_kind, subject_id, tenant_id, perspective, basis, canonical_json, created_at_unix, valuation_version FROM (
				SELECT subject_kind, subject_id, tenant_id, perspective, basis, canonical_json, created_at_unix, valuation_version, valuation_id,
					ROW_NUMBER() OVER (
						PARTITION BY `+partition+`
						ORDER BY created_at_unix DESC, valuation_id DESC, valuation_version DESC, id DESC
					) AS stream_rank
				FROM billing_valuations WHERE `+where+`
			) AS latest_valuation_streams
			WHERE stream_rank = 1
			ORDER BY subject_kind, subject_id, tenant_id, perspective, basis, canonical_json
			LIMIT ?`,
			args...).Scan(ctx, &rows); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("billingstore: economic detail valuations load: %w", err)
			}
			continue
		}
		if len(rows) > remaining {
			return nil, fmt.Errorf("%w: valuations exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxValuations)
		}
		for _, row := range rows {
			valuation, err := s.decodeEconomicDetailValuation(row)
			if err != nil {
				return nil, err
			}
			key := billing.EconomicValuationStreamKey(valuation)
			existing, exists := byStream[key]
			latest := row.CreatedAt > existing.createdAt ||
				(row.CreatedAt == existing.createdAt && (valuation.ID > existing.valuation.ID ||
					(valuation.ID == existing.valuation.ID && row.Version > existing.version)))
			if !exists || latest {
				byStream[key] = latestValuation{valuation: valuation, createdAt: row.CreatedAt, version: row.Version}
			}
		}
		loaded += len(rows)
	}
	// The database partition must be exactly as fine as the in-memory stream
	// key. If two selected rows collapse to one key, the boundary did not
	// distinguish a true stream and the whole read fails closed rather than
	// merging it.
	if loaded != len(byStream) {
		return nil, fmt.Errorf("billingstore: economic detail valuation identity mismatch")
	}
	out := make([]economics.Valuation, 0, len(byStream))
	for _, entry := range byStream {
		out = append(out, entry.valuation)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := billing.EconomicValuationStreamKey(out[i]), billing.EconomicValuationStreamKey(out[j])
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// economicDetailValuationStreamSubjectFields lists, in stable order, every
// SubjectRef field that billing.EconomicValuationStreamKey folds into a logical
// stream identity. The names are the canonical JSON tags of the persisted
// subject object, so the SQL extraction and the Go key cannot drift silently.
var economicDetailValuationStreamSubjectFields = []string{
	"kind", "store_id", "tenant_id", "account_id", "a_leg_id", "request_id",
	"billing_call_id", "call_id", "b_leg_id", "attempt_id", "attempt_seq",
	"submission_id", "provider_account_key", "provider_request_id", "provider_charge_id",
	"resource_id", "period_id", "pool_id", "window_id", "statement_id", "statement_line_id",
	"reset_at", "start_at", "end_at",
}

// economicDetailValuationPartitionSQL builds the dialect-correct window
// PARTITION BY clause for the complete canonical SubjectRef identity plus
// perspective and basis. SQLite extracts JSON paths with json_extract and
// PostgreSQL with the native jsonb ->/->> operators; both yield NULL for an
// absent optional field, which partitions identically to the empty value the
// decoded stream key uses.
func economicDetailValuationPartitionSQL(dialectName dialect.Name) (string, error) {
	parts := make([]string, 0, len(economicDetailValuationStreamSubjectFields)+2)
	for _, field := range economicDetailValuationStreamSubjectFields {
		expr, err := economicDetailSubjectFieldExpr(dialectName, field)
		if err != nil {
			return "", fmt.Errorf("billingstore: economic detail valuation partition: %w", err)
		}
		parts = append(parts, expr)
	}
	parts = append(parts, "perspective", "basis")
	return strings.Join(parts, ", "), nil
}

// economicDetailSubjectFieldExpr returns the dialect-correct SQL expression
// that extracts one canonical SubjectRef field from the persisted
// billing_valuations.canonical_json. SQLite uses json_extract and PostgreSQL
// the native jsonb ->/->> operators; both yield NULL for an absent optional
// field, so callers that must treat absence and empty alike apply COALESCE.
func economicDetailSubjectFieldExpr(dialectName dialect.Name, field string) (string, error) {
	switch dialectName {
	case dialect.SQLite:
		return `json_extract(canonical_json, '$.subject.` + field + `')`, nil
	case dialect.PG:
		return `(canonical_json::jsonb -> 'subject' ->> '` + field + `')`, nil
	default:
		return "", fmt.Errorf("unsupported dialect %s", dialectName.String())
	}
}

// economicDetailReconciliationSubjectFieldExpr returns the dialect-correct SQL
// expression that extracts one canonical SubjectRef field from the persisted
// billing_reconciliations.subject_json. Unlike the valuation payload the
// subject is the JSON root here, so the extraction has no subject envelope.
// SQLite uses json_extract and PostgreSQL the native jsonb ->> operator; both
// yield NULL for an absent optional field, so callers that must treat absence
// and empty alike apply COALESCE.
func economicDetailReconciliationSubjectFieldExpr(dialectName dialect.Name, field string) (string, error) {
	switch dialectName {
	case dialect.SQLite:
		return `json_extract(subject_json, '$.` + field + `')`, nil
	case dialect.PG:
		return `(subject_json::jsonb ->> '` + field + `')`, nil
	default:
		return "", fmt.Errorf("unsupported dialect %s", dialectName.String())
	}
}

// economicDetailProviderChargeReconciliationSubjects discovers every distinct
// genuine provider-charge reconciliation subject attributable to the requested
// scope's authoritative B-legs, independent of whether any valuation row
// exists. A request-scoped provider charge is owned by its B-leg, so discovery
// is driven by the in-scope B-leg identities and the trusted account recorded
// in the retained subject. It deliberately reads billing_reconciliations, not
// billing_valuations: a retained provider-charge reconciliation must be
// discoverable even when its stream never produced a valuation.
//
// The returned subjects keep the charge id, owning B-leg and trusted account so
// the later latest-revision read is an exact canonical ownership match that
// still preserves distinct provider-account and tenant streams. The read is
// bounded before any Go growth: the DISTINCT window query carries a
// database-side LIMIT of what is left of the caller-supplied budget, and each
// chunk is deduplicated as rows are scanned. Overflow fails closed with
// billing.ErrEconomicDetailBoundExceeded and is never truncated. A foreign
// account is excluded by the account predicate even when it reuses an in-scope
// charge id and B-leg id; a supplied tenant scope additionally excludes foreign
// tenants. bLegIDs must already be distinct; an empty list discovers nothing.
func (s *DurableStore) economicDetailProviderChargeReconciliationSubjects(ctx context.Context, tenantID, accountID string, bLegIDs []string, budget int) ([]economicDetailSubject, error) {
	if len(bLegIDs) == 0 {
		return nil, nil
	}
	if budget < 0 {
		budget = 0
	}
	dialectName := s.db.Dialect().Name()
	bLegExpr, err := economicDetailReconciliationSubjectFieldExpr(dialectName, "b_leg_id")
	if err != nil {
		return nil, err
	}
	accountExpr, err := economicDetailReconciliationSubjectFieldExpr(dialectName, "account_id")
	if err != nil {
		return nil, err
	}
	out := make([]economicDetailSubject, 0)
	seen := make(map[string]struct{})
	for _, chunk := range chunkStrings(bLegIDs, economicDetailChunkSize) {
		remaining := max(budget-len(out), 0)
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		where := `store_id = ? AND result_schema_version = ? AND subject_kind = ?`
		args := []any{s.storeID, ReconciliationRecordSchemaRetention, string(metering.SubjectProviderCharge)}
		if tenantID != "" {
			where += ` AND tenant_id = ?`
			args = append(args, tenantID)
		}
		where += ` AND COALESCE(` + bLegExpr + `, '') IN (` + placeholders + `)`
		for _, id := range chunk {
			args = append(args, id)
		}
		where += ` AND (` + accountExpr + ` = ? OR ` + accountExpr + ` IS NULL OR ` + accountExpr + ` = '')`
		args = append(args, accountID)
		args = append(args, remaining+1)
		var rows []economicDetailChargeSubjectRow
		if err := s.db.NewRaw(`SELECT DISTINCT subject_id, `+bLegExpr+` AS b_leg_id FROM billing_reconciliations WHERE `+where+` ORDER BY subject_id, b_leg_id LIMIT ?`,
			args...).Scan(ctx, &rows); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("billingstore: economic detail provider-charge reconciliations: %w", err)
			}
			continue
		}
		if len(rows) > remaining {
			return nil, fmt.Errorf("%w: provider-charge reconciliation subjects exceed %d", billing.ErrEconomicDetailBoundExceeded, budget)
		}
		for _, row := range rows {
			key := row.SubjectID + "\x00" + row.BLegID
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, economicDetailSubject{
				kind: metering.SubjectProviderCharge, id: row.SubjectID,
				providerBLegID: row.BLegID, providerAccountScope: accountID,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].id != out[j].id {
			return out[i].id < out[j].id
		}
		return out[i].providerBLegID < out[j].providerBLegID
	})
	return out, nil
}

// economicDetailValuationRow is the persisted identity and payload of one
// latest-revision valuation stream selected at the database boundary.
type economicDetailValuationRow struct {
	SubjectKind string `bun:"subject_kind"`
	SubjectID   string `bun:"subject_id"`
	TenantID    string `bun:"tenant_id"`
	Perspective string `bun:"perspective"`
	Basis       string `bun:"basis"`
	Payload     string `bun:"canonical_json"`
	CreatedAt   int64  `bun:"created_at_unix"`
	Version     int64  `bun:"valuation_version"`
	// InputSetHash is the persisted canonical evidence input identity. It is
	// selected only by the exact selected-valuation load; the latest-per-stream
	// display query leaves it empty and keeps its prior semantics.
	InputSetHash string `bun:"input_set_hash"`
}

// economicDetailSelectedValuationRefs returns the deduplicated immutable
// valuation identities named by the scope's authoritative selected heads. A
// head with no frozen selection contributes nothing; a repeated identity is
// collapsed. Refs carrying an empty id or input hash cannot be resolved and are
// dropped, so they can never be matched by approximation.
func economicDetailSelectedValuationRefs(heads []billing.SelectedCostHead) []billing.SelectedCostValuationRef {
	out := make([]billing.SelectedCostValuationRef, 0, len(heads))
	seen := make(map[string]struct{}, len(heads))
	for _, head := range heads {
		if head.Selected == nil {
			continue
		}
		ref := head.Selected.Ref
		if ref.ValuationID == "" || ref.InputSetHash == "" {
			continue
		}
		key := ref.ValuationID + "\x00" + ref.InputSetHash
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	return out
}

// detailSelectedValuations loads the exact frozen selected valuations named by
// the authoritative durable selected heads, in addition to the latest-per-stream
// display set. A selected head names an immutable valuation identity (id plus
// canonical input-set hash) that latest-per-stream selection may not contain
// once a newer revision of the same logical stream exists; resolving the exact
// revision is required before its payable lines may prove cost inclusion.
//
// The load is bounded before any decode: at most economicDetailMaxValuations
// distinct identities are accepted and each query is chunked. A row is matched
// only when its persisted payload decodes to the requested id and the persisted
// canonical input-set hash equals the requested hash; absent, conflicting or
// ambiguous matches are omitted so coverage fails closed rather than binding an
// unrelated revision. This reader never mutates the display projection.
func (s *DurableStore) detailSelectedValuations(ctx context.Context, tenantID string, refs []billing.SelectedCostValuationRef) ([]economics.Valuation, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	wantHash := make(map[string]string, len(refs))
	ambiguous := make(map[string]struct{})
	for _, ref := range refs {
		if ref.ValuationID == "" || ref.InputSetHash == "" {
			continue
		}
		if existing, ok := wantHash[ref.ValuationID]; ok {
			if existing != ref.InputSetHash {
				ambiguous[ref.ValuationID] = struct{}{}
			}
			continue
		}
		wantHash[ref.ValuationID] = ref.InputSetHash
	}
	for id := range ambiguous {
		delete(wantHash, id)
	}
	if len(wantHash) == 0 {
		return nil, nil
	}
	if len(wantHash) > economicDetailMaxValuations {
		return nil, fmt.Errorf("%w: selected valuations exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxValuations)
	}
	ids := make([]string, 0, len(wantHash))
	for id := range wantHash {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	byID := make(map[string][]economics.Valuation, len(ids))
	loaded := 0
	for _, chunk := range chunkStrings(ids, economicDetailChunkSize) {
		remaining := max(economicDetailMaxValuations-loaded, 0)
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		where := `store_id = ? AND valuation_id IN (` + placeholders + `)`
		args := []any{s.storeID}
		for _, id := range chunk {
			args = append(args, id)
		}
		if tenantID != "" {
			where += ` AND tenant_id = ?`
			args = append(args, tenantID)
		}
		args = append(args, remaining+1)
		var rows []economicDetailValuationRow
		if err := s.db.NewRaw(`SELECT subject_kind, subject_id, tenant_id, perspective, basis, canonical_json, created_at_unix, valuation_version, input_set_hash FROM billing_valuations WHERE `+where+` ORDER BY valuation_id, valuation_version, id LIMIT ?`,
			args...).Scan(ctx, &rows); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("billingstore: economic detail selected valuations load: %w", err)
			}
			continue
		}
		if len(rows) > remaining {
			return nil, fmt.Errorf("%w: selected valuations exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxValuations)
		}
		for _, row := range rows {
			valuation, err := s.decodeEconomicDetailValuation(row)
			if err != nil {
				return nil, err
			}
			byID[valuation.ID] = append(byID[valuation.ID], valuation)
		}
		loaded += len(rows)
	}
	out := make([]economics.Valuation, 0, len(wantHash))
	for _, id := range ids {
		var matched *economics.Valuation
		count := 0
		for i := range byID[id] {
			candidate := byID[id][i]
			if candidate.InputSetHash == "" || candidate.InputSetHash != wantHash[id] {
				continue
			}
			matched = &candidate
			count++
		}
		if count == 1 && matched != nil {
			out = append(out, *matched)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// decodeEconomicDetailValuation decodes one selected valuation payload and
// verifies it still matches the persisted identity columns that selected it,
// so a tampered or inconsistent row fails closed rather than being merged into
// another stream.
func (s *DurableStore) decodeEconomicDetailValuation(row economicDetailValuationRow) (economics.Valuation, error) {
	var valuation economics.Valuation
	if err := json.Unmarshal([]byte(row.Payload), &valuation); err != nil {
		return economics.Valuation{}, fmt.Errorf("billingstore: economic detail valuation record mismatch")
	}
	if string(valuation.Subject.Kind) != row.SubjectKind ||
		subjectIDForEconomics(valuation.Subject) != row.SubjectID ||
		valuation.Subject.TenantID != row.TenantID ||
		string(valuation.Perspective) != row.Perspective ||
		string(valuation.Basis) != row.Basis {
		return economics.Valuation{}, fmt.Errorf("billingstore: economic detail valuation identity mismatch")
	}
	return valuation, nil
}

// detailHeads lists current selected/posted heads for the scope calls in one
// bounded chunked read over the existing account/call index. Every query
// carries LIMIT remaining+1 against one global economicDetailMaxHeads budget,
// so chunking cannot multiply the bound and an over-bound scope fails closed
// with billing.ErrEconomicDetailBoundExceeded instead of materializing the
// excess. Stable order is by call identity then head key; rows decode through
// the canonical head reader and corrupt rows fail closed.
func (s *DurableStore) detailHeads(ctx context.Context, accountID string, callIDs []string) ([]billing.SelectedCostHead, error) {
	var out []billing.SelectedCostHead
	for _, chunk := range chunkStrings(callIDs, economicDetailChunkSize) {
		remaining := max(economicDetailMaxHeads-len(out), 0)
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+3)
		args = append(args, s.storeID, accountID)
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, remaining+1)
		var rows []providerCostHeadRow
		if err := s.db.NewRaw(providerCostHeadSelect+` WHERE store_id = ? AND account_id = ? AND call_id IN (`+placeholders+`) ORDER BY call_id, head_key LIMIT ?`,
			args...).Scan(ctx, &rows); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("billingstore: economic detail heads load: %w", err)
			}
			continue
		}
		if len(rows) > remaining {
			return nil, fmt.Errorf("%w: heads exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxHeads)
		}
		for _, row := range rows {
			head, err := s.selectedCostHeadFromRow(row)
			if err != nil {
				return nil, fmt.Errorf("billingstore: economic detail head record mismatch")
			}
			out = append(out, head)
		}
	}
	return out, nil
}

// reconciliationRetentionRef is one selected latest reconciliation revision, by
// its immutable durable identity.
type reconciliationRetentionRef struct {
	ID      string `bun:"reconciliation_id"`
	Version int64  `bun:"reconciliation_version"`
}

// detailReconciliations loads the newest full-result retention revision for
// every distinct authoritative SubjectRef identity of the requested scope and
// preserves each identity as an independent reconciliation comparison.
//
// Revision selection happens at the database boundary and partitions on the
// persisted canonical subject JSON, not on the coarse (kind, primary id,
// optional tenant) tuple a single latest probe can address: two retentions that
// share a BLegID but differ in provider-charge, provider-account, attempt or
// tenant lineage are distinct subjects, not revisions of one stream. Every
// field accepted by the canonical subject validator is part of that persisted
// identity. Selection is bounded before any Go growth: the window query carries
// a database-side LIMIT of what is left of one global
// billing.MaxEconomicDetailReconciliations budget across subject chunks, so a
// scope whose distinct identities exceed the budget fails closed with
// billing.ErrEconomicDetailBoundExceeded instead of materializing the excess.
// Each selected identity is then loaded through the authoritative retention
// reader. Independent subjects are never collapsed, merged, summed or
// FX-converted.
func (s *DurableStore) detailReconciliations(ctx context.Context, tenantID string, subjects []economicDetailSubject) ([]billing.EconomicDetailReconciliation, error) {
	if len(subjects) == 0 {
		return nil, nil
	}
	if len(subjects) > billing.MaxEconomicDetailReconciliations {
		return nil, fmt.Errorf("%w: reconciliation subjects=%d max=%d", billing.ErrEconomicDetailBoundExceeded, len(subjects), billing.MaxEconomicDetailReconciliations)
	}
	latest, err := s.latestReconciliationsForSubjects(ctx, tenantID, subjects)
	if err != nil {
		return nil, err
	}
	out := make([]billing.EconomicDetailReconciliation, 0, len(latest))
	for _, ref := range latest {
		result, err := s.GetReconciliationRetention(ctx, ref.ID, uint64(ref.Version))
		if err != nil {
			return nil, fmt.Errorf("billingstore: economic detail reconciliation load: %w", err)
		}
		entry := billing.EconomicDetailReconciliation{Subject: result.Subject.Clone()}
		if result.Quantity != nil {
			quantity := *result.Quantity
			entry.Quantity = &quantity
		}
		if result.Monetary != nil {
			monetary := *result.Monetary
			entry.Monetary = &monetary
		}
		if result.Aggregate != nil {
			aggregate := *result.Aggregate
			entry.Aggregate = &aggregate
		}
		out = append(out, entry)
	}
	return out, nil
}

// latestReconciliationsForSubjects selects the latest retention revision of
// every distinct full SubjectRef identity belonging to the requested scope
// subjects. Identity is the persisted canonical subject JSON, so every accepted
// subject field participates and distinct identities sharing one primary id
// never collapse. The optional tenant narrows the same identity space.
//
// A discovered provider-charge subject additionally pins its authoritative
// B-leg ownership and trusted account from the persisted subject JSON. The
// charge id alone is not a retention identity (another B-leg or account can
// reuse it), so the exact-match clause must carry the same ownership the
// discovery proved; an account-less legacy row stays selectable, exactly as the
// canonical subject validator treats an absent account as in-scope.
//
// The result is bounded before materialization: each chunk query carries LIMIT
// remaining+1 against one global billing.MaxEconomicDetailReconciliations
// budget, and overflow fails closed with
// billing.ErrEconomicDetailBoundExceeded rather than truncating.
func (s *DurableStore) latestReconciliationsForSubjects(ctx context.Context, tenantID string, subjects []economicDetailSubject) ([]reconciliationRetentionRef, error) {
	dialectName := s.db.Dialect().Name()
	bLegExpr, err := economicDetailReconciliationSubjectFieldExpr(dialectName, "b_leg_id")
	if err != nil {
		return nil, err
	}
	accountExpr, err := economicDetailReconciliationSubjectFieldExpr(dialectName, "account_id")
	if err != nil {
		return nil, err
	}
	out := make([]reconciliationRetentionRef, 0, len(subjects))
	for _, chunk := range chunkEconomicDetailSubjects(subjects, economicDetailChunkSize) {
		remaining := max(billing.MaxEconomicDetailReconciliations-len(out), 0)
		where := `store_id = ? AND result_schema_version = ?`
		args := []any{s.storeID, ReconciliationRecordSchemaRetention}
		if tenantID != "" {
			where += ` AND tenant_id = ?`
			args = append(args, tenantID)
		}
		clauses := make([]string, 0, len(chunk))
		for _, subject := range chunk {
			clause := `(subject_kind = ? AND subject_id = ?)`
			args = append(args, string(subject.kind), subject.id)
			if subject.kind == metering.SubjectProviderCharge {
				if subject.providerBLegID != "" {
					clause += ` AND COALESCE(` + bLegExpr + `, '') = ?`
					args = append(args, subject.providerBLegID)
				}
				if subject.providerAccountScope != "" {
					clause += ` AND (` + accountExpr + ` = ? OR ` + accountExpr + ` IS NULL OR ` + accountExpr + ` = '')`
					args = append(args, subject.providerAccountScope)
				}
			}
			clauses = append(clauses, clause)
		}
		where += ` AND (` + strings.Join(clauses, ` OR `) + `)`
		args = append(args, remaining+1)
		var rows []reconciliationRetentionRef
		if err := s.db.NewRaw(`SELECT reconciliation_id, reconciliation_version FROM (
				SELECT reconciliation_id, reconciliation_version,
					ROW_NUMBER() OVER (
						PARTITION BY subject_json
						ORDER BY created_at_unix DESC, reconciliation_id DESC, reconciliation_version DESC, id DESC
					) AS stream_rank
				FROM billing_reconciliations WHERE `+where+`
			) AS latest_reconciliation_streams
			WHERE stream_rank = 1
			ORDER BY reconciliation_id, reconciliation_version
			LIMIT ?`,
			args...).Scan(ctx, &rows); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("billingstore: economic detail reconciliations load: %w", err)
			}
			continue
		}
		if len(rows) > remaining {
			return nil, fmt.Errorf("%w: reconciliations exceed %d", billing.ErrEconomicDetailBoundExceeded, billing.MaxEconomicDetailReconciliations)
		}
		out = append(out, rows...)
	}
	return out, nil
}

// loadEconomicDetailCallJournals reads one call's financial journal history
// bounded before any decode or secondary journal-entry load. The query carries
// the canonical deterministic ORDER BY plus a database-side LIMIT of
// economicDetailMaxJournalTransactions+1; more than the budget fails closed
// with billing.ErrEconomicDetailBoundExceeded and is never materialized into
// Go slices or expanded by loadJournals. A scope within the budget keeps
// exactly the prior row order and summary semantics.
func (s *DurableStore) loadEconomicDetailCallJournals(ctx context.Context, accountID, callID string) ([]billing.JournalTransaction, error) {
	rows, err := loadCallJournalRows(ctx, s.db, accountID, callID, economicDetailMaxJournalTransactions+1)
	if err != nil {
		return nil, err
	}
	if len(rows) > economicDetailMaxJournalTransactions {
		return nil, fmt.Errorf("%w: call journal transactions exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxJournalTransactions)
	}
	return loadJournals(ctx, s.db, rows)
}

// loadEconomicDetailCallOperationSnapshots reads one call's settlement
// operation snapshots bounded before any slice growth. The query carries the
// canonical deterministic ORDER BY plus a database-side LIMIT of
// economicDetailMaxOperationSnapshots+1; more than the budget fails closed with
// billing.ErrEconomicDetailBoundExceeded instead of building unbounded
// customer/provider slices. A scope within the budget is projected exactly as
// before.
func (s *DurableStore) loadEconomicDetailCallOperationSnapshots(ctx context.Context, accountID string, callID billing.BillingCallID, legs []billing.CallLegUsageRecord) ([]billing.OperationSnapshot, []billing.OperationSnapshot, error) {
	rows, err := loadCallOperationSnapshotRows(ctx, s.db, accountID, callID, legs, economicDetailMaxOperationSnapshots+1)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) > economicDetailMaxOperationSnapshots {
		return nil, nil, fmt.Errorf("%w: call operation snapshots exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxOperationSnapshots)
	}
	customer, provider := operationSnapshotsFromRows(rows)
	return customer, provider, nil
}

// detailTurnSummary rebuilds the call settlement summary from durable
// journals and operation snapshots with the same revenue/cost/processed
// semantics as CallExplanation. No settlement evidence means no summary,
// never a zero summary. Both independent correction histories are bounded
// database-side before decode/secondary loads (Finding 4): an over-budget
// history fails closed with billing.ErrEconomicDetailBoundExceeded rather than
// accumulating an arbitrarily long journal or snapshot set while every other
// detail cap stays under limit.
func (s *DurableStore) detailTurnSummary(ctx context.Context, accountID string, callID billing.BillingCallID, legs []billing.CallLegUsageRecord) (*billing.TurnResultSummary, error) {
	account, err := getAccountTx(ctx, s.db, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("billingstore: economic detail account load: %w", err)
	}
	transactions, err := s.loadEconomicDetailCallJournals(ctx, accountID, callID.String())
	if err != nil {
		return nil, fmt.Errorf("billingstore: economic detail journals load: %w", err)
	}
	customerOps, _, err := s.loadEconomicDetailCallOperationSnapshots(ctx, accountID, callID, legs)
	if err != nil {
		return nil, fmt.Errorf("billingstore: economic detail operations load: %w", err)
	}
	if len(transactions) == 0 && len(customerOps) == 0 {
		return nil, nil
	}
	revenue, cost, _ := billing.SummarizeJournalForReport(transactions, account.Currency)
	margin, err := billing.ReportMargin(account.Currency, revenue, cost)
	if err != nil {
		return nil, fmt.Errorf("billingstore: economic detail margin: %w", err)
	}
	processed := false
	for _, op := range customerOps {
		if op.OperationKind == "customer_call_settlement" || op.OperationKind == "customer_no_charge_repair" {
			processed = true
			break
		}
	}
	return &billing.TurnResultSummary{
		CustomerCharge: billing.Money{Currency: account.Currency, Nano: revenue},
		ProviderCost:   billing.Money{Currency: account.Currency, Nano: cost},
		GrossMargin:    margin, Processed: processed,
	}, nil
}

// detailAllocations is the line-only projection of detailAllocationResult for
// callers that need the effective in-scope allocation lines and not the
// correction state. Discovery is intentionally broader than attribution: an
// envelope matches as soon as any one of its targets is in scope, and a single
// conserved envelope may also allocate to a target of another call, leg,
// account or tenant, so rolled lines are restricted to authoritative
// membership in the requested scope before they are returned. This prevents a
// shared allocation from exposing a foreign target's identity or attributed
// amount.
//
// Bounds (Finding 5B3/Finding 3): every step is bounded before it can grow.
// Discovery pushes one global distinct-envelope budget into SQL (stable
// deterministic ORDER BY plus LIMIT remaining+1 per target-ID chunk) and
// excludes envelopes already found, so a cross-chunk duplicate can neither
// consume the budget nor hide a later unique envelope. The supersession
// closure is one bounded same-source batch query with a database-side LIMIT,
// and before any rollup slice grows the cumulative potential line output (each
// target, including the unallocated remainder, becomes one line) is checked
// with overflow-safe arithmetic against billing.MaxEconomicDetailAllocations.
// Any overflow fails closed with billing.ErrEconomicDetailBoundExceeded;
// nothing is truncated.
func (s *DurableStore) detailAllocations(ctx context.Context, query billing.EconomicDetailQuery, callIDs, bLegIDs []string) ([]billing.AllocatedCostLine, error) {
	result, err := s.detailAllocationResult(ctx, query, callIDs, bLegIDs)
	if err != nil {
		return nil, err
	}
	return result.Lines, nil
}

// economicDetailAllocationResult couples the effective in-scope allocation
// lines with the truthful correction state of the lineage that produced them.
// The legacy line-only caller is preserved as detailAllocations, while the
// durable read adapter uses both fields so pending ancestry, retired
// predecessors and redaction are never discarded.
type economicDetailAllocationResult struct {
	Lines []billing.AllocatedCostLine
	State *billing.EconomicDetailAllocationState
}

// detailAllocationResult resolves the bounded authoritative allocation lineage
// attributable to the requested scope and returns both its effective in-scope
// lines and its correction state.
//
// Discovery finds every envelope that directly names a requested target. A
// conserved supersession edge always connects records of one canonical source
// subject, so loading every allocation of those source subjects is a complete
// bounded closure: it necessarily contains the predecessor and every
// replacement, including a replacement that reallocated the source entirely
// away from the requested target. The closure is then restricted to its
// connected components (unrelated same-source lineages never taint the state)
// and resolved by the canonical conserved allocator, so a target-moving
// replacement retires its predecessor instead of leaving it live.
//
// Components whose source ownership is foreign to the requested account/tenant
// are dropped entirely. Account-less (unattributed) components are retained but
// their full source aggregate and remainder are redacted, since the reader
// cannot prove those economics belong to the requesting account. Every bound
// fails closed with billing.ErrEconomicDetailBoundExceeded; corrupt or cyclic
// lineage fails closed without echoing raw bytes.
func (s *DurableStore) detailAllocationResult(ctx context.Context, query billing.EconomicDetailQuery, callIDs, bLegIDs []string) (economicDetailAllocationResult, error) {
	targetIDs := append(append([]string{}, callIDs...), bLegIDs...)
	if len(targetIDs) == 0 {
		return economicDetailAllocationResult{}, nil
	}
	candidates, err := s.discoverAllocationCandidates(ctx, targetIDs)
	if err != nil {
		return economicDetailAllocationResult{}, err
	}
	if len(candidates) == 0 {
		return economicDetailAllocationResult{}, nil
	}
	records, err := s.loadAllocationSupersessionClosure(ctx, candidates)
	if err != nil {
		return economicDetailAllocationResult{}, err
	}
	component := allocationSupersessionComponent(candidates, records)
	visible := make([]economics.AllocationRecord, 0, len(component))
	for _, record := range component {
		if allocationRecordVisibleToScope(record, query) {
			visible = append(visible, record)
		}
	}
	if len(visible) == 0 {
		return economicDetailAllocationResult{}, nil
	}
	if err := checkAllocationLineBudget(visible); err != nil {
		return economicDetailAllocationResult{}, err
	}
	rolled, err := billing.RollupAllocatedCostsDetailed(visible)
	if err != nil {
		return economicDetailAllocationResult{}, fmt.Errorf("billingstore: economic detail allocation lineage invalid")
	}
	state := &billing.EconomicDetailAllocationState{
		Status: rolled.Status, Complete: rolled.Complete, Payable: rolled.Payable,
		Pending:           append([]economics.AllocationRef(nil), rolled.Pending...),
		PendingSupersedes: append([]economics.AllocationRef(nil), rolled.PendingSupersedes...),
		Superseded:        append([]economics.AllocationRef(nil), rolled.Superseded...),
		Redacted:          allocationLineageNeedsRedaction(visible),
	}
	return economicDetailAllocationResult{
		Lines: scopeAllocationLines(rolled.Lines, query, callIDs, bLegIDs),
		State: state,
	}, nil
}

// allocationRecordVisibleToScope reports whether one authoritative allocation
// record may participate in the requested scope. An explicitly foreign account
// or tenant is excluded; an account-less legacy row stays eligible but its
// economics are later redacted because ownership cannot be proven.
func allocationRecordVisibleToScope(record economics.AllocationRecord, query billing.EconomicDetailQuery) bool {
	subject := record.SourceSubject
	if subject.AccountID != "" && subject.AccountID != query.AccountID {
		return false
	}
	if query.TenantID != "" && subject.TenantID != "" && subject.TenantID != query.TenantID {
		return false
	}
	return true
}

// allocationLineageNeedsRedaction reports whether any retained record is
// account-less, meaning its full source aggregate and remainder cannot be
// proven to belong to the requesting account.
func allocationLineageNeedsRedaction(records []economics.AllocationRecord) bool {
	for _, record := range records {
		if record.SourceSubject.AccountID == "" {
			return true
		}
	}
	return false
}

// allocationSourceAuthorized reports whether one line's full source economics
// may be exposed to the requesting scope. Only an explicit same-account (and
// matching-tenant) source is authorized; an account-less source is withheld.
func allocationSourceAuthorized(subject metering.SubjectRef, query billing.EconomicDetailQuery) bool {
	if subject.AccountID == "" || subject.AccountID != query.AccountID {
		return false
	}
	if query.TenantID != "" && subject.TenantID != "" && subject.TenantID != query.TenantID {
		return false
	}
	return true
}

// allocationSupersessionComponent returns the bounded connected component(s) of
// the loaded supersession closure that contain at least one discovered
// candidate. Edges are followed in both directions because a successor may be
// loaded before or after its predecessor. Unrelated same-source records that no
// edge connects to a candidate are excluded so they can never taint the
// correction state.
func allocationSupersessionComponent(candidates []economicDetailAllocationCandidate, records []economics.AllocationRecord) []economics.AllocationRecord {
	if len(records) == 0 {
		return nil
	}
	byKey := make(map[string]economics.AllocationRecord, len(records))
	for _, record := range records {
		byKey[allocationNodeKey(record.ID, record.Version)] = record
	}
	neighbors := make(map[string][]string, len(records))
	for _, record := range records {
		from := allocationNodeKey(record.ID, record.Version)
		for _, ref := range record.Supersedes {
			to := allocationNodeKey(ref.AllocationID, ref.Version)
			if _, ok := byKey[to]; !ok {
				continue
			}
			neighbors[from] = append(neighbors[from], to)
			neighbors[to] = append(neighbors[to], from)
		}
	}
	visited := make(map[string]struct{}, len(records))
	queue := make([]string, 0, len(records))
	for _, candidate := range candidates {
		key := allocationNodeKey(candidate.AllocationID, uint64(candidate.AllocationVersion))
		if _, ok := byKey[key]; !ok {
			continue
		}
		if _, seen := visited[key]; seen {
			continue
		}
		visited[key] = struct{}{}
		queue = append(queue, key)
	}
	for len(queue) > 0 {
		key := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, next := range neighbors[key] {
			if _, seen := visited[next]; seen {
				continue
			}
			visited[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	out := make([]economics.AllocationRecord, 0, len(visited))
	for key := range visited {
		out = append(out, byKey[key])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func allocationNodeKey(id string, version uint64) string {
	return id + "\x00" + fmt.Sprint(version)
}

// economicDetailAllocationCandidate is one distinct allocation envelope
// identity discovered as a target of the requested scope.
type economicDetailAllocationCandidate struct {
	AllocationID      string `bun:"allocation_id"`
	AllocationVersion int64  `bun:"allocation_version"`
}

// economicDetailAllocationEnvelopeRow is one loaded canonical envelope payload
// together with the identity columns that selected it.
type economicDetailAllocationEnvelopeRow struct {
	AllocationID      string `bun:"allocation_id"`
	AllocationVersion int64  `bun:"allocation_version"`
	Payload           string `bun:"canonical_json"`
}

// discoverAllocationCandidates returns the distinct in-store allocation
// envelopes that target any requested subject, in stable order. Each chunk
// query carries a database-side LIMIT against what is left of the one global
// distinct-envelope budget and explicitly excludes every already-discovered
// identity, so chunking cannot multiply the budget and a duplicate spanning
// chunks cannot falsely overflow it or hide a later unique envelope. Overflow
// fails closed; it is never truncated silently.
func (s *DurableStore) discoverAllocationCandidates(ctx context.Context, targetIDs []string) ([]economicDetailAllocationCandidate, error) {
	out := make([]economicDetailAllocationCandidate, 0, economicDetailMaxAllocationRecords)
	for _, chunk := range chunkStrings(targetIDs, economicDetailChunkSize) {
		remaining := max(economicDetailMaxAllocationRecords-len(out), 0)
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+2*len(out)+2)
		args = append(args, s.storeID)
		for _, id := range chunk {
			args = append(args, id)
		}
		exclusion := ""
		if len(out) > 0 {
			clauses := make([]string, 0, len(out))
			for _, candidate := range out {
				clauses = append(clauses, `(allocation_id = ? AND allocation_version = ?)`)
				args = append(args, candidate.AllocationID, candidate.AllocationVersion)
			}
			exclusion = ` AND NOT (` + strings.Join(clauses, ` OR `) + `)`
		}
		args = append(args, remaining+1)
		var rows []economicDetailAllocationCandidate
		if err := s.db.NewRaw(`SELECT DISTINCT allocation_id, allocation_version FROM billing_allocation_targets WHERE store_id = ? AND target_subject_id IN (`+placeholders+`)`+exclusion+` ORDER BY allocation_id, allocation_version LIMIT ?`,
			args...).Scan(ctx, &rows); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("billingstore: economic detail allocation targets: %w", err)
			}
			continue
		}
		if len(rows) > remaining {
			return nil, fmt.Errorf("%w: allocation envelopes exceed %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxAllocationRecords)
		}
		out = append(out, rows...)
	}
	return out, nil
}

// loadAllocationSupersessionClosure loads, in one indexed batch query, the
// discovered candidate envelopes together with every allocation that shares a
// candidate's canonical source subject. A conserved supersession edge always
// connects records of one source subject, so this same-source superset contains
// the entire authoritative lineage of every candidate: predecessors, resolved
// successors and replacements that reallocated the source to a different
// target. Predecessors that were never persisted simply remain absent and are
// surfaced as pending by the canonical resolver.
//
// The read is bounded before any Go growth: the query carries a database-side
// LIMIT of one global economicDetailMaxAllocationRecords budget plus one, so a
// source subject with an over-bound lineage fails closed with
// billing.ErrEconomicDetailBoundExceeded instead of materializing the excess.
// Projection rows are deliberately not reassembled; each payload is
// re-canonicalized through the conserved envelope contract and must still name
// the identity column that selected it. A missing candidate or inconsistent row
// fails closed without echoing raw bytes.
func (s *DurableStore) loadAllocationSupersessionClosure(ctx context.Context, candidates []economicDetailAllocationCandidate) ([]economics.AllocationRecord, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	tuples := make([]string, 0, len(candidates))
	args := make([]any, 0, 2*len(candidates)+3)
	args = append(args, s.storeID, s.storeID)
	for _, candidate := range candidates {
		tuples = append(tuples, `(?,?)`)
		args = append(args, candidate.AllocationID, candidate.AllocationVersion)
	}
	args = append(args, economicDetailMaxAllocationRecords+1)
	var rows []economicDetailAllocationEnvelopeRow
	err := s.db.NewRaw(`SELECT allocation_id, allocation_version, canonical_json FROM billing_allocations
		WHERE store_id = ?
		  AND source_subject_json IN (
		      SELECT seed.source_subject_json FROM billing_allocations seed
		      WHERE seed.store_id = ? AND (seed.allocation_id, seed.allocation_version) IN (`+strings.Join(tuples, `,`)+`)
		  )
		ORDER BY allocation_id, allocation_version LIMIT ?`,
		args...).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("billingstore: economic detail allocation load: %w", err)
	}
	if len(rows) > economicDetailMaxAllocationRecords {
		return nil, fmt.Errorf("%w: allocation lineage exceeds %d", billing.ErrEconomicDetailBoundExceeded, economicDetailMaxAllocationRecords)
	}
	byKey := make(map[string]economics.AllocationRecord, len(rows))
	for _, row := range rows {
		if row.AllocationVersion <= 0 {
			return nil, fmt.Errorf("billingstore: economic detail allocation record mismatch")
		}
		var record economics.AllocationRecord
		if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
			return nil, fmt.Errorf("billingstore: economic detail allocation record mismatch")
		}
		canonical, err := record.Canonical()
		if err != nil {
			return nil, fmt.Errorf("billingstore: economic detail allocation record mismatch")
		}
		if canonical.ID != row.AllocationID || canonical.Version != uint64(row.AllocationVersion) {
			return nil, fmt.Errorf("billingstore: economic detail allocation record mismatch")
		}
		byKey[row.AllocationID+"\x00"+fmt.Sprint(row.AllocationVersion)] = canonical
	}
	records := make([]economics.AllocationRecord, 0, len(rows))
	for _, row := range rows {
		record, ok := byKey[row.AllocationID+"\x00"+fmt.Sprint(row.AllocationVersion)]
		if !ok {
			return nil, fmt.Errorf("billingstore: economic detail allocation record mismatch")
		}
		records = append(records, record)
	}
	for _, candidate := range candidates {
		if _, ok := byKey[candidate.AllocationID+"\x00"+fmt.Sprint(candidate.AllocationVersion)]; !ok {
			return nil, fmt.Errorf("billingstore: economic detail allocation record mismatch")
		}
	}
	return records, nil
}

// checkAllocationLineBudget fails closed before a rollup slice grows when the
// loaded envelopes could emit more lines than the detail allocation line
// bound. Every target of every canonical record, including the unallocated
// remainder, becomes exactly one rolled line, so the sum is a faithful
// potential-output count. The subtraction keeps the comparison overflow-free
// and no slice is preallocated from a record-controlled length.
func checkAllocationLineBudget(records []economics.AllocationRecord) error {
	potential := 0
	for _, record := range records {
		if len(record.Targets) > billing.MaxEconomicDetailAllocations-potential {
			return fmt.Errorf("%w: allocation lines exceed %d", billing.ErrEconomicDetailBoundExceeded, billing.MaxEconomicDetailAllocations)
		}
		potential += len(record.Targets)
	}
	return nil
}

// scopeAllocationLines restricts rolled allocation contributions to targets
// that authoritatively belong to the requested economic-detail scope. Line
// identity, source subject, exact share and rounded amount are copied
// verbatim except when the source ownership is unauthorized, in which case the
// aggregate-derived economics are redacted in place. Foreign lines are omitted,
// so no allocation, value or total is invented; a retained redacted line keeps
// only auditable identity/share data.
//
// Membership is decided from the stored target subject identity against the
// requested call/B-leg traversal, never from co-location inside a shared
// envelope and never from the target's self-declared account:
//   - a billing-call target is in scope only when its call identity is one of
//     the requested calls;
//   - a B-leg target is in scope only when its leg identity is one of the
//     requested legs and any declared call identity agrees;
//   - an explicitly foreign account or tenant is rejected as defense in depth
//     (the canonical envelope already forbids mixed ownership);
//   - the unallocated remainder is retained when the allocation has at least
//     one in-scope member, preserving its conservation audit reference
//     without surfacing an unrelated envelope;
//   - an account-less (unattributed) source keeps its membership identity but
//     has its full source aggregate, source quantity and rounded amount
//     redacted, because those economics cannot be proven to belong to the
//     requesting account.
func scopeAllocationLines(lines []billing.AllocatedCostLine, query billing.EconomicDetailQuery, callIDs, bLegIDs []string) []billing.AllocatedCostLine {
	if len(lines) == 0 {
		return nil
	}
	calls := make(map[string]struct{}, len(callIDs))
	for _, id := range callIDs {
		calls[id] = struct{}{}
	}
	legs := make(map[string]struct{}, len(bLegIDs))
	for _, id := range bLegIDs {
		legs[id] = struct{}{}
	}
	member := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if allocationMemberTargetInScope(line, query, calls, legs) {
			member[allocationLineIdentity(line)] = struct{}{}
		}
	}
	out := make([]billing.AllocatedCostLine, 0, len(lines))
	for _, line := range lines {
		if line.Unallocated {
			if _, ok := member[allocationLineIdentity(line)]; ok {
				out = append(out, redactUnauthorizedAllocationLine(line, query))
			}
			continue
		}
		if allocationMemberTargetInScope(line, query, calls, legs) {
			out = append(out, redactUnauthorizedAllocationLine(line, query))
		}
	}
	return out
}

// redactUnauthorizedAllocationLine withholds the full source aggregate, exact
// source quantity and rounded amounts when the source ownership cannot be
// proven to belong to the requesting account/tenant. The authoritative
// membership identity, policy reference and exact shares survive so authorized
// conservation remains auditable through a safe reference instead of the
// withheld aggregate economics.
func redactUnauthorizedAllocationLine(line billing.AllocatedCostLine, query billing.EconomicDetailQuery) billing.AllocatedCostLine {
	if allocationSourceAuthorized(line.SourceSubject, query) {
		return line
	}
	line.Redacted = true
	line.SourceAmount = nil
	line.SourceQuantity = nil
	line.RoundedAmount = nil
	return line
}

func allocationLineIdentity(line billing.AllocatedCostLine) string {
	return line.AllocationID + "\x00" + fmt.Sprint(line.AllocationVersion)
}

func allocationMemberTargetInScope(line billing.AllocatedCostLine, query billing.EconomicDetailQuery, calls, legs map[string]struct{}) bool {
	target := line.Target
	if target.StoreID != "" && target.StoreID != query.StoreID {
		return false
	}
	if target.AccountID != "" && target.AccountID != query.AccountID {
		return false
	}
	if query.TenantID != "" && target.TenantID != "" && target.TenantID != query.TenantID {
		return false
	}
	switch target.Kind {
	case metering.SubjectBillingCall:
		call := target.BillingCallID
		if call == "" {
			call = target.CallID
		}
		if call == "" {
			return false
		}
		_, ok := calls[call]
		return ok
	case metering.SubjectBLeg:
		if target.BLegID == "" {
			return false
		}
		if _, ok := legs[target.BLegID]; !ok {
			return false
		}
		call := target.BillingCallID
		if call == "" {
			call = target.CallID
		}
		if call == "" {
			return true
		}
		_, ok := calls[call]
		return ok
	default:
		return false
	}
}

func chunkStrings(in []string, size int) [][]string {
	if size <= 0 {
		size = economicDetailChunkSize
	}
	var out [][]string
	for len(in) > 0 {
		n := min(size, len(in))
		out = append(out, in[:n])
		in = in[n:]
	}
	return out
}

func chunkEconomicDetailSubjects(in []economicDetailSubject, size int) [][]economicDetailSubject {
	if size <= 0 {
		size = economicDetailChunkSize
	}
	var out [][]economicDetailSubject
	for len(in) > 0 {
		n := min(size, len(in))
		out = append(out, in[:n])
		in = in[n:]
	}
	return out
}
