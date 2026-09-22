package billingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/uptrace/bun"
)

const economicBacklogDependencyScanLimit = 512

var _ billing.EconomicRevisionJobQueueStore = (*DurableStore)(nil)
var _ billing.EconomicRevisionBacklogReader = (*DurableStore)(nil)

// ClaimEconomicRevisionWorkBatch leases up to limit due jobs of one queue in a
// single bounded transaction. Jobs whose immutable dependency outputs are not
// yet durable are skipped without consuming an attempt or mutating their
// state. Each claimed job carries a fresh fence; a stale owner can never
// complete, heartbeat, retry or fail it afterwards.
func (s *DurableStore) ClaimEconomicRevisionWorkBatch(ctx context.Context, queue billing.EconomicQueue, owner string, lease time.Duration, limit int) ([]billing.EconomicRevisionClaimedWork, error) {
	return withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() ([]billing.EconomicRevisionClaimedWork, error) {
		return s.claimEconomicRevisionWorkBatchAttempt(ctx, queue, owner, lease, limit)
	})
}

func (s *DurableStore) claimEconomicRevisionWorkBatchAttempt(ctx context.Context, queue billing.EconomicQueue, owner string, lease time.Duration, limit int) ([]billing.EconomicRevisionClaimedWork, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	if err := queue.Validate(); err != nil {
		return nil, err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, fmt.Errorf("billingstore: economic revision claim owner is required")
	}
	if lease <= 0 {
		lease = defaultEconomicRevisionLease
	}
	if limit <= 0 {
		limit = billing.DefaultEconomicRevisionClaimBatchSize
	}
	if limit > billing.MaxEconomicRevisionClaimBatchSize {
		return nil, fmt.Errorf("%w: economic revision claim batch %d exceeds %d", billing.ErrInvalidEconomicRevision, limit, billing.MaxEconomicRevisionClaimBatchSize)
	}
	now := time.Now().UTC()
	nowUnix := now.UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("billingstore: begin economic revision batch claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var rows []economicRevisionWorkRow
	if err := tx.NewRaw(`
SELECT w.work_id, w.work_version, w.input_set_hash, w.payload_json, w.fingerprint, w.created_at_unix
FROM billing_economic_work AS w
LEFT JOIN billing_economic_revision_work_state AS q
	ON q.store_id = w.store_id AND q.work_id = w.work_id AND q.work_version = w.work_version
WHERE w.store_id = ? AND w.kind = ? AND w.status = 'pending'
	AND (q.id IS NULL OR (
		q.status IN (?, ?)
		AND q.next_attempt_at_unix <= ?
		AND (q.status <> ? OR q.lease_until_unix <= ?)
	))
ORDER BY COALESCE(q.attempt_count, 0) ASC, w.created_at_unix ASC, w.work_id ASC, w.work_version ASC, w.id ASC
LIMIT ?`, s.storeID, economicRevisionWorkKind(queue), economicRevisionWorkStatePending, economicRevisionWorkStateProcessing,
		nowUnix, economicRevisionWorkStateProcessing, nowUnix, limit).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: list %s economic revision claim candidates: %w", queue, err)
	}
	claims := make([]billing.EconomicRevisionClaimedWork, 0, len(rows))
	for _, row := range rows {
		work, identity, err := s.economicRevisionWorkFromRow(row)
		if err != nil {
			return nil, err
		}
		claimed, err := s.claimEconomicRevisionWorkInTx(ctx, tx, work, identity, owner, now, lease)
		if err != nil {
			return nil, err
		}
		if claimed == nil {
			continue
		}
		claims = append(claims, *claimed)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("billingstore: commit economic revision batch claim: %w", err)
	}
	return claims, nil
}

func (s *DurableStore) claimEconomicRevisionWorkInTx(ctx context.Context, tx bun.Tx, work billing.EconomicRevisionWork, identity billing.EconomicRevisionIdentity, owner string, now time.Time, lease time.Duration) (*billing.EconomicRevisionClaimedWork, error) {
	if err := s.ensureEconomicRevisionWorkStateInTx(ctx, tx, work, identity); err != nil {
		return nil, err
	}
	state, err := s.selectEconomicRevisionWorkStateInTx(ctx, tx, identity, true)
	if err != nil {
		return nil, err
	}
	if state.Status == economicRevisionWorkStateCompleted || state.Status == economicRevisionWorkStateFailed {
		return nil, nil
	}
	// R2 authoritative gate: shape plus mutable state decides; stale
	// payload EvidenceOnly/owner never decides.
	if allowed, gerr := s.economicClaimGateAllows(ctx, tx, work, state); gerr != nil {
		return nil, gerr
	} else if !allowed {
		return nil, nil
	}
	nowUnix := now.UnixNano()
	if state.Status == economicRevisionWorkStateProcessing && state.LeaseUntil > nowUnix {
		return nil, nil
	}
	if state.Status == economicRevisionWorkStatePending && state.NextAttemptAt > nowUnix {
		return nil, nil
	}
	if state.Status != economicRevisionWorkStatePending && state.Status != economicRevisionWorkStateProcessing {
		return nil, fmt.Errorf("%w: unknown work state %q", billing.ErrEconomicRevisionConflict, state.Status)
	}
	if state.AttemptCount == math.MaxInt64 || state.Fence == math.MaxInt64 {
		return nil, fmt.Errorf("%w: economic revision claim counters exhausted", billing.ErrInvalidEconomicRevision)
	}
	if len(work.Dependencies) > 0 {
		satisfied, err := s.economicDependenciesSatisfiedInTx(ctx, tx, work.Dependencies)
		if err != nil {
			return nil, err
		}
		if !satisfied {
			// Dependency-gated work stays pending without consuming an attempt
			// or holding any account/balance resource.
			return nil, nil
		}
	}
	leaseUntil := now.Add(lease).UTC()
	fence := state.Fence + 1
	result, err := tx.NewRaw(`
UPDATE billing_economic_revision_work_state
SET status = ?, attempt_count = attempt_count + 1, lease_owner = ?, lease_until_unix = ?, last_error = '', retry_reason = '', fence = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status IN (?, ?) AND fence = ?`,
		economicRevisionWorkStateProcessing, owner, leaseUntil.UnixNano(), fence, nowUnix,
		s.storeID, identity.Key(), int64(1), economicRevisionWorkStatePending, economicRevisionWorkStateProcessing, state.Fence).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("billingstore: claim economic revision work: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("billingstore: claim economic revision work rows affected: %w", err)
	}
	if affected != 1 {
		// Another writer claimed the same fence between read and update.
		return nil, nil
	}
	// R2 authoritative: return the state-overlaid work so split payload/state
	// can never disagree downstream.
	authoritative := billing.OverlayAuthoritativeEconomicWork(work, state.ProviderPosting == 1, state.PostingOwner)
	return &billing.EconomicRevisionClaimedWork{
		Work:  authoritative,
		Claim: billing.EconomicRevisionWorkClaim{Owner: owner, Fence: uint64(fence), LeaseUntil: leaseUntil},
	}, nil
}

// HeartbeatEconomicRevisionWork extends the lease of one live claim while
// keeping its fence. A stale or foreign owner is rejected so only the current
// worker can keep its work alive.
func (s *DurableStore) HeartbeatEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork, claim billing.EconomicRevisionWorkClaim, extend time.Duration) (billing.EconomicRevisionWorkClaim, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.EconomicRevisionWorkClaim{}, err
	}
	_, identity, err := s.normalizeEconomicRevisionStateWork(work)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, err
	}
	if err := validateEconomicRevisionClaim(claim); err != nil {
		return billing.EconomicRevisionWorkClaim{}, err
	}
	if extend <= 0 {
		extend = defaultEconomicRevisionLease
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(extend).UTC()
	result, err := s.db.NewRaw(`
UPDATE billing_economic_revision_work_state
SET lease_until_unix = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status = ? AND lease_owner = ? AND fence = ?`,
		leaseUntil.UnixNano(), now.UnixNano(), s.storeID, identity.Key(), int64(1),
		economicRevisionWorkStateProcessing, claim.Owner, int64(claim.Fence)).Exec(ctx)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, fmt.Errorf("billingstore: heartbeat economic revision work: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return billing.EconomicRevisionWorkClaim{}, fmt.Errorf("billingstore: heartbeat economic revision work rows affected: %w", err)
	} else if affected != 1 {
		return billing.EconomicRevisionWorkClaim{}, fmt.Errorf("%w: heartbeat economic revision %q", billing.ErrEconomicRevisionClaimLost, identity.Key())
	}
	return billing.EconomicRevisionWorkClaim{Owner: claim.Owner, Fence: claim.Fence, LeaseUntil: leaseUntil}, nil
}

// RetryEconomicRevisionWorkWithReason makes a failed attempt due again with a
// bounded, closed retry reason and an explicit next-attempt time. The claim
// owner and fence must still match.
func (s *DurableStore) RetryEconomicRevisionWorkWithReason(ctx context.Context, work billing.EconomicRevisionWork, claim billing.EconomicRevisionWorkClaim, reason billing.EconomicWorkReason, nextAttemptAt time.Time) error {
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
	if reason == "" {
		reason = billing.EconomicWorkReasonUnclassified
	}
	if err := reason.Validate(); err != nil {
		return err
	}
	if nextAttemptAt.IsZero() {
		nextAttemptAt = time.Now().UTC()
	} else {
		nextAttemptAt = nextAttemptAt.UTC()
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.NewRaw(`
UPDATE billing_economic_revision_work_state
SET status = ?, next_attempt_at_unix = ?, lease_owner = '', lease_until_unix = 0, last_error = '', retry_reason = ?, completed_at_unix = 0, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status = ? AND lease_owner = ? AND fence = ?`,
		economicRevisionWorkStatePending, nextAttemptAt.UnixNano(), reason.String(), now,
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

// FailEconomicRevisionWork terminally retires one claimed job with a bounded,
// closed failure reason. A failed job is no longer claimable or listed as
// pending; the immutable work marker remains durable history.
func (s *DurableStore) FailEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork, claim billing.EconomicRevisionWorkClaim, reason billing.EconomicWorkReason) error {
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
	if reason == "" {
		reason = billing.EconomicWorkReasonUnclassified
	}
	if err := reason.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.NewRaw(`
UPDATE billing_economic_revision_work_state
SET status = ?, failed_at_unix = ?, lease_owner = '', lease_until_unix = 0, last_error = '', retry_reason = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status = ? AND lease_owner = ? AND fence = ?`,
		economicRevisionWorkStateFailed, now, reason.String(), now,
		s.storeID, identity.Key(), int64(1), economicRevisionWorkStateProcessing, claim.Owner, int64(claim.Fence)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: fail economic revision work: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("billingstore: fail economic revision work rows affected: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("%w: fail economic revision %q", billing.ErrEconomicRevisionClaimLost, identity.Key())
	}
	return nil
}

// EconomicRevisionQueueBacklog returns a bounded operational snapshot for one
// queue: status counts, oldest pending age, next future attempt and the number
// of dependency-gated jobs whose immutable outputs are not yet durable. The
// dependency scan is bounded by economicBacklogDependencyScanLimit.
func (s *DurableStore) EconomicRevisionQueueBacklog(ctx context.Context, queue billing.EconomicQueue) (billing.EconomicRevisionBacklog, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.EconomicRevisionBacklog{}, err
	}
	if err := queue.Validate(); err != nil {
		return billing.EconomicRevisionBacklog{}, err
	}
	now := time.Now().UTC()
	nowUnix := now.UnixNano()
	var rows []struct {
		Status  string `bun:"status"`
		Count   int    `bun:"work_count"`
		Oldest  int64  `bun:"oldest_unix"`
		NextDue int64  `bun:"next_due_unix"`
	}
	if err := s.db.NewRaw(`
SELECT COALESCE(q.status, ?) AS status, COUNT(1) AS work_count,
	MIN(w.created_at_unix) AS oldest_unix,
	COALESCE(MIN(CASE WHEN q.next_attempt_at_unix > ? THEN q.next_attempt_at_unix END), 0) AS next_due_unix
FROM billing_economic_work AS w
LEFT JOIN billing_economic_revision_work_state AS q
	ON q.store_id = w.store_id AND q.work_id = w.work_id AND q.work_version = w.work_version
WHERE w.store_id = ? AND w.kind = ?
GROUP BY COALESCE(q.status, ?)`,
		economicRevisionWorkStatePending, nowUnix, s.storeID, economicRevisionWorkKind(queue), economicRevisionWorkStatePending).Scan(ctx, &rows); err != nil {
		return billing.EconomicRevisionBacklog{}, fmt.Errorf("billingstore: %s economic revision backlog: %w", queue, err)
	}
	backlog := billing.EconomicRevisionBacklog{Queue: queue}
	for _, row := range rows {
		switch row.Status {
		case economicRevisionWorkStatePending:
			backlog.Pending += row.Count
		case economicRevisionWorkStateProcessing:
			backlog.Processing += row.Count
		case economicRevisionWorkStateCompleted:
			backlog.Completed += row.Count
		case economicRevisionWorkStateFailed:
			backlog.Failed += row.Count
		default:
			return billing.EconomicRevisionBacklog{}, fmt.Errorf("%w: unknown work state %q", billing.ErrEconomicRevisionConflict, row.Status)
		}
		if row.Status != economicRevisionWorkStatePending && row.Status != economicRevisionWorkStateProcessing {
			continue
		}
		if row.Oldest > 0 && (backlog.OldestPendingAt.IsZero() || row.Oldest < backlog.OldestPendingAt.UnixNano()) {
			backlog.OldestPendingAt = time.Unix(0, row.Oldest).UTC()
		}
		if row.NextDue > 0 && (backlog.NextAttemptAt.IsZero() || row.NextDue < backlog.NextAttemptAt.UnixNano()) {
			backlog.NextAttemptAt = time.Unix(0, row.NextDue).UTC()
		}
	}
	if !backlog.OldestPendingAt.IsZero() {
		if age := now.Sub(backlog.OldestPendingAt); age > 0 {
			backlog.OldestPendingAge = age
		}
	}
	incomplete, err := s.incompleteEconomicDependencies(ctx, queue)
	if err != nil {
		return billing.EconomicRevisionBacklog{}, err
	}
	backlog.IncompleteDependencies = incomplete
	return backlog, nil
}

func (s *DurableStore) incompleteEconomicDependencies(ctx context.Context, queue billing.EconomicQueue) (int, error) {
	var rows []struct {
		PayloadJSON string `bun:"payload_json"`
	}
	if err := s.db.NewRaw(`
SELECT w.payload_json
FROM billing_economic_work AS w
JOIN billing_economic_revision_work_state AS q
	ON q.store_id = w.store_id AND q.work_id = w.work_id AND q.work_version = w.work_version
WHERE w.store_id = ? AND w.kind = ? AND q.status IN (?, ?) AND q.dependency_count > 0
ORDER BY w.created_at_unix ASC, w.work_id ASC
LIMIT ?`,
		s.storeID, economicRevisionWorkKind(queue), economicRevisionWorkStatePending, economicRevisionWorkStateProcessing,
		economicBacklogDependencyScanLimit).Scan(ctx, &rows); err != nil {
		return 0, fmt.Errorf("billingstore: %s economic revision dependency backlog: %w", queue, err)
	}
	incomplete := 0
	for _, row := range rows {
		var work billing.EconomicRevisionWork
		if err := json.Unmarshal([]byte(row.PayloadJSON), &work); err != nil {
			return 0, fmt.Errorf("billingstore: decode economic revision dependency backlog: %w", err)
		}
		normalized, err := work.Normalize()
		if err != nil {
			return 0, fmt.Errorf("billingstore: normalize economic revision dependency backlog: %w", err)
		}
		satisfied, err := s.economicDependenciesSatisfiedInTx(ctx, s.db, normalized.Dependencies)
		if err != nil {
			return 0, err
		}
		if !satisfied {
			incomplete++
		}
	}
	return incomplete, nil
}

// EconomicRevisionDependencyChecks probes the immutable rating outputs a
// dependency-anchored job references. Missing outputs make the job incomplete
// evidence; it remains pending until they are durably appended.
func (s *DurableStore) EconomicRevisionDependencyChecks(ctx context.Context, work billing.EconomicRevisionWork) ([]billing.EconomicJobDependencyCheck, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	normalized, _, err := s.normalizeEconomicRevisionStateWork(work)
	if err != nil {
		return nil, err
	}
	checks := make([]billing.EconomicJobDependencyCheck, 0, len(normalized.Dependencies))
	for _, dependency := range normalized.Dependencies {
		output, err := dependency.OutputIdentity()
		if err != nil {
			return nil, err
		}
		satisfied, err := s.economicValuationOutputExists(ctx, s.db, output)
		if err != nil {
			return nil, err
		}
		status := billing.EconomicJobDependencyMissing
		if satisfied {
			status = billing.EconomicJobDependencySatisfied
		}
		checks = append(checks, billing.EconomicJobDependencyCheck{Dependency: dependency, Status: status})
	}
	return checks, nil
}

func (s *DurableStore) economicDependenciesSatisfiedInTx(ctx context.Context, q bun.IDB, dependencies []billing.EconomicJobDependency) (bool, error) {
	for _, dependency := range dependencies {
		output, err := dependency.OutputIdentity()
		if err != nil {
			return false, err
		}
		satisfied, err := s.economicValuationOutputExists(ctx, q, output)
		if err != nil {
			return false, err
		}
		if !satisfied {
			return false, nil
		}
	}
	return true, nil
}

func (s *DurableStore) economicValuationOutputExists(ctx context.Context, q bun.IDB, identity billing.EconomicRevisionIdentity) (bool, error) {
	var count int
	if err := q.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ?`,
		s.storeID, identity.ValuationKey(), int64(economics.ValuationVersionV2)).Scan(ctx, &count); err != nil {
		return false, fmt.Errorf("billingstore: probe economic dependency output: %w", err)
	}
	return count != 0, nil
}
