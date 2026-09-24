package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Durable cutover coordinator for Task 17.3 subtask B2a (Migration Strategy
// step 6).
//
// The coordinator enters draining, classifies/pins all existing V1 in-flight
// financial operations, prevents new unpinned V1 work, and permits claims
// only for correctly pinned V1 work. Activation proves V1 in-flight is
// drained/completed/classified before v2_active. V2 new work is authorized
// only in v2_active.
//
// Namespaces reuse canonical identities:
//   - customer calls: usage_call_records claim_status != 'processed'
//     (pending/leased) via CustomerSettlementSourceKey.
//   - provider-cost work: provider_cost_work status='pending' joined to sealed
//     legs via ProviderCostSourceKey(CallLegUsageKey).
//   - adjustment work: F4 empty inventory. Synchronous selected-cost,
//     cost-pass-through, and direct adjustments have no durable pending queue:
//     F1 serializes them (commit complete before the drain marker lock or fence
//     after), so historical billing_provider_cost_heads projections are never
//     pending work. Only already-existing incomplete financial_adjustment pins
//     count (via B2a pin counts); classification never invents a pin from a
//     head.
//
// Transition shadow->draining closes the gate before classification so
// concurrent appends cannot slip in. Classification uses bounded deterministic
// batches (ORDER BY + LIMIT/OFFSET) and is idempotent/restart-safe via pin
// replay. Unclassifiable items remain draining with explicit status; completion
// is never invented. Per StoreID isolation holds for marker/pins/heads;
// legacy usage tables are deployment-scoped (single store per database in
// production) and enumerated deployment-wide. All readers are bounded and
// context-aware. SQLite and PostgreSQL share the same queries via bun `?`
// placeholders. Posting-time worker enforcement belongs to B2b; this file
// exposes only narrow claim metadata ports B2b consumes.

func validateCutoverTransitionID(transitionID string) error {
	if strings.TrimSpace(transitionID) == "" {
		return fmt.Errorf("%w: %w: transition identity is required", billing.ErrCutoverCoordinatorInvalid, billing.ErrInvalidRecord)
	}
	if len(transitionID) > 128 {
		return fmt.Errorf("%w: %w: transition identity exceeds 128 bytes", billing.ErrCutoverCoordinatorInvalid, billing.ErrInvalidRecord)
	}
	if strings.TrimSpace(transitionID) != transitionID {
		return fmt.Errorf("%w: %w: transition identity must be trimmed", billing.ErrCutoverCoordinatorInvalid, billing.ErrInvalidRecord)
	}
	if strings.ContainsAny(transitionID, "\x00\r\n") {
		return fmt.Errorf("%w: %w: transition identity contains control characters", billing.ErrCutoverCoordinatorInvalid, billing.ErrInvalidRecord)
	}
	return nil
}

func normalizeCutoverBatchSize(batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = billing.CutoverCoordinatorDefaultBatchSize
	}
	cfg, err := billing.NewCutoverCoordinatorConfig(batchSize)
	if err != nil {
		return 0, err
	}
	return cfg.BatchSize, nil
}

// checkV2NewWorkAuthorized fails closed unless the durable marker is
// v2_active. Shadow capture remains no-post and does not consult this gate.
func (s *DurableStore) CheckV2NewWorkAuthorized(ctx context.Context) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		if errors.Is(err, billing.ErrAccountingCutoverNotFound) {
			return fmt.Errorf("%w: no cutover marker for store %q", billing.ErrCutoverV2NotAuthorized, s.storeID)
		}
		return err
	}
	if !billing.IsV2NewWorkAuthorized(marker.State) {
		return fmt.Errorf("%w: store %q is in %q, V2 requires v2_active",
			billing.ErrCutoverV2NotAuthorized, s.storeID, string(marker.State))
	}
	return nil
}

// GetCutoverClaimMetadata returns the renewable current-marker token B2b
// workers consume (including leases waking after an epoch change).
//
// Pin owner/identity is stable historical ownership; the returned
// version/epoch/state is always the current marker (lease authority), never
// the pin's acquisition snapshot. In draining only classified V1 pins receive
// renewal; in active V1 receives none (fenced) and V2 eligible work receives
// V2. Pre-drain renews V1 only. Missing pins report NotFound (authorized first
// acquisition vs metadata failure is distinguished by the caller); operational,
// cancellation and malformed inputs fail closed and are never swallowed into
// nil/default tokens.
func (s *DurableStore) GetCutoverClaimMetadata(ctx context.Context, kind billing.PostingOperationKind, operationKey string) (billing.CutoverClaimMetadata, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.CutoverClaimMetadata{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.CutoverClaimMetadata{}, err
	}
	if !kind.Valid() {
		return billing.CutoverClaimMetadata{}, fmt.Errorf("%w: %w: unknown operation kind %q", billing.ErrCutoverCoordinatorInvalid, billing.ErrInvalidRecord, string(kind))
	}
	if strings.TrimSpace(operationKey) == "" || strings.TrimSpace(operationKey) != operationKey {
		return billing.CutoverClaimMetadata{}, fmt.Errorf("%w: %w: operation key is required and must be trimmed", billing.ErrCutoverCoordinatorInvalid, billing.ErrInvalidRecord)
	}
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		if errors.Is(err, billing.ErrAccountingCutoverNotFound) {
			// No cutover yet: legacy default. Renewal is the pin itself;
			// callers distinguish first acquisition (NotFound pin) separately.
			pin, perr := s.GetPostingPin(ctx, kind, operationKey)
			if perr != nil {
				return billing.CutoverClaimMetadata{}, perr
			}
			meta := billing.CutoverClaimMetadataForPin(pin)
			if verr := meta.Validate(); verr != nil {
				return billing.CutoverClaimMetadata{}, verr
			}
			return meta, nil
		}
		return billing.CutoverClaimMetadata{}, err
	}
	pin, err := s.GetPostingPin(ctx, kind, operationKey)
	if err != nil {
		return billing.CutoverClaimMetadata{}, err
	}
	// Eligibility: stable owner vs current state. Never invent a token for a
	// fenced owner; callers fail closed.
	switch marker.State {
	case billing.AccountingCutoverV1Active, billing.AccountingCutoverV2Shadow:
		if pin.Owner != billing.PostingOwnerV1 {
			return billing.CutoverClaimMetadata{}, fmt.Errorf("%w: %s owner %q not renewable in %q", billing.ErrPostingOwnershipFence, string(kind), pin.Owner, string(marker.State))
		}
	case billing.AccountingCutoverV1Draining:
		if pin.Owner != billing.PostingOwnerV1 {
			return billing.CutoverClaimMetadata{}, fmt.Errorf("%w: %s owner %q not renewable in draining", billing.ErrPostingOwnershipFence, string(kind), pin.Owner)
		}
	case billing.AccountingCutoverV2Active:
		if pin.Owner == billing.PostingOwnerV1 {
			return billing.CutoverClaimMetadata{}, fmt.Errorf("%w: V1 %s not renewable in v2_active", billing.ErrPostingOwnershipFence, string(kind))
		}
		if pin.Owner != billing.PostingOwnerV2 {
			return billing.CutoverClaimMetadata{}, fmt.Errorf("%w: %s owner %q not renewable in v2_active", billing.ErrPostingOwnershipFence, string(kind), pin.Owner)
		}
	default:
		return billing.CutoverClaimMetadata{}, fmt.Errorf("%w: unknown cutover state %q", billing.ErrCutoverCoordinatorInvalid, string(marker.State))
	}
	tok, err := billing.CutoverClaimTokenForPinAtMarker(pin, marker)
	if err != nil {
		return billing.CutoverClaimMetadata{}, err
	}
	return tok, nil
}

// BeginCutoverDraining closes the gate before classification: it CAS
// transitions v2_shadow->v1_draining first, then classifies in bounded batches.
// Already-draining markers resume classification (restart-safe). Any other
// state fails with ErrAccountingCutoverInvalid.
func (s *DurableStore) BeginCutoverDraining(ctx context.Context, transitionID string) (billing.AccountingCutoverMarker, billing.CutoverDrainStatus, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.AccountingCutoverMarker{}, billing.CutoverDrainStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.AccountingCutoverMarker{}, billing.CutoverDrainStatus{}, err
	}
	if err := validateCutoverTransitionID(transitionID); err != nil {
		return billing.AccountingCutoverMarker{}, billing.CutoverDrainStatus{}, err
	}
	marker, err := s.EnsureAccountingCutover(ctx)
	if err != nil {
		return billing.AccountingCutoverMarker{}, billing.CutoverDrainStatus{}, err
	}
	switch marker.State {
	case billing.AccountingCutoverV1Draining:
		// Gate already closed; resume classification below (restart-safe).
	case billing.AccountingCutoverV2Shadow:
		advanced, err := s.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: marker.Version,
			ExpectedEpoch:   marker.Epoch,
			NextState:       billing.AccountingCutoverV1Draining,
			TransitionID:    transitionID,
		})
		if err != nil {
			// Lost a concurrent CAS race: reload and resume if draining.
			if errors.Is(err, billing.ErrAccountingCutoverFence) || errors.Is(err, billing.ErrAccountingCutoverConflict) {
				current, rerr := s.GetAccountingCutover(ctx)
				if rerr != nil {
					return billing.AccountingCutoverMarker{}, billing.CutoverDrainStatus{}, err
				}
				if current.State == billing.AccountingCutoverV1Draining {
					marker = current
					break
				}
			}
			return billing.AccountingCutoverMarker{}, billing.CutoverDrainStatus{}, err
		}
		marker = advanced
	default:
		return billing.AccountingCutoverMarker{}, billing.CutoverDrainStatus{}, fmt.Errorf(
			"%w: begin draining requires v2_shadow, store %q is in %q",
			billing.ErrAccountingCutoverInvalid, s.storeID, string(marker.State))
	}
	status, err := s.ClassifyCutoverDraining(ctx, billing.CutoverCoordinatorDefaultBatchSize)
	if err != nil {
		return marker, billing.CutoverDrainStatus{}, err
	}
	// Return the post-classification marker (stable in draining) with status.
	current, err := s.GetAccountingCutover(ctx)
	if err != nil {
		return marker, billing.CutoverDrainStatus{}, err
	}
	status.State = current.State
	status.MarkerVersion = current.Version
	status.MarkerEpoch = current.Epoch
	return current, status, nil
}

// ClassifyCutoverDraining enumerates pending/leased customer calls and
// provider-cost work in bounded deterministic batches and pins V1 using
// canonical keys/marker epoch. Adjustment inventory is explicitly empty (F4):
// synchronous adjustments have no durable pending queue, so no head scan
// occurs. Unclassifiable items remain draining with explicit status;
// completion is never invented.
func (s *DurableStore) ClassifyCutoverDraining(ctx context.Context, batchSize int) (billing.CutoverDrainStatus, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	batch, err := normalizeCutoverBatchSize(batchSize)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	if marker.State != billing.AccountingCutoverV1Draining {
		return billing.CutoverDrainStatus{}, fmt.Errorf(
			"%w: classify requires v1_draining, store %q is in %q",
			billing.ErrAccountingCutoverInvalid, s.storeID, string(marker.State))
	}
	var unclassifiable []billing.CutoverUnclassifiableItem
	collect := func(item billing.CutoverUnclassifiableItem) {
		if len(unclassifiable) < billing.CutoverCoordinatorMaxUnclassifiableSamples {
			unclassifiable = append(unclassifiable, item)
		}
	}
	if err := s.classifyCustomerCalls(ctx, marker, batch, collect); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	if err := s.classifyProviderCostWork(ctx, marker, batch, collect); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	// F2A: legacy open exposures created before admission-time pinning are
	// classified/pinned here in bounded batches. Already-pinned admissions
	// refresh to the draining epoch so worker claims match current marker.
	if err := s.classifyOpenExposures(ctx, marker, batch, collect); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	// F2B: monetary provider economic revision work (pending and leased) is
	// classified/pinned with canonical provider operation identity. Evidence-only
	// customer rating, reconciliation, shadow, and no-adapter queues are never
	// pinned here.
	if err := s.classifyEconomicProviderWork(ctx, marker, batch, collect); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	if err := s.classifyAdjustmentHeads(ctx, marker, batch, collect); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	status, err := s.buildCutoverDrainStatus(ctx, marker, unclassifiable)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	return status, nil
}

// CutoverDrainStatus returns the durable precondition snapshot without
// writing pins. Unclassifiable samples come from a bounded dry-run that
// derives (but never acquires) canonical keys.
func (s *DurableStore) CutoverDrainStatus(ctx context.Context) (billing.CutoverDrainStatus, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	unclassifiable := s.probeUnclassifiable(ctx, billing.CutoverCoordinatorDefaultBatchSize)
	return s.buildCutoverDrainStatus(ctx, marker, unclassifiable)
}

// ActivateCutoverV2 CAS transitions v1_draining->v2_active only when durable
// queries prove no pending/leased/uncompleted V1 pins/work across all three
// namespaces. Exact replay with the same transition identity is idempotent
// (race-safe restart/resume).
//
// F1: the marker row is locked (SELECT FOR UPDATE on PostgreSQL, write
// upgrade on SQLite), drain counts and unclassifiable probes run inside the
// SAME transaction, and the version/epoch CAS UPDATE commits before release.
// Concurrent monetary transactions hold the same per-store marker lock, so
// activation either waits for in-flight work to commit (then observes it) or
// the stale poster fences after activation. Drain is never checked outside
// the transition tx.
func (s *DurableStore) ActivateCutoverV2(ctx context.Context, transitionID string) (billing.AccountingCutoverMarker, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if err := ctx.Err(); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if err := validateCutoverTransitionID(transitionID); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	var result billing.AccountingCutoverMarker
	err := withAccountTxErr(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("billingstore: begin cutover activation: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		row, found, err := s.loadAccountingCutoverLocked(ctx, tx)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: store %q has no cutover marker", billing.ErrAccountingCutoverNotFound, s.storeID)
		}
		current, err := accountingCutoverRowToMarker(row)
		if err != nil {
			return err
		}
		// Idempotent replay: already active under the same transition identity.
		if current.State == billing.AccountingCutoverV2Active {
			if current.TransitionID == transitionID {
				result = current
				if err := tx.Commit(); err != nil {
					return fmt.Errorf("billingstore: commit cutover activation replay: %w", err)
				}
				return nil
			}
			return fmt.Errorf(
				"%w: store %q already v2_active under %q",
				billing.ErrAccountingCutoverInvalid, s.storeID, current.TransitionID)
		}
		if current.State != billing.AccountingCutoverV1Draining {
			return fmt.Errorf(
				"%w: activate requires v1_draining, store %q is in %q",
				billing.ErrAccountingCutoverInvalid, s.storeID, string(current.State))
		}
		unclassifiable := s.probeUnclassifiableTx(ctx, tx, billing.CutoverCoordinatorDefaultBatchSize)
		status, err := s.buildCutoverDrainStatusTx(ctx, tx, current, unclassifiable)
		if err != nil {
			return err
		}
		if !status.ReadyForActivation {
			return fmt.Errorf(
				"%w: store %q has customer=%d provider=%d economic=%d adjustment=%d pinned=%d open=%d unclassifiable=%d",
				billing.ErrCutoverDrainBlocked, s.storeID,
				status.Counts.CustomerPending, status.Counts.ProviderPending, status.Counts.EconomicProviderPending, status.Counts.AdjustmentPending,
				status.Counts.V1Pinned, status.Counts.OpenExposures, status.Counts.Unclassifiable)
		}
		req := billing.AccountingCutoverTransition{
			ExpectedVersion: current.Version,
			ExpectedEpoch:   current.Epoch,
			NextState:       billing.AccountingCutoverV2Active,
			TransitionID:    transitionID,
		}
		if err := req.Validate(); err != nil {
			return err
		}
		now := time.Now().UTC().UnixNano()
		next, err := billing.ValidateAccountingCutoverTransition(current, req, now)
		if err != nil {
			return err
		}
		res, err := tx.NewRaw(`UPDATE billing_accounting_cutover SET
				generation = ?, state = ?, active_posting_owner = ?, compatibility_floor = ?,
				version = ?, epoch = ?, transition_id = ?, updated_at_unix = ?
			WHERE store_id = ? AND version = ? AND epoch = ?`,
			next.Generation, string(next.State), next.ActivePostingOwner, next.CompatibilityFloor,
			int64(next.Version), int64(next.Epoch), next.TransitionID, next.UpdatedAtUnix,
			s.storeID, int64(current.Version), int64(current.Epoch)).Exec(ctx)
		if err != nil {
			return fmt.Errorf("billingstore: advance accounting cutover for activation: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("billingstore: activation rows affected: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("%w: concurrent cutover advance for store %q", billing.ErrAccountingCutoverFence, s.storeID)
		}
		result = next
		if err := tx.Commit(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("billingstore: commit cutover activation: %w", err)
		}
		return nil
	})
	if err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	return result, nil
}

func (s *DurableStore) buildCutoverDrainStatus(ctx context.Context, marker billing.AccountingCutoverMarker, unclassifiable []billing.CutoverUnclassifiableItem) (billing.CutoverDrainStatus, error) {
	return s.buildCutoverDrainStatusTx(ctx, s.db, marker, unclassifiable)
}

func (s *DurableStore) countCustomerPending(ctx context.Context) (int, error) {
	return s.countCustomerPendingTx(ctx, s.db)
}

func (s *DurableStore) countProviderPending(ctx context.Context) (int, error) {
	return s.countProviderPendingTx(ctx, s.db)
}

func (s *DurableStore) countV1Pins(ctx context.Context) (pinned, completed int, err error) {
	return s.countV1PinsTx(ctx, s.db)
}

func (s *DurableStore) countAdjustmentPending(ctx context.Context) (int, error) {
	return s.countAdjustmentPendingTx(ctx, s.db)
}

// probeUnclassifiable performs a bounded dry-run deriving canonical keys
// without acquiring pins.
func (s *DurableStore) probeUnclassifiable(ctx context.Context, batch int) []billing.CutoverUnclassifiableItem {
	var out []billing.CutoverUnclassifiableItem
	collect := func(item billing.CutoverUnclassifiableItem) {
		if len(out) < billing.CutoverCoordinatorMaxUnclassifiableSamples {
			out = append(out, item)
		}
	}
	// Customer dry-run: one bounded page.
	type customerRow struct {
		CallID    string `bun:"call_id"`
		AccountID string `bun:"account_id"`
	}
	var customers []customerRow
	if err := s.db.NewRaw(`SELECT call_id, account_id FROM usage_call_records WHERE claim_status <> 'processed' ORDER BY sealed_at, call_id LIMIT ?`, batch).Scan(ctx, &customers); err == nil {
		for _, row := range customers {
			if err := ctx.Err(); err != nil {
				break
			}
			callID, err := billing.ParseBillingCallID(row.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: row.CallID, Reason: "unparseable call id"})
				continue
			}
			if _, err := billing.CustomerPostingOperationKey(row.AccountID, callID); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: row.CallID, Reason: "invalid account/call identity"})
			}
		}
	}
	// Provider dry-run: one bounded page.
	type providerRow struct {
		UsageLegKey string `bun:"usage_leg_key"`
		CallID      string `bun:"call_id"`
	}
	var providers []providerRow
	if err := s.db.NewRaw(`SELECT usage_leg_key, call_id FROM provider_cost_work WHERE status = 'pending' ORDER BY updated_at, usage_leg_key LIMIT ?`, batch).Scan(ctx, &providers); err == nil {
		for _, row := range providers {
			if err := ctx.Err(); err != nil {
				break
			}
			if strings.TrimSpace(row.UsageLegKey) == "" {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.CallID, Reason: "missing leg key"})
				continue
			}
			var payload string
			if err := s.db.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, row.UsageLegKey).Scan(ctx, &payload); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.UsageLegKey, Reason: "missing leg record"})
				continue
			}
			var leg billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(payload), &leg); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.UsageLegKey, Reason: "malformed leg payload"})
				continue
			}
			callID, err := billing.ParseBillingCallID(row.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.UsageLegKey, Reason: "unparseable call id"})
				continue
			}
			accountID := s.accountForCall(ctx, row.CallID)
			subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: s.storeID, AccountID: accountID, ALegID: leg.ALegID, BillingCallID: callID.String(), BLegID: leg.BLegID}
			if _, err := billing.ProviderPostingOperationKey(s.storeID, accountID, callID, subject); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.UsageLegKey, Reason: "invalid leg/account lineage"})
			}
		}
	}
	// F2A open-exposure dry-run: one bounded page. Valid open exposures block
	// via the OpenExposures count, not here; only invalid identities sample.
	type exposureProbeRow struct {
		CallID    string `bun:"call_id"`
		AccountID string `bun:"account_id"`
	}
	var exposures []exposureProbeRow
	if err := s.db.NewRaw(`SELECT call_id, account_id FROM call_exposures WHERE status = 'open' ORDER BY account_id, call_id LIMIT ?`, batch).Scan(ctx, &exposures); err == nil {
		for _, row := range exposures {
			if err := ctx.Err(); err != nil {
				break
			}
			callID, err := billing.ParseBillingCallID(row.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: row.CallID, Reason: "unparseable call id"})
				continue
			}
			if _, err := billing.CustomerPostingOperationKey(row.AccountID, callID); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: row.CallID, Reason: "invalid account/call identity"})
			}
		}
	}
	// F2B economic dry-run: one bounded page of monetary pending/processing
	// state plus unclaimed provider work. Valid monetary work blocks via the
	// EconomicProviderPending count, not here; only invalid lineage samples.
	{
		type economicProbeRow struct {
			PayloadJSON string `bun:"payload_json"`
			WorkID      string `bun:"work_id"`
		}
		var economicRows []economicProbeRow
		if err := s.db.NewRaw(`SELECT w.payload_json, w.work_id FROM billing_economic_work AS w JOIN billing_economic_revision_work_state AS q ON q.store_id = w.store_id AND q.work_id = w.work_id AND q.work_version = w.work_version WHERE w.store_id = ? AND q.store_id = ? AND q.provider_posting = 1 AND q.status IN ('pending', 'processing') ORDER BY w.work_id LIMIT ?`, s.storeID, s.storeID, batch).Scan(ctx, &economicRows); err == nil {
			for _, row := range economicRows {
				if err := ctx.Err(); err != nil {
					break
				}
				var work billing.EconomicRevisionWork
				if err := json.Unmarshal([]byte(row.PayloadJSON), &work); err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "malformed economic payload"})
					continue
				}
				normalized, err := work.Normalize()
				if err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "invalid economic work"})
					continue
				}
				if !billing.IsMonetaryEconomicRevisionWork(normalized) {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "non-monetary economic state"})
					continue
				}
				if _, _, _, err := billing.MonetaryEconomicPostingKey(s.storeID, normalized); err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "invalid economic lineage"})
				}
			}
		}
		var unclaimedRows []economicProbeRow
		if err := s.db.NewRaw(`SELECT w.payload_json, w.work_id FROM billing_economic_work AS w LEFT JOIN billing_economic_revision_work_state AS q ON q.store_id = w.store_id AND q.work_id = w.work_id AND q.work_version = w.work_version WHERE w.store_id = ? AND w.kind = ? AND w.status = 'pending' AND q.id IS NULL ORDER BY w.work_id LIMIT ?`, s.storeID, economicRevisionWorkKind(billing.EconomicQueueProvider), batch).Scan(ctx, &unclaimedRows); err == nil {
			for _, row := range unclaimedRows {
				if err := ctx.Err(); err != nil {
					break
				}
				var work billing.EconomicRevisionWork
				if err := json.Unmarshal([]byte(row.PayloadJSON), &work); err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "malformed economic payload"})
					continue
				}
				normalized, err := work.Normalize()
				if err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "invalid economic work"})
					continue
				}
				if !billing.IsMonetaryEconomicRevisionWork(normalized) {
					continue
				}
				if _, _, _, err := billing.MonetaryEconomicPostingKey(s.storeID, normalized); err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "invalid economic lineage"})
				}
			}
		}
	}
	// Adjustment dry-run: F4 empty inventory. Synchronous adjustments have no
	// durable pending queue (F1 serializes them), so historical heads are never
	// probed here; only real incomplete pins (counted separately) can block.
	_ = batch
	if out == nil {
		out = []billing.CutoverUnclassifiableItem{}
	}
	return out
}

func (s *DurableStore) accountForCall(ctx context.Context, callID string) string {
	var accountID string
	if err := s.db.NewRaw(`SELECT account_id FROM usage_call_records WHERE call_id = ?`, callID).Scan(ctx, &accountID); err == nil && strings.TrimSpace(accountID) != "" {
		return strings.TrimSpace(accountID)
	}
	// F2A fallback: terminal provider leg may materialize before closure;
	// derive the owning account from the admitted exposure.
	var exposureAccount string
	if err := s.db.NewRaw(`SELECT account_id FROM call_exposures WHERE call_id = ? LIMIT 1`, callID).Scan(ctx, &exposureAccount); err == nil && strings.TrimSpace(exposureAccount) != "" {
		return strings.TrimSpace(exposureAccount)
	}
	if strings.TrimSpace(accountID) != "" {
		return strings.TrimSpace(accountID)
	}
	return strings.TrimSpace(exposureAccount)
}

func (s *DurableStore) accountForCallTx(ctx context.Context, q bun.IDB, callID string) string {
	var accountID string
	if err := q.NewRaw(`SELECT account_id FROM usage_call_records WHERE call_id = ?`, callID).Scan(ctx, &accountID); err == nil && strings.TrimSpace(accountID) != "" {
		return strings.TrimSpace(accountID)
	}
	var exposureAccount string
	if err := q.NewRaw(`SELECT account_id FROM call_exposures WHERE call_id = ? LIMIT 1`, callID).Scan(ctx, &exposureAccount); err == nil && strings.TrimSpace(exposureAccount) != "" {
		return strings.TrimSpace(exposureAccount)
	}
	if strings.TrimSpace(accountID) != "" {
		return strings.TrimSpace(accountID)
	}
	return strings.TrimSpace(exposureAccount)
}

// probeUnclassifiableTx is the F1 transaction-scoped variant used by
// ActivateCutoverV2 so drain probes observe the same snapshot as the locked
// marker and counts. Logic mirrors probeUnclassifiable with q in place of
// s.db.
func (s *DurableStore) probeUnclassifiableTx(ctx context.Context, q bun.IDB, batch int) []billing.CutoverUnclassifiableItem {
	var out []billing.CutoverUnclassifiableItem
	collect := func(item billing.CutoverUnclassifiableItem) {
		if len(out) < billing.CutoverCoordinatorMaxUnclassifiableSamples {
			out = append(out, item)
		}
	}
	type customerRow struct {
		CallID    string `bun:"call_id"`
		AccountID string `bun:"account_id"`
	}
	var customers []customerRow
	if err := q.NewRaw(`SELECT call_id, account_id FROM usage_call_records WHERE claim_status <> 'processed' ORDER BY sealed_at, call_id LIMIT ?`, batch).Scan(ctx, &customers); err == nil {
		for _, row := range customers {
			if err := ctx.Err(); err != nil {
				break
			}
			callID, err := billing.ParseBillingCallID(row.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: row.CallID, Reason: "unparseable call id"})
				continue
			}
			if _, err := billing.CustomerPostingOperationKey(row.AccountID, callID); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: row.CallID, Reason: "invalid account/call identity"})
			}
		}
	}
	type providerRow struct {
		UsageLegKey string `bun:"usage_leg_key"`
		CallID      string `bun:"call_id"`
	}
	var providers []providerRow
	if err := q.NewRaw(`SELECT usage_leg_key, call_id FROM provider_cost_work WHERE status = 'pending' ORDER BY updated_at, usage_leg_key LIMIT ?`, batch).Scan(ctx, &providers); err == nil {
		for _, row := range providers {
			if err := ctx.Err(); err != nil {
				break
			}
			if strings.TrimSpace(row.UsageLegKey) == "" {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.CallID, Reason: "missing leg key"})
				continue
			}
			var payload string
			if err := q.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, row.UsageLegKey).Scan(ctx, &payload); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.UsageLegKey, Reason: "missing leg record"})
				continue
			}
			var leg billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(payload), &leg); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.UsageLegKey, Reason: "malformed leg payload"})
				continue
			}
			callID, err := billing.ParseBillingCallID(row.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.UsageLegKey, Reason: "unparseable call id"})
				continue
			}
			accountID := s.accountForCallTx(ctx, q, row.CallID)
			subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: s.storeID, AccountID: accountID, ALegID: leg.ALegID, BillingCallID: callID.String(), BLegID: leg.BLegID}
			if _, err := billing.ProviderPostingOperationKey(s.storeID, accountID, callID, subject); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.UsageLegKey, Reason: "invalid leg/account lineage"})
			}
		}
	}
	// F2A open-exposure dry-run (tx-scoped): valid open exposures block via
	// OpenExposures count; only invalid identities sample here.
	type exposureProbeRowTx struct {
		CallID    string `bun:"call_id"`
		AccountID string `bun:"account_id"`
	}
	var exposuresTx []exposureProbeRowTx
	if err := q.NewRaw(`SELECT call_id, account_id FROM call_exposures WHERE status = 'open' ORDER BY account_id, call_id LIMIT ?`, batch).Scan(ctx, &exposuresTx); err == nil {
		for _, row := range exposuresTx {
			if err := ctx.Err(); err != nil {
				break
			}
			callID, err := billing.ParseBillingCallID(row.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: row.CallID, Reason: "unparseable call id"})
				continue
			}
			if _, err := billing.CustomerPostingOperationKey(row.AccountID, callID); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: row.CallID, Reason: "invalid account/call identity"})
			}
		}
	}
	// F2B economic dry-run (tx-scoped): mirrors probeUnclassifiable with q.
	{
		type economicProbeRowTx struct {
			PayloadJSON string `bun:"payload_json"`
			WorkID      string `bun:"work_id"`
		}
		var economicRowsTx []economicProbeRowTx
		if err := q.NewRaw(`SELECT w.payload_json, w.work_id FROM billing_economic_work AS w JOIN billing_economic_revision_work_state AS q2 ON q2.store_id = w.store_id AND q2.work_id = w.work_id AND q2.work_version = w.work_version WHERE w.store_id = ? AND q2.store_id = ? AND q2.provider_posting = 1 AND q2.status IN ('pending', 'processing') ORDER BY w.work_id LIMIT ?`, s.storeID, s.storeID, batch).Scan(ctx, &economicRowsTx); err == nil {
			for _, row := range economicRowsTx {
				if err := ctx.Err(); err != nil {
					break
				}
				var work billing.EconomicRevisionWork
				if err := json.Unmarshal([]byte(row.PayloadJSON), &work); err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "malformed economic payload"})
					continue
				}
				normalized, err := work.Normalize()
				if err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "invalid economic work"})
					continue
				}
				if !billing.IsMonetaryEconomicRevisionWork(normalized) {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "non-monetary economic state"})
					continue
				}
				if _, _, _, err := billing.MonetaryEconomicPostingKey(s.storeID, normalized); err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "invalid economic lineage"})
				}
			}
		}
		var unclaimedRowsTx []economicProbeRowTx
		if err := q.NewRaw(`SELECT w.payload_json, w.work_id FROM billing_economic_work AS w LEFT JOIN billing_economic_revision_work_state AS q2 ON q2.store_id = w.store_id AND q2.work_id = w.work_id AND q2.work_version = w.work_version WHERE w.store_id = ? AND w.kind = ? AND w.status = 'pending' AND q2.id IS NULL ORDER BY w.work_id LIMIT ?`, s.storeID, economicRevisionWorkKind(billing.EconomicQueueProvider), batch).Scan(ctx, &unclaimedRowsTx); err == nil {
			for _, row := range unclaimedRowsTx {
				if err := ctx.Err(); err != nil {
					break
				}
				var work billing.EconomicRevisionWork
				if err := json.Unmarshal([]byte(row.PayloadJSON), &work); err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "malformed economic payload"})
					continue
				}
				normalized, err := work.Normalize()
				if err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "invalid economic work"})
					continue
				}
				if !billing.IsMonetaryEconomicRevisionWork(normalized) {
					continue
				}
				if _, _, _, err := billing.MonetaryEconomicPostingKey(s.storeID, normalized); err != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: row.WorkID, Reason: "invalid economic lineage"})
				}
			}
		}
	}
	// Adjustment dry-run: F4 empty inventory (same as probeUnclassifiable).
	_ = batch
	if out == nil {
		out = []billing.CutoverUnclassifiableItem{}
	}
	return out
}

// coordinatorInsertCustomerPin pins one pre-existing V1 customer call in
// draining. It bypasses the ordinary B1 new-work fence because the legacy
// row proves pre-existence (gate closed before classification guarantees no
// new rows slip in). Exact replay is idempotent; V2-owned pins are
// unclassifiable as V1. F2A: existing V1 pinned pins refresh to the draining
// marker epoch so worker claims match current marker (stable owner, renewable
// lease authority); completed history is never mutated.
func (s *DurableStore) coordinatorInsertCustomerPin(ctx context.Context, marker billing.AccountingCutoverMarker, accountID string, callID billing.BillingCallID) (conflict bool, err error) {
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		return false, err
	}
	if existing, err := s.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey); err == nil {
		if existing.Owner != billing.PostingOwnerV1 {
			return true, nil
		}
		s.refreshCustomerPinToMarker(ctx, marker, opKey, existing)
		return false, nil
	} else if !errors.Is(err, billing.ErrPostingOwnershipNotFound) {
		return false, err
	}
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := s.db.NewRaw(insert, s.storeID, string(billing.PostingOperationCustomerSettlement), opKey, accountID, callID.String(), "", "", "", "", "", billing.PostingOwnerV1, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return false, fmt.Errorf("billingstore: coordinator pin customer: %w", err)
	}
	existing, err := s.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		return false, err
	}
	return existing.Owner != billing.PostingOwnerV1, nil
}

// coordinatorInsertProviderPin pins one pre-existing V1 provider leg in
// draining, bypassing the B1 new-work fence for the same pre-existence
// reason as customer pins. F2A: existing V1 pinned pins refresh to draining
// epoch (stable owner, renewable authority); completed history untouched.
// Scope: legacy provider_cost_work lineage pins only. Economic revisions use
// coordinatorInsertProviderRevisionPin with revision-specific pins (F5+F7).
func (s *DurableStore) coordinatorInsertProviderPin(ctx context.Context, marker billing.AccountingCutoverMarker, accountID string, callID billing.BillingCallID, subject metering.SubjectRef) (conflict bool, err error) {
	opKey, err := billing.ProviderPostingOperationKey(s.storeID, accountID, callID, subject)
	if err != nil {
		return false, err
	}
	if existing, err := s.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey); err == nil {
		if existing.Owner != billing.PostingOwnerV1 {
			return true, nil
		}
		s.refreshProviderPinToMarker(ctx, marker, opKey, existing)
		return false, nil
	} else if !errors.Is(err, billing.ErrPostingOwnershipNotFound) {
		return false, err
	}
	payload, err := json.Marshal(subject)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := s.db.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), opKey, accountID, callID.String(), subject.BLegID, subject.ProviderChargeID, "", string(subject.Kind), string(payload), billing.PostingOwnerV1, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return false, fmt.Errorf("billingstore: coordinator pin provider: %w", err)
	}
	existing, err := s.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
	if err != nil {
		return false, err
	}
	return existing.Owner != billing.PostingOwnerV1, nil
}

// coordinatorInsertProviderRevisionPin pins one pre-existing V1 monetary
// economic revision in draining with its revision-specific immutable pin
// (F5+F7). Each revision outcome is a distinct pin derived from
// ProviderCostRevisionSourceKey/economic identity, not merely lineage. Base
// legacy charges keep lineage pins; heads/fences still order lineage.
// Existing V1 pinned pins refresh to draining epoch (stable owner, renewable
// authority via current-marker token); completed history untouched and never
// updated to another outcome.
func (s *DurableStore) coordinatorInsertProviderRevisionPin(ctx context.Context, marker billing.AccountingCutoverMarker, accountID string, callID billing.BillingCallID, subject metering.SubjectRef, revisionPinKey string) (conflict bool, err error) {
	if strings.TrimSpace(revisionPinKey) == "" || !billing.IsProviderRevisionPinKey(revisionPinKey) {
		return false, fmt.Errorf("%w: %w: revision pin key requires provider_call_cogs identity", billing.ErrPostingOwnershipInvalid, billing.ErrInvalidRecord)
	}
	if existing, err := s.GetPostingPin(ctx, billing.PostingOperationProviderCharge, revisionPinKey); err == nil {
		if existing.Owner != billing.PostingOwnerV1 {
			return true, nil
		}
		// Lineage must match the classified work; revision pins carry lineage
		// in BLeg/charge/subject columns.
		if existing.AccountID != accountID || existing.CallID != callID || existing.BLegID != subject.BLegID || existing.ProviderChargeID != subject.ProviderChargeID {
			return true, nil
		}
		s.refreshProviderPinToMarker(ctx, marker, revisionPinKey, existing)
		return false, nil
	} else if !errors.Is(err, billing.ErrPostingOwnershipNotFound) {
		return false, err
	}
	payload, err := json.Marshal(subject)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := s.db.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), revisionPinKey, accountID, callID.String(), subject.BLegID, subject.ProviderChargeID, "", string(subject.Kind), string(payload), billing.PostingOwnerV1, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return false, fmt.Errorf("billingstore: coordinator pin provider revision: %w", err)
	}
	existing, err := s.GetPostingPin(ctx, billing.PostingOperationProviderCharge, revisionPinKey)
	if err != nil {
		return false, err
	}
	return existing.Owner != billing.PostingOwnerV1, nil
}

// refreshCustomerPinToMarker renews a V1 pinned pin's lease authority to the
// draining marker epoch without changing its stable owner. Completed history
// is never mutated. Best-effort: refresh failures leave the original pin for
// workers to fence explicitly rather than failing classification.
func (s *DurableStore) refreshCustomerPinToMarker(ctx context.Context, marker billing.AccountingCutoverMarker, opKey string, existing billing.PostingPin) {
	if existing.Owner != billing.PostingOwnerV1 || existing.Status != billing.PostingPinPinned {
		return
	}
	if existing.MarkerVersion == marker.Version && existing.MarkerEpoch == marker.Epoch && existing.MarkerState == marker.State {
		return
	}
	now := time.Now().UTC().UnixNano()
	_, _ = s.db.NewRaw(`UPDATE billing_posting_ownership_pins SET marker_version = ?, marker_epoch = ?, marker_generation = ?, marker_state = ?, updated_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ? AND owner = ?`,
		int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), now,
		s.storeID, string(billing.PostingOperationCustomerSettlement), opKey, string(billing.PostingPinPinned), billing.PostingOwnerV1).Exec(ctx)
}

// refreshProviderPinToMarker renews a V1 pinned provider pin's lease authority
// to the draining marker epoch without changing its stable owner. Completed
// history is never mutated. Best-effort like the customer path.
func (s *DurableStore) refreshProviderPinToMarker(ctx context.Context, marker billing.AccountingCutoverMarker, opKey string, existing billing.PostingPin) {
	if existing.Owner != billing.PostingOwnerV1 || existing.Status != billing.PostingPinPinned {
		return
	}
	if existing.MarkerVersion == marker.Version && existing.MarkerEpoch == marker.Epoch && existing.MarkerState == marker.State {
		return
	}
	now := time.Now().UTC().UnixNano()
	_, _ = s.db.NewRaw(`UPDATE billing_posting_ownership_pins SET marker_version = ?, marker_epoch = ?, marker_generation = ?, marker_state = ?, updated_at_unix = ? WHERE store_id = ? AND operation_kind = ? AND operation_key = ? AND status = ? AND owner = ?`,
		int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), now,
		s.storeID, string(billing.PostingOperationProviderCharge), opKey, string(billing.PostingPinPinned), billing.PostingOwnerV1).Exec(ctx)
}

// classifyOpenExposures pins legacy open exposures created before
// admission-time pinning in bounded deterministic batches. Already-pinned
// admissions refresh to the draining epoch (stable owner, renewable
// authority). Unclassifiable exposures remain draining with explicit status;
// completion is never invented. Per StoreID isolation holds for pins;
// exposure rows are deployment-scoped (single store per database) and
// enumerated deployment-wide like the legacy usage tables.
func (s *DurableStore) classifyOpenExposures(ctx context.Context, marker billing.AccountingCutoverMarker, batch int, collect func(billing.CutoverUnclassifiableItem)) error {
	for offset := 0; offset < billing.CutoverCoordinatorDefaultMaxBatches*batch; offset += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		type row struct {
			CallID    string `bun:"call_id"`
			AccountID string `bun:"account_id"`
		}
		var rows []row
		if err := s.db.NewRaw(`SELECT call_id, account_id FROM call_exposures WHERE status = 'open' ORDER BY account_id, call_id LIMIT ? OFFSET ?`, batch, offset).Scan(ctx, &rows); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("billingstore: list open-exposure drain candidates: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			callID, err := billing.ParseBillingCallID(r.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: r.CallID, Reason: "unparseable call id"})
				continue
			}
			if _, err := billing.CustomerPostingOperationKey(r.AccountID, callID); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: r.CallID, Reason: "invalid account/call identity"})
				continue
			}
			conflict, err := s.coordinatorInsertCustomerPin(ctx, marker, r.AccountID, callID)
			if err != nil {
				if errors.Is(err, billing.ErrPostingOwnershipInvalid) {
					collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: r.CallID, Reason: "invalid pin identity"})
					continue
				}
				return err
			}
			if conflict {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: r.CallID, Reason: "conflicting owner"})
			}
		}
		if len(rows) < batch {
			return nil
		}
	}
	return nil
}

func (s *DurableStore) classifyCustomerCalls(ctx context.Context, marker billing.AccountingCutoverMarker, batch int, collect func(billing.CutoverUnclassifiableItem)) error {
	for offset := 0; offset < billing.CutoverCoordinatorDefaultMaxBatches*batch; offset += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		type row struct {
			CallID    string `bun:"call_id"`
			AccountID string `bun:"account_id"`
		}
		var rows []row
		if err := s.db.NewRaw(`SELECT call_id, account_id FROM usage_call_records WHERE claim_status <> 'processed' ORDER BY sealed_at, call_id LIMIT ? OFFSET ?`, batch, offset).Scan(ctx, &rows); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("billingstore: list customer drain candidates: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			callID, err := billing.ParseBillingCallID(r.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: r.CallID, Reason: "unparseable call id"})
				continue
			}
			if _, err := billing.CustomerPostingOperationKey(r.AccountID, callID); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: r.CallID, Reason: "invalid account/call identity"})
				continue
			}
			// Privileged drain pin: legacy row proves pre-existence, so the
			// ordinary B1 new-work fence does not apply. Replay is
			// idempotent; V2-owned pins are unclassifiable as V1.
			conflict, err := s.coordinatorInsertCustomerPin(ctx, marker, r.AccountID, callID)
			if err != nil {
				if errors.Is(err, billing.ErrPostingOwnershipInvalid) {
					collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: r.CallID, Reason: "invalid pin identity"})
					continue
				}
				return err
			}
			if conflict {
				collect(billing.CutoverUnclassifiableItem{Namespace: "customer_call_settlement", Key: r.CallID, Reason: "conflicting owner"})
			}
		}
		if len(rows) < batch {
			return nil
		}
	}
	return nil
}

func (s *DurableStore) classifyProviderCostWork(ctx context.Context, marker billing.AccountingCutoverMarker, batch int, collect func(billing.CutoverUnclassifiableItem)) error {
	for offset := 0; offset < billing.CutoverCoordinatorDefaultMaxBatches*batch; offset += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		type row struct {
			UsageLegKey string `bun:"usage_leg_key"`
			CallID      string `bun:"call_id"`
		}
		var rows []row
		if err := s.db.NewRaw(`SELECT usage_leg_key, call_id FROM provider_cost_work WHERE status = 'pending' ORDER BY updated_at, usage_leg_key LIMIT ? OFFSET ?`, batch, offset).Scan(ctx, &rows); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("billingstore: list provider drain candidates: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			var payload string
			if err := s.db.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, r.UsageLegKey).Scan(ctx, &payload); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.UsageLegKey, Reason: "missing leg record"})
				continue
			}
			var leg billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(payload), &leg); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.UsageLegKey, Reason: "malformed leg payload"})
				continue
			}
			callID, err := billing.ParseBillingCallID(r.CallID)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.UsageLegKey, Reason: "unparseable call id"})
				continue
			}
			accountID := s.accountForCall(ctx, r.CallID)
			if strings.TrimSpace(accountID) == "" {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.UsageLegKey, Reason: "missing account lineage"})
				continue
			}
			subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: s.storeID, AccountID: accountID, ALegID: leg.ALegID, BillingCallID: callID.String(), BLegID: leg.BLegID}
			if _, err := billing.ProviderPostingOperationKey(s.storeID, accountID, callID, subject); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.UsageLegKey, Reason: "invalid leg/account lineage"})
				continue
			}
			conflict, err := s.coordinatorInsertProviderPin(ctx, marker, accountID, callID, subject)
			if err != nil {
				if errors.Is(err, billing.ErrPostingOwnershipInvalid) {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.UsageLegKey, Reason: "invalid pin identity"})
					continue
				}
				return err
			}
			if conflict {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.UsageLegKey, Reason: "conflicting owner"})
			}
		}
		if len(rows) < batch {
			return nil
		}
	}
	return nil
}

// classifyAdjustmentHeads is the F4 empty inventory.
//
// Synchronous selected-cost, cost-pass-through, and direct adjustments have no
// durable pending/in-flight queue: F1 serializes every synchronous adjustment
// (it commits complete with its pin before the drain marker lock, or it is
// fenced after). Historical billing_provider_cost_heads rows are projections,
// not pending work, so scanning them would invent phantom pinned
// financial_adjustment pins that block activation with no worker to complete
// them. Only already-existing incomplete financial_adjustment pins count, via
// the B2a pin counts in buildCutoverDrainStatusTx. Historical replay/backfill
// happens only when that exact adjustment operation is replayed through its
// B2b3/B2b4 posting path, referencing its actual durable journal/snapshot/link
// outcome (stored operation/link keys and journal transaction IDs); drain never
// fabricates a completion transaction.
func (s *DurableStore) classifyAdjustmentHeads(ctx context.Context, marker billing.AccountingCutoverMarker, batch int, collect func(billing.CutoverUnclassifiableItem)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	_, _ = marker, batch
	_ = collect
	return nil
}

// cutoverGateForNewV1 reports whether new unpinned V1 work is fenced. Missing
// markers preserve the legacy v1_active default (allowed). Replay of already
// durable records is handled by callers before consulting this gate.
func (s *DurableStore) cutoverGateForNewV1(ctx context.Context) error {
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		if errors.Is(err, billing.ErrAccountingCutoverNotFound) {
			return nil
		}
		return err
	}
	if billing.IsV1FinancialWorkAllowed(marker.State) {
		return nil
	}
	return fmt.Errorf("%w: store %q forbids new V1 work in %q",
		billing.ErrAccountingCutoverFence, s.storeID, string(marker.State))
}

// cutoverGateForNewV1Tx is the transaction-scoped variant used while the
// caller already holds a write transaction (append/admission paths). F1: it
// locks/creates the per-store marker in the SAME tx (SELECT FOR UPDATE on
// PostgreSQL, write upgrade on SQLite) so concurrent activation serializes
// instead of reading stale state. Missing markers are initialized to the
// legacy v1_active default inside the same tx (allowed, no absent-row race).
func (s *DurableStore) cutoverGateForNewV1Tx(ctx context.Context, tx bun.IDB) error {
	if tx == nil {
		return s.cutoverGateForNewV1(ctx)
	}
	if btx, ok := tx.(bun.Tx); ok {
		marker, err := s.ensureAndLockAccountingCutoverTx(ctx, btx)
		if err != nil {
			return err
		}
		if billing.IsV1FinancialWorkAllowed(marker.State) {
			return nil
		}
		return fmt.Errorf("%w: store %q forbids new V1 work in %q",
			billing.ErrAccountingCutoverFence, s.storeID, string(marker.State))
	}
	row, found, err := s.loadAccountingCutover(ctx, tx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	marker, err := accountingCutoverRowToMarker(row)
	if err != nil {
		return err
	}
	if billing.IsV1FinancialWorkAllowed(marker.State) {
		return nil
	}
	return fmt.Errorf("%w: store %q forbids new V1 work in %q",
		billing.ErrAccountingCutoverFence, s.storeID, string(marker.State))
}

// isV1ClaimAllowedUnderMarker reports whether a V1 worker claim may proceed.
// Pre-drain states preserve ordinary claims; draining requires a V1 pin;
// v2_active forbids all V1 claims.
func (s *DurableStore) isV1ClaimAllowedUnderMarker(ctx context.Context, accountID string, callID billing.BillingCallID) (bool, error) {
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		if errors.Is(err, billing.ErrAccountingCutoverNotFound) {
			return true, nil
		}
		return false, err
	}
	switch marker.State {
	case billing.AccountingCutoverV1Active, billing.AccountingCutoverV2Shadow:
		return true, nil
	case billing.AccountingCutoverV2Active:
		return false, nil
	case billing.AccountingCutoverV1Draining:
		opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
		if err != nil {
			return false, nil
		}
		pin, err := s.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
		if err != nil {
			return false, nil
		}
		return billing.IsV1ClaimEligible(marker.State, pin), nil
	default:
		return false, nil
	}
}

// isProviderClaimAllowedUnderMarker filters provider-cost work claims by pins.
// Pre-drain states preserve ordinary claims; draining requires a V1 pin;
// v2_active withholds V1 but allows V2-pinned work (F3 usable V2 pipeline).
func (s *DurableStore) isProviderClaimAllowedUnderMarker(ctx context.Context, accountID string, callID billing.BillingCallID, bLegID, aLegID string) (bool, error) {
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		if errors.Is(err, billing.ErrAccountingCutoverNotFound) {
			return true, nil
		}
		return false, err
	}
	switch marker.State {
	case billing.AccountingCutoverV1Active, billing.AccountingCutoverV2Shadow:
		return true, nil
	case billing.AccountingCutoverV2Active:
		// F3: V2-pinned provider legs remain claimable; V1 and unpinned
		// withhold.
		subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: s.storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: callID.String(), BLegID: bLegID}
		opKey, err := billing.ProviderPostingOperationKey(s.storeID, accountID, callID, subject)
		if err != nil {
			return false, nil
		}
		pin, err := s.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
		if err != nil {
			return false, nil
		}
		return pin.Owner == billing.PostingOwnerV2, nil
	case billing.AccountingCutoverV1Draining:
		subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: s.storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: callID.String(), BLegID: bLegID}
		opKey, err := billing.ProviderPostingOperationKey(s.storeID, accountID, callID, subject)
		if err != nil {
			return false, nil
		}
		pin, err := s.GetPostingPin(ctx, billing.PostingOperationProviderCharge, opKey)
		if err != nil {
			return false, nil
		}
		if pin.Owner != billing.PostingOwnerV1 {
			return false, nil
		}
		return true, nil
	default:
		return false, nil
	}
}
