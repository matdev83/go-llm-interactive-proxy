package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const (
	economicRevisionWorkStatePending    = "pending"
	economicRevisionWorkStateProcessing = "processing"
	economicRevisionWorkStateCompleted  = "completed"
	economicRevisionWorkStateFailed     = "failed"
	defaultEconomicRevisionLease        = 30 * time.Second
)

type economicRevisionWorkStateRow struct {
	ID              int64  `bun:"id"`
	StoreID         string `bun:"store_id"`
	WorkID          string `bun:"work_id"`
	WorkVersion     int64  `bun:"work_version"`
	Queue           string `bun:"queue"`
	HeadKey         string `bun:"head_key"`
	WorkKind        string `bun:"work_kind"`
	DependencyCount int64  `bun:"dependency_count"`
	Status          string `bun:"status"`
	AttemptCount    int64  `bun:"attempt_count"`
	NextAttemptAt   int64  `bun:"next_attempt_at_unix"`
	LeaseOwner      string `bun:"lease_owner"`
	LeaseUntil      int64  `bun:"lease_until_unix"`
	LastError       string `bun:"last_error"`
	RetryReason     string `bun:"retry_reason"`
	Fence           int64  `bun:"fence"`
	CompletedAt     int64  `bun:"completed_at_unix"`
	FailedAt        int64  `bun:"failed_at_unix"`
	CreatedAt       int64  `bun:"created_at_unix"`
	UpdatedAt       int64  `bun:"updated_at_unix"`
	ProviderPosting int64  `bun:"provider_posting"`
	PostingOwner    string `bun:"posting_owner"`
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
	providerPosting, postingOwner := economicPostingIntentForWork(work)
	providerPostingInt := int64(0)
	if providerPosting {
		providerPostingInt = 1
	}
	if _, err := tx.NewRaw(`
INSERT INTO billing_economic_revision_work_state(
		store_id, work_id, work_version, queue, head_key, work_kind, dependency_count, status, attempt_count,
		next_attempt_at_unix, lease_owner, lease_until_unix, last_error, retry_reason, fence,
		completed_at_unix, failed_at_unix, created_at_unix, updated_at_unix, provider_posting, posting_owner
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, '', 0, '', '', 0, 0, 0, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`, s.storeID, identity.Key(), int64(1), work.Queue.String(), work.HeadKey,
		work.Kind.String(), len(work.Dependencies),
		economicRevisionWorkStatePending, now, now, providerPostingInt, postingOwner).Exec(ctx); err != nil {
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

// ensureEconomicRevisionWorkStateWithOwnerInTx persists delivery intent with an
// explicit posting owner for monetary work (V1 pre-boundary or V2 authorized).
// It is idempotent via ON CONFLICT DO NOTHING; existing rows keep their first
// intent (stable owner), while drain classification refreshes pin epochs
// separately for renewable authority.
func (s *DurableStore) ensureEconomicRevisionWorkStateWithOwnerInTx(ctx context.Context, tx bun.Tx, work billing.EconomicRevisionWork, identity billing.EconomicRevisionIdentity, owner string) error {
	now := time.Now().UTC().UnixNano()
	if owner != billing.PostingOwnerV1 && owner != billing.PostingOwnerV2 {
		return fmt.Errorf("%w: unknown economic posting owner %q", billing.ErrInvalidEconomicRevision, owner)
	}
	if !billing.IsMonetaryEconomicRevisionWork(work) {
		return fmt.Errorf("%w: explicit %q posting requires monetary provider work", billing.ErrInvalidEconomicRevision, owner)
	}
	if _, err := tx.NewRaw(`
INSERT INTO billing_economic_revision_work_state(
		store_id, work_id, work_version, queue, head_key, work_kind, dependency_count, status, attempt_count,
		next_attempt_at_unix, lease_owner, lease_until_unix, last_error, retry_reason, fence,
		completed_at_unix, failed_at_unix, created_at_unix, updated_at_unix, provider_posting, posting_owner
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, '', 0, '', '', 0, 0, 0, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`, s.storeID, identity.Key(), int64(1), work.Queue.String(), work.HeadKey,
		work.Kind.String(), len(work.Dependencies),
		economicRevisionWorkStatePending, now, now, int64(1), owner).Exec(ctx); err != nil {
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

// upgradeEconomicStateToMonetaryTx upgrades an evidence-only delivery row to
// monetary (evidence->monetary escalation for same immutable evidence). It
// never downgrades monetary->evidence. Used on replay-equivalent appends where
// the work marker already exists with a different delivery intent.
//
// R2: the caller holds the F1 marker lock in tx and passes that locked
// snapshot; owner/state authorization is revalidated under the same lock so a
// concurrent shadow->active transition between the rolled-back first tx and
// this retry cannot create V1 monetary work after activation. Stale V1 after
// v2_active fences with zero effects.
//
// S1 delivery-generation transition (requirements 10.6/14.4/17.4): escalation
// is atomic under the existing marker lock plus the queue row lock. The
// monotonically increasing fence is the delivery generation: upgrade bumps it
// and clears any evidence-only lease so a stale worker cannot retire the new
// monetary obligation. A completed/failed evidence delivery is reopened to
// pending exactly once so the monetary obligation is scheduled; the immutable
// evidence marker, valuation/head identity, and shadow no-post semantics are
// untouched. Already-monetary rows are an idempotent no-op (never reopened,
// never double-bumped).
func (s *DurableStore) upgradeEconomicStateToMonetaryTx(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, identity billing.EconomicRevisionIdentity, owner string) error {
	if owner != billing.PostingOwnerV1 && owner != billing.PostingOwnerV2 {
		return fmt.Errorf("%w: unknown economic posting owner %q", billing.ErrInvalidEconomicRevision, owner)
	}
	if err := marker.Validate(); err != nil {
		return fmt.Errorf("%w: upgrade marker: %v", billing.ErrAccountingCutoverInvalid, err)
	}
	if !billing.IsPostingOwnerAllowedForNew(marker.State, owner) {
		return fmt.Errorf("%w: economic upgrade owner %q not allowed for new in %q", billing.ErrPostingOwnershipFence, owner, string(marker.State))
	}
	if owner == billing.PostingOwnerV2 && !billing.IsV2NewWorkAuthorized(marker.State) {
		return fmt.Errorf("%w: V2 economic upgrade requires v2_active (store %q in %q)", billing.ErrCutoverV2NotAuthorized, s.storeID, string(marker.State))
	}
	if owner == billing.PostingOwnerV1 && !billing.IsV1FinancialWorkAllowed(marker.State) {
		return fmt.Errorf("%w: store %q forbids new V1 economic upgrade in %q", billing.ErrAccountingCutoverFence, s.storeID, string(marker.State))
	}
	state, err := s.selectEconomicRevisionWorkStateInTx(ctx, tx, identity, true)
	if err != nil {
		return err
	}
	if state.ProviderPosting == 1 {
		// Already monetary: idempotent no-op. Never reopen a completed
		// monetary obligation and never bump the generation twice.
		return nil
	}
	if state.Fence == math.MaxInt64 {
		return fmt.Errorf("%w: economic revision upgrade counters exhausted", billing.ErrInvalidEconomicRevision)
	}
	now := time.Now().UTC().UnixNano()
	nextFence := state.Fence + 1
	res, err := tx.NewRaw(`UPDATE billing_economic_revision_work_state
SET provider_posting = 1, posting_owner = ?,
    fence = ?,
    lease_owner = '', lease_until_unix = 0,
    status = CASE WHEN status IN ('completed', 'failed') THEN 'pending' ELSE status END,
    next_attempt_at_unix = CASE WHEN status IN ('completed', 'failed') THEN ? ELSE next_attempt_at_unix END,
    completed_at_unix = 0, failed_at_unix = 0,
    updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND provider_posting = 0 AND fence = ?`,
		owner, nextFence, now, now, s.storeID, identity.Key(), int64(1), state.Fence).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: upgrade economic state to monetary: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("billingstore: upgrade economic state rows affected: %w", err)
	}
	if affected == 0 {
		// Concurrent generation transition won the row lock. Re-read: a
		// monetary row means idempotent success; otherwise the caller
		// retries via its outer account-tx budget.
		current, rerr := s.selectEconomicRevisionWorkStateInTx(ctx, tx, identity, true)
		if rerr != nil {
			return rerr
		}
		if current.ProviderPosting == 1 {
			return nil
		}
		return fmt.Errorf("%w: economic upgrade for %q lost generation race", billing.ErrEconomicRevisionClaimLost, identity.Key())
	}
	if affected != 1 {
		return fmt.Errorf("%w: economic upgrade for %q affected %d rows", billing.ErrEconomicRevisionConflict, identity.Key(), affected)
	}
	return nil
}

// ensureEconomicRevisionWorkStateEvidenceInTx persists an explicitly
// evidence-only delivery row (provider_posting=0) for shadow/pure paths.
// Monetary inference never applies here; drain never inventories these rows.
func (s *DurableStore) ensureEconomicRevisionWorkStateEvidenceInTx(ctx context.Context, tx bun.Tx, work billing.EconomicRevisionWork, identity billing.EconomicRevisionIdentity) error {
	now := time.Now().UTC().UnixNano()
	if _, err := tx.NewRaw(`
INSERT INTO billing_economic_revision_work_state(
		store_id, work_id, work_version, queue, head_key, work_kind, dependency_count, status, attempt_count,
		next_attempt_at_unix, lease_owner, lease_until_unix, last_error, retry_reason, fence,
		completed_at_unix, failed_at_unix, created_at_unix, updated_at_unix, provider_posting, posting_owner
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, '', 0, '', '', 0, 0, 0, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`, s.storeID, identity.Key(), int64(1), work.Queue.String(), work.HeadKey,
		work.Kind.String(), len(work.Dependencies),
		economicRevisionWorkStatePending, now, now, int64(0), "").Exec(ctx); err != nil {
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
	query := `SELECT id, store_id, work_id, work_version, queue, head_key, work_kind, dependency_count, status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error, retry_reason, fence, completed_at_unix, failed_at_unix, created_at_unix, updated_at_unix, provider_posting, posting_owner
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
	type claimOut struct {
		claim   billing.EconomicRevisionWorkClaim
		claimed bool
	}
	out, err := withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (claimOut, error) {
		claim, claimed, cerr := s.claimEconomicRevisionWorkAttempt(ctx, work, owner, lease)
		return claimOut{claim: claim, claimed: claimed}, cerr
	})
	return out.claim, out.claimed, err
}

func (s *DurableStore) claimEconomicRevisionWorkAttempt(ctx context.Context, work billing.EconomicRevisionWork, owner string, lease time.Duration) (billing.EconomicRevisionWorkClaim, bool, error) {
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
	if state.Status == economicRevisionWorkStateCompleted || state.Status == economicRevisionWorkStateFailed {
		return billing.EconomicRevisionWorkClaim{}, false, nil
	}
	// F2B cutover gate: monetary provider work requires a classified V1 pin in
	// draining and fences V1 in active; evidence-only work always proceeds.
	if allowed, gerr := s.economicClaimGateAllows(ctx, tx, normalized, state); gerr != nil {
		return billing.EconomicRevisionWorkClaim{}, false, gerr
	} else if !allowed {
		_ = tx.Rollback()
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
SET status = ?, attempt_count = attempt_count + 1, lease_owner = ?, lease_until_unix = ?, last_error = '', retry_reason = '', fence = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status IN (?, ?) AND fence = ?`,
		economicRevisionWorkStateProcessing, owner, leaseUntil.UnixNano(), fence, nowUnix,
		s.storeID, identity.Key(), int64(1), economicRevisionWorkStatePending, economicRevisionWorkStateProcessing, state.Fence).Exec(ctx); err != nil {
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
	reason = billing.BoundedEconomicWorkReasonText(reason)
	if nextAttemptAt.IsZero() {
		nextAttemptAt = time.Now().UTC()
	} else {
		nextAttemptAt = nextAttemptAt.UTC()
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.NewRaw(`
UPDATE billing_economic_revision_work_state
SET status = ?, next_attempt_at_unix = ?, lease_owner = '', lease_until_unix = 0, last_error = ?, retry_reason = ?, completed_at_unix = 0, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status = ? AND lease_owner = ? AND fence = ?`,
		economicRevisionWorkStatePending, nextAttemptAt.UnixNano(), reason, billing.EconomicWorkReasonUnclassified.String(), now,
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

// ClaimEconomicRevisionWorkWithCutover atomically leases one revision and
// issues a current-marker cutover token for monetary provider work in a
// single transaction holding the per-store marker lock. Lease fence, pin
// acquisition/validation (including first-acquisition), and current-marker
// authority issuance commit together, so a marker transition between lease
// and return cannot yield a token authorized for the wrong epoch/state and
// the lease fence is bound to the token. Evidence-only work returns a nil
// cutover (no monetary authority) with its lease. It never swallows
// operational, cancellation or malformed errors; ineligible monetary work is
// withheld (claimed=false) without consuming a lease attempt.
// First-acquisition monetary work acquires its pin atomically and returns a
// nonempty fully validated token bound to the new lease fence.
//
// R2 authoritative: monetary intent comes from mutable delivery state plus
// shape, never payload EvidenceOnly/owner alone, so upgraded shadow is
// monetary here even when the stored payload remains evidence-only.
func (s *DurableStore) ClaimEconomicRevisionWorkWithCutover(ctx context.Context, work billing.EconomicRevisionWork, owner string, lease time.Duration) (billing.EconomicRevisionWorkClaim, *billing.CutoverClaimMetadata, bool, error) {
	type cutoverOut struct {
		claim   billing.EconomicRevisionWorkClaim
		cutover *billing.CutoverClaimMetadata
		claimed bool
	}
	out, err := withAccountTx(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() (cutoverOut, error) {
		claim, cutover, claimed, cerr := s.claimEconomicRevisionWorkWithCutoverAttempt(ctx, work, owner, lease)
		return cutoverOut{claim: claim, cutover: cutover, claimed: claimed}, cerr
	})
	return out.claim, out.cutover, out.claimed, err
}

func (s *DurableStore) claimEconomicRevisionWorkWithCutoverAttempt(ctx context.Context, work billing.EconomicRevisionWork, owner string, leaseDur time.Duration) (billing.EconomicRevisionWorkClaim, *billing.CutoverClaimMetadata, bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return billing.EconomicRevisionWorkClaim{}, nil, false, fmt.Errorf("billingstore: economic revision claim owner is required")
	}
	if leaseDur <= 0 {
		leaseDur = defaultEconomicRevisionLease
	}
	normalized, identity, err := s.normalizeEconomicRevisionStateWork(work)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, fmt.Errorf("billingstore: begin economic cutover claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// R4 atomic: lock the marker first; eligibility, lease, pin and token
	// all observe this single snapshot.
	lockedMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, err
	}
	if err := s.ensureEconomicRevisionWorkStateInTx(ctx, tx, normalized, identity); err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, err
	}
	state, err := s.selectEconomicRevisionWorkStateInTx(ctx, tx, identity, true)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, err
	}
	if state.Status == economicRevisionWorkStateCompleted || state.Status == economicRevisionWorkStateFailed {
		_ = tx.Rollback()
		return billing.EconomicRevisionWorkClaim{}, nil, false, nil
	}
	authoritative := billing.OverlayAuthoritativeEconomicWork(normalized, state.ProviderPosting == 1, state.PostingOwner)
	if !billing.IsMonetaryEconomicRevisionWork(authoritative) {
		// Evidence-only: lease without monetary authority (nil cutover).
		claim, claimed, cerr := s.leaseEconomicStateTx(ctx, tx, identity, state, owner, leaseDur)
		if cerr != nil {
			return billing.EconomicRevisionWorkClaim{}, nil, false, cerr
		}
		if !claimed {
			_ = tx.Rollback()
			return billing.EconomicRevisionWorkClaim{}, nil, false, nil
		}
		if err := tx.Commit(); err != nil {
			return billing.EconomicRevisionWorkClaim{}, nil, false, fmt.Errorf("billingstore: commit evidence cutover lease: %w", err)
		}
		return claim, nil, true, nil
	}
	// Monetary: gate on the locked marker snapshot (not a fresh unlocked
	// read) so a concurrent transition cannot slip between check and lease.
	if allowed, gerr := s.economicCutoverGateAllowsLocked(ctx, tx, lockedMarker, authoritative); gerr != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, gerr
	} else if !allowed {
		_ = tx.Rollback()
		return billing.EconomicRevisionWorkClaim{}, nil, false, nil
	}
	claim, claimed, cerr := s.leaseEconomicStateTx(ctx, tx, identity, state, owner, leaseDur)
	if cerr != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, cerr
	}
	if !claimed {
		_ = tx.Rollback()
		return billing.EconomicRevisionWorkClaim{}, nil, false, nil
	}
	acct, callID, pinKey, kerr := billing.MonetaryEconomicPostingKey(s.storeID, authoritative)
	if kerr != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, kerr
	}
	pinRow, pinFound, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, err
	}
	var pin billing.PostingPin
	if pinFound {
		pin, err = postingOwnershipRowToPin(pinRow)
		if err != nil {
			return billing.EconomicRevisionWorkClaim{}, nil, false, err
		}
		if !isProviderPinEligibleForClaim(lockedMarker, pin) {
			_ = tx.Rollback()
			return billing.EconomicRevisionWorkClaim{}, nil, false, nil
		}
		if pin.AccountID != acct || pin.CallID != callID || pin.OperationKey != pinKey {
			return billing.EconomicRevisionWorkClaim{}, nil, false, fmt.Errorf("%w: economic cutover identity mismatch", billing.ErrPostingOwnershipConflict)
		}
	} else {
		wantOwner := billing.EffectiveEconomicPostingOwner(authoritative)
		if wantOwner == "" {
			wantOwner = lockedMarker.ActivePostingOwner
		}
		if !billing.IsPostingOwnerAllowedForNew(lockedMarker.State, wantOwner) {
			_ = tx.Rollback()
			return billing.EconomicRevisionWorkClaim{}, nil, false, nil
		}
		subjectPayload, merr := jsonMarshalSubject(authoritative.Subject)
		if merr != nil {
			return billing.EconomicRevisionWorkClaim{}, nil, false, merr
		}
		if err := insertEconomicRevisionPinTx(ctx, tx, s, lockedMarker, acct, callID, authoritative.Subject, subjectPayload, pinKey, wantOwner); err != nil {
			return billing.EconomicRevisionWorkClaim{}, nil, false, err
		}
		rerow, refound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
		if rerr != nil {
			return billing.EconomicRevisionWorkClaim{}, nil, false, rerr
		}
		if !refound {
			return billing.EconomicRevisionWorkClaim{}, nil, false, fmt.Errorf("%w: economic pin unavailable after acquire", billing.ErrPostingOwnershipNotFound)
		}
		pin, err = postingOwnershipRowToPin(rerow)
		if err != nil {
			return billing.EconomicRevisionWorkClaim{}, nil, false, err
		}
		if pin.Owner != wantOwner {
			_ = tx.Rollback()
			return billing.EconomicRevisionWorkClaim{}, nil, false, nil
		}
	}
	tok, err := billing.CutoverClaimTokenForEconomicLease(pin, lockedMarker, identity.Key(), claim)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, err
	}
	if tok.OperationKey != pinKey {
		return billing.EconomicRevisionWorkClaim{}, nil, false, fmt.Errorf("%w: economic cutover key mismatch", billing.ErrPostingOwnershipConflict)
	}
	if tok.AccountID != acct || tok.CallID != callID {
		return billing.EconomicRevisionWorkClaim{}, nil, false, fmt.Errorf("%w: economic cutover identity mismatch", billing.ErrPostingOwnershipConflict)
	}
	if err := tok.Validate(); err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return billing.EconomicRevisionWorkClaim{}, nil, false, fmt.Errorf("billingstore: commit economic cutover claim: %w", err)
	}
	out := tok
	return claim, &out, true, nil
}

// leaseEconomicStateTx transitions one pending/expired-processing row to
// processing with a bumped fence in the caller's tx (marker lock held).
// ok=false means not claimable now (leased, backed off, or terminal) without
// error; the caller rolls back without effects.
func (s *DurableStore) leaseEconomicStateTx(ctx context.Context, tx bun.Tx, identity billing.EconomicRevisionIdentity, state economicRevisionWorkStateRow, owner string, leaseDur time.Duration) (billing.EconomicRevisionWorkClaim, bool, error) {
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
	leaseUntil := now.Add(leaseDur).UTC()
	fence := state.Fence + 1
	res, err := tx.NewRaw(`
UPDATE billing_economic_revision_work_state
SET status = ?, attempt_count = attempt_count + 1, lease_owner = ?, lease_until_unix = ?, last_error = '', retry_reason = '', fence = ?, updated_at_unix = ?
WHERE store_id = ? AND work_id = ? AND work_version = ? AND status IN (?, ?) AND fence = ?`,
		economicRevisionWorkStateProcessing, owner, leaseUntil.UnixNano(), fence, nowUnix,
		s.storeID, identity.Key(), int64(1), economicRevisionWorkStatePending, economicRevisionWorkStateProcessing, state.Fence).Exec(ctx)
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, fmt.Errorf("billingstore: claim economic revision work: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return billing.EconomicRevisionWorkClaim{}, false, fmt.Errorf("billingstore: economic lease rows affected: %w", err)
	}
	if affected != 1 {
		return billing.EconomicRevisionWorkClaim{}, false, nil
	}
	return billing.EconomicRevisionWorkClaim{Owner: owner, Fence: uint64(fence), LeaseUntil: leaseUntil}, true, nil
}

// economicCutoverGateAllowsLocked is the marker-locked variant of
// economicClaimGateAllows: eligibility is decided from the locked snapshot,
// never a fresh unlocked read, so a concurrent transition cannot authorize
// the wrong epoch.
func (s *DurableStore) economicCutoverGateAllowsLocked(ctx context.Context, tx bun.Tx, marker billing.AccountingCutoverMarker, work billing.EconomicRevisionWork) (bool, error) {
	if !billing.IsMonetaryEconomicShape(work) {
		return true, nil
	}
	authoritative := work
	// Caller already overlaid authoritative delivery state; shape check
	// above is sufficient for the evidence-only bypass.
	switch marker.State {
	case billing.AccountingCutoverV1Active, billing.AccountingCutoverV2Shadow:
		return true, nil
	case billing.AccountingCutoverV1Draining:
		_, _, pinKey, err := billing.MonetaryEconomicPostingKey(s.storeID, authoritative)
		if err != nil {
			return false, nil
		}
		row, found, lerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
		if lerr != nil || !found {
			return false, nil
		}
		pin, err := postingOwnershipRowToPin(row)
		if err != nil {
			return false, nil
		}
		if pin.Owner != billing.PostingOwnerV1 {
			return false, nil
		}
		return true, nil
	case billing.AccountingCutoverV2Active:
		if authoritative.PostingOwner == billing.PostingOwnerV2 {
			return true, nil
		}
		return false, nil
	default:
		return false, nil
	}
}

func jsonMarshalSubject(subject interface{}) (string, error) {
	payload, err := json.Marshal(subject)
	if err != nil {
		return "", fmt.Errorf("%w: pin subject encode: %v", billing.ErrPostingOwnershipInvalid, err)
	}
	return string(payload), nil
}

func insertEconomicRevisionPinTx(ctx context.Context, tx bun.Tx, s *DurableStore, marker billing.AccountingCutoverMarker, accountID string, callID billing.BillingCallID, subject metering.SubjectRef, subjectJSON string, pinKey, owner string) error {
	now := time.Now().UTC().UnixNano()
	insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
	if s.db.Dialect().Name() == dialect.SQLite {
		insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	}
	if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), pinKey, accountID, callID.String(), subject.BLegID, subject.ProviderChargeID, "", string(subject.Kind), subjectJSON, owner, int64(marker.Version), int64(marker.Epoch), marker.Generation, string(marker.State), string(billing.PostingPinPinned), "", "", now, now, 0).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: acquire economic cutover pin: %w", err)
	}
	return nil
}
