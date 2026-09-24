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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

var (
	_ billing.ProviderCostRevisionStore = (*DurableStore)(nil)
	_ billing.ProviderCostHeadReader    = (*DurableStore)(nil)
)

type providerCostHeadRow struct {
	ID                    int64  `bun:"id"`
	StoreID               string `bun:"store_id"`
	AccountID             string `bun:"account_id"`
	CallID                string `bun:"call_id"`
	HeadKey               string `bun:"head_key"`
	SubjectKind           string `bun:"subject_kind"`
	SubjectID             string `bun:"subject_id"`
	SubjectJSON           string `bun:"subject_json"`
	EvidenceRevision      int64  `bun:"evidence_revision"`
	InputSetHash          string `bun:"input_set_hash"`
	ValuationID           string `bun:"valuation_id"`
	AmountNano            int64  `bun:"amount_nano"`
	Currency              string `bun:"currency"`
	HeadVersion           int64  `bun:"head_version"`
	Fence                 int64  `bun:"fence"`
	LastOperationKey      string `bun:"last_operation_key"`
	OriginalTransactionID string `bun:"original_transaction_id"`
	LastTransactionID     string `bun:"last_transaction_id"`
	CreatedAt             int64  `bun:"created_at_unix"`
	UpdatedAt             int64  `bun:"updated_at_unix"`
	// Task 13.3B exact selected valuation identity. Legacy rows upgraded by
	// the additive migration retain zero/empty values and are read through
	// their integer nanos and selection defaults.
	ValuationRevision   int64  `bun:"valuation_revision"`
	SelectionStatus     string `bun:"selection_status"`
	SelectionReason     string `bun:"selection_reason"`
	SelectionBasis      string `bun:"selection_basis"`
	SelectionProvenance string `bun:"selection_provenance"`
	PostedAmountJSON    string `bun:"posted_amount_json"`
	NativeAmountJSON    string `bun:"native_amount_json"`
	FXJSON              string `bun:"fx_json"`
	PostingState        string `bun:"posting_state"`
}

const providerCostHeadSelect = `SELECT id, store_id, account_id, call_id, head_key, subject_kind, subject_id, subject_json, evidence_revision, input_set_hash, valuation_id, amount_nano, currency, head_version, fence, last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix, valuation_revision, selection_status, selection_reason, selection_basis, selection_provenance, posted_amount_json, native_amount_json, fx_json, posting_state FROM billing_provider_cost_heads`

// providerCostHeadPostedAmountJSON projects the retained integer nanos into the
// canonical exact-amount JSON shared with the selected-cost adjustment writer,
// so both writers keep one coherent head identity.
func providerCostHeadPostedAmountJSON(amount billing.Money) (string, error) {
	decimal := metering.DecimalFromNanoUnits(amount.Nano)
	exact := billing.MonetaryExactAmount{Currency: amount.Currency, Decimal: &decimal}
	if err := exact.Validate(); err != nil {
		return "", fmt.Errorf("billingstore: provider cost exact amount: %w", err)
	}
	payload, err := json.Marshal(exact)
	if err != nil {
		return "", fmt.Errorf("billingstore: encode provider cost exact amount: %w", err)
	}
	return string(payload), nil
}

// ApplyProviderCostRevision atomically advances one provider-cost head and
// posts only the signed delta from its prior selected amount. Pure provider
// COGS does not lock or update the customer account balance/version.
func (s *DurableStore) ApplyProviderCostRevision(ctx context.Context, input billing.ProviderCostRevisionInput) (billing.ProviderCostRevisionResult, error) {
	if s == nil || s.db == nil {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: nil store")
	}
	if ctx == nil {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: nil context", billing.ErrProviderCostRevisionInvalid)
	}
	if err := ctx.Err(); err != nil {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: %w", billing.ErrProviderCostRevisionInvalid, err)
	}
	normalized, err := input.Normalize()
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if normalized.Subject.StoreID != s.storeID {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost subject store", ErrEconomicsOutOfScope)
	}
	if _, err := billing.ResolveProviderRevisionOwner(normalized); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if normalized.Claim != nil {
		if err := billing.ValidateProviderRevisionClaim(*normalized.Claim, normalized); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	}
	return withAccountTx(ctx, accountTxRetry{
		Attempts: 80, Delay: 3 * time.Millisecond,
		Exhausted: fmt.Errorf("%w: provider cost revision retry budget exhausted", billing.ErrBillingStoreUnavailable),
	}, func() (billing.ProviderCostRevisionResult, error) {
		return s.applyProviderCostRevisionAttempt(ctx, normalized)
	})
}

// ApplyOperatorCostRevision is the descriptive operator-side alias.
func (s *DurableStore) ApplyOperatorCostRevision(ctx context.Context, input billing.OperatorCostRevisionInput) (billing.OperatorCostRevisionResult, error) {
	return s.ApplyProviderCostRevision(ctx, input)
}

func ignoredProviderCostRevision(input billing.ProviderCostRevisionInput) billing.ProviderCostRevisionResult {
	return billing.ProviderCostRevisionResult{
		AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey,
		ALegID: input.Subject.ALegID, BLegID: input.Subject.BLegID,
		Revision: input.EvidenceRevision, Ignored: true,
	}
}

func (s *DurableStore) applyProviderCostRevisionAttempt(ctx context.Context, input billing.ProviderCostRevisionInput) (billing.ProviderCostRevisionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: begin provider cost revision: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: lock the per-store marker before account/head/pin effects.
	if _, err := s.ensureAndLockAccountingCutoverTx(ctx, tx); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	account, err := getAccountTx(ctx, tx, input.AccountID)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	amount := billing.Money{Currency: account.Currency}
	if input.Cost.Payable {
		var hasCurrency bool
		amount, hasCurrency = input.Cost.KnownSubtotalByCurrency[account.Currency]
		if !hasCurrency {
			// Provider costs remain native-currency values. Without an explicit FX
			// valuation, selecting zero for a different account currency would hide
			// an amount rather than reject an unpostable revision.
			return billing.ProviderCostRevisionResult{}, billing.ErrMoneyCurrencyMismatch
		}
		for currency := range input.Cost.KnownSubtotalByCurrency {
			if currency != account.Currency {
				// A native-currency cost cannot be silently dropped merely because
				// the account has a different currency. A future explicit FX/clearing
				// valuation must own that conversion before this writer is called.
				return billing.ProviderCostRevisionResult{}, billing.ErrMoneyCurrencyMismatch
			}
		}
		if amount.Currency != account.Currency {
			return billing.ProviderCostRevisionResult{}, billing.ErrMoneyCurrencyMismatch
		}
	}
	if amount.Nano < 0 {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: selected cost cannot be negative", billing.ErrUnreconciledCost)
	}
	if input.EvidenceRevision > math.MaxInt64 {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: revision exceeds database range", billing.ErrInvalidEconomicRevision)
	}
	if input.Subject.StoreID != s.storeID {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost subject store", ErrEconomicsOutOfScope)
	}
	if input.Subject.AccountID != "" && input.Subject.AccountID != input.AccountID {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost subject account", ErrEconomicsOutOfScope)
	}
	if input.Subject.BillingCallID != "" && input.Subject.BillingCallID != input.CallID.String() {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost subject call", ErrEconomicsOutOfScope)
	}
	if input.Subject.CallID != "" && input.Subject.CallID != input.CallID.String() {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost subject call", ErrEconomicsOutOfScope)
	}

	sourceKey, err := billing.ProviderCostRevisionSourceKey(input)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	fingerprint, err := input.SemanticFingerprint()
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	subjectJSON, err := json.Marshal(input.Subject)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: encode provider cost subject: %w", err)
	}
	operationKey := billing.ScopedOperationKey("provider_call_cogs", input.AccountID, sourceKey)
	// B2b2 posting-time ownership fence (provider_charge only). F5+F7: each
	// immutable revision outcome has its own canonical pin derived from
	// ProviderCostRevisionSourceKey (revision-specific, operationKey), not
	// merely B-leg lineage. Base legacy charges keep lineage pins; higher/
	// replacement revisions are distinct pins while heads/fences order lineage.
	// Pin + head/exclusion/journal effects commit atomically; exact replay
	// (same operation+fingerprint+tx) returns existing outcome with completed
	// pin; completed pins never update to another revision outcome.
	owner, err := billing.ResolveProviderRevisionOwner(input)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := s.b2b2Fault("b2b2-enter"); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	// F1: already holds the marker lock; re-lock keeps ordinary SELECT out.
	markerRow, markerFound, err := s.loadAccountingCutoverLocked(ctx, tx)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	var revState billing.AccountingCutoverState
	var revVersion, revEpoch uint64
	var revGeneration int
	if markerFound {
		marker, merr := accountingCutoverRowToMarker(markerRow)
		if merr != nil {
			return billing.ProviderCostRevisionResult{}, merr
		}
		revState = marker.State
		revVersion = marker.Version
		revEpoch = marker.Epoch
		revGeneration = marker.Generation
	} else {
		revState = b2b2ProviderStateForMissingMarker()
		revVersion, revEpoch = 1, 1
		revGeneration = billing.AccountingCutoverGenerationV1
	}
	pinKey, err := billing.ProviderRevisionPostingOperationKey(input)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	// F5+F7: revision pin equals the canonical journal operation (scoped
	// sourceKey). Pin and journal share one immutable outcome identity.
	if pinKey != operationKey {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: revision pin/operation mismatch", billing.ErrProviderCostRevisionInvalid)
	}
	if input.Claim != nil {
		if input.Claim.OperationKey != pinKey {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: claim key %q != canonical %q", billing.ErrPostingOwnershipConflict, input.Claim.OperationKey, pinKey)
		}
		if input.Claim.Owner != owner {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: claim owner %q != posting owner %q", billing.ErrPostingOwnershipConflict, input.Claim.Owner, owner)
		}
		// R4 mandatory lease binding: when the token carries an economic
		// lease fence, it must match current delivery state before any
		// monetary effect. A reclaimed lease fences the stale token.
		if strings.TrimSpace(input.Claim.WorkID) != "" || strings.TrimSpace(input.Claim.LeaseOwner) != "" || input.Claim.LeaseFence != 0 {
			if err := input.Claim.Validate(); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			if strings.TrimSpace(input.Claim.WorkID) == "" || strings.TrimSpace(input.Claim.LeaseOwner) == "" || input.Claim.LeaseFence == 0 {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: economic lease binding requires work, owner and fence together", billing.ErrPostingOwnershipConflict)
			}
			var st struct {
				Status     string `bun:"status"`
				LeaseOwner string `bun:"lease_owner"`
				Fence      int64  `bun:"fence"`
			}
			if err := tx.NewRaw(`SELECT status, lease_owner, fence FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ? AND work_version = 1 LIMIT 1`, s.storeID, strings.TrimSpace(input.Claim.WorkID)).Scan(ctx, &st); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: economic lease %q not found", billing.ErrPostingOwnershipFence, strings.TrimSpace(input.Claim.WorkID))
				}
				return billing.ProviderCostRevisionResult{}, err
			}
			if strings.TrimSpace(st.Status) != economicRevisionWorkStateProcessing || strings.TrimSpace(st.LeaseOwner) != strings.TrimSpace(input.Claim.LeaseOwner) || uint64(st.Fence) != input.Claim.LeaseFence {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: economic lease fence mismatch for %q (stale token fenced)", billing.ErrPostingOwnershipFence, strings.TrimSpace(input.Claim.WorkID))
			}
		}
	}
	pinRow, pinFound, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	var pin billing.PostingPin
	if pinFound {
		pin, err = postingOwnershipRowToPin(pinRow)
		if err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if pin.Kind != billing.PostingOperationProviderCharge || pin.OperationKey != pinKey || pin.AccountID != input.AccountID || pin.CallID != input.CallID {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider revision pin identity mismatch for %q", billing.ErrPostingOwnershipConflict, pinKey)
		}
		if pin.Owner != owner {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider revision pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, pin.Owner, owner)
		}
	}
	// ensureRevisionReplayPin backfills/completes the pin for idempotent
	// replay paths (no new money). It never requires a claim. Completed pins
	// are immutable: exact same operation+fingerprint+tx replay only; callers
	// already verified fingerprint/amount against durable head/fence/snapshot
	// before invoking. Backfill uses actual durable outcome, never fabricated tx.
	ensureRevisionReplayPin := func(completionOpKey, completionTxID string) error {
		if err := s.b2b2Fault("b2b2-replay-pin"); err != nil {
			return err
		}
		if !pinFound {
			nowUnix := nowUnixNano()
			insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
			if s.db.Dialect().Name() == dialect.PG {
				// bun uses ? placeholders for both dialects; ON CONFLICT works on PG.
			} else {
				insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			}
			if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), pinKey, input.AccountID, input.CallID.String(), input.Subject.BLegID, input.Subject.ProviderChargeID, "", string(input.Subject.Kind), string(subjectJSON), owner, int64(revVersion), int64(revEpoch), revGeneration, string(revState), string(billing.PostingPinCompleted), completionOpKey, completionTxID, nowUnix, nowUnix, nowUnix).Exec(ctx); err != nil {
				return fmt.Errorf("billingstore: b2b2 backfill revision pin: %w", err)
			}
			rrow, rfound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
			if rerr != nil {
				return rerr
			}
			if !rfound {
				return fmt.Errorf("%w: revision pin unavailable after backfill", billing.ErrPostingOwnershipNotFound)
			}
			repinned, perr := postingOwnershipRowToPin(rrow)
			if perr != nil {
				return perr
			}
			if repinned.Owner != owner {
				return fmt.Errorf("%w: revision pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repinned.Owner, owner)
			}
			if repinned.Status == billing.PostingPinPinned {
				pin = repinned
				pinFound = true
			} else {
				pin = repinned
				pinFound = true
				return nil
			}
		}
		if pinFound && pin.Status == billing.PostingPinCompleted {
			if pin.Owner != owner {
				return fmt.Errorf("%w: revision pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, pin.Owner, owner)
			}
			// Immutable outcome: exact replay must carry identical completion
			// identity. A different outcome under same pin is conflict, never
			// an update.
			if pin.CompletionOperationKey != completionOpKey || pin.CompletionTransactionID != completionTxID {
				return fmt.Errorf("%w: revision pin %q completion mismatch (immutable outcome)", billing.ErrPostingOwnershipConflict, pinKey)
			}
			return nil
		}
		if pinFound && pin.Status == billing.PostingPinPinned {
			nowUnix := nowUnixNano()
			if nowUnix < pin.CreatedAtUnix {
				nowUnix = pin.CreatedAtUnix
			}
			if err := b2b2CompleteProviderPinTx(ctx, tx, s.storeID, pinKey, completionOpKey, completionTxID, nowUnix); err != nil {
				rrow, rfound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
				if rerr != nil {
					return rerr
				}
				if !rfound {
					return fmt.Errorf("%w: revision pin missing after race", billing.ErrPostingOwnershipNotFound)
				}
				remarker, merr := postingOwnershipRowToPin(rrow)
				if merr != nil {
					return merr
				}
				if remarker.Status == billing.PostingPinCompleted && remarker.Owner == owner && remarker.CompletionOperationKey == completionOpKey && remarker.CompletionTransactionID == completionTxID {
					pin = remarker
					return nil
				}
				return fmt.Errorf("%w: revision pin completion race for %q", billing.ErrPostingOwnershipConflict, pinKey)
			}
			rrow, _, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
			if rerr != nil {
				return rerr
			}
			completed, perr := postingOwnershipRowToPin(rrow)
			if perr != nil {
				return perr
			}
			pin = completed
		}
		return nil
	}
	// checkRevisionNewPin enforces the one-owner fence for new postings
	// (payable deltas and nonpayable exclusions). F5: each revision is its own
	// pin; completed pins never authorize new money (exact replay uses the
	// ensure path, never this check). New V1 allowed only while marker permits
	// V1 (active/shadow or classified draining with current token); after
	// v2_active changed amount/evidence/higher revision with V1 owner fenced
	// even when older V1 pin/head exists. Draining requires a classified V1
	// pin plus matching current-marker token; v2_active permits only V2; stale
	// tokens fence before money. Pin owner stable; only token==current required
	// (F6, no pin-epoch comparison). F6 token binds revision-specific pin.
	checkRevisionNewPin := func() error {
		if input.Claim != nil {
			if input.Claim.MarkerVersion != revVersion || input.Claim.MarkerEpoch != revEpoch || input.Claim.MarkerState != revState {
				return fmt.Errorf("%w: provider revision claim %d/%d/%q != current %d/%d/%q",
					billing.ErrPostingOwnershipFence, input.Claim.MarkerVersion, input.Claim.MarkerEpoch, string(input.Claim.MarkerState), revVersion, revEpoch, string(revState))
			}
		}
		if !pinFound {
			if !billing.IsPostingOwnerAllowedForNew(revState, owner) {
				return fmt.Errorf("%w: provider revision owner %q not allowed for new in %q", billing.ErrPostingOwnershipFence, owner, string(revState))
			}
			if input.Claim != nil {
				return fmt.Errorf("%w: provider revision claim without classified pin for %q", billing.ErrPostingOwnershipFence, pinKey)
			}
			if revState == billing.AccountingCutoverV1Draining {
				return fmt.Errorf("%w: draining forbids new provider revision for %q", billing.ErrPostingOwnershipFence, pinKey)
			}
			if owner == billing.PostingOwnerV2 && !billing.IsV2NewWorkAuthorized(revState) {
				return fmt.Errorf("%w: V2 provider revision requires v2_active", billing.ErrCutoverV2NotAuthorized)
			}
			if err := s.b2b2Fault("b2b2-pin-acquire"); err != nil {
				return err
			}
			nowUnix := nowUnixNano()
			acquired, err := b2b2InsertProviderPinTx(ctx, tx, s, input.AccountID, input.CallID, s.storeID, input.Subject.BLegID, input.Subject.ProviderChargeID, string(input.Subject.Kind), string(subjectJSON), pinKey, owner, revVersion, revEpoch, revGeneration, revState, nowUnix)
			if err != nil {
				return err
			}
			pin = acquired
			pinFound = true
		} else {
			// F5 immutable: completed pins never authorize new outcomes.
			// Exact historical replay flows through ensureRevisionReplayPin;
			// any new money with same pin identity is conflict.
			if pin.Status == billing.PostingPinCompleted {
				return fmt.Errorf("%w: provider revision pin %q already completed (immutable outcome)", billing.ErrPostingOwnershipConflict, pinKey)
			}
			if revState == billing.AccountingCutoverV1Draining && input.Claim == nil {
				return fmt.Errorf("%w: draining provider revision requires claim metadata for %q", billing.ErrPostingOwnershipFence, pinKey)
			}
			if !billing.IsPostingPinAcquireReplayAllowed(revState, pin) {
				return fmt.Errorf("%w: provider revision pin replay not allowed in %q", billing.ErrPostingOwnershipFence, string(revState))
			}
			if !billing.IsPostingPinCompleteAllowed(revState, pin) && pin.Status == billing.PostingPinPinned {
				return fmt.Errorf("%w: provider revision pin completion not allowed in %q", billing.ErrPostingOwnershipFence, string(revState))
			}
		}
		return nil
	}
	// completeRevisionPin records the posting outcome atomically: pinned pins
	// complete once. Completed pins are immutable and never advance to another
	// revision outcome; callers must use distinct revision pins for replacements
	// and the ensure path for exact replay.
	completeRevisionPin := func(completionOpKey, completionTxID string) error {
		if err := s.b2b2Fault("b2b2-before-pin-complete"); err != nil {
			return err
		}
		nowUnix := nowUnixNano()
		if pinFound && nowUnix < pin.CreatedAtUnix {
			nowUnix = pin.CreatedAtUnix
		}
		if !pinFound {
			return fmt.Errorf("%w: revision pin missing for completion", billing.ErrPostingOwnershipNotFound)
		}
		if pin.Status == billing.PostingPinPinned {
			if err := b2b2CompleteProviderPinTx(ctx, tx, s.storeID, pinKey, completionOpKey, completionTxID, nowUnix); err != nil {
				return err
			}
			rrow, _, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
			if rerr != nil {
				return rerr
			}
			completed, perr := postingOwnershipRowToPin(rrow)
			if perr != nil {
				return perr
			}
			pin = completed
			return nil
		}
		return fmt.Errorf("%w: provider revision pin %q already completed (immutable, use distinct revision pin)", billing.ErrPostingOwnershipConflict, pinKey)
	}
	_ = ensureRevisionReplayPin
	_ = checkRevisionNewPin
	_ = completeRevisionPin
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	// R3: deterministic durable disposition for terminal no-money outcomes
	// (stale/superseded/ignored/excluded/nonpayable) in the same tx. No journal
	// or money movement is fabricated: the operation snapshot records the
	// immutable fingerprint with zero balance delta (Before==After, no sequence),
	// and the per-revision pin completes with (operationKey, "") when it is not
	// already terminal. Exact replay (same operation+fingerprint) succeeds
	// idempotently; conflicting replay (same pin, different fingerprint) fails
	// closed without mutating the completed outcome. Pin identity remains per
	// immutable logical revision; completed outcomes are never reopened.
	// A stale replay of an already-posted payable revision (pin completed with
	// its real journal tx) is terminal success without mutating that payable
	// outcome: snapshot fingerprint already proves exactness.
	completeNoMoneyOutcome := func() error {
		existing, exists, lookupErr := loadOperationSnapshot(ctx, tx, input.AccountID, "provider_call_cogs", sourceKey)
		if lookupErr != nil {
			return lookupErr
		}
		if exists {
			if existing.Fingerprint != fingerprint {
				return ErrOperationConflict
			}
			if existing.OperationKey != operationKey {
				return fmt.Errorf("%w: revision snapshot operation mismatch for %q", billing.ErrProviderCostRevisionConflict, sourceKey)
			}
		}
		// Already-terminal pin (payable posted with real tx, or prior no-money
		// completed) stays immutable: exact fingerprint (snapshot above) proves
		// terminal success without mutating the outcome. Only pinned/missing
		// pins advance to (operationKey, "") below.
		if pinFound && pin.Status == billing.PostingPinCompleted {
			if pin.Owner != owner {
				return fmt.Errorf("%w: revision pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, pin.Owner, owner)
			}
			if !exists {
				if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
					OperationKey: operationKey, AccountID: input.AccountID, OperationKind: "provider_call_cogs", SourceKey: sourceKey,
					Fingerprint: fingerprint, Before: before, After: before,
				}); err != nil {
					return err
				}
			}
			return nil
		}
		if !exists {
			if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
				OperationKey: operationKey, AccountID: input.AccountID, OperationKind: "provider_call_cogs", SourceKey: sourceKey,
				Fingerprint: fingerprint, Before: before, After: before,
			}); err != nil {
				return err
			}
		}
		return ensureRevisionReplayPin(operationKey, "")
	}

	lineageKey, err := providerCostRevisionLineageKey(input)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	executionLineageKey, err := providerCostExecutionLineageKey(input.CallID, input.Subject.BLegID)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	// The execution fence is the cross-generation writer boundary. It is keyed
	// by the concrete B-leg lineage and therefore is shared by a legacy
	// aggregate and all V2 provider-charge child heads. The posting fence below
	// remains per selected head so distinct provider charges retain independent
	// delta histories.
	executionFence, executionFound, err := s.loadProviderCostExecutionFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, true)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	fence, fenceFound, err := s.loadProviderCostPostingFence(ctx, tx, input.AccountID, input.CallID, lineageKey, true)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	canonicalFence := providerCostPostingFenceRow{}
	canonicalFenceFound := false
	if lineageKey != executionLineageKey {
		canonicalFence, canonicalFenceFound, err = s.loadProviderCostPostingFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, true)
		if err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	}
	fenceNeedsInsert := false
	if !canonicalFenceFound && lineageKey == executionLineageKey && fenceFound {
		canonicalFence, canonicalFenceFound = fence, true
	}
	if !canonicalFenceFound {
		if recovered, recoveredFound, recoveryErr := s.recoverLegacyProviderCostFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, account.Currency); recoveryErr != nil {
			return billing.ProviderCostRevisionResult{}, recoveryErr
		} else if recoveredFound {
			canonicalFence, canonicalFenceFound = recovered, true
			if lineageKey == executionLineageKey {
				fence, fenceFound, fenceNeedsInsert = recovered, true, true
			}
		}
	}

	// A PostgreSQL row lock serializes same-head revisions before the CAS
	// transition. SQLite relies on its writer transaction, while the CAS still
	// protects against a stale snapshot if a competing writer wins first.
	head, found, err := s.loadProviderCostHead(ctx, tx, input.AccountID, input.CallID, input.HeadKey, true)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if !fenceFound && found {
		// Databases upgraded from the first revision implementation have provider
		// heads but no cross-path fence. Seed the durable fence from that already
		// committed head before any new result can return or post.
		fence = providerCostPostingFenceFromHead(s, input, head)
		fence.Authority = providerCostFenceAuthorityRevision
		fence.Fingerprint = "provider-cost-head:v1:" + head.InputSetHash
		if err := s.insertProviderCostPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, lineageKey,
			fence.Authority, fence.HeadKey, fence.EvidenceRevision, fence.InputSetHash, fence.Fingerprint,
			providerCostFenceAmount(fence), fence.OriginalTransactionID, fence.LastOperationKey, fence.LastTransactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		fenceFound = true
	}
	if !canonicalFenceFound && lineageKey == executionLineageKey && fenceFound {
		canonicalFence, canonicalFenceFound = fence, true
	}
	providerChargeFence := providerCostPostingFenceRow{}
	providerChargeFenceFound := false
	if !executionFound && !canonicalFenceFound && !fenceFound {
		// A database upgraded from the first V2 revision schema may already have
		// one or more provider-charge child fences without the execution-level
		// gate. Recover any child before a B-leg aggregate can claim the same
		// execution. The child fence is an unambiguous proof that revision
		// authority already owns this B-leg, regardless of which child arrived
		// first.
		providerChargeFence, providerChargeFenceFound, err = s.loadProviderCostProviderChargeFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, true)
		if err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if providerChargeFenceFound && providerChargeFence.Authority != providerCostFenceAuthorityRevision {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider charge fence authority", billing.ErrProviderCostRevisionFence)
		}
	}
	if !executionFound {
		var seeded providerCostExecutionFenceRow
		var seedErr error
		switch {
		case canonicalFenceFound:
			seeded, seedErr = s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, executionLineageKey, canonicalFence, string(metering.SubjectBLeg))
		case fenceFound:
			seeded, seedErr = s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, executionLineageKey, fence, string(input.Subject.Kind))
		case found:
			seeded, seedErr = s.seedProviderCostExecutionFenceFromHeadInTx(ctx, tx, input, executionLineageKey, head)
		case providerChargeFenceFound:
			seeded, seedErr = s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, executionLineageKey, providerChargeFence, string(metering.SubjectProviderCharge))
		default:
			fingerprint, fingerprintErr := input.SemanticFingerprint()
			if fingerprintErr != nil {
				return billing.ProviderCostRevisionResult{}, fingerprintErr
			}
			if err := s.insertProviderCostExecutionFenceInTx(ctx, tx, input.AccountID, input.CallID, executionLineageKey,
				providerCostFenceAuthorityRevision, string(input.Subject.Kind), input.HeadKey, int64(input.EvidenceRevision),
				input.InputSetHash, fingerprint, operationKey, ""); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			var seededFound bool
			seeded, seededFound, seedErr = s.loadProviderCostExecutionFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, true)
			if seedErr == nil && !seededFound {
				seedErr = fmt.Errorf("billingstore: inserted provider cost execution fence was not found")
			}
		}
		if seedErr != nil {
			return billing.ProviderCostRevisionResult{}, seedErr
		}
		executionFence, executionFound = seeded, true
	}
	if fenceFound && fence.Authority == providerCostFenceAuthorityRevision && found {
		if fence.EvidenceRevision != head.EvidenceRevision || fence.InputSetHash != head.InputSetHash || fence.AmountNano != head.AmountNano || fence.Currency != head.Currency {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost posting fence/head mismatch", billing.ErrProviderCostRevisionFence)
		}
	}
	if executionFound && executionFence.Authority == providerCostFenceAuthorityRevision && executionFence.OwnerSubjectKind != string(input.Subject.Kind) {
		// A B-leg aggregate and a provider-charge child are mutually exclusive
		// owners of one execution. Distinct children of the same V2 authority
		// remain independent and continue below on their own posting heads.
		// R3: terminal no-money exclusion completes its own revision pin
		// atomically with snapshot fingerprint; exact replay succeeds via
		// ensure, conflicting replay fails closed. No journal fabricated.
		if err := s.b2b2Fault("b2b2-before-pin-complete"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := completeNoMoneyOutcome(); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost execution fence exclusion: %w", err)
		}
		return ignoredProviderCostRevision(input), nil
	}
	if input.Cost.Completeness == billing.CostCompletenessPartial && executionFound && executionFence.Authority == providerCostFenceAuthorityLegacy {
		// Legacy already owns this execution. Partial/unavailable evidence has no
		// amount to reconcile and must not turn the legacy aggregate into a zero
		// reversal; the existing legacy authority remains the durable fence.
		// R3: same terminal no-money disposition as above.
		if err := s.b2b2Fault("b2b2-before-pin-complete"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := completeNoMoneyOutcome(); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit partial provider cost exclusion: %w", err)
		}
		return ignoredProviderCostRevision(input), nil
	}
	if input.Subject.Kind == metering.SubjectProviderCharge && executionFound && executionFence.Authority == providerCostFenceAuthorityLegacy {
		// A legacy aggregate may already include this and other provider charges;
		// without an explicit correction link, a child amount cannot prove that it
		// is additive. Preserve the legacy first-writer authority and fence every
		// later child revision rather than risking an overlapping aggregate.
		// R3: same terminal no-money disposition.
		if err := s.b2b2Fault("b2b2-before-pin-complete"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := completeNoMoneyOutcome(); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost child exclusion: %w", err)
		}
		return ignoredProviderCostRevision(input), nil
	}
	if !input.Cost.Payable && executionFound && executionFence.Authority == providerCostFenceAuthorityRevision &&
		(executionFence.OwnerSubjectKind != string(input.Subject.Kind) || executionFence.OwnerHeadKey != input.HeadKey) {
		// A non-payable revision for another head is a complete no-op once this
		// execution has already been claimed. Keep the first durable exclusion
		// identity stable while allowing a later payable revision for its own head
		// to advance the selected-cost reducer.
		// R3: same terminal no-money disposition.
		if err := s.b2b2Fault("b2b2-before-pin-complete"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := completeNoMoneyOutcome(); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost exclusion replay: %w", err)
		}
		return ignoredProviderCostRevision(input), nil
	}
	if fenceFound && fence.Authority == providerCostFenceAuthorityLegacy {
		// B2b2: enforce pin ownership before legacy handoff. The handoff posts
		// money in its own tx; pin completion converges on retry via the main
		// replacement/replay path (equivalent exact fence: money fence blocks
		// duplicates while the pin stays pinned).
		if err := checkRevisionNewPin(); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		return s.applyProviderCostRevisionAfterLegacyFence(ctx, tx, input, before, sourceKey, fingerprint, operationKey, lineageKey, fence, fenceNeedsInsert, head, found, amount)
	}
	if fenceFound && fence.Authority == providerCostFenceAuthorityRevision && !found {
		// A known non-payable revision intentionally has a fence but no provider
		// head. It must suppress a stale legacy worker; a later payable revision
		// may still promote the zero fence into a real selected-cost head.
		if input.EvidenceRevision < uint64(fence.EvidenceRevision) {
			// R3: stale vs exclusion fence is terminal with own pin disposition.
			if err := completeNoMoneyOutcome(); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost exclusion replay: %w", err)
			}
			return providerCostRevisionStale(input, providerCostFenceAmount(fence)), nil
		}
		if input.EvidenceRevision == uint64(fence.EvidenceRevision) {
			if fence.InputSetHash != input.InputSetHash || fence.Fingerprint != fingerprint || providerCostFenceAmount(fence) != amount {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost exclusion %s revision=%d", billing.ErrProviderCostRevisionConflict, lineageKey, input.EvidenceRevision)
			}
			// R3: exact exclusion replay validates via fence above plus snapshot
			// below; conflicting fingerprint fails closed in helper.
			if err := completeNoMoneyOutcome(); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost exclusion replay: %w", err)
			}
			return ignoredProviderCostRevision(input), nil
		}
		if err := checkRevisionNewPin(); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		return s.applyProviderCostRevisionAfterLegacyFence(ctx, tx, input, before, sourceKey, fingerprint, operationKey, lineageKey, fence, false, head, found, amount)
	}
	if !found && !fenceFound && !input.Cost.Payable {
		// A known non-operator payer is an exclusion, not a zero-valued head.
		// Once a payable head exists, the same exclusion is handled below as an
		// authoritative zero target so a payer correction reverses prior COGS.
		// R3: first exclusion records fence plus snapshot/pin atomically; exact
		// replay later hits the fence-exact branch above and succeeds via the
		// same snapshot identity. No journal fabricated.
		if err := s.b2b2Fault("b2b2-before-effects"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.insertProviderCostPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, lineageKey,
			providerCostFenceAuthorityRevision, input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, amount, "", operationKey, ""); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.economicFault("after_provider_cost_revision"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-pin-complete"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := completeNoMoneyOutcome(); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost exclusion: %w", err)
		}
		return ignoredProviderCostRevision(input), nil
	}
	if found {
		previous := billing.Money{Nano: head.AmountNano, Currency: head.Currency}
		if input.EvidenceRevision < uint64(head.EvidenceRevision) {
			// R3: older revision behind newer head is terminal stale with its
			// own pin disposition in the same tx (plus seeded fence when the
			// head predates fences). The worker treats nil error as success
			// and completes its queue item; activation no longer strands a pin.
			if err := completeNoMoneyOutcome(); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit stale provider cost revision: %w", err)
			}
			return providerCostRevisionStale(input, previous), nil
		}
		if input.EvidenceRevision == uint64(head.EvidenceRevision) && head.InputSetHash == input.InputSetHash {
			if head.InputSetHash != input.InputSetHash || head.ValuationID != input.ValuationID ||
				head.SubjectKind != string(input.Subject.Kind) || head.SubjectID != subjectIDForEconomics(input.Subject) ||
				head.SubjectJSON != string(subjectJSON) || previous != amount {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: head=%s revision=%d", billing.ErrProviderCostRevisionConflict, input.HeadKey, input.EvidenceRevision)
			}
			// A crash cannot leave the head and operation rows split because both
			// are committed together, but this repair path makes a manually
			// truncated snapshot safe as well. F7: replay backfill uses actual
			// durable outcome (head tx), never fabricated.
			if existing, exists, lookupErr := loadOperationSnapshot(ctx, tx, input.AccountID, "provider_call_cogs", sourceKey); lookupErr != nil {
				return billing.ProviderCostRevisionResult{}, lookupErr
			} else if exists {
				if existing.Fingerprint != fingerprint {
					return billing.ProviderCostRevisionResult{}, ErrOperationConflict
				}
				replayTx := head.LastTransactionID
				if replayTx == "" {
					replayTx = fence.LastTransactionID
				}
				if err := ensureRevisionReplayPin(existing.OperationKey, replayTx); err != nil {
					return billing.ProviderCostRevisionResult{}, err
				}
				if err := tx.Commit(); err != nil {
					return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost replay: %w", err)
				}
				return providerCostRevisionReplayed(input, previous, existing.OperationKey), nil
			}
			if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
				OperationKey: operationKey, AccountID: input.AccountID, OperationKind: "provider_call_cogs", SourceKey: sourceKey,
				Fingerprint: fingerprint, Before: before, After: before,
			}); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			repairTx := head.LastTransactionID
			if repairTx == "" {
				repairTx = fence.LastTransactionID
			}
			if err := ensureRevisionReplayPin(operationKey, repairTx); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost replay repair: %w", err)
			}
			return providerCostRevisionReplayed(input, previous, operationKey), nil
		}
		existingIdentity, identityErr := billing.NewEconomicRevisionIdentity(billing.EconomicQueueProvider, head.HeadKey, uint64(head.EvidenceRevision), head.InputSetHash)
		incomingIdentity, incomingErr := billing.NewEconomicRevisionIdentity(billing.EconomicQueueProvider, input.HeadKey, input.EvidenceRevision, input.InputSetHash)
		if identityErr != nil || incomingErr != nil {
			// R3: unorderable identities are terminal stale with pin disposition.
			if err := completeNoMoneyOutcome(); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit stale provider cost revision: %w", err)
			}
			return providerCostRevisionStale(input, previous), nil
		}
		advance := existingIdentity.Less(incomingIdentity)
		if existingIdentity.EvidenceRevision == incomingIdentity.EvidenceRevision && existingIdentity.InputSetHash != incomingIdentity.InputSetHash {
			relation, relationErr := s.providerCostEvidenceRelation(ctx, tx, head, input)
			if relationErr != nil {
				return billing.ProviderCostRevisionResult{}, relationErr
			}
			switch relation {
			case billing.EconomicEvidenceSetCandidateSuperset:
				advance = true
			case billing.EconomicEvidenceSetCandidateSubset:
				advance = false
			case billing.EconomicEvidenceSetEqual:
				// Equal reference sets are semantically equivalent. Hash
				// ordering is retained only as their deterministic tie-breaker.
				advance = existingIdentity.Less(incomingIdentity)
			case billing.EconomicEvidenceSetIncomparable:
				// The provider-cost writer cannot rate a missing union. Keep
				// this work retryable rather than acknowledging one branch and
				// silently dropping the other branch's evidence.
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: incomparable same-revision provider evidence sets", billing.ErrProviderCostRevisionFence)
			}
		}
		if !advance {
			// R3: superseded same-revision loser (candidate-subset or hash
			// tie-break) is terminal stale with its own pin disposition in the
			// same tx. No journal fabricated; exact replay succeeds via ensure.
			if err := completeNoMoneyOutcome(); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit stale provider cost revision: %w", err)
			}
			return providerCostRevisionStale(input, previous), nil
		}
		// B2b2: replacement revision retains one pin authority; pin + delta/
		// head/fence effects commit atomically.
		if err := checkRevisionNewPin(); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-effects"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-journal"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		posting, err := s.postProviderCostDeltaInTx(ctx, tx, input, before, previous, amount, operationKey, head.LastTransactionID)
		if err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		delta, err := postingDelta(previous, amount)
		if err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		transactionID := posting.Transaction.ID
		if transactionID == "" {
			transactionID = head.LastTransactionID
			if transactionID == "" {
				transactionID = fence.LastTransactionID
			}
		}
		if err := s.advanceProviderCostHeadInTx(ctx, tx, head, input, amount, operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
			OperationKey: operationKey, AccountID: input.AccountID, OperationKind: "provider_call_cogs", SourceKey: sourceKey,
			Fingerprint: fingerprint, Before: before, After: before,
			SequenceStart: posting.Transaction.AccountSequence, SequenceEnd: posting.Transaction.AccountSequence,
		}); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.advanceProviderCostPostingFenceInTx(ctx, tx, fence, input.AccountID, input.CallID, lineageKey,
			providerCostFenceAuthorityRevision, input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, amount, operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		// Every applied revision atomically advances the execution fence
		// to the new current owner envelope in the same transaction,
		// whether the fence was just inserted or already existed.
		// Replays, stale revisions, and ignored exclusions return earlier
		// and never reach this branch, so the fence always names the
		// latest applied revision.
		if err := s.advanceProviderCostExecutionFenceInTx(ctx, tx, executionFence, input.AccountID, input.CallID, executionLineageKey,
			providerCostFenceAuthorityRevision, string(input.Subject.Kind), input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.economicFault("after_provider_cost_revision"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := completeRevisionPin(operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost revision: %w", err)
		}
		return billing.ProviderCostRevisionResult{
			AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey,
			ALegID: input.Subject.ALegID, BLegID: input.Subject.BLegID,
			Revision: input.EvidenceRevision, PreviousAmount: previous, CurrentAmount: amount,
			Delta: delta, Posting: posting, Applied: true,
		}, nil
	}

	// B2b2: first payable revision acquires/completes the pin atomically.
	if err := checkRevisionNewPin(); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := s.b2b2Fault("b2b2-before-effects"); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := s.b2b2Fault("b2b2-before-journal"); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	posting, err := s.postProviderCostDeltaInTx(ctx, tx, input, before, billing.Money{Currency: account.Currency}, amount, operationKey, "")
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	delta, err := postingDelta(billing.Money{Currency: account.Currency}, amount)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := s.insertProviderCostHeadInTx(ctx, tx, input, amount, posting.Transaction.ID, operationKey, posting.Transaction.ID); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
		OperationKey: operationKey, AccountID: input.AccountID, OperationKind: "provider_call_cogs", SourceKey: sourceKey,
		Fingerprint: fingerprint, Before: before, After: before,
		SequenceStart: posting.Transaction.AccountSequence, SequenceEnd: posting.Transaction.AccountSequence,
	}); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := s.insertProviderCostPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, lineageKey,
		providerCostFenceAuthorityRevision, input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
		fingerprint, amount, posting.Transaction.ID, operationKey, posting.Transaction.ID); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	// Every applied first revision advances the shared execution gate
	// to the new owner's envelope in the same transaction, whether the
	// gate was just inserted or already named another head: the gate
	// always names the most recently applied revision.
	if err := s.advanceProviderCostExecutionFenceInTx(ctx, tx, executionFence, input.AccountID, input.CallID, executionLineageKey,
		providerCostFenceAuthorityRevision, string(input.Subject.Kind), input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
		fingerprint, operationKey, posting.Transaction.ID); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := s.economicFault("after_provider_cost_revision"); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := completeRevisionPin(operationKey, posting.Transaction.ID); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost revision: %w", err)
	}
	return billing.ProviderCostRevisionResult{
		AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey,
		ALegID: input.Subject.ALegID, BLegID: input.Subject.BLegID,
		Revision: input.EvidenceRevision, PreviousAmount: billing.Money{Currency: account.Currency},
		CurrentAmount: amount, Delta: delta, Posting: posting, Applied: true,
	}, nil
}

func providerCostPostingFenceFromHead(s *DurableStore, input billing.ProviderCostRevisionInput, head providerCostHeadRow) providerCostPostingFenceRow {
	originalTransactionID := providerCostOriginalTransactionID(head.OriginalTransactionID, head.LastTransactionID)
	return providerCostPostingFenceRow{
		StoreID: s.storeID, AccountID: input.AccountID, CallID: input.CallID.String(),
		Authority: providerCostFenceAuthorityRevision, HeadKey: head.HeadKey,
		EvidenceRevision: head.EvidenceRevision, InputSetHash: head.InputSetHash,
		AmountNano: head.AmountNano, Currency: head.Currency, Fence: 1,
		OriginalTransactionID: originalTransactionID,
		LastOperationKey:      head.LastOperationKey, LastTransactionID: head.LastTransactionID,
	}
}

func providerCostOriginalTransactionID(original, latest string) string {
	if strings.TrimSpace(original) != "" {
		return original
	}
	return latest
}

// applyProviderCostRevisionAfterLegacyFence performs the one-time handoff from
// the legacy LUR writer to the revision writer. F7: both legacy handoff and
// promotion branches acquire the revision-specific pin, post delta/update
// heads/fences, and complete pin atomically in SAME transaction before commit.
// Pre-pin legacy rows exact replay backfill from actual durable outcome; no
// fabricated tx. The legacy amount is the durable prior head; a matching first
// revision only adopts the new identity, while a later revision posts the
// exact signed delta from that amount.
func (s *DurableStore) applyProviderCostRevisionAfterLegacyFence(ctx context.Context, tx bun.Tx, input billing.ProviderCostRevisionInput, before billing.AccountSnapshot, sourceKey, fingerprint, operationKey, lineageKey string, fence providerCostPostingFenceRow, fenceNeedsInsert bool, head providerCostHeadRow, headFound bool, amount billing.Money) (billing.ProviderCostRevisionResult, error) {
	previous := providerCostFenceAmount(fence)
	if input.EvidenceRevision < uint64(fence.EvidenceRevision) {
		if fenceNeedsInsert {
			if err := s.insertProviderCostPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, lineageKey,
				fence.Authority, fence.HeadKey, fence.EvidenceRevision, fence.InputSetHash, fence.Fingerprint,
				previous, fence.OriginalTransactionID, fence.LastOperationKey, fence.LastTransactionID); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
		}
		// R3: legacy stale is terminal with its own pin disposition plus
		// snapshot fingerprint (fail closed on conflict). No journal fabricated.
		if existing, exists, lookupErr := loadOperationSnapshot(ctx, tx, input.AccountID, "provider_call_cogs", sourceKey); lookupErr != nil {
			return billing.ProviderCostRevisionResult{}, lookupErr
		} else if exists {
			if existing.Fingerprint != fingerprint {
				return billing.ProviderCostRevisionResult{}, ErrOperationConflict
			}
		} else if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
			OperationKey: operationKey, AccountID: input.AccountID, OperationKind: "provider_call_cogs", SourceKey: sourceKey,
			Fingerprint: fingerprint, Before: before, After: before,
		}); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.ensureNoMoneyRevisionPinInTx(ctx, tx, input, operationKey); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit stale provider cost revision: %w", err)
		}
		return providerCostRevisionStale(input, previous), nil
	}
	if input.EvidenceRevision == uint64(fence.EvidenceRevision) && amount != previous {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: legacy provider cost fence %s revision=%d", billing.ErrProviderCostRevisionConflict, lineageKey, input.EvidenceRevision)
	}
	if headFound && (head.AmountNano != previous.Nano || head.Currency != previous.Currency) {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: legacy provider cost head/fence mismatch", billing.ErrProviderCostRevisionFence)
	}

	posting, err := s.postProviderCostDeltaInTx(ctx, tx, input, before, previous, amount, operationKey, fence.LastTransactionID)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	delta, err := postingDelta(previous, amount)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	transactionID := posting.Transaction.ID
	if transactionID == "" {
		transactionID = head.LastTransactionID
		if transactionID == "" {
			transactionID = fence.LastTransactionID
		}
	}
	if headFound {
		if input.EvidenceRevision > uint64(head.EvidenceRevision) {
			if err := s.advanceProviderCostHeadInTx(ctx, tx, head, input, amount, operationKey, transactionID); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
		}
	} else {
		if err := s.insertProviderCostHeadInTx(ctx, tx, input, amount, providerCostOriginalTransactionID(fence.OriginalTransactionID, fence.LastTransactionID), operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	}
	if existing, exists, lookupErr := loadOperationSnapshot(ctx, tx, input.AccountID, "provider_call_cogs", sourceKey); lookupErr != nil {
		return billing.ProviderCostRevisionResult{}, lookupErr
	} else if exists {
		if existing.Fingerprint != fingerprint {
			return billing.ProviderCostRevisionResult{}, ErrOperationConflict
		}
	} else if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
		OperationKey: operationKey, AccountID: input.AccountID, OperationKind: "provider_call_cogs", SourceKey: sourceKey,
		Fingerprint: fingerprint, Before: before, After: before,
		SequenceStart: posting.Transaction.AccountSequence, SequenceEnd: posting.Transaction.AccountSequence,
	}); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if fenceNeedsInsert {
		if err := s.insertProviderCostPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, lineageKey,
			providerCostFenceAuthorityRevision, input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, amount, fence.OriginalTransactionID, operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	} else {
		if err := s.advanceProviderCostPostingFenceInTx(ctx, tx, fence, input.AccountID, input.CallID, lineageKey,
			providerCostFenceAuthorityRevision, input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, amount, operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	}
	executionLineageKey, err := providerCostExecutionLineageKey(input.CallID, input.Subject.BLegID)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if executionFence, executionFound, executionErr := s.loadProviderCostExecutionFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, true); executionErr != nil {
		return billing.ProviderCostRevisionResult{}, executionErr
	} else if executionFound && executionFence.Authority == providerCostFenceAuthorityLegacy && input.EvidenceRevision == uint64(fence.EvidenceRevision) {
		if err := s.advanceProviderCostExecutionFenceInTx(ctx, tx, executionFence, input.AccountID, input.CallID, executionLineageKey,
			providerCostFenceAuthorityRevision, string(input.Subject.Kind), input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	} else if input.EvidenceRevision != uint64(fence.EvidenceRevision) {
		// An applied cutover or promotion advances an already
		// revision-owned execution fence to the new current owner
		// envelope in the same transaction. Same-revision replays
		// keep the fence untouched.
		if executionFence, executionFound, executionErr := s.loadProviderCostExecutionFence(ctx, tx, input.AccountID, input.CallID, executionLineageKey, true); executionErr != nil {
			return billing.ProviderCostRevisionResult{}, executionErr
		} else if !executionFound {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost execution fence %s", billing.ErrProviderCostRevisionFence, executionLineageKey)
		} else if err := s.advanceProviderCostExecutionFenceInTx(ctx, tx, executionFence, input.AccountID, input.CallID, executionLineageKey,
			providerCostFenceAuthorityRevision, string(input.Subject.Kind), input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	}
	if err := s.economicFault("after_provider_cost_revision"); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	// F7 atomic: complete revision-specific pin in same tx before commit.
	// Pin key equals operationKey (revision-specific immutable outcome).
	// Completion uses actual durable transaction (new journal or existing
	// legacy/head tx for zero-delta adoption), never fabricated.
	if err := s.b2b2Fault("b2b2-before-pin-complete"); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	nowUnix := nowUnixNano()
	if err := b2b2CompleteProviderPinTx(ctx, tx, s.storeID, operationKey, operationKey, transactionID, nowUnix); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
		return billing.ProviderCostRevisionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost legacy cutover: %w", err)
	}
	result := billing.ProviderCostRevisionResult{
		AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey,
		ALegID: input.Subject.ALegID, BLegID: input.Subject.BLegID,
		Revision: input.EvidenceRevision, PreviousAmount: previous, CurrentAmount: amount,
		Delta: delta, Posting: posting,
	}
	if input.EvidenceRevision == uint64(fence.EvidenceRevision) {
		result.Replayed = true
	} else {
		result.Applied = true
	}
	return result, nil
}

func postingDelta(previous, current billing.Money) (billing.Money, error) {
	return current.Sub(previous)
}

func (s *DurableStore) providerCostEvidenceRelation(ctx context.Context, tx bun.Tx, head providerCostHeadRow, input billing.ProviderCostRevisionInput) (billing.EconomicEvidenceSetRelation, error) {
	currentRefs, found, err := loadValuationInputObservationsInTx(ctx, tx, s.storeID, head.ValuationID, int64(economics.ValuationVersionV2))
	if err != nil {
		return billing.EconomicEvidenceSetIncomparable, err
	}
	if !found {
		return billing.EconomicEvidenceSetIncomparable, fmt.Errorf("%w: provider head valuation evidence %q is unavailable", billing.ErrProviderCostRevisionFence, head.ValuationID)
	}
	candidateRefs, err := providerCostEvidenceRefs(input)
	if err != nil {
		return billing.EconomicEvidenceSetIncomparable, err
	}
	relation, err := billing.CompareEconomicEvidenceSets(currentRefs, candidateRefs)
	if err != nil {
		return billing.EconomicEvidenceSetIncomparable, fmt.Errorf("%w: compare provider evidence: %v", billing.ErrProviderCostRevisionFence, err)
	}
	return relation, nil
}

func providerCostEvidenceRefs(input billing.ProviderCostRevisionInput) ([]metering.ObservationRef, error) {
	if len(input.Evidence.Observations) == 0 {
		return append([]metering.ObservationRef(nil), input.Evidence.ObservationRefs...), nil
	}
	refs := make([]metering.ObservationRef, 0, len(input.Evidence.Observations))
	for i, observation := range input.Evidence.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return nil, fmt.Errorf("%w: provider evidence observation %d: %v", billing.ErrProviderCostRevisionInvalid, i, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func providerCostRevisionStale(input billing.ProviderCostRevisionInput, current billing.Money) billing.ProviderCostRevisionResult {
	return billing.ProviderCostRevisionResult{
		AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey,
		ALegID: input.Subject.ALegID, BLegID: input.Subject.BLegID,
		Revision: input.EvidenceRevision, PreviousAmount: current, CurrentAmount: current,
		Delta: billing.Money{Currency: current.Currency}, Stale: true,
	}
}

func providerCostRevisionReplayed(input billing.ProviderCostRevisionInput, current billing.Money, operationKey string) billing.ProviderCostRevisionResult {
	return billing.ProviderCostRevisionResult{
		AccountID: input.AccountID, CallID: input.CallID, HeadKey: input.HeadKey,
		ALegID: input.Subject.ALegID, BLegID: input.Subject.BLegID,
		Revision: input.EvidenceRevision, PreviousAmount: current, CurrentAmount: current,
		Delta: billing.Money{Currency: current.Currency}, Posting: billing.Posting{OperationKey: operationKey, Replayed: true}, Replayed: true,
	}
}

func (s *DurableStore) postProviderCostDeltaInTx(ctx context.Context, tx bun.Tx, input billing.ProviderCostRevisionInput, before billing.AccountSnapshot, previous, current billing.Money, operationKey, priorTransactionID string) (billing.Posting, error) {
	delta, err := current.Sub(previous)
	if err != nil {
		return billing.Posting{}, err
	}
	posting := billing.Posting{OperationKey: operationKey, Before: before, After: before}
	if delta.Nano == 0 {
		return posting, nil
	}
	amount := delta
	debit, credit := "inference_provider_cogs", "provider_payable_clearing"
	if delta.Nano < 0 {
		amount, err = delta.Neg()
		if err != nil {
			return billing.Posting{}, err
		}
		debit, credit = credit, debit
	}
	journal := billing.JournalTransaction{
		ID: operationKey, Book: billing.JournalBookFinancial, Currency: amount.Currency,
		SourceKey: operationKey, AccountID: input.AccountID, TurnID: input.CallID.String(),
		ALegID: input.Subject.ALegID, BLegID: input.Subject.BLegID,
		OperationKind: "provider_call_cogs", CorrectionGroupID: input.HeadKey,
		BalanceBefore: before.BalanceNano, BalanceAfter: before.BalanceNano,
		SpendableBefore: before.SpendableNano, SpendableAfter: before.SpendableNano,
		CreditFloor: before.CreditFloorNano, CreditLimit: before.CreditLimitNano,
		Mode: string(before.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: before.Version,
		Entries: []billing.JournalEntry{
			{LedgerAccount: debit, Side: billing.JournalDebit, Amount: amount},
			{LedgerAccount: credit, Side: billing.JournalCredit, Amount: amount},
		},
	}
	if strings.TrimSpace(priorTransactionID) != "" {
		// Provider-cost adjustments are immutable delta postings. Keep both
		// approved correction references on the delta so auditors can follow
		// the latest-to-prior chain while CorrectionGroupID remains stable.
		journal.ReversalOf = priorTransactionID
		journal.CorrectsTransactionID = priorTransactionID
		journal.CorrectionGroupID = ""
	}
	posted, replayed, err := s.postJournalInTx(ctx, tx, journal)
	if err != nil {
		return billing.Posting{}, err
	}
	if replayed {
		posting.Transaction = posted
		posting.Replayed = true
		return posting, nil
	}
	posting.Transaction = posted
	return posting, nil
}

func (s *DurableStore) loadProviderCostHead(ctx context.Context, q bun.IDB, accountID string, callID billing.BillingCallID, headKey string, forUpdate bool) (providerCostHeadRow, bool, error) {
	query := providerCostHeadSelect + ` WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ? LIMIT 1`
	if forUpdate && q.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var row providerCostHeadRow
	err := q.NewRaw(query, s.storeID, accountID, callID.String(), headKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return providerCostHeadRow{}, false, nil
	}
	if err != nil {
		return providerCostHeadRow{}, false, fmt.Errorf("billingstore: load provider cost head: %w", err)
	}
	return row, true, nil
}

func (s *DurableStore) insertProviderCostHeadInTx(ctx context.Context, tx bun.Tx, input billing.ProviderCostRevisionInput, amount billing.Money, originalTransactionID, operationKey, transactionID string) error {
	subjectJSON, err := json.Marshal(input.Subject)
	if err != nil {
		return fmt.Errorf("billingstore: encode provider cost head subject: %w", err)
	}
	postedJSON, err := providerCostHeadPostedAmountJSON(amount)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixNano()
	if _, err := tx.NewRaw(`INSERT INTO billing_provider_cost_heads(
			store_id, account_id, call_id, head_key, subject_kind, subject_id, subject_json,
			evidence_revision, input_set_hash, valuation_id, amount_nano, currency, head_version, fence,
			last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix,
			valuation_revision, selection_status, selection_reason, selection_basis, selection_provenance,
			posted_amount_json, native_amount_json, fx_json, posting_state
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.storeID, input.AccountID, input.CallID.String(), input.HeadKey, string(input.Subject.Kind), subjectIDForEconomics(input.Subject), string(subjectJSON),
		input.EvidenceRevision, input.InputSetHash, input.ValuationID, amount.Nano, amount.Currency, 1, 1, operationKey, originalTransactionID, transactionID, now, now,
		input.EvidenceRevision, string(billing.OperatorCostSelectionStatusFinal), "", "", string(billing.OperatorCostProvenanceAttempted),
		postedJSON, "", "", string(billing.SelectedCostPostingApplied)).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert provider cost head: %w", err)
	}
	return nil
}

func (s *DurableStore) advanceProviderCostHeadInTx(ctx context.Context, tx bun.Tx, existing providerCostHeadRow, input billing.ProviderCostRevisionInput, amount billing.Money, operationKey, transactionID string) error {
	subjectJSON, err := json.Marshal(input.Subject)
	if err != nil {
		return fmt.Errorf("billingstore: encode provider cost correction subject: %w", err)
	}
	if existing.HeadVersion == math.MaxInt64 || existing.Fence == math.MaxInt64 {
		return fmt.Errorf("%w: provider cost head version overflow", billing.ErrProviderCostRevisionInvalid)
	}
	postedJSON, err := providerCostHeadPostedAmountJSON(amount)
	if err != nil {
		return err
	}
	result, err := tx.NewRaw(`UPDATE billing_provider_cost_heads SET
			subject_kind = ?, subject_id = ?, subject_json = ?, evidence_revision = ?, input_set_hash = ?, valuation_id = ?,
			amount_nano = ?, currency = ?, head_version = head_version + 1, fence = fence + 1,
			last_operation_key = ?, original_transaction_id = CASE WHEN original_transaction_id <> '' THEN original_transaction_id WHEN last_transaction_id <> '' THEN last_transaction_id ELSE ? END,
			last_transaction_id = ?, updated_at_unix = ?,
			valuation_revision = ?, selection_status = ?, selection_reason = ?, selection_basis = ?, selection_provenance = ?,
			posted_amount_json = ?, native_amount_json = ?, fx_json = ?, posting_state = ?
			WHERE id = ? AND head_version = ? AND fence = ?`,
		string(input.Subject.Kind), subjectIDForEconomics(input.Subject), string(subjectJSON), input.EvidenceRevision, input.InputSetHash, input.ValuationID,
		amount.Nano, amount.Currency, operationKey, transactionID, transactionID, time.Now().UTC().UnixNano(),
		input.EvidenceRevision, string(billing.OperatorCostSelectionStatusFinal), "", "", string(billing.OperatorCostProvenanceAttempted),
		postedJSON, "", "", string(billing.SelectedCostPostingApplied),
		existing.ID, existing.HeadVersion, existing.Fence).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: advance provider cost head: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("billingstore: provider cost head rows affected: %w", affectedErr)
	} else if affected != 1 {
		return fmt.Errorf("%w: provider cost head %s", billing.ErrProviderCostRevisionFence, input.HeadKey)
	}
	return nil
}

// GetProviderCostHead returns the current selected-cost pointer and never
// waits on or mutates the customer account balance.
func (s *DurableStore) GetProviderCostHead(ctx context.Context, accountID string, callID billing.BillingCallID, headKey string) (billing.ProviderCostHead, error) {
	if s == nil || s.db == nil {
		return billing.ProviderCostHead{}, fmt.Errorf("billingstore: nil store")
	}
	if ctx == nil {
		return billing.ProviderCostHead{}, fmt.Errorf("%w: nil context", billing.ErrProviderCostRevisionInvalid)
	}
	if err := ctx.Err(); err != nil {
		return billing.ProviderCostHead{}, fmt.Errorf("%w: %w", billing.ErrProviderCostRevisionInvalid, err)
	}
	if strings.TrimSpace(accountID) == "" || callID.Validate() != nil || strings.TrimSpace(headKey) != headKey || headKey == "" {
		return billing.ProviderCostHead{}, fmt.Errorf("%w: head identity", billing.ErrProviderCostRevisionInvalid)
	}
	row, found, err := s.loadProviderCostHead(ctx, s.db, strings.TrimSpace(accountID), callID, headKey, false)
	if err != nil {
		return billing.ProviderCostHead{}, err
	}
	if !found {
		return billing.ProviderCostHead{}, billing.ErrProviderCostHeadNotFound
	}
	return providerCostHeadFromRow(row)
}

// GetOperatorCostHead is the descriptive operator-side alias.
func (s *DurableStore) GetOperatorCostHead(ctx context.Context, accountID string, callID billing.BillingCallID, headKey string) (billing.OperatorCostHead, error) {
	return s.GetProviderCostHead(ctx, accountID, callID, headKey)
}

func providerCostHeadFromRow(row providerCostHeadRow) (billing.ProviderCostHead, error) {
	callID, err := billing.ParseBillingCallID(row.CallID)
	if err != nil {
		return billing.ProviderCostHead{}, err
	}
	var subject metering.SubjectRef
	if err := json.Unmarshal([]byte(row.SubjectJSON), &subject); err != nil {
		return billing.ProviderCostHead{}, fmt.Errorf("billingstore: decode provider cost head subject: %w", err)
	}
	return billing.ProviderCostHead{
		AccountID: row.AccountID, CallID: callID, HeadKey: row.HeadKey, Subject: subject,
		EvidenceRevision: uint64(maxInt64ToZero(row.EvidenceRevision)), InputSetHash: row.InputSetHash,
		ValuationID: row.ValuationID, CurrentAmount: billing.Money{Nano: row.AmountNano, Currency: row.Currency},
		HeadVersion: uint64(maxInt64ToZero(row.HeadVersion)), Fence: uint64(maxInt64ToZero(row.Fence)),
		LastOperationKey: row.LastOperationKey, OriginalTransactionID: providerCostOriginalTransactionID(row.OriginalTransactionID, row.LastTransactionID), LastTransactionID: row.LastTransactionID,
		UpdatedAt: time.Unix(0, row.UpdatedAt).UTC(),
	}, nil
}

// ensureNoMoneyRevisionPinInTx completes one terminal no-money revision pin
// (stale/superseded/ignored/excluded/nonpayable) with (operationKey, "") in the
// caller's tx. It backfills a completed pin when none exists, completes a pinned
// pin, and validates exact completion identity for already-completed pins
// (immutable outcome: different completion is conflict, never an update).
// No journal is fabricated; fingerprint conflict is enforced by the caller's
// operation snapshot, not here. Marker state is recorded for audit but never
// gates terminal no-money completion (draining new-money fences do not apply).
func (s *DurableStore) ensureNoMoneyRevisionPinInTx(ctx context.Context, tx bun.Tx, input billing.ProviderCostRevisionInput, operationKey string) error {
	normalized, err := input.Normalize()
	if err != nil {
		return err
	}
	owner, err := billing.ResolveProviderRevisionOwner(normalized)
	if err != nil {
		return err
	}
	subjectJSON, err := json.Marshal(normalized.Subject)
	if err != nil {
		return fmt.Errorf("billingstore: encode provider cost subject: %w", err)
	}
	markerRow, markerFound, err := s.loadAccountingCutoverLocked(ctx, tx)
	if err != nil {
		return err
	}
	var revVersion, revEpoch uint64
	var revGeneration int
	var revState billing.AccountingCutoverState
	if markerFound {
		marker, merr := accountingCutoverRowToMarker(markerRow)
		if merr != nil {
			return merr
		}
		revVersion, revEpoch, revState = marker.Version, marker.Epoch, marker.State
		revGeneration = marker.Generation
	} else {
		revState = b2b2ProviderStateForMissingMarker()
		revVersion, revEpoch = 1, 1
		revGeneration = billing.AccountingCutoverGenerationV1
	}
	pinRow, pinFound, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, operationKey)
	if err != nil {
		return err
	}
	if !pinFound {
		nowUnix := nowUnixNano()
		insert := `INSERT INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(store_id, operation_kind, operation_key) DO NOTHING`
		if s.db.Dialect().Name() == dialect.SQLite {
			insert = `INSERT OR IGNORE INTO billing_posting_ownership_pins (store_id, operation_kind, operation_key, account_id, call_id, b_leg_id, provider_charge_id, head_key, subject_kind, subject_json, owner, marker_version, marker_epoch, marker_generation, marker_state, status, completion_operation_key, completion_transaction_id, created_at_unix, updated_at_unix, completed_at_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		}
		if _, err := tx.NewRaw(insert, s.storeID, string(billing.PostingOperationProviderCharge), operationKey, normalized.AccountID, normalized.CallID.String(), normalized.Subject.BLegID, normalized.Subject.ProviderChargeID, "", string(normalized.Subject.Kind), string(subjectJSON), owner, int64(revVersion), int64(revEpoch), revGeneration, string(revState), string(billing.PostingPinCompleted), operationKey, "", nowUnix, nowUnix, nowUnix).Exec(ctx); err != nil {
			return fmt.Errorf("billingstore: backfill no-money revision pin: %w", err)
		}
		rrow, rfound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, operationKey)
		if rerr != nil {
			return rerr
		}
		if !rfound {
			return fmt.Errorf("%w: revision pin unavailable after backfill", billing.ErrPostingOwnershipNotFound)
		}
		repinned, perr := postingOwnershipRowToPin(rrow)
		if perr != nil {
			return perr
		}
		if repinned.Owner != owner {
			return fmt.Errorf("%w: revision pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repinned.Owner, owner)
		}
		if repinned.Status == billing.PostingPinCompleted {
			// Already terminal (payable posted or prior no-money). Snapshot
			// fingerprint validated by the caller proves exactness; leave the
			// immutable outcome untouched regardless of its completion tx.
			return nil
		}
		// Lost insert race with a pinned pin below: fall through to complete it.
		pinRow = rrow
	}
	pin, err := postingOwnershipRowToPin(pinRow)
	if err != nil {
		return err
	}
	if pin.Owner != owner {
		return fmt.Errorf("%w: revision pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, pin.Owner, owner)
	}
	if pin.Status == billing.PostingPinCompleted {
		// Same terminal-success rule as above.
		return nil
	}
	nowUnix := nowUnixNano()
	if nowUnix < pin.CreatedAtUnix {
		nowUnix = pin.CreatedAtUnix
	}
	if err := b2b2CompleteProviderPinTx(ctx, tx, s.storeID, operationKey, operationKey, "", nowUnix); err != nil {
		rrow, rfound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, operationKey)
		if rerr != nil {
			return rerr
		}
		if !rfound {
			return fmt.Errorf("%w: revision pin missing after race", billing.ErrPostingOwnershipNotFound)
		}
		remarker, merr := postingOwnershipRowToPin(rrow)
		if merr != nil {
			return merr
		}
		if remarker.Status == billing.PostingPinCompleted && remarker.Owner == owner && remarker.CompletionOperationKey == operationKey && remarker.CompletionTransactionID == "" {
			return nil
		}
		return fmt.Errorf("%w: revision pin completion race for %q", billing.ErrPostingOwnershipConflict, operationKey)
	}
	return nil
}
