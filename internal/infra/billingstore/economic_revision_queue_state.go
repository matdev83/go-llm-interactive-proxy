package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const (
	economicRevisionWorkStatePending    = "pending"
	economicRevisionWorkStateProcessing = "processing"
	economicRevisionWorkStateCompleted  = "completed"
	defaultEconomicRevisionLease        = 30 * time.Second
)

type economicRevisionWorkStateRow struct {
	ID            int64  `bun:"id"`
	StoreID       string `bun:"store_id"`
	WorkID        string `bun:"work_id"`
	WorkVersion   int64  `bun:"work_version"`
	Queue         string `bun:"queue"`
	HeadKey       string `bun:"head_key"`
	Status        string `bun:"status"`
	AttemptCount  int64  `bun:"attempt_count"`
	NextAttemptAt int64  `bun:"next_attempt_at_unix"`
	LeaseOwner    string `bun:"lease_owner"`
	LeaseUntil    int64  `bun:"lease_until_unix"`
	LastError     string `bun:"last_error"`
	Fence         int64  `bun:"fence"`
	CompletedAt   int64  `bun:"completed_at_unix"`
	CreatedAt     int64  `bun:"created_at_unix"`
	UpdatedAt     int64  `bun:"updated_at_unix"`
}

var _ billing.EconomicRevisionWorkStateStore = (*DurableStore)(nil)

func (s *DurableStore) normalizeEconomicRevisionStateWork(work billing.EconomicRevisionWork) (billing.EconomicRevisionWork, billing.EconomicRevisionIdentity, error) {
	if s == nil || s.db == nil {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("billingstore: nil store")
	}
	normalized, err := work.Normalize()
	if err != nil {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, err
	}
	identity, err := normalized.Identity()
	if err != nil {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, err
	}
	if normalized.Subject.StoreID != s.storeID {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("%w: economic revision subject store", ErrEconomicsOutOfScope)
	}
	if normalized.EvidenceRevision > math.MaxInt64 {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("%w: evidence revision exceeds database range", billing.ErrInvalidEconomicRevision)
	}
	return normalized, identity, nil
}

func (s *DurableStore) ensureEconomicRevisionWorkStateInTx(ctx context.Context, tx bun.Tx, work billing.EconomicRevisionWork, identity billing.EconomicRevisionIdentity) error {
	now := time.Now().UTC().UnixNano()
	if _, err := tx.NewRaw(`
INSERT INTO billing_economic_revision_work_state(
		store_id, work_id, work_version, queue, head_key, status, attempt_count,
		next_attempt_at_unix, lease_owner, lease_until_unix, last_error, fence,
		completed_at_unix, created_at_unix, updated_at_unix
) VALUES (?, ?, ?, ?, ?, ?, 0, 0, '', 0, '', 0, 0, ?, ?)
ON CONFLICT DO NOTHING`, s.storeID, identity.Key(), int64(1), work.Queue.String(), work.HeadKey,
		economicRevisionWorkStatePending, now, now).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert economic revision work state: %w", err)
	}
	state, err := s.selectEconomicRevisionWorkStateInTx(ctx, tx, identity, false)
	if err != nil {
		return err
	}
	if state.Queue != work.Queue.String() || state.HeadKey != work.HeadKey {
		return fmt.Errorf("%w: economic revision work state identity %q", billing.ErrEconomicRevisionConflict, identity.Key())
	}
	return nil
}

func (s *DurableStore) selectEconomicRevisionWorkStateInTx(ctx context.Context, tx bun.Tx, identity billing.EconomicRevisionIdentity, forUpdate bool) (economicRevisionWorkStateRow, error) {
	query := `SELECT id, store_id, work_id, work_version, queue, head_key, status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error, fence, completed_at_unix, created_at_unix, updated_at_unix
FROM billing_economic_revision_work_state
WHERE store_id = ? AND work_id = ? AND work_version = ? LIMIT 1`
	if forUpdate && tx.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var state economicRevisionWorkStateRow
	if err := tx.NewRaw(query, s.storeID, identity.Key(), int64(1)).Scan(ctx, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return economicRevisionWorkStateRow{}, fmt.Errorf("%w: state for %q", billing.ErrEconomicRevisionClaimLost, identity.Key())
		}
		return economicRevisionWorkStateRow{}, fmt.Errorf("billingstore: select economic revision work state: %w", err)
	}
	return state, nil
}

// ClaimEconomicRevisionWork leases one immutable revision marker. The state
// row is mutable delivery metadata; its monotonically increasing fence makes
// reclaiming an expired lease safe across multiple workers.
func (s *DurableStore) ClaimEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork, owner string, lease time.Duration) (billing.EconomicRevisionWorkClaim, bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return billing.EconomicRevisionWorkClaim{}, false, fmt.Errorf("billingstore: economic revision claim owner is required")
	}
	if lease <= 0 {
		lease = defaultEconomicRevisionLease
	}
	normalized, identity, err := s.normalizeEconomicRevisionStateWork(work)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, fmt.Errorf("billingstore: begin economic revision claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.ensureEconomicRevisionWorkStateInTx(ctx, tx, normalized, identity); err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, err
	}
	state, err := s.selectEconomicRevisionWorkStateInTx(ctx, tx, identity, true)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, err
	}
	if state.Status == economicRevisionWorkStateCompleted {
		return billing.EconomicRevisionWorkClaim{}, false, nil
	}
	now := time.Now().UTC()
	nowUnix := now.UnixNano()
	if state.Status == economicRevisionWorkStateProcessing && state.LeaseUntil > nowUnix {
		return billing.EconomicRevisionWorkClaim{}, false, nil
	}
	if state.Status == economicRevisionWorkStatePending && state.NextAttemptAt > nowUnix {
		return billing.EconomicRevisionWorkClaim{}, false, nil
	}
	if state.Status != economicRevisionWorkStatePending && state.Status != economicRevisionWorkStateProcessing {
		return billing.EconomicRevisionWorkClaim{}, false, fmt.Errorf("%w: unknown work state %q", billing.ErrEconomicRevisionConflict, state.Status)
	}
	if state.AttemptCount == math.MaxInt64 || state.Fence == math.MaxInt64 {
		return billing.EconomicRevisionWorkClaim{}, false, fmt.Errorf("%w: economic revision claim counters exhausted", billing.ErrInvalidEconomicRevision)
	}
	leaseUntil := now.Add(lease).UTC()
	fence := state.Fence + 1
	if _, err := tx.NewRaw(`
UPDATE billing_economic_revision_work_state
SET status = ?, attempt_count = attempt_count + 1, lease_owner = ?, lease_until_unix = ?, last_error = '', fence = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status <> ?`,
		economicRevisionWorkStateProcessing, owner, leaseUntil.UnixNano(), fence, nowUnix,
		s.storeID, identity.Key(), int64(1), economicRevisionWorkStateCompleted).Exec(ctx); err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, fmt.Errorf("billingstore: claim economic revision work: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, fmt.Errorf("billingstore: commit economic revision claim: %w", err)
	}
	return billing.EconomicRevisionWorkClaim{Owner: owner, Fence: uint64(fence), LeaseUntil: leaseUntil}, true, nil
}

// CompleteEconomicRevisionWork durably retires a successful attempt without
// mutating the immutable evidence marker or any derived result/head.
func (s *DurableStore) CompleteEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork, claim billing.EconomicRevisionWorkClaim) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	_, identity, err := s.normalizeEconomicRevisionStateWork(work)
	if err != nil {
		return err
	}
	if err := validateEconomicRevisionClaim(claim); err != nil {
		return err
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.NewRaw(`
UPDATE billing_economic_revision_work_state
SET status = ?, lease_owner = '', lease_until_unix = 0, last_error = '', completed_at_unix = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status = ? AND lease_owner = ? AND fence = ?`,
		economicRevisionWorkStateCompleted, now, now, s.storeID, identity.Key(), int64(1),
		economicRevisionWorkStateProcessing, claim.Owner, int64(claim.Fence)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: complete economic revision work: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("billingstore: complete economic revision work rows affected: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("%w: complete economic revision %q", billing.ErrEconomicRevisionClaimLost, identity.Key())
	}
	return nil
}

// RetryEconomicRevisionWork makes a failed attempt due again and releases
// the lease. A caller may supply a future time for backoff; workers use the
// current time so the next bounded poll retries without starvation.
func (s *DurableStore) RetryEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork, claim billing.EconomicRevisionWorkClaim, reason string, nextAttemptAt time.Time) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	_, identity, err := s.normalizeEconomicRevisionStateWork(work)
	if err != nil {
		return err
	}
	if err := validateEconomicRevisionClaim(claim); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "economic_revision_failed"
	}
	if nextAttemptAt.IsZero() {
		nextAttemptAt = time.Now().UTC()
	} else {
		nextAttemptAt = nextAttemptAt.UTC()
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.NewRaw(`
UPDATE billing_economic_revision_work_state
SET status = ?, next_attempt_at_unix = ?, lease_owner = '', lease_until_unix = 0, last_error = ?, completed_at_unix = 0, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status = ? AND lease_owner = ? AND fence = ?`,
		economicRevisionWorkStatePending, nextAttemptAt.UnixNano(), reason, now,
		s.storeID, identity.Key(), int64(1), economicRevisionWorkStateProcessing, claim.Owner, int64(claim.Fence)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: retry economic revision work: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("billingstore: retry economic revision work rows affected: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("%w: retry economic revision %q", billing.ErrEconomicRevisionClaimLost, identity.Key())
	}
	return nil
}

func validateEconomicRevisionClaim(claim billing.EconomicRevisionWorkClaim) error {
	if strings.TrimSpace(claim.Owner) == "" {
		return fmt.Errorf("billingstore: economic revision claim owner is required")
	}
	if claim.Fence == 0 || claim.Fence > math.MaxInt64 {
		return fmt.Errorf("billingstore: economic revision claim fence is invalid")
	}
	return nil
}
