package billing

import (
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 12.3A aggregate discrepancy projection. It turns comparable and
// non-comparable reconciliation evidence into bounded, deterministic
// operational signals while retaining every source reference.

var (
	// ErrReconciliationAggregateInvalid identifies malformed findings or an
	// invalid tolerance policy.
	ErrReconciliationAggregateInvalid = errors.New("billing: invalid reconciliation aggregate input")
	// ErrReconciliationAggregateBoundExceeded identifies input or row
	// cardinality beyond the bounded aggregate contract.
	ErrReconciliationAggregateBoundExceeded = errors.New("billing: reconciliation aggregate bound exceeded")
	// ErrReconciliationAggregateOverflow identifies an exact aggregate total
	// outside the bounded decimal/rational contract.
	ErrReconciliationAggregateOverflow = errors.New("billing: reconciliation aggregate arithmetic overflow")
)

const (
	// MaxReconciliationAggregateFindings bounds one aggregate projection.
	MaxReconciliationAggregateFindings = 4096
	// MaxReconciliationAggregateRows bounds distinct scope/currency/unit rows.
	MaxReconciliationAggregateRows = 256
)

// ReconciliationFinding is one comparable or non-comparable evidence item.
// Expected/Reported are quantity or monetary exact values; the amount's
// Currency field carries the exact unit key. Quality labels and source refs
// are retained verbatim.
type ReconciliationFinding struct {
	ID              string                         `json:"id"`
	Scope           string                         `json:"scope,omitempty"`
	Direction       metering.FlowDirection         `json:"direction,omitempty"`
	Unit            string                         `json:"unit,omitempty"`
	Currency        string                         `json:"currency,omitempty"`
	Component       string                         `json:"component,omitempty"`
	SchemaID        string                         `json:"schema_id,omitempty"`
	Context         string                         `json:"context,omitempty"`
	Status          ReconciliationComparisonStatus `json:"status"`
	Reason          ReconciliationComparisonReason `json:"reason,omitempty"`
	LocalQuality    string                         `json:"local_quality,omitempty"`
	ProviderQuality string                         `json:"provider_quality,omitempty"`

	Expected *MonetaryExactAmount `json:"expected,omitempty"`
	Reported *MonetaryExactAmount `json:"reported,omitempty"`

	SourceObservationRefs []metering.ObservationRef `json:"source_observation_refs,omitempty"`
	ValuationIDs          []string                  `json:"valuation_ids,omitempty"`

	EvaluatedStatus  ReconciliationComparisonStatus     `json:"evaluated_status"`
	EvaluationReason ReconciliationComparisonReason     `json:"evaluation_reason,omitempty"`
	Evaluation       *ReconciliationToleranceEvaluation `json:"evaluation,omitempty"`
}

// ReconciliationStatusCount keeps deterministic per-status counts.
type ReconciliationStatusCount struct {
	Status ReconciliationComparisonStatus `json:"status"`
	Count  int                            `json:"count"`
}

// ReconciliationAggregateRow is one scope/currency/unit projection. Gross
// absolute discrepancy never nets offsets; the signed net is separate.
type ReconciliationAggregateRow struct {
	Scope    string `json:"scope,omitempty"`
	Currency string `json:"currency,omitempty"`
	Unit     string `json:"unit,omitempty"`

	GrossAbsoluteDiscrepancy      *MonetaryExactAmount `json:"gross_absolute_discrepancy"`
	DiscrepantAbsoluteDiscrepancy *MonetaryExactAmount `json:"discrepant_absolute_discrepancy"`
	NetSignedDiscrepancy          *MonetaryExactAmount `json:"net_signed_discrepancy"`

	AffectedCount          int `json:"affected_count"`
	EstimatedAffectedCount int `json:"estimated_affected_count"`

	StatusCounts    []ReconciliationStatusCount `json:"status_counts"`
	MissingIDs      []string                    `json:"missing_ids,omitempty"`
	IncomparableIDs []string                    `json:"incomparable_ids,omitempty"`
	ConflictIDs     []string                    `json:"conflict_ids,omitempty"`
}

// ReconciliationAggregate is the immutable projection result. It retains the
// policy reference, every classified finding and its source refs.
type ReconciliationAggregate struct {
	Policy   VersionRef                   `json:"policy"`
	Findings []ReconciliationFinding      `json:"findings"`
	Rows     []ReconciliationAggregateRow `json:"rows"`
}

// ReconciliationFindingsFromQuantityComparison projects Task 12.1 outcomes
// into aggregate findings. Local values are the expected side and provider
// values the reported side.
func ReconciliationFindingsFromQuantityComparison(scope string, comparison ComponentQuantityComparison) ([]ReconciliationFinding, error) {
	if !validEconomicIdentity(scope, metering.MaxSchemaIDBytes) {
		return nil, fmt.Errorf("%w: finding scope required", ErrReconciliationAggregateInvalid)
	}
	findings := make([]ReconciliationFinding, 0, len(comparison.Items))
	for i, item := range comparison.Items {
		finding, err := reconciliationFindingFromQuantityItem(scope, item)
		if err != nil {
			return nil, fmt.Errorf("%w: quantity item %d: %v", ErrReconciliationAggregateInvalid, i, err)
		}
		findings = append(findings, finding)
	}
	sortReconciliationFindings(findings)
	return findings, nil
}

func reconciliationFindingFromQuantityItem(scope string, item ComponentQuantityComparisonItem) (ReconciliationFinding, error) {
	context, err := canonicalReconciliationQualifierKey(item.Key.Dimensions)
	if err != nil {
		return ReconciliationFinding{}, err
	}
	finding := ReconciliationFinding{
		ID: item.Key.CanonicalKey(), Scope: scope,
		Direction: item.Key.Direction, Unit: item.Key.Unit,
		Component: item.Key.Component, SchemaID: item.Key.SchemaID, Context: context,
		Status: item.Status, Reason: item.Reason,
	}
	if len(item.Local) == 1 {
		finding.LocalQuality = item.Local[0].Quality
		if item.Local[0].Value != nil {
			amount, err := quantityExactAmount(item.Key.Unit, *item.Local[0].Value)
			if err != nil {
				return ReconciliationFinding{}, err
			}
			finding.Expected = &amount
		}
	}
	if len(item.Provider) == 1 {
		finding.ProviderQuality = item.Provider[0].Quality
		if item.Provider[0].Value != nil {
			amount, err := quantityExactAmount(item.Key.Unit, *item.Provider[0].Value)
			if err != nil {
				return ReconciliationFinding{}, err
			}
			finding.Reported = &amount
		}
	}
	for _, evidence := range item.Local {
		finding.SourceObservationRefs = append(finding.SourceObservationRefs, evidence.Observation)
	}
	for _, evidence := range item.Provider {
		finding.SourceObservationRefs = append(finding.SourceObservationRefs, evidence.Observation)
	}
	return finding, nil
}

func quantityExactAmount(unit string, value metering.Decimal) (MonetaryExactAmount, error) {
	rat, err := value.ToRat()
	if err != nil {
		return MonetaryExactAmount{}, err
	}
	amount, err := newMonetaryExactAmount(unit, rat)
	if err != nil {
		return MonetaryExactAmount{}, fmt.Errorf("%w: %v", ErrReconciliationAggregateOverflow, err)
	}
	return amount, nil
}

// ReconciliationFindingsFromMonetaryComparison projects Task 12.2 outcomes
// into aggregate findings. E totals are the expected side and P totals the
// reported side; missing/incomparable end-to-end terms keep their status.
func ReconciliationFindingsFromMonetaryComparison(scope string, comparison MonetaryDiscrepancyComparison) ([]ReconciliationFinding, error) {
	if !validEconomicIdentity(scope, metering.MaxSchemaIDBytes) {
		return nil, fmt.Errorf("%w: finding scope required", ErrReconciliationAggregateInvalid)
	}
	valuationIDs := make([]string, 0, len(comparison.Valuations))
	var sourceRefs []metering.ObservationRef
	for _, evidence := range comparison.Valuations {
		valuationIDs = append(valuationIDs, evidence.Valuation.ID)
		sourceRefs = append(sourceRefs, evidence.Valuation.InputObservations...)
	}
	findings := make([]ReconciliationFinding, 0, len(comparison.Rows))
	for _, row := range comparison.Rows {
		expected, err := monetaryComparisonTotal(comparison, MonetaryRoleE, row.Currency)
		if err != nil {
			return nil, err
		}
		reported, err := monetaryComparisonTotal(comparison, MonetaryRoleP, row.Currency)
		if err != nil {
			return nil, err
		}
		status, reason := monetaryTermFindingStatus(row.EndToEndCostDelta)
		if row.EndToEndCostDelta.Status == MonetaryTermComplete && (expected == nil || reported == nil) {
			status = ReconciliationStatusPartial
			reason = monetaryFindingReason(MonetaryReasonAmountUnavailable)
		}
		if row.EndToEndCostDelta.Status == MonetaryTermMissing {
			switch {
			case expected == nil && reported != nil:
				status, reason = ReconciliationStatusMissingLocal, monetaryFindingReason(row.EndToEndCostDelta.Reason)
			case reported == nil && expected != nil:
				status, reason = ReconciliationStatusMissingProvider, monetaryFindingReason(row.EndToEndCostDelta.Reason)
			case expected != nil && reported != nil:
				status, reason = ReconciliationStatusPartial, monetaryFindingReason(row.EndToEndCostDelta.Reason)
			default:
				status = ReconciliationStatusPartial
			}
		}
		finding := ReconciliationFinding{
			ID: "end-to-end:" + row.Currency, Scope: scope, Currency: row.Currency,
			Status: status, Reason: reason,
			Expected: expected, Reported: reported,
			ValuationIDs:          append([]string(nil), valuationIDs...),
			SourceObservationRefs: append([]metering.ObservationRef(nil), sourceRefs...),
		}
		findings = append(findings, finding)
	}
	sortReconciliationFindings(findings)
	return findings, nil
}

func monetaryComparisonTotal(comparison MonetaryDiscrepancyComparison, role MonetaryDiscrepancyRole, currency string) (*MonetaryExactAmount, error) {
	for _, evidence := range comparison.Valuations {
		if evidence.Role != role {
			continue
		}
		for _, total := range evidence.Valuation.Totals {
			normalized, err := economics.NormalizeCurrency(total.Currency)
			if err != nil {
				return nil, fmt.Errorf("%w: %s total currency: %v", ErrReconciliationAggregateInvalid, role, err)
			}
			if normalized != currency {
				continue
			}
			rat, exact, err := monetaryTotalRat(total)
			if err != nil {
				return nil, fmt.Errorf("%w: %s total: %v", ErrReconciliationAggregateInvalid, role, err)
			}
			if !exact {
				return nil, nil
			}
			amount, err := newMonetaryExactAmount(currency, rat)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrReconciliationAggregateOverflow, err)
			}
			return &amount, nil
		}
		return nil, nil
	}
	return nil, nil
}

// monetaryFindingReason carries the Task 12.2 typed reason into the shared
// reconciliation finding vocabulary without losing its exact value.
func monetaryFindingReason(reason MonetaryDiscrepancyReason) ReconciliationComparisonReason {
	return ReconciliationComparisonReason(reason)
}

// reconciliationFindingReasonKnown reports membership in the deliberate
// finding/diagnostic reason union retained by schema version 2: the Task 12.1
// comparison vocabulary plus the Task 12.2 monetary vocabulary. The union
// exists because ReconciliationFindingsFromMonetaryComparison preserves the
// typed monetary reason (for example missing_p) in this shared field, so the
// narrower ReconciliationComparisonReason.IsKnown must not gate it. Both member
// vocabularies keep their own IsKnown gate, so no unenumerated value passes.
func reconciliationFindingReasonKnown(reason ReconciliationComparisonReason) bool {
	return reason.IsKnown() || MonetaryDiscrepancyReason(reason).IsKnown()
}

func monetaryTermFindingStatus(term MonetaryDiscrepancyTerm) (ReconciliationComparisonStatus, ReconciliationComparisonReason) {
	switch term.Status {
	case MonetaryTermComplete:
		if term.Amount != nil {
			if rat, err := term.Amount.Rat(); err == nil && rat.Sign() == 0 {
				return ReconciliationStatusMatched, ReconciliationReasonNone
			}
		}
		return ReconciliationStatusDiscrepant, ReconciliationReasonNone
	case MonetaryTermIncomparable:
		return ReconciliationStatusIncomparable, monetaryFindingReason(term.Reason)
	case MonetaryTermMissing:
		switch term.Reason {
		case MonetaryReasonMissingE:
			return ReconciliationStatusMissingLocal, monetaryFindingReason(term.Reason)
		case MonetaryReasonMissingP:
			return ReconciliationStatusMissingProvider, monetaryFindingReason(term.Reason)
		default:
			return ReconciliationStatusMissingLocal, monetaryFindingReason(term.Reason)
		}
	default:
		return ReconciliationStatusPartial, monetaryFindingReason(term.Reason)
	}
}

// AggregateReconciliationFindings applies the tolerance policy to every
// comparable finding, retains all classified evidence and produces
// deterministic per-scope/currency/unit rows. Gross absolute discrepancy sums
// absolute deltas without offsetting; the signed net is reported separately.
func AggregateReconciliationFindings(policy ReconciliationTolerancePolicy, findings []ReconciliationFinding) (ReconciliationAggregate, error) {
	if err := policy.Validate(); err != nil {
		return ReconciliationAggregate{}, err
	}
	if len(findings) > MaxReconciliationAggregateFindings {
		return ReconciliationAggregate{}, fmt.Errorf("%w: findings=%d max=%d", ErrReconciliationAggregateBoundExceeded, len(findings), MaxReconciliationAggregateFindings)
	}
	result := ReconciliationAggregate{Policy: policy.Ref}
	classified := make([]ReconciliationFinding, 0, len(findings))
	for i := range findings {
		finding, err := classifyReconciliationFinding(policy, findings[i])
		if err != nil {
			return ReconciliationAggregate{}, fmt.Errorf("%w: finding %d: %w", ErrReconciliationAggregateInvalid, i, err)
		}
		classified = append(classified, finding)
	}
	sortReconciliationFindings(classified)
	result.Findings = classified
	rows, err := buildReconciliationAggregateRows(classified)
	if err != nil {
		return ReconciliationAggregate{}, err
	}
	result.Rows = rows
	return result, nil
}

func classifyReconciliationFinding(policy ReconciliationTolerancePolicy, in ReconciliationFinding) (ReconciliationFinding, error) {
	if err := validateReconciliationFinding(in); err != nil {
		return ReconciliationFinding{}, err
	}
	out := cloneReconciliationFinding(in)
	switch out.Status {
	case ReconciliationStatusMatched, ReconciliationStatusDiscrepant, ReconciliationStatusWithinTolerance:
		if out.Expected == nil || out.Reported == nil {
			out.EvaluatedStatus = ReconciliationStatusPartial
			out.EvaluationReason = ReconciliationReasonTolerancePolicyMissing
			return out, nil
		}
		target := ReconciliationToleranceTarget{
			Unit: out.Unit, Currency: out.Currency,
			Component: out.Component, SchemaID: out.SchemaID, Context: out.Context,
		}
		evaluation, err := EvaluateReconciliationTolerance(policy, target, *out.Expected, *out.Reported)
		if err != nil {
			return ReconciliationFinding{}, err
		}
		out.Evaluation = &evaluation
		out.EvaluatedStatus = evaluation.Status
		out.EvaluationReason = evaluation.Reason
		if evaluation.Status == ReconciliationStatusMatched && reconciliationFindingEstimated(out) {
			evaluation.Status = ReconciliationStatusWithinTolerance
			evaluation.Reason = ReconciliationReasonEstimatedNotExact
			out.EvaluatedStatus = evaluation.Status
			out.EvaluationReason = evaluation.Reason
		}
		return out, nil
	default:
		out.EvaluatedStatus = out.Status
		out.EvaluationReason = out.Reason
		return out, nil
	}
}

func reconciliationFindingEstimated(finding ReconciliationFinding) bool {
	return reconciliationQualityNonObserved(finding.LocalQuality) || reconciliationQualityNonObserved(finding.ProviderQuality)
}

func reconciliationQualityNonObserved(quality string) bool {
	return quality != "" && quality != metering.QualityObserved
}

func validateReconciliationFinding(finding ReconciliationFinding) error {
	if !validEconomicIdentity(finding.ID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("finding id required")
	}
	if finding.Scope != "" && !validEconomicIdentity(finding.Scope, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("finding scope is not a bounded identity")
	}
	unitKey := finding.Currency
	if unitKey == "" {
		unitKey = finding.Unit
	}
	if !validEconomicIdentity(unitKey, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("finding currency or unit key required")
	}
	if finding.Direction != "" && !finding.Direction.IsKnown() {
		return fmt.Errorf("unknown flow direction %q", finding.Direction)
	}
	for name, value := range map[string]string{
		"finding component": finding.Component, "finding schema": finding.SchemaID, "finding context": finding.Context,
	} {
		if value != "" && !validEconomicIdentity(value, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("%s is not a bounded identity", name)
		}
	}
	if !reconciliationFindingStatusKnown(finding.Status) {
		return fmt.Errorf("unknown finding status %q", finding.Status)
	}
	for name, quality := range map[string]string{"local quality": finding.LocalQuality, "provider quality": finding.ProviderQuality} {
		if !reconciliationQualityKnown(quality) {
			return fmt.Errorf("unknown %s %q", name, quality)
		}
	}
	for name, amount := range map[string]*MonetaryExactAmount{"expected": finding.Expected, "reported": finding.Reported} {
		if amount == nil {
			continue
		}
		if _, err := amount.Rat(); err != nil {
			return fmt.Errorf("%s amount: %w", name, err)
		}
		if amount.Currency != unitKey {
			return fmt.Errorf("%s amount unit %q does not match finding unit %q", name, amount.Currency, unitKey)
		}
	}
	for i, ref := range finding.SourceObservationRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("source observation ref %d: %v", i, err)
		}
	}
	for i, id := range finding.ValuationIDs {
		if !validEconomicIdentity(id, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("valuation id %d is not a bounded identity", i)
		}
	}
	return nil
}

func reconciliationFindingStatusKnown(status ReconciliationComparisonStatus) bool {
	switch status {
	case ReconciliationStatusMatched, ReconciliationStatusWithinTolerance, ReconciliationStatusDiscrepant,
		ReconciliationStatusPartial, ReconciliationStatusIncomparable,
		ReconciliationStatusMissingLocal, ReconciliationStatusMissingProvider, ReconciliationStatusConflict:
		return true
	default:
		return false
	}
}

func reconciliationQualityKnown(quality string) bool {
	switch quality {
	case "", metering.QualityObserved, metering.QualityEstimated, metering.QualityUnavailable,
		metering.QualityNotApplicable, metering.QualityUnknown:
		return true
	default:
		return false
	}
}

type reconciliationAggregateKey struct {
	Scope    string
	Currency string
	Unit     string
}

type reconciliationRowAccumulator struct {
	gross             *big.Rat
	discrepant        *big.Rat
	net               *big.Rat
	counts            map[ReconciliationComparisonStatus]int
	missingIDs        []string
	incomparableIDs   []string
	conflictIDs       []string
	affected          int
	estimatedAffected int
}

func buildReconciliationAggregateRows(findings []ReconciliationFinding) ([]ReconciliationAggregateRow, error) {
	accumulators := make(map[reconciliationAggregateKey]*reconciliationRowAccumulator)
	for _, finding := range findings {
		key := reconciliationAggregateKey{Scope: finding.Scope, Currency: finding.Currency, Unit: finding.Unit}
		accumulator, ok := accumulators[key]
		if !ok {
			accumulator = &reconciliationRowAccumulator{
				gross: new(big.Rat), discrepant: new(big.Rat), net: new(big.Rat),
				counts: make(map[ReconciliationComparisonStatus]int),
			}
			accumulators[key] = accumulator
		}
		accumulator.counts[finding.EvaluatedStatus]++
		switch finding.EvaluatedStatus {
		case ReconciliationStatusMissingLocal, ReconciliationStatusMissingProvider:
			accumulator.missingIDs = append(accumulator.missingIDs, finding.ID)
		case ReconciliationStatusIncomparable:
			accumulator.incomparableIDs = append(accumulator.incomparableIDs, finding.ID)
		case ReconciliationStatusConflict:
			accumulator.conflictIDs = append(accumulator.conflictIDs, finding.ID)
		}
		if finding.Evaluation == nil || finding.Evaluation.SignedDelta == nil || finding.Evaluation.AbsoluteDelta == nil {
			continue
		}
		signed, err := finding.Evaluation.SignedDelta.Rat()
		if err != nil {
			return nil, fmt.Errorf("%w: signed delta: %v", ErrReconciliationAggregateInvalid, err)
		}
		absolute, err := finding.Evaluation.AbsoluteDelta.Rat()
		if err != nil {
			return nil, fmt.Errorf("%w: absolute delta: %v", ErrReconciliationAggregateInvalid, err)
		}
		if accumulator.net, err = addAggregateRat(accumulator.net, signed); err != nil {
			return nil, err
		}
		if accumulator.gross, err = addAggregateRat(accumulator.gross, absolute); err != nil {
			return nil, err
		}
		if absolute.Sign() != 0 {
			accumulator.affected++
			if reconciliationFindingEstimated(finding) {
				accumulator.estimatedAffected++
			}
		}
		if finding.EvaluatedStatus == ReconciliationStatusDiscrepant {
			if accumulator.discrepant, err = addAggregateRat(accumulator.discrepant, absolute); err != nil {
				return nil, err
			}
		}
	}
	if len(accumulators) > MaxReconciliationAggregateRows {
		return nil, fmt.Errorf("%w: rows=%d max=%d", ErrReconciliationAggregateBoundExceeded, len(accumulators), MaxReconciliationAggregateRows)
	}
	keys := make([]reconciliationAggregateKey, 0, len(accumulators))
	for key := range accumulators {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Scope != keys[j].Scope {
			return keys[i].Scope < keys[j].Scope
		}
		if keys[i].Currency != keys[j].Currency {
			return keys[i].Currency < keys[j].Currency
		}
		return keys[i].Unit < keys[j].Unit
	})
	rows := make([]ReconciliationAggregateRow, 0, len(keys))
	for _, key := range keys {
		accumulator := accumulators[key]
		unitKey := key.Currency
		if unitKey == "" {
			unitKey = key.Unit
		}
		gross, err := aggregateExactAmount(unitKey, accumulator.gross)
		if err != nil {
			return nil, err
		}
		discrepant, err := aggregateExactAmount(unitKey, accumulator.discrepant)
		if err != nil {
			return nil, err
		}
		net, err := aggregateExactAmount(unitKey, accumulator.net)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ReconciliationAggregateRow{
			Scope: key.Scope, Currency: key.Currency, Unit: key.Unit,
			GrossAbsoluteDiscrepancy:      &gross,
			DiscrepantAbsoluteDiscrepancy: &discrepant,
			NetSignedDiscrepancy:          &net,
			AffectedCount:                 accumulator.affected,
			EstimatedAffectedCount:        accumulator.estimatedAffected,
			StatusCounts:                  reconciliationStatusCounts(accumulator.counts),
			MissingIDs:                    canonicalReconciliationStrings(accumulator.missingIDs),
			IncomparableIDs:               canonicalReconciliationStrings(accumulator.incomparableIDs),
			ConflictIDs:                   canonicalReconciliationStrings(accumulator.conflictIDs),
		})
	}
	return rows, nil
}

func addAggregateRat(current, delta *big.Rat) (*big.Rat, error) {
	sum := new(big.Rat).Add(current, delta)
	if err := boundedRat(sum); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReconciliationAggregateOverflow, err)
	}
	return sum, nil
}

func aggregateExactAmount(unit string, value *big.Rat) (MonetaryExactAmount, error) {
	amount, err := newMonetaryExactAmount(unit, value)
	if err != nil {
		return MonetaryExactAmount{}, fmt.Errorf("%w: %v", ErrReconciliationAggregateOverflow, err)
	}
	return amount, nil
}

var reconciliationStatusCountOrder = []ReconciliationComparisonStatus{
	ReconciliationStatusMatched,
	ReconciliationStatusWithinTolerance,
	ReconciliationStatusDiscrepant,
	ReconciliationStatusPartial,
	ReconciliationStatusIncomparable,
	ReconciliationStatusMissingLocal,
	ReconciliationStatusMissingProvider,
	ReconciliationStatusConflict,
}

func reconciliationStatusCounts(counts map[ReconciliationComparisonStatus]int) []ReconciliationStatusCount {
	out := make([]ReconciliationStatusCount, 0, len(counts))
	for _, status := range reconciliationStatusCountOrder {
		if count := counts[status]; count != 0 {
			out = append(out, ReconciliationStatusCount{Status: status, Count: count})
		}
	}
	return out
}

func canonicalReconciliationStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	deduped := out[:0]
	for i, value := range out {
		if i > 0 && value == out[i-1] {
			continue
		}
		deduped = append(deduped, value)
	}
	return deduped
}

func canonicalReconciliationObservationRefs(refs []metering.ObservationRef) []metering.ObservationRef {
	if len(refs) == 0 {
		return nil
	}
	out := append([]metering.ObservationRef(nil), refs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].StoreID != out[j].StoreID {
			return out[i].StoreID < out[j].StoreID
		}
		if out[i].ObservationID != out[j].ObservationID {
			return out[i].ObservationID < out[j].ObservationID
		}
		if out[i].Revision != out[j].Revision {
			return out[i].Revision < out[j].Revision
		}
		return out[i].PayloadHash < out[j].PayloadHash
	})
	deduped := out[:0]
	for i, ref := range out {
		if i > 0 && ref.Equal(out[i-1]) {
			continue
		}
		deduped = append(deduped, ref)
	}
	return deduped
}

func cloneReconciliationFinding(in ReconciliationFinding) ReconciliationFinding {
	out := in
	out.Expected = cloneExactAmount(in.Expected)
	out.Reported = cloneExactAmount(in.Reported)
	out.SourceObservationRefs = canonicalReconciliationObservationRefs(in.SourceObservationRefs)
	out.ValuationIDs = canonicalReconciliationStrings(in.ValuationIDs)
	if in.Evaluation != nil {
		evaluation := *in.Evaluation
		evaluation.Expected = cloneExactAmount(in.Evaluation.Expected)
		evaluation.Reported = cloneExactAmount(in.Evaluation.Reported)
		evaluation.SignedDelta = cloneExactAmount(in.Evaluation.SignedDelta)
		evaluation.AbsoluteDelta = cloneExactAmount(in.Evaluation.AbsoluteDelta)
		evaluation.RelativeDifference = cloneExactAmount(in.Evaluation.RelativeDifference)
		evaluation.Threshold = cloneExactAmount(in.Evaluation.Threshold)
		evaluation.AbsoluteLimit = cloneDecimal(in.Evaluation.AbsoluteLimit)
		evaluation.RelativeLimit = cloneDecimal(in.Evaluation.RelativeLimit)
		out.Evaluation = &evaluation
	}
	return out
}

func sortReconciliationFindings(findings []ReconciliationFinding) {
	sort.SliceStable(findings, func(i, j int) bool {
		left, right := findings[i], findings[j]
		if left.Scope != right.Scope {
			return left.Scope < right.Scope
		}
		if left.Currency != right.Currency {
			return left.Currency < right.Currency
		}
		if left.Unit != right.Unit {
			return left.Unit < right.Unit
		}
		return left.ID < right.ID
	})
}
