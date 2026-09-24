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

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	maxRationalDigits = 128
	defaultRounding   = economics.RoundingHalfAwayFromZero
)

// ErrSchemaOverlapConflict reports a frozen component-schema inclusion or
// partition relationship whose parent aggregate and declared child are both
// priced within the same source/B-leg/work scope and economic direction. The
// rater refuses to emit those overlapping payable lines rather than
// double-charge the same underlying work, while unrelated scopes, unrelated
// components, and independent fixed fees stay payable. Cross-direction,
// transform, and separate-scope edges are not overlaps and remain additive.
var ErrSchemaOverlapConflict = errors.New("billing: frozen schema component overlap is not payable")

// ReferenceRater is a deterministic post-usage evaluator over one immutable
// tariff snapshot. It has no provider, SQL or stream lifecycle dependency.
type ReferenceRater struct {
	snapshot economics.TariffSnapshot
}

// ComponentRater is the descriptive name used by billing composition.
type ComponentRater = ReferenceRater

// NewReferenceRater validates and freezes the supplied tariff. Subsequent
// caller mutations cannot change replay behavior.
func NewReferenceRater(snapshot economics.TariffSnapshot) (*ReferenceRater, error) {
	canonical, err := snapshot.Canonical()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTariffInvalid, err)
	}
	if err := validateRatingRuleSet(canonical.Rules); err != nil {
		return nil, err
	}
	return &ReferenceRater{snapshot: canonical.Clone()}, nil
}

// NewComponentRater is an explicit alias for NewReferenceRater.
func NewComponentRater(snapshot economics.TariffSnapshot) (*ComponentRater, error) {
	return NewReferenceRater(snapshot)
}

// Snapshot returns a caller-owned immutable material copy for durable binding
// and replay diagnostics.
func (r *ReferenceRater) Snapshot() economics.TariffSnapshot {
	if r == nil {
		return economics.TariffSnapshot{}
	}
	return r.snapshot.Clone()
}

// Rate evaluates one explicit basis after post-usage evidence is durable.
// Provider-reported P uses provider charges directly; E and Q use only their
// respective source quantities and never fall back to P.
func (r *ReferenceRater) Rate(ctx context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	if r == nil {
		return economics.Valuation{}, fmt.Errorf("%w: nil reference rater", ErrTariffInvalid)
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return economics.Valuation{}, ctx.Err()
		default:
		}
	}
	if err := input.Validate(); err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: %v", ErrRatingInvalid, err)
	}
	if err := r.validateSnapshotBinding(input); err != nil {
		return economics.Valuation{}, err
	}
	switch input.Basis {
	case economics.BasisLocalExpected:
		input = inputForPlane(input, isLocalQuantityObservation)
	case economics.BasisCustomerPolicy:
		// Customer retail rating consumes the frozen B-leg quantity envelope.
		// Provider-origin observations remain valid here because the selector
		// already established that they are normalized backend-attempt evidence;
		// customer-boundary observations are rejected before this seam.
		// Retain B-leg envelopes even when a quantity is unavailable so the
		// resulting valuation carries the selected reference and a typed
		// incomplete state rather than turning missing evidence into an empty
		// input set.
		input = inputForPlane(input, isRetailBLegObservation)
	case economics.BasisProviderQuantityLocal:
		input = inputForPlane(input, isProviderQuantityObservation)
	case economics.BasisProviderReported:
		input = inputForPlane(input, isProviderRevisionObservation)
	}
	var err error
	input, err = canonicalizeRatingInput(input)
	if err != nil {
		return economics.Valuation{}, err
	}
	refs, err := inputObservationRefs(input)
	if err != nil {
		return economics.Valuation{}, err
	}
	input.InputSetHash, err = verifyInputSetHash(input.Basis, input.InputSetHash, refs)
	if err != nil {
		return economics.Valuation{}, err
	}
	qualifiers, err := effectiveQualifiers(r.snapshot.EffectiveQualifiers, input.EffectiveQualifiers)
	if err != nil {
		return economics.Valuation{}, err
	}
	valuation := newValuation(input, r.snapshot, refs, qualifiers)
	if input.Basis == economics.BasisProviderReported {
		return r.rateReported(input, valuation)
	}
	if input.Basis != economics.BasisLocalExpected && input.Basis != economics.BasisProviderQuantityLocal && input.Basis != economics.BasisCustomerPolicy {
		return valuation, fmt.Errorf("%w: basis %q is not supported by reference component rater", ErrRateUnsupported, input.Basis)
	}
	return r.rateMeasures(input, valuation)
}

// RateWithTariff is a one-shot convenience for post-turn workers that resolve
// a snapshot from a catalog immediately before rating.
func RateWithTariff(ctx context.Context, input economics.PostUsageRatingInput, snapshot economics.TariffSnapshot) (economics.Valuation, error) {
	rater, err := NewReferenceRater(snapshot)
	if err != nil {
		return economics.Valuation{}, err
	}
	return rater.Rate(ctx, input)
}

// RateCustomerPolicyObservation rates the incremental B-leg inference plane
// from a frozen customer tariff. Call/submission fixed fees are commercial
// lines owned by terminal call settlement, so they are intentionally omitted
// from this B-leg valuation; evaluating them once per observation head would
// multiply a call-scoped fee across retries or selected legs.
func RateCustomerPolicyObservation(ctx context.Context, input economics.PostUsageRatingInput, snapshot economics.TariffSnapshot) (economics.Valuation, error) {
	if input.Basis != economics.BasisCustomerPolicy {
		return economics.Valuation{}, fmt.Errorf("%w: customer-policy helper requires customer policy basis", ErrRateUnsupported)
	}
	// Same frozen contractual basis as call settlement: competing
	// local/provider measures of the same work select one channel for R.
	// E/Q/P, reconciliation and query keep both; this incremental plane bills
	// one. The original envelope is validated before selection so a supplied
	// reference or input-set identity can never be silently replaced; the
	// valuation identity is then narrowed to the selected original
	// references deterministically.
	if len(input.Observations) != 0 {
		if err := validateCustomerPolicyOriginalEnvelope(input); err != nil {
			return economics.Valuation{}, err
		}
		selection, err := selectRetailInferenceSelection(input.Observations)
		if err != nil {
			return economics.Valuation{}, err
		}
		valuation, rateErr := rateRetailValuation(ctx, snapshot, input, isRetailQuantityObservation, false, "", selection.kept)
		if valuation.ID == "" {
			return valuation, rateErr
		}
		scoped, narrowErr := selection.narrowValuationInputs(valuation, input.Observations)
		if narrowErr != nil {
			return scoped, errors.Join(narrowErr, rateErr)
		}
		return scoped, rateErr
	}
	return rateRetailValuation(ctx, snapshot, input, isRetailQuantityObservation, false, "", nil)
}

// validateCustomerPolicyOriginalEnvelope verifies the caller's original
// observation envelope before any source/component selection is derived. A
// supplied observation reference set must describe exactly the supplied
// observations, and a supplied nonempty input-set hash must equal the
// canonical identity of the full original set. Integrity failures return the
// typed input-set mismatch instead of being cleared by selection filtering.
func validateCustomerPolicyOriginalEnvelope(input economics.PostUsageRatingInput) error {
	if err := input.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrRatingInvalid, err)
	}
	derived := make([]metering.ObservationRef, 0, len(input.Observations))
	for i, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return fmt.Errorf("%w: observation %d reference: %v", ErrRatingEvidenceMissing, i, err)
		}
		derived = append(derived, ref)
	}
	derived, err := canonicalizeObservationRefs(derived)
	if err != nil {
		return fmt.Errorf("%w: original observation references: %v", ErrRatingInvalid, err)
	}
	if len(input.ObservationRefs) != 0 {
		supplied, err := canonicalizeObservationRefs(input.ObservationRefs)
		if err != nil {
			return fmt.Errorf("%w: supplied observation references: %v", ErrRatingInvalid, err)
		}
		if len(supplied) != len(derived) {
			return fmt.Errorf("%w: supplied observation reference count %d differs from observation count %d", ErrInputSetHashMismatch, len(supplied), len(derived))
		}
		for i := range supplied {
			if !supplied[i].Equal(derived[i]) {
				return fmt.Errorf("%w: supplied observation reference %+v does not match original observation", ErrInputSetHashMismatch, supplied[i])
			}
		}
	}
	if input.InputSetHash != "" {
		expected, err := economics.CanonicalInputSetHash(input.Basis, derived)
		if err != nil {
			return fmt.Errorf("%w: original input identity: %v", ErrRatingInvalid, err)
		}
		if input.InputSetHash != expected {
			return fmt.Errorf("%w: basis=%q supplied=%q expected=%q", ErrInputSetHashMismatch, input.Basis, input.InputSetHash, expected)
		}
	}
	return nil
}

// RateProviderReported preserves provider monetary claims without requiring a
// local tariff catalog. P is an evidence plane, not a locally priced estimate;
// a missing or refreshing customer/provider tariff must not block its durable
// representation.
func RateProviderReported(ctx context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return economics.Valuation{}, ctx.Err()
		default:
		}
	}
	if input.Basis != economics.BasisProviderReported {
		return economics.Valuation{}, fmt.Errorf("%w: provider-reported helper requires P basis", ErrRateUnsupported)
	}
	if err := input.Validate(); err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: %v", ErrRatingInvalid, err)
	}
	input = inputForPlane(input, isProviderRevisionObservation)
	var err error
	input, err = canonicalizeRatingInput(input)
	if err != nil {
		return economics.Valuation{}, err
	}
	refs, err := inputObservationRefs(input)
	if err != nil {
		return economics.Valuation{}, err
	}
	input.InputSetHash, err = verifyInputSetHash(input.Basis, input.InputSetHash, refs)
	if err != nil {
		return economics.Valuation{}, err
	}
	qualifiers, err := effectiveQualifiers(nil, input.EffectiveQualifiers)
	if err != nil {
		return economics.Valuation{}, err
	}
	valuation := newValuation(input, economics.TariffSnapshot{}, refs, qualifiers)
	return (&ReferenceRater{}).rateReported(input, valuation)
}

func (r *ReferenceRater) validateSnapshotBinding(input economics.PostUsageRatingInput) error {
	derived := input.Basis == economics.BasisLocalExpected || input.Basis == economics.BasisProviderQuantityLocal || input.Basis == economics.BasisCustomerPolicy
	if !derived {
		return nil
	}
	// E and Q explicitly select the tariff used for local arithmetic. A
	// customer-policy valuation may use this same deterministic evaluator, but
	// its commercial policy is the selected authority and a tariff reference is
	// optional at the public input boundary. If a caller supplies one, it still
	// must identify the accepted snapshot rather than silently selecting a
	// second rule set.
	tariffRequired := input.Basis == economics.BasisLocalExpected || input.Basis == economics.BasisProviderQuantityLocal
	if tariffRequired && (input.Tariff.ID != r.snapshot.Ref.ID || input.Tariff.Version != r.snapshot.Ref.Version) {
		return fmt.Errorf("%w: tariff %s@%s does not match accepted snapshot %s@%s", ErrRatingSnapshotMismatch, input.Tariff.ID, input.Tariff.Version, r.snapshot.Ref.ID, r.snapshot.Ref.Version)
	}
	if !tariffRequired && input.Tariff.ID == "" && input.Tariff.Version == "" {
		return nil
	}
	if input.Tariff.ID != r.snapshot.Ref.ID || input.Tariff.Version != r.snapshot.Ref.Version {
		return fmt.Errorf("%w: supplied tariff %s@%s does not match accepted snapshot %s@%s", ErrRatingSnapshotMismatch, input.Tariff.ID, input.Tariff.Version, r.snapshot.Ref.ID, r.snapshot.Ref.Version)
	}
	if r.snapshot.Content.ContentHash != "" && (input.TariffContent == nil || input.TariffContent.ContentHash != r.snapshot.Content.ContentHash) {
		return fmt.Errorf("%w: tariff content hash does not match accepted snapshot", ErrRatingSnapshotMismatch)
	}
	return nil
}

func newValuation(input economics.PostUsageRatingInput, snapshot economics.TariffSnapshot, refs []metering.ObservationRef, qualifiers []metering.Dimension) economics.Valuation {
	createdAt := input.AsOf
	if createdAt.IsZero() {
		for _, observation := range input.Observations {
			if observation.ObservedAt.After(createdAt) {
				createdAt = observation.ObservedAt
			}
		}
	}
	if createdAt.IsZero() {
		createdAt = input.AsOf
	}
	inputIdentity := input.InputSetHash
	if inputIdentity == "" {
		// Direct P evidence may arrive without a derived input-set hash. Keep
		// its durable identity unique and deterministic from the exact source
		// references rather than collapsing every such claim into one ID.
		encoded, _ := json.Marshal(refs)
		sum := sha256.Sum256(encoded)
		inputIdentity = hex.EncodeToString(sum[:])
	}
	valuation := economics.Valuation{
		ID:                   "",
		Version:              economics.ValuationVersionV2,
		Perspective:          input.Perspective,
		Basis:                input.Basis,
		Subject:              input.Subject.Clone(),
		Scope:                input.Scope,
		Payer:                input.Payer,
		InputObservations:    append([]metering.ObservationRef(nil), refs...),
		InputSetHash:         input.InputSetHash,
		Rater:                input.Rater,
		RaterContent:         cloneSnapshotContent(input.RaterContent),
		Tariff:               input.Tariff,
		TariffContent:        cloneSnapshotContent(input.TariffContent),
		Policy:               input.Policy,
		PolicyContent:        cloneSnapshotContent(input.PolicyContent),
		QualifierSnapshot:    qualifierHash(input.QualifierSnapshotRef),
		QualifierSnapshotRef: cloneSnapshotContent(input.QualifierSnapshotRef),
		EffectiveQualifiers:  append([]metering.Dimension(nil), qualifiers...),
		Lines:                nil,
		Totals:               nil,
		Completeness:         economics.CompletenessComplete,
		CreatedAt:            createdAt,
	}
	switch input.Basis {
	case economics.BasisLocalExpected, economics.BasisProviderQuantityLocal, economics.BasisCustomerPolicy:
		valuation.Tariff = snapshot.Ref
		valuation.TariffContent = cloneSnapshotContent(&snapshot.Content)
	case economics.BasisProviderReported:
		// P is a provider claim, not a local tariff evaluation. Do not carry
		// incidental local rater/tariff labels from a shared input into the
		// independent provider valuation.
		valuation.Rater = economics.RatingSnapshotRef{}
		valuation.RaterContent = nil
		valuation.Tariff = economics.RatingSnapshotRef{}
		valuation.TariffContent = nil
	}
	valuation.ID = valuationIdentity(valuation, inputIdentity)
	return valuation
}

// valuationIdentity binds an input set to the immutable economic context that
// interpreted it. The public economics context preimage is shared with the
// durable store; source references remain part of the in-memory valuation ID
// so equal labels cannot collapse different evidence batches.
func valuationIdentity(valuation economics.Valuation, inputIdentity string) string {
	preimage := struct {
		Input        string                    `json:"input"`
		Observations []metering.ObservationRef `json:"observations"`
		ContextHash  string                    `json:"context_hash"`
	}{
		Input:        inputIdentity,
		Observations: append([]metering.ObservationRef(nil), valuation.InputObservations...),
		ContextHash:  valuation.ContextHash(),
	}
	encoded, _ := json.Marshal(preimage)
	sum := sha256.Sum256(encoded)
	return "valuation:" + string(valuation.Basis) + ":" + inputIdentity + ":" + hex.EncodeToString(sum[:])
}

func snapshotContentIdentity(ref *economics.SnapshotContentRef, encode func(economics.SnapshotContentRef) string) string {
	if ref == nil {
		return ""
	}
	return encode(*ref)
}

func cloneSnapshotContent(in *economics.SnapshotContentRef) *economics.SnapshotContentRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func qualifierHash(ref *economics.SnapshotContentRef) string {
	if ref == nil {
		return ""
	}
	return ref.ContentHash
}

type aggregateMeasure struct {
	key       metering.ComponentKey
	scopeKey  string
	rat       *big.Rat
	refs      []metering.ObservationRef
	issueRefs []metering.ObservationRef
	complete  bool
}

func (r *ReferenceRater) rateMeasures(input economics.PostUsageRatingInput, valuation economics.Valuation) (economics.Valuation, error) {
	return r.rateMeasuresWithPredicate(input, valuation, true, nil, nil)
}

func (r *ReferenceRater) rateMeasuresWithPredicate(input economics.PostUsageRatingInput, valuation economics.Valuation, includeFixed bool, predicate func(metering.Observation) bool, mask retailComponentMask) (economics.Valuation, error) {
	return r.rateMeasuresWithPredicateAndFixedScope(input, valuation, includeFixed, predicate, "", mask)
}

// rateMeasuresForFixedScope is used by customer retail composition when a
// tariff carries more than one fixed-fee scope. It evaluates only the rules
// declared for this scope, so a submission rule cannot make an otherwise valid
// call valuation partial merely because it was intentionally deferred to the
// submission pass.
func (r *ReferenceRater) rateMeasuresForFixedScope(input economics.PostUsageRatingInput, valuation economics.Valuation, scope string) (economics.Valuation, error) {
	return r.rateMeasuresWithPredicateAndFixedScope(input, valuation, true, nil, scope, nil)
}

func (r *ReferenceRater) rateMeasuresWithPredicateAndFixedScope(input economics.PostUsageRatingInput, valuation economics.Valuation, includeFixed bool, predicate func(metering.Observation) bool, fixedScope string, mask retailComponentMask) (economics.Valuation, error) {
	selected := func(observation metering.Observation) bool {
		if predicate != nil {
			return predicate(observation)
		}
		switch input.Basis {
		case economics.BasisProviderQuantityLocal:
			return observation.Origin == metering.OriginProvider
		case economics.BasisCustomerPolicy:
			return isRetailQuantityObservation(observation)
		default:
			return observation.Origin == metering.OriginLocal
		}
	}
	// Qualifiers are needed before reduction because a frozen inclusion edge
	// only excuses an included child when the parent's rule actually resolves
	// under the effective qualifiers.
	qualifiers, qualifierErr := effectiveQualifiers(r.snapshot.EffectiveQualifiers, input.EffectiveQualifiers)
	var exclusions map[string]map[string]struct{}
	if qualifierErr == nil {
		exclusions = r.includedChildExclusions(input.Observations, selected, mask, qualifiers)
	}
	aggregates, unavailable, err := aggregateMeasures(input.Observations, input.Subject.StoreID, selected, mask, exclusionPredicate(exclusions))
	// A child-only tariff leaves the aggregate parent unpriced. When the frozen
	// schema declares that parent's complete child partition and every declared
	// child is present and complete in the same scope, the parent's missing rule
	// is not an independent missing charge: the children are the authoritative
	// billers for that share. The evidence and observation references are never
	// removed; only the spurious rate-missing diagnostic is excused.
	completePartitionParents := r.completeChildPartitionCoverage(aggregates, qualifiers)
	if err != nil {
		valuation.Completeness = economics.CompletenessPartial
		// A reduction may contain both independently complete and incomplete
		// partitions. Keep projecting every partition so one unavailable
		// B-leg/component does not erase a sibling's payable line.
		if aggregates == nil {
			return valuation, err
		}
	}
	if qualifierErr != nil {
		valuation.Completeness = economics.CompletenessConflict
		return valuation, qualifierErr
	}
	// A covered aggregate summary is removed from the economic context used for
	// sibling whole-context tier/threshold selection, not only from line
	// emission. Its quantity is already represented by the complete child
	// partition that proved coverage, so counting the parent again would inflate
	// the context and select an overcharged tier. The exclusion is derived from
	// the frozen selected basis itself (the reduced, mask-filtered aggregates)
	// minus exactly the proven, genuinely unpriced covered parents; it is never a
	// filter over arbitrary raw source evidence, and a priced parent keeps
	// contributing its own quantity to its tier.
	contextAggregates := aggregates
	if len(completePartitionParents) != 0 {
		contextAggregates = make([]aggregateMeasure, 0, len(aggregates))
		for _, item := range aggregates {
			if r.suppressedCompletePartitionParent(completePartitionParents, item, qualifiers) {
				continue
			}
			contextAggregates = append(contextAggregates, item)
		}
	}
	// Rate each aggregate exactly once. The frozen-schema overlap check must
	// judge a component by its effective payable monetary contribution after
	// rule evaluation, not by its raw quantity or by the mere resolvability of a
	// rule: a zero-quantity minimum charge is priced, while a positive quantity
	// at an explicit zero rate is free. Reusing the rated line keeps the priced
	// signal and the emitted line from diverging.
	type ratedAggregate struct {
		item    aggregateMeasure
		line    economics.LineItem
		lineErr error
	}
	rated := make([]ratedAggregate, 0, len(aggregates))
	ratedMeasureCount := 0
	for _, item := range aggregates {
		if isInformationalMeasure(item.key) {
			if _, probeErr := r.resolveRule(item.key, qualifiers); errors.Is(probeErr, ErrRateMissing) {
				// Totals/reasoning observations are retained as evidence but do
				// not become billable lines unless the tariff explicitly prices
				// that component.
				continue
			}
		}
		line, lineErr := r.rateMeasureLine(item, contextAggregates, qualifiers, input, valuation.InputObservations)
		ratedMeasureCount++
		rated = append(rated, ratedAggregate{item: item, line: line, lineErr: lineErr})
	}
	// Two distinct sets drive the frozen-schema overlap check. rateableByScope
	// carries every component whose rule resolved under the effective qualifiers,
	// including an observed zero quantity whose effective charge is zero: a
	// zero-valued child is still a genuine member of a complete partition.
	// payableByScope carries only components with a positive effective amount,
	// which is the signal for an independently payable subset. Keeping them
	// separate lets a zero-valued partition member complete the partition
	// without turning a zero-valued subset into a conflict.
	rateableByScope := make(map[string]map[string]struct{}, len(rated))
	payableByScope := make(map[string]map[string]struct{}, len(rated))
	for _, entry := range rated {
		if entry.lineErr != nil {
			continue
		}
		key, keyErr := entry.item.key.Normalize()
		if keyErr != nil {
			continue
		}
		canonical := key.CanonicalKey()
		rateable := rateableByScope[entry.item.scopeKey]
		if rateable == nil {
			rateable = make(map[string]struct{})
			rateableByScope[entry.item.scopeKey] = rateable
		}
		rateable[canonical] = struct{}{}
		amount, ok := lineAmountRat(entry.line)
		if !ok || amount.Sign() <= 0 {
			continue
		}
		payable := payableByScope[entry.item.scopeKey]
		if payable == nil {
			payable = make(map[string]struct{})
			payableByScope[entry.item.scopeKey] = payable
		}
		payable[canonical] = struct{}{}
	}
	// A frozen schema may declare that the parent aggregate already includes a
	// priced child. Emitting both quantity lines would additively double-charge
	// the same source/B-leg/work evidence, so only those specific overlapping
	// quantity lines are suppressed rather than choosing a winner. Unrelated
	// scopes and unrelated components stay payable; independent fixed fees are
	// owned by their own trusted scope and remain payable.
	overlapConflicts, overlapErr := r.overlappingSchemaInclusionConflicts(payableByScope, rateableByScope)
	if overlapErr != nil {
		valuation.Completeness = economics.CompletenessConflict
	}
	for _, entry := range rated {
		if schemaConflictSuppressed(overlapConflicts, entry.item) {
			continue
		}
		if errors.Is(entry.lineErr, ErrRateMissing) && parentCoveredByCompletePartition(completePartitionParents, entry.item) {
			// The aggregate parent is fully covered by a proven complete
			// disjoint child partition, so its own absent rule is not a missing
			// charge. The observation (including the aggregate measure) stays in
			// the immutable input set for audit; only the diagnostic line is
			// withheld.
			continue
		}
		valuation.Lines = append(valuation.Lines, entry.line)
		for _, ref := range entry.item.issueRefs {
			valuation.MissingObservations = appendUniqueObservationRef(valuation.MissingObservations, ref)
		}
		if entry.lineErr != nil {
			valuation.Completeness = economics.CompletenessPartial
			if unavailable == nil {
				unavailable = entry.lineErr
			}
		}
	}
	var fixedLines []economics.LineItem
	var fixedErr error
	if includeFixed {
		if fixedScope == "" {
			fixedLines, fixedErr = r.rateFixedLines(input, qualifiers, valuation.InputObservations)
		} else {
			fixedLines, fixedErr = r.rateFixedLinesForScope(input, qualifiers, valuation.InputObservations, fixedScope)
		}
	}
	valuation.Lines = append(valuation.Lines, fixedLines...)
	if fixedErr != nil {
		// A fixed fee can remain independently payable, but a failed sibling
		// qualifier/scope/rate is still material evidence and therefore makes
		// the enclosing valuation partial. Never let the presence of one valid
		// line mask an unavailable required input.
		if valuation.Completeness == economics.CompletenessComplete {
			valuation.Completeness = economics.CompletenessPartial
		}
		if unavailable == nil {
			unavailable = fixedErr
		}
	}
	if overlapErr != nil {
		// The frozen-schema conflict is the authoritative classification even
		// when an independent fixed fee also reported an issue.
		valuation.Completeness = economics.CompletenessConflict
		unavailable = overlapErr
	}
	if unavailable != nil && valuation.Completeness == economics.CompletenessComplete {
		// Preserve the independent lines, while truthfully carrying the
		// quantity/authority failure to the valuation boundary.
		valuation.Completeness = economics.CompletenessPartial
	}
	if overlapErr == nil && ratedMeasureCount == 0 && len(fixedLines) == 0 && fixedErr == nil {
		valuation.Completeness = economics.CompletenessUnavailable
		return finalizeValuation(valuation, fmt.Errorf("%w: no %s quantity observations", ErrRatingEvidenceMissing, input.Basis))
	}
	var totalsErr error
	valuation.Totals, totalsErr = totalsFromLines(valuation.Lines, r.snapshot.Currency)
	if totalsErr != nil && unavailable == nil {
		unavailable = totalsErr
		valuation.Completeness = economics.CompletenessPartial
	}
	if unavailable != nil {
		return finalizeValuation(valuation, unavailable)
	}
	return finalizeValuation(valuation, nil)
}

// inclusionRelationship reports whether a frozen schema relationship declares
// that the parent aggregate already includes the child. Aggregate, subset and
// partition edges all carry same-unit containment semantics. A transform edge
// is a separately governed unit derivation and never an overlap.
func inclusionRelationship(kind metering.RelationshipKind) bool {
	switch kind {
	case metering.RelationshipAggregate, metering.RelationshipSubset, metering.RelationshipPartition:
		return true
	default:
		return false
	}
}

// completeCoverageRelationship reports whether a frozen schema relationship
// declares that the parent is composed of the declared children as a complete
// coverage. An aggregate parent and a partition of the parent both assert that
// the declared children together account for the parent; a subset edge is a
// partial containment and never proves complete coverage, and a transform is a
// separately governed unit derivation.
func completeCoverageRelationship(kind metering.RelationshipKind) bool {
	switch kind {
	case metering.RelationshipAggregate, metering.RelationshipPartition:
		return true
	default:
		return false
	}
}

// completeChildPartitionCoverage returns, per reduction scope, the set of
// present aggregate parent component keys whose frozen complete child partition
// is fully present and complete in that exact same scope and whose declared
// partition structure is unambiguous. A parent in the returned set may have its
// own missing rule excused by the child-only rating because the declared
// children are the authoritative billers for its share.
//
// The proof is structural and explicit: only partition/aggregate edges with
// equal economic direction and unit are considered; a parent is covered only
// when it declares at least one complete child, every declared complete child
// is present and complete in the exact same scope, and no complete child is
// shared by two distinct complete parents anywhere in the frozen schema set
// (an ambiguous overlapping partition fails closed for every parent). It is
// never inferred from component names, quantities, or arithmetic equality.
//
// Presence and quantity completeness are not enough: every child that proves
// the parent's coverage must itself be an eligible biller for the selected
// rating with a resolving rule under the effective qualifiers. A present but
// unselected or unpriced child (for example an informational input_token_total
// that the rating loop skips for lack of a rule) cannot stand in for the
// parent's share; without this the parent would be excused while nothing
// charges its quantity, yielding a false complete valuation.
func (r *ReferenceRater) completeChildPartitionCoverage(aggregates []aggregateMeasure, qualifiers []metering.Dimension) map[string]map[string]struct{} {
	if r == nil || len(r.snapshot.Schemas) == 0 || len(aggregates) == 0 {
		return nil
	}
	childrenByParent := make(map[string][]metering.ComponentKey)
	parentByChild := make(map[string]string)
	ambiguous := false
	for _, schema := range r.snapshot.Schemas {
		for _, relationship := range schema.Relationships {
			if !completeCoverageRelationship(relationship.Kind) {
				continue
			}
			if relationship.Parent.Direction != relationship.Child.Direction || relationship.Parent.Unit != relationship.Child.Unit {
				continue
			}
			parent, parentErr := relationship.Parent.Normalize()
			child, childErr := relationship.Child.Normalize()
			if parentErr != nil || childErr != nil {
				continue
			}
			parentKey := parent.CanonicalKey()
			childKey := child.CanonicalKey()
			childrenByParent[parentKey] = append(childrenByParent[parentKey], child)
			if prior, ok := parentByChild[childKey]; ok && prior != parentKey {
				ambiguous = true
			}
			parentByChild[childKey] = parentKey
		}
	}
	if len(childrenByParent) == 0 || ambiguous {
		return nil
	}
	present := make(map[string]map[string]struct{})
	complete := make(map[string]map[string]struct{})
	for _, item := range aggregates {
		key, keyErr := item.key.Normalize()
		if keyErr != nil {
			continue
		}
		if present[item.scopeKey] == nil {
			present[item.scopeKey] = make(map[string]struct{})
			complete[item.scopeKey] = make(map[string]struct{})
		}
		canonical := key.CanonicalKey()
		present[item.scopeKey][canonical] = struct{}{}
		if item.complete {
			complete[item.scopeKey][canonical] = struct{}{}
		}
	}
	var covered map[string]map[string]struct{}
	for scopeKey, byKey := range present {
		for parentKey, children := range childrenByParent {
			if _, ok := byKey[parentKey]; !ok {
				continue
			}
			covers := true
			for _, child := range children {
				childKey := child.CanonicalKey()
				if _, ok := byKey[childKey]; !ok {
					covers = false
					break
				}
				if _, ok := complete[scopeKey][childKey]; !ok {
					covers = false
					break
				}
				if _, ruleErr := r.resolveRule(child, qualifiers); ruleErr != nil {
					// A declaring child proves the parent's coverage only when it
					// is itself selected and rateable. An informational summary
					// with no rule is skipped by the rating loop, and an
					// unpriced/qualifier-incomplete child cannot stand in for
					// the parent's share; accepting either would suppress the
					// parent while nothing charges its quantity.
					covers = false
					break
				}
			}
			if !covers {
				continue
			}
			if covered == nil {
				covered = make(map[string]map[string]struct{})
			}
			if covered[scopeKey] == nil {
				covered[scopeKey] = make(map[string]struct{})
			}
			covered[scopeKey][parentKey] = struct{}{}
		}
	}
	return covered
}

// parentCoveredByCompletePartition reports whether one rated aggregate lands on
// a specific scope/component pair whose complete child partition coverage was
// proven.
func parentCoveredByCompletePartition(covered map[string]map[string]struct{}, item aggregateMeasure) bool {
	if len(covered) == 0 {
		return false
	}
	scope := covered[item.scopeKey]
	if len(scope) == 0 {
		return false
	}
	key, keyErr := item.key.Normalize()
	if keyErr != nil {
		return false
	}
	_, ok := scope[key.CanonicalKey()]
	return ok
}

// suppressedCompletePartitionParent reports whether one present aggregate's own
// absent rate is excused by a proven complete child partition: the parent must
// itself be complete (so it is not a quantity-incomplete diagnostic), belong to
// a proven covered (scope, parent) pair, and have no resolving rule of its own
// under the effective qualifiers. Only such a genuinely unpriced, fully covered
// summary is removed from its siblings' economic context; a priced parent keeps
// contributing its own quantity to its whole-context tier. This mirrors the
// line-emission suppression exactly, so context and emitted lines agree.
func (r *ReferenceRater) suppressedCompletePartitionParent(covered map[string]map[string]struct{}, item aggregateMeasure, qualifiers []metering.Dimension) bool {
	if !item.complete || !parentCoveredByCompletePartition(covered, item) {
		return false
	}
	_, ruleErr := r.resolveRule(item.key, qualifiers)
	return errors.Is(ruleErr, ErrRateMissing)
}

// includedChildExclusions returns the frozen-schema-declared included children
// that must not be independently rated in an aggregate-only tariff. A child is
// excluded from arithmetic only when all of the following hold:
//
//   - an explicit inclusion relationship (aggregate/subset/partition) names it
//     as the child, with the parent and child sharing direction and unit;
//   - the parent is an effective, selected, eligible aggregate that actually
//     participates in rating: it is retained by the retail selection mask (when
//     one applies) so a policy-unselected raw measure can never excuse a
//     selected child, it is not an informational total/reasoning summary, and
//     it has a rule that resolves under the effective qualifiers so the
//     aggregate is genuinely priced rather than silently dropped;
//   - the child has no rule of its own (ErrRateMissing), so it could not be
//     billed independently; and
//   - the parent aggregate is present in the exact same reduction scope as the
//     child, so scopes are never conflated.
//
// Informational totals (input_token_total, total_token) are inclusive evidence
// summaries, not disjoint aggregate billers: a rule that happens to price one
// must never silently drop the child's own billable evidence. The effective
// selection mask is the same allowlist the rating arithmetic applies, so a raw
// parent measure that lost its source competition is not proof of a parent
// rating. The excluded child evidence stays retained on its original
// observation (the observation reference is never rewritten), it simply does
// not require a rate and does not contribute charge. Children with their own
// rule are left to the B1 overlap logic, and unrelated components keep their
// existing behavior.
func (r *ReferenceRater) includedChildExclusions(observations []metering.Observation, selected func(metering.Observation) bool, mask retailComponentMask, qualifiers []metering.Dimension) map[string]map[string]struct{} {
	if r == nil || len(r.snapshot.Schemas) == 0 || len(observations) == 0 {
		return nil
	}
	presentByScope := make(map[string]map[string]struct{})
	for _, observation := range observations {
		if selected != nil && !selected(observation) {
			continue
		}
		scopeKey := aggregate.ScopeFor(observation).Key()
		for _, measure := range observation.Measures {
			key, keyErr := measure.Key.Normalize()
			if keyErr != nil {
				continue
			}
			if mask != nil {
				if _, retained := mask[retailObservationMaskKey(observation, key.CanonicalKey())]; !retained {
					continue
				}
			}
			keys := presentByScope[scopeKey]
			if keys == nil {
				keys = make(map[string]struct{})
				presentByScope[scopeKey] = keys
			}
			keys[key.CanonicalKey()] = struct{}{}
		}
	}
	if len(presentByScope) == 0 {
		return nil
	}
	var exclusions map[string]map[string]struct{}
	for _, schema := range r.snapshot.Schemas {
		for _, relationship := range schema.Relationships {
			if !inclusionRelationship(relationship.Kind) {
				continue
			}
			if relationship.Parent.Direction != relationship.Child.Direction || relationship.Parent.Unit != relationship.Child.Unit {
				continue
			}
			parent, parentErr := relationship.Parent.Normalize()
			child, childErr := relationship.Child.Normalize()
			if parentErr != nil || childErr != nil {
				continue
			}
			if isInformationalMeasure(parent) {
				continue
			}
			if _, priced := r.resolveRule(parent, qualifiers); priced != nil {
				continue
			}
			if _, childRule := r.resolveRule(child, qualifiers); !errors.Is(childRule, ErrRateMissing) {
				continue
			}
			for scopeKey, keys := range presentByScope {
				if _, ok := keys[parent.CanonicalKey()]; !ok {
					continue
				}
				if exclusions == nil {
					exclusions = make(map[string]map[string]struct{})
				}
				scope := exclusions[scopeKey]
				if scope == nil {
					scope = make(map[string]struct{})
					exclusions[scopeKey] = scope
				}
				scope[child.CanonicalKey()] = struct{}{}
			}
		}
	}
	return exclusions
}

// exclusionPredicate turns an inclusion exclusion set into the measure-level
// predicate consumed by aggregateMeasures.
func exclusionPredicate(exclusions map[string]map[string]struct{}) func(scopeKey string, key metering.ComponentKey) bool {
	if len(exclusions) == 0 {
		return nil
	}
	return func(scopeKey string, key metering.ComponentKey) bool {
		scope := exclusions[scopeKey]
		if len(scope) == 0 {
			return false
		}
		_, excluded := scope[key.CanonicalKey()]
		return excluded
	}
}

// overlappingSchemaInclusionConflicts reports every frozen inclusion or
// partition edge whose parent and child are both effectively payable in the
// exact same reduction scope and economic direction, and every priced subset
// child that collides with a payable complete partition of its own parent even
// when that parent aggregate is unpriced. The returned conflict set names the
// specific (scope, component) pairs that must not be emitted as payable lines;
// unrelated scopes and unrelated components remain additive. Cross-direction
// edges, transform edges, and edges whose halves land in different scopes are
// not conflicts. The error is the typed fail-closed classification for the
// enclosing valuation and is returned whenever any conflict exists. This is
// driven only by the explicit frozen relationship, never by component names or
// coincidental numbers.
//
// The subset/partition rule is needed because the parent aggregate may itself be
// unpriced by a child-only tariff: a direct parent-child check cannot see the
// overlap, yet the priced subset tokens are already inside the complete
// partition. The frozen schema declares no allocation of the subset into a
// particular partition member, so the rater fails closed rather than guessing a
// member or dropping the subset.
//
// The two sides use different evidence sets. A complete partition is proven
// from rateableByScope: every declared partition child must be observed and
// have a rule that resolves under the effective qualifiers in the same scope,
// but a child whose effective charge is zero (an explicitly observed zero
// quantity) still completes the partition. The subset side uses
// payableByScope: only a subset with a strictly positive effective amount can
// conflict, so a zero-valued subset stays nonconflicting, exactly as a
// zero-valued partition member stays payable-free. An absent or unavailable
// child is in neither set and therefore cannot complete the partition.
func (r *ReferenceRater) overlappingSchemaInclusionConflicts(payableByScope map[string]map[string]struct{}, rateableByScope map[string]map[string]struct{}) (map[string]map[string]struct{}, error) {
	if r == nil || len(r.snapshot.Schemas) == 0 || len(payableByScope) == 0 {
		return nil, nil
	}
	type schemaEdge struct {
		schemaID string
		kind     metering.RelationshipKind
		parent   metering.ComponentKey
		child    metering.ComponentKey
	}
	var edges []schemaEdge
	completeChildrenByParent := make(map[string][]metering.ComponentKey)
	subsetChildrenByParent := make(map[string][]metering.ComponentKey)
	parentByCompleteChild := make(map[string]string)
	ambiguousComplete := false
	for _, schema := range r.snapshot.Schemas {
		for _, relationship := range schema.Relationships {
			if !inclusionRelationship(relationship.Kind) {
				continue
			}
			if relationship.Parent.Direction != relationship.Child.Direction {
				continue
			}
			if relationship.Parent.Unit != relationship.Child.Unit {
				continue
			}
			parent, parentErr := relationship.Parent.Normalize()
			child, childErr := relationship.Child.Normalize()
			if parentErr != nil || childErr != nil {
				continue
			}
			edges = append(edges, schemaEdge{schemaID: schema.ID, kind: relationship.Kind, parent: parent, child: child})
			parentKey := parent.CanonicalKey()
			if relationship.Kind == metering.RelationshipSubset {
				subsetChildrenByParent[parentKey] = appendUniqueComponentKey(subsetChildrenByParent[parentKey], child)
				continue
			}
			// Aggregate and partition edges assert a complete coverage of the
			// parent, so they form the partition side of the subset conflict.
			completeChildrenByParent[parentKey] = appendUniqueComponentKey(completeChildrenByParent[parentKey], child)
			childKey := child.CanonicalKey()
			if prior, ok := parentByCompleteChild[childKey]; ok && prior != parentKey {
				ambiguousComplete = true
			}
			parentByCompleteChild[childKey] = parentKey
		}
	}
	if ambiguousComplete {
		// An ambiguous complete partition (a child shared by two distinct
		// parents) proves nothing about any parent's coverage, mirroring the
		// completeChildPartitionCoverage fail-closed rule.
		completeChildrenByParent = nil
	}
	var conflicts map[string]map[string]struct{}
	var firstErr error
	record := func(scope string, keys ...metering.ComponentKey) {
		if len(keys) == 0 {
			return
		}
		if conflicts == nil {
			conflicts = make(map[string]map[string]struct{})
		}
		scopeConflicts := conflicts[scope]
		if scopeConflicts == nil {
			scopeConflicts = make(map[string]struct{})
			conflicts[scope] = scopeConflicts
		}
		for _, key := range keys {
			scopeConflicts[key.CanonicalKey()] = struct{}{}
		}
	}
	for _, edge := range edges {
		for scope, keys := range payableByScope {
			if _, ok := keys[edge.parent.CanonicalKey()]; !ok {
				continue
			}
			if _, ok := keys[edge.child.CanonicalKey()]; !ok {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: schema %q %s relationship prices aggregate parent %s and included child %s in one %q direction %q unit scope",
					ErrSchemaOverlapConflict, edge.schemaID, edge.kind,
					edge.parent.CanonicalKey(), edge.child.CanonicalKey(), string(edge.parent.Direction), edge.parent.Unit)
			}
			record(scope, edge.parent, edge.child)
		}
	}
	for scope, keys := range rateableByScope {
		for parentKey, children := range completeChildrenByParent {
			if len(children) == 0 {
				continue
			}
			complete := true
			for _, child := range children {
				if _, ok := keys[child.CanonicalKey()]; !ok {
					complete = false
					break
				}
			}
			if !complete {
				continue
			}
			payable := payableByScope[scope]
			for _, subset := range subsetChildrenByParent[parentKey] {
				if _, ok := payable[subset.CanonicalKey()]; !ok {
					continue
				}
				if firstErr == nil {
					firstErr = fmt.Errorf("%w: complete partition parent %s has payable partition children and priced included subset child %s in one %q direction %q unit scope",
						ErrSchemaOverlapConflict, parentKey, subset.CanonicalKey(), string(subset.Direction), subset.Unit)
				}
				record(scope, subset)
				record(scope, children...)
			}
		}
	}
	return conflicts, firstErr
}

// appendUniqueComponentKey adds key to keys unless an equal canonical key is
// already present, so a duplicated frozen edge cannot double-list a component.
func appendUniqueComponentKey(keys []metering.ComponentKey, key metering.ComponentKey) []metering.ComponentKey {
	canonical := key.CanonicalKey()
	for _, prior := range keys {
		if prior.CanonicalKey() == canonical {
			return keys
		}
	}
	return append(keys, key)
}

// schemaConflictSuppressed reports whether one rated aggregate lands on a
// specific scope/component pair identified as a frozen-schema overlap conflict.
func schemaConflictSuppressed(conflicts map[string]map[string]struct{}, item aggregateMeasure) bool {
	if len(conflicts) == 0 {
		return false
	}
	scope := conflicts[item.scopeKey]
	if len(scope) == 0 {
		return false
	}
	key, keyErr := item.key.Normalize()
	if keyErr != nil {
		return false
	}
	_, ok := scope[key.CanonicalKey()]
	return ok
}

func (r *ReferenceRater) rateFixedLines(input economics.PostUsageRatingInput, qualifiers []metering.Dimension, inputRefs []metering.ObservationRef) ([]economics.LineItem, error) {
	return r.rateFixedLinesForScope(input, qualifiers, inputRefs, "")
}

func (r *ReferenceRater) rateFixedLinesForScope(input economics.PostUsageRatingInput, qualifiers []metering.Dimension, inputRefs []metering.ObservationRef, scope string) ([]economics.LineItem, error) {
	values := make(map[string]string, len(qualifiers))
	for _, qualifier := range qualifiers {
		values[qualifier.Name] = qualifier.Value
	}
	var lines []economics.LineItem
	var firstErr error
	newLine := func(rule economics.RatingRule) economics.LineItem {
		line := economics.LineItem{
			ID:                    "fixed:" + rule.ID,
			RuleID:                rule.ID,
			ItemID:                rule.ID,
			FixedFee:              &economics.FixedFeeIdentity{ID: rule.ID, Scope: rule.FixedScope, Version: r.snapshot.Ref.Version},
			Unit:                  metering.UnitCount,
			RoundingScope:         economics.RoundingScopeLine,
			RoundingPolicy:        defaultRounding,
			IncludedUnit:          rule.IncludedUnit,
			Status:                economics.RatingLineRated,
			SourceObservationRefs: append([]metering.ObservationRef(nil), inputRefs...),
		}
		if rule.RoundingScope != "" {
			line.RoundingScope = rule.RoundingScope
		}
		if rule.RoundingPolicy != economics.RoundingUnspecified {
			line.RoundingPolicy = rule.RoundingPolicy
		}
		if isZeroDecimal(rule.FixedAmount) {
			line.Status = economics.RatingLineExplicitFree
		}
		return line
	}
	for _, rule := range r.snapshot.Rules {
		if rule.FixedAmount == nil {
			continue
		}
		if scope != "" && fixedFeeScopeKind(rule.FixedScope) != scope {
			continue
		}
		missing := ""
		matches := true
		for _, condition := range rule.Conditions {
			value, ok := values[condition.Name]
			if !ok {
				missing = condition.Name
				matches = false
				continue
			}
			if value != condition.Value {
				matches = false
			}
		}
		if !matches {
			if missing != "" && firstErr == nil {
				firstErr = fmt.Errorf("%w: fixed rule %s requires %s", ErrQualifierMissing, rule.ID, missing)
			}
			continue
		}
		if !fixedFeeScopeMatches(rule.FixedScope, input.Scope) {
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: rule %s requires %s valuation scope, got %q", ErrFixedFeeScopeMismatch, rule.ID, rule.FixedScope, input.Scope)
			}
			continue
		}
		if !strings.EqualFold(rule.Currency, r.snapshot.Currency) {
			line := newLine(rule)
			line.Status = economics.RatingLineCurrencyMismatch
			lines = append(lines, line)
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: fixed rule %s uses %s, tariff uses %s", ErrRateCurrencyMismatch, rule.ID, rule.Currency, r.snapshot.Currency)
			}
			continue
		}
		amount, err := rule.FixedAmount.ToRat()
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: fixed rule %s", ErrRatePrecision, rule.ID)
			}
			continue
		}
		line := newLine(rule)
		if amountErr := setExactAmount(&line, amount, r.snapshot.Currency); amountErr != nil {
			line.Status = statusForRatingError(amountErr)
			lines = append(lines, line)
			if firstErr == nil {
				firstErr = amountErr
			}
			continue
		}
		lines = append(lines, line)
	}
	return lines, firstErr
}

func fixedFeeScopeMatches(feeScope economics.FixedFeeScope, valuationScope string) bool {
	return fixedFeeScopeKind(feeScope) != "" && fixedFeeScopeKind(feeScope) == valuationScopeKind(valuationScope)
}

func fixedFeeScopeKind(scope economics.FixedFeeScope) string {
	switch scope {
	case economics.FixedFeeScopeSubmission:
		return "submission"
	case economics.FixedFeeScopeCall:
		return "call"
	case economics.FixedFeeScopePeriod:
		return "period"
	default:
		return ""
	}
}

func valuationScopeKind(scope string) string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	for _, kind := range []string{"submission", "call", "period"} {
		if scope == kind || strings.HasPrefix(scope, kind+":") {
			return kind
		}
	}
	return ""
}

func totalsFromLines(lines []economics.LineItem, currency string) ([]economics.CurrencyTotal, error) {
	values := make(map[string]*big.Rat)
	buckets := make(map[string]*roundingBucket)
	policies := make(map[economics.RoundingScope]economics.RoundingPolicy)
	for _, line := range lines {
		value, ok := lineAmountRat(line)
		if !ok {
			continue
		}
		if values[currency] == nil {
			values[currency] = new(big.Rat)
		}
		values[currency].Add(values[currency], value)
		scope := line.RoundingScope
		if scope == "" {
			scope = economics.RoundingScopeLine
		}
		if !scope.IsKnown() {
			return nil, fmt.Errorf("%w: unknown rounding scope %q", ErrRatingInvalid, scope)
		}
		policy := line.RoundingPolicy
		if policy == economics.RoundingUnspecified {
			policy = defaultRounding
		}
		if !policy.IsKnown() {
			return nil, fmt.Errorf("%w: unknown rounding policy %q", ErrRatingInvalid, policy)
		}
		if scope != economics.RoundingScopeLine {
			if prior, exists := policies[scope]; exists && prior != policy {
				return nil, fmt.Errorf("%w: conflicting %s rounding policies %q and %q for %s", ErrRatingInvalid, scope, prior, policy, currency)
			}
			policies[scope] = policy
		}
		bucketKey := string(scope) + "\x00" + string(policy)
		bucket := buckets[bucketKey]
		if bucket == nil {
			bucket = &roundingBucket{scope: scope, policy: policy, exact: new(big.Rat)}
			buckets[bucketKey] = bucket
		}
		bucket.exact.Add(bucket.exact, value)
		if scope == economics.RoundingScopeLine {
			if line.RoundedAmount == nil || !line.RoundedAmount.Present {
				return nil, fmt.Errorf("%w: line-rounded amount missing for %s", ErrRatingInvalid, line.ID)
			}
			if bucket.rounded.Present {
				var err error
				bucket.rounded, err = bucket.rounded.Add(*line.RoundedAmount)
				if err != nil {
					return nil, fmt.Errorf("%w: line-rounded total: %v", ErrRatePrecision, err)
				}
			} else {
				bucket.rounded = *line.RoundedAmount
			}
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]economics.CurrencyTotal, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		total := economics.CurrencyTotal{Currency: key}
		decimal, rational, terminating := decimalFromRat(value)
		if terminating {
			total.Amount = &decimal
		} else {
			total.AmountNumerator = rational.Num().String()
			total.AmountDenominator = rational.Denom().String()
		}
		total.RoundedAmount = economics.Money{Currency: key, Present: true}
		bucketKeys := make([]string, 0, len(buckets))
		for bucketKey := range buckets {
			bucketKeys = append(bucketKeys, bucketKey)
		}
		sort.Strings(bucketKeys)
		for _, bucketKey := range bucketKeys {
			bucket := buckets[bucketKey]
			var roundedAmount economics.Money
			if bucket.scope == economics.RoundingScopeLine {
				roundedAmount = bucket.rounded
			} else {
				var err error
				roundedAmount, err = roundMoney(bucket.exact, key, bucket.policy)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrRatePrecision, err)
				}
			}
			if !roundedAmount.Present {
				return nil, fmt.Errorf("%w: rounded amount missing for %s", ErrRatingInvalid, bucketKey)
			}
			var err error
			total.RoundedAmount, err = total.RoundedAmount.Add(roundedAmount)
			if err != nil {
				return nil, fmt.Errorf("%w: rounded total: %v", ErrRatePrecision, err)
			}
		}
		out = append(out, total)
	}
	return out, nil
}

type roundingBucket struct {
	scope   economics.RoundingScope
	policy  economics.RoundingPolicy
	exact   *big.Rat
	rounded economics.Money
}

func lineAmountRat(line economics.LineItem) (*big.Rat, bool) {
	if line.Amount != nil {
		value, err := line.Amount.ToRat()
		return value, err == nil
	}
	if line.AmountNumerator == "" || line.AmountDenominator == "" {
		return nil, false
	}
	numerator, ok := new(big.Int).SetString(line.AmountNumerator, 10)
	if !ok {
		return nil, false
	}
	denominator, ok := new(big.Int).SetString(line.AmountDenominator, 10)
	if !ok || denominator.Sign() <= 0 {
		return nil, false
	}
	return new(big.Rat).SetFrac(numerator, denominator), true
}

func aggregateMeasures(observations []metering.Observation, store string, selected func(metering.Observation) bool, mask retailComponentMask, exclude func(scopeKey string, key metering.ComponentKey) bool) ([]aggregateMeasure, error, error) {
	selectedObservations := make([]metering.Observation, 0, len(observations))
	for _, observation := range observations {
		if selected(observation) {
			selectedObservations = append(selectedObservations, observation)
		}
	}
	// Phase 3 owns replay ordering, source scope, cumulative/replacement
	// semantics and supersession. Rating only projects its effective measures;
	// it must not maintain a second reduction state machine.
	reduced, err := aggregate.ApplyObservations(selectedObservations)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: reduce observations: %v", ErrRatingInvalid, err)
	}
	// A retail selection mask allowlists original (observation, component)
	// pairs for rating. Reduction still runs over the complete original set
	// so supersession and correction links validate against the original
	// payload hashes and every emitted reference stays the original full
	// observation reference. Unselected competing measures never reach
	// arithmetic, completeness, or diagnostics.
	var keptEntries map[string]struct{}
	var retainedIDs map[string]struct{}
	if mask != nil {
		keptEntries = make(map[string]struct{})
		retainedIDs = make(map[string]struct{})
		for _, observation := range reduced.Observations {
			if len(observation.Measures) == 0 {
				retainedIDs[observation.ID] = struct{}{}
				continue
			}
			for _, measure := range observation.Measures {
				key, keyErr := measure.Key.Normalize()
				if keyErr != nil {
					return nil, nil, fmt.Errorf("%w: component key: %v", ErrRatingInvalid, keyErr)
				}
				if _, ok := mask[retailObservationMaskKey(observation, key.CanonicalKey())]; !ok {
					continue
				}
				retainedIDs[observation.ID] = struct{}{}
				keptEntries[aggregate.ScopeFor(observation).Key()+"\x00"+key.CanonicalKey()] = struct{}{}
			}
		}
	}
	byKey := make(map[string]*aggregateMeasure)
	refsByKey := make(map[string][]metering.ObservationRef)
	type observationIdentity struct {
		store, id string
		revision  uint64
	}
	known := make(map[observationIdentity]struct{}, len(reduced.Observations))
	for _, observation := range reduced.Observations {
		known[observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}] = struct{}{}
	}
	superseded := make(map[observationIdentity]struct{})
	for _, observation := range reduced.Observations {
		for _, ref := range observation.Supersedes {
			identity := observationIdentity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}
			if _, exists := known[identity]; exists {
				superseded[identity] = struct{}{}
			}
		}
	}
	pendingReplacementObservations := make(map[observationIdentity]struct{})
	for _, pending := range reduced.PendingSupersedes {
		pendingKey := observationRefKey(pending)
		for _, observation := range reduced.Observations {
			if observation.Semantics != metering.SemanticsReplacement {
				continue
			}
			for _, ref := range observation.Supersedes {
				if observationRefKey(ref) == pendingKey {
					pendingReplacementObservations[observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}] = struct{}{}
				}
			}
		}
	}
	var firstErr error
	setFirstErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	// Build audit refs from replay survivors, not the raw delivery slice. The
	// effective measure below decides completeness; an unavailable predecessor
	// remains an audit ref but cannot poison a complete replacement.
	for _, observation := range reduced.Observations {
		ref, refErr := observation.Ref(store)
		if refErr != nil {
			return nil, nil, fmt.Errorf("%w: observation %q reference: %v", ErrRatingEvidenceMissing, observation.ID, refErr)
		}
		scopeKey := aggregate.ScopeFor(observation).Key()
		for _, measure := range observation.Measures {
			key, keyErr := measure.Key.Normalize()
			if keyErr != nil {
				return nil, nil, fmt.Errorf("%w: component key: %v", ErrRatingInvalid, keyErr)
			}
			if exclude != nil && exclude(scopeKey, key) {
				continue
			}
			identity := scopeKey + "\x00" + key.CanonicalKey()
			if keptEntries != nil {
				if _, ok := keptEntries[identity]; !ok {
					continue
				}
			}
			refsByKey[identity] = appendUniqueObservationRef(refsByKey[identity], ref)
			if _, replaced := superseded[observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}]; replaced {
				continue
			}
			entry := byKey[identity]
			if entry == nil {
				entry = &aggregateMeasure{key: key, scopeKey: scopeKey, complete: true}
				byKey[identity] = entry
			}
			entry.refs = appendUniqueObservationRef(entry.refs, ref)
			if _, pending := pendingReplacementObservations[observationIdentity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}]; pending {
				entry.complete = false
				entry.issueRefs = appendUniqueObservationRef(entry.issueRefs, ref)
				setFirstErr(fmt.Errorf("%w: component %s has unresolved supersession", ErrRatingEvidenceMissing, key.CanonicalKey()))
			}
			if measureIsIncomplete(measure) {
				entry.complete = false
				entry.issueRefs = appendUniqueObservationRef(entry.issueRefs, ref)
				if measure.Value == nil {
					setFirstErr(fmt.Errorf("%w: component %s has no effective value", ErrQuantityIncomplete, key.CanonicalKey()))
				} else {
					setFirstErr(fmt.Errorf("%w: component %s evidence is unavailable", ErrRatingEvidenceMissing, key.CanonicalKey()))
				}
			}
		}
	}
	if len(reduced.Unavailable) != 0 {
		// An authority-unavailable observation without measures has no component
		// line to attach to. Keep the diagnostic at the valuation boundary while
		// leaving unrelated reduced component partitions untouched. Under a
		// retail selection mask, unavailable evidence outside the selected
		// basis must not fail the selected rating.
		report := true
		if retainedIDs != nil {
			report = false
			for _, id := range reduced.Unavailable {
				if _, ok := retainedIDs[id]; ok {
					report = true
					break
				}
			}
		}
		if report {
			setFirstErr(fmt.Errorf("%w: effective quantity evidence is unavailable", ErrRatingEvidenceMissing))
		}
	}
	for _, measure := range reduced.Measures {
		if exclude != nil {
			if key, keyErr := measure.Key.Normalize(); keyErr == nil && exclude(measure.Scope.Key(), key) {
				continue
			}
		}
		identity := measure.Scope.Key() + "\x00" + measure.Key.CanonicalKey()
		if keptEntries != nil {
			if _, ok := keptEntries[identity]; !ok {
				continue
			}
		}
		entry := byKey[identity]
		if entry == nil {
			entry = &aggregateMeasure{key: measure.Key, scopeKey: measure.Scope.Key(), complete: true}
			byKey[identity] = entry
		}
		entry.refs = append([]metering.ObservationRef(nil), refsByKey[identity]...)
		value, valueErr := measure.Value.ToRat()
		if valueErr != nil {
			return nil, nil, fmt.Errorf("%w: reduced measure %s: %v", ErrRatePrecision, measure.Key.CanonicalKey(), valueErr)
		}
		entry.rat = value
		entry.complete = entry.complete && measure.Complete
		if !measure.Complete {
			for _, observation := range reduced.Observations {
				if observation.ID != measure.LastObservationID || observation.Revision != measure.LastRevision || observation.Subject.StoreID != measure.Scope.Subject.StoreID {
					continue
				}
				if ref, refErr := observation.Ref(store); refErr == nil {
					entry.issueRefs = appendUniqueObservationRef(entry.issueRefs, ref)
				}
				break
			}
			setFirstErr(fmt.Errorf("%w: component %s has an unusable correction predecessor", ErrRatingEvidenceMissing, measure.Key.CanonicalKey()))
		}
	}
	// A correction is present-field only. If a superseded predecessor carried
	// an unresolved sibling field that no effective revision replaces, retain a
	// diagnostic partition for that field instead of silently erasing it. An
	// effective reduced value wins over the historical unresolved observation,
	// so a valid replacement does not inherit stale incompleteness.
	for _, observation := range reduced.Observations {
		ref, refErr := observation.Ref(store)
		if refErr != nil {
			return nil, nil, fmt.Errorf("%w: observation %q reference: %v", ErrRatingEvidenceMissing, observation.ID, refErr)
		}
		scopeKey := aggregate.ScopeFor(observation).Key()
		for _, measure := range observation.Measures {
			if !measureIsIncomplete(measure) {
				continue
			}
			key, keyErr := measure.Key.Normalize()
			if keyErr != nil {
				return nil, nil, fmt.Errorf("%w: component key: %v", ErrRatingInvalid, keyErr)
			}
			if exclude != nil && exclude(scopeKey, key) {
				continue
			}
			identity := scopeKey + "\x00" + key.CanonicalKey()
			if keptEntries != nil {
				if _, ok := keptEntries[identity]; !ok {
					continue
				}
			}
			if _, replaced := byKey[identity]; replaced {
				continue
			}
			entry := &aggregateMeasure{key: key, scopeKey: scopeKey, complete: false}
			entry.refs = appendUniqueObservationRef(entry.refs, ref)
			entry.issueRefs = appendUniqueObservationRef(entry.issueRefs, ref)
			byKey[identity] = entry
			if measure.Value == nil {
				setFirstErr(fmt.Errorf("%w: component %s has no effective value", ErrQuantityIncomplete, key.CanonicalKey()))
			} else {
				setFirstErr(fmt.Errorf("%w: component %s evidence is unavailable", ErrRatingEvidenceMissing, key.CanonicalKey()))
			}
		}
	}
	for _, entry := range byKey {
		if entry.rat == nil {
			entry.complete = false
			setFirstErr(fmt.Errorf("%w: component %s has no effective value", ErrQuantityIncomplete, entry.key.CanonicalKey()))
		}
	}
	if len(reduced.PendingSupersedes) != 0 || len(reduced.PendingCoverage) != 0 {
		setFirstErr(fmt.Errorf("%w: effective quantity evidence has unresolved replay links", ErrRatingEvidenceMissing))
	}
	if len(reduced.UnusablePredecessors) != 0 {
		setFirstErr(fmt.Errorf("%w: correction predecessor has no usable quantity baseline", ErrRatingEvidenceMissing))
	}
	if !reduced.Complete && firstErr == nil && keptEntries == nil && reductionIncompleteBeyondExclusions(reduced, exclude) {
		firstErr = fmt.Errorf("%w: reduced quantity evidence is incomplete", ErrQuantityIncomplete)
	}
	// Under a retail selection mask every kept-side gap already sets firstErr
	// above, so unselected evidence must not fail the selected basis with the
	// generic reduction diagnostic.
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]aggregateMeasure, 0, len(keys))
	for _, key := range keys {
		entry := *byKey[key]
		entry.refs = append([]metering.ObservationRef(nil), entry.refs...)
		entry.issueRefs = append([]metering.ObservationRef(nil), entry.issueRefs...)
		if entry.rat == nil {
			entry.rat = new(big.Rat)
		}
		out = append(out, entry)
	}
	return out, firstErr, nil
}

func measureIsIncomplete(measure metering.Measure) bool {
	return measure.Value == nil || measure.Quality == metering.QualityUnknown || measure.Quality == metering.QualityUnavailable
}

// reductionIncompleteBeyondExclusions reports whether the reduced snapshot
// carries any incompleteness that is not attributable to an excluded
// included-child. aggregate.ApplyObservations maintains one global Complete
// flag, so a NULL included child that is already covered by its priced
// aggregate still flips it false. The per-measure attribution loops skip
// excluded children, which leaves the generic fallback as the only reason such
// a child would poison the valuation. This predicate lets the fallback
// distinguish "the only gap is an excluded child" from a genuine unrelated
// reducer failure: an unavailable observation, an incomplete effective measure,
// an incomplete charge, or (when no exclusion applies) the bare global flag
// always keeps the failure visible.
//
// The audit envelope retains superseded revisions, so a historical null measure
// can coexist with the complete replacement that supersedes it. Those
// historical gaps are not effective state: SnapshotV2.Measures is the reducer's
// effective projection and owns its own completeness. A gap in an audit
// observation is therefore attributed only when no effective measure exists for
// the same scope/component; if one exists, the reduced-measure loop below
// decides. This matches the reducer's own effective-history completeness rule
// instead of scanning superseded audit history, so a valid replacement can
// never be poisoned by the null it replaced while an effective null or an
// unresolved replay link still fails closed.
func reductionIncompleteBeyondExclusions(reduced aggregate.SnapshotV2, exclude func(scopeKey string, key metering.ComponentKey) bool) bool {
	if exclude == nil {
		return true
	}
	effective := make(map[string]struct{}, len(reduced.Measures))
	for _, measure := range reduced.Measures {
		key, keyErr := measure.Key.Normalize()
		if keyErr != nil {
			continue
		}
		effective[measure.Scope.Key()+"\x00"+key.CanonicalKey()] = struct{}{}
	}
	for _, observation := range reduced.Observations {
		scopeKey := aggregate.ScopeFor(observation).Key()
		for _, measure := range observation.Measures {
			if !measureIsIncomplete(measure) {
				continue
			}
			key, keyErr := measure.Key.Normalize()
			if keyErr != nil {
				return true
			}
			if exclude(scopeKey, key) {
				continue
			}
			if _, projected := effective[scopeKey+"\x00"+key.CanonicalKey()]; projected {
				continue
			}
			return true
		}
	}
	for _, measure := range reduced.Measures {
		if measure.Complete {
			continue
		}
		key, keyErr := measure.Key.Normalize()
		if keyErr != nil || !exclude(measure.Scope.Key(), key) {
			return true
		}
	}
	for _, charge := range reduced.Charges {
		if !charge.Complete {
			return true
		}
	}
	return false
}

func appendUniqueObservationRef(refs []metering.ObservationRef, ref metering.ObservationRef) []metering.ObservationRef {
	for _, prior := range refs {
		if prior.Equal(ref) {
			return refs
		}
	}
	return append(refs, ref)
}

func effectiveQualifiers(snapshot, input []metering.Dimension) ([]metering.Dimension, error) {
	merged := make(map[string]string, len(snapshot)+len(input))
	for _, dimensions := range [][]metering.Dimension{snapshot, input} {
		for _, dimension := range dimensions {
			if prior, ok := merged[dimension.Name]; ok && prior != dimension.Value {
				return nil, fmt.Errorf("%w: %s=%s conflicts with %s", ErrQualifierConflict, dimension.Name, prior, dimension.Value)
			}
			merged[dimension.Name] = dimension.Value
		}
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]metering.Dimension, 0, len(keys))
	for _, key := range keys {
		out = append(out, metering.Dimension{Name: key, Value: merged[key]})
	}
	return out, nil
}

func (r *ReferenceRater) rateMeasureLine(item aggregateMeasure, all []aggregateMeasure, qualifiers []metering.Dimension, input economics.PostUsageRatingInput, inputRefs []metering.ObservationRef) (economics.LineItem, error) {
	rule, err := r.resolveRule(item.key, qualifiers)
	lineID := "measure:" + item.key.CanonicalKey()
	if item.scopeKey != "" {
		lineID += ":" + scopeDigest(item.scopeKey)
	}
	line := economics.LineItem{
		ID:                    lineID,
		RuleID:                "unresolved",
		ItemID:                item.key.CanonicalKey(),
		Component:             componentPtr(item.key),
		Quantity:              decimalPtrFromRat(item.rat),
		Unit:                  item.key.Unit,
		RoundingScope:         economics.RoundingScopeLine,
		RoundingPolicy:        defaultRounding,
		SourceObservationRefs: append([]metering.ObservationRef(nil), item.refs...),
		Status:                economics.RatingLineRated,
	}
	if !item.complete {
		line.Quantity = nil
		line.Status = economics.RatingLineQuantityIncomplete
		return line, fmt.Errorf("%w: component %s", ErrQuantityIncomplete, item.key.CanonicalKey())
	}
	if err != nil {
		line.Status = statusForRatingError(err)
		return line, err
	}
	if !strings.EqualFold(rule.Currency, r.snapshot.Currency) {
		line.Status = economics.RatingLineCurrencyMismatch
		return line, fmt.Errorf("%w: rule %s uses %s, tariff uses %s", ErrRateCurrencyMismatch, rule.ID, rule.Currency, r.snapshot.Currency)
	}
	line.RuleID = rule.ID
	line.IncludedUnit = rule.IncludedUnit
	if rule.RoundingScope != "" {
		line.RoundingScope = rule.RoundingScope
	}
	if rule.RoundingPolicy != economics.RoundingUnspecified {
		line.RoundingPolicy = rule.RoundingPolicy
	}
	periodAllowed := scopeIsPeriod(input.Scope)
	if (rule.SelectionScope == economics.SelectionPeriod || rule.RoundingScope == economics.RoundingScopePeriod) && !periodAllowed {
		line.Status = statusForRatingError(ErrPeriodScopeRequired)
		return line, fmt.Errorf("%w: rule %q", ErrPeriodScopeRequired, rule.ID)
	}
	if (rule.SelectionScope == economics.SelectionWholeContext || rule.SelectionScope == economics.SelectionPeriod) && !contextMeasuresComplete(item.key, all) {
		line.Quantity = nil
		line.Status = economics.RatingLineQuantityIncomplete
		return line, fmt.Errorf("%w: context for %s is incomplete", ErrQuantityIncomplete, item.key.CanonicalKey())
	}
	if rule.UnitPrice != nil {
		line.UnitPrice = cloneDecimal(rule.UnitPrice)
	} else if rule.RateNumerator != nil {
		line.RateNumerator = cloneDecimal(rule.RateNumerator)
		line.RateDenominator = cloneDecimal(rule.RateDenominator)
	}
	contextQuantity := item.rat
	if rule.SelectionScope == economics.SelectionWholeContext || rule.SelectionScope == economics.SelectionPeriod {
		contextQuantity = contextTotal(item.key, all)
	}
	amount, price, err := evaluateQuantityRule(rule, item.rat, contextQuantity, periodAllowed)
	if err != nil {
		line.Status = statusForRatingError(err)
		return line, err
	}
	if price != nil {
		setPriceFields(&line, price)
	}
	// A zero unit rate is explicit free only when the final charge is also
	// zero. A positive minimum is a real charge even when its base rate is
	// zero, so it must remain distinguishable from an explicit free line.
	if amount.Sign() == 0 && (rule.UnitPrice != nil && isZeroDecimal(rule.UnitPrice) || price != nil && price.rat.Sign() == 0) {
		line.Status = economics.RatingLineExplicitFree
	}
	if amountErr := setExactAmount(&line, amount, r.snapshot.Currency); amountErr != nil {
		line.Status = statusForRatingError(amountErr)
		return line, amountErr
	}
	line.SourceObservationRefs = append([]metering.ObservationRef(nil), item.refs...)
	if len(line.SourceObservationRefs) == 0 {
		line.SourceObservationRefs = append([]metering.ObservationRef(nil), inputRefs...)
	}
	return line, nil
}

func scopeDigest(scopeKey string) string {
	sum := sha256.Sum256([]byte(scopeKey))
	return hex.EncodeToString(sum[:])
}

func componentPtr(key metering.ComponentKey) *metering.ComponentKey {
	copy := key.Clone()
	return &copy
}

func contextTotal(key metering.ComponentKey, all []aggregateMeasure) *big.Rat {
	total := new(big.Rat)
	for _, item := range all {
		if isInformationalMeasure(item.key) {
			// Inclusive total/reasoning counters are evidence summaries, not
			// additional billable units. Including them in a whole-context
			// threshold would select a tier from duplicated quantity.
			continue
		}
		// A whole-context threshold is intentionally broader than the priced
		// line: it includes sibling native components in the same direction,
		// unit and inclusion schema (for example uncached and cache-read input
		// tokens), while never equating unlike units or opposite flow directions.
		// Component and dimension identity remain on the charge line itself.
		if item.key.Direction == key.Direction && item.key.Unit == key.Unit && item.key.SchemaID == key.SchemaID {
			total.Add(total, item.rat)
		}
	}
	return total
}

func contextMeasuresComplete(key metering.ComponentKey, all []aggregateMeasure) bool {
	for _, item := range all {
		if isInformationalMeasure(item.key) || item.key.Direction != key.Direction || item.key.Unit != key.Unit || item.key.SchemaID != key.SchemaID {
			continue
		}
		if !item.complete || item.rat == nil {
			return false
		}
	}
	return true
}

func isInformationalMeasure(key metering.ComponentKey) bool {
	switch key.Component {
	case metering.ComponentInputTokenTotal, metering.ComponentTotalToken:
		return true
	default:
		return false
	}
}

type resolvedPrice struct {
	rat         *big.Rat
	numerator   *metering.Decimal
	denominator *metering.Decimal
	unitPrice   *metering.Decimal
	pricePer    *metering.Decimal
}

func evaluateQuantityRule(rule economics.RatingRule, quantity, contextQuantity *big.Rat, periodAllowed bool) (*big.Rat, *resolvedPrice, error) {
	kind := rule.Kind
	if kind == "" {
		switch {
		case len(rule.Tiers) != 0 && rule.TierMode == economics.TierAllUnits:
			kind = economics.RatingRuleAllUnits
		case len(rule.Tiers) != 0 && rule.TierMode == economics.TierGraduated:
			kind = economics.RatingRuleGraduated
		case rule.BlockSize != nil && rule.MinimumAmount == nil:
			kind = economics.RatingRuleBlock
		case rule.MinimumAmount != nil && rule.BlockSize == nil:
			kind = economics.RatingRuleMinimum
		default:
			kind = economics.RatingRuleLinear
		}
	}
	switch kind {
	case economics.RatingRuleConversion:
		return nil, nil, fmt.Errorf("%w: conversion rule %q requires an explicit transform adapter", ErrRateUnsupported, rule.ID)
	case economics.RatingRuleFixed:
		return nil, nil, fmt.Errorf("%w: fixed rule %q is not a quantity rule", ErrRateInvalid, rule.ID)
	case economics.RatingRuleBlock:
		if rule.BlockSize == nil {
			return nil, nil, fmt.Errorf("%w: block rule %q requires block size", ErrRateInvalid, rule.ID)
		}
	case economics.RatingRuleMinimum:
		if rule.MinimumAmount == nil {
			return nil, nil, fmt.Errorf("%w: minimum rule %q requires minimum amount", ErrRateInvalid, rule.ID)
		}
	case economics.RatingRuleAllUnits:
		if rule.TierMode != economics.TierAllUnits || len(rule.Tiers) == 0 {
			return nil, nil, fmt.Errorf("%w: all_units rule %q requires all_units tiers", ErrRateInvalid, rule.ID)
		}
	case economics.RatingRuleGraduated:
		if rule.TierMode != economics.TierGraduated || len(rule.Tiers) == 0 {
			return nil, nil, fmt.Errorf("%w: graduated rule %q requires graduated tiers", ErrRateInvalid, rule.ID)
		}
	case economics.RatingRuleLinear:
		// Linear rules may carry the legacy block/minimum modifiers. Explicit
		// block and minimum kinds above require exactly their own modifier.
	default:
		return nil, nil, fmt.Errorf("%w: unknown quantity rule kind %q", ErrRateInvalid, kind)
	}
	if quantity.Sign() < 0 {
		return nil, nil, fmt.Errorf("%w: negative quantity for rule %q", ErrRatingInvalid, rule.ID)
	}
	if (rule.SelectionScope == economics.SelectionPeriod || rule.RoundingScope == economics.RoundingScopePeriod) && !periodAllowed {
		return nil, nil, fmt.Errorf("%w: rule %q", ErrPeriodScopeRequired, rule.ID)
	}
	price, err := priceForRule(rule, contextQuantity)
	if err != nil {
		return nil, nil, err
	}
	billedQuantity := new(big.Rat).Set(quantity)
	if rule.BlockSize != nil {
		if kind != economics.RatingRuleLinear && kind != economics.RatingRuleBlock {
			return nil, nil, fmt.Errorf("%w: rule %q block modifier conflicts with kind %q", ErrRateInvalid, rule.ID, kind)
		}
		block, err := rule.BlockSize.ToRat()
		if err != nil || block.Sign() <= 0 {
			return nil, nil, fmt.Errorf("%w: block size for rule %q", ErrRatePrecision, rule.ID)
		}
		blocks := ceilRat(new(big.Rat).Quo(billedQuantity, block))
		billedQuantity.Mul(new(big.Rat).SetInt(blocks), block)
	}
	amount := new(big.Rat)
	if kind == economics.RatingRuleGraduated {
		amount, err = graduatedAmount(rule.Tiers, billedQuantity)
		if err != nil {
			return nil, nil, err
		}
	} else {
		amount.Mul(billedQuantity, price.rat)
	}
	if rule.MinimumAmount != nil {
		if kind != economics.RatingRuleLinear && kind != economics.RatingRuleMinimum {
			return nil, nil, fmt.Errorf("%w: rule %q minimum modifier conflicts with kind %q", ErrRateInvalid, rule.ID, kind)
		}
		minimum, err := rule.MinimumAmount.ToRat()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: minimum amount for rule %q", ErrRatePrecision, rule.ID)
		}
		if amount.Cmp(minimum) < 0 {
			amount = minimum
		}
	}
	if err := boundedRat(amount); err != nil {
		return nil, nil, err
	}
	return amount, price, nil
}

func (r *ReferenceRater) resolveRule(key metering.ComponentKey, qualifiers []metering.Dimension) (economics.RatingRule, error) {
	qualifierValues := make(map[string]string, len(qualifiers))
	for _, qualifier := range qualifiers {
		qualifierValues[qualifier.Name] = qualifier.Value
	}
	var candidates []economics.RatingRule
	missing := map[string]struct{}{}
	for _, rule := range r.snapshot.Rules {
		if rule.Component == nil || !ruleComponentMatches(*rule.Component, key) {
			continue
		}
		match := true
		for _, condition := range rule.Conditions {
			value, ok := qualifierValues[condition.Name]
			if !ok {
				missing[condition.Name] = struct{}{}
				match = false
				continue
			}
			if value != condition.Value {
				match = false
			}
		}
		if match {
			candidates = append(candidates, rule)
		}
	}
	if len(candidates) == 0 {
		if len(missing) != 0 {
			return economics.RatingRule{}, fmt.Errorf("%w: %s", ErrQualifierMissing, strings.Join(sortedKeys(missing), ","))
		}
		return economics.RatingRule{}, fmt.Errorf("%w: component %s", ErrRateMissing, key.CanonicalKey())
	}
	best := candidates[0]
	bestSpecificity := ruleSpecificity(best)
	for _, candidate := range candidates[1:] {
		specificity := ruleSpecificity(candidate)
		if specificity > bestSpecificity {
			best, bestSpecificity = candidate, specificity
			continue
		}
		if specificity == bestSpecificity && candidate.ID != best.ID {
			return economics.RatingRule{}, fmt.Errorf("%w: rules %q and %q for %s", economics.ErrRatingRuleOverlap, best.ID, candidate.ID, key.CanonicalKey())
		}
	}
	return best, nil
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ruleSpecificity(rule economics.RatingRule) int {
	if rule.Component == nil {
		return len(rule.Conditions)
	}
	return len(rule.Component.Dimensions) + len(rule.Conditions)
}

func priceForRule(rule economics.RatingRule, contextQuantity *big.Rat) (*resolvedPrice, error) {
	if len(rule.Tiers) == 0 {
		if rule.UnitPrice == nil && rule.RateNumerator == nil {
			return nil, fmt.Errorf("%w: rule %q", ErrRateMissing, rule.ID)
		}
		return priceFromParts(rule.UnitPrice, rule.RateNumerator, rule.RateDenominator, rule.PricePer)
	}
	if rule.TierMode != economics.TierAllUnits {
		// graduatedAmount resolves each tier independently; a representative
		// price is still returned for line provenance when possible.
		return priceFromTier(rule.Tiers[0])
	}
	for _, tier := range rule.Tiers {
		if tier.UpTo == nil {
			return priceFromTier(tier)
		}
		limit, err := tier.UpTo.ToRat()
		if err != nil {
			return nil, fmt.Errorf("%w: tier threshold", ErrRatePrecision)
		}
		if contextQuantity.Cmp(limit) <= 0 {
			return priceFromTier(tier)
		}
	}
	return priceFromTier(rule.Tiers[len(rule.Tiers)-1])
}

func priceFromTier(tier economics.RatingTier) (*resolvedPrice, error) {
	return priceFromParts(tier.UnitPrice, tier.RateNumerator, tier.RateDenominator, tier.PricePer)
}

func priceFromParts(unitPrice, numerator, denominator, pricePer *metering.Decimal) (*resolvedPrice, error) {
	var rate *big.Rat
	if unitPrice != nil {
		var err error
		rate, err = unitPrice.ToRat()
		if err != nil {
			return nil, fmt.Errorf("%w: unit price: %v", ErrRatePrecision, err)
		}
	} else {
		if numerator == nil || denominator == nil {
			return nil, fmt.Errorf("%w: rational unit rate missing", ErrRateMissing)
		}
		n, errN := numerator.ToRat()
		d, errD := denominator.ToRat()
		if errN != nil || errD != nil || d.Sign() <= 0 {
			return nil, fmt.Errorf("%w: rational unit rate invalid", ErrRatePrecision)
		}
		rate = new(big.Rat).Quo(n, d)
	}
	if rate.Sign() < 0 {
		return nil, fmt.Errorf("%w: negative unit rate", ErrRateInvalid)
	}
	per := new(big.Rat).SetInt64(1)
	if pricePer != nil {
		var err error
		per, err = pricePer.ToRat()
		if err != nil || per.Sign() <= 0 {
			return nil, fmt.Errorf("%w: price_per", ErrRatePrecision)
		}
	}
	rate.Quo(rate, per)
	if err := boundedRat(rate); err != nil {
		return nil, err
	}
	return &resolvedPrice{
		rat: rate, numerator: cloneDecimal(numerator), denominator: cloneDecimal(denominator),
		unitPrice: cloneDecimal(unitPrice), pricePer: cloneDecimal(pricePer),
	}, nil
}

func graduatedAmount(tiers []economics.RatingTier, quantity *big.Rat) (*big.Rat, error) {
	remaining := new(big.Rat).Set(quantity)
	previous := new(big.Rat)
	total := new(big.Rat)
	for _, tier := range tiers {
		limit := new(big.Rat).Set(remaining)
		if tier.UpTo != nil {
			upTo, err := tier.UpTo.ToRat()
			if err != nil {
				return nil, fmt.Errorf("%w: graduated threshold", ErrRatePrecision)
			}
			if upTo.Cmp(previous) < 0 {
				return nil, fmt.Errorf("%w: graduated thresholds decrease", ErrRatingInvalid)
			}
			limit.Sub(upTo, previous)
			if limit.Sign() < 0 {
				limit.SetInt64(0)
			}
			if limit.Cmp(remaining) > 0 {
				limit.Set(remaining)
			}
		}
		price, err := priceFromTier(tier)
		if err != nil {
			return nil, err
		}
		total.Add(total, new(big.Rat).Mul(limit, price.rat))
		remaining.Sub(remaining, limit)
		if remaining.Sign() <= 0 {
			return total, nil
		}
		if tier.UpTo != nil {
			previous = new(big.Rat).Set(previous)
			previous.Set(new(big.Rat).Add(previous, limit))
		}
	}
	if remaining.Sign() > 0 {
		return nil, fmt.Errorf("%w: graduated tiers leave quantity uncovered", ErrRateMissing)
	}
	return total, nil
}

func ceilRat(value *big.Rat) *big.Int {
	quo, rem := new(big.Int), new(big.Int)
	quo.QuoRem(value.Num(), value.Denom(), rem)
	if rem.Sign() != 0 && value.Sign() > 0 {
		quo.Add(quo, big.NewInt(1))
	}
	return quo
}

func decimalPtrFromRat(value *big.Rat) *metering.Decimal {
	if value == nil {
		return nil
	}
	decimal, _, ok := decimalFromRat(value)
	if !ok {
		return nil
	}
	return &decimal
}

func setExactAmount(line *economics.LineItem, value *big.Rat, currency string) error {
	if line == nil || value == nil {
		return fmt.Errorf("%w: nil exact amount", ErrRatePrecision)
	}
	if err := boundedRat(value); err != nil {
		return err
	}
	decimal, numerator, ok := decimalFromRat(value)
	if ok {
		line.Amount = &decimal
		line.AmountNumerator = ""
		line.AmountDenominator = ""
	} else {
		line.Amount = nil
		line.AmountNumerator = numerator.Num().String()
		line.AmountDenominator = numerator.Denom().String()
	}
	// A call- or period-scoped line carries the exact pre-round value only;
	// its total is the authoritative rounding boundary. Rounding here would
	// create a second, non-authoritative amount that downstream code could
	// accidentally sum. Line-scoped output is rounded exactly once here.
	scope := line.RoundingScope
	if scope == "" {
		scope = economics.RoundingScopeLine
	}
	if !scope.IsKnown() {
		return fmt.Errorf("%w: unknown rounding scope %q", ErrRatingInvalid, scope)
	}
	if scope == economics.RoundingScopeLine {
		rounded, err := roundMoney(value, currency, line.RoundingPolicy)
		if err != nil {
			return fmt.Errorf("%w: rounded amount: %v", ErrRatePrecision, err)
		}
		line.RoundedAmount = &rounded
	} else {
		line.RoundedAmount = nil
	}
	return nil
}

func setPriceFields(line *economics.LineItem, price *resolvedPrice) {
	if line == nil || price == nil {
		return
	}
	// Preserve the configured decimal only when no divisor changes its
	// effective rate. Otherwise retain the exact effective rational so replay
	// cannot mistake a display price for the applied price.
	if price.unitPrice != nil && price.pricePer == nil {
		line.UnitPrice = cloneDecimal(price.unitPrice)
		line.RateNumerator = nil
		line.RateDenominator = nil
		return
	}
	if price.rat == nil {
		return
	}
	if decimal, rational, terminating := decimalFromRat(price.rat); terminating {
		line.UnitPrice = &decimal
		line.RateNumerator = nil
		line.RateDenominator = nil
	} else {
		line.UnitPrice = nil
		line.RateNumerator = cloneDecimalFromRat(rational.Num())
		line.RateDenominator = cloneDecimalFromRat(rational.Denom())
	}
}

func cloneDecimalFromRat(value *big.Int) *metering.Decimal {
	if value == nil {
		return nil
	}
	d := metering.Decimal{Coefficient: value.String()}
	return &d
}

func decimalFromRat(value *big.Rat) (metering.Decimal, *big.Rat, bool) {
	if value == nil {
		return metering.Decimal{}, new(big.Rat), false
	}
	if err := boundedRat(value); err != nil {
		return metering.Decimal{}, new(big.Rat).Set(value), false
	}
	den := new(big.Int).Set(value.Denom())
	two, five := 0, 0
	for new(big.Int).Mod(den, big.NewInt(2)).Sign() == 0 {
		two++
		den.Quo(den, big.NewInt(2))
	}
	for new(big.Int).Mod(den, big.NewInt(5)).Sign() == 0 {
		five++
		den.Quo(den, big.NewInt(5))
	}
	if den.Cmp(big.NewInt(1)) != 0 {
		return metering.Decimal{}, new(big.Rat).Set(value), false
	}
	scale := max(five, two)
	if scale > int(metering.MaxDecimalScale) {
		return metering.Decimal{}, new(big.Rat).Set(value), false
	}
	coefficient := new(big.Int).Set(value.Num())
	coefficient.Mul(coefficient, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
	coefficient.Quo(coefficient, value.Denom())
	d := metering.Decimal{Coefficient: coefficient.String(), Scale: uint8(scale)}
	normalized, err := d.Normalize()
	if err != nil {
		return metering.Decimal{}, new(big.Rat).Set(value), false
	}
	return normalized, new(big.Rat).Set(value), true
}

func roundMoney(value *big.Rat, currency string, policy economics.RoundingPolicy) (economics.Money, error) {
	nanos := new(big.Rat).Mul(value, new(big.Rat).SetInt64(1_000_000_000))
	if policy == economics.RoundingUnspecified {
		policy = defaultRounding
	}
	units, err := economics.RoundToInt64(nanos, policy)
	if err != nil {
		return economics.Money{}, err
	}
	return economics.Money{NanoUnits: units, Currency: currency, Present: true}, nil
}

func boundedRat(value *big.Rat) error {
	if value == nil || value.Denom().Sign() <= 0 {
		return fmt.Errorf("%w: invalid rational", ErrRatePrecision)
	}
	if len(value.Num().String()) > maxRationalDigits || len(value.Denom().String()) > maxRationalDigits {
		return fmt.Errorf("%w: rational operand bound exceeded", ErrRatePrecision)
	}
	return nil
}

func cloneDecimal(value *metering.Decimal) *metering.Decimal {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func isZeroDecimal(value *metering.Decimal) bool {
	if value == nil {
		return false
	}
	n, err := value.Normalize()
	return err == nil && n.Coefficient == "0"
}

func statusForRatingError(err error) economics.RatingLineStatus {
	switch {
	case errors.Is(err, ErrRateMissing):
		return economics.RatingLineRateMissing
	case errors.Is(err, ErrRateUnsupported):
		return economics.RatingLineRateUnsupported
	case errors.Is(err, ErrRatePrecision):
		return economics.RatingLineRateUnsupported
	case errors.Is(err, ErrRateCurrencyMismatch):
		return economics.RatingLineCurrencyMismatch
	case errors.Is(err, ErrQuantityIncomplete):
		return economics.RatingLineQuantityIncomplete
	case errors.Is(err, ErrCoverageInvalid):
		return economics.RatingLineCoverageIncomplete
	default:
		return economics.RatingLineRateUnsupported
	}
}
