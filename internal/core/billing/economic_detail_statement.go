package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Finding 4B core contract: statement-origin S evidence and statement detail.
//
// The embedded leg observations loaded by the durable detail adapter are not
// statement evidence. S-basis valuations reference verified statement-line
// observations that live in the explicit observation journal; the pure detail
// model must carry them separately, with exact statement/line/observation
// lineage and an explicit enumeration of missing, unattributed or unresolved
// references. This file contains no SQL, no journal coupling and no rating: a
// consumer-owned EconomicDetailStatementSource supplies exact retained
// observations by immutable reference, and the loader classifies each one.

var (
	// ErrEconomicDetailStatementObservationMissing identifies a referenced
	// statement observation that the composed source authoritatively reports as
	// not retained. It is deliberately distinct from a failing or unavailable
	// reader so a missing fact is never confused with an unimplemented seam.
	ErrEconomicDetailStatementObservationMissing = errors.New("billing: referenced statement observation is not retained")
	// ErrEconomicDetailStatementSourceUnavailable identifies a composed
	// statement observation reader that failed for a non-missing reason. The
	// caller must not treat this as an absent fact.
	ErrEconomicDetailStatementSourceUnavailable = errors.New("billing: statement observation reader unavailable")
)

const (
	// MaxEconomicDetailStatementLines bounds one statement-evidence line set.
	MaxEconomicDetailStatementLines = 256
	// MaxEconomicDetailStatementRefs bounds the referenced observation set that
	// may be resolved for one detail page.
	MaxEconomicDetailStatementRefs = 1024
	// MaxEconomicDetailStatementLineRevisions bounds the persisted
	// statement-line revisions resolved for one referenced observation before Go
	// growth.
	MaxEconomicDetailStatementLineRevisions = 64
	// EconomicDetailStatementReaderUnavailableReason is the stable reason
	// recorded when no statement observation source was composed.
	EconomicDetailStatementReaderUnavailableReason = "statement_observation_reader_unavailable"
)

// EconomicDetailStatementSource is the consumer-owned, exact observation reader
// used to resolve statement-origin observation refs referenced by in-scope S
// valuations. Implementations return
// ErrEconomicDetailStatementObservationMissing for an absent revision and must
// never synthesize an observation from a reference. Store scope is enforced by
// the implementation; the loader re-checks it.
type EconomicDetailStatementSource interface {
	GetStatementObservation(context.Context, metering.ObservationRef) (metering.Observation, error)
}

// EconomicDetailStatementLineSource is the optional consumer-owned extension of
// EconomicDetailStatementSource implemented by sources that can also resolve
// the exact persisted statement-line outcome for one referenced observation. It
// returns the bounded persisted statement-line revisions for the observation's
// statement-line identity and must never synthesize a line from an observation.
type EconomicDetailStatementLineSource interface {
	GetStatementLineRevisions(context.Context, metering.Observation) ([]economics.StatementLineView, error)
}

// EconomicDetailStatementOutcomeSource identifies how one statement line's
// outcome was established. Persisted means the outcome is the exact durable
// statement-line outcome for the referenced observation. Observation means the
// observation is authoritatively linked to the requested scope but no single
// durable statement-line outcome was resolved, so no matched outcome is
// claimed.
type EconomicDetailStatementOutcomeSource string

const (
	// EconomicDetailStatementOutcomePersisted marks an outcome read from the
	// durable statement-line ledger.
	EconomicDetailStatementOutcomePersisted EconomicDetailStatementOutcomeSource = "persisted"
	// EconomicDetailStatementOutcomeObservation marks an observation-only
	// linkage whose persisted statement-line outcome was not authoritatively
	// resolved.
	EconomicDetailStatementOutcomeObservation EconomicDetailStatementOutcomeSource = "observation"
)

// EconomicDetailStatementLine is one matched in-scope statement-line fact with
// its exact referenced verified observation and S valuation linkage. The
// statement identity is the observation's persisted statement-line subject;
// the immutable observation ref/hash and the exact observation revision are
// preserved verbatim. No amount is recomputed or summed here.
type EconomicDetailStatementLine struct {
	StatementID        string                               `json:"statement_id"`
	StatementLineID    string                               `json:"statement_line_id"`
	Revision           uint64                               `json:"revision"`
	ProviderAccountKey string                               `json:"provider_account_key"`
	PeriodID           string                               `json:"period_id"`
	Outcome            economics.StatementLineOutcome       `json:"outcome,omitempty"`
	OutcomeSource      EconomicDetailStatementOutcomeSource `json:"outcome_source,omitempty"`
	UnmatchedReason    string                               `json:"unmatched_reason,omitempty"`
	Subject            metering.SubjectRef                  `json:"subject"`
	ObservationRef     metering.ObservationRef              `json:"observation_ref"`
	Observation        metering.Observation                 `json:"observation"`
	ChargeItemIDs      []string                             `json:"charge_item_ids,omitempty"`
	ValuationIDs       []string                             `json:"valuation_ids,omitempty"`
}

// EconomicDetailStatementEvidence is the source-separated statement evidence of
// one detail scope. ReaderAvailable reports whether an observation source was
// composed; when false, ReaderReason is a stable explanation and every
// referenced observation is enumerated in UnresolvedRefs rather than being
// implied. MissingRefs enumerates referenced observations that are absent or
// are not verified statement-line evidence. UnattributedRefs enumerates
// retained verified observations that carry no authoritative in-scope
// call/B-leg linkage (aggregate/account-period lines) and are therefore never
// attributed to the call. Lines holds the resolved in-scope evidence.
type EconomicDetailStatementEvidence struct {
	ReaderAvailable  bool                          `json:"reader_available"`
	ReaderReason     string                        `json:"reader_reason,omitempty"`
	Lines            []EconomicDetailStatementLine `json:"lines,omitempty"`
	MissingRefs      []metering.ObservationRef     `json:"missing_refs,omitempty"`
	UnattributedRefs []metering.ObservationRef     `json:"unattributed_refs,omitempty"`
	UnresolvedRefs   []metering.ObservationRef     `json:"unresolved_refs,omitempty"`
}

// statementRefRecord is one deduplicated referenced observation with the S
// valuation identities that reference it.
type statementRefRecord struct {
	ref          metering.ObservationRef
	valuationIDs []string
}

// NewStatementEvidenceEconomicDetailReader wraps a scoped detail reader so each
// page also resolves statement-origin S evidence through the supplied exact
// observation source. The wrapper is opt-in: passing a nil base returns nil,
// and passing a nil source is supported and surfaces an explicit unavailable
// reader rather than silently omitting the references.
func NewStatementEvidenceEconomicDetailReader(base EconomicDetailReader, source EconomicDetailStatementSource) EconomicDetailReader {
	if base == nil {
		return nil
	}
	return statementEvidenceEconomicDetailReader{base: base, source: source}
}

type statementEvidenceEconomicDetailReader struct {
	base   EconomicDetailReader
	source EconomicDetailStatementSource
}

func (r statementEvidenceEconomicDetailReader) QueryEconomicDetail(ctx context.Context, query EconomicDetailQuery) (EconomicDetail, error) {
	detail, err := r.base.QueryEconomicDetail(ctx, query)
	if err != nil {
		return EconomicDetail{}, err
	}
	scope := detail.Scope
	if scope.StoreID == "" {
		scope = query
	}
	evidence, err := LoadEconomicDetailStatementEvidence(ctx, scope, detail.Valuations, r.source)
	if err != nil {
		return EconomicDetail{}, err
	}
	// Statement evidence is resolved above the durable reader, so fold it into
	// the same authenticated snapshot boundary. A continuation issued against a
	// different repeated-fact set (including a corrected persisted line) is
	// rejected with the stale-cursor classification instead of changing
	// supposedly frozen full-scope facts.
	composed := extendEconomicDetailSnapshotFingerprint(detail.SnapshotFingerprint, evidence)
	if detail.PreviousSnapshotFingerprint != "" && detail.PreviousSnapshotFingerprint != composed {
		return EconomicDetail{}, fmt.Errorf("%w: snapshot changed; restart pagination", economics.ErrOperatorCursorStale)
	}
	detail.StatementEvidence = evidence
	if detail.NextPosition != nil {
		if encoder, ok := r.base.(EconomicDetailSnapshotContinuationEncoder); ok {
			if encoded := encoder.EncodeEconomicDetailSnapshotContinuation(scope, *detail.NextPosition, detail.SnapshotFingerprint, composed); encoded != "" {
				detail.NextCursor = encoded
			}
		}
	}
	detail.PreviousSnapshotFingerprint = ""
	return detail, nil
}

// LoadEconomicDetailStatementEvidence resolves the statement evidence for one
// detail scope from the S-basis valuation refs and the composed exact
// observation source. It performs no rating and no sums: each retained
// statement line stays an independent contribution.
func LoadEconomicDetailStatementEvidence(ctx context.Context, query EconomicDetailQuery, valuations []economics.Valuation, source EconomicDetailStatementSource) (EconomicDetailStatementEvidence, error) {
	normalized, err := query.Normalize()
	if err != nil {
		return EconomicDetailStatementEvidence{}, err
	}
	if ctx == nil {
		return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: nil context", ErrEconomicDetailInvalid)
	}
	if err := ctx.Err(); err != nil {
		return EconomicDetailStatementEvidence{}, err
	}

	records := collectEconomicDetailStatementRefs(valuations)
	if len(records) > MaxEconomicDetailStatementRefs {
		return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: statement refs=%d max=%d", ErrEconomicDetailBoundExceeded, len(records), MaxEconomicDetailStatementRefs)
	}

	evidence := EconomicDetailStatementEvidence{ReaderAvailable: source != nil}
	if source == nil {
		evidence.ReaderReason = EconomicDetailStatementReaderUnavailableReason
		for _, record := range records {
			evidence.UnresolvedRefs = append(evidence.UnresolvedRefs, record.ref)
		}
		return normalizeEconomicDetailStatementEvidence(&evidence, normalized)
	}

	scopeIndex := buildEconomicDetailStatementScopeIndex(valuations)
	lineSource, _ := source.(EconomicDetailStatementLineSource)
	for _, record := range records {
		if record.ref.StoreID != normalized.StoreID {
			return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: statement ref %q store mismatch", ErrEconomicDetailScopeMismatch, record.ref.ObservationID)
		}
		observation, err := source.GetStatementObservation(ctx, record.ref)
		if err != nil {
			if errors.Is(err, ErrEconomicDetailStatementObservationMissing) {
				evidence.MissingRefs = append(evidence.MissingRefs, record.ref)
				continue
			}
			return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: %v", ErrEconomicDetailStatementSourceUnavailable, err)
		}
		if err := observation.Validate(); err != nil {
			return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: statement observation %q: %v", ErrEconomicDetailInvalid, record.ref.ObservationID, err)
		}
		if observation.Origin != metering.OriginStatement || observation.Authority != metering.AuthorityVerifiedStatement ||
			observation.Subject.Kind != metering.SubjectStatementLine {
			// The S plane references statement evidence; a non-verified or
			// non-statement observation cannot satisfy that claim and must not
			// be presented as statement detail.
			evidence.MissingRefs = append(evidence.MissingRefs, record.ref)
			continue
		}
		if observation.ID != record.ref.ObservationID || observation.Revision != record.ref.Revision {
			return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: statement observation %q revision mismatch", ErrEconomicDetailInvalid, record.ref.ObservationID)
		}
		if !statementObservationRefHashMatches(observation, record.ref) {
			return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: statement observation %q payload hash mismatch", ErrEconomicDetailInvalid, record.ref.ObservationID)
		}
		if err := statementObservationScopeCompatible(observation, normalized); err != nil {
			return EconomicDetailStatementEvidence{}, err
		}
		linked, foreign := statementObservationLinkage(observation, normalized, scopeIndex)
		if foreign {
			return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: statement observation %q call/A-leg linkage", ErrEconomicDetailScopeMismatch, record.ref.ObservationID)
		}
		if !linked {
			// A retained verified observation that is not authoritatively
			// linked to the requested call/B-leg (aggregate/account-period) is
			// never attributed; it is enumerated explicitly instead.
			evidence.UnattributedRefs = append(evidence.UnattributedRefs, record.ref)
			continue
		}
		if len(evidence.Lines) >= MaxEconomicDetailStatementLines {
			return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: statement lines exceed %d", ErrEconomicDetailBoundExceeded, MaxEconomicDetailStatementLines)
		}
		outcome, err := resolveEconomicDetailStatementLineOutcome(ctx, observation, record.ref, lineSource)
		if err != nil {
			return EconomicDetailStatementEvidence{}, err
		}
		evidence.Lines = append(evidence.Lines, newEconomicDetailStatementLine(observation, record.ref, record.valuationIDs, outcome))
	}
	return normalizeEconomicDetailStatementEvidence(&evidence, normalized)
}

func collectEconomicDetailStatementRefs(valuations []economics.Valuation) []statementRefRecord {
	byKey := map[string]*statementRefRecord{}
	for i := range valuations {
		valuation := valuations[i]
		if valuation.Basis != economics.BasisStatementReported {
			continue
		}
		add := func(ref metering.ObservationRef) {
			key := observationRefIdentity(ref)
			record, exists := byKey[key]
			if !exists {
				record = &statementRefRecord{ref: ref}
				byKey[key] = record
			}
			record.valuationIDs = appendUniqueSorted(record.valuationIDs, valuation.ID)
		}
		for _, ref := range valuation.InputObservations {
			add(ref)
		}
		for _, line := range valuation.Lines {
			for _, ref := range line.SourceObservationRefs {
				add(ref)
			}
		}
	}
	records := make([]statementRefRecord, 0, len(byKey))
	for _, record := range byKey {
		records = append(records, *record)
	}
	sort.SliceStable(records, func(i, j int) bool {
		return observationRefLess(records[i].ref, records[j].ref)
	})
	return records
}

func appendUniqueSorted(values []string, value string) []string {
	if value == "" {
		return values
	}
	if slices.Contains(values, value) {
		return values
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

func observationRefIdentity(ref metering.ObservationRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + fmt.Sprint(ref.Revision) + "\x00" + ref.PayloadHash
}

func observationRefLess(left, right metering.ObservationRef) bool {
	if left.StoreID != right.StoreID {
		return left.StoreID < right.StoreID
	}
	if left.ObservationID != right.ObservationID {
		return left.ObservationID < right.ObservationID
	}
	if left.Revision != right.Revision {
		return left.Revision < right.Revision
	}
	return left.PayloadHash < right.PayloadHash
}

// economicDetailStatementScopeIndex names every call and B-leg identity that
// is authoritative for the loaded detail valuations. A statement observation
// is attributed only when its trusted correlation lies inside this index.
type economicDetailStatementScopeIndex struct {
	calls map[string]struct{}
	legs  map[string]struct{}
}

func buildEconomicDetailStatementScopeIndex(valuations []economics.Valuation) economicDetailStatementScopeIndex {
	index := economicDetailStatementScopeIndex{calls: map[string]struct{}{}, legs: map[string]struct{}{}}
	for _, valuation := range valuations {
		subject := valuation.Subject
		if subject.BillingCallID != "" {
			index.calls[subject.BillingCallID] = struct{}{}
		}
		if subject.CallID != "" {
			index.calls[subject.CallID] = struct{}{}
		}
		if subject.BLegID != "" {
			index.legs[subject.BLegID] = struct{}{}
		}
	}
	return index
}

// statementObservationLinkage classifies whether one verified statement
// observation authoritatively belongs to the requested scope. An observation
// with no request lineage is explicitly not linked; an observation whose
// explicit lineage contradicts the scope is foreign and fails closed.
func statementObservationLinkage(observation metering.Observation, query EconomicDetailQuery, index economicDetailStatementScopeIndex) (linked bool, foreign bool) {
	call := observation.Correlation.BillingCallID
	if call == "" {
		call = observation.Correlation.CallID
	}
	aLeg := observation.Correlation.ALegID
	bLeg := observation.Correlation.BLegID
	if call == "" && aLeg == "" && bLeg == "" {
		return false, false
	}
	if query.IsCallScope() {
		if call != "" && call != query.BillingCallID {
			return false, true
		}
		if query.ALegID != "" && aLeg != "" && aLeg != query.ALegID {
			return false, true
		}
		if bLeg != "" {
			if _, ok := index.legs[bLeg]; !ok {
				return false, true
			}
		}
		switch {
		case call == query.BillingCallID && call != "":
			return true, false
		case bLeg != "":
			return true, false
		default:
			// One A-leg/session can contain arbitrarily many later calls, so
			// A-leg ancestry alone cannot identify which call owns a statement
			// charge. Such evidence stays unattributed rather than being
			// invented onto an individual call.
			return false, false
		}
	}
	if aLeg != "" && aLeg != query.ALegID {
		return false, true
	}
	if call != "" {
		if _, ok := index.calls[call]; !ok {
			return false, true
		}
	}
	if bLeg != "" {
		if _, ok := index.legs[bLeg]; !ok {
			return false, true
		}
	}
	switch {
	case aLeg == query.ALegID && aLeg != "":
		return true, false
	case call != "":
		return true, false
	case bLeg != "":
		return true, false
	default:
		return false, false
	}
}

func statementObservationRefHashMatches(observation metering.Observation, ref metering.ObservationRef) bool {
	if ref.PayloadHash == "" {
		return true
	}
	replayHash, err := observation.ReplayFingerprint()
	if err == nil && replayHash == ref.PayloadHash {
		return true
	}
	return observation.Fingerprint() == ref.PayloadHash
}

func statementObservationScopeCompatible(observation metering.Observation, query EconomicDetailQuery) error {
	if observation.Subject.StoreID != query.StoreID || observation.Correlation.StoreID != query.StoreID {
		return fmt.Errorf("%w: statement observation store mismatch", ErrEconomicDetailScopeMismatch)
	}
	if query.TenantID != "" {
		for _, tenant := range []string{observation.Subject.TenantID, observation.Correlation.TenantID} {
			if tenant != "" && tenant != query.TenantID {
				return fmt.Errorf("%w: statement observation tenant mismatch", ErrEconomicDetailScopeMismatch)
			}
		}
	}
	if observation.Subject.AccountID != "" && observation.Subject.AccountID != query.AccountID {
		return fmt.Errorf("%w: statement observation account mismatch", ErrEconomicDetailScopeMismatch)
	}
	return nil
}

// economicDetailStatementLineOutcome is the resolved outcome of one
// observation-linked statement line. isPersisted is true only when the exact
// durable statement-line ledger supplied the outcome.
type economicDetailStatementLineOutcome struct {
	isPersisted     bool
	outcome         economics.StatementLineOutcome
	unmatchedReason string
}

// resolveEconomicDetailStatementLineOutcome reads the bounded persisted
// statement-line revisions for one observation and selects the exact durable
// outcome that authoritatively covers it. It never synthesizes an outcome: a
// source without the persisted-line capability, an absent line, a line matched
// to a different observation or conflicting revisions all leave the line
// explicitly observation-linked without claiming matched.
func resolveEconomicDetailStatementLineOutcome(ctx context.Context, observation metering.Observation, ref metering.ObservationRef, source EconomicDetailStatementLineSource) (economicDetailStatementLineOutcome, error) {
	observationLinked := economicDetailStatementLineOutcome{}
	if source == nil {
		return observationLinked, nil
	}
	lines, err := source.GetStatementLineRevisions(ctx, observation)
	if err != nil {
		return economicDetailStatementLineOutcome{}, fmt.Errorf("%w: persisted statement line read: %v", ErrEconomicDetailStatementSourceUnavailable, err)
	}
	if len(lines) > MaxEconomicDetailStatementLineRevisions {
		return economicDetailStatementLineOutcome{}, fmt.Errorf("%w: statement line revisions=%d max=%d", ErrEconomicDetailBoundExceeded, len(lines), MaxEconomicDetailStatementLineRevisions)
	}
	var selected *economics.StatementLineView
	for i := range lines {
		view := lines[i]
		if err := view.Validate(); err != nil {
			return economicDetailStatementLineOutcome{}, fmt.Errorf("%w: persisted statement line %q: %v", ErrEconomicDetailInvalid, view.LineID, err)
		}
		if view.LineID != observation.Subject.StatementLineID {
			continue
		}
		if view.StatementID != observation.Subject.StatementID ||
			view.ProviderAccountKey != observation.Subject.ProviderAccountKey ||
			view.PeriodID != observation.Subject.PeriodID {
			return economicDetailStatementLineOutcome{}, fmt.Errorf("%w: persisted statement line %q identity mismatch", ErrEconomicDetailScopeMismatch, view.LineID)
		}
		switch {
		case selected == nil, view.Revision > selected.Revision:
			candidate := view
			selected = &candidate
		case view.Revision == selected.Revision &&
			(view.Outcome != selected.Outcome || !view.Observation.Equal(selected.Observation)):
			// Conflicting content under one immutable revision: no single
			// durable outcome is authoritative.
			return observationLinked, nil
		}
	}
	if selected == nil {
		return observationLinked, nil
	}
	switch selected.Outcome {
	case economics.StatementLineMatched:
		if !selected.Observation.Equal(ref) {
			// The durable line for this identity is matched to a different
			// observation, so the referenced observation is not the persisted
			// matched line.
			return observationLinked, nil
		}
		return economicDetailStatementLineOutcome{isPersisted: true, outcome: economics.StatementLineMatched}, nil
	case economics.StatementLineUnmatched:
		return economicDetailStatementLineOutcome{
			isPersisted: true, outcome: economics.StatementLineUnmatched, unmatchedReason: selected.UnmatchedReason,
		}, nil
	default:
		return economicDetailStatementLineOutcome{}, fmt.Errorf("%w: persisted statement line %q unknown outcome", ErrEconomicDetailInvalid, selected.LineID)
	}
}

func newEconomicDetailStatementLine(observation metering.Observation, ref metering.ObservationRef, valuationIDs []string, outcome economicDetailStatementLineOutcome) EconomicDetailStatementLine {
	line := EconomicDetailStatementLine{
		StatementID:        observation.Subject.StatementID,
		StatementLineID:    observation.Subject.StatementLineID,
		Revision:           ref.Revision,
		ProviderAccountKey: observation.Subject.ProviderAccountKey,
		PeriodID:           observation.Subject.PeriodID,
		OutcomeSource:      EconomicDetailStatementOutcomeObservation,
		Subject:            observation.Subject.Clone(),
		ObservationRef:     ref,
		Observation:        observation.Clone(),
		ValuationIDs:       append([]string(nil), valuationIDs...),
	}
	if outcome.isPersisted {
		line.Outcome = outcome.outcome
		line.OutcomeSource = EconomicDetailStatementOutcomePersisted
		line.UnmatchedReason = outcome.unmatchedReason
	}
	for _, charge := range observation.Charges {
		line.ChargeItemIDs = append(line.ChargeItemIDs, charge.ChargeItemID)
	}
	sort.Strings(line.ChargeItemIDs)
	if line.ChargeItemIDs == nil {
		line.ChargeItemIDs = []string{}
	}
	if line.ValuationIDs == nil {
		line.ValuationIDs = []string{}
	}
	return line
}

// normalizeEconomicDetailStatementEvidence validates and deep-copies statement
// evidence against the requested scope. Foreign store/account/tenant evidence
// fails closed; ordering is deterministic and every bound fails closed.
func normalizeEconomicDetailStatementEvidence(in *EconomicDetailStatementEvidence, query EconomicDetailQuery) (EconomicDetailStatementEvidence, error) {
	if in == nil {
		return EconomicDetailStatementEvidence{}, nil
	}
	if len(in.Lines) > MaxEconomicDetailStatementLines {
		return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: statement lines=%d max=%d", ErrEconomicDetailBoundExceeded, len(in.Lines), MaxEconomicDetailStatementLines)
	}
	out := EconomicDetailStatementEvidence{
		ReaderAvailable: in.ReaderAvailable,
		ReaderReason:    in.ReaderReason,
	}
	var err error
	if out.MissingRefs, err = normalizeStatementObservationRefs(in.MissingRefs, query, "missing"); err != nil {
		return EconomicDetailStatementEvidence{}, err
	}
	if out.UnattributedRefs, err = normalizeStatementObservationRefs(in.UnattributedRefs, query, "unattributed"); err != nil {
		return EconomicDetailStatementEvidence{}, err
	}
	if out.UnresolvedRefs, err = normalizeStatementObservationRefs(in.UnresolvedRefs, query, "unresolved"); err != nil {
		return EconomicDetailStatementEvidence{}, err
	}
	seen := make(map[string]struct{}, len(in.Lines))
	out.Lines = make([]EconomicDetailStatementLine, 0, len(in.Lines))
	for i := range in.Lines {
		line := in.Lines[i]
		if err := validateEconomicDetailStatementLine(line, query); err != nil {
			return EconomicDetailStatementEvidence{}, err
		}
		key := observationRefIdentity(line.ObservationRef) + "\x00" + line.Subject.StoreID + "\x00" + line.Subject.StatementLineID
		if _, exists := seen[key]; exists {
			return EconomicDetailStatementEvidence{}, fmt.Errorf("%w: duplicate statement line %q", ErrEconomicDetailInvalid, line.StatementLineID)
		}
		seen[key] = struct{}{}
		out.Lines = append(out.Lines, cloneEconomicDetailStatementLine(line))
	}
	sort.SliceStable(out.Lines, func(i, j int) bool {
		left, right := out.Lines[i], out.Lines[j]
		if left.StatementID != right.StatementID {
			return left.StatementID < right.StatementID
		}
		if left.StatementLineID != right.StatementLineID {
			return left.StatementLineID < right.StatementLineID
		}
		if left.Revision != right.Revision {
			return left.Revision < right.Revision
		}
		return observationRefLess(left.ObservationRef, right.ObservationRef)
	})
	return out, nil
}

func normalizeStatementObservationRefs(refs []metering.ObservationRef, query EconomicDetailQuery, label string) ([]metering.ObservationRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]metering.ObservationRef, 0, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("%w: statement %s ref: %v", ErrEconomicDetailInvalid, label, err)
		}
		if ref.StoreID != query.StoreID {
			return nil, fmt.Errorf("%w: statement %s ref store mismatch", ErrEconomicDetailScopeMismatch, label)
		}
		out = append(out, ref)
	}
	sort.SliceStable(out, func(i, j int) bool { return observationRefLess(out[i], out[j]) })
	deduped := out[:0]
	for _, ref := range out {
		if len(deduped) > 0 && deduped[len(deduped)-1].Equal(ref) {
			continue
		}
		deduped = append(deduped, ref)
	}
	if len(deduped) > MaxEconomicDetailMissingRefs {
		return nil, fmt.Errorf("%w: statement %s refs=%d max=%d", ErrEconomicDetailBoundExceeded, label, len(deduped), MaxEconomicDetailMissingRefs)
	}
	return deduped, nil
}

func validateEconomicDetailStatementLine(line EconomicDetailStatementLine, query EconomicDetailQuery) error {
	if err := line.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: statement line subject: %v", ErrEconomicDetailInvalid, err)
	}
	if line.Subject.Kind != metering.SubjectStatementLine {
		return fmt.Errorf("%w: statement line subject must be statement_line", ErrEconomicDetailInvalid)
	}
	if line.Subject.StoreID != query.StoreID {
		return fmt.Errorf("%w: statement line subject store mismatch", ErrEconomicDetailScopeMismatch)
	}
	if query.TenantID != "" && line.Subject.TenantID != "" && line.Subject.TenantID != query.TenantID {
		return fmt.Errorf("%w: statement line subject tenant mismatch", ErrEconomicDetailScopeMismatch)
	}
	if line.Subject.AccountID != "" && line.Subject.AccountID != query.AccountID {
		return fmt.Errorf("%w: statement line subject account mismatch", ErrEconomicDetailScopeMismatch)
	}
	if err := line.ObservationRef.Validate(); err != nil {
		return fmt.Errorf("%w: statement line observation ref: %v", ErrEconomicDetailInvalid, err)
	}
	if line.ObservationRef.StoreID != query.StoreID {
		return fmt.Errorf("%w: statement line observation ref store mismatch", ErrEconomicDetailScopeMismatch)
	}
	if err := line.Observation.Validate(); err != nil {
		return fmt.Errorf("%w: statement line observation: %v", ErrEconomicDetailInvalid, err)
	}
	if line.Observation.Subject.Kind != metering.SubjectStatementLine {
		return fmt.Errorf("%w: statement line observation subject must be statement_line", ErrEconomicDetailInvalid)
	}
	if line.Observation.Origin != metering.OriginStatement || line.Observation.Authority != metering.AuthorityVerifiedStatement {
		return fmt.Errorf("%w: statement line observation must be verified statement evidence", ErrEconomicDetailInvalid)
	}
	if line.Observation.Subject.StoreID != query.StoreID {
		return fmt.Errorf("%w: statement line observation store mismatch", ErrEconomicDetailScopeMismatch)
	}
	if line.Observation.ID != line.ObservationRef.ObservationID || line.Observation.Revision != line.ObservationRef.Revision {
		return fmt.Errorf("%w: statement line observation identity mismatch", ErrEconomicDetailInvalid)
	}
	if !statementObservationRefHashMatches(line.Observation, line.ObservationRef) {
		return fmt.Errorf("%w: statement line observation payload hash mismatch", ErrEconomicDetailInvalid)
	}
	if line.Outcome != "" && !line.Outcome.IsKnown() {
		return fmt.Errorf("%w: unknown statement line outcome %q", ErrEconomicDetailInvalid, line.Outcome)
	}
	switch line.OutcomeSource {
	case EconomicDetailStatementOutcomePersisted:
		if !line.Outcome.IsKnown() {
			return fmt.Errorf("%w: persisted statement line outcome required", ErrEconomicDetailInvalid)
		}
		if line.Outcome == economics.StatementLineUnmatched {
			if line.UnmatchedReason == "" {
				return fmt.Errorf("%w: persisted unmatched statement line reason required", ErrEconomicDetailInvalid)
			}
		} else if line.UnmatchedReason != "" {
			return fmt.Errorf("%w: persisted matched statement line cannot carry unmatched reason", ErrEconomicDetailInvalid)
		}
	case EconomicDetailStatementOutcomeObservation:
		if line.Outcome != "" || line.UnmatchedReason != "" {
			return fmt.Errorf("%w: observation-linked statement line cannot claim a persisted outcome", ErrEconomicDetailInvalid)
		}
	case "":
		if line.Outcome != "" || line.UnmatchedReason != "" {
			return fmt.Errorf("%w: statement line outcome requires an explicit source", ErrEconomicDetailInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown statement line outcome source %q", ErrEconomicDetailInvalid, line.OutcomeSource)
	}
	return nil
}

func cloneEconomicDetailStatementLine(line EconomicDetailStatementLine) EconomicDetailStatementLine {
	out := line
	out.Subject = line.Subject.Clone()
	out.Observation = line.Observation.Clone()
	out.ChargeItemIDs = append([]string(nil), line.ChargeItemIDs...)
	out.ValuationIDs = append([]string(nil), line.ValuationIDs...)
	return out
}
