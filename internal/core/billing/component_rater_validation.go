package billing

import (
	"fmt"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func validateRatingRuleSet(rules []economics.RatingRule) error {
	for i, rule := range rules {
		if rule.Component == nil {
			continue
		}
		for j := i + 1; j < len(rules); j++ {
			other := rules[j]
			if other.Component == nil || !componentDomainsOverlap(*rule.Component, *other.Component) {
				continue
			}
			if !conditionsOverlap(rule.Conditions, other.Conditions) {
				continue
			}
			// A more-specific conditional rule is an intentional override of a
			// generic rule. Equal specificity would make replay selection depend
			// on source ordering and is rejected at publication.
			if ruleSpecificity(rule) == ruleSpecificity(other) {
				return fmt.Errorf("%w: rules %q and %q overlap for %s", economics.ErrRatingRuleOverlap, rule.ID, other.ID, rule.Component.CanonicalKey())
			}
		}
	}
	return nil
}

func conditionsOverlap(left, right []economics.QualifierCondition) bool {
	values := make(map[string]string, len(left)+len(right))
	for _, condition := range left {
		values[condition.Name] = condition.Value
	}
	for _, condition := range right {
		if prior, exists := values[condition.Name]; exists && prior != condition.Value {
			return false
		}
		values[condition.Name] = condition.Value
	}
	return true
}

// componentDomainsOverlap answers whether two component selectors can match
// one concrete measure. A selector with no value for a dimension is a
// wildcard; this is intentionally different from ruleComponentMatches,
// which answers one-way runtime matching. Publication must catch equal-
// specificity overlaps even when one rule is split across dimensions and the
// other across qualifier conditions.
func componentDomainsOverlap(left, right metering.ComponentKey) bool {
	if left.Direction != right.Direction || left.Component != right.Component || left.Unit != right.Unit || left.SchemaID != right.SchemaID {
		return false
	}
	values := make(map[string]string, len(left.Dimensions)+len(right.Dimensions))
	for _, dimension := range left.Dimensions {
		values[dimension.Name] = dimension.Value
	}
	for _, dimension := range right.Dimensions {
		if prior, exists := values[dimension.Name]; exists && prior != dimension.Value {
			return false
		}
		values[dimension.Name] = dimension.Value
	}
	return true
}

type chargeNode struct {
	key         string
	observation metering.Observation
	charge      metering.ReportedCharge
}

func validateChargeCoverageGraph(observations []metering.Observation) (map[string]chargeNode, []metering.ChargeCoverageRef, error) {
	// Reuse the canonical metering graph checks first. The billing resolver
	// then adds its stricter closed-graph and additive aggregate checks below.
	if err := metering.ValidateCoverageGraph(observations); err != nil {
		return nil, nil, invalidCoveragef("%v", err)
	}
	nodes := make(map[string]chargeNode)
	var refs []metering.ChargeCoverageRef
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			key := chargeNodeKey(observation, charge)
			if _, exists := nodes[key]; exists {
				return nil, nil, invalidCoveragef("duplicate charge identity %s", key)
			}
			nodes[key] = chargeNode{key: key, observation: observation, charge: charge}
			refs = append(refs, charge.Covers...)
		}
	}
	relations := make(map[string]metering.CoverageRelation)
	inclusiveParents := make(map[string]int)
	graph := make(map[string][]string)
	for parentKey, node := range nodes {
		for _, edge := range node.charge.Covers {
			if edge.Ref.StoreID != node.observation.Subject.StoreID {
				return nil, nil, invalidCoveragef("cross-store coverage reference %s", edge.Ref.StoreID)
			}
			childKey := chargeRefKey(edge.Ref)
			child, exists := nodes[childKey]
			if !exists {
				return nil, nil, invalidCoveragef("coverage target %s is unavailable", childKey)
			}
			if prior, exists := relations[childKey]; exists && prior != edge.Relation {
				return nil, nil, invalidCoveragef("child %s is both inclusive and additive", childKey)
			}
			relations[childKey] = edge.Relation
			if edge.Relation == metering.CoverageInclusive {
				inclusiveParents[childKey]++
				if inclusiveParents[childKey] > 1 {
					return nil, nil, invalidCoveragef("child %s has ambiguous inclusive parents", childKey)
				}
			} else if child.charge.Component != nil && node.charge.Component == nil && !isExplicitAdditiveCharge(child.charge) && !isExplicitAdditiveCharge(node.charge) {
				return nil, nil, invalidCoveragef("aggregate %s additively overlaps component %s", parentKey, childKey)
			}
			graph[parentKey] = append(graph[parentKey], childKey)
		}
	}
	state := make(map[string]uint8, len(nodes))
	var visit func(string) error
	visit = func(key string) error {
		switch state[key] {
		case 1:
			return invalidCoveragef("coverage cycle at %s", key)
		case 2:
			return nil
		}
		state[key] = 1
		for _, child := range graph[key] {
			if err := visit(child); err != nil {
				return err
			}
		}
		state[key] = 2
		return nil
	}
	keys := make([]string, 0, len(nodes))
	for key := range nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := visit(key); err != nil {
			return nil, nil, err
		}
	}
	if err := validateAggregateComponentOverlap(nodes); err != nil {
		return nil, nil, err
	}
	sort.Slice(refs, func(i, j int) bool {
		return chargeRefKey(refs[i].Ref)+string(refs[i].Relation) < chargeRefKey(refs[j].Ref)+string(refs[j].Relation)
	})
	return nodes, refs, nil
}

func invalidCoveragef(format string, args ...any) error {
	values := make([]any, 0, len(args)+2)
	values = append(values, ErrCoverageInvalid, metering.ErrInvalidCoverage)
	values = append(values, args...)
	return fmt.Errorf("%w: %w: "+format, values...)
}

func chargeNodeKey(observation metering.Observation, charge metering.ReportedCharge) string {
	return observation.Subject.StoreID + "\x00" + observation.ID + "\x00" + fmt.Sprint(observation.Revision) + "\x00" + charge.ChargeItemID
}

func chargeRefKey(ref metering.ChargeRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + fmt.Sprint(ref.Revision) + "\x00" + ref.ChargeItemID
}

func isExplicitAdditiveCharge(charge metering.ReportedCharge) bool {
	switch charge.Kind {
	case metering.ChargeKindSurcharge, metering.ChargeKindTax, metering.ChargeKindAdjustment, metering.ChargeKindCredit:
		return true
	default:
		return false
	}
}

func validateAggregateComponentOverlap(nodes map[string]chargeNode) error {
	aggregates := make([]chargeNode, 0, len(nodes))
	for _, node := range nodes {
		if node.charge.Component == nil && node.charge.Kind == metering.ChargeKindAggregate {
			aggregates = append(aggregates, node)
		}
	}
	for childKey, child := range nodes {
		if child.charge.Component == nil || isExplicitAdditiveCharge(child.charge) {
			continue
		}
		for _, aggregate := range aggregates {
			if aggregate.observation.NormalizedLineageIdentity() != child.observation.NormalizedLineageIdentity() {
				continue
			}
			if relation, covered := aggregateCoverageRelation(aggregate, childKey); covered && relation == metering.CoverageInclusive {
				continue
			}
			return invalidCoveragef("aggregate %s additively overlaps component %s", aggregate.key, child.key)
		}
	}
	return nil
}

func aggregateCoverageRelation(aggregate chargeNode, childKey string) (metering.CoverageRelation, bool) {
	for _, edge := range aggregate.charge.Covers {
		if chargeRefKey(edge.Ref) == childKey {
			return edge.Relation, true
		}
	}
	return "", false
}
