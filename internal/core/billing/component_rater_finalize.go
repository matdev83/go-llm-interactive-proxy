package billing

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func (r *ReferenceRater) rateReported(input economics.PostUsageRatingInput, valuation economics.Valuation) (economics.Valuation, error) {
	providerObservations := make([]metering.Observation, 0, len(input.Observations))
	for _, observation := range input.Observations {
		if observation.Origin == metering.OriginProvider && (len(observation.Charges) != 0 || observation.Authority == metering.AuthorityUnavailableClaim) {
			providerObservations = append(providerObservations, observation)
		}
	}
	if len(providerObservations) == 0 {
		valuation.Completeness = economics.CompletenessPartial
		return valuation, fmt.Errorf("%w: provider charge evidence is absent", ErrRatingEvidenceMissing)
	}
	reduced, err := aggregate.ApplyObservations(providerObservations)
	if err != nil {
		valuation.Completeness = economics.CompletenessConflict
		return valuation, fmt.Errorf("%w: reduce provider charges: %v", ErrCoverageInvalid, err)
	}
	var firstErr error
	if len(reduced.PendingSupersedes) != 0 || len(reduced.PendingCoverage) != 0 {
		valuation.Completeness = economics.CompletenessPartial
		valuation.MissingObservations = append(valuation.MissingObservations, reduced.PendingSupersedes...)
		for _, coverage := range reduced.PendingCoverage {
			valuation.CoverageRefs = appendUniqueCoverageRef(valuation.CoverageRefs, coverage)
		}
		firstErr = fmt.Errorf("%w: provider charge replay links are unresolved", ErrRatingEvidenceMissing)
	}
	if len(reduced.UnusablePredecessors) != 0 {
		valuation.Completeness = economics.CompletenessPartial
		valuation.MissingObservations = append(valuation.MissingObservations, reduced.UnusablePredecessors...)
		if firstErr == nil {
			firstErr = fmt.Errorf("%w: provider charge correction predecessor has no usable baseline", ErrRatingEvidenceMissing)
		}
	}
	if len(reduced.Unavailable) != 0 {
		valuation.Completeness = economics.CompletenessPartial
		for _, observation := range reduced.Observations {
			for _, unavailableID := range reduced.Unavailable {
				if observation.ID != unavailableID {
					continue
				}
				if ref, refErr := observation.Ref(observation.Subject.StoreID); refErr == nil {
					valuation.MissingObservations = appendUniqueObservationRef(valuation.MissingObservations, ref)
				}
			}
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%w: provider charge authority is unavailable", ErrRatingEvidenceMissing)
		}
	}
	for _, charge := range reduced.Charges {
		if charge.Complete {
			continue
		}
		valuation.Completeness = economics.CompletenessPartial
		for _, observation := range reduced.Observations {
			if observation.ID != charge.ObservationID || observation.Revision != charge.Revision {
				continue
			}
			if ref, refErr := observation.Ref(observation.Subject.StoreID); refErr == nil {
				valuation.MissingObservations = appendUniqueObservationRef(valuation.MissingObservations, ref)
			}
			break
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%w: provider charge successor has unresolved field evidence", ErrRatingEvidenceMissing)
		}
	}
	incompleteChargeSources := incompleteChargeSources(reduced)
	effectiveObservations := effectiveChargeObservations(reduced, incompleteChargeSources)
	if len(effectiveObservations) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("%w: provider charge evidence is absent", ErrRatingEvidenceMissing)
		}
		if valuation.Completeness == economics.CompletenessComplete {
			valuation.Completeness = economics.CompletenessPartial
		}
		return finalizeValuation(valuation, firstErr)
	}
	nodes, coverage, err := validateChargeCoverageGraph(effectiveObservations)
	if err != nil {
		valuation.Completeness = economics.CompletenessConflict
		return valuation, err
	}
	valuation.CoverageRefs = append(valuation.CoverageRefs, coverage...)
	inclusiveChildren := make(map[string]struct{})
	baseAggregates := make(map[string]struct{})
	for key, node := range nodes {
		if node.charge.Component == nil && node.charge.Kind == metering.ChargeKindAggregate {
			baseAggregates[key] = struct{}{}
		}
	}
	for _, node := range nodes {
		for _, edge := range node.charge.Covers {
			if edge.Relation == metering.CoverageInclusive {
				inclusiveChildren[chargeRefKey(edge.Ref)] = struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(nodes))
	for key := range nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, covered := inclusiveChildren[key]; covered {
			continue
		}
		node := nodes[key]
		line := reportedLine(node)
		if node.charge.Component != nil && hasUncoveredAggregate(node, nodes, baseAggregates) {
			line.Status = economics.RatingLineCoverageIncomplete
			valuation.Lines = append(valuation.Lines, line)
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: component charge %s has no explicit aggregate coverage", ErrCoverageInvalid, node.charge.ChargeItemID)
			}
			continue
		}
		if node.charge.Amount == nil {
			line.Status = economics.RatingLineQuantityIncomplete
			valuation.Lines = append(valuation.Lines, line)
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: provider charge %s has no amount", ErrQuantityIncomplete, node.charge.ChargeItemID)
			}
			continue
		}
		amount, amountErr := node.charge.Amount.ToRat()
		if amountErr != nil {
			line.Status = economics.RatingLineRateUnsupported
			valuation.Lines = append(valuation.Lines, line)
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: provider charge %s amount: %v", ErrRatePrecision, node.charge.ChargeItemID, amountErr)
			}
			continue
		}
		if amountErr := setExactAmount(&line, amount, node.charge.Currency); amountErr != nil {
			line.Status = economics.RatingLineRateUnsupported
			valuation.Lines = append(valuation.Lines, line)
			if firstErr == nil {
				firstErr = amountErr
			}
			continue
		}
		valuation.Lines = append(valuation.Lines, line)
	}
	var totalsErr error
	valuation.Totals, totalsErr = totalsFromReportedLines(valuation.Lines)
	if totalsErr != nil && firstErr == nil {
		firstErr = totalsErr
	}
	if firstErr != nil {
		valuation.Completeness = economics.CompletenessPartial
	}
	return finalizeValuation(valuation, firstErr)
}

// effectiveChargeObservations projects the reducer's charge view back onto
// canonical observations. Source observations remain retained by the
// valuation refs, while only effective charge revisions can become payable
// lines. This is the only place where P crosses from metering reduction into
// monetary rating.
func effectiveChargeObservations(snapshot aggregate.SnapshotV2, incompleteSources map[chargeIdentity]struct{}) []metering.Observation {
	byScope := make(map[string][]metering.ReportedCharge)
	byIdentity := make(map[string]struct{})
	for _, reduced := range snapshot.Charges {
		key := reduced.Scope.Key() + "\x00" + reduced.ObservationID + "\x00" + fmt.Sprint(reduced.Revision)
		if _, incomplete := incompleteSources[chargeIdentity{
			scope: reduced.Scope.Key(), store: reduced.Scope.Subject.StoreID, id: reduced.ObservationID,
			revision: reduced.Revision, item: reduced.Charge.ChargeItemID,
		}]; incomplete {
			continue
		}
		byScope[key] = append(byScope[key], reduced.Charge.Clone())
		byIdentity[key] = struct{}{}
	}
	out := make([]metering.Observation, 0, len(byScope))
	for _, observation := range snapshot.Observations {
		scopeKey := aggregate.ScopeFor(observation).Key() + "\x00" + observation.ID + "\x00" + fmt.Sprint(observation.Revision)
		if _, ok := byIdentity[scopeKey]; !ok {
			continue
		}
		clone := observation.Clone()
		clone.Charges = append([]metering.ReportedCharge(nil), byScope[scopeKey]...)
		out = append(out, clone)
	}
	return out
}

// chargeIdentity keeps completeness diagnostics at the same source scope and
// charge-item granularity as the effective provider view. A provider
// observation may legitimately contain both payable and incomplete charges.
type chargeIdentity struct {
	scope     string
	store, id string
	revision  uint64
	item      string
}

func incompleteChargeSources(snapshot aggregate.SnapshotV2) map[chargeIdentity]struct{} {
	incomplete := make(map[chargeIdentity]struct{})
	for _, reduced := range snapshot.Charges {
		if reduced.Complete {
			continue
		}
		incomplete[chargeIdentity{
			scope: reduced.Scope.Key(), store: reduced.Scope.Subject.StoreID,
			id: reduced.ObservationID, revision: reduced.Revision,
			item: reduced.Charge.ChargeItemID,
		}] = struct{}{}
	}
	pending := make(map[string]struct{}, len(snapshot.PendingSupersedes))
	for _, ref := range snapshot.PendingSupersedes {
		pending[observationRefKey(ref)] = struct{}{}
	}
	unusable := make(map[string]struct{}, len(snapshot.UnusablePredecessors))
	for _, ref := range snapshot.UnusablePredecessors {
		unusable[observationRefKey(ref)] = struct{}{}
	}
	pendingCoverage := make(map[string]struct{}, len(snapshot.PendingCoverage))
	for _, coverage := range snapshot.PendingCoverage {
		pendingCoverage[chargeRefKey(coverage.Ref)+"\x00"+string(coverage.Relation)] = struct{}{}
	}
	for _, observation := range snapshot.Observations {
		scopeKey := aggregate.ScopeFor(observation).Key()
		unresolvedReplacement := false
		if observation.Semantics == metering.SemanticsReplacement {
			for _, ref := range observation.Supersedes {
				refKey := observationRefKey(ref)
				if _, ok := pending[refKey]; ok {
					unresolvedReplacement = true
					break
				}
				if _, ok := unusable[refKey]; ok {
					unresolvedReplacement = true
					break
				}
			}
		}
		for _, charge := range observation.Charges {
			identity := chargeIdentity{scope: scopeKey, store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision, item: charge.ChargeItemID}
			if unresolvedReplacement || (observation.Authority == metering.AuthorityUnavailableClaim && charge.Amount == nil) {
				incomplete[identity] = struct{}{}
			}
			for _, coverage := range charge.Covers {
				if _, ok := pendingCoverage[chargeRefKey(coverage.Ref)+"\x00"+string(coverage.Relation)]; ok {
					incomplete[identity] = struct{}{}
				}
			}
		}
	}
	return incomplete
}

func observationRefKey(ref metering.ObservationRef) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s", ref.StoreID, ref.ObservationID, ref.Revision, ref.PayloadHash)
}

func hasUncoveredAggregate(node chargeNode, nodes map[string]chargeNode, aggregates map[string]struct{}) bool {
	if node.charge.Component == nil || isExplicitAdditiveCharge(node.charge) {
		return false
	}
	for aggregateKey := range aggregates {
		aggregate := nodes[aggregateKey]
		// Aggregate and component charge evidence may arrive in separate
		// observations while still belonging to the same economic subject. A
		// same-observation check would let an unlinked cross-observation
		// aggregate/component pair be added silently.
		if aggregate.observation.NormalizedLineageIdentity() == node.observation.NormalizedLineageIdentity() {
			if relation, covered := aggregateCoverageRelation(aggregate, chargeNodeKey(node.observation, node.charge)); !covered || relation != metering.CoverageInclusive {
				return true
			}
		}
	}
	return false
}

func reportedLine(node chargeNode) economics.LineItem {
	line := economics.LineItem{
		ID:                    "reported:" + node.observation.ID + ":" + node.charge.ChargeItemID,
		RuleID:                "provider_reported",
		ItemID:                node.charge.ChargeItemID,
		Unit:                  "reported",
		RoundingScope:         economics.RoundingScopeLine,
		RoundingPolicy:        defaultRounding,
		Status:                economics.RatingLineProviderReported,
		ChargeKind:            string(node.charge.Kind),
		SourceObservationRefs: nil,
	}
	if node.charge.Component != nil {
		component := node.charge.Component.Clone()
		line.Component = &component
		line.Unit = component.Unit
	} else {
		line.ReportedAggregate = true
	}
	if ref, err := node.observation.Ref(node.observation.Subject.StoreID); err == nil {
		line.SourceObservationRefs = []metering.ObservationRef{ref}
	}
	return line
}

func finalizeValuation(valuation economics.Valuation, ratingErr error) (economics.Valuation, error) {
	if len(valuation.Lines) != 0 {
		sort.Slice(valuation.Lines, func(i, j int) bool { return valuation.Lines[i].ID < valuation.Lines[j].ID })
	}
	if len(valuation.Totals) != 0 {
		sort.Slice(valuation.Totals, func(i, j int) bool { return valuation.Totals[i].Currency < valuation.Totals[j].Currency })
	}
	if ratingErr != nil {
		return valuation, ratingErr
	}
	if err := valuation.Validate(); err != nil {
		return valuation, fmt.Errorf("%w: valuation: %v", ErrRatingInvalid, err)
	}
	return valuation, nil
}

func appendUniqueCoverageRef(refs []metering.ChargeCoverageRef, ref metering.ChargeCoverageRef) []metering.ChargeCoverageRef {
	for _, prior := range refs {
		if prior.Ref == ref.Ref && prior.Relation == ref.Relation {
			return refs
		}
	}
	return append(refs, ref)
}

func totalsFromRats(values map[string]*big.Rat) ([]economics.CurrencyTotal, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]economics.CurrencyTotal, 0, len(keys))
	for _, currency := range keys {
		value := values[currency]
		total := economics.CurrencyTotal{Currency: currency}
		decimal, rational, terminating := decimalFromRat(value)
		if terminating {
			total.Amount = &decimal
		} else {
			total.AmountNumerator = rational.Num().String()
			total.AmountDenominator = rational.Denom().String()
		}
		if rounded, err := roundMoney(value, currency, defaultRounding); err == nil {
			total.RoundedAmount = rounded
		} else {
			return nil, fmt.Errorf("%w: total %s: %v", ErrRatePrecision, currency, err)
		}
		out = append(out, total)
	}
	return out, nil
}

// totalsFromReportedLines uses the same aggregation boundary as derived
// lines. Provider-reported amounts are authoritative at line scope, so their
// already-rounded line values are summed instead of rounding the exact
// aggregate a second time. Any overflow remains a typed precision error.
func totalsFromReportedLines(lines []economics.LineItem) ([]economics.CurrencyTotal, error) {
	byCurrency := make(map[string][]economics.LineItem)
	for _, line := range lines {
		if _, ok := lineAmountRat(line); !ok {
			continue
		}
		if line.RoundedAmount == nil || !line.RoundedAmount.Present || line.RoundedAmount.Currency == "" {
			return nil, fmt.Errorf("%w: provider line %s has no authoritative rounded amount", ErrRatePrecision, line.ID)
		}
		currency := line.RoundedAmount.Currency
		byCurrency[currency] = append(byCurrency[currency], line)
	}
	keys := make([]string, 0, len(byCurrency))
	for currency := range byCurrency {
		keys = append(keys, currency)
	}
	sort.Strings(keys)
	out := make([]economics.CurrencyTotal, 0, len(keys))
	for _, currency := range keys {
		totals, err := totalsFromLines(byCurrency[currency], currency)
		if err != nil {
			return nil, err
		}
		out = append(out, totals...)
	}
	return out, nil
}
