package authoritystore

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func (c *storeCore) release(cmd app.ReleaseCommand, log MutationLog) (app.ReleaseResult, error) {
	working := c.clone()
	out, err := working.releaseSetInPlace(cmd, log)
	if err != nil {
		return app.ReleaseResult{}, err
	}
	*c = *working
	return out, nil
}

func (c *storeCore) releaseSetInPlace(cmd app.ReleaseCommand, log MutationLog) (app.ReleaseResult, error) {
	descriptors := releaseDescriptors(cmd)
	if len(descriptors) == 0 {
		return app.ReleaseResult{}, fmt.Errorf("%w: release set is empty", app.ErrReservationConflict)
	}
	var result app.ReleaseResult
	for i, descriptor := range descriptors {
		one := cmd
		one.Reservations = nil
		one.ReservationKey = descriptor.Reservation.ReservationKey
		one.ReservationID = descriptor.Reservation.ReservationID
		one.RuleID = descriptor.Reservation.RuleID
		one.Amount = descriptor.Reservation.Amount
		one.SourceKey = descriptor.SourceKey
		sequence := descriptor.Sequence
		if sequence <= 0 {
			sequence = one.ReleaseKey.Sequence
		}
		one.ReleaseKey = domain.ReleaseKey{ReservationKey: descriptor.Reservation.ReservationKey, Sequence: sequence}
		if one.SourceKey == "" {
			one.SourceKey = descriptor.Reservation.SourceKey
		}
		if one.SourceKey == "" {
			one.SourceKey = one.ReleaseKey.String() + "|" + string(one.Kind)
		}
		out, err := c.releaseOne(one, log)
		if err != nil {
			return app.ReleaseResult{}, err
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

func (c *storeCore) releaseOne(cmd app.ReleaseCommand, log MutationLog) (app.ReleaseResult, error) {
	if cmd.SourceKey == "" {
		cmd.SourceKey = cmd.ReleaseKey.String() + "|" + string(cmd.Kind)
	}
	if out, ok := c.releaseExisting(cmd.SourceKey); ok {
		return out, nil
	}
	if err := c.ensureWritable(); err != nil {
		return app.ReleaseResult{}, err
	}
	rec, ok := c.reservations[cmd.ReservationKey.String()]
	if !ok {
		return app.ReleaseResult{}, fmt.Errorf("%w: reservation not found", app.ErrReservationConflict)
	}
	row, key, ok := c.reservationRow(rec)
	if !ok {
		return app.ReleaseResult{}, wrapUnavailable("release", "matching limit not found")
	}
	if rec.Released {
		return app.ReleaseResult{Applied: false, ReservationID: rec.ReservationID}, nil
	}

	amount := cmd.Amount
	if amount.Unit == "" {
		amount = rec.ReservedAmount
	}
	if amount.Value <= 0 {
		return app.ReleaseResult{}, fmt.Errorf("%w: empty release amount", app.ErrReservationConflict)
	}
	if err := validateRowAmount(row, amount); err != nil {
		return app.ReleaseResult{}, err
	}
	released := amount.Value
	released = min(released, rec.ReservedAmount.Value)
	row.Reserved = max(0, row.Reserved-released)
	row.Adjustment += released
	row.Remaining = max(0, row.Limit-row.Consumed-row.Reserved)
	c.limits[key] = row
	log.CaptureLimitUpdate(key, row)

	rec.Released = true
	rec.ReleasedAt = cmd.At
	rec.ReleaseKind = cmd.Kind
	rec.ReleaseSources = appendUniqueString(rec.ReleaseSources, cmd.SourceKey)
	c.releaseBySrc[cmd.SourceKey] = rec.ReservationKey
	c.reservations[rec.ReservationKey] = rec
	log.CaptureReservationUpsert(rec.ReservationKey, rec)

	c.appendDecision(log, mutationSnapshot(cmd.Correlation, cmd.Scope, rec.Dimensions), row, rec.ReservationID, mutationDecisionSourceKey(cmd.SourceKey, "release"), controlplane.AccountingOutcomeReconcile, "released", controlplane.AccountingAuthoritySourceReserved, controlplane.AccountingSettlementReleased, amount, released, 0, released)

	mutation := app.ReleaseMutation{
		RuleID:        rec.RuleID,
		ReservationID: rec.ReservationID,
		ReleasedDelta: domain.Amount{Unit: amount.Unit, Value: released, Currency: amount.Currency},
	}
	return app.ReleaseResult{
		Applied:       true,
		ReservationID: rec.ReservationID,
		ReleasedDelta: mutation.ReleasedDelta,
		Mutations:     []app.ReleaseMutation{mutation},
	}, nil
}

func (c *storeCore) releaseExisting(sourceKey string) (app.ReleaseResult, bool) {
	if sourceKey == "" {
		return app.ReleaseResult{}, false
	}
	reservationKey, ok := c.releaseBySrc[sourceKey]
	if !ok {
		return app.ReleaseResult{}, false
	}
	rec := c.reservations[reservationKey]
	if rec == nil {
		return app.ReleaseResult{}, false
	}
	return app.ReleaseResult{
		Applied:       false,
		ReservationID: rec.ReservationID,
	}, true
}
