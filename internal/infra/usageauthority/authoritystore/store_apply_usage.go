package authoritystore

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// applyUsage applies final usage/cost to matched accounting windows WITHOUT
// requiring a reservation (requirement 7.7). For each rule ID in cmd.RuleIDs it
// matches the live limit row (advancing expired windows via advanceWindow),
// selects the actual amount per rule unit (money rule -> FinalCost; token rule
// -> per-unit usage; request rule -> RequestCount), adds it to row.Consumed,
// recomputes row.Remaining, captures the limit update, and appends an advisory
// decision. No reservation record is created. Idempotent via cmd.SourceKey:
// replays return Applied=false before mutating anything.
func (c *storeCore) applyUsage(cmd app.ApplyUsageCommand, log MutationLog) (app.ApplyUsageResult, error) {
	working := c.clone()
	out, err := working.applyUsageInPlace(cmd, log)
	if err != nil {
		return app.ApplyUsageResult{}, err
	}
	*c = *working
	return out, nil
}

func (c *storeCore) applyUsageInPlace(cmd app.ApplyUsageCommand, log MutationLog) (app.ApplyUsageResult, error) {
	if err := c.ensureWritable(); err != nil {
		return app.ApplyUsageResult{}, err
	}
	snapshot := enrichCommandSnapshot(commandSnapshot{
		Correlation: cmd.Correlation,
		Scope:       scopeSnapshotFromDimensionsWithFallback(cmd.Scope, cmd.Dimensions),
	})
	if len(cmd.RuleIDs) == 0 {
		return app.ApplyUsageResult{}, fmt.Errorf("%w: advisory rule set is empty", app.ErrReservationConflict)
	}
	applied := make([]string, 0, len(cmd.RuleIDs))
	for _, ruleID := range cmd.RuleIDs {
		row, key, ok := c.matchLimitRow(ruleID, cmd.Dimensions, cmd.At)
		if !ok {
			return app.ApplyUsageResult{}, wrapUnavailable("apply_usage", "matching limit not found")
		}
		measurementAuthority := cmd.MeasurementAuthority
		if measurementAuthority.Usage == "" && measurementAuthority.Cost == "" {
			measurementAuthority = app.MeasurementAuthority{
				Usage:                    cmd.Authority,
				Cost:                     cmd.Authority,
				AuthoritativeCostPresent: cmd.CostPresent,
			}
			if measurementAuthority.Cost == domain.AuthorityLevelAuthoritative && !measurementAuthority.AuthoritativeCostPresent {
				measurementAuthority.Cost = domain.AuthorityLevelEstimated
			}
		}
		effectiveAuthority := measurementAuthority.ForUnit(domain.AmountUnit(row.Unit))
		actual := enforceableAmount(limitRowIsMoney(row), advisoryTokenAmount(row, cmd), cmd.FinalCost, domain.Amount{}, row.Currency)
		if err := validateRowAmount(row, actual); err != nil {
			return app.ApplyUsageResult{}, err
		}
		factKey := unreservedUsageFactKey(cmd.SourceKey, ruleID)
		previous, hasPrevious := c.unreservedUsageFacts[factKey]
		if !hasPrevious && cmd.SourceKey != "" {
			// Facts written by an older store version are represented only in
			// applyUsageBySrc. Preserve their replay idempotency after upgrade;
			// new writes always populate the richer fact map below.
			if _, seen := c.applyUsageBySrc[cmd.SourceKey]; seen {
				continue
			}
		}
		if hasPrevious && usageFactPrecedence(effectiveAuthority, cmd.Kind) < usageFactPrecedence(previous.Authority, previous.Kind) {
			continue
		}
		if hasPrevious && usageFactPrecedence(effectiveAuthority, cmd.Kind) == usageFactPrecedence(previous.Authority, previous.Kind) &&
			previous.Amount.Unit == actual.Unit && previous.Amount.Value == actual.Value &&
			strings.EqualFold(previous.Amount.Currency, actual.Currency) && previous.LimitRowKey == key {
			continue
		}

		if hasPrevious && previous.LimitRowKey != "" && previous.LimitRowKey != key {
			if previousRow := c.limits[previous.LimitRowKey]; previousRow != nil {
				previousRow.Consumed = maxInt64(0, previousRow.Consumed-previous.Amount.Value)
				previousRow.Remaining = maxInt64(0, previousRow.Limit-previousRow.Consumed-previousRow.Reserved)
				log.CaptureLimitUpdate(previous.LimitRowKey, previousRow)
			}
		}
		delta := actual.Value
		if hasPrevious && previous.LimitRowKey == key {
			delta -= previous.Amount.Value
		}
		row.Consumed = maxInt64(0, row.Consumed+delta)
		row.Remaining = maxInt64(0, row.Limit-row.Consumed-row.Reserved)
		c.limits[key] = row
		log.CaptureLimitUpdate(key, row)
		applied = append(applied, ruleID)
		fact := unreservedUsageFact{
			SourceKey: cmd.SourceKey, RuleID: ruleID, LimitRowKey: key,
			Amount: actual, Authority: effectiveAuthority, MeasurementAuthority: measurementAuthority, Kind: cmd.Kind, At: cmd.At,
		}
		c.appendDecision(log, snapshot, row, "", advisoryDecisionSourceKey(fact), controlplane.AccountingOutcomeReconcile, "advisory_usage", authoritySourceForUsage(effectiveAuthority), controlplane.AccountingSettlementSettled, actual, 0, 0, 0)
		if cmd.SourceKey != "" {
			c.unreservedUsageFacts[factKey] = fact
			log.CaptureUnreservedUsageFact(factKey, fact)
		}
	}
	if cmd.SourceKey != "" {
		c.applyUsageBySrc[cmd.SourceKey] = struct{}{}
	}
	return app.ApplyUsageResult{Applied: len(applied) > 0, RuleIDs: applied}, nil
}

func usageFactPrecedence(authority domain.AuthorityLevel, kind app.SettlementKind) int {
	return authorityRank(authority)*100 + app.SettlementSequence(kind, authority)
}

func unreservedUsageFactKey(sourceKey, ruleID string) string {
	if sourceKey == "" {
		return ""
	}
	return sourceKey + "\x1f" + ruleID
}

func authorityRank(authority domain.AuthorityLevel) int {
	switch authority {
	case domain.AuthorityLevelAuthoritative:
		return 3
	case domain.AuthorityLevelEstimated:
		return 2
	default:
		return 0
	}
}

func authoritySourceForUsage(authority domain.AuthorityLevel) controlplane.AccountingAuthoritySource {
	switch authority {
	case domain.AuthorityLevelAuthoritative:
		return controlplane.AccountingAuthoritySourceAuthoritative
	case domain.AuthorityLevelEstimated:
		return controlplane.AccountingAuthoritySourceEstimated
	case domain.AuthorityLevelUnavailable:
		return controlplane.AccountingAuthoritySourceUnavailable
	default:
		return controlplane.AccountingAuthoritySourceAdvisory
	}
}

// advisoryDecisionSourceKey builds a per-fact decision source key for
// unreserved usage. The decisions table enforces UNIQUE(store_id, source_key),
// so authority upgrades or changed final amounts sharing one logical source
// need a distinct ledger key. The prefix before the first unit separator is
// still the logical source used to hydrate applyUsageBySrc after restart.
func advisoryDecisionSourceKey(fact unreservedUsageFact) string {
	logical := fact.SourceKey
	if logical == "" {
		logical = fact.RuleID
	}
	return logical + "\x1f" + fact.RuleID + "\x1f" + string(fact.Authority) + "\x1f" + string(fact.Kind) + "\x1f" + fact.LimitRowKey + "\x1f" + fact.Amount.String()
}
