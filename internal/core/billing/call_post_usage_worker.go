package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type CallRatingResolver interface {
	ResolveCallRating(context.Context, CompleteCall, CallExposure) (CallRatingResult, error)
}
type CallPostUsageWorker struct {
	usage      CallUsageStore
	settlement CallSettlementStore
	resolver   CallRatingResolver
	// claimProvider is the narrow B2a claim port legacy production workers use.
	// Nil preserves the legacy test-only path; production compositions must use
	// NewCallPostUsageWorkerWithClaim (fail-closed lookup) or preferably
	// NewCallPostUsageWorkerWithCutover (atomic token-carrying claim).
	claimProvider CutoverClaimMetadataProvider
	// cutoverClaimer is the F6+F8 production token-carrying claim port. When
	// non-nil, ProcessOnce consumes ClaimCompleteCallsWithCutover so each
	// claimed item already carries its current-marker token; no optional
	// post-claim lookup occurs.
	cutoverClaimer ClaimedCompleteCallClaimer
	batch          int
	interval       time.Duration
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
}

// NewCallPostUsageWorker constructs the legacy test-only complete-call worker
// without a required claim port. It preserves pure test doubles that do not
// implement GetCutoverClaimMetadata. Production compositions must use
// NewCallPostUsageWorkerWithClaim.
func NewCallPostUsageWorker(usage CallUsageStore, settlement CallSettlementStore, resolver CallRatingResolver, batch int) (*CallPostUsageWorker, error) {
	if usage == nil || settlement == nil || resolver == nil {
		return nil, errors.New("billing: complete-call worker dependencies are required")
	}
	if batch <= 0 {
		batch = 32
	}
	return &CallPostUsageWorker{usage: usage, settlement: settlement, resolver: resolver, batch: batch, interval: time.Second}, nil
}

// NewCallPostUsageWorkerWithClaim constructs the production complete-call
// worker with a required B2a claim port. A nil claim provider is rejected so
// durable/posting compositions cannot silently bypass claim metadata via a
// decorator hiding the optional interface. Lookup failures other than
// authorized first acquisition (NotFound) fail closed without posting; they
// are never swallowed into nil/default owner.
func NewCallPostUsageWorkerWithClaim(usage CallUsageStore, settlement CallSettlementStore, resolver CallRatingResolver, claimProvider CutoverClaimMetadataProvider, batch int) (*CallPostUsageWorker, error) {
	if usage == nil || settlement == nil || resolver == nil {
		return nil, errors.New("billing: complete-call worker dependencies are required")
	}
	if claimProvider == nil {
		return nil, errors.New("billing: complete-call claim metadata provider is required for production posting")
	}
	if batch <= 0 {
		batch = 32
	}
	return &CallPostUsageWorker{usage: usage, settlement: settlement, resolver: resolver, claimProvider: claimProvider, batch: batch, interval: time.Second}, nil
}

// NewCallPostUsageWorkerWithCutover constructs the F6+F8 production
// complete-call worker with a required token-carrying claim port. The claimed
// item already carries its current-marker token; no optional post-claim
// lookup occurs. A nil claimer is rejected. Legacy test-only construction
// remains NewCallPostUsageWorker.
func NewCallPostUsageWorkerWithCutover(usage CallUsageStore, settlement CallSettlementStore, resolver CallRatingResolver, claimer ClaimedCompleteCallClaimer, batch int) (*CallPostUsageWorker, error) {
	if usage == nil || settlement == nil || resolver == nil {
		return nil, errors.New("billing: complete-call worker dependencies are required")
	}
	if claimer == nil {
		return nil, errors.New("billing: complete-call cutover claimer is required for production posting")
	}
	if batch <= 0 {
		batch = 32
	}
	return &CallPostUsageWorker{usage: usage, settlement: settlement, resolver: resolver, cutoverClaimer: claimer, batch: batch, interval: time.Second}, nil
}

func (w *CallPostUsageWorker) Start(ctx context.Context) error {
	if w == nil {
		return errors.New("billing: nil complete-call worker")
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

func (w *CallPostUsageWorker) Stop(ctx context.Context) error {
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

func (w *CallPostUsageWorker) ProcessOnce(ctx context.Context) error {
	if w == nil || w.usage == nil || w.settlement == nil || w.resolver == nil {
		return errors.New("billing: incomplete complete-call worker")
	}
	// F6+F8 production path: token arrives as part of the claimed item.
	if w.cutoverClaimer != nil {
		return w.processCutoverOnce(ctx)
	}
	calls, err := w.usage.ClaimCompleteCalls(ctx, w.batch)
	if err != nil {
		return fmt.Errorf("billing: claim complete calls: %w", err)
	}
	var allErr error
	for _, complete := range calls {
		exposure, err := w.usage.GetCallExposure(ctx, complete.Closure.CallID)
		if err != nil {
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, "exposure_lookup", err))
			continue
		}
		// Phase 18 blocker 1: resolve the durable B1 pin owner before
		// rating so V2-owned work can never silently select the scalar
		// live engine. F8 fail-closed claim semantics are preserved:
		// authorized first acquisition (NotFound, no pin yet) rates with
		// empty owner (historical-replay default, safe via V2 inference);
		// every other lookup failure retries without posting and never
		// swallows into nil/default.
		claimInput, cerr := w.customerSettlementClaim(ctx, complete.Closure)
		if cerr != nil {
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, "claim_metadata", cerr))
			continue
		}
		result, err := w.resolveCallRatingWithOwner(ctx, complete, exposure, claimInput.owner)
		if err != nil {
			code := "rating_input"
			if errors.Is(err, ErrBillingAttemptSequenceUnknown) {
				code = "settlement_reconcile_required"
			}
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, code, err))
			continue
		}
		if _, err := w.settlement.ApplyCallBillingResult(ctx, ApplyCallBillingInput{Call: complete.Closure, Exposure: exposure, Result: result, PostingOwner: claimInput.owner, Claim: claimInput.claim}); err != nil {
			code := "settlement"
			if errors.Is(err, ErrSettlementReconcileRequired) {
				code = "settlement_reconcile_required"
			}
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, code, err))
		}
	}
	return allErr
}

// processCutoverOnce consumes the token-carrying claim port. Each claimed item
// already carries its current-marker token issued atomically with the claim;
// missing or malformed required tokens fail closed without posting. There is
// no first-acquisition nil fallback on this production path: the durable
// claim transaction issues a nonempty fully validated token even for fresh
// work (acquiring the pin atomically), so an empty OperationKey always means
// a mandatory-port bypass and must fence with zero effects.
func (w *CallPostUsageWorker) processCutoverOnce(ctx context.Context) error {
	if ctx == nil {
		return errors.New("billing: nil context for cutover claim")
	}
	claimed, err := w.cutoverClaimer.ClaimCompleteCallsWithCutover(ctx, w.batch)
	if err != nil {
		return fmt.Errorf("billing: claim complete calls with cutover: %w", err)
	}
	var allErr error
	for _, item := range claimed {
		complete := item.Call
		exposure, err := w.usage.GetCallExposure(ctx, complete.Closure.CallID)
		if err != nil {
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, "exposure_lookup", err))
			continue
		}
		// R4 mandatory: every production item must carry a complete valid
		// token. Nil/empty/partial/mismatched tokens fail closed; never fall
		// back to legacy metadata lookup or nil/default authority. Phase 18
		// blocker 1: the validated token owner selects V1 drain vs V2
		// component rating before any money, so a V2 token can never
		// silently select the scalar live engine.
		if verr := item.Validate(); verr != nil {
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, "claim_metadata", verr))
			continue
		}
		copied := item.Claim
		owner, claim := copied.Owner, &copied
		result, err := w.resolveCallRatingWithOwner(ctx, complete, exposure, owner)
		if err != nil {
			code := "rating_input"
			if errors.Is(err, ErrBillingAttemptSequenceUnknown) {
				code = "settlement_reconcile_required"
			}
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, code, err))
			continue
		}
		if _, err := w.settlement.ApplyCallBillingResult(ctx, ApplyCallBillingInput{Call: complete.Closure, Exposure: exposure, Result: result, PostingOwner: owner, Claim: claim}); err != nil {
			code := "settlement"
			if errors.Is(err, ErrSettlementReconcileRequired) {
				code = "settlement_reconcile_required"
			}
			allErr = errors.Join(allErr, w.retryCall(ctx, complete.Closure.CallID, code, err))
		}
	}
	return allErr
}

type customerSettlementClaimInput struct {
	owner string
	claim *CutoverClaimMetadata
}

// customerSettlementClaim loads the B2a claim record for one claimed call.
// Production workers constructed with NewCallPostUsageWorkerWithClaim use the
// required claim port directly (no optional bypass) and fail closed: authorized
// first acquisition (NotFound, no pin yet) returns empty input with nil error
// so posting can acquire atomically; every other operational, cancellation or
// validation failure returns an error and the caller must retry without
// posting, never swallowing into nil/default owner. Legacy test workers (nil
// claim provider) fall back to type-asserting usage/settlement; missing ports
// preserve legacy auto-acquire for pure doubles.
func (w *CallPostUsageWorker) customerSettlementClaim(ctx context.Context, closure CallUsageRecord) (customerSettlementClaimInput, error) {
	if ctx == nil {
		return customerSettlementClaimInput{}, errors.New("billing: nil context for claim")
	}
	if err := ctx.Err(); err != nil {
		return customerSettlementClaimInput{}, err
	}
	opKey, err := CustomerPostingOperationKey(closure.AccountID, closure.CallID)
	if err != nil {
		return customerSettlementClaimInput{}, err
	}
	// Production path: required claim port, no type-assert bypass.
	if w.claimProvider != nil {
		meta, merr := w.claimProvider.GetCutoverClaimMetadata(ctx, PostingOperationCustomerSettlement, opKey)
		if merr != nil {
			if errors.Is(merr, ErrPostingOwnershipNotFound) {
				return customerSettlementClaimInput{}, nil
			}
			return customerSettlementClaimInput{}, merr
		}
		if verr := ValidateCustomerSettlementClaim(meta, closure.AccountID, closure.CallID); verr != nil {
			return customerSettlementClaimInput{}, verr
		}
		claimed := meta
		return customerSettlementClaimInput{owner: meta.Owner, claim: &claimed}, nil
	}
	// Legacy test-only fallback: probe usage then settlement for the narrow port.
	if provider, ok := w.usage.(CutoverClaimMetadataProvider); ok {
		if meta, merr := provider.GetCutoverClaimMetadata(ctx, PostingOperationCustomerSettlement, opKey); merr == nil {
			if verr := ValidateCustomerSettlementClaim(meta, closure.AccountID, closure.CallID); verr == nil {
				claimed := meta
				return customerSettlementClaimInput{owner: meta.Owner, claim: &claimed}, nil
			}
		}
	}
	if provider, ok := w.settlement.(CutoverClaimMetadataProvider); ok {
		if meta, merr := provider.GetCutoverClaimMetadata(ctx, PostingOperationCustomerSettlement, opKey); merr == nil {
			if verr := ValidateCustomerSettlementClaim(meta, closure.AccountID, closure.CallID); verr == nil {
				claimed := meta
				return customerSettlementClaimInput{owner: meta.Owner, claim: &claimed}, nil
			}
		}
	}
	return customerSettlementClaimInput{}, nil
}

// resolveCallRatingWithOwner carries the durable B1 pin owner into rating
// selection and enforces the generic V2 valuation fence before money.
// V2-owned work requires an OwnerAwareCallRatingResolver: an old
// owner-unaware resolver (including a decorator exposing only that port)
// fails closed with zero effects, even if it would return a scalar result.
// Every resolved result is independently validated for its settlement
// binding via ValidateCallRatingResultForSettlement before Apply: V2
// requires a complete bound component CustomerValuation (subject/scope,
// currency, amount, and result identity bound to the settled call/exposure)
// or an explicit cost-pass-through under its complete contract, while V1
// drain and legacy empty owners preserve historical scalar replay.
// Production cutover paths always supply the explicit V2 token owner; the
// old interface survives only for V1/test-only non-cutover paths.
func (w *CallPostUsageWorker) resolveCallRatingWithOwner(ctx context.Context, complete CompleteCall, exposure CallExposure, owner string) (CallRatingResult, error) {
	trimmed := strings.TrimSpace(owner)
	if trimmed == PostingOwnerV2 {
		aware, ok := w.resolver.(OwnerAwareCallRatingResolver)
		if !ok || aware == nil {
			return CallRatingResult{}, fmt.Errorf("%w: V2-owned rating requires an owner-aware resolver", ErrPostingOwnershipInvalid)
		}
		result, err := aware.ResolveCallRatingForOwner(ctx, complete, exposure, owner)
		if err != nil {
			return CallRatingResult{}, err
		}
		if err := ValidateCallRatingResultForSettlement(result, complete.Closure, exposure, owner); err != nil {
			return CallRatingResult{}, err
		}
		return result, nil
	}
	var result CallRatingResult
	var err error
	if aware, ok := w.resolver.(OwnerAwareCallRatingResolver); ok && aware != nil {
		result, err = aware.ResolveCallRatingForOwner(ctx, complete, exposure, owner)
	} else {
		result, err = w.resolver.ResolveCallRating(ctx, complete, exposure)
	}
	if err != nil {
		return CallRatingResult{}, err
	}
	if err := ValidateCallRatingResultForSettlement(result, complete.Closure, exposure, owner); err != nil {
		return CallRatingResult{}, err
	}
	return result, nil
}

func (w *CallPostUsageWorker) retryCall(ctx context.Context, callID BillingCallID, code string, cause error) error {
	if retryer, ok := w.usage.(interface {
		RetryCompleteCall(context.Context, BillingCallID, string) error
	}); ok {
		if err := retryer.RetryCompleteCall(ctx, callID, code); err != nil {
			return errors.Join(fmt.Errorf("billing: %s: %w", code, cause), err)
		}
	}
	return fmt.Errorf("billing: %s: %w", code, cause)
}
