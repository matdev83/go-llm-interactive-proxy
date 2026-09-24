package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type CallProviderCostWorker struct {
	work     ProviderCostWorkReader
	store    ProviderCostStore
	resolver ProviderCostResolver
	// claimProvider is the narrow B2a claim port legacy production workers use.
	// Nil preserves the legacy test-only path; production should prefer
	// NewCallProviderCostWorkerWithCutover (atomic token-carrying claim).
	claimProvider ProviderCostWorkClaimStore
	// cutoverClaimer is the F6+F8 production token-carrying claim port. When
	// non-nil, ProcessOnce consumes ClaimProviderCostWorkWithCutover so each
	// item already carries its current-marker token.
	cutoverClaimer ClaimedProviderCostWorkClaimer
	batch          int
	interval       time.Duration
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
}

func NewCallProviderCostWorker(work ProviderCostWorkReader, store ProviderCostStore, resolver ProviderCostResolver, batch int) (*CallProviderCostWorker, error) {
	if work == nil || store == nil || resolver == nil {
		return nil, errors.New("billing: durable provider-cost work, store, and resolver are required")
	}
	if batch <= 0 {
		batch = 32
	}
	return &CallProviderCostWorker{work: work, store: store, resolver: resolver, batch: batch, interval: time.Second}, nil
}

// NewCallProviderCostWorkerWithClaim constructs the production provider-cost
// worker with a required B2a claim port. A nil claim provider is rejected.
// Lookup failures other than authorized first acquisition (NotFound) fail
// closed without posting; they are never swallowed into nil/default owner.
func NewCallProviderCostWorkerWithClaim(work ProviderCostWorkReader, store ProviderCostStore, resolver ProviderCostResolver, claimProvider ProviderCostWorkClaimStore, batch int) (*CallProviderCostWorker, error) {
	if work == nil || store == nil || resolver == nil {
		return nil, errors.New("billing: durable provider-cost work, store, and resolver are required")
	}
	if claimProvider == nil {
		return nil, errors.New("billing: provider-cost claim metadata provider is required for production posting")
	}
	if batch <= 0 {
		batch = 32
	}
	return &CallProviderCostWorker{work: work, store: store, resolver: resolver, claimProvider: claimProvider, batch: batch, interval: time.Second}, nil
}

// NewCallProviderCostWorkerWithCutover constructs the F6+F8 production
// provider-cost worker with a required token-carrying claim port. Each claimed
// item already carries its current-marker token; no optional post-claim lookup
// occurs. Legacy test-only construction remains NewCallProviderCostWorker.
func NewCallProviderCostWorkerWithCutover(work ProviderCostWorkReader, store ProviderCostStore, resolver ProviderCostResolver, claimer ClaimedProviderCostWorkClaimer, batch int) (*CallProviderCostWorker, error) {
	if work == nil || store == nil || resolver == nil {
		return nil, errors.New("billing: durable provider-cost work, store, and resolver are required")
	}
	if claimer == nil {
		return nil, errors.New("billing: provider-cost cutover claimer is required for production posting")
	}
	if batch <= 0 {
		batch = 32
	}
	return &CallProviderCostWorker{work: work, store: store, resolver: resolver, cutoverClaimer: claimer, batch: batch, interval: time.Second}, nil
}

func (w *CallProviderCostWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil provider-cost worker")
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

func (w *CallProviderCostWorker) Stop(ctx context.Context) error {
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

func (w *CallProviderCostWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.work == nil || w.store == nil || w.resolver == nil {
		return errors.New("billing: incomplete provider-cost worker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// F6+F8 production path: token arrives as part of claimed work.
	if w.cutoverClaimer != nil {
		claimed, err := w.cutoverClaimer.ClaimProviderCostWorkWithCutover(ctx, w.batch)
		if err != nil {
			return fmt.Errorf("billing: claim provider-cost work with cutover: %w", err)
		}
		return w.processClaimedWork(ctx, claimed)
	}
	work, err := w.work.ListPendingProviderCostWork(ctx, w.batch)
	if err != nil {
		return fmt.Errorf("billing: list pending provider-cost work: %w", err)
	}
	return w.processWork(ctx, work)
}

func (w *CallProviderCostWorker) processClaimedWork(ctx context.Context, claimed []ClaimedProviderCostWork) error {
	failureStore, hasFailureStore := w.store.(ProviderCostWorkFailureStore)
	var allErr error
	for _, c := range claimed {
		item := c.Work
		if item.AccountID == "" {
			err := ErrProviderCostCallUnavailable
			if hasFailureStore {
				err = errors.Join(err, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			}
			allErr = errors.Join(allErr, err)
			continue
		}
		if cutover, ok := w.store.(ProviderCostWorkCutoverStore); ok {
			owned, err := cutover.ClaimProviderCostWorkForRevision(ctx, item)
			if err != nil {
				if hasFailureStore {
					allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
				} else {
					allErr = errors.Join(allErr, err)
				}
				continue
			}
			if owned {
				continue
			}
		}
		result, err := w.resolver.ResolveProviderCost(ctx, item.Leg)
		if err != nil {
			if failures, ok := w.store.(ProviderCostFailureStore); ok {
				markerErr := failures.MarkProviderCostUnreconciled(ctx, ApplyProviderCostInput{AccountID: item.AccountID, CallID: item.CallID, Leg: item.Leg}, err.Error())
				allErr = errors.Join(allErr, err, markerErr)
			}
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			} else {
				allErr = errors.Join(allErr, err)
			}
			continue
		}
		if err := ValidateProviderCostAuthority(item.Leg, result); err != nil {
			if failures, ok := w.store.(ProviderCostFailureStore); ok {
				markerErr := failures.MarkProviderCostUnreconciled(ctx, ApplyProviderCostInput{AccountID: item.AccountID, CallID: item.CallID, Leg: item.Leg, Result: result}, err.Error())
				allErr = errors.Join(allErr, err, markerErr)
			} else {
				allErr = errors.Join(allErr, err)
			}
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			}
			continue
		}
		var owner string
		var claim *CutoverClaimMetadata
		// R4 mandatory: every production item must carry a complete valid
		// token (including first acquisition, which the durable claim
		// transaction issues atomically with pin acquisition). Empty,
		// partial, or mismatched tokens fail closed without posting; never
		// fall back to nil/default authority.
		if verr := c.Validate(); verr != nil {
			if hasFailureStore {
				allErr = errors.Join(allErr, verr, failureStore.DeferProviderCostWork(ctx, item, verr.Error()))
			} else {
				allErr = errors.Join(allErr, verr)
			}
			continue
		}
		copied := c.Claim
		owner, claim = copied.Owner, &copied
		if _, err := w.store.ApplyProviderCost(ctx, ApplyProviderCostInput{AccountID: item.AccountID, CallID: item.CallID, Leg: item.Leg, Result: result, PostingOwner: owner, Claim: claim}); err != nil {
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			} else {
				allErr = errors.Join(allErr, err)
			}
		}
	}
	return allErr
}

func (w *CallProviderCostWorker) processWork(ctx context.Context, work []ProviderCostWork) error {
	failureStore, hasFailureStore := w.store.(ProviderCostWorkFailureStore)
	var allErr error
	for _, item := range work {
		if item.AccountID == "" {
			err := ErrProviderCostCallUnavailable
			if hasFailureStore {
				err = errors.Join(err, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			}
			allErr = errors.Join(allErr, err)
			continue
		}
		if cutover, ok := w.store.(ProviderCostWorkCutoverStore); ok {
			owned, err := cutover.ClaimProviderCostWorkForRevision(ctx, item)
			if err != nil {
				if hasFailureStore {
					allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
				} else {
					allErr = errors.Join(allErr, err)
				}
				continue
			}
			if owned {
				continue
			}
		}
		result, err := w.resolver.ResolveProviderCost(ctx, item.Leg)
		if err != nil {
			if failures, ok := w.store.(ProviderCostFailureStore); ok {
				markerErr := failures.MarkProviderCostUnreconciled(ctx, ApplyProviderCostInput{AccountID: item.AccountID, CallID: item.CallID, Leg: item.Leg}, err.Error())
				allErr = errors.Join(allErr, err, markerErr)
			}
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			} else {
				allErr = errors.Join(allErr, err)
			}
			continue
		}
		if err := ValidateProviderCostAuthority(item.Leg, result); err != nil {
			// A local/estimated result is useful advisory evidence, but it is
			// not eligible for the legacy monetary writer. Keep the existing
			// durable diagnostic and retry semantics so a later provider report
			// can be processed without losing the queued B-leg.
			if failures, ok := w.store.(ProviderCostFailureStore); ok {
				markerErr := failures.MarkProviderCostUnreconciled(ctx, ApplyProviderCostInput{AccountID: item.AccountID, CallID: item.CallID, Leg: item.Leg, Result: result}, err.Error())
				allErr = errors.Join(allErr, err, markerErr)
			} else {
				allErr = errors.Join(allErr, err)
			}
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			}
			continue
		}
		// F8: fail closed on operational/cancellation/malformed claim errors.
		// Authorized first acquisition (NotFound) posts without a token.
		claim, cerr := w.providerChargeClaim(ctx, item)
		if cerr != nil {
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, cerr.Error()))
			} else {
				allErr = errors.Join(allErr, cerr)
			}
			continue
		}
		if _, err := w.store.ApplyProviderCost(ctx, ApplyProviderCostInput{AccountID: item.AccountID, CallID: item.CallID, Leg: item.Leg, Result: result, PostingOwner: claim.owner, Claim: claim.claim}); err != nil {
			if hasFailureStore {
				allErr = errors.Join(allErr, failureStore.DeferProviderCostWork(ctx, item, err.Error()))
			} else {
				allErr = errors.Join(allErr, err)
			}
		}
	}
	return allErr
}

// providerChargeClaimInput carries the resolved owner/claim pair for one
// provider work item.
type providerChargeClaimInput struct {
	owner string
	claim *CutoverClaimMetadata
}

// providerChargeClaim loads the B2a claim record for one B-leg and fails
// closed: authorized first acquisition (NotFound) returns empty input with nil
// error; every other operational, cancellation or validation failure returns
// an error and the caller must defer without posting.
func (w *CallProviderCostWorker) providerChargeClaim(ctx context.Context, item ProviderCostWork) (providerChargeClaimInput, error) {
	if ctx == nil {
		return providerChargeClaimInput{}, errors.New("billing: nil context for claim")
	}
	if err := ctx.Err(); err != nil {
		return providerChargeClaimInput{}, err
	}
	sealed, err := item.Leg.Seal()
	if err != nil {
		return providerChargeClaimInput{}, err
	}
	opKey, err := ProviderCostSourceKey(sealed.Key)
	if err != nil {
		return providerChargeClaimInput{}, err
	}
	// Production path: required claim port, no type-assert bypass.
	if w.claimProvider != nil {
		meta, merr := w.claimProvider.GetCutoverClaimMetadata(ctx, PostingOperationProviderCharge, opKey)
		if merr != nil {
			if errors.Is(merr, ErrPostingOwnershipNotFound) {
				return providerChargeClaimInput{}, nil
			}
			return providerChargeClaimInput{}, merr
		}
		if verr := ValidateProviderCostClaim(meta, item.AccountID, item.CallID, sealed.Key); verr != nil {
			return providerChargeClaimInput{}, verr
		}
		claimed := meta
		return providerChargeClaimInput{owner: meta.Owner, claim: &claimed}, nil
	}
	// Legacy test-only fallback: probe work then store for the narrow port.
	if provider, ok := w.work.(ProviderCostWorkClaimStore); ok {
		if meta, merr := provider.GetCutoverClaimMetadata(ctx, PostingOperationProviderCharge, opKey); merr == nil {
			if verr := ValidateProviderCostClaim(meta, item.AccountID, item.CallID, sealed.Key); verr == nil {
				claimed := meta
				return providerChargeClaimInput{owner: meta.Owner, claim: &claimed}, nil
			}
		}
	}
	if provider, ok := w.store.(ProviderCostWorkClaimStore); ok {
		if meta, merr := provider.GetCutoverClaimMetadata(ctx, PostingOperationProviderCharge, opKey); merr == nil {
			if verr := ValidateProviderCostClaim(meta, item.AccountID, item.CallID, sealed.Key); verr == nil {
				claimed := meta
				return providerChargeClaimInput{owner: meta.Owner, claim: &claimed}, nil
			}
		}
	}
	return providerChargeClaimInput{}, nil
}
