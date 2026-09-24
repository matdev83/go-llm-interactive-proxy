package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// EconomicDetailReader is the durable query port for scoped call/A-leg
// economic detail. Implementations load persisted sources and assemble them
// through AssembleEconomicDetail; they perform no rating or posting.
type EconomicDetailReader interface {
	QueryEconomicDetail(context.Context, EconomicDetailQuery) (EconomicDetail, error)
}

// Task 16.1A scoped call and A-leg economic detail query contract and
// pure/domain assembly.
//
// The assembler is pure: it validates explicit tenant/store/account/call/A-leg
// scope, preserves source-separated E/Q/P/S/R planes without collapsing them,
// projects native-currency totals with an explicit incomplete margin, and
// echoes existing summary projections verbatim. It performs no SQL, no HTTP,
// no rating and no posting. Storage (16.1B) and control surface (16.2) build
// on this contract later.
//
// Public reader seams are reused where the approved design requires public
// types (metering.Observation, economics.Valuation, reconciliation/selection
// identities); the domain read model itself stays in internal/core/billing.

var (
	// ErrEconomicDetailInvalid identifies a malformed query or input set.
	ErrEconomicDetailInvalid = errors.New("billing: invalid economic detail query")
	// ErrEconomicDetailScopeMismatch identifies evidence outside the trusted
	// query scope. Assembly fails closed rather than mixing scopes.
	ErrEconomicDetailScopeMismatch = errors.New("billing: economic detail scope mismatch")
	// ErrEconomicDetailBoundExceeded identifies an input or result that exceeds
	// the bounded detail cardinality. Exceeding a bound fails closed and never
	// truncates silently except for the explicit Limit page.
	ErrEconomicDetailBoundExceeded = errors.New("billing: economic detail bound exceeded")
)

const (
	// EconomicDetailDefaultLimit bounds one detail page when the caller omits a limit.
	EconomicDetailDefaultLimit = 100
	// EconomicDetailMaxLimit is the hard bounded page maximum.
	EconomicDetailMaxLimit = 500
	// MaxEconomicDetailObservations bounds one assembly input set.
	MaxEconomicDetailObservations = 1024
	// MaxEconomicDetailValuations bounds retained valuation contributions per
	// scope. A scope may legitimately gather one latest revision per logical
	// subject/perspective/basis stream across many calls and B-legs, so this
	// matches the observation bound instead of a single-plane count; exceeding
	// it still fails closed.
	MaxEconomicDetailValuations = 1024
	// MaxEconomicDetailMissingRefs bounds retained missing evidence refs.
	MaxEconomicDetailMissingRefs = 1024
	// MaxEconomicDetailCoverageRefs bounds retained coverage edges.
	MaxEconomicDetailCoverageRefs = 1024
	// MaxEconomicDetailHeads bounds retained selected-cost heads.
	MaxEconomicDetailHeads = 128
	// MaxEconomicDetailAllocations bounds retained allocation lines.
	MaxEconomicDetailAllocations = 256
	// MaxEconomicDetailReconciliations bounds the independent reconciliation
	// comparisons one detail page may carry: one per authoritative in-scope
	// subject (primary call/A-leg plus its calls and B-legs). Exceeding it fails
	// closed rather than silently dropping later child subjects, so a
	// reconciliation present only on a later subject is never lost without an
	// explicit error.
	MaxEconomicDetailReconciliations = 128
	// MaxEconomicDetailExecutionLegs bounds the authoritative executed B-leg
	// execution facts one detail page may carry into assembly. It reuses the
	// same finite leg-record budget the durable reader already enforces, so a
	// scope above it fails closed before any Go growth instead of materializing
	// the excess.
	MaxEconomicDetailExecutionLegs = MaxEconomicDetailReconciliations
	// MaxEconomicDetailJournalTransactions bounds the financial journal rows
	// one call summary may materialize. A call's correction history is an
	// operator-query result set, so it shares the public economic-detail page
	// ceiling instead of being unbounded; exceeding it fails closed before any
	// secondary journal-entry load.
	MaxEconomicDetailJournalTransactions = EconomicDetailMaxLimit
	// MaxEconomicDetailOperationSnapshots bounds the settlement operation
	// snapshots one call summary may materialize. It mirrors the journal bound
	// so both independent correction histories are finite before Go growth.
	MaxEconomicDetailOperationSnapshots = EconomicDetailMaxLimit
	// EconomicDetailObservationOrder names the limit-independent canonical
	// observation ordering of an economic-detail page. It is part of the
	// continuation contract: a durable cursor binds this exact definition, so
	// changing the ordering invalidates previously issued cursors instead of
	// silently skipping or repeating observations.
	EconomicDetailObservationOrder = "economic-detail-observation-v1"
	// EconomicDetailSnapshotVersion names the exact deterministic
	// full-scope-snapshot fingerprint definition. It is prefixed onto every
	// fingerprint, so a future fingerprint-shape change invalidates outstanding
	// continuations by mismatch instead of silently accepting a token issued
	// against a different snapshot definition. v2 folds each reconciliation's
	// aggregate plane into the repeated full-scope fact set.
	EconomicDetailSnapshotVersion = "economic-detail-snapshot-v2"
)

// EconomicDetailSnapshotContinuationEncoder is implemented by durable readers
// that authenticate and produce economic-detail continuation tokens. A reader
// composed above the durable adapter (for example the statement-evidence
// wrapper) uses it to bind facts it resolves after the durable read into the
// same authenticated snapshot boundary, so one continuation never crosses two
// different full-scope snapshots.
type EconomicDetailSnapshotContinuationEncoder interface {
	EncodeEconomicDetailSnapshotContinuation(query EconomicDetailQuery, position EconomicObservationPosition, baseFingerprint, outerFingerprint string) string
}

// EconomicDetailQuery scopes one call or A-leg economic detail page to
// StoreID + AccountID plus exactly one primary continuity identity. A call
// query carries BillingCallID with an optional ALegID continuity check; an
// A-leg query carries ALegID only. TenantID is an optional additional trust
// bound. Limit bounds returned observations per page. Cursor is the opaque,
// durable-store-authenticated continuation position echoed from a previous
// page's NextCursor; assembly never interprets it, and the durable adapter
// rejects a tampered, cross-scope or cross-kind token before it is trusted.
// A continuation is valid only while the full-scope snapshot it was issued
// against is unchanged; a resumed call inserted ahead of the position or a late
// correction invalidates it with a stale-cursor classification that requires
// restarting pagination rather than silently mixing two snapshots.
type EconomicDetailQuery struct {
	StoreID       string `json:"store_id"`
	TenantID      string `json:"tenant_id,omitempty"`
	AccountID     string `json:"account_id"`
	BillingCallID string `json:"billing_call_id,omitempty"`
	ALegID        string `json:"a_leg_id,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
}

// Normalize trims scope, applies the default limit and rejects unbounded or
// ambiguous queries. BillingCallID is validated through the canonical
// BillingCallID contract; other identities use the bounded economic identity
// contract without surrounding whitespace.
func (q EconomicDetailQuery) Normalize() (EconomicDetailQuery, error) {
	out := EconomicDetailQuery{
		StoreID:       strings.TrimSpace(q.StoreID),
		TenantID:      strings.TrimSpace(q.TenantID),
		AccountID:     strings.TrimSpace(q.AccountID),
		BillingCallID: strings.TrimSpace(q.BillingCallID),
		ALegID:        strings.TrimSpace(q.ALegID),
		Limit:         q.Limit,
		Cursor:        strings.TrimSpace(q.Cursor),
	}
	if err := economics.ValidateOperatorCursorSize("economic detail cursor", out.Cursor); err != nil {
		return EconomicDetailQuery{}, fmt.Errorf("%w: %v", ErrEconomicDetailInvalid, err)
	}
	if !validEconomicIdentity(out.StoreID, metering.MaxSchemaIDBytes) {
		return EconomicDetailQuery{}, fmt.Errorf("%w: store scope required", ErrEconomicDetailInvalid)
	}
	if !validEconomicIdentity(out.AccountID, metering.MaxSchemaIDBytes) {
		return EconomicDetailQuery{}, fmt.Errorf("%w: account scope required", ErrEconomicDetailInvalid)
	}
	if out.TenantID != "" && !validEconomicIdentity(out.TenantID, metering.MaxSchemaIDBytes) {
		return EconomicDetailQuery{}, fmt.Errorf("%w: tenant scope is not a bounded identity", ErrEconomicDetailInvalid)
	}
	hasCall := out.BillingCallID != ""
	hasALeg := out.ALegID != ""
	if !hasCall && !hasALeg {
		return EconomicDetailQuery{}, fmt.Errorf("%w: billing call or A-leg scope required", ErrEconomicDetailInvalid)
	}
	if hasCall {
		if _, err := ParseBillingCallID(out.BillingCallID); err != nil {
			return EconomicDetailQuery{}, fmt.Errorf("%w: billing call scope: %v", ErrEconomicDetailInvalid, err)
		}
		if hasALeg && !validEconomicIdentity(out.ALegID, metering.MaxSchemaIDBytes) {
			return EconomicDetailQuery{}, fmt.Errorf("%w: A-leg scope is not a bounded identity", ErrEconomicDetailInvalid)
		}
	} else {
		if !validEconomicIdentity(out.ALegID, metering.MaxSchemaIDBytes) {
			return EconomicDetailQuery{}, fmt.Errorf("%w: A-leg scope required", ErrEconomicDetailInvalid)
		}
	}
	if out.Limit == 0 {
		out.Limit = EconomicDetailDefaultLimit
	}
	if out.Limit < 1 || out.Limit > EconomicDetailMaxLimit {
		return EconomicDetailQuery{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrEconomicDetailInvalid, EconomicDetailMaxLimit)
	}
	return out, nil
}

// IsCallScope reports whether the query targets one billing call.
func (q EconomicDetailQuery) IsCallScope() bool { return q.BillingCallID != "" }

// EconomicObservationPosition is the decoded, authenticated keyset position of
// one observation in the canonical EconomicDetailObservationOrder. It is the
// continuation payload an authenticated cursor carries: the durable adapter
// verifies the opaque token and converts it to this position before assembly,
// and converts the assembled next position back into an opaque token. It is
// never trusted as caller input on its own.
type EconomicObservationPosition struct {
	StreamID       string `json:"stream_id"`
	Sequence       uint64 `json:"sequence"`
	SourceEventKey string `json:"source_event_key"`
	ObservationID  string `json:"observation_id"`
	Revision       uint64 `json:"revision"`
}

// Validate checks the position is a usable keyset fragment. A malformed
// position fails closed instead of silently selecting a first or arbitrary
// page.
func (p EconomicObservationPosition) Validate() error {
	if !validEconomicIdentity(p.StreamID, metering.MaxStreamIDBytes) {
		return fmt.Errorf("%w: cursor stream identity required", ErrEconomicDetailInvalid)
	}
	if !validEconomicIdentity(p.ObservationID, metering.MaxObservationIDBytes) {
		return fmt.Errorf("%w: cursor observation identity required", ErrEconomicDetailInvalid)
	}
	if p.Revision == 0 {
		return fmt.Errorf("%w: cursor observation revision required", ErrEconomicDetailInvalid)
	}
	return nil
}

// EconomicObservationPositionOf returns the immutable keyset position of one
// observation in the canonical observation order.
func EconomicObservationPositionOf(observation metering.Observation) EconomicObservationPosition {
	return EconomicObservationPosition{
		StreamID:       observation.StreamID,
		Sequence:       observation.Sequence,
		SourceEventKey: observation.SourceEventKey,
		ObservationID:  observation.ID,
		Revision:       observation.Revision,
	}
}

// economicObservationOrderLess is the canonical, limit-independent observation
// order: stream, sequence, source event key, observation identity, revision.
// It is total for distinct immutable identities, so the order does not depend
// on arrival, page size or iteration order.
func economicObservationOrderLess(left, right metering.Observation) bool {
	if left.StreamID != right.StreamID {
		return left.StreamID < right.StreamID
	}
	if left.Sequence != right.Sequence {
		return left.Sequence < right.Sequence
	}
	if left.SourceEventKey != right.SourceEventKey {
		return left.SourceEventKey < right.SourceEventKey
	}
	if left.ID != right.ID {
		return left.ID < right.ID
	}
	return left.Revision < right.Revision
}

// economicObservationAfterPosition reports whether observation is strictly
// after the keyset position under the same total order that produced the page,
// so page N+1 continues after page N without repeating or omitting a position.
func economicObservationAfterPosition(observation metering.Observation, position EconomicObservationPosition) bool {
	keyset := EconomicObservationPositionOf(observation)
	switch {
	case keyset.StreamID != position.StreamID:
		return keyset.StreamID > position.StreamID
	case keyset.Sequence != position.Sequence:
		return keyset.Sequence > position.Sequence
	case keyset.SourceEventKey != position.SourceEventKey:
		return keyset.SourceEventKey > position.SourceEventKey
	case keyset.ObservationID != position.ObservationID:
		return keyset.ObservationID > position.ObservationID
	default:
		return keyset.Revision > position.Revision
	}
}

// EconomicDetailInput is the frozen assembly input. Observations and
// valuations carry full source separation; comparisons, selection and heads
// carry their own statuses for echo without recomputation. Summaries are
// existing projections echoed verbatim, never reinterpreted.
type EconomicDetailInput struct {
	Query EconomicDetailQuery
	// After is the decoded continuation position of an authenticated cursor, or
	// nil for the first page. When set, assembly returns only observations
	// strictly after it in the canonical order. It is never trusted raw: the
	// durable adapter authenticates the opaque token and validates the decoded
	// position before assembly.
	After        *EconomicObservationPosition
	Observations []metering.Observation
	Valuations   []economics.Valuation
	// SelectedValuations is the bounded set of exact frozen selected
	// valuations resolved by their authoritative durable selected identity
	// (valuation id plus canonical input-set hash). It is deliberately separate
	// from Valuations so the latest-per-stream display projection is unchanged
	// while cost coverage can resolve the exact frozen revision a selected head
	// names even when a newer revision of the same stream exists. Age is
	// ordering, never identity: a submitted candidate is matched only by its
	// exact immutable id and input-set hash.
	SelectedValuations []economics.Valuation
	Quantity           *ComponentQuantityComparison
	Monetary           *MonetaryDiscrepancyComparison
	Selection          *OperatorCostSelectionResult
	Heads              []SelectedCostHead
	TurnSummary        *TurnResultSummary
	RetailTotals       *ALegRetailTotals
	ProviderTotals     *ALegProviderTotals
	Allocations        []AllocatedCostLine
	// AllocationState is the correction/completeness state of the allocation
	// lineage that produced Allocations. It is optional; when supplied it is
	// bounded, scope-validated and projected verbatim into Coverage so a
	// superseded, pending or redacted contribution is explicit rather than
	// implied by an empty or partial line slice.
	AllocationState *EconomicDetailAllocationState
	// Reconciliations is the bounded set of independently identified
	// reconciliation comparisons, one per authoritative in-scope subject. When
	// non-empty it is authoritative: the singular Quantity/Monetary inputs
	// above are ignored and the output's singular convenience fields are
	// projected from the deterministic-first entry that carries a comparison
	// plane. Supply either the set or the singular inputs, not both.
	Reconciliations []EconomicDetailReconciliation
	// ExecutionCoverage is the bounded set of authoritative in-scope executed
	// B-leg facts the durable reader already loaded. Each fact is a provider-
	// neutral execution identity/outcome/evidence-presence classification that
	// keeps an executed leg without materialized observations from disappearing
	// from cost completeness. Attempted work without accepted provider evidence
	// stays unresolved; only canonical never-started/nonbillable/known-zero
	// proof exempts it. It carries no measurement or amount.
	ExecutionCoverage []EconomicDetailExecutionLeg
	// StatementEvidence is the source-separated statement-origin S evidence
	// resolved through a consumer-owned exact observation source. It is
	// optional; nil means no statement reader was composed and is projected as
	// an explicit unavailable reader rather than as absent evidence.
	StatementEvidence *EconomicDetailStatementEvidence
}

// EconomicDetailReconciliation is one independently identified reconciliation
// comparison result for one authoritative in-scope subject. Subject is the
// ownership root and lineage of the comparison; Quantity, Monetary and
// Aggregate are that subject's retained comparison planes copied verbatim.
// Independent subjects are never merged, summed or FX-converted, and revision
// selection happens only within one true subject/source identity before
// assembly. A subject with no retained comparison plane is still preserved so
// its reconciliation is never silently dropped.
type EconomicDetailReconciliation struct {
	Subject  metering.SubjectRef            `json:"subject"`
	Quantity *ComponentQuantityComparison   `json:"quantity,omitempty"`
	Monetary *MonetaryDiscrepancyComparison `json:"monetary,omitempty"`
	// Aggregate is that subject's retained aggregate reconciliation plane. It is
	// an independent typed projection: it is preserved verbatim, participates in
	// full-scope completeness, and is never coerced into the quantity or
	// monetary planes or treated as a second monetary authority.
	Aggregate *ReconciliationAggregate `json:"aggregate,omitempty"`
}

// EconomicDetailBasisTotal preserves one valuation plane's native totals
// verbatim for audit without merging planes.
type EconomicDetailBasisTotal struct {
	Basis          economics.ValuationBasis    `json:"basis"`
	ValuationID    string                      `json:"valuation_id"`
	CurrencyTotals []economics.CurrencyTotal   `json:"currency_totals"`
	Completeness   economics.Completeness      `json:"completeness"`
	Payer          metering.PaymentParty       `json:"payer,omitzero"`
	Rater          economics.RatingSnapshotRef `json:"rater"`
	Tariff         economics.RatingSnapshotRef `json:"tariff"`
	Policy         economics.PolicySnapshotRef `json:"policy"`
}

// EconomicDetailTotals carries the operator selected subtotal and the
// distinct native currencies observed. Margin is separate.
//
// The selected fields are populated only when the scope has one unambiguous
// selected fact: either a caller-supplied full selection or exactly one
// authoritative persisted head. SelectedHeadCount counts the persisted heads
// that carry a frozen selection; SelectedAmbiguous is true when the scope holds
// more than one independent head, so no single truthful subtotal exists and
// independent heads are never summed, collapsed or FX-converted.
type EconomicDetailTotals struct {
	SelectedAmount       *MonetaryExactAmount        `json:"selected_amount,omitempty"`
	SelectedCurrency     string                      `json:"selected_currency,omitempty"`
	SelectedBasis        OperatorCostSelectionBasis  `json:"selected_basis,omitempty"`
	SelectedStatus       OperatorCostSelectionStatus `json:"selected_status,omitempty"`
	SelectedReason       OperatorCostSelectionReason `json:"selected_reason,omitempty"`
	SelectedCompleteness economics.Completeness      `json:"selected_completeness,omitempty"`
	SelectedHeadCount    int                         `json:"selected_head_count,omitempty"`
	SelectedAmbiguous    bool                        `json:"selected_ambiguous,omitempty"`
	Currencies           []string                    `json:"currencies,omitempty"`
}

// EconomicDetailMargin is explicit incomplete margin. Amount is present only
// when Complete is true; otherwise Reason names the bounded cause.
type EconomicDetailMargin struct {
	Currency string               `json:"currency,omitempty"`
	Amount   *MonetaryExactAmount `json:"amount,omitempty"`
	Complete bool                 `json:"complete"`
	Reason   string               `json:"reason"`
}

// EconomicDetailAllocationState is the truthful correction state of the
// bounded allocation lineage backing Coverage.Allocations. It is separate from
// the line slice because the lines are the effective, in-scope contributions
// while pending ancestry, retired predecessors, completeness and payable state
// explain why that contribution set may be partial or missing. Every retained
// reference is an immutable allocation identity plus payload hash, never money,
// a source aggregate or a remainder. Redacted is true when at least one
// retained line had unauthorized (account-less) source economics withheld.
type EconomicDetailAllocationState struct {
	Status            economics.AllocationSupersessionStatus `json:"status,omitempty"`
	Complete          bool                                   `json:"complete"`
	Payable           bool                                   `json:"payable"`
	Pending           []economics.AllocationRef              `json:"pending,omitempty"`
	PendingSupersedes []economics.AllocationRef              `json:"pending_supersedes,omitempty"`
	Superseded        []economics.AllocationRef              `json:"superseded,omitempty"`
	Redacted          bool                                   `json:"redacted,omitempty"`
}

// EconomicDetailCoverage surfaces missing evidence, aggregate-only charges,
// account-period subjects and conserved allocations without inventing
// per-request allocations.
type EconomicDetailCoverage struct {
	MissingRefs        []metering.ObservationRef    `json:"missing_refs,omitempty"`
	MissingCount       int                          `json:"missing_count"`
	AggregateCharges   []metering.ReportedCharge    `json:"aggregate_charges,omitempty"`
	AggregateOnly      bool                         `json:"aggregate_only"`
	CoverageRefs       []metering.ChargeCoverageRef `json:"coverage_refs,omitempty"`
	NonRequestSubjects []metering.SubjectRef        `json:"non_request_subjects,omitempty"`
	Allocations        []AllocatedCostLine          `json:"allocations,omitempty"`
	// AllocationState is the bounded correction/completeness state of the
	// allocation lineage that produced Allocations. It is populated whenever an
	// authoritative in-scope allocation lineage exists, even when no line is
	// effective, so a superseded, pending or redacted contribution is never
	// silently reported as an absent or complete allocation set.
	AllocationState *EconomicDetailAllocationState `json:"allocation_state,omitempty"`
	// CostCoverage is the bounded full-scope projection of attributable executed
	// in-scope cost subjects and charge identities. It is the marker that lets
	// margin completeness prove every operator-payable cost is either covered by
	// the single authoritative selected result, explicitly known-zero,
	// customer-BYOK, or proven inclusively covered. A Complete margin requires
	// CostCoverage.Complete.
	CostCoverage *EconomicDetailCostCoverage `json:"cost_coverage,omitempty"`
}

// EconomicDetailCostCoverageState classifies why one attributable in-scope cost
// subject is or is not covered by the singular authoritative selected result.
type EconomicDetailCostCoverageState string

const (
	// EconomicDetailCostCoverageSelected names the subject of the singular
	// authoritative selected result itself.
	EconomicDetailCostCoverageSelected EconomicDetailCostCoverageState = "selected"
	// EconomicDetailCostCoverageInclusive names a subject proven contained in
	// the selected result through an explicit inclusive charge-coverage edge.
	EconomicDetailCostCoverageInclusive EconomicDetailCostCoverageState = "inclusive"
	// EconomicDetailCostCoverageHead names a subject with its own authoritative
	// frozen selected/posted head, so it is independently valued.
	EconomicDetailCostCoverageHead EconomicDetailCostCoverageState = "authoritative_head"
	// EconomicDetailCostCoverageKnownZero names a subject whose only
	// operator-attributable charges are explicitly reported as exactly zero.
	EconomicDetailCostCoverageKnownZero EconomicDetailCostCoverageState = "known_zero"
	// EconomicDetailCostCoverageBYOK names a subject whose attributable charges
	// are all customer-paid, so no operator payable exists.
	EconomicDetailCostCoverageBYOK EconomicDetailCostCoverageState = "customer_byok"
	// EconomicDetailCostCoverageUnresolved names an attributable operator-payable
	// or unpriced subject with no authoritative coverage in the selected result.
	EconomicDetailCostCoverageUnresolved EconomicDetailCostCoverageState = "unresolved"
	// EconomicDetailCostCoverageNeverStarted names an authoritative leg whose
	// canonical outcome proves it never began execution, so it carries no
	// operator exposure (parent design C4: proven never-started work may be
	// known zero with its basis).
	EconomicDetailCostCoverageNeverStarted EconomicDetailCostCoverageState = "never_started"
	// EconomicDetailCostCoverageNonbillable names an authoritative leg whose
	// canonical outcome proves the provider never accepted billable execution
	// (a rejected attempt), so it carries no operator exposure.
	EconomicDetailCostCoverageNonbillable EconomicDetailCostCoverageState = "nonbillable"
)

// EconomicDetailExecutionLeg is one bounded, provider-neutral execution fact
// carried from an already-loaded authoritative call-leg record into assembly. It
// carries only canonical lineage, outcome and evidence-presence/authority
// classification: never a measurement, token count or amount, and missing
// evidence is never turned into zero. It exists so an executed/attempted B-leg
// whose embedded observations are absent, empty or unusable still participates
// in cost completeness; parent design C4 says attempted work without accepted
// provider evidence is unknown, not reconciled zero.
type EconomicDetailExecutionLeg struct {
	Subject    metering.SubjectRef `json:"subject"`
	AttemptSeq int                 `json:"attempt_seq,omitempty"`
	Outcome    LegOutcome          `json:"outcome"`
	Surfaced   SurfacedState       `json:"surfaced,omitempty"`
	// AcceptedEvidence is a presence-only fact: the leg's canonical V1 evidence
	// carries an authoritative provider cost or at least one accepted quantity.
	AcceptedEvidence bool `json:"accepted_evidence,omitempty"`
	// AuthoritativeZeroCost is true only for an explicit authoritative provider
	// cost of exactly zero, which is a definite known-zero cost rather than a
	// missing or estimated one.
	AuthoritativeZeroCost bool `json:"authoritative_zero_cost,omitempty"`
}

// NewEconomicDetailExecutionLeg classifies one already-loaded authoritative leg
// record into a bounded execution fact. The classification reads only canonical
// evidence presence and authority, never a value, so no measurement or amount is
// invented and absent evidence stays absent.
func NewEconomicDetailExecutionLeg(subject metering.SubjectRef, attemptSeq int, outcome LegOutcome, surfaced SurfacedState, evidence FinalBillingEvidence) EconomicDetailExecutionLeg {
	return EconomicDetailExecutionLeg{
		Subject:               subject.Clone(),
		AttemptSeq:            attemptSeq,
		Outcome:               outcome,
		Surfaced:              surfaced,
		AcceptedEvidence:      providerAcceptedEvidence(evidence),
		AuthoritativeZeroCost: authoritativeProviderCost(evidence) && evidence.Cost.NanoUnits == 0,
	}
}

// Validate bounds one execution fact without requiring the source leg.
func (l EconomicDetailExecutionLeg) Validate() error {
	if err := l.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: execution leg subject: %v", ErrEconomicDetailInvalid, err)
	}
	if l.Subject.Kind != metering.SubjectBLeg {
		return fmt.Errorf("%w: execution leg must be a B-leg subject", ErrEconomicDetailInvalid)
	}
	if !validLegOutcome(l.Outcome) {
		return fmt.Errorf("%w: execution leg outcome %q", ErrEconomicDetailInvalid, l.Outcome)
	}
	if l.Surfaced != "" && !validSurfacedState(l.Surfaced) {
		return fmt.Errorf("%w: execution leg surfaced state %q", ErrEconomicDetailInvalid, l.Surfaced)
	}
	if l.AttemptSeq < 0 {
		return fmt.Errorf("%w: execution leg attempt sequence cannot be negative", ErrEconomicDetailInvalid)
	}
	return nil
}

// coverageState resolves the exempting canonical classification of one execution
// fact, or unresolved when the leg was attempted/possibly executed without a
// definitive no-exposure proof. It mirrors RateProviderCost precedence: an
// explicit authoritative zero is a known zero, any other accepted provider
// evidence is exposure that the selected result must cover regardless of the
// terminal outcome label, and only a leg with no accepted evidence may use the
// canonical never-started/nonbillable exemption.
func (l EconomicDetailExecutionLeg) coverageState() EconomicDetailCostCoverageState {
	if l.AuthoritativeZeroCost {
		return EconomicDetailCostCoverageKnownZero
	}
	if l.AcceptedEvidence {
		return EconomicDetailCostCoverageUnresolved
	}
	switch l.Outcome {
	case LegOutcomeNeverStarted:
		return EconomicDetailCostCoverageNeverStarted
	case LegOutcomeRejected:
		return EconomicDetailCostCoverageNonbillable
	default:
		return EconomicDetailCostCoverageUnresolved
	}
}

// EconomicDetailCostSubject is one attributable executed in-scope cost subject
// with its bounded coverage classification. ChargeItemIDs lists the observed
// charge identities that made the subject attributable to the operator.
type EconomicDetailCostSubject struct {
	Subject         metering.SubjectRef             `json:"subject"`
	State           EconomicDetailCostCoverageState `json:"state"`
	OperatorPayable bool                            `json:"operator_payable"`
	ChargeItemIDs   []string                        `json:"charge_item_ids,omitempty"`
}

// EconomicDetailCostCoverage is the bounded, deterministic, deduped full-scope
// cost-coverage projection. UnresolvedCount counts subjects with no authoritative
// coverage; AllocationUnresolvedCount counts active attributable monetary
// allocations with no explicit selected-result inclusion proof. Complete is true
// exactly when every attributable operator-payable cost subject is covered,
// explicitly known-zero, customer-BYOK or proven inclusive and every active
// attributable monetary allocation is either proven included or a canonical
// explicit zero. The projection is part of the snapshot fingerprint, so a
// between-page uncovered charge or allocation invalidates an outstanding
// continuation and the current page margin stays incomplete.
type EconomicDetailCostCoverage struct {
	Subjects        []EconomicDetailCostSubject `json:"subjects,omitempty"`
	UnresolvedCount int                         `json:"unresolved_count"`
	// AllocationCoverage classifies every active attributable monetary
	// allocation line against the singular selected result. An allocation is
	// never added to the selected amount or margin; it only participates in
	// completeness.
	AllocationCoverage []EconomicDetailAllocationCoverage `json:"allocation_coverage,omitempty"`
	// AllocationUnresolvedCount counts active attributable monetary allocations
	// that are neither proven included nor a canonical explicit zero.
	AllocationUnresolvedCount int  `json:"allocation_unresolved_count,omitempty"`
	Complete                  bool `json:"complete"`
}

// EconomicDetailAllocationCoverageReason is the bounded truthful cause of one
// allocation line's coverage classification. It carries no source or share
// amount, so an unauthorized allocation can fail closed without leaking
// economics.
type EconomicDetailAllocationCoverageReason string

const (
	// EconomicDetailAllocationCoverageIncluded means the exact allocation
	// revision was named by a frozen selected valuation's allocation-coverage
	// reference and is currency-compatible with the selected amount.
	EconomicDetailAllocationCoverageIncluded EconomicDetailAllocationCoverageReason = "included"
	// EconomicDetailAllocationCoverageKnownZero means the allocation carries an
	// exact canonical zero, so it cannot move the margin.
	EconomicDetailAllocationCoverageKnownZero EconomicDetailAllocationCoverageReason = "known_zero"
	// EconomicDetailAllocationCoverageNotIncluded means the allocation is
	// positive and no frozen selected result proves it included.
	EconomicDetailAllocationCoverageNotIncluded EconomicDetailAllocationCoverageReason = "not_included"
	// EconomicDetailAllocationCoverageCurrencyMismatch means the allocation's
	// native currency differs from the selected amount's currency, so no
	// selected inclusion can be asserted without a frozen conversion basis.
	EconomicDetailAllocationCoverageCurrencyMismatch EconomicDetailAllocationCoverageReason = "currency_mismatch"
	// EconomicDetailAllocationCoverageRedactedSource means the source aggregate
	// was withheld as unauthorized, so inclusion can neither be proven nor
	// exempted and the allocation fails closed without exposing an amount.
	EconomicDetailAllocationCoverageRedactedSource EconomicDetailAllocationCoverageReason = "redacted_source"
)

// EconomicDetailAllocationCoverage is the coverage classification of one active
// attributable monetary allocation line. It carries membership identity, target
// reference and state only: never a source amount, share or remainder.
type EconomicDetailAllocationCoverage struct {
	AllocationID      string                                 `json:"allocation_id"`
	AllocationVersion uint64                                 `json:"allocation_version"`
	TargetID          string                                 `json:"target_id,omitempty"`
	State             EconomicDetailCostCoverageState        `json:"state"`
	Reason            EconomicDetailAllocationCoverageReason `json:"reason"`
	Currency          string                                 `json:"currency,omitempty"`
	Redacted          bool                                   `json:"redacted,omitempty"`
}

// EconomicDetailBasisPayer binds one valuation plane to its trusted payer.
type EconomicDetailBasisPayer struct {
	Basis economics.ValuationBasis `json:"basis"`
	Payer metering.PaymentParty    `json:"payer,omitzero"`
}

// EconomicDetailPayers makes BYOK payment responsibility explicit. Retail
// customer policy payers do not count as BYOK; only provider-side customer
// payers on E/Q/P/S planes do.
type EconomicDetailPayers struct {
	ByBasis            []EconomicDetailBasisPayer `json:"by_basis,omitempty"`
	Distinct           []metering.PaymentParty    `json:"distinct,omitempty"`
	HasOperatorPayable bool                       `json:"has_operator_payable"`
	HasCustomerBYOK    bool                       `json:"has_customer_byok"`
	HasUnallocated     bool                       `json:"has_unallocated"`
}

// EconomicDetailSummary echoes existing summary projections verbatim. Detail
// augments these totals; it never overwrites them.
type EconomicDetailSummary struct {
	Turn     *TurnResultSummary  `json:"turn,omitempty"`
	Retail   *ALegRetailTotals   `json:"retail,omitempty"`
	Provider *ALegProviderTotals `json:"provider,omitempty"`
}

// EconomicDetail is one assembled scoped page. Observations are the Limit page
// in canonical order; every other fact (valuations, heads, per-basis totals,
// margin, coverage, payers, summary, reconciliations, selection and statement
// evidence) is the same full-scope snapshot fact set on every page of one
// continuation. The continuation is only valid while that full-scope snapshot
// is unchanged: page one binds an authenticated deterministic fingerprint of
// the full observation membership and every repeated fact, and a continuation
// whose recomputed fingerprint differs is rejected as a stale cursor that
// requires restarting pagination. A caller therefore never consumes a mixed
// snapshot (for example a resumed call silently omitted while newer totals
// appear), and monetary aggregation is never duplicated per page.
//
// NextCursor is the opaque, authenticated continuation token for the next
// page; it is empty exactly on a final or empty page. NextPosition is the
// decoded form the durable adapter converts into NextCursor; it is not part of
// the wire contract. Truncated is retained for compatibility and is true
// exactly when NextPosition is present.
type EconomicDetail struct {
	Scope        EconomicDetailQuery    `json:"scope"`
	Observations []metering.Observation `json:"observations,omitempty"`
	Valuations   []economics.Valuation  `json:"valuations,omitempty"`
	// SelectedValuations is the bounded set of exact frozen selected revisions
	// resolved by authoritative selected identity. It is separate from
	// Valuations so the latest-per-stream display projection is unchanged while
	// cost coverage can still resolve the exact revision a selected head names.
	SelectedValuations []economics.Valuation          `json:"selected_valuations,omitempty"`
	Quantity           *ComponentQuantityComparison   `json:"quantity,omitempty"`
	Monetary           *MonetaryDiscrepancyComparison `json:"monetary,omitempty"`
	// Selection is the singular selection convenience field. It is the
	// caller-supplied full selection when present, otherwise a candidate-less
	// projection of the one unambiguous persisted head. When the scope holds
	// multiple independent heads it stays nil and Totals.SelectedAmbiguous is
	// set; it never represents an invented aggregate or candidate set.
	Selection *OperatorCostSelectionResult `json:"selection,omitempty"`
	// Heads is the authoritative bounded, deterministically ordered set of
	// persisted selected/posted heads with their exact identity, version,
	// frozen selection and transaction/operation lineage. It is never collapsed
	// to one entry.
	Heads          []SelectedCostHead         `json:"heads,omitempty"`
	PerBasisTotals []EconomicDetailBasisTotal `json:"per_basis_totals,omitempty"`
	Totals         EconomicDetailTotals       `json:"totals"`
	Margin         EconomicDetailMargin       `json:"margin"`
	Coverage       EconomicDetailCoverage     `json:"coverage"`
	Payers         EconomicDetailPayers       `json:"payers"`
	Summary        EconomicDetailSummary      `json:"summary"`
	// Reconciliations is the full bounded, deterministically ordered set of
	// independent in-scope subject comparisons. Quantity and Monetary above are
	// the singular convenience projection of the deterministic-first entry that
	// carries a comparison plane; this set is authoritative and is never
	// collapsed to one result.
	Reconciliations []EconomicDetailReconciliation `json:"reconciliations,omitempty"`
	// StatementEvidence is the resolved statement-origin S evidence of the
	// scope, exposed separately from the embedded leg observations. Missing and
	// unattributed references are enumerated inside it, never implied.
	StatementEvidence EconomicDetailStatementEvidence `json:"statement_evidence,omitzero"`
	// ExecutionCoverage is the bounded, deterministically ordered set of
	// authoritative in-scope executed B-leg facts carried from the loaded leg
	// records. It is part of the repeated full-scope snapshot: a leg added,
	// removed or reclassified between pages invalidates the continuation even
	// when it contributed no observation or valuation. It is never a monetary
	// authority.
	ExecutionCoverage []EconomicDetailExecutionLeg `json:"execution_coverage,omitempty"`
	// NextCursor is the opaque continuation token for the next observation
	// page, populated by the durable adapter from NextPosition. Empty means the
	// page is final or empty.
	NextCursor string `json:"next_cursor,omitempty"`
	// NextPosition is the decoded keyset position of this page's last
	// observation. It is a durable-adapter seam, never serialized.
	NextPosition *EconomicObservationPosition `json:"-"`
	// SnapshotFingerprint is the deterministic, bounded fingerprint of the full
	// pre-pagination observation membership and every repeated full-scope fact
	// of this detail. It is a durable-adapter seam and is never serialized.
	SnapshotFingerprint string `json:"-"`
	// PreviousSnapshotFingerprint is the authenticated composed snapshot
	// fingerprint asserted by the incoming continuation, or empty on a first
	// page. The outermost composed reader verifies it before returning a next
	// cursor. It is a durable-adapter seam and is never serialized.
	PreviousSnapshotFingerprint string `json:"-"`
	Truncated                   bool   `json:"truncated"`
}

// ValuationFor returns the first deterministically ordered valuation for one
// basis, or nil when the plane is absent. A scoped detail may legitimately
// retain several independent contributions that share a basis but belong to
// different subjects; callers that need all of them must iterate Valuations.
// Planes are never merged.
func (d EconomicDetail) ValuationFor(basis economics.ValuationBasis) *economics.Valuation {
	for i := range d.Valuations {
		if d.Valuations[i].Basis == basis {
			out := d.Valuations[i].Clone()
			return &out
		}
	}
	return nil
}

// EconomicValuationStreamKey returns the stable identity of the logical
// valuation stream that a valuation revision belongs to.
//
// The ownership root of an economic valuation is its authoritative subject
// (Requirement 6.2: B-leg/attempt, call, A-leg or another declared subject),
// the economic party is its reporting perspective and the source plane is its
// basis. Two valuations are revisions of one stream only when all three agree;
// distinct subjects, economic parties or bases are independent contributions
// even when their basis label matches. This mirrors the durable head identity
// (queue + subject-derived head key) with the basis plane made explicit.
//
// The key deliberately excludes mutable derived values (amounts, native
// currency totals, payer, completeness and snapshot revisions). Those are
// preserved verbatim per revision, and advancing evidence changes the revision
// while the stream identity stays the same. Callers select the latest revision
// within one key and must never collapse across keys, so totals and payers of
// independent subjects are never merged or mixed. Every field the canonical
// subject validator accepts, including the ResetAt/StartAt/EndAt interval
// identity, is folded in through writeEconomicSubjectIdentity.
func EconomicValuationStreamKey(valuation economics.Valuation) string {
	var builder strings.Builder
	writeEconomicSubjectIdentity(&builder, valuation.Subject)
	writeEconomicStreamPart(&builder, string(valuation.Perspective))
	writeEconomicStreamPart(&builder, string(valuation.Basis))
	return builder.String()
}

// writeEconomicSubjectIdentity writes every field of an authoritative SubjectRef
// identity in a fixed NUL-delimited order. It is the single canonical encoder
// for subject identity in this package: valuation stream keys and reconciliation
// subject sort keys both use it, so a field accepted by the canonical subject
// validator (including the ResetAt/StartAt/EndAt temporal identity) can never be
// omitted from grouping or revision selection silently.
func writeEconomicSubjectIdentity(builder *strings.Builder, subject metering.SubjectRef) {
	writeEconomicStreamPart(builder, string(subject.Kind))
	writeEconomicStreamPart(builder, subject.StoreID)
	writeEconomicStreamPart(builder, subject.TenantID)
	writeEconomicStreamPart(builder, subject.AccountID)
	writeEconomicStreamPart(builder, subject.ALegID)
	writeEconomicStreamPart(builder, subject.RequestID)
	writeEconomicStreamPart(builder, subject.BillingCallID)
	writeEconomicStreamPart(builder, subject.CallID)
	writeEconomicStreamPart(builder, subject.BLegID)
	writeEconomicStreamPart(builder, subject.AttemptID)
	writeEconomicStreamPart(builder, fmt.Sprint(subject.AttemptSeq))
	writeEconomicStreamPart(builder, subject.SubmissionID)
	writeEconomicStreamPart(builder, subject.ProviderAccountKey)
	writeEconomicStreamPart(builder, subject.ProviderRequestID)
	writeEconomicStreamPart(builder, subject.ProviderChargeID)
	writeEconomicStreamPart(builder, subject.ResourceID)
	writeEconomicStreamPart(builder, subject.PeriodID)
	writeEconomicStreamPart(builder, subject.PoolID)
	writeEconomicStreamPart(builder, subject.WindowID)
	writeEconomicStreamPart(builder, subject.StatementID)
	writeEconomicStreamPart(builder, subject.StatementLineID)
	writeEconomicStreamPart(builder, fmt.Sprint(subject.ResetAt))
	writeEconomicStreamPart(builder, fmt.Sprint(subject.StartAt))
	writeEconomicStreamPart(builder, fmt.Sprint(subject.EndAt))
}

func writeEconomicStreamPart(builder *strings.Builder, value string) {
	builder.WriteString(value)
	builder.WriteByte(0)
}

// AssembleEconomicDetail validates scope, bounds and cross-scope identities,
// then projects the frozen input sets into a deterministic scoped detail.
func AssembleEconomicDetail(in EconomicDetailInput) (EconomicDetail, error) {
	query, err := in.Query.Normalize()
	if err != nil {
		return EconomicDetail{}, err
	}
	if len(in.Observations) > MaxEconomicDetailObservations {
		return EconomicDetail{}, fmt.Errorf("%w: observations=%d max=%d", ErrEconomicDetailBoundExceeded, len(in.Observations), MaxEconomicDetailObservations)
	}
	if len(in.Valuations) > MaxEconomicDetailValuations {
		return EconomicDetail{}, fmt.Errorf("%w: valuations=%d max=%d", ErrEconomicDetailBoundExceeded, len(in.Valuations), MaxEconomicDetailValuations)
	}
	if len(in.Heads) > MaxEconomicDetailHeads {
		return EconomicDetail{}, fmt.Errorf("%w: heads=%d max=%d", ErrEconomicDetailBoundExceeded, len(in.Heads), MaxEconomicDetailHeads)
	}
	if len(in.Allocations) > MaxEconomicDetailAllocations {
		return EconomicDetail{}, fmt.Errorf("%w: allocations=%d max=%d", ErrEconomicDetailBoundExceeded, len(in.Allocations), MaxEconomicDetailAllocations)
	}
	if len(in.ExecutionCoverage) > MaxEconomicDetailExecutionLegs {
		return EconomicDetail{}, fmt.Errorf("%w: execution legs=%d max=%d", ErrEconomicDetailBoundExceeded, len(in.ExecutionCoverage), MaxEconomicDetailExecutionLegs)
	}

	observations := make([]metering.Observation, 0, len(in.Observations))
	for i := range in.Observations {
		observation := in.Observations[i]
		if err := observation.Validate(); err != nil {
			return EconomicDetail{}, fmt.Errorf("%w: observation %d: %v", ErrEconomicDetailInvalid, i, err)
		}
		if err := observationMatchesScope(observation, query); err != nil {
			return EconomicDetail{}, err
		}
		canonical, err := observation.Canonical()
		if err != nil {
			return EconomicDetail{}, fmt.Errorf("%w: observation %d canonical: %v", ErrEconomicDetailInvalid, i, err)
		}
		observations = append(observations, canonical)
	}

	valuations := make([]economics.Valuation, 0, len(in.Valuations))
	seenStreams := make(map[string]struct{}, len(in.Valuations))
	for i := range in.Valuations {
		valuation := in.Valuations[i]
		if err := valuation.Validate(); err != nil {
			return EconomicDetail{}, fmt.Errorf("%w: valuation %d: %v", ErrEconomicDetailInvalid, i, err)
		}
		// Independent subjects, perspectives and bases are separate logical
		// streams even when their basis label matches. Only a repeated revision
		// of the same stream identity is a genuine duplicate/conflict.
		streamKey := EconomicValuationStreamKey(valuation)
		if _, exists := seenStreams[streamKey]; exists {
			return EconomicDetail{}, fmt.Errorf("%w: duplicate valuation stream subject_kind=%q basis=%q", ErrEconomicDetailInvalid, valuation.Subject.Kind, valuation.Basis)
		}
		seenStreams[streamKey] = struct{}{}
		if err := valuationMatchesScope(valuation, query); err != nil {
			return EconomicDetail{}, err
		}
		canonical, err := valuation.Canonical()
		if err != nil {
			return EconomicDetail{}, fmt.Errorf("%w: valuation %d canonical: %v", ErrEconomicDetailInvalid, i, err)
		}
		valuations = append(valuations, canonical)
	}
	sort.SliceStable(valuations, func(i, j int) bool {
		left, right := economicDetailBasisRank(valuations[i].Basis), economicDetailBasisRank(valuations[j].Basis)
		if left != right {
			return left < right
		}
		return valuations[i].ID < valuations[j].ID
	})

	// SelectedValuations are the exact frozen selected revisions resolved by
	// their authoritative durable identity. They are deliberately not folded
	// into the latest-per-stream display set: the same logical stream may be
	// represented by a newer display revision, so duplicate-stream rejection
	// must not apply across the two roles. They are bounded, scope-validated and
	// canonicalized exactly like display valuations.
	if len(in.SelectedValuations) > MaxEconomicDetailValuations {
		return EconomicDetail{}, fmt.Errorf("%w: selected valuations=%d max=%d", ErrEconomicDetailBoundExceeded, len(in.SelectedValuations), MaxEconomicDetailValuations)
	}
	selectedValuations := make([]economics.Valuation, 0, len(in.SelectedValuations))
	for i := range in.SelectedValuations {
		valuation := in.SelectedValuations[i]
		if err := valuation.Validate(); err != nil {
			return EconomicDetail{}, fmt.Errorf("%w: selected valuation %d: %v", ErrEconomicDetailInvalid, i, err)
		}
		if err := valuationMatchesScope(valuation, query); err != nil {
			return EconomicDetail{}, err
		}
		canonical, err := valuation.Canonical()
		if err != nil {
			return EconomicDetail{}, fmt.Errorf("%w: selected valuation %d canonical: %v", ErrEconomicDetailInvalid, i, err)
		}
		selectedValuations = append(selectedValuations, canonical)
	}
	sort.SliceStable(selectedValuations, func(i, j int) bool {
		left, right := economicDetailBasisRank(selectedValuations[i].Basis), economicDetailBasisRank(selectedValuations[j].Basis)
		if left != right {
			return left < right
		}
		return selectedValuations[i].ID < selectedValuations[j].ID
	})

	reconciliations, err := normalizeEconomicDetailReconciliations(in.Reconciliations, query)
	if err != nil {
		return EconomicDetail{}, err
	}
	var quantity *ComponentQuantityComparison
	var monetary *MonetaryDiscrepancyComparison
	if len(reconciliations) > 0 {
		// The bounded set is authoritative; the singular convenience fields
		// project the deterministic-first entry carrying a comparison plane.
		quantity, monetary = projectSingularReconciliation(reconciliations)
	} else {
		if in.Quantity != nil {
			if err := validateQuantityScope(*in.Quantity, query); err != nil {
				return EconomicDetail{}, err
			}
			cloned := cloneComponentQuantityComparison(*in.Quantity)
			quantity = &cloned
		}
		if in.Monetary != nil {
			if err := validateMonetaryScope(*in.Monetary, query); err != nil {
				return EconomicDetail{}, err
			}
			cloned := cloneMonetaryComparison(*in.Monetary)
			monetary = &cloned
		}
	}
	var selection *OperatorCostSelectionResult
	if in.Selection != nil {
		if err := validateSelectionScope(*in.Selection, query); err != nil {
			return EconomicDetail{}, err
		}
		cloned := cloneOperatorCostSelection(*in.Selection)
		selection = &cloned
	}
	heads := make([]SelectedCostHead, 0, len(in.Heads))
	seenHeads := make(map[string]struct{}, len(in.Heads))
	for i := range in.Heads {
		head := in.Heads[i]
		if err := head.Validate(); err != nil {
			return EconomicDetail{}, fmt.Errorf("%w: head %d: %v", ErrEconomicDetailInvalid, i, err)
		}
		if err := headMatchesScope(head, query); err != nil {
			return EconomicDetail{}, err
		}
		// One economic charge has exactly one current head. A repeated
		// account/call/head identity is a genuine duplicate, not an
		// independent contribution, and must fail closed rather than
		// overwrite or double-count.
		identity := head.AccountID + "\x00" + head.CallID.String() + "\x00" + head.HeadKey
		if _, exists := seenHeads[identity]; exists {
			return EconomicDetail{}, fmt.Errorf("%w: duplicate head identity account=%q call=%q head=%q", ErrEconomicDetailInvalid, head.AccountID, head.CallID, head.HeadKey)
		}
		seenHeads[identity] = struct{}{}
		heads = append(heads, head.Clone())
	}
	sort.SliceStable(heads, func(i, j int) bool {
		if heads[i].CallID != heads[j].CallID {
			return heads[i].CallID < heads[j].CallID
		}
		return heads[i].HeadKey < heads[j].HeadKey
	})

	// The full Phase 12 candidate set is not retained by a durable head. When
	// the caller supplies no explicit selection, expose a singular convenience
	// selection only for the one case that is truthful: exactly one
	// authoritative head carrying a frozen selection. All other scopes stay
	// missing or explicitly ambiguous and are never summed or FX-converted.
	persistedSelection, selectedAmbiguous, selectedHeadCount := economicDetailPersistedSelection(heads)
	effectiveSelection := selection
	if effectiveSelection == nil {
		effectiveSelection = persistedSelection
	}
	if selection == nil {
		selection = persistedSelection
	}
	// Persisted-head ambiguity only governs the selected fact when no explicit
	// full selection was supplied; an explicit result stays authoritative.
	ambiguousScope := selectedAmbiguous && in.Selection == nil

	allocations := make([]AllocatedCostLine, 0, len(in.Allocations))
	for i := range in.Allocations {
		line := in.Allocations[i]
		if err := validateAllocationScope(line, query); err != nil {
			return EconomicDetail{}, err
		}
		allocations = append(allocations, line)
	}
	sort.SliceStable(allocations, func(i, j int) bool {
		if allocations[i].AllocationID != allocations[j].AllocationID {
			return allocations[i].AllocationID < allocations[j].AllocationID
		}
		return allocations[i].AllocationVersion < allocations[j].AllocationVersion
	})

	statementEvidence, err := normalizeEconomicDetailStatementEvidence(in.StatementEvidence, query)
	if err != nil {
		return EconomicDetail{}, err
	}

	executionCoverage, err := normalizeEconomicDetailExecutionCoverage(in.ExecutionCoverage, query)
	if err != nil {
		return EconomicDetail{}, err
	}

	sort.SliceStable(observations, func(i, j int) bool {
		return economicObservationOrderLess(observations[i], observations[j])
	})

	perBasisTotals, currencies := projectBasisTotals(valuations)
	coverage, err := projectCoverage(observations, valuations, allocations)
	if err != nil {
		return EconomicDetail{}, err
	}
	allocationState, err := normalizeEconomicDetailAllocationState(in.AllocationState, query)
	if err != nil {
		return EconomicDetail{}, err
	}
	coverage.AllocationState = allocationState
	costCoverage := projectCostCoverage(observations, valuations, selectedValuations, heads, effectiveSelection, in.Selection != nil, allocations, executionCoverage)
	coverage.CostCoverage = &costCoverage
	payers := projectPayers(observations, valuations)
	totals := projectSelectedTotals(effectiveSelection, valuations, currencies, selectedHeadCount, ambiguousScope)
	margin := projectMargin(effectiveSelection, valuations, quantity, monetary, reconciliations, allocationState, currencies, ambiguousScope, costCoverage)

	// Keyset continuation: skip every observation at or before the decoded
	// authenticated position, then return the next bounded page. A position
	// that is not a valid keyset fragment fails closed instead of silently
	// returning the first or an arbitrary page.
	remaining := observations
	if in.After != nil {
		if err := in.After.Validate(); err != nil {
			return EconomicDetail{}, err
		}
		position := *in.After
		start := sort.Search(len(remaining), func(i int) bool {
			return economicObservationAfterPosition(remaining[i], position)
		})
		remaining = remaining[start:]
	}
	var nextPosition *EconomicObservationPosition
	paged := remaining
	if len(remaining) > query.Limit {
		position := EconomicObservationPositionOf(remaining[query.Limit-1])
		nextPosition = &position
		paged = append([]metering.Observation(nil), remaining[:query.Limit]...)
	}
	truncated := nextPosition != nil

	summary := EconomicDetailSummary{}
	if in.TurnSummary != nil {
		cloned := *in.TurnSummary
		summary.Turn = &cloned
	}
	if in.RetailTotals != nil {
		cloned := *in.RetailTotals
		summary.Retail = &cloned
	}
	if in.ProviderTotals != nil {
		cloned := *in.ProviderTotals
		summary.Provider = &cloned
	}

	detail := EconomicDetail{
		Scope: query, Observations: paged, Valuations: valuations,
		SelectedValuations: selectedValuations,
		Quantity:           quantity, Monetary: monetary, Selection: selection, Heads: heads,
		PerBasisTotals: perBasisTotals, Totals: totals, Margin: margin,
		Coverage: coverage, Payers: payers, Summary: summary,
		Reconciliations: reconciliations, StatementEvidence: statementEvidence,
		ExecutionCoverage: executionCoverage,
		NextPosition:      nextPosition, Truncated: truncated,
	}
	// The full pre-pagination observation membership and every repeated
	// full-scope fact are bound into one deterministic fingerprint. A durable
	// continuation is only valid while this fingerprint is unchanged.
	detail.SnapshotFingerprint = economicDetailSnapshotFingerprint(observations, detail)
	return detail, nil
}

// economicDetailSnapshotFingerprint hashes the complete repeated full-scope
// fact set of one assembled detail: the full (pre-pagination) canonical
// observation membership plus valuations, comparisons, selection, heads,
// per-basis totals, totals, margin, coverage, payers, summary, reconciliations
// and statement evidence. Page-local fields (the observation page, NextCursor,
// NextPosition, Truncated) and the fingerprint itself are excluded, so the
// fingerprint is stable across page sizes and continuation positions.
//
// The input is already bounded by assembly and already canonicalized and
// deterministically ordered by AssembleEconomicDetail, so the fingerprint is
// bounded, deterministic and storage-neutral. It contains no key material and
// is never serialized to the wire.
func economicDetailSnapshotFingerprint(full []metering.Observation, detail EconomicDetail) string {
	type snapshotPayload struct {
		Version           string
		Observations      []metering.Observation
		Valuations        []economics.Valuation
		SelectedValuation []economics.Valuation
		Quantity          *ComponentQuantityComparison
		Monetary          *MonetaryDiscrepancyComparison
		Selection         *OperatorCostSelectionResult
		Heads             []SelectedCostHead
		PerBasisTotals    []EconomicDetailBasisTotal
		Totals            EconomicDetailTotals
		Margin            EconomicDetailMargin
		Coverage          EconomicDetailCoverage
		Payers            EconomicDetailPayers
		Summary           EconomicDetailSummary
		Reconciliations   []EconomicDetailReconciliation
		StatementEvidence EconomicDetailStatementEvidence
		ExecutionCoverage []EconomicDetailExecutionLeg
	}
	payload := snapshotPayload{
		Version: EconomicDetailSnapshotVersion, Observations: full,
		Valuations: detail.Valuations, SelectedValuation: detail.SelectedValuations,
		Quantity: detail.Quantity, Monetary: detail.Monetary,
		Selection: detail.Selection, Heads: detail.Heads, PerBasisTotals: detail.PerBasisTotals,
		Totals: detail.Totals, Margin: detail.Margin, Coverage: detail.Coverage,
		Payers: detail.Payers, Summary: detail.Summary, Reconciliations: detail.Reconciliations,
		StatementEvidence: detail.StatementEvidence, ExecutionCoverage: detail.ExecutionCoverage,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// Canonical DTOs always marshal; keep a deterministic fail-closed value
		// rather than returning an unverifiable empty fingerprint.
		encoded = []byte(fmt.Sprintf("%s:unencodable", EconomicDetailSnapshotVersion))
	}
	sum := sha256.Sum256(encoded)
	return EconomicDetailSnapshotVersion + ":" + hex.EncodeToString(sum[:])
}

// extendEconomicDetailSnapshotFingerprint folds repeated full-scope facts
// resolved above the durable reader (statement evidence) into an existing base
// fingerprint, so a composed reader applies one continuation boundary to the
// entire repeated fact set. It is deterministic and bounded by the already
// normalized evidence.
func extendEconomicDetailSnapshotFingerprint(base string, evidence EconomicDetailStatementEvidence) string {
	payload := struct {
		Version  string
		Base     string
		Evidence EconomicDetailStatementEvidence
	}{EconomicDetailSnapshotVersion, base, evidence}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(EconomicDetailSnapshotVersion + ":" + base + ":unencodable")
	}
	sum := sha256.Sum256(encoded)
	return EconomicDetailSnapshotVersion + ":" + hex.EncodeToString(sum[:])
}

func economicDetailBasisRank(basis economics.ValuationBasis) int {
	switch basis {
	case economics.BasisLocalExpected:
		return 0
	case economics.BasisProviderQuantityLocal:
		return 1
	case economics.BasisProviderReported:
		return 2
	case economics.BasisStatementReported:
		return 3
	case economics.BasisCustomerPolicy:
		return 4
	default:
		return 5
	}
}

func observationMatchesScope(observation metering.Observation, query EconomicDetailQuery) error {
	if observation.Subject.StoreID != query.StoreID || observation.Correlation.StoreID != query.StoreID {
		return fmt.Errorf("%w: observation %q store mismatch", ErrEconomicDetailScopeMismatch, observation.ID)
	}
	if query.TenantID != "" {
		if observation.Subject.TenantID != "" && observation.Subject.TenantID != query.TenantID {
			return fmt.Errorf("%w: observation %q tenant mismatch", ErrEconomicDetailScopeMismatch, observation.ID)
		}
		if observation.Correlation.TenantID != "" && observation.Correlation.TenantID != query.TenantID {
			return fmt.Errorf("%w: observation %q tenant mismatch", ErrEconomicDetailScopeMismatch, observation.ID)
		}
	}
	if observation.Subject.AccountID != "" && observation.Subject.AccountID != query.AccountID {
		return fmt.Errorf("%w: observation %q account mismatch", ErrEconomicDetailScopeMismatch, observation.ID)
	}
	if query.IsCallScope() {
		subjectCall := observation.Subject.BillingCallID
		correlationCall := observation.Correlation.BillingCallID
		if subjectCall == "" {
			subjectCall = observation.Subject.CallID
		}
		if correlationCall == "" {
			correlationCall = observation.Correlation.CallID
		}
		matched := subjectCall == query.BillingCallID || correlationCall == query.BillingCallID
		// Statement and account-window subjects are account-period scoped and
		// carry no call lineage; they remain in scope through store/account.
		if !matched && !isNonRequestSubject(observation.Subject.Kind) {
			return fmt.Errorf("%w: observation %q billing call mismatch", ErrEconomicDetailScopeMismatch, observation.ID)
		}
		if query.ALegID != "" {
			subjectALeg := observation.Subject.ALegID
			if subjectALeg == "" {
				subjectALeg = observation.Correlation.ALegID
			}
			if subjectALeg != "" && subjectALeg != query.ALegID {
				return fmt.Errorf("%w: observation %q A-leg mismatch", ErrEconomicDetailScopeMismatch, observation.ID)
			}
		}
		return nil
	}
	subjectALeg := observation.Subject.ALegID
	if subjectALeg == "" {
		subjectALeg = observation.Correlation.ALegID
	}
	if subjectALeg == "" || subjectALeg != query.ALegID {
		if !isNonRequestSubject(observation.Subject.Kind) {
			return fmt.Errorf("%w: observation %q A-leg mismatch", ErrEconomicDetailScopeMismatch, observation.ID)
		}
	}
	return nil
}

func isNonRequestSubject(kind metering.SubjectKind) bool {
	switch kind {
	case metering.SubjectResource, metering.SubjectAccountWindow, metering.SubjectStatementLine:
		return true
	default:
		return false
	}
}

func valuationMatchesScope(valuation economics.Valuation, query EconomicDetailQuery) error {
	if valuation.Subject.StoreID != query.StoreID {
		return fmt.Errorf("%w: valuation %q store mismatch", ErrEconomicDetailScopeMismatch, valuation.ID)
	}
	if query.TenantID != "" && valuation.Subject.TenantID != "" && valuation.Subject.TenantID != query.TenantID {
		return fmt.Errorf("%w: valuation %q tenant mismatch", ErrEconomicDetailScopeMismatch, valuation.ID)
	}
	if valuation.Subject.AccountID != "" && valuation.Subject.AccountID != query.AccountID {
		return fmt.Errorf("%w: valuation %q account mismatch", ErrEconomicDetailScopeMismatch, valuation.ID)
	}
	for i, ref := range valuation.InputObservations {
		if ref.StoreID != query.StoreID {
			return fmt.Errorf("%w: valuation %q input ref %d store mismatch", ErrEconomicDetailScopeMismatch, valuation.ID, i)
		}
	}
	for i, ref := range valuation.MissingObservations {
		if ref.StoreID != query.StoreID {
			return fmt.Errorf("%w: valuation %q missing ref %d store mismatch", ErrEconomicDetailScopeMismatch, valuation.ID, i)
		}
	}
	for i, ref := range valuation.CoverageRefs {
		if ref.Ref.StoreID != query.StoreID {
			return fmt.Errorf("%w: valuation %q coverage %d store mismatch", ErrEconomicDetailScopeMismatch, valuation.ID, i)
		}
	}
	for i, line := range valuation.Lines {
		for j, ref := range line.SourceObservationRefs {
			if ref.StoreID != query.StoreID {
				return fmt.Errorf("%w: valuation %q line %d source %d store mismatch", ErrEconomicDetailScopeMismatch, valuation.ID, i, j)
			}
		}
		for j, ref := range line.AdjustmentRefs {
			if ref.StoreID != query.StoreID {
				return fmt.Errorf("%w: valuation %q line %d adjustment %d store mismatch", ErrEconomicDetailScopeMismatch, valuation.ID, i, j)
			}
		}
	}
	if query.IsCallScope() {
		call := valuation.Subject.BillingCallID
		if call == "" {
			call = valuation.Subject.CallID
		}
		if call != "" && call != query.BillingCallID && !isNonRequestSubject(valuation.Subject.Kind) {
			return fmt.Errorf("%w: valuation %q billing call mismatch", ErrEconomicDetailScopeMismatch, valuation.ID)
		}
		if query.ALegID != "" && valuation.Subject.ALegID != "" && valuation.Subject.ALegID != query.ALegID {
			return fmt.Errorf("%w: valuation %q A-leg mismatch", ErrEconomicDetailScopeMismatch, valuation.ID)
		}
		return nil
	}
	if valuation.Subject.ALegID != "" && valuation.Subject.ALegID != query.ALegID && !isNonRequestSubject(valuation.Subject.Kind) {
		return fmt.Errorf("%w: valuation %q A-leg mismatch", ErrEconomicDetailScopeMismatch, valuation.ID)
	}
	return nil
}

func validateQuantityScope(quantity ComponentQuantityComparison, query EconomicDetailQuery) error {
	for i, item := range quantity.Items {
		for j, evidence := range item.Local {
			if evidence.Observation.StoreID != query.StoreID {
				return fmt.Errorf("%w: quantity item %d local %d store mismatch", ErrEconomicDetailScopeMismatch, i, j)
			}
		}
		for j, evidence := range item.Provider {
			if evidence.Observation.StoreID != query.StoreID {
				return fmt.Errorf("%w: quantity item %d provider %d store mismatch", ErrEconomicDetailScopeMismatch, i, j)
			}
		}
	}
	return nil
}

func validateMonetaryScope(monetary MonetaryDiscrepancyComparison, query EconomicDetailQuery) error {
	if monetary.Subject.Kind != "" {
		if monetary.Subject.StoreID != query.StoreID {
			return fmt.Errorf("%w: monetary subject store mismatch", ErrEconomicDetailScopeMismatch)
		}
		if query.TenantID != "" && monetary.Subject.TenantID != "" && monetary.Subject.TenantID != query.TenantID {
			return fmt.Errorf("%w: monetary subject tenant mismatch", ErrEconomicDetailScopeMismatch)
		}
	}
	for i := range monetary.Valuations {
		valuation := monetary.Valuations[i].Valuation
		if valuation.Subject.StoreID != query.StoreID {
			return fmt.Errorf("%w: monetary valuation %d store mismatch", ErrEconomicDetailScopeMismatch, i)
		}
	}
	if monetary.Quantity != nil {
		return validateQuantityScope(*monetary.Quantity, query)
	}
	return nil
}

// validateReconciliationScope enforces that one reconciliation entry belongs to
// the requested store/account/tenant/call/A-leg scope. The entry subject is the
// authoritative ownership root and its comparison planes are additionally
// validated for cross-scope evidence, so foreign subject or foreign evidence
// fails closed rather than leaking into another scope.
func validateReconciliationScope(entry EconomicDetailReconciliation, query EconomicDetailQuery) error {
	if err := entry.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: reconciliation subject: %v", ErrEconomicDetailInvalid, err)
	}
	if entry.Subject.StoreID != query.StoreID {
		return fmt.Errorf("%w: reconciliation subject store mismatch", ErrEconomicDetailScopeMismatch)
	}
	if query.TenantID != "" && entry.Subject.TenantID != "" && entry.Subject.TenantID != query.TenantID {
		return fmt.Errorf("%w: reconciliation subject tenant mismatch", ErrEconomicDetailScopeMismatch)
	}
	if entry.Subject.AccountID != "" && entry.Subject.AccountID != query.AccountID {
		return fmt.Errorf("%w: reconciliation subject account mismatch", ErrEconomicDetailScopeMismatch)
	}
	if query.IsCallScope() {
		call := entry.Subject.BillingCallID
		if call == "" {
			call = entry.Subject.CallID
		}
		if call != "" && call != query.BillingCallID && !isNonRequestSubject(entry.Subject.Kind) {
			return fmt.Errorf("%w: reconciliation subject billing call mismatch", ErrEconomicDetailScopeMismatch)
		}
		if query.ALegID != "" && entry.Subject.ALegID != "" && entry.Subject.ALegID != query.ALegID {
			return fmt.Errorf("%w: reconciliation subject A-leg mismatch", ErrEconomicDetailScopeMismatch)
		}
	} else if entry.Subject.ALegID != "" && entry.Subject.ALegID != query.ALegID && !isNonRequestSubject(entry.Subject.Kind) {
		return fmt.Errorf("%w: reconciliation subject A-leg mismatch", ErrEconomicDetailScopeMismatch)
	}
	if entry.Quantity != nil {
		if err := validateQuantityScope(*entry.Quantity, query); err != nil {
			return err
		}
	}
	if entry.Monetary != nil {
		if err := validateMonetaryScope(*entry.Monetary, query); err != nil {
			return err
		}
	}
	if entry.Aggregate != nil {
		if err := validateReconciliationAggregateScope(*entry.Aggregate, query); err != nil {
			return err
		}
	}
	return nil
}

// validateReconciliationAggregateScope enforces that one aggregate plane is a
// bounded projection of the requested store. The aggregate policy is required,
// and every retained finding source reference must belong to the trusted store;
// a foreign reference fails closed rather than leaking another store's
// aggregate evidence.
func validateReconciliationAggregateScope(aggregate ReconciliationAggregate, query EconomicDetailQuery) error {
	if !validEconomicIdentity(aggregate.Policy.ID, metering.MaxSchemaIDBytes) || !validEconomicIdentity(aggregate.Policy.Version, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: reconciliation aggregate policy is required", ErrEconomicDetailInvalid)
	}
	for i := range aggregate.Findings {
		for j, ref := range aggregate.Findings[i].SourceObservationRefs {
			if ref.StoreID != query.StoreID {
				return fmt.Errorf("%w: reconciliation aggregate finding %d ref %d store mismatch", ErrEconomicDetailScopeMismatch, i, j)
			}
		}
	}
	return nil
}

// normalizeEconomicDetailReconciliations validates every independent in-scope
// reconciliation subject, rejects a genuine duplicate subject identity instead
// of silently merging it, deep-copies each entry and returns them in stable
// deterministic subject order. The explicit bound fails closed.
func normalizeEconomicDetailReconciliations(in []EconomicDetailReconciliation, query EconomicDetailQuery) ([]EconomicDetailReconciliation, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxEconomicDetailReconciliations {
		return nil, fmt.Errorf("%w: reconciliations=%d max=%d", ErrEconomicDetailBoundExceeded, len(in), MaxEconomicDetailReconciliations)
	}
	out := make([]EconomicDetailReconciliation, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i := range in {
		entry := in[i]
		if err := validateReconciliationScope(entry, query); err != nil {
			return nil, err
		}
		key := economicDetailSubjectSortKey(entry.Subject)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: duplicate reconciliation subject kind=%q", ErrEconomicDetailInvalid, entry.Subject.Kind)
		}
		seen[key] = struct{}{}
		out = append(out, cloneEconomicDetailReconciliation(entry))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return economicDetailSubjectSortKey(out[i].Subject) < economicDetailSubjectSortKey(out[j].Subject)
	})
	return out, nil
}

// normalizeEconomicDetailAllocationState bounds and scope-validates the
// allocation correction state before it is projected. Every retained reference
// is an immutable in-store allocation identity plus payload hash, so a foreign
// store fails closed. A nil state stays nil: an absent allocation lineage is
// never presented as a known-complete state.
func normalizeEconomicDetailAllocationState(in *EconomicDetailAllocationState, query EconomicDetailQuery) (*EconomicDetailAllocationState, error) {
	if in == nil {
		return nil, nil
	}
	if !in.Status.IsKnown() {
		return nil, fmt.Errorf("%w: allocation state status %q", ErrEconomicDetailInvalid, in.Status)
	}
	groups := [][]economics.AllocationRef{in.Pending, in.PendingSupersedes, in.Superseded}
	for _, refs := range groups {
		if len(refs) > MaxEconomicDetailAllocations {
			return nil, fmt.Errorf("%w: allocation state refs=%d max=%d", ErrEconomicDetailBoundExceeded, len(refs), MaxEconomicDetailAllocations)
		}
		for i := range refs {
			if err := refs[i].Validate(); err != nil {
				return nil, fmt.Errorf("%w: allocation state ref: %v", ErrEconomicDetailInvalid, err)
			}
			if refs[i].StoreID != "" && refs[i].StoreID != query.StoreID {
				return nil, fmt.Errorf("%w: allocation state ref store mismatch", ErrEconomicDetailScopeMismatch)
			}
		}
	}
	return &EconomicDetailAllocationState{
		Status: in.Status, Complete: in.Complete, Payable: in.Payable, Redacted: in.Redacted,
		Pending:           cloneAllocationRefs(in.Pending),
		PendingSupersedes: cloneAllocationRefs(in.PendingSupersedes),
		Superseded:        cloneAllocationRefs(in.Superseded),
	}, nil
}

func cloneAllocationRefs(in []economics.AllocationRef) []economics.AllocationRef {
	if len(in) == 0 {
		return nil
	}
	return append([]economics.AllocationRef(nil), in...)
}

// projectSingularReconciliation projects the singular convenience comparison
// fields from the deterministic-first entry that carries a comparison plane.
// The full bounded set remains authoritative and is never replaced by this
// projection.
func projectSingularReconciliation(reconciliations []EconomicDetailReconciliation) (*ComponentQuantityComparison, *MonetaryDiscrepancyComparison) {
	for i := range reconciliations {
		if reconciliations[i].Quantity == nil && reconciliations[i].Monetary == nil {
			continue
		}
		var quantity *ComponentQuantityComparison
		var monetary *MonetaryDiscrepancyComparison
		if reconciliations[i].Quantity != nil {
			cloned := cloneComponentQuantityComparison(*reconciliations[i].Quantity)
			quantity = &cloned
		}
		if reconciliations[i].Monetary != nil {
			cloned := cloneMonetaryComparison(*reconciliations[i].Monetary)
			monetary = &cloned
		}
		return quantity, monetary
	}
	return nil, nil
}

func cloneEconomicDetailReconciliation(in EconomicDetailReconciliation) EconomicDetailReconciliation {
	out := EconomicDetailReconciliation{Subject: in.Subject.Clone()}
	if in.Quantity != nil {
		cloned := cloneComponentQuantityComparison(*in.Quantity)
		out.Quantity = &cloned
	}
	if in.Monetary != nil {
		cloned := cloneMonetaryComparison(*in.Monetary)
		out.Monetary = &cloned
	}
	if in.Aggregate != nil {
		cloned := cloneReconciliationAggregate(*in.Aggregate)
		out.Aggregate = &cloned
	}
	return out
}

// cloneReconciliationAggregate deep-copies one aggregate plane, including every
// finding, row, exact amount and evidence identity, so a caller cannot mutate
// retained assembly output through a shared reference.
func cloneReconciliationAggregate(in ReconciliationAggregate) ReconciliationAggregate {
	out := ReconciliationAggregate{Policy: in.Policy}
	if len(in.Findings) > 0 {
		out.Findings = make([]ReconciliationFinding, len(in.Findings))
		for i := range in.Findings {
			out.Findings[i] = cloneReconciliationFinding(in.Findings[i])
		}
	}
	if len(in.Rows) > 0 {
		out.Rows = make([]ReconciliationAggregateRow, len(in.Rows))
		for i := range in.Rows {
			row := in.Rows[i]
			row.GrossAbsoluteDiscrepancy = cloneExactAmount(row.GrossAbsoluteDiscrepancy)
			row.DiscrepantAbsoluteDiscrepancy = cloneExactAmount(row.DiscrepantAbsoluteDiscrepancy)
			row.NetSignedDiscrepancy = cloneExactAmount(row.NetSignedDiscrepancy)
			row.StatusCounts = append([]ReconciliationStatusCount(nil), row.StatusCounts...)
			row.MissingIDs = append([]string(nil), row.MissingIDs...)
			row.IncomparableIDs = append([]string(nil), row.IncomparableIDs...)
			row.ConflictIDs = append([]string(nil), row.ConflictIDs...)
			out.Rows[i] = row
		}
	}
	return out
}

// economicDetailSubjectKindRank orders in-scope subjects primary-first: A-leg,
// then billing call, then B-leg. Any other kind keeps a stable trailing rank.
func economicDetailSubjectKindRank(kind metering.SubjectKind) int {
	switch kind {
	case metering.SubjectALeg:
		return 0
	case metering.SubjectBillingCall:
		return 1
	case metering.SubjectBLeg:
		return 2
	default:
		return 3
	}
}

// economicDetailSubjectSortKey is the total deterministic identity key for an
// in-scope reconciliation subject. It includes every subject field, so two
// entries with the same key are one subject identity and distinct identities
// never collide.
func economicDetailSubjectSortKey(subject metering.SubjectRef) string {
	var builder strings.Builder
	fmt.Fprint(&builder, economicDetailSubjectKindRank(subject.Kind))
	builder.WriteByte(0)
	writeEconomicSubjectIdentity(&builder, subject)
	return builder.String()
}

func validateSelectionScope(selection OperatorCostSelectionResult, query EconomicDetailQuery) error {
	if selection.Subject.StoreID != query.StoreID {
		return fmt.Errorf("%w: selection subject store mismatch", ErrEconomicDetailScopeMismatch)
	}
	if query.TenantID != "" && selection.Subject.TenantID != "" && selection.Subject.TenantID != query.TenantID {
		return fmt.Errorf("%w: selection subject tenant mismatch", ErrEconomicDetailScopeMismatch)
	}
	for i, candidate := range selection.Candidates {
		for j, ref := range candidate.SourceRefs {
			if ref.StoreID != query.StoreID {
				return fmt.Errorf("%w: selection candidate %d source %d store mismatch", ErrEconomicDetailScopeMismatch, i, j)
			}
		}
	}
	return nil
}

func headMatchesScope(head SelectedCostHead, query EconomicDetailQuery) error {
	if head.AccountID != query.AccountID {
		return fmt.Errorf("%w: head %q account mismatch", ErrEconomicDetailScopeMismatch, head.HeadKey)
	}
	if head.Subject.StoreID != query.StoreID {
		return fmt.Errorf("%w: head %q store mismatch", ErrEconomicDetailScopeMismatch, head.HeadKey)
	}
	if query.TenantID != "" && head.Subject.TenantID != "" && head.Subject.TenantID != query.TenantID {
		return fmt.Errorf("%w: head %q tenant mismatch", ErrEconomicDetailScopeMismatch, head.HeadKey)
	}
	if query.IsCallScope() {
		if head.CallID.String() != query.BillingCallID {
			return fmt.Errorf("%w: head %q billing call mismatch", ErrEconomicDetailScopeMismatch, head.HeadKey)
		}
		if query.ALegID != "" && head.Subject.ALegID != "" && head.Subject.ALegID != query.ALegID {
			return fmt.Errorf("%w: head %q A-leg mismatch", ErrEconomicDetailScopeMismatch, head.HeadKey)
		}
		return nil
	}
	if head.Subject.ALegID != "" && head.Subject.ALegID != query.ALegID {
		return fmt.Errorf("%w: head %q A-leg mismatch", ErrEconomicDetailScopeMismatch, head.HeadKey)
	}
	return nil
}

// economicDetailPersistedSelection projects the persisted selected/posted heads
// into the singular convenience selection and the scope ambiguity signal.
//
// A durable head is the frozen outcome of one economic charge; the full Phase
// 12 candidate set is not retained as a unit. This function therefore:
//   - counts the heads that carry a frozen selection;
//   - treats a scope with more than one independent head (or one selected head
//     alongside other heads) as ambiguous, so independent calls/B-legs/head
//     keys are never collapsed or summed merely because a basis matches;
//   - returns a candidate-less convenience result only for the one truthful
//     case: exactly one authoritative head carrying a frozen selection. The
//     empty candidate set is intentional and must never be filled with
//     candidates that were not persisted.
func economicDetailPersistedSelection(heads []SelectedCostHead) (*OperatorCostSelectionResult, bool, int) {
	selectedCount := 0
	var single *SelectedCostValuation
	var subject metering.SubjectRef
	for i := range heads {
		if heads[i].Selected == nil {
			continue
		}
		selectedCount++
		if single == nil {
			selected := heads[i].Selected.Clone()
			single = &selected
			subject = heads[i].Subject.Clone()
		}
	}
	ambiguous := selectedCount > 1 || (selectedCount == 1 && len(heads) > 1)
	if selectedCount != 1 || ambiguous {
		return nil, ambiguous, selectedCount
	}
	return &OperatorCostSelectionResult{
		Subject: subject, Provenance: single.Provenance, Currency: single.Currency,
		Status: single.Status, Reason: single.Reason, Basis: single.Basis,
		Amount: cloneExactAmount(single.Amount), NativeAmount: cloneExactAmount(single.NativeAmount),
		FX: cloneOperatorCostFX(single.FX),
		// Candidates stay nil: the full candidate set was never persisted.
	}, false, selectedCount
}

// economicDetailSelectionCompleteness maps a persisted selection status to the
// neutral completeness contract. It is a deterministic interpretation of the
// selection plane, not a re-rating: final and known-zero selections are
// complete, provisional partial, conflicts conflicted and incomparable or
// non-payable selections unavailable.
func economicDetailSelectionCompleteness(status OperatorCostSelectionStatus) economics.Completeness {
	switch status {
	case OperatorCostSelectionStatusFinal, OperatorCostSelectionStatusKnownZero:
		return economics.CompletenessComplete
	case OperatorCostSelectionStatusProvisional:
		return economics.CompletenessPartial
	case OperatorCostSelectionStatusConflict:
		return economics.CompletenessConflict
	case OperatorCostSelectionStatusIncomparable, OperatorCostSelectionStatusNotOperatorPayable:
		return economics.CompletenessUnavailable
	default:
		return economics.CompletenessUnknown
	}
}

// validateAllocationScope enforces that one allocation contribution belongs to
// the requested store/account/tenant/call/A-leg scope. Store identity alone is
// insufficient: a shared conserved envelope can carry targets of another call,
// leg, account or tenant, and those contributions must never be echoed back
// under a scope they do not own. The rule mirrors observation scope handling:
// explicit foreign ownership fails closed, while an absent optional identity
// (legacy/unallocated targets) is tolerated.
func validateAllocationScope(line AllocatedCostLine, query EconomicDetailQuery) error {
	if line.SourceSubject.StoreID != "" && line.SourceSubject.StoreID != query.StoreID {
		return fmt.Errorf("%w: allocation %q source store mismatch", ErrEconomicDetailScopeMismatch, line.AllocationID)
	}
	if line.Target.StoreID != "" && line.Target.StoreID != query.StoreID {
		return fmt.Errorf("%w: allocation %q target store mismatch", ErrEconomicDetailScopeMismatch, line.AllocationID)
	}
	if line.Target.AccountID != "" && line.Target.AccountID != query.AccountID {
		return fmt.Errorf("%w: allocation %q target account mismatch", ErrEconomicDetailScopeMismatch, line.AllocationID)
	}
	if query.TenantID != "" && line.Target.TenantID != "" && line.Target.TenantID != query.TenantID {
		return fmt.Errorf("%w: allocation %q target tenant mismatch", ErrEconomicDetailScopeMismatch, line.AllocationID)
	}
	if query.IsCallScope() {
		call := line.Target.BillingCallID
		if call == "" {
			call = line.Target.CallID
		}
		if call != "" && call != query.BillingCallID {
			return fmt.Errorf("%w: allocation %q target billing call mismatch", ErrEconomicDetailScopeMismatch, line.AllocationID)
		}
		if query.ALegID != "" && line.Target.ALegID != "" && line.Target.ALegID != query.ALegID {
			return fmt.Errorf("%w: allocation %q target A-leg mismatch", ErrEconomicDetailScopeMismatch, line.AllocationID)
		}
		return nil
	}
	if line.Target.ALegID != "" && line.Target.ALegID != query.ALegID {
		return fmt.Errorf("%w: allocation %q target A-leg mismatch", ErrEconomicDetailScopeMismatch, line.AllocationID)
	}
	return nil
}

// normalizeEconomicDetailExecutionCoverage validates, scopes, dedupes and
// deterministically orders the bounded execution facts carried into assembly.
// Every in-scope authoritative leg participates in validation and in the
// repeated full-scope snapshot, so a leg added or reclassified between pages
// invalidates an outstanding continuation.
func normalizeEconomicDetailExecutionCoverage(in []EconomicDetailExecutionLeg, query EconomicDetailQuery) ([]EconomicDetailExecutionLeg, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]EconomicDetailExecutionLeg, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i := range in {
		leg := in[i]
		if err := leg.Validate(); err != nil {
			return nil, fmt.Errorf("%w: execution leg %d: %v", ErrEconomicDetailInvalid, i, err)
		}
		if err := validateExecutionCoverageScope(leg, query); err != nil {
			return nil, err
		}
		leg.Subject = leg.Subject.Clone()
		key := costCoverageSubjectIdentity(leg.Subject) + "\x00" + fmt.Sprint(leg.AttemptSeq)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, leg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := economicDetailSubjectSortKey(out[i].Subject)
		right := economicDetailSubjectSortKey(out[j].Subject)
		if left != right {
			return left < right
		}
		if out[i].AttemptSeq != out[j].AttemptSeq {
			return out[i].AttemptSeq < out[j].AttemptSeq
		}
		return out[i].Outcome < out[j].Outcome
	})
	return out, nil
}

// validateExecutionCoverageScope enforces the same trusted store/tenant/account
// and call/A-leg scope boundaries as observations, so a foreign leg can never
// participate in cost completeness.
func validateExecutionCoverageScope(leg EconomicDetailExecutionLeg, query EconomicDetailQuery) error {
	subject := leg.Subject
	if subject.StoreID != query.StoreID {
		return fmt.Errorf("%w: execution leg %q store mismatch", ErrEconomicDetailScopeMismatch, subject.BLegID)
	}
	if query.TenantID != "" && subject.TenantID != "" && subject.TenantID != query.TenantID {
		return fmt.Errorf("%w: execution leg %q tenant mismatch", ErrEconomicDetailScopeMismatch, subject.BLegID)
	}
	if subject.AccountID != "" && subject.AccountID != query.AccountID {
		return fmt.Errorf("%w: execution leg %q account mismatch", ErrEconomicDetailScopeMismatch, subject.BLegID)
	}
	if query.IsCallScope() {
		subjectCall := subject.BillingCallID
		if subjectCall == "" {
			subjectCall = subject.CallID
		}
		if subjectCall != query.BillingCallID {
			return fmt.Errorf("%w: execution leg %q billing call mismatch", ErrEconomicDetailScopeMismatch, subject.BLegID)
		}
		if query.ALegID != "" && subject.ALegID != "" && subject.ALegID != query.ALegID {
			return fmt.Errorf("%w: execution leg %q A-leg mismatch", ErrEconomicDetailScopeMismatch, subject.BLegID)
		}
		return nil
	}
	if subject.ALegID == "" || subject.ALegID != query.ALegID {
		return fmt.Errorf("%w: execution leg %q A-leg mismatch", ErrEconomicDetailScopeMismatch, subject.BLegID)
	}
	return nil
}

func projectBasisTotals(valuations []economics.Valuation) ([]EconomicDetailBasisTotal, []string) {
	out := make([]EconomicDetailBasisTotal, 0, len(valuations))
	currencySet := map[string]struct{}{}
	for _, valuation := range valuations {
		totals := append([]economics.CurrencyTotal(nil), valuation.Totals...)
		for _, total := range totals {
			currencySet[total.Currency] = struct{}{}
		}
		out = append(out, EconomicDetailBasisTotal{
			Basis: valuation.Basis, ValuationID: valuation.ID, CurrencyTotals: totals,
			Completeness: valuation.Completeness, Payer: valuation.Payer,
			Rater: valuation.Rater, Tariff: valuation.Tariff, Policy: valuation.Policy,
		})
	}
	currencies := make([]string, 0, len(currencySet))
	for currency := range currencySet {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	return out, currencies
}

func projectCoverage(observations []metering.Observation, valuations []economics.Valuation, allocations []AllocatedCostLine) (EconomicDetailCoverage, error) {
	coverage := EconomicDetailCoverage{}
	missingSeen := map[string]metering.ObservationRef{}
	for _, valuation := range valuations {
		for _, ref := range valuation.MissingObservations {
			key := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + fmt.Sprint(ref.Revision) + "\x00" + ref.PayloadHash
			if _, exists := missingSeen[key]; !exists {
				missingSeen[key] = ref
			}
		}
	}
	for _, ref := range missingSeen {
		coverage.MissingRefs = append(coverage.MissingRefs, ref)
	}
	sort.SliceStable(coverage.MissingRefs, func(i, j int) bool {
		if coverage.MissingRefs[i].StoreID != coverage.MissingRefs[j].StoreID {
			return coverage.MissingRefs[i].StoreID < coverage.MissingRefs[j].StoreID
		}
		if coverage.MissingRefs[i].ObservationID != coverage.MissingRefs[j].ObservationID {
			return coverage.MissingRefs[i].ObservationID < coverage.MissingRefs[j].ObservationID
		}
		return coverage.MissingRefs[i].Revision < coverage.MissingRefs[j].Revision
	})
	if len(coverage.MissingRefs) > MaxEconomicDetailMissingRefs {
		return EconomicDetailCoverage{}, fmt.Errorf("%w: missing refs=%d max=%d", ErrEconomicDetailBoundExceeded, len(coverage.MissingRefs), MaxEconomicDetailMissingRefs)
	}
	coverage.MissingCount = len(coverage.MissingRefs)

	for _, observation := range observations {
		for _, charge := range observation.Charges {
			if charge.Component == nil {
				coverage.AggregateCharges = append(coverage.AggregateCharges, charge.Clone())
			}
		}
	}
	for _, valuation := range valuations {
		for _, line := range valuation.Lines {
			if line.ReportedAggregate {
				coverage.AggregateOnly = true
			}
		}
	}
	if len(coverage.AggregateCharges) > 0 {
		coverage.AggregateOnly = true
	}
	sort.SliceStable(coverage.AggregateCharges, func(i, j int) bool {
		return coverage.AggregateCharges[i].ChargeItemID < coverage.AggregateCharges[j].ChargeItemID
	})

	edgeSeen := map[string]metering.ChargeCoverageRef{}
	for _, valuation := range valuations {
		for _, edge := range valuation.CoverageRefs {
			key := edge.Ref.StoreID + "\x00" + edge.Ref.ObservationID + "\x00" + fmt.Sprint(edge.Ref.Revision) + "\x00" + edge.Ref.ChargeItemID + "\x00" + string(edge.Relation)
			if _, exists := edgeSeen[key]; !exists {
				edgeSeen[key] = edge
			}
		}
	}
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			for _, edge := range charge.Covers {
				key := edge.Ref.StoreID + "\x00" + edge.Ref.ObservationID + "\x00" + fmt.Sprint(edge.Ref.Revision) + "\x00" + edge.Ref.ChargeItemID + "\x00" + string(edge.Relation)
				if _, exists := edgeSeen[key]; !exists {
					edgeSeen[key] = edge
				}
			}
		}
	}
	for _, edge := range edgeSeen {
		coverage.CoverageRefs = append(coverage.CoverageRefs, edge)
	}
	sort.SliceStable(coverage.CoverageRefs, func(i, j int) bool {
		left, right := coverage.CoverageRefs[i], coverage.CoverageRefs[j]
		if left.Ref.StoreID != right.Ref.StoreID {
			return left.Ref.StoreID < right.Ref.StoreID
		}
		if left.Ref.ObservationID != right.Ref.ObservationID {
			return left.Ref.ObservationID < right.Ref.ObservationID
		}
		if left.Ref.Revision != right.Ref.Revision {
			return left.Ref.Revision < right.Ref.Revision
		}
		if left.Ref.ChargeItemID != right.Ref.ChargeItemID {
			return left.Ref.ChargeItemID < right.Ref.ChargeItemID
		}
		return left.Relation < right.Relation
	})
	if len(coverage.CoverageRefs) > MaxEconomicDetailCoverageRefs {
		return EconomicDetailCoverage{}, fmt.Errorf("%w: coverage refs=%d max=%d", ErrEconomicDetailBoundExceeded, len(coverage.CoverageRefs), MaxEconomicDetailCoverageRefs)
	}

	subjectSeen := map[string]metering.SubjectRef{}
	for _, observation := range observations {
		if !isNonRequestSubject(observation.Subject.Kind) {
			continue
		}
		key := string(observation.Subject.Kind) + "\x00" + observation.Subject.StoreID + "\x00" + observation.Subject.ResourceID + "\x00" + observation.Subject.PeriodID + "\x00" + observation.Subject.ProviderAccountKey + "\x00" + observation.Subject.PoolID + "\x00" + observation.Subject.WindowID + "\x00" + observation.Subject.StatementID + "\x00" + observation.Subject.StatementLineID
		if _, exists := subjectSeen[key]; !exists {
			subjectSeen[key] = observation.Subject.Clone()
		}
	}
	for _, subject := range subjectSeen {
		coverage.NonRequestSubjects = append(coverage.NonRequestSubjects, subject)
	}
	sort.SliceStable(coverage.NonRequestSubjects, func(i, j int) bool {
		if coverage.NonRequestSubjects[i].Kind != coverage.NonRequestSubjects[j].Kind {
			return coverage.NonRequestSubjects[i].Kind < coverage.NonRequestSubjects[j].Kind
		}
		return coverage.NonRequestSubjects[i].StoreID < coverage.NonRequestSubjects[j].StoreID
	})
	coverage.Allocations = append([]AllocatedCostLine(nil), allocations...)
	return coverage, nil
}

func projectPayers(observations []metering.Observation, valuations []economics.Valuation) EconomicDetailPayers {
	payers := EconomicDetailPayers{}
	for _, valuation := range valuations {
		payers.ByBasis = append(payers.ByBasis, EconomicDetailBasisPayer{Basis: valuation.Basis, Payer: valuation.Payer})
	}
	sort.SliceStable(payers.ByBasis, func(i, j int) bool {
		left, right := economicDetailBasisRank(payers.ByBasis[i].Basis), economicDetailBasisRank(payers.ByBasis[j].Basis)
		if left != right {
			return left < right
		}
		return payers.ByBasis[i].Basis < payers.ByBasis[j].Basis
	})
	distinctSeen := map[string]metering.PaymentParty{}
	mark := func(payer metering.PaymentParty) {
		key := string(payer.Kind) + "\x00" + payer.ID
		if _, exists := distinctSeen[key]; !exists {
			distinctSeen[key] = payer
		}
		if payer.Kind == metering.PaymentPartyOperator {
			payers.HasOperatorPayable = true
		}
		// Unresolved payer presence (absent, explicit unknown, or unallocated)
		// is monotonic across the full scope: a resolved customer/operator payer
		// observed later never clears an earlier unresolved payer.
		if payer.IsUnknown() {
			payers.HasUnallocated = true
		}
	}
	for _, valuation := range valuations {
		mark(valuation.Payer)
	}
	byok := false
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			mark(charge.Payer)
			if charge.Payer.Kind == metering.PaymentPartyCustomer {
				byok = true
			}
		}
	}
	for _, valuation := range valuations {
		if valuation.Basis == economics.BasisCustomerPolicy {
			continue
		}
		if valuation.Payer.Kind == metering.PaymentPartyCustomer {
			byok = true
		}
	}
	payers.HasCustomerBYOK = byok
	for _, payer := range distinctSeen {
		payers.Distinct = append(payers.Distinct, payer)
	}
	sort.SliceStable(payers.Distinct, func(i, j int) bool {
		if payers.Distinct[i].Kind != payers.Distinct[j].Kind {
			return payers.Distinct[i].Kind < payers.Distinct[j].Kind
		}
		return payers.Distinct[i].ID < payers.Distinct[j].ID
	})
	return payers
}

func projectSelectedTotals(selection *OperatorCostSelectionResult, valuations []economics.Valuation, currencies []string, selectedHeadCount int, ambiguous bool) EconomicDetailTotals {
	totals := EconomicDetailTotals{
		Currencies:        append([]string(nil), currencies...),
		SelectedHeadCount: selectedHeadCount,
		SelectedAmbiguous: ambiguous,
	}
	if selection == nil {
		return totals
	}
	totals.SelectedCurrency = selection.Currency
	totals.SelectedBasis = selection.Basis
	totals.SelectedStatus = selection.Status
	totals.SelectedReason = selection.Reason
	if selection.Amount != nil {
		totals.SelectedAmount = cloneExactAmount(selection.Amount)
	}
	// A candidate carries the retained completeness of the frozen alternative.
	// A head-derived convenience selection has no candidates, so completeness is
	// derived deterministically from the persisted selection status instead of
	// being invented.
	totals.SelectedCompleteness = economicDetailSelectionCompleteness(selection.Status)
	for _, candidate := range selection.Candidates {
		if candidate.Basis == selection.Basis {
			totals.SelectedCompleteness = candidate.Completeness
			break
		}
	}
	return totals
}

// projectMargin derives the explicit margin from the full assembled scope. The
// amount remains anchored to one unambiguous selected cost head and the
// approved retail (customer-policy) projection; overlapping independent heads
// are never summed and no second monetary authority is introduced. Completeness
// is monotonic with full-scope evidence completeness: it additionally requires
// every retained reconciliation identity, the authoritative allocation coverage
// and the bounded full-scope cost-coverage projection to be complete, so a
// pending/missing allocation predecessor, a later incomplete reconciliation, or
// an attributable executed subject/charge with no authoritative coverage can
// never coexist with a complete margin.
func projectMargin(selection *OperatorCostSelectionResult, valuations []economics.Valuation, quantity *ComponentQuantityComparison, monetary *MonetaryDiscrepancyComparison, reconciliations []EconomicDetailReconciliation, allocationState *EconomicDetailAllocationState, currencies []string, ambiguous bool, costCoverage EconomicDetailCostCoverage) EconomicDetailMargin {
	if ambiguous {
		// Multiple independent persisted heads are never summed or FX-converted,
		// so no single truthful margin exists for the scope.
		return EconomicDetailMargin{Complete: false, Reason: "ambiguous_selected"}
	}
	if len(currencies) > 1 {
		return EconomicDetailMargin{Complete: false, Reason: "multi_currency"}
	}
	if !economicDetailReconciliationsComplete(quantity, monetary, reconciliations) {
		// A complete margin requires complete comparison evidence for every
		// retained independent reconciliation identity, not only the singular
		// convenience projection; partial, incomparable or conflicted evidence
		// cannot support it.
		return EconomicDetailMargin{Complete: false, Reason: "comparison_incomparable"}
	}
	if selection == nil {
		return EconomicDetailMargin{Complete: false, Reason: "missing_selected"}
	}
	if selection.Status == OperatorCostSelectionStatusNotOperatorPayable {
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "not_operator_payable"}
	}
	// A final selected amount or an explicitly proven known-zero selection is a
	// definite persisted fact; provisional, unknown, incomparable and conflict
	// selections remain explicitly incomplete.
	if (selection.Status != OperatorCostSelectionStatusFinal && selection.Status != OperatorCostSelectionStatusKnownZero) || selection.Amount == nil {
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "selected_incomplete"}
	}
	if allocationState != nil && (!allocationState.Complete || !allocationState.Payable) {
		// Allocation coverage is part of COGS coverage (parent design C4): a
		// pending or missing supersession predecessor leaves the operator cost
		// contribution set incomplete, so no complete margin can be asserted.
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "allocation_incomplete"}
	}
	retails := economicDetailCustomerPolicyValuations(valuations)
	if len(retails) == 0 {
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "missing_retail"}
	}
	if len(retails) > 1 {
		// Independent retail identities cannot be collapsed, summed or
		// arbitrarily reduced to one without asserting an invented margin.
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "retail_ambiguous"}
	}
	retail := &retails[0]
	if retail.Completeness != economics.CompletenessComplete {
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "retail_incomplete"}
	}
	retailTotal := findCurrencyTotal(*retail, selection.Currency)
	if retailTotal == nil {
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "currency_mismatch"}
	}
	retailRat, exact, err := monetaryTotalRat(*retailTotal)
	if err != nil || !exact {
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "retail_incomplete"}
	}
	selectedRat, err := selection.Amount.Rat()
	if err != nil {
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "selected_incomplete"}
	}
	if costCoverage.AllocationUnresolvedCount > 0 {
		// An active attributable monetary allocation with no explicit
		// selected-result inclusion proof cannot coexist with a complete margin:
		// the selected amount would otherwise understate operator COGS. The
		// allocation amount is never summed in; it stays unresolved until the
		// frozen selected result names its exact revision.
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "allocation_coverage_unresolved"}
	}
	if !costCoverage.Complete {
		// Every attributable operator-payable cost subject/charge must be proven
		// covered by the selected result, known-zero, BYOK, or explicitly
		// inclusive; an observation-only or unpriced subject can never coexist
		// with a complete margin.
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "cost_coverage_unresolved"}
	}
	delta := new(big.Rat).Sub(retailRat, selectedRat)
	amount, err := newMonetaryExactAmount(selection.Currency, delta)
	if err != nil {
		return EconomicDetailMargin{Currency: selection.Currency, Complete: false, Reason: "selected_incomplete"}
	}
	return EconomicDetailMargin{Currency: selection.Currency, Amount: &amount, Complete: true, Reason: "complete"}
}

// economicDetailCustomerPolicyValuations returns every independent retail
// (customer-policy) valuation of the scope. Each is its own logical stream
// identity, so more than one is genuine ambiguity rather than a revision of one
// retail fact.
func economicDetailCustomerPolicyValuations(valuations []economics.Valuation) []economics.Valuation {
	var out []economics.Valuation
	for i := range valuations {
		if valuations[i].Basis == economics.BasisCustomerPolicy {
			out = append(out, valuations[i])
		}
	}
	return out
}

// economicDetailReconciliationsComplete reports whether every retained
// independent reconciliation identity is complete enough to support a complete
// margin. When the authoritative set is supplied it subsumes the singular
// convenience projection and every entry is checked; otherwise only the
// singular comparison planes are. An entry carrying no comparison plane is
// unproven and fails closed. An aggregate plane is complete exactly when it
// retains no missing, incomparable, conflicting or partial classification, so a
// complete aggregate can satisfy reconciliation completeness without ever
// becoming a monetary amount.
func economicDetailReconciliationsComplete(quantity *ComponentQuantityComparison, monetary *MonetaryDiscrepancyComparison, reconciliations []EconomicDetailReconciliation) bool {
	if len(reconciliations) > 0 {
		for i := range reconciliations {
			entry := reconciliations[i]
			if entry.Quantity == nil && entry.Monetary == nil && entry.Aggregate == nil {
				return false
			}
			if entry.Quantity != nil && !economicDetailQuantityComparisonComplete(*entry.Quantity) {
				return false
			}
			if entry.Monetary != nil && entry.Monetary.Status != MonetaryDiscrepancyComplete {
				return false
			}
			if entry.Aggregate != nil && !economicDetailAggregateComplete(*entry.Aggregate) {
				return false
			}
		}
		return true
	}
	if quantity != nil && !economicDetailQuantityComparisonComplete(*quantity) {
		return false
	}
	if monetary != nil && monetary.Status != MonetaryDiscrepancyComplete {
		return false
	}
	return true
}

// economicDetailAggregateComplete reports whether one retained aggregate plane
// is proven complete enough to support a complete margin. A row that retains
// missing, incomparable or conflicting evidence identity, or any finding
// classified partial, is unproven. No aggregate amount is ever read as money:
// completeness is derived only from the bounded classification distribution.
func economicDetailAggregateComplete(aggregate ReconciliationAggregate) bool {
	for i := range aggregate.Rows {
		row := aggregate.Rows[i]
		if len(row.MissingIDs) > 0 || len(row.IncomparableIDs) > 0 || len(row.ConflictIDs) > 0 {
			return false
		}
		for _, count := range row.StatusCounts {
			switch count.Status {
			case ReconciliationStatusMatched, ReconciliationStatusWithinTolerance, ReconciliationStatusDiscrepant:
			default:
				return false
			}
		}
	}
	return true
}

func economicDetailQuantityComparisonComplete(quantity ComponentQuantityComparison) bool {
	if !quantity.Complete {
		return false
	}
	switch quantity.Status {
	case ReconciliationStatusMatched, ReconciliationStatusDiscrepant, ReconciliationStatusWithinTolerance:
		return true
	default:
		return false
	}
}

func findCurrencyTotal(valuation economics.Valuation, currency string) *economics.CurrencyTotal {
	for i := range valuation.Totals {
		if valuation.Totals[i].Currency == currency {
			return &valuation.Totals[i]
		}
	}
	return nil
}

func cloneMonetaryComparison(in MonetaryDiscrepancyComparison) MonetaryDiscrepancyComparison {
	out := MonetaryDiscrepancyComparison{Status: in.Status, Reason: in.Reason, Subject: in.Subject.Clone()}
	out.Rows = append([]MonetaryDiscrepancyRow(nil), in.Rows...)
	for i := range in.Rows {
		row := in.Rows[i]
		if row.MeteringCostEffect.Amount != nil {
			row.MeteringCostEffect.Amount = cloneExactAmount(row.MeteringCostEffect.Amount)
		}
		if row.ReportedPriceResidual.Amount != nil {
			row.ReportedPriceResidual.Amount = cloneExactAmount(row.ReportedPriceResidual.Amount)
		}
		if row.EndToEndCostDelta.Amount != nil {
			row.EndToEndCostDelta.Amount = cloneExactAmount(row.EndToEndCostDelta.Amount)
		}
		out.Rows[i] = row
	}
	out.Valuations = make([]MonetaryValuationEvidence, len(in.Valuations))
	for i, evidence := range in.Valuations {
		out.Valuations[i] = MonetaryValuationEvidence{Role: evidence.Role, Valuation: evidence.Valuation.Clone()}
	}
	if in.Quantity != nil {
		cloned := cloneComponentQuantityComparison(*in.Quantity)
		out.Quantity = &cloned
	}
	return out
}

func cloneOperatorCostSelection(in OperatorCostSelectionResult) OperatorCostSelectionResult {
	out := in
	out.Subject = in.Subject.Clone()
	out.FX = cloneOperatorCostFX(in.FX)
	out.Amount = cloneExactAmount(in.Amount)
	out.NativeAmount = cloneExactAmount(in.NativeAmount)
	out.Candidates = canonicalOperatorCostCandidates(in.Candidates)
	return out
}
