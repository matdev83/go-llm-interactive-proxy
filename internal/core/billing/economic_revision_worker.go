package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

const (
	defaultEconomicRevisionBatchSize = 32
	economicRevisionClaimLease       = 30 * time.Second
	economicRevisionStateReleaseWait = 5 * time.Second
)

var economicRevisionWorkerSequence atomic.Uint64

// EconomicRevisionWorker performs bounded valuation/reconciliation over
// durable revisions. An optional provider-cost port may apply an independently
// stable operator COGS delta for provider-queue work; customer settlement and
// customer balance mutation remain outside this worker.
type EconomicRevisionWorker struct {
	work         EconomicRevisionWorkReader
	results      EconomicRevisionResultStore
	rater        PostUsageRater
	reconciler   EconomicRevisionReconciler
	providerCost ProviderCostRevisionStore
	// claimProvider is the narrow B2a claim port legacy production
	// provider-queue workers use. Nil preserves the legacy test-only path.
	claimProvider ProviderCostWorkClaimStore
	// cutoverClaimer is the F6+F8 production token-carrying lease port. When
	// non-nil, processRevision leases via ClaimEconomicRevisionWorkWithCutover
	// so the monetary cutover token arrives atomically with the work lease.
	cutoverClaimer EconomicRevisionWorkCutoverClaimer
	queue          EconomicQueue
	batch          int
	interval       time.Duration
	owner          string
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
}

// NewEconomicRevisionWorker constructs a pure worker for one independent
// customer or provider queue. Test-only: preserves pure doubles without a
// claim port. Production provider-queue posting must use the WithClaim
// variant.
func NewEconomicRevisionWorker(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	return newEconomicRevisionWorker(work, results, rater, nil, nil, nil, queue, batch)
}

// NewEconomicRevisionWorkerWithReconciler adds an optional pure reconciliation
// calculation to the valuation worker. Reconciliation output is persisted in
// the same local transaction as its valuation and head transition. Test-only
// for provider posting; production provider posting must use the WithClaim
// variant.
func NewEconomicRevisionWorkerWithReconciler(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, reconciler EconomicRevisionReconciler, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	return newEconomicRevisionWorker(work, results, rater, reconciler, nil, nil, queue, batch)
}

// NewEconomicRevisionWorkerWithProviderCost adds the independent operator
// COGS posting seam. Provider posting is deliberately after pure valuation
// persistence and is scoped to one authoritative B-leg revision; customer
// queues never call this dependency. Test-only: production provider posting
// must use NewEconomicRevisionWorkerWithProviderCostWithClaim or the
// reconciler+claim variant.
func NewEconomicRevisionWorkerWithProviderCost(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, providerCost ProviderCostRevisionStore, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	return newEconomicRevisionWorker(work, results, rater, nil, providerCost, nil, queue, batch)
}

// NewEconomicRevisionWorkerWithReconcilerAndProviderCost combines pure
// reconciliation with the independent operator COGS posting seam. Test-only:
// production provider posting must use the WithClaim variant below.
func NewEconomicRevisionWorkerWithReconcilerAndProviderCost(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, reconciler EconomicRevisionReconciler, providerCost ProviderCostRevisionStore, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	return newEconomicRevisionWorker(work, results, rater, reconciler, providerCost, nil, queue, batch)
}

// NewEconomicRevisionWorkerWithProviderCostWithClaim constructs the production
// provider-queue worker with a required B2a claim port. A nil claim provider
// is rejected. Lookup failures other than authorized first acquisition fail
// closed without posting.
func NewEconomicRevisionWorkerWithProviderCostWithClaim(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, providerCost ProviderCostRevisionStore, claimProvider ProviderCostWorkClaimStore, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	if claimProvider == nil {
		return nil, errors.New("billing: economic revision claim metadata provider is required for production provider posting")
	}
	return newEconomicRevisionWorker(work, results, rater, nil, providerCost, claimProvider, queue, batch)
}

// NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithClaim combines pure
// reconciliation with the production provider COGS posting seam and a required
// B2a claim port. A nil claim provider is rejected.
func NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithClaim(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, reconciler EconomicRevisionReconciler, providerCost ProviderCostRevisionStore, claimProvider ProviderCostWorkClaimStore, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	if claimProvider == nil {
		return nil, errors.New("billing: economic revision claim metadata provider is required for production provider posting")
	}
	return newEconomicRevisionWorker(work, results, rater, reconciler, providerCost, claimProvider, queue, batch)
}

// NewEconomicRevisionWorkerWithProviderCostWithCutover constructs the F6+F8
// production provider-queue worker with a required token-carrying lease port.
// Each monetary lease atomically carries its current-marker cutover token.
func NewEconomicRevisionWorkerWithProviderCostWithCutover(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, providerCost ProviderCostRevisionStore, claimer EconomicRevisionWorkCutoverClaimer, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	if claimer == nil {
		return nil, errors.New("billing: economic revision cutover claimer is required for production provider posting")
	}
	w, err := newEconomicRevisionWorker(work, results, rater, nil, providerCost, nil, queue, batch)
	if err != nil {
		return nil, err
	}
	w.cutoverClaimer = claimer
	return w, nil
}

// NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover combines
// reconciliation with the F6+F8 production token-carrying lease port.
func NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithCutover(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, reconciler EconomicRevisionReconciler, providerCost ProviderCostRevisionStore, claimer EconomicRevisionWorkCutoverClaimer, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	if claimer == nil {
		return nil, errors.New("billing: economic revision cutover claimer is required for production provider posting")
	}
	w, err := newEconomicRevisionWorker(work, results, rater, reconciler, providerCost, nil, queue, batch)
	if err != nil {
		return nil, err
	}
	w.cutoverClaimer = claimer
	return w, nil
}

func newEconomicRevisionWorker(work EconomicRevisionWorkReader, results EconomicRevisionResultStore, rater PostUsageRater, reconciler EconomicRevisionReconciler, providerCost ProviderCostRevisionStore, claimProvider ProviderCostWorkClaimStore, queue EconomicQueue, batch int) (*EconomicRevisionWorker, error) {
	if work == nil || results == nil || rater == nil {
		return nil, errors.New("billing: economic revision queue, result store, and rater are required")
	}
	if err := queue.Validate(); err != nil {
		return nil, err
	}
	// Provider-posting recovery must bind the exact persisted valuation
	// identity (Hfull). A result store that exposes the processed-result probe
	// without the exact valuation loader cannot distinguish an ordinary
	// observation-only output from a lenient legacy allocation-bearing output
	// for work with an empty DerivationHash, so it would silently substitute
	// Hobs on recovery. Reject that composition before it can persist/post.
	// Pure workers (no provider posting, or a non-provider queue where posting
	// is a no-op) remain compatible without a loader.
	if providerCost != nil && queue == EconomicQueueProvider {
		if _, hasProbe := results.(EconomicRevisionResultProbe); hasProbe {
			if _, hasLoader := results.(EconomicRevisionValuationLoader); !hasLoader {
				return nil, fmt.Errorf("%w: provider-cost revision worker with a processed-result probe requires an exact valuation loader", ErrInvalidEconomicRevision)
			}
		}
	}
	if batch <= 0 {
		batch = defaultEconomicRevisionBatchSize
	}
	return &EconomicRevisionWorker{
		work: work, results: results, rater: rater, reconciler: reconciler,
		providerCost: providerCost, claimProvider: claimProvider, queue: queue, batch: batch, interval: time.Second,
		owner: fmt.Sprintf("economic-revision-worker-%d", economicRevisionWorkerSequence.Add(1)),
	}, nil
}

// Start runs bounded polling until the supplied context is canceled.
func (w *EconomicRevisionWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil economic revision worker")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			_ = w.ProcessOnce(workerCtx)
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

// Stop cancels polling and waits for the worker goroutine to leave.
func (w *EconomicRevisionWorker) Stop(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ProcessOnce claims a bounded queue snapshot and computes each revision. A
// durable result probe is used when available so restart/replay does not call
// the rater again for an already persisted immutable result.
func (w *EconomicRevisionWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.work == nil || w.results == nil || w.rater == nil {
		return errors.New("billing: incomplete economic revision worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	items, err := w.work.ListPendingEconomicRevisionWork(ctx, w.queue, w.batch)
	if err != nil {
		return fmt.Errorf("billing: list %s economic revisions: %w", w.queue, err)
	}
	var allErr error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return errors.Join(allErr, err)
		}
		if err := w.processRevision(ctx, item); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func (w *EconomicRevisionWorker) processRevision(ctx context.Context, item EconomicRevisionWork) error {
	work, err := item.Normalize()
	if err != nil {
		return fmt.Errorf("billing: normalize %s economic revision: %w", w.queue, err)
	}
	identity, err := work.Identity()
	if err != nil {
		return fmt.Errorf("billing: identify %s economic revision: %w", w.queue, err)
	}
	stateStore, hasState := w.work.(EconomicRevisionWorkStateStore)
	var claim EconomicRevisionWorkClaim
	var cutover *CutoverClaimMetadata
	if w.cutoverClaimer != nil && w.providerCost != nil && w.queue == EconomicQueueProvider {
		// F6+F8 atomic lease+token: monetary token arrives with the lease.
		var claimed bool
		var cutErr error
		claim, cutover, claimed, cutErr = w.cutoverClaimer.ClaimEconomicRevisionWorkWithCutover(ctx, work, w.owner, economicRevisionClaimLease)
		if cutErr != nil {
			return fmt.Errorf("billing: claim %s economic revision %s with cutover: %w", w.queue, identity.Key(), cutErr)
		}
		if !claimed {
			return nil
		}
		// S1 authoritative delivery intent: the atomic claim observes current
		// mutable queue state under the marker lock. A worker that listed an
		// evidence-only envelope before an evidence->monetary upgrade must not
		// continue with that stale intent (it would skip money and strand the
		// new monetary pin). Consume the authoritative claimed intent: a
		// monetary token means monetary delivery regardless of the stale
		// pre-claim envelope; nil cutover means evidence-only. Immutable
		// evidence identity is unchanged, so retry/complete fences still match.
		if cutover != nil {
			work = OverlayAuthoritativeEconomicWork(work, true, cutover.Owner)
			if renewed, rerr := work.Normalize(); rerr == nil {
				work = renewed
			}
		}
		// Align hasState so retry/complete use the atomic lease.
		if _, ok := w.work.(EconomicRevisionWorkStateStore); ok {
			hasState = true
			// stateStore for retry/complete: prefer work's state store when available,
			// otherwise the cutover claimer cannot retry/complete (should not happen
			// for DurableStore which implements both).
			if ss, ok := w.work.(EconomicRevisionWorkStateStore); ok {
				stateStore = ss
			} else {
				// No state store to retire the lease: fail closed without posting.
				return fmt.Errorf("billing: cutover lease without state store for %s", identity.Key())
			}
		} else {
			// Cutover claimer without state store: treat as claimed with no
			// retry/complete (pure in-memory test path should not use cutover).
			hasState = false
		}
	} else if hasState {
		var claimed bool
		claim, claimed, err = stateStore.ClaimEconomicRevisionWork(ctx, work, w.owner, economicRevisionClaimLease)
		if err != nil {
			return fmt.Errorf("billing: claim %s economic revision %s: %w", w.queue, identity.Key(), err)
		}
		if !claimed {
			return nil
		}
	}

	retry := func(cause error) error {
		if !hasState {
			return cause
		}
		retryCtx, cancel := economicRevisionStateContext(ctx)
		defer cancel()
		retryErr := stateStore.RetryEconomicRevisionWork(retryCtx, work, claim, cause.Error(), time.Now().UTC())
		if retryErr != nil {
			return errors.Join(cause, fmt.Errorf("billing: retry %s economic revision %s: %w", w.queue, identity.Key(), retryErr))
		}
		return cause
	}
	complete := func() error {
		if !hasState {
			return nil
		}
		completeCtx, cancel := economicRevisionStateContext(ctx)
		defer cancel()
		if err := stateStore.CompleteEconomicRevisionWork(completeCtx, work, claim); err != nil {
			return fmt.Errorf("billing: complete %s economic revision %s: %w", w.queue, identity.Key(), err)
		}
		return nil
	}

	if probe, ok := w.results.(EconomicRevisionResultProbe); ok {
		processed, probeErr := probe.HasEconomicRevisionResult(ctx, identity)
		if probeErr != nil {
			return retry(fmt.Errorf("billing: probe %s economic revision: %w", w.queue, probeErr))
		}
		if processed {
			recovered, recoverErr := w.recoverProcessedValuation(ctx, work, identity)
			if recoverErr != nil {
				return retry(fmt.Errorf("billing: recover %s economic revision %s: %w", w.queue, identity.Key(), recoverErr))
			}
			if err := w.postProviderCost(ctx, work, recovered, cutover); err != nil {
				return retry(fmt.Errorf("billing: post %s provider cost %s: %w", w.queue, identity.Key(), err))
			}
			return complete()
		}
	}

	valuation, err := w.rater.Rate(ctx, work.Input.Clone())
	if err != nil {
		return retry(fmt.Errorf("billing: rate %s economic revision %s: %w", w.queue, identity.Key(), err))
	}
	valuation, err = normalizeRevisionValuation(work, identity, valuation)
	if err != nil {
		return retry(fmt.Errorf("billing: normalize valuation %s: %w", identity.Key(), err))
	}
	var reconciliation *EconomicReconciliation
	if w.reconciler != nil {
		reconciliation, err = w.reconciler.Reconcile(ctx, work, valuation)
		if err != nil {
			return retry(fmt.Errorf("billing: reconcile economic revision %s: %w", identity.Key(), err))
		}
		if reconciliation == nil {
			return retry(fmt.Errorf("billing: reconcile economic revision %s: nil result", identity.Key()))
		}
		reconciliation, err = normalizeRevisionReconciliation(work, identity, *reconciliation)
		if err != nil {
			return retry(fmt.Errorf("billing: normalize reconciliation %s: %w", identity.Key(), err))
		}
	}
	if err := w.results.AppendEconomicRevisionResult(ctx, work, EconomicRevisionResult{Valuation: valuation, Reconciliation: reconciliation}); err != nil {
		return retry(fmt.Errorf("billing: persist %s economic revision %s: %w", w.queue, identity.Key(), err))
	}
	if err := w.postProviderCost(ctx, work, valuation, cutover); err != nil {
		return retry(fmt.Errorf("billing: post %s provider cost %s: %w", w.queue, identity.Key(), err))
	}
	return complete()
}

func (w *EconomicRevisionWorker) postProviderCost(ctx context.Context, work EconomicRevisionWork, valuation economics.Valuation, cutover *CutoverClaimMetadata) error {
	if w.providerCost == nil || w.queue != EconomicQueueProvider {
		return nil
	}
	// F2B: evidence-only work (customer rating, reconciliation, shadow,
	// explicit evidence flag, or queues without a posting adapter) never
	// posts provider money. Only monetary provider-rating work with valid
	// B-leg lineage reaches the provider-cost seam; nonpayable exclusions
	// still flow through Apply (which completes pins without journals).
	normalizedForGate, gateErr := work.Normalize()
	if gateErr != nil {
		return gateErr
	}
	if normalizedForGate.EvidenceOnly || !IsMonetaryEconomicRevisionWork(normalizedForGate) {
		return nil
	}
	input, err := BuildProviderCostRevisionInput(work, valuation)
	if err != nil {
		return err
	}
	// F2B V2: explicit V2 monetary work carries V2 ownership even before its
	// first pin exists (posting acquires V2 pins in v2_active). Fresh pin
	// metadata below upgrades it to a full claim when available.
	if normalizedForGate.PostingOwner == PostingOwnerV2 && input.PostingOwner == "" {
		input.PostingOwner = PostingOwnerV2
	}
	// R4 mandatory: when the atomic lease carries a monetary cutover token, it
	// must be complete and valid (work identity, operation/owner, marker and
	// lease fence). A nil cutover for monetary work on the production cutover
	// path fails closed; it never falls back to optional lookup or
	// nil/default authority. Evidence-only work never reaches here.
	if cutover != nil {
		if verr := ValidateProviderRevisionClaim(*cutover, input); verr != nil {
			return verr
		}
		if verr := cutover.Validate(); verr != nil {
			return verr
		}
		// Production monetary tokens must bind the lease fence; a token
		// without lease binding is a mandatory-port bypass.
		if w.cutoverClaimer != nil {
			if strings.TrimSpace(cutover.WorkID) == "" || strings.TrimSpace(cutover.LeaseOwner) == "" || cutover.LeaseFence == 0 {
				return fmt.Errorf("%w: monetary cutover token requires lease binding", ErrCutoverCoordinatorInvalid)
			}
		}
		copied := *cutover
		input.PostingOwner = copied.Owner
		input.Claim = &copied
		_, err = w.providerCost.ApplyProviderCostRevision(ctx, input)
		return err
	}
	// Production cutover path requires a token for monetary work: nil means
	// a decorator dropped it or the claim withheld ineligible work. Fail
	// closed with zero effects; never fall back to legacy lookup.
	if w.cutoverClaimer != nil {
		return fmt.Errorf("%w: monetary work requires a cutover token (missing token fails closed)", ErrCutoverCoordinatorInvalid)
	}
	// Legacy fail-closed lookup for workers without an atomic cutover.
	// Production (claimProvider != nil) distinguishes authorized first
	// acquisition (NotFound, no pin yet) from operational/cancellation/
	// malformed failures which return an error and never post. F6: token binds
	// the revision-specific pin.
	if w.claimProvider != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		pinKey, kerr := ProviderRevisionPostingOperationKey(input)
		if kerr != nil {
			return kerr
		}
		meta, merr := w.claimProvider.GetCutoverClaimMetadata(ctx, PostingOperationProviderCharge, pinKey)
		if merr != nil {
			if errors.Is(merr, ErrPostingOwnershipNotFound) {
				_, err = w.providerCost.ApplyProviderCostRevision(ctx, input)
				return err
			}
			return merr
		}
		if verr := ValidateProviderRevisionClaim(meta, input); verr != nil {
			return verr
		}
		claimed := meta
		input.PostingOwner = meta.Owner
		input.Claim = &claimed
		_, err = w.providerCost.ApplyProviderCostRevision(ctx, input)
		return err
	} else if provider, ok := w.providerCost.(ProviderCostWorkClaimStore); ok {
		if pinKey, kerr := ProviderRevisionPostingOperationKey(input); kerr == nil {
			if meta, merr := provider.GetCutoverClaimMetadata(ctx, PostingOperationProviderCharge, pinKey); merr == nil {
				if verr := ValidateProviderRevisionClaim(meta, input); verr == nil {
					claimed := meta
					input.PostingOwner = meta.Owner
					input.Claim = &claimed
				}
			}
		}
	} else if provider, ok := w.results.(ProviderCostWorkClaimStore); ok {
		if pinKey, kerr := ProviderRevisionPostingOperationKey(input); kerr == nil {
			if meta, merr := provider.GetCutoverClaimMetadata(ctx, PostingOperationProviderCharge, pinKey); merr == nil {
				if verr := ValidateProviderRevisionClaim(meta, input); verr == nil {
					claimed := meta
					input.PostingOwner = meta.Owner
					input.Claim = &claimed
				}
			}
		}
	}
	_, err = w.providerCost.ApplyProviderCostRevision(ctx, input)
	return err
}

// recoverProcessedValuation reloads the exact immutable valuation identity
// persisted on the fresh path so provider-posting replay binds the same
// EconomicRevisionIdentity, source key and journal fence. Durable stores must
// implement EconomicRevisionValuationLoader. The fallback reconstructs the
// declared derivation hash from immutable work and is only reachable for pure
// workers without provider posting: provider-posting workers are rejected at
// construction when the loader is absent, and recovery refuses to guess here
// so a lenient legacy allocation-bearing output can never silently replay as
// observation-only.
func (w *EconomicRevisionWorker) recoverProcessedValuation(ctx context.Context, work EconomicRevisionWork, identity EconomicRevisionIdentity) (economics.Valuation, error) {
	if loader, ok := w.results.(EconomicRevisionValuationLoader); ok {
		loaded, err := loader.LoadEconomicRevisionValuation(ctx, identity)
		if err != nil {
			return economics.Valuation{}, err
		}
		if loaded.ID != identity.ValuationKey() {
			return economics.Valuation{}, fmt.Errorf("%w: recovered valuation id got=%q want=%q", ErrEconomicRevisionInputMismatch, loaded.ID, identity.ValuationKey())
		}
		if loaded.Version != economics.ValuationVersionV2 {
			return economics.Valuation{}, fmt.Errorf("%w: recovered valuation version got=%d", ErrEconomicRevisionInputMismatch, loaded.Version)
		}
		if loaded.InputSetHash == "" {
			return economics.Valuation{}, fmt.Errorf("%w: recovered valuation lacks input identity", ErrEconomicRevisionInputMismatch)
		}
		observationHash, err := economics.CanonicalInputSetHash(loaded.Basis, loaded.InputObservations)
		if err != nil {
			return economics.Valuation{}, fmt.Errorf("%w: recovered valuation observation inputs: %v", ErrEconomicRevisionInputMismatch, err)
		}
		if observationHash != work.InputSetHash {
			return economics.Valuation{}, &EconomicRevisionInputMismatchError{Expected: work.InputSetHash, Actual: observationHash}
		}
		fullHash, err := economics.CanonicalValuationInputSetHash(loaded.Basis, loaded.InputObservations, loaded.AllocationCoverageRefs)
		if err != nil {
			return economics.Valuation{}, fmt.Errorf("%w: recovered valuation allocation inputs: %v", ErrEconomicRevisionInputMismatch, err)
		}
		if fullHash != loaded.InputSetHash {
			return economics.Valuation{}, &EconomicRevisionInputMismatchError{Expected: fullHash, Actual: loaded.InputSetHash}
		}
		if identity.DerivationHash != "" && loaded.InputSetHash != identity.DerivationHash {
			return economics.Valuation{}, &EconomicRevisionInputMismatchError{Expected: identity.DerivationHash, Actual: loaded.InputSetHash}
		}
		return loaded, nil
	}
	if w.providerCost != nil && w.queue == EconomicQueueProvider {
		return economics.Valuation{}, fmt.Errorf("%w: provider-cost recovery requires an exact valuation loader, refusing observation-only fallback", ErrInvalidEconomicRevision)
	}
	inputSetHash := identity.DerivationHash
	if inputSetHash == "" {
		inputSetHash = identity.InputSetHash
	}
	return economics.Valuation{ID: identity.ValuationKey(), InputSetHash: inputSetHash}, nil
}

// economicRevisionStateContext keeps a bounded release attempt possible when
// a rater observes cancellation. Normal database work still honors the caller
// context; only cleanup after cancellation is detached and time-limited.
func economicRevisionStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil || ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), economicRevisionStateReleaseWait)
}

func normalizeRevisionValuation(work EconomicRevisionWork, identity EconomicRevisionIdentity, valuation economics.Valuation) (economics.Valuation, error) {
	out := valuation.Clone()
	if !sameSubject(out.Subject, work.Subject) {
		return economics.Valuation{}, fmt.Errorf("%w: valuation subject", ErrEconomicRevisionSubjectMismatch)
	}
	if out.Basis != work.Input.Basis {
		return economics.Valuation{}, fmt.Errorf("%w: got=%q want=%q", ErrEconomicRevisionBasisMismatch, out.Basis, work.Input.Basis)
	}
	if out.Perspective != work.Input.Perspective {
		return economics.Valuation{}, fmt.Errorf("%w: valuation perspective got=%q want=%q", ErrEconomicRevisionInputMismatch, out.Perspective, work.Input.Perspective)
	}
	if out.Scope != work.Input.Scope {
		return economics.Valuation{}, fmt.Errorf("%w: valuation scope got=%q want=%q", ErrEconomicRevisionInputMismatch, out.Scope, work.Input.Scope)
	}
	// The immutable work envelope declares the claimed allocation coverage set.
	// An allocation-aware rater must price exactly that set; a rater may not
	// substitute an unclaimed allocation revision under this work identity.
	// Legacy work that declares no allocation coverage remains lenient so the
	// stock observation-only seam is unchanged.
	workAllocations, err := economics.CanonicalAllocationCoverageRefs(work.Input.AllocationCoverageRefs)
	if err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: work allocation coverage: %v", ErrEconomicRevisionInputMismatch, err)
	}
	outputAllocations, err := economics.CanonicalAllocationCoverageRefs(out.AllocationCoverageRefs)
	if err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: output allocation coverage: %v", ErrEconomicRevisionInputMismatch, err)
	}
	if len(workAllocations) != 0 && !slices.Equal(workAllocations, outputAllocations) {
		return economics.Valuation{}, fmt.Errorf("%w: output allocation coverage does not match the immutable work claim", ErrEconomicRevisionInputMismatch)
	}
	out.AllocationCoverageRefs = outputAllocations
	// The immutable work envelope fences the observation plane: a rater may not
	// silently change which observations support the revision. The stored
	// valuation identity additionally covers allocation coverage references, so
	// an allocation-only correction is a distinct, persistable revision.
	observationHash, err := economics.CanonicalInputSetHash(out.Basis, out.InputObservations)
	if err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: output references: %v", ErrEconomicRevisionInputMismatch, err)
	}
	if observationHash != work.InputSetHash {
		return economics.Valuation{}, &EconomicRevisionInputMismatchError{Expected: work.InputSetHash, Actual: observationHash}
	}
	computedHash, err := economics.CanonicalValuationInputSetHash(out.Basis, out.InputObservations, out.AllocationCoverageRefs)
	if err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: output references: %v", ErrEconomicRevisionInputMismatch, err)
	}
	if out.InputSetHash != "" && out.InputSetHash != computedHash {
		return economics.Valuation{}, &EconomicRevisionInputMismatchError{Expected: computedHash, Actual: out.InputSetHash}
	}
	out.InputSetHash = computedHash
	out.ID = identity.ValuationKey()
	out.Version = economics.ValuationVersionV2
	// CreatedAt is derived processing metadata, not rater-authored evidence.
	// Anchor it to the immutable work envelope so retries cannot vary the
	// valuation fingerprint with the rater's wall clock.
	out.CreatedAt = work.CreatedAt
	if err := out.Validate(); err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: valuation: %v", ErrEconomicRevisionInputMismatch, err)
	}
	return out, nil
}

// NormalizeRevisionValuationForWork is the single shared shadow/worker
// valuation contract. It preserves the full valid rater clone (allocation
// coverage, snapshot identities and content, qualifiers, payer, missing
// observations, charge coverage and other provenance) while rejecting foreign
// subject/basis/perspective/scope/observation/allocation identity. CreatedAt
// anchors to immutable work metadata so retries stay byte-stable.
func NormalizeRevisionValuationForWork(work EconomicRevisionWork, identity EconomicRevisionIdentity, valuation economics.Valuation) (economics.Valuation, error) {
	return normalizeRevisionValuation(work, identity, valuation)
}

func normalizeRevisionReconciliation(work EconomicRevisionWork, identity EconomicRevisionIdentity, reconciliation EconomicReconciliation) (*EconomicReconciliation, error) {
	out := reconciliation
	if !sameSubject(out.Subject, work.Subject) {
		return nil, fmt.Errorf("%w: reconciliation subject", ErrEconomicRevisionSubjectMismatch)
	}
	if out.Basis != work.Input.Basis {
		return nil, fmt.Errorf("%w: reconciliation got=%q want=%q", ErrEconomicRevisionBasisMismatch, out.Basis, work.Input.Basis)
	}
	if out.InputSetHash != "" && out.InputSetHash != work.InputSetHash {
		return nil, fmt.Errorf("%w: reconciliation hash supplied=%q expected=%q", ErrEconomicRevisionInputMismatch, out.InputSetHash, work.InputSetHash)
	}
	out.ID = identity.ReconciliationKey()
	out.Version = work.EvidenceRevision
	out.InputSetHash = work.InputSetHash
	// Reconciliation CreatedAt is likewise derived processing metadata. The
	// immutable work timestamp keeps duplicate workers byte/fingerprint-stable.
	out.CreatedAt = work.CreatedAt
	normalized, err := out.Normalize()
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}
