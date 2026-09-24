package billingstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
)

var _ billing.ProviderCostStore = (*DurableStore)(nil)

func (s *DurableStore) ApplyProviderCost(ctx context.Context, input billing.ApplyProviderCostInput) (billing.Posting, error) {
	if s == nil || s.db == nil {
		return billing.Posting{}, fmt.Errorf("billingstore: nil store")
	}
	leg, err := input.Leg.Seal()
	if err != nil {
		return billing.Posting{}, err
	}
	if err := input.CallID.Validate(); err != nil {
		return billing.Posting{}, err
	}
	accountID := strings.TrimSpace(input.AccountID)
	if accountID == "" || leg.CallID != input.CallID || input.Result.LURKey != leg.Key {
		return billing.Posting{}, fmt.Errorf("%w: provider-cost identity mismatch", billing.ErrSettlementInvalid)
	}
	if _, err := billing.ResolveProviderCostOwner(input); err != nil {
		return billing.Posting{}, err
	}
	if input.Claim != nil {
		if err := billing.ValidateProviderCostClaim(*input.Claim, accountID, input.CallID, leg.Key); err != nil {
			return billing.Posting{}, err
		}
	}
	if err := billing.ValidateProviderCostAuthority(leg, input.Result); err != nil {
		// Validate before opening the monetary transaction: an estimate or local
		// fallback may be retained as advisory work, but it must never create a
		// provider_call_cogs journal, head, or posting fence.
		return billing.Posting{}, err
	}
	if input.Result.Amount.Nano < 0 || strings.TrimSpace(input.Result.Amount.Currency) == "" || !input.Result.AmountPresent || !input.Result.Reconciled {
		return billing.Posting{}, fmt.Errorf("%w: provider cost must be reconciled and present", billing.ErrUnreconciledCost)
	}
	if input.Result.Amount.Currency != input.Leg.Evidence.Cost.Currency && input.Leg.Evidence.Cost.Present && input.Leg.Evidence.Cost.Currency != "" {
		return billing.Posting{}, billing.ErrRatingCurrencyMismatch
	}
	return withAccountTx(ctx, accountTxRetry{Attempts: 40}, func() (billing.Posting, error) {
		return s.applyProviderCostAttempt(ctx, accountID, input.CallID, leg, input)
	})
}

func markProviderCostWorkProcessed(ctx context.Context, tx bun.Tx, legKey string) error {
	result, err := tx.NewRaw(`UPDATE provider_cost_work SET status = 'processed', updated_at = ? WHERE usage_leg_key = ? AND status = 'pending'`, time.Now().UTC(), legKey).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: mark provider cost work processed: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("billingstore: provider cost work rows affected: %w", err)
	} else if count > 1 {
		return fmt.Errorf("billingstore: provider cost work updated %d rows", count)
	}
	return nil
}

func (s *DurableStore) applyProviderCostAttempt(ctx context.Context, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord, input billing.ApplyProviderCostInput) (billing.Posting, error) {
	result := input.Result
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.Posting{}, fmt.Errorf("billingstore: begin provider cost: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// F1: lock the per-store marker before account/pin effects. Provider COGS
	// still does not lock billing_accounts for balance mutation (see below);
	// the marker lock alone serializes with activation.
	if _, err := s.ensureAndLockAccountingCutoverTx(ctx, tx); err != nil {
		return billing.Posting{}, err
	}
	// Provider COGS is not a customer balance mutation. In particular, do not
	// lock billing_accounts here: customer exposure admission must remain
	// independent while provider work is slow or backlogged.
	account, err := getAccountTx(ctx, tx, accountID)
	if err != nil {
		return billing.Posting{}, err
	}
	if account.Currency != result.Amount.Currency {
		return billing.Posting{}, billing.ErrMoneyCurrencyMismatch
	}
	operationKey, err := billing.ProviderCostSourceKey(leg.Key)
	if err != nil {
		return billing.Posting{}, err
	}
	fingerprint, err := result.SemanticFingerprint()
	if err != nil {
		return billing.Posting{}, err
	}
	lineageKey, err := providerCostLineageKey(callID, leg.BLegID)
	if err != nil {
		return billing.Posting{}, err
	}
	executionLineageKey, err := providerCostExecutionLineageKey(callID, leg.BLegID)
	if err != nil {
		return billing.Posting{}, err
	}
	// B2b2 posting-time ownership fence (provider_charge only). Bind canonical
	// sourceKey to exactly one owner/marker epoch via B1 pins before any
	// provider monetary effect. Pin + money commit atomically in this
	// transaction; exact retry returns the existing outcome with a completed
	// pin; conflicting replay fails.
	owner, err := billing.ResolveProviderCostOwner(input)
	if err != nil {
		return billing.Posting{}, err
	}
	if err := s.b2b2Fault("b2b2-enter"); err != nil {
		return billing.Posting{}, err
	}
	// F1: already holds the marker lock; re-lock keeps ordinary SELECT out.
	markerRow, markerFound, err := s.loadAccountingCutoverLocked(ctx, tx)
	if err != nil {
		return billing.Posting{}, err
	}
	var curState billing.AccountingCutoverState
	var curVersion, curEpoch uint64
	var curGeneration int
	if markerFound {
		marker, merr := accountingCutoverRowToMarker(markerRow)
		if merr != nil {
			return billing.Posting{}, merr
		}
		curState = marker.State
		curVersion = marker.Version
		curEpoch = marker.Epoch
		curGeneration = marker.Generation
	} else {
		curState = b2b2ProviderStateForMissingMarker()
		curVersion, curEpoch = 1, 1
		curGeneration = billing.AccountingCutoverGenerationV1
	}
	if input.Claim != nil {
		if input.Claim.OperationKey != operationKey {
			return billing.Posting{}, fmt.Errorf("%w: claim key %q != canonical %q", billing.ErrPostingOwnershipConflict, input.Claim.OperationKey, operationKey)
		}
		if input.Claim.Owner != owner {
			return billing.Posting{}, fmt.Errorf("%w: claim owner %q != posting owner %q", billing.ErrPostingOwnershipConflict, input.Claim.Owner, owner)
		}
	}
	pinRow, pinFound, err := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, operationKey)
	if err != nil {
		return billing.Posting{}, err
	}
	var pin billing.PostingPin
	if pinFound {
		pin, err = postingOwnershipRowToPin(pinRow)
		if err != nil {
			return billing.Posting{}, err
		}
		if pin.Kind != billing.PostingOperationProviderCharge || pin.OperationKey != operationKey || pin.AccountID != accountID || pin.CallID != callID {
			return billing.Posting{}, fmt.Errorf("%w: provider pin identity mismatch for %q", billing.ErrPostingOwnershipConflict, operationKey)
		}
		if pin.Owner != owner {
			return billing.Posting{}, fmt.Errorf("%w: provider pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, pin.Owner, owner)
		}
	}
	expectedTxID := ""
	if result.Amount.Nano > 0 {
		expectedTxID = operationKey
	}
	// Replay paths: existing money returns the durable outcome and ensures a
	// completed pin in the same transaction (backfill for pre-B2b2 money,
	// completion for crash-orphaned pins). Cross-path replays (revision-owned
	// execution) share the same V1 pin authority and return without touching
	// the pin outcome beyond ensuring it exists when owned by this claimant.
	ensureReplayPin := func() error {
		if err := s.b2b2Fault("b2b2-replay-pin"); err != nil {
			return err
		}
		if !pinFound {
			nowUnix := nowUnixNano()
			if err := b2b2BackfillProviderPinTx(ctx, tx, s, accountID, callID, leg, operationKey, owner, curVersion, curEpoch, curGeneration, curState, operationKey, expectedTxID, nowUnix); err != nil {
				return err
			}
			rrow, rfound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, operationKey)
			if rerr != nil {
				return rerr
			}
			if !rfound {
				return fmt.Errorf("%w: provider pin unavailable after backfill", billing.ErrPostingOwnershipNotFound)
			}
			repinned, perr := postingOwnershipRowToPin(rrow)
			if perr != nil {
				return perr
			}
			if repinned.Owner != owner {
				return fmt.Errorf("%w: provider pin owned by %q, claimant %q", billing.ErrPostingOwnershipConflict, repinned.Owner, owner)
			}
			if repinned.Status == billing.PostingPinPinned {
				pin = repinned
				pinFound = true
			} else {
				return nil
			}
		}
		if pinFound && pin.Status == billing.PostingPinCompleted {
			return nil
		}
		if pinFound && pin.Status == billing.PostingPinPinned {
			nowUnix := max(nowUnixNano(), pin.CreatedAtUnix)
			if err := b2b2CompleteProviderPinTx(ctx, tx, s.storeID, operationKey, operationKey, expectedTxID, nowUnix); err != nil {
				rrow, rfound, rerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, operationKey)
				if rerr != nil {
					return rerr
				}
				if !rfound {
					return fmt.Errorf("%w: provider pin missing after race", billing.ErrPostingOwnershipNotFound)
				}
				remarker, merr := postingOwnershipRowToPin(rrow)
				if merr != nil {
					return merr
				}
				if remarker.Status == billing.PostingPinCompleted && remarker.Owner == owner {
					return nil
				}
				return fmt.Errorf("%w: provider pin completion race for %q", billing.ErrPostingOwnershipConflict, operationKey)
			}
		}
		return nil
	}
	if executionFence, executionFound, lookupErr := s.loadProviderCostExecutionFence(ctx, tx, accountID, callID, executionLineageKey, true); lookupErr != nil {
		return billing.Posting{}, lookupErr
	} else if executionFound && executionFence.Authority == providerCostFenceAuthorityRevision {
		// A V2 revision owns the whole B-leg execution, including when its
		// current selected amount is unavailable. The legacy aggregate resolver
		// must not make a second monetary decision after that cutover.
		if err := ensureReplayPin(); err != nil {
			return billing.Posting{}, err
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost execution fence replay: %w", err)
		}
		return billing.Posting{OperationKey: executionFence.LastOperationKey, Replayed: true}, nil
	}
	if fence, found, lookupErr := s.loadProviderCostPostingFence(ctx, tx, accountID, callID, lineageKey, true); lookupErr != nil {
		return billing.Posting{}, lookupErr
	} else if found {
		if _, executionFound, executionErr := s.loadProviderCostExecutionFence(ctx, tx, accountID, callID, executionLineageKey, true); executionErr != nil {
			return billing.Posting{}, executionErr
		} else if !executionFound {
			if _, err := s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, accountID, callID, executionLineageKey, fence, string(metering.SubjectBLeg)); err != nil {
				return billing.Posting{}, err
			}
		}
		// A revision-authoritative fence supersedes any pending legacy LUR work.
		// The old worker may still be running after a restart, but it must not
		// create a second provider journal or roll a corrected head backward.
		if fence.Authority == providerCostFenceAuthorityRevision {
			if err := ensureReplayPin(); err != nil {
				return billing.Posting{}, err
			}
			if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
				return billing.Posting{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost fence replay: %w", err)
			}
			return billing.Posting{OperationKey: fence.LastOperationKey, Replayed: true}, nil
		}
		if fence.Fingerprint != fingerprint {
			return billing.Posting{}, ErrOperationConflict
		}
		if err := ensureReplayPin(); err != nil {
			return billing.Posting{}, err
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost replay: %w", err)
		}
		return billing.Posting{OperationKey: operationKey, Replayed: true}, nil
	}
	if childFence, found, lookupErr := s.loadProviderCostProviderChargeFence(ctx, tx, accountID, callID, executionLineageKey, true); lookupErr != nil {
		return billing.Posting{}, lookupErr
	} else if found {
		if childFence.Authority != providerCostFenceAuthorityRevision {
			return billing.Posting{}, fmt.Errorf("%w: provider charge fence authority", billing.ErrProviderCostRevisionFence)
		}
		if err := ensureReplayPin(); err != nil {
			return billing.Posting{}, err
		}
		if _, err := s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, accountID, callID, executionLineageKey, childFence, string(metering.SubjectProviderCharge)); err != nil {
			return billing.Posting{}, err
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, fmt.Errorf("billingstore: commit provider charge fence recovery: %w", err)
		}
		return billing.Posting{OperationKey: childFence.LastOperationKey, Replayed: true}, nil
	}
	// Upgrade/restart recovery: a legacy journal committed before the durable
	// fence migration is imported into the same fence before this attempt can
	// proceed. The import is in this transaction, so it cannot race a revision
	// writer that claims the same lineage.
	if recovered, found, recoveryErr := s.recoverLegacyProviderCostFence(ctx, tx, accountID, callID, lineageKey, account.Currency); recoveryErr != nil {
		return billing.Posting{}, recoveryErr
	} else if found {
		if recovered.Fingerprint != fingerprint {
			return billing.Posting{}, ErrOperationConflict
		}
		if err := s.insertProviderCostPostingFenceInTx(ctx, tx, accountID, callID, lineageKey,
			recovered.Authority, recovered.HeadKey, recovered.EvidenceRevision, recovered.InputSetHash,
			recovered.Fingerprint, providerCostFenceAmount(recovered), recovered.OriginalTransactionID, recovered.LastOperationKey, recovered.LastTransactionID); err != nil {
			return billing.Posting{}, err
		}
		if _, executionFound, executionErr := s.loadProviderCostExecutionFence(ctx, tx, accountID, callID, executionLineageKey, true); executionErr != nil {
			return billing.Posting{}, executionErr
		} else if !executionFound {
			if _, err := s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, accountID, callID, executionLineageKey, recovered, string(metering.SubjectBLeg)); err != nil {
				return billing.Posting{}, err
			}
		}
		if err := ensureReplayPin(); err != nil {
			return billing.Posting{}, err
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost legacy recovery: %w", err)
		}
		return billing.Posting{OperationKey: operationKey, Replayed: true}, nil
	}
	if existing, found, lookupErr := loadOperationSnapshot(ctx, tx, accountID, "provider_call_cogs", leg.Key); lookupErr != nil {
		return billing.Posting{}, lookupErr
	} else if found {
		if existing.Fingerprint != fingerprint {
			return billing.Posting{}, ErrOperationConflict
		}
		if err := ensureReplayPin(); err != nil {
			return billing.Posting{}, err
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return billing.Posting{}, err
		}
		if err := tx.Commit(); err != nil {
			return billing.Posting{}, err
		}
		return billing.Posting{OperationKey: operationKey, Replayed: true}, nil
	}
	// New posting (no snapshot): enforce one-owner fence before any
	// journal/fence/work mutation. Pin owner is stable; token carries
	// current-marker authority (F6). Only token==current is required.
	if input.Claim != nil {
		// Stale worker (lease waking after epoch change) fails closed before money.
		if input.Claim.MarkerVersion != curVersion || input.Claim.MarkerEpoch != curEpoch || input.Claim.MarkerState != curState {
			return billing.Posting{}, fmt.Errorf("%w: provider claim %d/%d/%q != current %d/%d/%q",
				billing.ErrPostingOwnershipFence, input.Claim.MarkerVersion, input.Claim.MarkerEpoch, string(input.Claim.MarkerState), curVersion, curEpoch, string(curState))
		}
	}
	if !pinFound {
		if !billing.IsPostingOwnerAllowedForNew(curState, owner) {
			return billing.Posting{}, fmt.Errorf("%w: provider owner %q not allowed for new in %q", billing.ErrPostingOwnershipFence, owner, string(curState))
		}
		if input.Claim != nil {
			// New work with a claim but no pin is unpinned (never classified).
			return billing.Posting{}, fmt.Errorf("%w: provider claim without classified pin for %q", billing.ErrPostingOwnershipFence, operationKey)
		}
		if curState == billing.AccountingCutoverV1Draining {
			return billing.Posting{}, fmt.Errorf("%w: draining forbids new provider posting for %q", billing.ErrPostingOwnershipFence, operationKey)
		}
		if owner == billing.PostingOwnerV2 && !billing.IsV2NewWorkAuthorized(curState) {
			return billing.Posting{}, fmt.Errorf("%w: V2 provider posting requires v2_active", billing.ErrCutoverV2NotAuthorized)
		}
		if err := s.b2b2Fault("b2b2-pin-acquire"); err != nil {
			return billing.Posting{}, err
		}
		nowUnix := nowUnixNano()
		subjectJSON, _ := legSubjectJSONForPin(s.storeID, accountID, callID, leg)
		acquired, err := b2b2InsertProviderPinTx(ctx, tx, s, accountID, callID, s.storeID, leg.BLegID, "", string(metering.SubjectBLeg), subjectJSON, operationKey, owner, curVersion, curEpoch, curGeneration, curState, nowUnix)
		if err != nil {
			return billing.Posting{}, err
		}
		pin = acquired
		pinFound = true
		if !billing.IsPostingPinReplay(pin, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationProviderCharge, AccountID: accountID, CallID: callID, Subject: pin.Subject, Owner: owner, ExpectedMarkerVersion: curVersion, ExpectedMarkerEpoch: curEpoch}, operationKey) {
			// The pin subject carries the canonical B-leg lineage; a mismatch
			// here is a cross-lineage collision, not a replay.
			if pin.BLegID != leg.BLegID {
				return billing.Posting{}, fmt.Errorf("%w: provider pin lineage mismatch", billing.ErrPostingOwnershipConflict)
			}
		}
	} else {
		// Pinned path: only correctly pinned work may complete. Draining
		// requires the worker's matching claim metadata (no optional bypass);
		// without it the posting fences even though a pin exists.
		if curState == billing.AccountingCutoverV1Draining && input.Claim == nil {
			return billing.Posting{}, fmt.Errorf("%w: draining provider posting requires claim metadata for %q", billing.ErrPostingOwnershipFence, operationKey)
		}
		if !billing.IsPostingPinAcquireReplayAllowed(curState, pin) {
			return billing.Posting{}, fmt.Errorf("%w: provider pin replay not allowed in %q", billing.ErrPostingOwnershipFence, string(curState))
		}
		if !billing.IsPostingPinCompleteAllowed(curState, pin) {
			return billing.Posting{}, fmt.Errorf("%w: provider pin completion not allowed in %q", billing.ErrPostingOwnershipFence, string(curState))
		}
		if pin.Status == billing.PostingPinCompleted {
			// Completed without snapshot (manual completion or cross-path
			// completion): never invent money.
			return billing.Posting{}, fmt.Errorf("%w: provider pin already completed for %q", billing.ErrPostingOwnershipConflict, operationKey)
		}
	}
	if err := s.b2b2Fault("b2b2-before-effects"); err != nil {
		return billing.Posting{}, err
	}
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.Posting{}, err
	}
	posting := billing.Posting{OperationKey: operationKey, Before: before, After: before}
	if result.Amount.Nano > 0 {
		if err := s.b2b2Fault("b2b2-before-journal"); err != nil {
			return billing.Posting{}, err
		}
		journal := billing.JournalTransaction{
			ID: operationKey, Book: billing.JournalBookFinancial, Currency: result.Amount.Currency,
			SourceKey: operationKey, AccountID: accountID, TurnID: callID.String(),
			ALegID: leg.ALegID, BLegID: leg.BLegID, OperationKind: "provider_call_cogs",
			CorrectionGroupID: leg.Key,
			BalanceBefore:     before.BalanceNano, BalanceAfter: before.BalanceNano,
			SpendableBefore: before.SpendableNano, SpendableAfter: before.SpendableNano,
			CreditFloor: before.CreditFloorNano, CreditLimit: before.CreditLimitNano,
			Mode: string(before.Mode), SnapshotVersionBefore: before.Version, SnapshotVersionAfter: before.Version,
			Entries: []billing.JournalEntry{
				{LedgerAccount: "inference_provider_cogs", Side: billing.JournalDebit, Amount: result.Amount},
				{LedgerAccount: "provider_payable_clearing", Side: billing.JournalCredit, Amount: result.Amount},
			},
		}
		posted, replayed, postErr := s.postJournalInTx(ctx, tx, journal)
		if postErr != nil {
			return billing.Posting{}, postErr
		}
		if replayed {
			return billing.Posting{}, ErrOperationConflict
		}
		posting.Transaction = posted
	}
	if err := insertOperationSnapshot(ctx, tx, operationSnapshotInput{
		OperationKey: operationKey, AccountID: accountID, OperationKind: "provider_call_cogs",
		SourceKey: leg.Key, Fingerprint: fingerprint, Before: before, After: before,
		SequenceStart: posting.Transaction.AccountSequence, SequenceEnd: posting.Transaction.AccountSequence,
	}); err != nil {
		return billing.Posting{}, err
	}
	if _, executionFound, executionErr := s.loadProviderCostExecutionFence(ctx, tx, accountID, callID, executionLineageKey, true); executionErr != nil {
		return billing.Posting{}, executionErr
	} else if !executionFound {
		if err := s.insertProviderCostExecutionFenceInTx(ctx, tx, accountID, callID, executionLineageKey,
			providerCostFenceAuthorityLegacy, string(metering.SubjectBLeg), leg.Key, 1,
			legacyProviderCostFenceInputHash(lineageKey), fingerprint, operationKey, posting.Transaction.ID); err != nil {
			return billing.Posting{}, err
		}
	}
	if err := s.insertProviderCostPostingFenceInTx(ctx, tx, accountID, callID, lineageKey,
		providerCostFenceAuthorityLegacy, leg.Key, 1, legacyProviderCostFenceInputHash(lineageKey),
		fingerprint, result.Amount, posting.Transaction.ID, operationKey, posting.Transaction.ID); err != nil {
		return billing.Posting{}, err
	}
	if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
		return billing.Posting{}, err
	}
	// B2b2 atomic completion: pin + journal/fences/snapshot/work commit in the
	// same transaction. No window where money is posted but the pin remains
	// reusable.
	if err := s.b2b2Fault("b2b2-before-pin-complete"); err != nil {
		return billing.Posting{}, err
	}
	pinCompletionTxID := posting.Transaction.ID
	pinNow := nowUnixNano()
	if pinFound && pinNow < pin.CreatedAtUnix {
		pinNow = pin.CreatedAtUnix
	}
	if err := b2b2CompleteProviderPinTx(ctx, tx, s.storeID, operationKey, operationKey, pinCompletionTxID, pinNow); err != nil {
		return billing.Posting{}, err
	}
	if err := s.b2b2Fault("b2b2-before-commit"); err != nil {
		return billing.Posting{}, err
	}
	if err := tx.Commit(); err != nil {
		return billing.Posting{}, fmt.Errorf("billingstore: commit provider cost: %w", err)
	}
	return posting, nil
}
