package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Phase 17.3 Finding F1: shared per-store transactional serialization
// boundary between activation and every authorized admission/posting path.
//
// Lock protocol (per store_id, durable, no global mutex):
//   - PostgreSQL: SELECT ... WHERE store_id = ? FOR UPDATE on
//     billing_accounting_cutover. Row-level lock serializes concurrent
//     monetary transactions and activation on the same store; different
//     store_id rows do not block each other.
//   - SQLite: SELECT + dummy UPDATE ... SET updated_at_unix =
//     updated_at_unix WHERE store_id = ? upgrades the DEFERRED transaction
//     to a write (RESERVED) immediately, so concurrent writers serialize via
//     BUSY + bounded retry (withAccountTx). Readers (WAL) do not block the
//     single writer longer than the tx.
// Lock ordering (where practical): marker before account/head/pins. All
// monetary posting and financial admission/new-work transactions acquire the
// marker first, then accounts/heads/pins. Activation acquires the same marker
// first, recomputes drain inside the SAME transaction, then transitions
// before commit. Missing markers are initialized inside the same tx
// (INSERT ... ON CONFLICT DO NOTHING / OR IGNORE + locked reload) so no
// absent-row race remains. Evidence-only shadow journal paths do not acquire
// this boundary.
//
// Table:
//   | Path                              | Marker lock site                     | Drain/authorize inside same tx |
//   | AdmitExposure (new)               | ensureAndLock... first, before acct  | gate uses locked snapshot      |
//   | AppendCallUsage (new)             | ensureAndLock... before gate/insert  | gate uses locked snapshot      |
//   | AppendCallLegUsage (new)          | ensureAndLock... before gate/enqueue | gate uses locked snapshot      |
//   | ApplyCallBillingResult (customer) | ensureAndLock... before acct/pin     | pin/epoch checks use snapshot  |
//   | ApplyProviderCost (legacy)        | ensureAndLock... before acct/pin     | pin/epoch checks use snapshot  |
//   | ApplyProviderCostRevision         | ensureAndLock... before acct/pin     | pin/epoch checks use snapshot  |
//   | ApplySelectedCostAdjustment (B2b3)| ensureAndLock... before acct/head    | pin/epoch checks use snapshot  |
//   | ApplyCostPassThroughRevision (B2b4)| ensureAndLock... before acct/head   | pin/epoch checks use snapshot  |
//   | PostAdjustment (direct B2b4)      | ensureAndLock... before acct/pin     | pin/epoch checks use snapshot  |
//   | Acquire/CompletePostingPin        | locked load before pin CAS           | epoch checks use snapshot      |
//   | TransitionAccountingCutover       | locked load before CAS UPDATE        | CAS in same tx                 |
//   | ActivateCutoverV2                 | locked load + drain counts + CAS in one tx | yes                    |

func (s *DurableStore) loadAccountingCutoverLocked(ctx context.Context, tx bun.Tx) (accountingCutoverRow, bool, error) {
	if err := ctx.Err(); err != nil {
		return accountingCutoverRow{}, false, err
	}
	if s.db.Dialect().Name() == dialect.PG {
		var row accountingCutoverRow
		err := tx.NewRaw(accountingCutoverSelect+` WHERE store_id = ? FOR UPDATE`, s.storeID).Scan(ctx, &row)
		if errors.Is(err, sql.ErrNoRows) {
			return accountingCutoverRow{}, false, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return accountingCutoverRow{}, false, ctx.Err()
			}
			return accountingCutoverRow{}, false, fmt.Errorf("billingstore: lock accounting cutover: %w", err)
		}
		return row, true, nil
	}
	var row accountingCutoverRow
	err := tx.NewRaw(accountingCutoverSelect+` WHERE store_id = ? LIMIT 1`, s.storeID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return accountingCutoverRow{}, false, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return accountingCutoverRow{}, false, ctx.Err()
		}
		return accountingCutoverRow{}, false, fmt.Errorf("billingstore: load accounting cutover: %w", err)
	}
	// Upgrade to a write transaction immediately so concurrent SQLite writers
	// serialize via BUSY + bounded retry instead of committing on stale reads.
	if _, err := tx.NewRaw(`UPDATE billing_accounting_cutover SET updated_at_unix = updated_at_unix WHERE store_id = ?`, s.storeID).Exec(ctx); err != nil {
		if ctx.Err() != nil {
			return accountingCutoverRow{}, false, ctx.Err()
		}
		return accountingCutoverRow{}, false, fmt.Errorf("billingstore: lock accounting cutover: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return accountingCutoverRow{}, false, err
	}
	return row, true, nil
}

// ensureAndLockAccountingCutoverTx returns the locked marker snapshot,
// creating the safe V1 default inside the same tx when absent. Concurrent
// initializers converge via INSERT ... ON CONFLICT DO NOTHING / OR IGNORE;
// on PostgreSQL the second inserter waits on the first uncommitted row via
// the subsequent FOR UPDATE reload.
//
// Historical migration drains (e.g. usage-append outbox retirement) construct
// a scope-less DurableStore{db} with empty storeID. They replay legacy
// evidence outside any cutover scope; bypass the per-store lock there and
// report the legacy V1 default without a durable write. Production stores
// always carry a non-empty storeID via NewDurableStore.
func (s *DurableStore) ensureAndLockAccountingCutoverTx(ctx context.Context, tx bun.Tx) (billing.AccountingCutoverMarker, error) {
	if err := ctx.Err(); err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if strings.TrimSpace(s.storeID) == "" {
		return billing.AccountingCutoverMarker{
			StoreID:            "",
			Generation:         billing.AccountingCutoverGenerationV1,
			State:              billing.AccountingCutoverV1Active,
			ActivePostingOwner: billing.AccountingPostingOwnerV1,
			CompatibilityFloor: billing.AccountingCompatFloorV1,
			Version:            1,
			Epoch:              1,
			TransitionID:       "init:v1_active",
			CreatedAtUnix:      1,
			UpdatedAtUnix:      1,
		}, nil
	}
	if row, found, err := s.loadAccountingCutoverLocked(ctx, tx); err != nil {
		return billing.AccountingCutoverMarker{}, err
	} else if found {
		return accountingCutoverRowToMarker(row)
	}
	now := time.Now().UTC().UnixNano()
	def, err := billing.DefaultAccountingCutoverMarker(s.storeID, now)
	if err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	insert := `INSERT INTO billing_accounting_cutover (store_id, generation, state, active_posting_owner, compatibility_floor, version, epoch, transition_id, created_at_unix, updated_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_accounting_cutover (store_id, generation, state, active_posting_owner, compatibility_floor, version, epoch, transition_id, created_at_unix, updated_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, def.StoreID, def.Generation, string(def.State), def.ActivePostingOwner, def.CompatibilityFloor, int64(def.Version), int64(def.Epoch), def.TransitionID, def.CreatedAtUnix, def.UpdatedAtUnix).Exec(ctx); err != nil {
		if ctx.Err() != nil {
			return billing.AccountingCutoverMarker{}, ctx.Err()
		}
		return billing.AccountingCutoverMarker{}, fmt.Errorf("billingstore: ensure accounting cutover in tx: %w", err)
	}
	row, found, err := s.loadAccountingCutoverLocked(ctx, tx)
	if err != nil {
		return billing.AccountingCutoverMarker{}, err
	}
	if !found {
		return billing.AccountingCutoverMarker{}, fmt.Errorf("%w: store %q cutover unavailable after ensure", billing.ErrAccountingCutoverNotFound, s.storeID)
	}
	return accountingCutoverRowToMarker(row)
}

// countCustomerPendingTx counts non-processed call closures inside q.
func (s *DurableStore) countCustomerPendingTx(ctx context.Context, q bun.IDB) (int, error) {
	var n int
	if err := q.NewRaw(`SELECT COUNT(1) FROM usage_call_records WHERE claim_status <> 'processed'`).Scan(ctx, &n); err != nil {
		return 0, fmt.Errorf("billingstore: count customer pending: %w", err)
	}
	return n, nil
}

// countProviderPendingTx counts pending provider-cost work inside q.
func (s *DurableStore) countProviderPendingTx(ctx context.Context, q bun.IDB) (int, error) {
	var n int
	if err := q.NewRaw(`SELECT COUNT(1) FROM provider_cost_work WHERE status = 'pending'`).Scan(ctx, &n); err != nil {
		return 0, fmt.Errorf("billingstore: count provider pending: %w", err)
	}
	return n, nil
}

// countV1PinsTx counts V1 pinned/completed pins inside q.
func (s *DurableStore) countV1PinsTx(ctx context.Context, q bun.IDB) (pinned, completed int, err error) {
	if err := q.NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND owner = ? AND status = ?`,
		s.storeID, billing.PostingOwnerV1, string(billing.PostingPinPinned)).Scan(ctx, &pinned); err != nil {
		return 0, 0, fmt.Errorf("billingstore: count V1 pinned: %w", err)
	}
	if err := q.NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND owner = ? AND status = ?`,
		s.storeID, billing.PostingOwnerV1, string(billing.PostingPinCompleted)).Scan(ctx, &completed); err != nil {
		return 0, 0, fmt.Errorf("billingstore: count V1 completed: %w", err)
	}
	return pinned, completed, nil
}

// countAdjustmentPendingTx counts pinned financial-adjustment pins inside q.
func (s *DurableStore) countAdjustmentPendingTx(ctx context.Context, q bun.IDB) (int, error) {
	var pinned int
	if err := q.NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND status = ?`,
		s.storeID, string(billing.PostingOperationFinancialAdjustment), string(billing.PostingPinPinned)).Scan(ctx, &pinned); err != nil {
		return 0, fmt.Errorf("billingstore: count adjustment pending: %w", err)
	}
	return pinned, nil
}

// countOpenExposuresTx counts admitted call_exposures rows with status='open'.
// F2A: open admitted exposures block activation even when no closure/leg/pin
// exists yet; they close only via terminal settlement (or legitimate
// cancellation outcome through the same seam). Deployment-scoped table in
// production (single store per database); counted deployment-wide like the
// legacy usage tables enumerated by the coordinator.
func (s *DurableStore) countOpenExposuresTx(ctx context.Context, q bun.IDB) (int, error) {
	var n int
	if err := q.NewRaw(`SELECT COUNT(1) FROM call_exposures WHERE status = 'open'`).Scan(ctx, &n); err != nil {
		return 0, fmt.Errorf("billingstore: count open exposures: %w", err)
	}
	return n, nil
}

// countEconomicProviderPendingTx counts pending/leased monetary provider
// economic revision work (F2B). Evidence-only customer rating, reconciliation,
// shadow, and queues without a posting adapter are never counted: only state
// rows with provider_posting=1 in pending/processing block activation, plus a
// safe overcount of provider work without delivery state (resolved by
// classification, which materializes state and pins monetary work).
func (s *DurableStore) countEconomicProviderPendingTx(ctx context.Context, q bun.IDB) (int, error) {
	var n int
	if err := q.NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND provider_posting = 1 AND status IN ('pending', 'processing')`, s.storeID).Scan(ctx, &n); err != nil {
		return 0, fmt.Errorf("billingstore: count economic provider pending: %w", err)
	}
	var nullN int
	if err := q.NewRaw(`SELECT COUNT(1) FROM billing_economic_work AS w LEFT JOIN billing_economic_revision_work_state AS q2 ON q2.store_id = w.store_id AND q2.work_id = w.work_id AND q2.work_version = w.work_version WHERE w.store_id = ? AND w.kind = ? AND w.status = 'pending' AND q2.id IS NULL`, s.storeID, economicRevisionWorkKind(billing.EconomicQueueProvider)).Scan(ctx, &nullN); err != nil {
		return 0, fmt.Errorf("billingstore: count economic provider unclaimed: %w", err)
	}
	return n + nullN, nil
}

// buildCutoverDrainStatusTx builds the drain snapshot from q (tx for
// activation serialization, s.db for status probes).
func (s *DurableStore) buildCutoverDrainStatusTx(ctx context.Context, q bun.IDB, marker billing.AccountingCutoverMarker, unclassifiable []billing.CutoverUnclassifiableItem) (billing.CutoverDrainStatus, error) {
	customerPending, err := s.countCustomerPendingTx(ctx, q)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	providerPending, err := s.countProviderPendingTx(ctx, q)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	v1Pinned, v1Completed, err := s.countV1PinsTx(ctx, q)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	adjustmentPending, err := s.countAdjustmentPendingTx(ctx, q)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	openExposures, err := s.countOpenExposuresTx(ctx, q)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	economicPending, err := s.countEconomicProviderPendingTx(ctx, q)
	if err != nil {
		return billing.CutoverDrainStatus{}, err
	}
	if unclassifiable == nil {
		unclassifiable = []billing.CutoverUnclassifiableItem{}
	}
	ready := customerPending == 0 && providerPending == 0 && economicPending == 0 && v1Pinned == 0 && openExposures == 0 && len(unclassifiable) == 0
	_ = adjustmentPending
	return billing.CutoverDrainStatus{
		State:         marker.State,
		MarkerVersion: marker.Version,
		MarkerEpoch:   marker.Epoch,
		Counts: billing.CutoverDrainCounts{
			CustomerPending:         customerPending,
			ProviderPending:         providerPending,
			EconomicProviderPending: economicPending,
			AdjustmentPending:       adjustmentPending,
			V1Pinned:                v1Pinned,
			V1Completed:             v1Completed,
			Unclassifiable:          len(unclassifiable),
			OpenExposures:           openExposures,
		},
		Unclassifiable:     unclassifiable,
		ReadyForActivation: ready,
	}, nil
}
