package authoritystore

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	_ "modernc.org/sqlite"
)

func (s *DurableStore) runReserveTx(ctx context.Context, cmd app.ReserveCommand) (app.ReserveResult, error) {
	out, err := s.runReserveTxOnce(ctx, cmd)
	if !errors.Is(err, errConcurrentReservationCreate) && !errors.Is(err, errConcurrentLimitRowCreate) {
		return out, err
	}
	return s.runReserveTxOnce(ctx, cmd)
}

func (s *DurableStore) runReserveTxOnce(ctx context.Context, cmd app.ReserveCommand) (app.ReserveResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.ReserveResult{}, storeUnavailableError("reserve begin", err)
	}
	core := s.newMutationCore()
	limitKeys := make(map[string]struct{})
	reservationPreImage := make(map[string]string)
	for _, descriptor := range reserveDescriptors(cmd) {
		sourceKey := descriptor.SourceKey
		if sourceKey == "" {
			sourceKey = descriptor.ReservationKey.String()
		}
		loaded, err := s.lockReservationTx(ctx, tx, core, descriptor.ReservationKey.String(), sourceKey)
		if err != nil {
			_ = tx.Rollback()
			return app.ReserveResult{}, storeUnavailableError("reserve load", err)
		}
		mergePreImages(reservationPreImage, loaded)
		_, key, ok := core.configuredLimitRow(descriptor.RuleID, descriptor.Dimensions, cmd.At)
		if ok {
			limitKeys[key] = struct{}{}
		}
	}
	preImage, err := s.lockLimitRowsByKeyTx(ctx, tx, core, limitKeys)
	if err != nil {
		_ = tx.Rollback()
		return app.ReserveResult{}, storeUnavailableError("reserve load", err)
	}
	if err := s.lockStateTx(ctx, tx, core); err != nil {
		_ = tx.Rollback()
		return app.ReserveResult{}, storeUnavailableError("reserve state", err)
	}
	// A reservation row that was absent during the first read can be created by
	// the transaction that held the limit-row lock ahead of us. Reload after
	// both coordination locks are acquired so identical cross-instance retries
	// resolve through reserveExisting before evaluating the now-reduced capacity.
	for _, descriptor := range reserveDescriptors(cmd) {
		sourceKey := descriptor.SourceKey
		if sourceKey == "" {
			sourceKey = descriptor.ReservationKey.String()
		}
		loaded, err := s.lockReservationTx(ctx, tx, core, descriptor.ReservationKey.String(), sourceKey)
		if err != nil {
			_ = tx.Rollback()
			return app.ReserveResult{}, storeUnavailableError("reserve reload", err)
		}
		mergePreImages(reservationPreImage, loaded)
	}
	// A strict-cap denial has no reservation row to lock. Once the stable state
	// row is held, re-check its deterministic decision key so a retry returns
	// the original typed capacity outcome instead of attempting a second insert
	// into the decisions ledger.
	for _, descriptor := range reserveDescriptors(cmd) {
		decisionKey := descriptor.ReservationKey.String() + "|" + capacityReason(string(descriptor.Kind))
		decision, found, err := s.decisionBySourceTx(ctx, tx, decisionKey)
		if err != nil {
			_ = tx.Rollback()
			return app.ReserveResult{}, err
		}
		if found && decision.Row.Outcome == controlplane.AccountingOutcomeDeny &&
			(decision.Row.ReasonCode == "quota_exceeded" || decision.Row.ReasonCode == "rate_limited" || decision.Row.ReasonCode == "budget_exceeded") {
			_ = tx.Rollback()
			return app.ReserveResult{}, reservationCapacityReplay(descriptor, decision)
		}
	}
	log := newRecordingMutationLog()
	out, err := core.reserve(cmd, log)
	if err != nil {
		// A strict-cap denial is a business result, but its decision row is
		// durable evidence. Commit only the denial rows; any successful
		// descriptors captured before the failing descriptor belong to the
		// discarded transactional projection and must not be flushed.
		denyLog := log.denialOnly()
		if !denyLog.isEmpty() {
			if ferr := s.flushInTx(ctx, tx, core, denyLog, preImage, reservationPreImage, nil, false); ferr != nil {
				_ = tx.Rollback()
				return app.ReserveResult{}, s.markFlushFailed(ferr)
			}
			if cerr := tx.Commit(); cerr != nil {
				return app.ReserveResult{}, s.markFlushFailed(storeUnavailableError("reserve denial commit", cerr))
			}
			return app.ReserveResult{}, err
		}
		_ = tx.Rollback()
		return app.ReserveResult{}, err
	}
	if log.isEmpty() {
		_ = tx.Rollback()
		return out, nil
	}
	if ferr := s.flushInTx(ctx, tx, core, log, preImage, reservationPreImage, nil, false); ferr != nil {
		_ = tx.Rollback()
		return app.ReserveResult{}, s.markFlushFailed(ferr)
	}
	if cerr := tx.Commit(); cerr != nil {
		return app.ReserveResult{}, s.markFlushFailed(storeUnavailableError("reserve commit", cerr))
	}
	return out, nil
}

func (s *DurableStore) runSettleTx(ctx context.Context, cmd app.SettleCommand) (app.SettleResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.SettleResult{}, storeUnavailableError("settle begin", err)
	}
	core := s.newMutationCore()
	reservationPreImage := make(map[string]string)
	for _, descriptor := range settleDescriptors(cmd) {
		loaded, err := s.lockReservationTx(ctx, tx, core, descriptor.Reservation.ReservationKey.String(), "")
		if err != nil {
			_ = tx.Rollback()
			return app.SettleResult{}, storeUnavailableError("settle load", err)
		}
		mergePreImages(reservationPreImage, loaded)
	}
	limitKeys := make(map[string]struct{})
	for _, rec := range core.reservations {
		if rec.LimitRowKey != "" {
			limitKeys[rec.LimitRowKey] = struct{}{}
			continue
		}
		at := rec.CreatedAt
		if at.IsZero() {
			at = cmd.At
		}
		_, key, ok := core.configuredLimitRow(rec.RuleID, rec.Dimensions, at)
		if ok {
			limitKeys[key] = struct{}{}
		}
	}
	preImage, err := s.lockLimitRowsByKeyTx(ctx, tx, core, limitKeys)
	if err != nil {
		_ = tx.Rollback()
		return app.SettleResult{}, storeUnavailableError("settle load", err)
	}
	if err := s.lockStateTx(ctx, tx, core); err != nil {
		_ = tx.Rollback()
		return app.SettleResult{}, storeUnavailableError("settle state", err)
	}
	log := newRecordingMutationLog()
	out, err := core.settle(cmd, log)
	if err != nil {
		_ = tx.Rollback()
		return app.SettleResult{}, err
	}
	if log.isEmpty() {
		_ = tx.Rollback()
		return out, nil
	}
	if ferr := s.flushInTx(ctx, tx, core, log, preImage, reservationPreImage, nil, false); ferr != nil {
		_ = tx.Rollback()
		return app.SettleResult{}, s.markFlushFailed(ferr)
	}
	if cerr := tx.Commit(); cerr != nil {
		return app.SettleResult{}, s.markFlushFailed(storeUnavailableError("settle commit", cerr))
	}
	return out, nil
}

func (s *DurableStore) runReleaseTx(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.ReleaseResult{}, storeUnavailableError("release begin", err)
	}
	core := s.newMutationCore()
	reservationPreImage := make(map[string]string)
	for _, descriptor := range releaseDescriptors(cmd) {
		loaded, err := s.lockReservationTx(ctx, tx, core, descriptor.Reservation.ReservationKey.String(), "")
		if err != nil {
			_ = tx.Rollback()
			return app.ReleaseResult{}, storeUnavailableError("release load", err)
		}
		mergePreImages(reservationPreImage, loaded)
	}
	limitKeys := make(map[string]struct{})
	for _, rec := range core.reservations {
		if rec.LimitRowKey != "" {
			limitKeys[rec.LimitRowKey] = struct{}{}
			continue
		}
		at := rec.CreatedAt
		if at.IsZero() {
			at = cmd.At
		}
		_, key, ok := core.configuredLimitRow(rec.RuleID, rec.Dimensions, at)
		if ok {
			limitKeys[key] = struct{}{}
		}
	}
	preImage, err := s.lockLimitRowsByKeyTx(ctx, tx, core, limitKeys)
	if err != nil {
		_ = tx.Rollback()
		return app.ReleaseResult{}, storeUnavailableError("release load", err)
	}
	if err := s.lockStateTx(ctx, tx, core); err != nil {
		_ = tx.Rollback()
		return app.ReleaseResult{}, storeUnavailableError("release state", err)
	}
	log := newRecordingMutationLog()
	out, err := core.release(cmd, log)
	if err != nil {
		_ = tx.Rollback()
		return app.ReleaseResult{}, err
	}
	if log.isEmpty() {
		_ = tx.Rollback()
		return out, nil
	}
	if ferr := s.flushInTx(ctx, tx, core, log, preImage, reservationPreImage, nil, false); ferr != nil {
		_ = tx.Rollback()
		return app.ReleaseResult{}, s.markFlushFailed(ferr)
	}
	if cerr := tx.Commit(); cerr != nil {
		return app.ReleaseResult{}, s.markFlushFailed(storeUnavailableError("release commit", cerr))
	}
	return out, nil
}

func (s *DurableStore) runApplyUsageTx(ctx context.Context, cmd app.ApplyUsageCommand) (app.ApplyUsageResult, error) {
	out, err := s.runApplyUsageTxOnce(ctx, cmd)
	if !errors.Is(err, errConcurrentUsageFactMutation) && !errors.Is(err, errConcurrentLimitRowCreate) {
		return out, err
	}
	return s.runApplyUsageTxOnce(ctx, cmd)
}

func (s *DurableStore) runApplyUsageTxOnce(ctx context.Context, cmd app.ApplyUsageCommand) (app.ApplyUsageResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.ApplyUsageResult{}, storeUnavailableError("apply_usage begin", err)
	}
	core := s.newMutationCore()
	limitKeys := make(map[string]struct{})
	factKeys := make([]string, 0, len(cmd.RuleIDs))
	factPreImage := make(map[string]string)
	for _, ruleID := range cmd.RuleIDs {
		factKey := unreservedUsageFactKey(cmd.SourceKey, ruleID)
		factKeys = append(factKeys, factKey)
		raw, found, err := s.lockUsageFactTx(ctx, tx, core, factKey)
		if err != nil {
			_ = tx.Rollback()
			return app.ApplyUsageResult{}, storeUnavailableError("apply_usage load", err)
		}
		if found {
			factPreImage[factKey] = raw
		}
		if previous, ok := core.unreservedUsageFacts[factKey]; ok && previous.LimitRowKey != "" {
			limitKeys[previous.LimitRowKey] = struct{}{}
		}
		_, key, ok := core.configuredLimitRow(ruleID, cmd.Dimensions, cmd.At)
		if ok {
			limitKeys[key] = struct{}{}
		}
	}
	preImage, err := s.lockLimitRowsByKeyTx(ctx, tx, core, limitKeys)
	if err != nil {
		_ = tx.Rollback()
		return app.ApplyUsageResult{}, storeUnavailableError("apply_usage load", err)
	}
	// A missing fact cannot be row-locked. Re-read after the limit locks have
	// serialized same-window writers so a concurrent creator becomes the delta
	// baseline rather than a second full application.
	for _, factKey := range factKeys {
		raw, found, err := s.lockUsageFactTx(ctx, tx, core, factKey)
		if err != nil {
			_ = tx.Rollback()
			return app.ApplyUsageResult{}, storeUnavailableError("apply_usage reload", err)
		}
		if !found {
			delete(factPreImage, factKey)
			continue
		}
		factPreImage[factKey] = raw
		previous := core.unreservedUsageFacts[factKey]
		if previous.LimitRowKey != "" {
			if _, locked := limitKeys[previous.LimitRowKey]; !locked {
				_ = tx.Rollback()
				return app.ApplyUsageResult{}, fmt.Errorf("%w: fact %q references unlocked limit row", errConcurrentUsageFactMutation, factKey)
			}
		}
	}
	for _, ruleID := range cmd.RuleIDs {
		if len(core.limitTemplates[ruleID]) != 0 {
			continue
		}
		factKey := unreservedUsageFactKey(cmd.SourceKey, ruleID)
		if previous, ok := core.unreservedUsageFacts[factKey]; ok {
			if row := core.limits[previous.LimitRowKey]; row != nil {
				core.limitTemplates[ruleID] = append(core.limitTemplates[ruleID], *row)
			}
		}
	}
	if err := s.lockStateTx(ctx, tx, core); err != nil {
		_ = tx.Rollback()
		return app.ApplyUsageResult{}, storeUnavailableError("apply_usage state", err)
	}
	log := newRecordingMutationLog()
	out, err := core.applyUsage(cmd, log)
	if err != nil {
		_ = tx.Rollback()
		return app.ApplyUsageResult{}, err
	}
	if log.isEmpty() {
		_ = tx.Rollback()
		return out, nil
	}
	if ferr := s.flushInTx(ctx, tx, core, log, preImage, nil, factPreImage, false); ferr != nil {
		_ = tx.Rollback()
		return app.ApplyUsageResult{}, s.markFlushFailed(ferr)
	}
	if cerr := tx.Commit(); cerr != nil {
		return app.ApplyUsageResult{}, s.markFlushFailed(storeUnavailableError("apply_usage commit", cerr))
	}
	return out, nil
}

// markFlushFailed identifies transactional flush failures. Mutation methods
// operate on transaction-local projections, so rollback simply discards them.
func (s *DurableStore) markFlushFailed(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", errDurableFlushFailed, err)
}
