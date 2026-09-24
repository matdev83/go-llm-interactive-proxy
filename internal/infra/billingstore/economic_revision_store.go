package billingstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const economicRevisionWorkKindPrefix = "economic_revision:"

var (
	// ErrEconomicRevisionHeadNotFound identifies a head that has not yet been
	// produced by the pure worker.
	ErrEconomicRevisionHeadNotFound = errors.New("billingstore: economic revision head not found")
	// ErrEconomicRevisionWorkConflict identifies a durable work replay whose
	// immutable payload differs from the original identity.
	ErrEconomicRevisionWorkConflict = errors.New("billingstore: economic revision work conflict")
)

var (
	_ billing.EconomicRevisionWorkReader     = (*DurableStore)(nil)
	_ billing.EconomicRevisionWorkStateStore = (*DurableStore)(nil)
	_ billing.EconomicRevisionResultStore    = (*DurableStore)(nil)
	_ billing.EconomicRevisionResultProbe    = (*DurableStore)(nil)
)

func economicRevisionWorkKind(queue billing.EconomicQueue) string {
	return economicRevisionWorkKindPrefix + queue.String()
}

// AppendEconomicRevisionWork appends an immutable revision marker. The marker
// ID is derived from queue/head/revision/input hash; a duplicate is a no-op,
// while a changed payload under the same identity is rejected by the existing
// economic-work identity fence.
//
// F2B: monetary provider work (provider queue + provider_rating + valid B-leg
// lineage) is fenced as new V1 work in draining/active (F1 marker lock held in
// the same tx). Evidence-only customer rating, reconciliation, shadow, and
// queues without a posting adapter remain operable in all states.
func (s *DurableStore) AppendEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork) error {
	return s.appendEconomicRevisionWorkWithOwner(ctx, work, "")
}

// AppendProviderPostingEconomicRevisionWork appends monetary provider work with
// an explicit posting owner (production enqueue). Empty owner preserves the
// legacy V1 default; "v2" requires v2_active authorization and is the only
// permitted new monetary owner after activation. Evidence-only envelopes fail
// closed here; use AppendEconomicRevisionWork for evidence-only work.
func (s *DurableStore) AppendProviderPostingEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork, owner string) error {
	return s.appendEconomicRevisionWorkWithOwner(ctx, work, owner)
}

// AppendEvidenceEconomicRevisionWork appends an explicitly evidence-only
// marker that never creates monetary intent, pins, or fences. Shadow
// observations/valuations and pure workers without a posting adapter use this
// path so drain never inventories them as payable work.
func (s *DurableStore) AppendEvidenceEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork) error {
	return withAccountTxErr(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() error {
		return s.appendEvidenceEconomicRevisionWorkAttempt(ctx, work)
	})
}

func (s *DurableStore) appendEvidenceEconomicRevisionWorkAttempt(ctx context.Context, work billing.EconomicRevisionWork) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	normalized, err := work.Normalize()
	if err != nil {
		return err
	}
	identity, err := normalized.Identity()
	if err != nil {
		return err
	}
	if normalized.Subject.StoreID != s.storeID {
		return fmt.Errorf("%w: economic revision subject store", ErrEconomicsOutOfScope)
	}
	if normalized.EvidenceRevision > math.MaxInt64 {
		return fmt.Errorf("%w: evidence revision exceeds database range", billing.ErrInvalidEconomicRevision)
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("billingstore: encode economic revision work: %w", err)
	}
	marker := EconomicWorkMarker{
		ID: identity.Key(), Version: 1, Kind: economicRevisionWorkKind(normalized.Queue),
		SubjectKind: normalized.Subject.Kind, SubjectID: subjectIDForEconomics(normalized.Subject),
		InputSetHash: normalized.InputSetHash, PayloadJSON: payload, Status: "pending",
		CreatedAt: normalized.CreatedAt,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: economic revision work begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: hold the per-store marker lock so activation serializes with enqueue,
	// but evidence-only work never consults the V1/V2 new-work fence.
	if _, err := s.ensureAndLockAccountingCutoverTx(ctx, tx); err != nil {
		return err
	}
	if err := s.AppendEconomicWorkInTx(ctx, tx, marker); err != nil {
		_ = tx.Rollback()
		if errors.Is(err, ErrIdentityConflict) {
			equivalent, replayErr := s.economicRevisionReplayEquivalent(ctx, normalized, identity)
			if replayErr != nil {
				return replayErr
			}
			if equivalent {
				return nil
			}
			return fmt.Errorf("%w: %v", ErrEconomicRevisionWorkConflict, err)
		}
		return err
	}
	if err := s.ensureEconomicRevisionWorkStateEvidenceInTx(ctx, tx, normalized, identity); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: economic revision work commit: %w", err)
	}
	return nil
}

func (s *DurableStore) appendEconomicRevisionWorkWithOwner(ctx context.Context, work billing.EconomicRevisionWork, owner string) error {
	return withAccountTxErr(ctx, accountTxRetry{Attempts: 40, Delay: 3 * time.Millisecond}, func() error {
		return s.appendEconomicRevisionWorkWithOwnerAttempt(ctx, work, owner)
	})
}

func (s *DurableStore) appendEconomicRevisionWorkWithOwnerAttempt(ctx context.Context, work billing.EconomicRevisionWork, owner string) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	normalized, err := work.Normalize()
	if err != nil {
		return err
	}
	identity, err := normalized.Identity()
	if err != nil {
		return err
	}
	if normalized.Subject.StoreID != s.storeID {
		return fmt.Errorf("%w: economic revision subject store", ErrEconomicsOutOfScope)
	}
	if normalized.EvidenceRevision > math.MaxInt64 {
		return fmt.Errorf("%w: evidence revision exceeds database range", billing.ErrInvalidEconomicRevision)
	}
	isMonetary := billing.IsMonetaryEconomicRevisionWork(normalized)
	effectiveOwner := owner
	if isMonetary && effectiveOwner == "" {
		effectiveOwner = billing.PostingOwnerV1
	}
	if effectiveOwner != "" && effectiveOwner != billing.PostingOwnerV1 && effectiveOwner != billing.PostingOwnerV2 {
		return fmt.Errorf("%w: %w: unknown economic posting owner %q", billing.ErrInvalidEconomicRevision, billing.ErrInvalidRecord, owner)
	}
	if effectiveOwner != "" && !isMonetary {
		return fmt.Errorf("%w: explicit %q posting requires monetary provider work", billing.ErrInvalidEconomicRevision, effectiveOwner)
	}
	// R2 authoritative intent: stamp the immutable payload with the effective
	// monetary owner so payload and mutable delivery state can never disagree
	// for newly enqueued work. Identity excludes owner/evidence intent, so the
	// work ID is unchanged; the fingerprint covers the stamped payload.
	if isMonetary {
		normalized.PostingOwner = effectiveOwner
		normalized.EvidenceOnly = false
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("billingstore: encode economic revision work: %w", err)
	}
	marker := EconomicWorkMarker{
		ID: identity.Key(), Version: 1, Kind: economicRevisionWorkKind(normalized.Queue),
		SubjectKind: normalized.Subject.Kind, SubjectID: subjectIDForEconomics(normalized.Subject),
		InputSetHash: normalized.InputSetHash, PayloadJSON: payload, Status: "pending",
		CreatedAt: normalized.CreatedAt,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: economic revision work begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: lock the per-store marker in the SAME tx as work/state inserts so
	// concurrent activation serializes instead of reading stale state.
	lockedMarker, err := s.ensureAndLockAccountingCutoverTx(ctx, tx)
	if err != nil {
		return err
	}
	if isMonetary {
		if effectiveOwner == billing.PostingOwnerV2 {
			if !billing.IsV2NewWorkAuthorized(lockedMarker.State) {
				return fmt.Errorf("%w: V2 economic work requires v2_active (store %q in %q)", billing.ErrCutoverV2NotAuthorized, s.storeID, string(lockedMarker.State))
			}
		} else {
			if !billing.IsV1FinancialWorkAllowed(lockedMarker.State) {
				return fmt.Errorf("%w: store %q forbids new V1 economic work in %q", billing.ErrAccountingCutoverFence, s.storeID, string(lockedMarker.State))
			}
		}
	}
	if err := s.AppendEconomicWorkInTx(ctx, tx, marker); err != nil {
		_ = tx.Rollback()
		if errors.Is(err, ErrIdentityConflict) {
			// Evidence delivery can legitimately change transport metadata (for
			// example ReceivedAt) while retaining the same source revision and
			// input hash. Treat that replay as a no-op, but keep rejecting a
			// changed semantic payload under the same immutable identity.
			// Delivery intent is queue state, not evidence: ignore intent in
			// equivalence, then upgrade evidence->monetary state if needed.
			equivalent, replayErr := s.economicRevisionReplayEquivalent(ctx, normalized, identity)
			if replayErr != nil {
				return replayErr
			}
			if equivalent {
				if isMonetary {
					utx, uerr := s.db.BeginTx(ctx, nil)
					if uerr != nil {
						return fmt.Errorf("billingstore: economic upgrade begin: %w", uerr)
					}
					defer func() { _ = utx.Rollback() }()
					// R2: reacquire the F1 marker lock in the retry tx and
					// revalidate owner/state authorization under that same
					// lock, so a concurrent shadow->active transition between
					// the rolled-back first tx and this retry cannot create
					// V1 monetary work after activation.
					upgradeMarker, lerr := s.ensureAndLockAccountingCutoverTx(ctx, utx)
					if lerr != nil {
						return lerr
					}
					if err := s.upgradeEconomicStateToMonetaryTx(ctx, utx, upgradeMarker, identity, effectiveOwner); err != nil {
						return err
					}
					if err := utx.Commit(); err != nil {
						return fmt.Errorf("billingstore: economic upgrade commit: %w", err)
					}
				}
				return nil
			}
			return fmt.Errorf("%w: %v", ErrEconomicRevisionWorkConflict, err)
		}
		return err
	}
	if isMonetary {
		if err := s.ensureEconomicRevisionWorkStateWithOwnerInTx(ctx, tx, normalized, identity, effectiveOwner); err != nil {
			return err
		}
	} else if err := s.ensureEconomicRevisionWorkStateInTx(ctx, tx, normalized, identity); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: economic revision work commit: %w", err)
	}
	return nil
}

func (s *DurableStore) economicRevisionReplayEquivalent(ctx context.Context, incoming billing.EconomicRevisionWork, identity billing.EconomicRevisionIdentity) (bool, error) {
	var row struct {
		Kind         string `bun:"kind"`
		InputSetHash string `bun:"input_set_hash"`
		PayloadJSON  string `bun:"payload_json"`
	}
	if err := s.db.NewRaw(`SELECT kind, input_set_hash, payload_json FROM billing_economic_work WHERE store_id = ? AND work_id = ? AND work_version = 1 LIMIT 1`, s.storeID, identity.Key()).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("billingstore: economic revision replay lookup: %w", err)
	}
	if row.Kind != economicRevisionWorkKind(incoming.Queue) || row.InputSetHash != incoming.InputSetHash {
		return false, nil
	}
	var existing billing.EconomicRevisionWork
	if err := json.Unmarshal([]byte(row.PayloadJSON), &existing); err != nil {
		return false, fmt.Errorf("billingstore: decode economic revision replay %q: %w", identity.Key(), err)
	}
	stored, err := existing.Normalize()
	if err != nil {
		return false, fmt.Errorf("billingstore: normalize economic revision replay %q: %w", identity.Key(), err)
	}
	storedIdentity, err := stored.Identity()
	if err != nil {
		return false, fmt.Errorf("billingstore: identify economic revision replay %q: %w", identity.Key(), err)
	}
	if storedIdentity != identity {
		return false, nil
	}
	incomingFingerprint, err := economicRevisionReplayFingerprint(incoming)
	if err != nil {
		return false, err
	}
	storedFingerprint, err := economicRevisionReplayFingerprint(stored)
	if err != nil {
		return false, err
	}
	return incomingFingerprint == storedFingerprint, nil
}

// economicRevisionReplayFingerprint excludes transport-only receipt metadata
// while retaining the full source identity and semantic observation payload.
// The durable row still keeps the first full envelope for audit/history.
func economicRevisionReplayFingerprint(work billing.EconomicRevisionWork) (string, error) {
	normalized, err := work.Normalize()
	if err != nil {
		return "", err
	}
	semantic := normalized
	semantic.CreatedAt = time.Unix(0, 0).UTC()
	// Delivery intent (F2B posting owner/evidence flag) is queue state, not
	// immutable evidence: same observations with different intent share one
	// work identity; state upgrades evidence->monetary without forking work.
	semantic.PostingOwner = ""
	semantic.EvidenceOnly = false
	semantic.Input = normalized.Input.Clone()
	type observationReplayIdentity struct {
		Identity   string `json:"identity"`
		ReplayHash string `json:"replay_hash"`
	}
	semanticObservations := make([]observationReplayIdentity, 0, len(semantic.Input.Observations))
	for _, observation := range semantic.Input.Observations {
		replayHash, err := observation.ReplayFingerprint()
		if err != nil {
			return "", fmt.Errorf("billingstore: economic revision replay observation: %w", err)
		}
		semanticObservations = append(semanticObservations, observationReplayIdentity{Identity: observation.IdentityKey(), ReplayHash: replayHash})
	}
	slices.SortFunc(semanticObservations, func(left, right observationReplayIdentity) int {
		if left.Identity != right.Identity {
			return strings.Compare(left.Identity, right.Identity)
		}
		return strings.Compare(left.ReplayHash, right.ReplayHash)
	})
	semantic.Input.Observations = nil
	payload, err := json.Marshal(struct {
		Work         billing.EconomicRevisionWork `json:"work"`
		Observations any                          `json:"observations"`
	}{Work: semantic, Observations: semanticObservations})
	if err != nil {
		return "", fmt.Errorf("billingstore: economic revision replay fingerprint: %w", err)
	}
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return "", fmt.Errorf("billingstore: economic revision replay canonical payload: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// EnqueueEconomicRevision is a concise composition spelling retained for
// callers that treat the durable marker as a queue operation.
func (s *DurableStore) EnqueueEconomicRevision(ctx context.Context, work billing.EconomicRevisionWork) error {
	return s.AppendEconomicRevisionWork(ctx, work)
}

// ListPendingEconomicRevisionWork returns only one queue's due revision
// markers. Immutable markers remain durable history; mutable state retires
// completed work and hides active/future-retry claims so a bounded page keeps
// making progress when late corrections arrive. Retry attempts are ordered
// after never-attempted work to keep a repeatedly failing item from occupying
// every page.
//
// R2 authoritative intent: each returned work overlays mutable delivery state
// (provider_posting/posting_owner) onto the immutable payload, so explicit V2
// with legacy-empty payload and upgraded shadow (payload evidence-only, state
// monetary) are consumed as monetary V2/V1. Evidence identity (work ID) is
// validated before overlay and never changes.
func (s *DurableStore) ListPendingEconomicRevisionWork(ctx context.Context, queue billing.EconomicQueue, limit int) ([]billing.EconomicRevisionWork, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	if err := queue.Validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > v2EconomicsHardPageMaximum {
		return nil, ErrEconomicsPageSizeExceeded
	}
	now := time.Now().UTC().UnixNano()
	var rows []struct {
		economicRevisionWorkRow
		ProviderPosting int64  `bun:"provider_posting"`
		PostingOwner    string `bun:"posting_owner"`
		StateID         *int64 `bun:"state_id"`
	}
	if err := s.db.NewRaw(`
SELECT w.work_id, w.work_version, w.input_set_hash, w.payload_json, w.fingerprint, w.created_at_unix,
	COALESCE(q.provider_posting, 0) AS provider_posting, COALESCE(q.posting_owner, '') AS posting_owner, q.id AS state_id
FROM billing_economic_work AS w
LEFT JOIN billing_economic_revision_work_state AS q
	ON q.store_id = w.store_id AND q.work_id = w.work_id AND q.work_version = w.work_version
WHERE w.store_id = ? AND w.kind = ? AND w.status = 'pending'
	AND (q.id IS NULL OR (
		q.status IN ('pending', 'processing')
		AND q.next_attempt_at_unix <= ?
		AND (q.status <> 'processing' OR q.lease_until_unix <= ?)
	))
	ORDER BY COALESCE(q.attempt_count, 0) ASC, w.created_at_unix ASC, w.work_id ASC, w.work_version ASC, w.id ASC
LIMIT ?`, s.storeID, economicRevisionWorkKind(queue), now, now, limit).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("billingstore: list %s economic revisions: %w", queue, err)
	}
	items := make([]billing.EconomicRevisionWork, 0, len(rows))
	for _, row := range rows {
		normalized, _, err := s.economicRevisionWorkFromRow(row.economicRevisionWorkRow)
		if err != nil {
			return nil, err
		}
		if row.StateID != nil {
			normalized = billing.OverlayAuthoritativeEconomicWork(normalized, row.ProviderPosting == 1, row.PostingOwner)
		}
		items = append(items, normalized)
	}
	return items, nil
}

// economicRevisionWorkFromRow decodes and self-validates one durable immutable
// work marker against its canonical identity, input hash and fingerprint.
func (s *DurableStore) economicRevisionWorkFromRow(row economicRevisionWorkRow) (billing.EconomicRevisionWork, billing.EconomicRevisionIdentity, error) {
	if row.WorkVersion != 1 {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("%w: unsupported work version %d for %q", ErrEconomicRevisionWorkConflict, row.WorkVersion, row.WorkID)
	}
	var work billing.EconomicRevisionWork
	if err := json.Unmarshal([]byte(row.PayloadJSON), &work); err != nil {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("billingstore: decode economic revision work %q: %w", row.WorkID, err)
	}
	normalized, err := work.Normalize()
	if err != nil {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("billingstore: validate economic revision work %q: %w", row.WorkID, err)
	}
	if normalized.Subject.StoreID != s.storeID {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("%w: economic revision subject store for %q", ErrEconomicsOutOfScope, row.WorkID)
	}
	identity, err := normalized.Identity()
	if err != nil {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("billingstore: identify economic revision work %q: %w", row.WorkID, err)
	}
	if identity.Key() != row.WorkID || normalized.InputSetHash != row.InputSetHash {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("%w: durable identity projection for %q", ErrEconomicRevisionWorkConflict, row.WorkID)
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("billingstore: canonicalize economic revision work %q: %w", row.WorkID, err)
	}
	payload, err = canonicalJSON(payload)
	if err != nil {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("billingstore: canonicalize economic revision work payload %q: %w", row.WorkID, err)
	}
	fingerprint := sha256.Sum256(payload)
	if row.Fingerprint != hex.EncodeToString(fingerprint[:]) {
		return billing.EconomicRevisionWork{}, billing.EconomicRevisionIdentity{}, fmt.Errorf("%w: durable payload fingerprint for %q", ErrEconomicRevisionWorkConflict, row.WorkID)
	}
	return normalized, identity, nil
}

// ListEconomicRevisionWork is an alias for the bounded pending queue reader.
func (s *DurableStore) ListEconomicRevisionWork(ctx context.Context, queue billing.EconomicQueue, limit int) ([]billing.EconomicRevisionWork, error) {
	return s.ListPendingEconomicRevisionWork(ctx, queue, limit)
}

type economicRevisionWorkRow struct {
	WorkID       string `bun:"work_id"`
	WorkVersion  int64  `bun:"work_version"`
	InputSetHash string `bun:"input_set_hash"`
	PayloadJSON  string `bun:"payload_json"`
	Fingerprint  string `bun:"fingerprint"`
	CreatedAt    int64  `bun:"created_at_unix"`
}

// HasEconomicRevisionResult probes the immutable valuation history using the
// deterministic revision valuation identity. It performs no head/balance
// mutation and lets a restarted worker avoid re-rating an already completed
// revision.
func (s *DurableStore) HasEconomicRevisionResult(ctx context.Context, identity billing.EconomicRevisionIdentity) (bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return false, err
	}
	validated := identity
	if err := validated.Validate(); err != nil {
		return false, err
	}
	var valuationCount int
	if err := s.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ?`, s.storeID, validated.ValuationKey(), int64(economics.ValuationVersionV2)).Scan(ctx, &valuationCount); err != nil {
		return false, fmt.Errorf("billingstore: probe economic revision valuation: %w", err)
	}
	if valuationCount == 0 {
		return false, nil
	}
	// A valuation without its rebuildable head is an incomplete projection. Let
	// replay repair the head atomically instead of treating the row as done.
	var headCount int
	if err := s.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_valuation_heads WHERE store_id = ? AND queue = ? AND head_key = ?`, s.storeID, validated.Queue.String(), validated.HeadKey).Scan(ctx, &headCount); err != nil {
		return false, fmt.Errorf("billingstore: probe economic revision head: %w", err)
	}
	return headCount != 0, nil
}

var _ billing.EconomicRevisionValuationLoader = (*DurableStore)(nil)

// LoadEconomicRevisionValuation returns the exact immutable valuation persisted
// for one revision identity. It is the durable reader for provider-posting
// recovery: the returned InputSetHash is the full allocation-aware identity
// used on the fresh path, including the lenient legacy work/output seam.
// A missing valuation is reported as sql.ErrNoRows wrapped for retry; callers
// must not guess the identity from work alone.
func (s *DurableStore) LoadEconomicRevisionValuation(ctx context.Context, identity billing.EconomicRevisionIdentity) (economics.Valuation, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.Valuation{}, err
	}
	if err := identity.Validate(); err != nil {
		return economics.Valuation{}, err
	}
	valuation, err := s.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	if err != nil {
		return economics.Valuation{}, err
	}
	if valuation.ID != identity.ValuationKey() {
		return economics.Valuation{}, fmt.Errorf("%w: valuation id got=%q want=%q", billing.ErrEconomicRevisionInputMismatch, valuation.ID, identity.ValuationKey())
	}
	if valuation.InputSetHash == "" {
		return economics.Valuation{}, fmt.Errorf("%w: persisted valuation lacks input identity", billing.ErrEconomicRevisionInputMismatch)
	}
	return valuation, nil
}

// AppendEconomicRevisionResult atomically persists pure valuation,
// reconciliation and the rebuildable current head. No account, exposure,
// journal or customer-unit table is read or written by this transaction.
func (s *DurableStore) AppendEconomicRevisionResult(ctx context.Context, work billing.EconomicRevisionWork, result billing.EconomicRevisionResult) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	normalizedWork, err := work.Normalize()
	if err != nil {
		return err
	}
	identity, err := normalizedWork.Identity()
	if err != nil {
		return err
	}
	if result.Valuation.ID != identity.ValuationKey() {
		return fmt.Errorf("%w: valuation id got=%q want=%q", billing.ErrEconomicRevisionInputMismatch, result.Valuation.ID, identity.ValuationKey())
	}
	canonicalValuation, _, err := canonicalValuationForStore(s.storeID, result.Valuation)
	if err != nil {
		return err
	}
	if !sameEconomicSubject(canonicalValuation.Subject, normalizedWork.Subject) {
		return fmt.Errorf("%w: valuation subject", billing.ErrEconomicRevisionSubjectMismatch)
	}
	if canonicalValuation.Basis != normalizedWork.Input.Basis {
		return fmt.Errorf("%w: valuation basis got=%q want=%q", billing.ErrEconomicRevisionBasisMismatch, canonicalValuation.Basis, normalizedWork.Input.Basis)
	}
	// Dual identity fence. The immutable work envelope authenticates only the
	// claimed observation plane, so the observation-only projection derived from
	// the valuation's retained references must equal the work hash. The stored
	// valuation identity is broader and additionally authenticates allocation
	// coverage references; canonicalValuationForStore has already verified and
	// filled it, and the explicit recomputation below keeps both fences
	// independent instead of collapsing them into one comparison.
	observationHash, err := economics.CanonicalInputSetHash(canonicalValuation.Basis, canonicalValuation.InputObservations)
	if err != nil {
		return fmt.Errorf("%w: valuation observation inputs: %v", billing.ErrEconomicRevisionInputMismatch, err)
	}
	if observationHash != normalizedWork.InputSetHash {
		return fmt.Errorf("%w: valuation observation hash got=%q want=%q", billing.ErrEconomicRevisionInputMismatch, observationHash, normalizedWork.InputSetHash)
	}
	fullHash, err := economics.CanonicalValuationInputSetHash(canonicalValuation.Basis, canonicalValuation.InputObservations, canonicalValuation.AllocationCoverageRefs)
	if err != nil {
		return fmt.Errorf("%w: valuation allocation inputs: %v", billing.ErrEconomicRevisionInputMismatch, err)
	}
	if canonicalValuation.InputSetHash != fullHash {
		return fmt.Errorf("%w: valuation full hash got=%q want=%q", billing.ErrEconomicRevisionInputMismatch, canonicalValuation.InputSetHash, fullHash)
	}
	// An allocation-aware rater may only price the allocation coverage set
	// declared by the immutable work; substituting an unclaimed allocation
	// revision is a trust-boundary violation, not a distinct revision.
	workAllocations, err := economics.CanonicalAllocationCoverageRefs(normalizedWork.Input.AllocationCoverageRefs)
	if err != nil {
		return fmt.Errorf("%w: work allocation coverage: %v", billing.ErrEconomicRevisionInputMismatch, err)
	}
	valuationAllocations, err := economics.CanonicalAllocationCoverageRefs(canonicalValuation.AllocationCoverageRefs)
	if err != nil {
		return fmt.Errorf("%w: valuation allocation coverage: %v", billing.ErrEconomicRevisionInputMismatch, err)
	}
	if len(workAllocations) != 0 && !slices.Equal(workAllocations, valuationAllocations) {
		return fmt.Errorf("%w: valuation allocation coverage does not match the immutable work claim", billing.ErrEconomicRevisionInputMismatch)
	}
	result.Valuation = canonicalValuation
	if result.Reconciliation != nil && result.Reconciliation.ID != identity.ReconciliationKey() {
		return fmt.Errorf("%w: reconciliation id got=%q want=%q", billing.ErrEconomicRevisionInputMismatch, result.Reconciliation.ID, identity.ReconciliationKey())
	}
	if result.Reconciliation != nil {
		normalizedReconciliation, normalizeErr := result.Reconciliation.Normalize()
		if normalizeErr != nil {
			return normalizeErr
		}
		if !sameEconomicSubject(normalizedReconciliation.Subject, normalizedWork.Subject) {
			return fmt.Errorf("%w: reconciliation subject", billing.ErrEconomicRevisionSubjectMismatch)
		}
		if normalizedReconciliation.Basis != normalizedWork.Input.Basis {
			return fmt.Errorf("%w: reconciliation basis got=%q want=%q", billing.ErrEconomicRevisionBasisMismatch, normalizedReconciliation.Basis, normalizedWork.Input.Basis)
		}
		if normalizedReconciliation.InputSetHash != normalizedWork.InputSetHash {
			return fmt.Errorf("%w: reconciliation hash got=%q want=%q", billing.ErrEconomicRevisionInputMismatch, normalizedReconciliation.InputSetHash, normalizedWork.InputSetHash)
		}
		if normalizedReconciliation.Version != identity.EvidenceRevision {
			return fmt.Errorf("%w: reconciliation version got=%d want=%d", billing.ErrEconomicRevisionInputMismatch, normalizedReconciliation.Version, identity.EvidenceRevision)
		}
		result.Reconciliation = &normalizedReconciliation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingstore: economic revision result begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.AppendValuationInTx(ctx, tx, result.Valuation); err != nil {
		if errors.Is(err, ErrIdentityConflict) {
			return fmt.Errorf("%w: valuation: %v", billing.ErrEconomicRevisionConflict, err)
		}
		return err
	}
	var reconciliationFingerprint string
	var reconciliationID string
	var reconciliationVersion uint64
	if result.Reconciliation != nil {
		reconciliation, err := result.Reconciliation.Normalize()
		if err != nil {
			return err
		}
		record := ReconciliationRecord{
			ID: reconciliation.ID, Version: reconciliation.Version, Subject: reconciliation.Subject,
			Scope: reconciliation.Scope, Basis: reconciliation.Basis, InputSetHash: reconciliation.InputSetHash,
			LocalInputHash: reconciliation.LocalInputHash, ProviderInputHash: reconciliation.ProviderInputHash,
			PolicyID: reconciliation.PolicyID, PolicyVersion: reconciliation.PolicyVersion,
			ResultJSON: append(json.RawMessage(nil), reconciliation.ResultJSON...), CreatedAt: reconciliation.CreatedAt,
		}
		if err := s.AppendReconciliationInTx(ctx, tx, record); err != nil {
			if errors.Is(err, ErrIdentityConflict) {
				return fmt.Errorf("%w: reconciliation: %v", billing.ErrEconomicRevisionConflict, err)
			}
			return err
		}
		reconciliationFingerprint = record.Fingerprint()
		reconciliationID = record.ID
		reconciliationVersion = record.Version
	}
	if err := s.appendEconomicValuationHeadInTx(ctx, tx, normalizedWork, identity, result.Valuation, reconciliationID, reconciliationVersion, reconciliationFingerprint); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingstore: economic revision result commit: %w", err)
	}
	return nil
}

func sameEconomicSubject(left, right metering.SubjectRef) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// loadValuationInputObservationsInTx reads the immutable reference set that
// authorized a previously selected head. Head rows retain only the valuation
// identity and input hash; the canonical valuation payload is the durable
// source for same-revision evidence containment decisions.
func loadValuationInputObservationsInTx(ctx context.Context, q bun.IDB, storeID, valuationID string, valuationVersion int64) ([]metering.ObservationRef, bool, error) {
	if strings.TrimSpace(valuationID) == "" || valuationVersion <= 0 {
		return nil, false, nil
	}
	var payload string
	if err := q.NewRaw(`SELECT canonical_json FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ? LIMIT 1`, storeID, valuationID, valuationVersion).Scan(ctx, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("billingstore: load valuation evidence %q/%d: %w", valuationID, valuationVersion, err)
	}
	var valuation economics.Valuation
	if err := json.Unmarshal([]byte(payload), &valuation); err != nil {
		return nil, false, fmt.Errorf("billingstore: decode valuation evidence %q/%d: %w", valuationID, valuationVersion, err)
	}
	return append([]metering.ObservationRef(nil), valuation.InputObservations...), true, nil
}

// PersistEconomicRevision is an alias used by process composition code.
func (s *DurableStore) PersistEconomicRevision(ctx context.Context, work billing.EconomicRevisionWork, result billing.EconomicRevisionResult) error {
	return s.AppendEconomicRevisionResult(ctx, work, result)
}

type economicValuationHeadRow struct {
	ID                    int64  `bun:"id"`
	StoreID               string `bun:"store_id"`
	Queue                 string `bun:"queue"`
	HeadKey               string `bun:"head_key"`
	SubjectKind           string `bun:"subject_kind"`
	SubjectID             string `bun:"subject_id"`
	SubjectJSON           string `bun:"subject_json"`
	EvidenceRevision      int64  `bun:"evidence_revision"`
	InputSetHash          string `bun:"input_set_hash"`
	WorkID                string `bun:"work_id"`
	ValuationID           string `bun:"valuation_id"`
	ValuationVersion      int64  `bun:"valuation_version"`
	ReconciliationID      string `bun:"reconciliation_id"`
	ReconciliationVersion int64  `bun:"reconciliation_version"`
	ValuationFingerprint  string `bun:"valuation_fingerprint"`
	ReconciliationFP      string `bun:"reconciliation_fingerprint"`
	Fingerprint           string `bun:"fingerprint"`
	HeadVersion           int64  `bun:"head_version"`
	Fence                 int64  `bun:"fence"`
	CreatedAt             int64  `bun:"created_at_unix"`
	UpdatedAt             int64  `bun:"updated_at_unix"`
	// DerivationHash and DependenciesHash persist the durable current winning
	// ordering tuple's full derivation identity. UpdatedAt already persists
	// the winning work's CreatedAt; these columns persist every other field
	// used by the same-observation-plane comparison so delayed and equal-time
	// arrivals order deterministically. See appendEconomicValuationHeadInTx.
	DerivationHash   string `bun:"derivation_hash"`
	DependenciesHash string `bun:"dependencies_hash"`
}

func (s *DurableStore) appendEconomicValuationHeadInTx(ctx context.Context, tx bun.Tx, work billing.EconomicRevisionWork, identity billing.EconomicRevisionIdentity, valuation economics.Valuation, reconciliationID string, reconciliationVersion uint64, reconciliationFingerprint string) error {
	if identity.EvidenceRevision > math.MaxInt64 {
		return fmt.Errorf("%w: evidence revision exceeds database range", billing.ErrInvalidEconomicRevision)
	}
	if reconciliationVersion > math.MaxInt64 {
		return fmt.Errorf("%w: derived version exceeds database range", billing.ErrInvalidEconomicRevision)
	}
	if work.Subject.StoreID != s.storeID {
		return fmt.Errorf("%w: head subject store", ErrEconomicsOutOfScope)
	}
	subjectJSON, err := json.Marshal(work.Subject)
	if err != nil {
		return fmt.Errorf("billingstore: encode economic head subject: %w", err)
	}
	valuationFingerprint := valuation.Fingerprint()
	fingerprintInput := identity.Key() + "\x00" + valuationFingerprint + "\x00" + reconciliationFingerprint
	headDigest := sha256.Sum256([]byte(fingerprintInput))
	headFingerprint := hex.EncodeToString(headDigest[:])
	incoming := economicValuationHeadRow{
		StoreID: s.storeID, Queue: work.Queue.String(), HeadKey: work.HeadKey,
		SubjectKind: string(work.Subject.Kind), SubjectID: subjectIDForEconomics(work.Subject), SubjectJSON: string(subjectJSON),
		EvidenceRevision: int64(identity.EvidenceRevision), InputSetHash: identity.InputSetHash, WorkID: identity.Key(),
		ValuationID: valuation.ID, ValuationVersion: int64(valuation.Version), ReconciliationID: reconciliationID,
		ReconciliationVersion: int64(reconciliationVersion), ValuationFingerprint: valuationFingerprint,
		ReconciliationFP: reconciliationFingerprint, Fingerprint: headFingerprint,
		HeadVersion: 1, Fence: 1, CreatedAt: work.CreatedAt.UnixNano(), UpdatedAt: work.CreatedAt.UnixNano(),
		DerivationHash: identity.DerivationHash, DependenciesHash: identity.DependenciesHash,
	}
	if _, err := tx.NewRaw(`INSERT INTO billing_economic_valuation_heads(
		store_id, queue, head_key, subject_kind, subject_id, subject_json, evidence_revision, input_set_hash,
		work_id, valuation_id, valuation_version, reconciliation_id, reconciliation_version,
		valuation_fingerprint, reconciliation_fingerprint, fingerprint, head_version, fence, created_at_unix, updated_at_unix,
		derivation_hash, dependencies_hash
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		incoming.StoreID, incoming.Queue, incoming.HeadKey, incoming.SubjectKind, incoming.SubjectID, incoming.SubjectJSON,
		incoming.EvidenceRevision, incoming.InputSetHash, incoming.WorkID, incoming.ValuationID, incoming.ValuationVersion,
		incoming.ReconciliationID, incoming.ReconciliationVersion, incoming.ValuationFingerprint, incoming.ReconciliationFP,
		incoming.Fingerprint, incoming.HeadVersion, incoming.Fence, incoming.CreatedAt, incoming.UpdatedAt,
		incoming.DerivationHash, incoming.DependenciesHash).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert economic valuation head: %w", err)
	}
	var existing economicValuationHeadRow
	selectSQL := `SELECT id, store_id, queue, head_key, subject_kind, subject_id, subject_json, evidence_revision, input_set_hash, work_id, valuation_id, valuation_version, reconciliation_id, reconciliation_version, valuation_fingerprint, reconciliation_fingerprint, fingerprint, head_version, fence, created_at_unix, updated_at_unix, derivation_hash, dependencies_hash FROM billing_economic_valuation_heads WHERE store_id = ? AND queue = ? AND head_key = ? LIMIT 1`
	if tx.Dialect().Name() == dialect.PG {
		selectSQL += ` FOR UPDATE`
	}
	if err := tx.NewRaw(selectSQL, s.storeID, incoming.Queue, incoming.HeadKey).Scan(ctx, &existing); err != nil {
		return fmt.Errorf("billingstore: read economic valuation head after insert: %w", err)
	}
	if existing.WorkID == incoming.WorkID {
		if existing.Fingerprint != incoming.Fingerprint {
			return fmt.Errorf("%w: head identity=%q", billing.ErrEconomicRevisionConflict, incoming.WorkID)
		}
		return nil
	}
	existingIdentity := billing.EconomicRevisionIdentity{
		Queue:            billing.EconomicQueue(existing.Queue),
		HeadKey:          existing.HeadKey,
		EvidenceRevision: uint64(maxInt64ToZero(existing.EvidenceRevision)),
		InputSetHash:     existing.InputSetHash,
		DerivationHash:   existing.DerivationHash,
		DependenciesHash: existing.DependenciesHash,
	}
	existingErr := existingIdentity.Validate()
	// A legacy row backfilled without derivation columns validates with empty
	// derivation hashes; that preserves observation-only legacy ordering while
	// allocation-aware rows carry their full identity. An invalid stored
	// identity is treated as older so recovery can repair it (same as before).
	if existingErr == nil {
		advance := existingIdentity.Less(identity)
		if existingIdentity.EvidenceRevision == identity.EvidenceRevision {
			if existingIdentity.InputSetHash != identity.InputSetHash {
				existingRefs, found, evidenceErr := loadValuationInputObservationsInTx(ctx, tx, s.storeID, existing.ValuationID, existing.ValuationVersion)
				if evidenceErr != nil {
					return evidenceErr
				}
				if !found {
					return fmt.Errorf("%w: existing valuation evidence %q/%d is unavailable", billing.ErrEconomicRevisionFence, existing.ValuationID, existing.ValuationVersion)
				}
				relation, relationErr := billing.CompareEconomicEvidenceSets(existingRefs, valuation.InputObservations)
				if relationErr != nil {
					return fmt.Errorf("%w: compare same-revision evidence: %v", billing.ErrEconomicRevisionFence, relationErr)
				}
				switch relation {
				case billing.EconomicEvidenceSetCandidateSuperset:
					advance = true
				case billing.EconomicEvidenceSetCandidateSubset:
					advance = false
				case billing.EconomicEvidenceSetEqual:
					// Equal reference sets are semantically equivalent. Hash ordering
					// remains only a deterministic tie-breaker for that case.
					advance = existingIdentity.Less(identity)
				case billing.EconomicEvidenceSetIncomparable:
					// A worker cannot safely derive the missing union inside this
					// transaction. Keep the durable work retryable rather than
					// acknowledging an evidence branch that would lose coverage.
					return fmt.Errorf("%w: incomparable same-revision evidence sets", billing.ErrEconomicRevisionFence)
				}
			} else {
				// Same observation revision and same observation plane. Order by
				// the durable current winning tuple, never by the head's
				// initial created_at nor by arrival order:
				//   1. winning CreatedAt (updated_at_unix persists the winning
				//      work's CreatedAt and is updated atomically on every
				//      transition): larger wins, smaller never regresses;
				//   2. equal time (including normalized-zero): total
				//      deterministic lexical tie-break over the full
				//      derivation identity (derivation, dependencies, work
				//      key), larger wins, consistent SQLite/PG.
				// The immutable losing valuation is never mutated and stays
				// independently queryable, so delayed or reordered replays of
				// an older derivation cannot regress the head, and equal-time
				// arrivals converge independent of completion order.
				winningCreatedAt := existing.UpdatedAt
				if incoming.CreatedAt != winningCreatedAt {
					advance = incoming.CreatedAt > winningCreatedAt
				} else {
					advance = existingIdentity.Less(identity)
				}
			}
		}
		if !advance {
			return nil
		}
	}
	if _, err := tx.NewRaw(`UPDATE billing_economic_valuation_heads SET
		subject_kind = ?, subject_id = ?, subject_json = ?, evidence_revision = ?, input_set_hash = ?, work_id = ?,
		valuation_id = ?, valuation_version = ?, reconciliation_id = ?, reconciliation_version = ?, valuation_fingerprint = ?,
		reconciliation_fingerprint = ?, fingerprint = ?, head_version = head_version + 1, fence = fence + 1, updated_at_unix = ?,
		derivation_hash = ?, dependencies_hash = ?
		WHERE id = ?`, incoming.SubjectKind, incoming.SubjectID, incoming.SubjectJSON, incoming.EvidenceRevision, incoming.InputSetHash,
		incoming.WorkID, incoming.ValuationID, incoming.ValuationVersion, incoming.ReconciliationID, incoming.ReconciliationVersion,
		incoming.ValuationFingerprint, incoming.ReconciliationFP, incoming.Fingerprint, incoming.UpdatedAt,
		incoming.DerivationHash, incoming.DependenciesHash, existing.ID).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: advance economic valuation head: %w", err)
	}
	return nil
}

func maxInt64ToZero(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

// GetEconomicValuationHead returns the deterministic current pointer for one
// queue/head. Immutable historical valuations remain queryable independently.
func (s *DurableStore) GetEconomicValuationHead(ctx context.Context, queue billing.EconomicQueue, headKey string) (billing.EconomicValuationHead, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.EconomicValuationHead{}, err
	}
	if err := queue.Validate(); err != nil {
		return billing.EconomicValuationHead{}, err
	}
	if err := economics.ValidateSafeRef("economic revision head key", headKey); err != nil || strings.TrimSpace(headKey) != headKey {
		if err == nil {
			err = errors.New("head key must not have surrounding whitespace")
		}
		return billing.EconomicValuationHead{}, fmt.Errorf("%w: head key: %v", billing.ErrInvalidEconomicRevision, err)
	}
	var row economicValuationHeadRow
	if err := s.db.NewRaw(`SELECT id, store_id, queue, head_key, subject_kind, subject_id, subject_json, evidence_revision, input_set_hash, work_id, valuation_id, valuation_version, reconciliation_id, reconciliation_version, valuation_fingerprint, reconciliation_fingerprint, fingerprint, head_version, fence, created_at_unix, updated_at_unix FROM billing_economic_valuation_heads WHERE store_id = ? AND queue = ? AND head_key = ? LIMIT 1`, s.storeID, queue.String(), headKey).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return billing.EconomicValuationHead{}, ErrEconomicRevisionHeadNotFound
		}
		return billing.EconomicValuationHead{}, fmt.Errorf("billingstore: get economic valuation head: %w", err)
	}
	var subject metering.SubjectRef
	if err := json.Unmarshal([]byte(row.SubjectJSON), &subject); err != nil {
		return billing.EconomicValuationHead{}, fmt.Errorf("billingstore: decode economic valuation head subject: %w", err)
	}
	return billing.EconomicValuationHead{
		Queue: queue, HeadKey: row.HeadKey, Subject: subject,
		EvidenceRevision: uint64(maxInt64ToZero(row.EvidenceRevision)), InputSetHash: row.InputSetHash,
		WorkID: row.WorkID, ValuationID: row.ValuationID, ValuationVersion: uint32(maxInt64ToZero(row.ValuationVersion)),
		ReconciliationID: row.ReconciliationID, ReconciliationVersion: uint64(maxInt64ToZero(row.ReconciliationVersion)),
		Fingerprint: row.Fingerprint, HeadVersion: uint64(maxInt64ToZero(row.HeadVersion)), Fence: uint64(maxInt64ToZero(row.Fence)),
		UpdatedAt: time.Unix(0, row.UpdatedAt).UTC(),
	}, nil
}

// CurrentEconomicValuationHead is an alias for read-side query composition.
func (s *DurableStore) CurrentEconomicValuationHead(ctx context.Context, queue billing.EconomicQueue, headKey string) (billing.EconomicValuationHead, error) {
	return s.GetEconomicValuationHead(ctx, queue, headKey)
}
