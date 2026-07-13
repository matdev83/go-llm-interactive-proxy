package authoritystore

import (
	"fmt"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func reserveAmount(cmd app.ReserveCommand) domain.Amount {
	if cmd.Request.Value != 0 && (cmd.Request.Unit != domain.AmountUnitMoneyNano || cmd.Request.Currency != "") {
		return cmd.Request
	}
	if cmd.Spend.Value != 0 {
		return cmd.Spend
	}
	return cmd.Request
}

func reserveDescriptors(cmd app.ReserveCommand) []app.ReservationDescriptor {
	if len(cmd.Reservations) > 0 {
		return append([]app.ReservationDescriptor(nil), cmd.Reservations...)
	}
	key := cmd.ReservationKey
	if key.RuleID == "" {
		key.RuleID = cmd.RuleID
	}
	kind := domain.RuleKind(cmd.RuleType)
	if !kind.IsKnown() {
		kind = domain.RuleKindQuota
	}
	amount := reserveAmount(cmd)
	return []app.ReservationDescriptor{{
		RuleID:         cmd.RuleID,
		Kind:           kind,
		Unit:           amount.Unit,
		Currency:       amount.Currency,
		Dimensions:     cmd.Dimensions,
		ReservationKey: key,
		ReservationID:  key.String(),
		Amount:         amount,
		SourceKey:      cmd.SourceKey,
	}}
}

func settleDescriptors(cmd app.SettleCommand) []app.SettlementDescriptor {
	if len(cmd.Reservations) > 0 {
		return append([]app.SettlementDescriptor(nil), cmd.Reservations...)
	}
	key := cmd.ReservationKey
	if key.RuleID == "" {
		key.RuleID = cmd.RuleID
	}
	return []app.SettlementDescriptor{{
		Reservation: app.ReservationDescriptor{
			RuleID:         cmd.RuleID,
			Unit:           cmd.ReservedUsage.Unit,
			Currency:       cmd.ReservedUsage.Currency,
			ReservationKey: key,
			ReservationID:  cmd.ReservationID,
			Amount:         cmd.ReservedUsage,
			SourceKey:      cmd.SourceKey,
		},
		FinalUsage:           cmd.FinalUsage,
		FinalCost:            cmd.FinalCost,
		EstimatedUsage:       cmd.EstimatedUsage,
		EstimatedCost:        cmd.EstimatedCost,
		SourceKey:            cmd.SourceKey,
		Sequence:             cmd.SettlementKey.Sequence,
		Authority:            cmd.Authority,
		MeasurementAuthority: cmd.MeasurementAuthority,
	}}
}

func releaseDescriptors(cmd app.ReleaseCommand) []app.ReleaseDescriptor {
	if len(cmd.Reservations) > 0 {
		return append([]app.ReleaseDescriptor(nil), cmd.Reservations...)
	}
	key := cmd.ReservationKey
	if key.RuleID == "" {
		key.RuleID = cmd.RuleID
	}
	return []app.ReleaseDescriptor{{
		Reservation: app.ReservationDescriptor{
			RuleID:         cmd.RuleID,
			Unit:           cmd.Amount.Unit,
			Currency:       cmd.Amount.Currency,
			ReservationKey: key,
			ReservationID:  cmd.ReservationID,
			Amount:         cmd.Amount,
			SourceKey:      cmd.SourceKey,
		},
		SourceKey: cmd.SourceKey,
		Sequence:  cmd.ReleaseKey.Sequence,
	}}
}

func (c *storeCore) reservationForDescriptor(descriptor app.ReservationDescriptor) (*reservationRecord, bool) {
	sourceKey := descriptor.SourceKey
	if sourceKey != "" {
		if key, ok := c.resBySource[sourceKey]; ok {
			if rec := c.reservations[key]; rec != nil {
				return rec, true
			}
		}
		return nil, false
	}
	key := descriptor.ReservationKey
	if key.RuleID == "" {
		key.RuleID = descriptor.RuleID
	}
	rec, ok := c.reservations[key.String()]
	return rec, ok
}

func validateRowAmount(row *controlplane.AccountingLimitStatusRow, amount domain.Amount) error {
	if row == nil {
		return fmt.Errorf("%w: limit row is nil", app.ErrReservationConflict)
	}
	normalized := amount
	if amount.Unit == "" {
		normalized.Unit = domain.AmountUnit(row.Unit)
	}
	if err := normalized.Validate(); err != nil {
		return fmt.Errorf("%w: %v", app.ErrReservationConflict, err)
	}
	if row.Unit != "" && normalized.Unit != domain.AmountUnit(row.Unit) {
		return fmt.Errorf("%w: amount unit %q does not match limit unit %q", app.ErrReservationConflict, normalized.Unit, row.Unit)
	}
	if row.Currency != "" && normalized.IsMoney() && !strings.EqualFold(row.Currency, normalized.Currency) {
		return fmt.Errorf("%w: amount currency %q does not match limit currency %q", app.ErrReservationConflict, normalized.Currency, row.Currency)
	}
	return nil
}

func mutationDecisionSourceKey(sourceKey, kind string) string {
	return sourceKey + "|" + kind
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

// reservationIsMoney reports whether the reservation enforces a money
// (budget/spend-cap) rule, so settlement consumes cost rather than usage.
// It checks both the reserved amount's unit and the rule type so a money rule
// whose reservation record carries the unit is detected either way.
func reservationIsMoney(rec *reservationRecord) bool {
	if rec == nil {
		return false
	}
	if rec.ReservedAmount.Unit == domain.AmountUnitMoneyNano {
		return true
	}
	switch domain.RuleKind(rec.RuleType) {
	case domain.RuleKindBudget, domain.RuleKindSpendCap:
		return true
	}
	return false
}

func normalizedMeasurementAuthority(value app.MeasurementAuthority, fallback domain.AuthorityLevel) app.MeasurementAuthority {
	if value.Usage == "" && value.Cost == "" {
		return app.MeasurementAuthority{
			Usage:                    fallback,
			Cost:                     fallback,
			AuthoritativeCostPresent: fallback == domain.AuthorityLevelAuthoritative,
		}
	}
	if value.Usage == "" {
		value.Usage = domain.AuthorityLevelEstimated
	}
	if value.Cost == "" {
		value.Cost = domain.AuthorityLevelEstimated
	}
	if value.Cost == domain.AuthorityLevelAuthoritative && !value.AuthoritativeCostPresent {
		value.Cost = domain.AuthorityLevelEstimated
	}
	return value
}

// settlementAuthorityForCommand preserves the legacy Authority field while
// keeping modern partial observations from being promoted accidentally. New
// callers provide MeasurementAuthority explicitly; legacy partial commands
// historically represented an estimate until a final authoritative update.
func settlementAuthorityForCommand(cmd app.SettleCommand) app.MeasurementAuthority {
	if cmd.MeasurementAuthority.Usage != "" || cmd.MeasurementAuthority.Cost != "" {
		return normalizedMeasurementAuthority(cmd.MeasurementAuthority, cmd.Authority)
	}
	fallback := cmd.Authority
	if cmd.Kind != app.SettlementKindFinal && fallback == domain.AuthorityLevelAuthoritative {
		fallback = domain.AuthorityLevelEstimated
	}
	return normalizedMeasurementAuthority(cmd.MeasurementAuthority, fallback)
}

func measurementAuthorityUpgrade(prior, incoming app.MeasurementAuthority, unit domain.AmountUnit) bool {
	incoming = normalizedMeasurementAuthority(incoming, domain.AuthorityLevelEstimated)
	prior = normalizedMeasurementAuthority(prior, domain.AuthorityLevelEstimated)
	if incoming.ForUnit(unit) != domain.AuthorityLevelAuthoritative || prior.ForUnit(unit) == domain.AuthorityLevelAuthoritative {
		return false
	}
	if unit == domain.AmountUnitMoneyNano && !incoming.AuthoritativeCostPresent {
		return false
	}
	return true
}

func mergeMeasurementAuthority(prior, incoming app.MeasurementAuthority) app.MeasurementAuthority {
	incoming = normalizedMeasurementAuthority(incoming, domain.AuthorityLevelEstimated)
	prior = normalizedMeasurementAuthority(prior, domain.AuthorityLevelEstimated)
	if prior.Usage != domain.AuthorityLevelAuthoritative && incoming.Usage == domain.AuthorityLevelAuthoritative {
		prior.Usage = incoming.Usage
	}
	if prior.Cost != domain.AuthorityLevelAuthoritative && incoming.Cost == domain.AuthorityLevelAuthoritative && incoming.AuthoritativeCostPresent {
		prior.Cost = incoming.Cost
	}
	prior.AuthoritativeCostPresent = prior.AuthoritativeCostPresent || incoming.AuthoritativeCostPresent
	return prior
}

// settleActual selects the enforceable amount a settlement consumes for the
// reservation. Money (budget/spend-cap) rules consume cost: cmd.FinalCost, or
// cmd.EstimatedCost when FinalCost is zero/unavailable so an estimated
// settlement still records spend. Token/request rules consume cmd.FinalUsage,
// or cmd.EstimatedUsage for non-final settlements with no final usage yet.
//
// Currency consistency: when the selected money amount carries no currency, the
// reservation's ReservedAmount currency is adopted (then the estimated cost's
// currency as a final fallback) so the deltas are never currency-less.
func settleActual(rec *reservationRecord, cmd app.SettleCommand) domain.Amount {
	isMoney := reservationIsMoney(rec)
	if isMoney {
		actual := cmd.FinalCost
		if !cmd.FinalCostPresent && actual.Value == 0 {
			actual = cmd.EstimatedCost
		}
		if actual.Unit == "" {
			actual.Unit = domain.AmountUnitMoneyNano
		}
		if actual.Currency == "" {
			actual.Currency = rec.ReservedAmount.Currency
			if actual.Currency == "" {
				actual.Currency = cmd.EstimatedCost.Currency
			}
		}
		return actual
	}
	actual := cmd.FinalUsage
	if !cmd.FinalUsagePresent && actual.Value == 0 {
		actual = cmd.EstimatedUsage
	}
	if actual.Unit == "" {
		actual.Unit = rec.ReservedAmount.Unit
	}
	return actual
}

// enforceableAmount selects the money or token/request amount that a settlement
// or advisory usage consumes for a matched window. isMoney picks finalCost
// (money) vs tokenAmount (tokens/requests). When the selected final amount is
// zero, fallback is used so estimated evidence still records spend/usage
// (settle passes its estimates; advisory apply passes a zero fallback since it
// runs after final usage). Money amounts are normalized to the money_nano unit
// and currencyFallback currency (then the fallback's currency as a last
// resort). Token/request amounts are returned as-is.
func enforceableAmount(isMoney bool, tokenAmount, finalCost, fallback domain.Amount, currencyFallback string) domain.Amount {
	if isMoney {
		actual := finalCost
		if actual.Value == 0 {
			actual = fallback
		}
		if actual.Unit == "" {
			actual.Unit = domain.AmountUnitMoneyNano
		}
		if actual.Currency == "" {
			actual.Currency = currencyFallback
			if actual.Currency == "" {
				actual.Currency = fallback.Currency
			}
		}
		return actual
	}
	actual := tokenAmount
	if actual.Value == 0 {
		actual = fallback
	}
	return actual
}

// limitRowIsMoney reports whether a matched live limit row enforces a money
// (budget/spend-cap) rule, so advisory usage consumes cost rather than usage.
// It mirrors reservationIsMoney for the reservation-free applyUsage path,
// checking the row's unit and rule type.
func limitRowIsMoney(row *controlplane.AccountingLimitStatusRow) bool {
	if row == nil {
		return false
	}
	if row.Unit == string(domain.AmountUnitMoneyNano) {
		return true
	}
	switch domain.RuleKind(row.RuleType) {
	case domain.RuleKindBudget, domain.RuleKindSpendCap:
		return true
	}
	return false
}

// advisoryTokenAmount selects the final token/request amount for a non-money
// advisory rule from the per-unit breakdown in ApplyUsageCommand. Request-unit
// rules consume the request count (defaulting to 1); other token units consume
// the matching per-unit reading from cmd.Usage.
func advisoryTokenAmount(row *controlplane.AccountingLimitStatusRow, cmd app.ApplyUsageCommand) domain.Amount {
	unit := domain.AmountUnit(row.Unit)
	if unit == domain.AmountUnitRequests {
		if cmd.RequestCount.Unit != "" {
			return cmd.RequestCount
		}
		return domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}
	}
	if amount, ok := cmd.Usage.AmountForUnit(unit); ok {
		return amount
	}
	return domain.Amount{Unit: unit}
}

func settlementStateForCommand(kind app.SettlementKind) controlplane.AccountingSettlementState {
	switch kind {
	case app.SettlementKindPartial, app.SettlementKindUnavailable, app.SettlementKindCancellation:
		return controlplane.AccountingSettlementUnavailable
	case app.SettlementKindSwallowed, app.SettlementKindLosing:
		return controlplane.AccountingSettlementReleased
	default:
		return controlplane.AccountingSettlementSettled
	}
}

func wrapUnavailable(op, reason string) error {
	return fmt.Errorf("authoritystore %s: %w: %s", op, app.ErrUnavailable, reason)
}
