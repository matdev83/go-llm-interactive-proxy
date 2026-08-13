package authoritystore

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func (c *storeCore) reserve(cmd app.ReserveCommand, log MutationLog) (app.ReserveResult, error) {
	working := c.clone()
	out, err := working.reserveSetInPlace(cmd, log)
	if err != nil {
		// A denied reservation has no state mutation to roll back, but the
		// denial decision is durable operator evidence. Preserve only deny rows;
		// successful earlier descriptors remain isolated in the discarded clone.
		for _, decision := range working.decisions[len(c.decisions):] {
			if decision.Row.Outcome != controlplane.AccountingOutcomeDeny {
				continue
			}
			c.decisions = append(c.decisions, decision)
			if decision.Seq >= c.nextDecision {
				c.nextDecision = decision.Seq + 1
			}
		}
		return app.ReserveResult{}, err
	}
	*c = *working
	return out, nil
}

func (c *storeCore) reserveSetInPlace(cmd app.ReserveCommand, log MutationLog) (app.ReserveResult, error) {
	descriptors := reserveDescriptors(cmd)
	if len(descriptors) == 0 {
		return app.ReserveResult{}, fmt.Errorf("%w: reservation set is empty", app.ErrReservationConflict)
	}

	existing := 0
	for _, descriptor := range descriptors {
		if _, ok := c.reservationForDescriptor(descriptor); ok {
			existing++
		}
	}
	if existing > 0 && existing < len(descriptors) {
		return app.ReserveResult{}, fmt.Errorf("%w: reservation set is partially applied", app.ErrReservationConflict)
	}
	if existing == len(descriptors) {
		return c.reserveSetExisting(descriptors), nil
	}

	var result app.ReserveResult
	for i, descriptor := range descriptors {
		one := cmd
		one.Reservations = nil
		one.ReservationKey = descriptor.ReservationKey
		one.RuleID = descriptor.RuleID
		one.RuleType = string(descriptor.Kind)
		one.Dimensions = descriptor.Dimensions
		one.Request = descriptor.Amount
		one.Spend = descriptor.Amount
		one.SourceKey = descriptor.SourceKey
		if one.SourceKey == "" {
			one.SourceKey = descriptor.ReservationKey.String()
		}
		out, err := c.reserveOne(one, log)
		if err != nil {
			return app.ReserveResult{}, &app.RuleReservationError{RuleID: descriptor.RuleID, Err: err}
		}
		if i == 0 {
			result = out
		}
		result.Reservations = append(result.Reservations, app.AdmissionReservation{
			ReservationID:  out.ReservationID,
			RuleID:         descriptor.RuleID,
			ReservedAmount: out.ReservedAmount,
		})
	}
	return result, nil
}

func (c *storeCore) reserveSetExisting(descriptors []app.ReservationDescriptor) app.ReserveResult {
	result := app.ReserveResult{}
	for i, descriptor := range descriptors {
		rec, _ := c.reservationForDescriptor(descriptor)
		item := app.AdmissionReservation{
			ReservationID:  rec.ReservationID,
			RuleID:         rec.RuleID,
			ReservedAmount: rec.ReservedAmount,
		}
		result.Reservations = append(result.Reservations, item)
		if i == 0 {
			result.ReservationID = rec.ReservationID
			result.ReservedAmount = rec.ReservedAmount
			result.RuleID = rec.RuleID
			result.RuleType = rec.RuleType
		}
	}
	return result
}

func (c *storeCore) reserveOne(cmd app.ReserveCommand, log MutationLog) (app.ReserveResult, error) {
	if cmd.SourceKey == "" {
		cmd.SourceKey = cmd.ReservationKey.String()
	}
	if out, ok := c.reserveExisting(cmd.SourceKey); ok {
		return out, nil
	}
	if existing := c.reservations[cmd.ReservationKey.String()]; existing != nil {
		return app.ReserveResult{}, fmt.Errorf("%w: reservation key already belongs to another source", app.ErrReservationConflict)
	}
	if err := c.ensureWritable(); err != nil {
		return app.ReserveResult{}, err
	}
	if cmd.EstimateOnly {
		return app.ReserveResult{
			Applied:       false,
			ReservationID: cmd.ReservationKey.String(),
			RuleID:        cmd.RuleID,
			RuleType:      cmd.RuleType,
		}, nil
	}

	row, key, ok := c.matchLimitRow(cmd.RuleID, cmd.Dimensions, cmd.At)
	if !ok {
		return app.ReserveResult{}, wrapUnavailable("reserve", "matching limit not found")
	}

	amount := reserveAmount(cmd)
	if amount.Value == 0 {
		return app.ReserveResult{}, fmt.Errorf("%w: empty reserve amount", app.ErrReservationConflict)
	}
	if err := validateRowAmount(row, amount); err != nil {
		return app.ReserveResult{}, err
	}
	if cmd.RuleType == "" {
		cmd.RuleType = row.RuleType
	}

	if c.cfg.Backing.StrictCapable() {
		remaining := row.Limit - row.Consumed - row.Reserved
		if amount.Value > remaining {
			c.appendDecision(log, mutationSnapshot(cmd.Correlation, cmd.Scope, cmd.Dimensions), row, cmd.ReservationKey.String(), "", controlplane.AccountingOutcomeDeny, capacityReason(cmd.RuleType), controlplane.AccountingAuthoritySourceAuthoritative, controlplane.AccountingSettlementUnavailable, amount, 0, 0, 0)
			return app.ReserveResult{}, &app.ReservationCapacityError{
				Requested: amount,
				Remaining: domain.Amount{Unit: amount.Unit, Value: max(0, remaining), Currency: amount.Currency},
			}
		}
	}

	row.Reserved += amount.Value
	row.Remaining = max(0, row.Limit-row.Consumed-row.Reserved)
	c.limits[key] = row
	log.CaptureLimitUpdate(key, row)

	// The reservation ID is the reservation key string: deterministic per
	// logical request so repeated admission for the same (request, attempt,
	// rule) tuple converges on one reservation idempotently.
	record := &reservationRecord{
		ReservationKey: cmd.ReservationKey.String(),
		LimitRowKey:    key,
		SourceKey:      cmd.SourceKey,
		ReservationID:  cmd.ReservationKey.String(),
		RuleID:         cmd.RuleID,
		RuleType:       cmd.RuleType,
		Dimensions:     cmd.Dimensions,
		Request:        cmd.Request,
		Spend:          cmd.Spend,
		Authority:      cmd.Authority,
		Applied:        true,
		ReservedAmount: amount,
		CreatedAt:      cmd.At,
	}
	c.reservations[record.ReservationKey] = record
	c.resBySource[cmd.SourceKey] = record.ReservationKey
	log.CaptureReservationUpsert(record.ReservationKey, record)
	c.appendDecision(log, mutationSnapshot(cmd.Correlation, cmd.Scope, cmd.Dimensions), row, record.ReservationID, mutationDecisionSourceKey(cmd.SourceKey, "reserve"), controlplane.AccountingOutcomeReserve, "reserved", controlplane.AccountingAuthoritySourceReserved, controlplane.AccountingSettlementPending, amount, 0, 0, 0)

	return app.ReserveResult{
		Applied:        true,
		ReservationID:  record.ReservationID,
		ReservedAmount: amount,
		RuleID:         cmd.RuleID,
		RuleType:       cmd.RuleType,
	}, nil
}

func capacityReason(ruleType string) string {
	switch domain.RuleKind(ruleType) {
	case domain.RuleKindRate:
		return "rate_limited"
	case domain.RuleKindBudget, domain.RuleKindSpendCap:
		return "budget_exceeded"
	default:
		return "quota_exceeded"
	}
}

func (c *storeCore) reserveExisting(sourceKey string) (app.ReserveResult, bool) {
	if sourceKey == "" {
		return app.ReserveResult{}, false
	}
	reservationKey, ok := c.resBySource[sourceKey]
	if !ok {
		return app.ReserveResult{}, false
	}
	rec := c.reservations[reservationKey]
	if rec == nil {
		return app.ReserveResult{}, false
	}
	return app.ReserveResult{
		Applied:        false,
		ReservationID:  rec.ReservationID,
		ReservedAmount: rec.ReservedAmount,
		RuleID:         rec.RuleID,
		RuleType:       rec.RuleType,
	}, true
}
