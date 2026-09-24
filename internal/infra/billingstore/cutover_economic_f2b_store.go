package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Phase 17.3 F2B: monetary economic revision inventory.
//
// Only durable economic revision work that can create provider payable/cost/
// exclusion through the production EconomicRevisionWorker with a provider-cost
// adapter is classified/pinned here, using the canonical provider operation
// identity (ProviderPostingOperationKey). Pending and leased (processing)
// monetary work is enumerated in bounded deterministic batches
// (ORDER BY work_id LIMIT/OFFSET) and pinned V1 idempotently; existing V1
// pinned pins refresh to the draining epoch for renewable worker authority
// (stable owner). Evidence-only customer rating, reconciliation jobs, shadow
// observations/valuations, and queues without a posting adapter are never
// pinned, counted, or fenced as monetary work.
//
// Unclassifiable monetary candidates (unparseable call, invalid lineage)
// remain draining with explicit status; completion is never invented.

// classifyEconomicProviderWork pins pre-boundary monetary provider economic
// work in bounded batches. It covers state-tracked pending/processing monetary
// rows plus provider work without delivery state (legacy NULL, safe overcount
// resolved here by materializing state). Evidence-only rows materialize
// evidence state without pins so future counts exclude them.
func (s *DurableStore) classifyEconomicProviderWork(ctx context.Context, marker billing.AccountingCutoverMarker, batch int, collect func(billing.CutoverUnclassifiableItem)) error {
	if err := s.classifyEconomicStateMonetary(ctx, marker, batch, collect); err != nil {
		return err
	}
	return s.classifyEconomicUnclaimedProviderWork(ctx, marker, batch, collect)
}

func (s *DurableStore) classifyEconomicStateMonetary(ctx context.Context, marker billing.AccountingCutoverMarker, batch int, collect func(billing.CutoverUnclassifiableItem)) error {
	for offset := 0; offset < billing.CutoverCoordinatorDefaultMaxBatches*batch; offset += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rows []struct {
			PayloadJSON string `bun:"payload_json"`
			WorkID      string `bun:"work_id"`
		}
		if err := s.db.NewRaw(`SELECT w.payload_json, w.work_id FROM billing_economic_work AS w JOIN billing_economic_revision_work_state AS q ON q.store_id = w.store_id AND q.work_id = w.work_id AND q.work_version = w.work_version WHERE w.store_id = ? AND q.store_id = ? AND q.provider_posting = 1 AND q.status IN ('pending', 'processing') ORDER BY w.work_id LIMIT ? OFFSET ?`, s.storeID, s.storeID, batch, offset).Scan(ctx, &rows); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("billingstore: list economic drain candidates: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			var work billing.EconomicRevisionWork
			if err := json.Unmarshal([]byte(r.PayloadJSON), &work); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "malformed economic payload"})
				continue
			}
			normalized, err := work.Normalize()
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "invalid economic work"})
				continue
			}
			// R2 authoritative: state already says monetary (provider_posting=1
			// selection); payload shape decides, never payload EvidenceOnly.
			// Upgraded shadow (payload evidence-only, state monetary) has
			// monetary shape and must be pinned. Pin identity ignores
			// delivery intent, so clear the stale evidence flag for key
			// derivation only.
			if !billing.IsMonetaryEconomicShape(normalized) {
				// State intent says monetary but payload no longer qualifies
				// (e.g. backfilled legacy row whose subject is invalid).
				// Leave it draining with explicit status; do not invent a pin.
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "non-monetary economic state"})
				continue
			}
			forPin := normalized
			forPin.EvidenceOnly = false
			accountID, callID, pinKey, err := billing.MonetaryEconomicPostingKey(s.storeID, forPin)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "invalid economic lineage"})
				continue
			}
			conflict, err := s.coordinatorInsertProviderRevisionPin(ctx, marker, accountID, callID, normalized.Subject, pinKey)
			if err != nil {
				if errors.Is(err, billing.ErrPostingOwnershipInvalid) {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "invalid pin identity"})
					continue
				}
				return err
			}
			if conflict {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "conflicting owner"})
			}
		}
		if len(rows) < batch {
			return nil
		}
	}
	return nil
}

func (s *DurableStore) classifyEconomicUnclaimedProviderWork(ctx context.Context, marker billing.AccountingCutoverMarker, batch int, collect func(billing.CutoverUnclassifiableItem)) error {
	for offset := 0; offset < billing.CutoverCoordinatorDefaultMaxBatches*batch; offset += batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rows []struct {
			PayloadJSON string `bun:"payload_json"`
			WorkID      string `bun:"work_id"`
		}
		if err := s.db.NewRaw(`SELECT w.payload_json, w.work_id FROM billing_economic_work AS w LEFT JOIN billing_economic_revision_work_state AS q ON q.store_id = w.store_id AND q.work_id = w.work_id AND q.work_version = w.work_version WHERE w.store_id = ? AND w.kind = ? AND w.status = 'pending' AND q.id IS NULL ORDER BY w.work_id LIMIT ? OFFSET ?`, s.storeID, economicRevisionWorkKind(billing.EconomicQueueProvider), batch, offset).Scan(ctx, &rows); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("billingstore: list unclaimed economic drain candidates: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			var work billing.EconomicRevisionWork
			if err := json.Unmarshal([]byte(r.PayloadJSON), &work); err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "malformed economic payload"})
				continue
			}
			normalized, err := work.Normalize()
			if err != nil {
				// Malformed payloads cannot be pinned; record and continue.
				// Materialize evidence state so future counts do not overcount
				// them as monetary NULL.
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "invalid economic work"})
				continue
			}
			if !billing.IsMonetaryEconomicRevisionWork(normalized) {
				// Evidence-only (customer-shaped, reconciliation, or invalid
				// lineage on provider queue): materialize evidence state so
				// counts exclude it, but never pin.
				identity, ierr := normalized.Identity()
				if ierr != nil {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "invalid economic identity"})
					continue
				}
				tx, terr := s.db.BeginTx(ctx, nil)
				if terr != nil {
					return fmt.Errorf("billingstore: begin unclaimed evidence state: %w", terr)
				}
				_ = s.ensureEconomicRevisionWorkStateEvidenceInTx(ctx, tx, normalized, identity)
				_ = tx.Commit()
				continue
			}
			accountID, callID, pinKey, err := billing.MonetaryEconomicPostingKey(s.storeID, normalized)
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "invalid economic lineage"})
				continue
			}
			// Materialize monetary state first so counts track it even if pin
			// insertion races; then pin V1 idempotently.
			identity, err := normalized.Identity()
			if err != nil {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "invalid economic identity"})
				continue
			}
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("billingstore: begin unclaimed monetary state: %w", err)
			}
			if serr := s.ensureEconomicRevisionWorkStateWithOwnerInTx(ctx, tx, normalized, identity, billing.PostingOwnerV1); serr != nil {
				_ = tx.Rollback()
				return serr
			}
			if cerr := tx.Commit(); cerr != nil {
				return fmt.Errorf("billingstore: commit unclaimed monetary state: %w", cerr)
			}
			conflict, err := s.coordinatorInsertProviderRevisionPin(ctx, marker, accountID, callID, normalized.Subject, pinKey)
			if err != nil {
				if errors.Is(err, billing.ErrPostingOwnershipInvalid) {
					collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "invalid pin identity"})
					continue
				}
				return err
			}
			if conflict {
				collect(billing.CutoverUnclassifiableItem{Namespace: "provider_charge", Key: r.WorkID, Reason: "conflicting owner"})
			}
		}
		if len(rows) < batch {
			return nil
		}
	}
	return nil
}
