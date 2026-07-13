package authoritystore

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func (c *storeCore) settle(cmd app.SettleCommand, log MutationLog) (app.SettleResult, error) {
	working := c.clone()
	out, err := working.settleSetInPlace(cmd, log)
	if err != nil {
		return app.SettleResult{}, err
	}
	*c = *working
	return out, nil
}

func (c *storeCore) settleSetInPlace(cmd app.SettleCommand, log MutationLog) (app.SettleResult, error) {
	descriptors := settleDescriptors(cmd)
	if len(descriptors) == 0 {
		return app.SettleResult{}, fmt.Errorf("%w: settlement set is empty", app.ErrReservationConflict)
	}
	var result app.SettleResult
	for i, descriptor := range descriptors {
		one := cmd
		one.Reservations = nil
		one.ReservationKey = descriptor.Reservation.ReservationKey
		one.ReservationID = descriptor.Reservation.ReservationID
		one.RuleID = descriptor.Reservation.RuleID
		one.FinalUsage = descriptor.FinalUsage
		one.FinalCost = descriptor.FinalCost
		one.EstimatedUsage = descriptor.EstimatedUsage
		one.EstimatedCost = descriptor.EstimatedCost
		one.ReservedUsage = descriptor.Reservation.Amount
		if descriptor.MeasurementAuthority.Usage != "" || descriptor.MeasurementAuthority.Cost != "" || descriptor.Authority != "" {
			one.MeasurementAuthority = descriptor.MeasurementAuthority
			one.Authority = descriptor.Authority
		}
		one.MeasurementAuthority = settlementAuthorityForCommand(one)
		one.Authority = one.MeasurementAuthority.ForUnit(descriptor.Reservation.Amount.Unit)
		one.SourceKey = descriptor.SourceKey
		sequence := descriptor.Sequence
		if sequence <= 0 {
			sequence = one.SettlementKey.Sequence
		}
		one.SettlementKey = domain.SettlementKey{ReservationKey: descriptor.Reservation.ReservationKey, Sequence: sequence}
		if one.SourceKey == "" {
			one.SourceKey = descriptor.Reservation.SourceKey
		}
		if one.SourceKey == "" {
			one.SourceKey = one.SettlementKey.String() + "|" + string(one.Kind)
		}
		out, err := c.settleOne(one, log)
		if err != nil {
			return app.SettleResult{}, err
		}
		if i == 0 {
			result = out
			continue
		}
		result.Applied = result.Applied || out.Applied
		result.Mutations = append(result.Mutations, out.Mutations...)
	}
	return result, nil
}

func (c *storeCore) settleOne(cmd app.SettleCommand, log MutationLog) (app.SettleResult, error) {
	if cmd.SourceKey == "" {
		cmd.SourceKey = cmd.SettlementKey.String() + "|" + string(cmd.Kind)
	}
	if out, ok := c.settleExisting(cmd.SourceKey); ok {
		return out, nil
	}
	if err := c.ensureWritable(); err != nil {
		return app.SettleResult{}, err
	}
	rec, ok := c.reservations[cmd.ReservationKey.String()]
	if !ok {
		return app.SettleResult{}, fmt.Errorf("%w: reservation not found", app.ErrReservationConflict)
	}
	row, key, ok := c.reservationRow(rec)
	if !ok {
		return app.SettleResult{}, wrapUnavailable("settle", "matching limit not found")
	}
	// A reservation that is already settled but reached with a NEW source key
	// is an authoritative re-settlement: apply an adjustment instead of a
	// permanent no-op (requirement 7.6, 8.4-8.6). Replays of an already-applied
	// source key are caught by settleExisting above.
	if rec.Settled {
		if !measurementAuthorityUpgrade(rec.SettledAuthority, settlementAuthorityForCommand(cmd), rec.ReservedAmount.Unit) {
			return app.SettleResult{}, fmt.Errorf("%w: reservation already settled", app.ErrDuplicateSettlement)
		}
		return c.authoritativeResettle(cmd, rec, row, key, log)
	}

	reservedAmount := cmd.ReservedUsage
	if reservedAmount.Unit == "" {
		reservedAmount = rec.ReservedAmount
	}
	if err := validateRowAmount(row, reservedAmount); err != nil {
		return app.SettleResult{}, err
	}
	actual := settleActual(rec, cmd)
	if err := validateRowAmount(row, actual); err != nil {
		return app.SettleResult{}, err
	}
	released := int64(0)
	overage := int64(0)
	if actual.Value < reservedAmount.Value {
		released = reservedAmount.Value - actual.Value
	} else if actual.Value > reservedAmount.Value {
		overage = actual.Value - reservedAmount.Value
	}
	adjustment := released - overage

	row.Consumed += actual.Value
	row.Reserved = maxInt64(0, row.Reserved-reservedAmount.Value)
	row.Adjustment += adjustment
	row.Remaining = maxInt64(0, row.Limit-row.Consumed-row.Reserved)
	c.limits[key] = row
	log.CaptureLimitUpdate(key, row)

	rec.Settled = true
	rec.SettledAt = cmd.At
	rec.SettlementKind = cmd.Kind
	rec.SettledAuthority = settlementAuthorityForCommand(cmd)
	rec.SettledActual = actual
	rec.SettlementSources = appendUniqueString(rec.SettlementSources, cmd.SourceKey)
	c.settleBySrc[cmd.SourceKey] = rec.ReservationKey
	c.reservations[rec.ReservationKey] = rec
	log.CaptureReservationUpsert(rec.ReservationKey, rec)

	c.appendDecision(log, mutationSnapshot(cmd.Correlation, cmd.Scope, rec.Dimensions), row, rec.ReservationID, mutationDecisionSourceKey(cmd.SourceKey, "settle"), controlplane.AccountingOutcomeReconcile, "reconciled", controlplane.AccountingAuthoritySourceReconciled, settlementStateForCommand(cmd.Kind), actual, released, overage, adjustment)

	mutation := app.SettlementMutation{
		RuleID:          rec.RuleID,
		ReservationID:   rec.ReservationID,
		ReleasedDelta:   domain.Amount{Unit: actual.Unit, Value: released, Currency: actual.Currency},
		OverageDelta:    domain.Amount{Unit: actual.Unit, Value: overage, Currency: actual.Currency},
		AdjustmentDelta: domain.Amount{Unit: actual.Unit, Value: adjustment, Currency: actual.Currency},
	}
	return app.SettleResult{
		Applied:         true,
		ReservationID:   rec.ReservationID,
		ReleasedDelta:   mutation.ReleasedDelta,
		OverageDelta:    mutation.OverageDelta,
		AdjustmentDelta: mutation.AdjustmentDelta,
		Mutations:       []app.SettlementMutation{mutation},
	}, nil
}

// authoritativeResettle applies a later authoritative final usage/cost as an
// adjustment to a prior (typically estimated) settlement. The prior settled
// actual is preserved in evidence (its decision row stays in history) and the
// adjustment is recorded as a new decision with a distinct reason code so the
// durable ledger keeps both rows. The delta is applied to the live limit row's
// Consumed and Adjustment counters; a zero delta still records an authoritative
// decision without moving counters.
func (c *storeCore) authoritativeResettle(cmd app.SettleCommand, rec *reservationRecord, row *controlplane.AccountingLimitStatusRow, key string, log MutationLog) (app.SettleResult, error) {
	newActual := settleActual(rec, cmd)
	if err := validateRowAmount(row, newActual); err != nil {
		return app.SettleResult{}, err
	}
	priorActual := rec.SettledActual
	delta := newActual.Value - priorActual.Value
	released := int64(0)
	overage := int64(0)
	if delta < 0 {
		released = -delta
	} else if delta > 0 {
		overage = delta
	}
	adjustment := released - overage

	if delta != 0 && row != nil {
		row.Consumed += delta
		row.Adjustment += adjustment
		row.Remaining = maxInt64(0, row.Limit-row.Consumed-row.Reserved)
		c.limits[key] = row
		log.CaptureLimitUpdate(key, row)
	}

	rec.SettledActual = newActual
	rec.SettlementKind = cmd.Kind
	rec.SettledAuthority = mergeMeasurementAuthority(rec.SettledAuthority, settlementAuthorityForCommand(cmd))
	rec.SettlementSources = appendUniqueString(rec.SettlementSources, cmd.SourceKey)
	c.settleBySrc[cmd.SourceKey] = rec.ReservationKey
	c.reservations[rec.ReservationKey] = rec
	log.CaptureReservationUpsert(rec.ReservationKey, rec)

	// Distinct source key (per authoritative source) so the durable decision
	// ledger does not collide with the prior "reconciled" row or with later
	// authoritative adjustments for the same reservation.
	decisionSourceKey := rec.ReservationID + "|authoritative_adjustment|" + cmd.SourceKey
	c.appendDecision(log, mutationSnapshot(cmd.Correlation, cmd.Scope, rec.Dimensions), row, rec.ReservationID, decisionSourceKey, controlplane.AccountingOutcomeReconcile, "authoritative_adjustment", controlplane.AccountingAuthoritySourceReconciled, settlementStateForCommand(cmd.Kind), newActual, released, overage, adjustment)

	mutation := app.SettlementMutation{
		RuleID:          rec.RuleID,
		ReservationID:   rec.ReservationID,
		ReleasedDelta:   domain.Amount{Unit: newActual.Unit, Value: released, Currency: newActual.Currency},
		OverageDelta:    domain.Amount{Unit: newActual.Unit, Value: overage, Currency: newActual.Currency},
		AdjustmentDelta: domain.Amount{Unit: newActual.Unit, Value: adjustment, Currency: newActual.Currency},
	}
	return app.SettleResult{
		Applied:         true,
		ReservationID:   rec.ReservationID,
		ReleasedDelta:   mutation.ReleasedDelta,
		OverageDelta:    mutation.OverageDelta,
		AdjustmentDelta: mutation.AdjustmentDelta,
		Mutations:       []app.SettlementMutation{mutation},
	}, nil
}

func (c *storeCore) settleExisting(sourceKey string) (app.SettleResult, bool) {
	if sourceKey == "" {
		return app.SettleResult{}, false
	}
	reservationKey, ok := c.settleBySrc[sourceKey]
	if !ok {
		return app.SettleResult{}, false
	}
	rec := c.reservations[reservationKey]
	if rec == nil {
		return app.SettleResult{}, false
	}
	return app.SettleResult{
		Applied:       false,
		ReservationID: rec.ReservationID,
	}, true
}

func (c *storeCore) reservationRow(rec *reservationRecord) (*controlplane.AccountingLimitStatusRow, string, bool) {
	if rec == nil {
		return nil, "", false
	}
	if rec.LimitRowKey != "" {
		if row := c.limits[rec.LimitRowKey]; row != nil {
			return row, rec.LimitRowKey, true
		}
	}
	for key, row := range c.limits {
		if row.RuleID == rec.RuleID && ScopeDimensionsMatch(row.Scope, rec.Dimensions) &&
			correlationFieldMatches(row.Correlation.BackendID, valueString(rec.Dimensions.Backend)) &&
			correlationFieldMatches(row.Correlation.Model, valueString(rec.Dimensions.Model)) {
			return row, key, true
		}
	}
	return nil, "", false
}
