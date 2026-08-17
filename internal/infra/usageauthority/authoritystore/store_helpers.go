package authoritystore

import (
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func reserveAmount(cmd app.ReserveCommand) domain.Amount {
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
			ReservationKey: key,
			ReservationID:  cmd.ReservationID,
			Amount:         cmd.ReservedUsage,
			SourceKey:      cmd.SourceKey,
		},
		FinalUsage:           cmd.FinalUsage,
		EstimatedUsage:       cmd.EstimatedUsage,
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
	return nil
}

func mutationDecisionSourceKey(sourceKey, kind string) string { return sourceKey + "|" + kind }

func appendUniqueString(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func normalizedMeasurementAuthority(value app.MeasurementAuthority, fallback domain.AuthorityLevel) app.MeasurementAuthority {
	if value.Usage == "" {
		value.Usage = fallback
	}
	return value
}

func settlementAuthorityForCommand(cmd app.SettleCommand) app.MeasurementAuthority {
	fallback := cmd.Authority
	if cmd.Kind != app.SettlementKindFinal && fallback == domain.AuthorityLevelAuthoritative {
		fallback = domain.AuthorityLevelEstimated
	}
	return normalizedMeasurementAuthority(cmd.MeasurementAuthority, fallback)
}

func measurementAuthorityUpgrade(prior, incoming app.MeasurementAuthority, _ domain.AmountUnit) bool {
	return authorityRank(incoming.Usage) > authorityRank(prior.Usage)
}

func mergeMeasurementAuthority(prior, incoming app.MeasurementAuthority) app.MeasurementAuthority {
	if authorityRank(incoming.Usage) > authorityRank(prior.Usage) {
		prior.Usage = incoming.Usage
	}
	return prior
}

func settleActual(rec *reservationRecord, cmd app.SettleCommand) domain.Amount {
	actual := cmd.FinalUsage
	if !cmd.FinalUsagePresent && actual.Value == 0 {
		actual = cmd.EstimatedUsage
	}
	if actual.Unit == "" && rec != nil {
		actual.Unit = rec.ReservedAmount.Unit
	}
	return actual
}

func enforceableAmount(tokenAmount, fallback domain.Amount) domain.Amount {
	actual := tokenAmount
	if actual.Value == 0 {
		actual = fallback
	}
	return actual
}

func advisoryTokenAmount(row *controlplane.AccountingLimitStatusRow, cmd app.ApplyUsageCommand) domain.Amount {
	unit := domain.AmountUnit(row.Unit)
	if unit == domain.AmountUnitRequests {
		if cmd.RequestCount.Unit != "" {
			return cmd.RequestCount
		}
		return domain.Amount{Unit: unit, Value: 1}
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
