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
}

const providerCostHeadSelect = `SELECT id, store_id, account_id, call_id, head_key, subject_kind, subject_id, subject_json, evidence_revision, input_set_hash, valuation_id, amount_nano, currency, head_version, fence, last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix FROM billing_provider_cost_heads`

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
	before, err := snapshotForAccount(account)
	if err != nil {
		return billing.ProviderCostRevisionResult{}, err
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
	fenceSeeded := false
	executionFenceInserted := false
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
		fenceSeeded = true
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
			executionFenceInserted = true
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
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost execution fence exclusion: %w", err)
		}
		return ignoredProviderCostRevision(input), nil
	}
	if input.Cost.Completeness == billing.CostCompletenessPartial && executionFound && executionFence.Authority == providerCostFenceAuthorityLegacy {
		// Legacy already owns this execution. Partial/unavailable evidence has no
		// amount to reconcile and must not turn the legacy aggregate into a zero
		// reversal; the existing legacy authority remains the durable fence.
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
		if err := tx.Commit(); err != nil {
			return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost exclusion replay: %w", err)
		}
		return ignoredProviderCostRevision(input), nil
	}
	if fenceFound && fence.Authority == providerCostFenceAuthorityLegacy {
		return s.applyProviderCostRevisionAfterLegacyFence(ctx, tx, input, before, sourceKey, fingerprint, operationKey, lineageKey, fence, fenceNeedsInsert, head, found, amount)
	}
	if fenceFound && fence.Authority == providerCostFenceAuthorityRevision && !found {
		// A known non-payable revision intentionally has a fence but no provider
		// head. It must suppress a stale legacy worker; a later payable revision
		// may still promote the zero fence into a real selected-cost head.
		if input.EvidenceRevision < uint64(fence.EvidenceRevision) {
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost exclusion replay: %w", err)
			}
			return providerCostRevisionStale(input, providerCostFenceAmount(fence)), nil
		}
		if input.EvidenceRevision == uint64(fence.EvidenceRevision) {
			if fence.InputSetHash != input.InputSetHash || fence.Fingerprint != fingerprint || providerCostFenceAmount(fence) != amount {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("%w: provider cost exclusion %s revision=%d", billing.ErrProviderCostRevisionConflict, lineageKey, input.EvidenceRevision)
			}
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost exclusion replay: %w", err)
			}
			return ignoredProviderCostRevision(input), nil
		}
		return s.applyProviderCostRevisionAfterLegacyFence(ctx, tx, input, before, sourceKey, fingerprint, operationKey, lineageKey, fence, false, head, found, amount)
	}
	if !found && !fenceFound && !input.Cost.Payable {
		// A known non-operator payer is an exclusion, not a zero-valued head.
		// Once a payable head exists, the same exclusion is handled below as an
		// authoritative zero target so a payer correction reverses prior COGS.
		if err := s.insertProviderCostPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, lineageKey,
			providerCostFenceAuthorityRevision, input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, amount, "", operationKey, ""); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
		if err := s.economicFault("after_provider_cost_revision"); err != nil {
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
			if fenceSeeded {
				if err := tx.Commit(); err != nil {
					return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit seeded provider cost fence: %w", err)
				}
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
			// truncated snapshot safe as well.
			if existing, exists, lookupErr := loadOperationSnapshot(ctx, tx, input.AccountID, "provider_call_cogs", sourceKey); lookupErr != nil {
				return billing.ProviderCostRevisionResult{}, lookupErr
			} else if exists {
				if existing.Fingerprint != fingerprint {
					return billing.ProviderCostRevisionResult{}, ErrOperationConflict
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
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit provider cost replay repair: %w", err)
			}
			return providerCostRevisionReplayed(input, previous, operationKey), nil
		}
		existingIdentity, identityErr := billing.NewEconomicRevisionIdentity(billing.EconomicQueueProvider, head.HeadKey, uint64(head.EvidenceRevision), head.InputSetHash)
		incomingIdentity, incomingErr := billing.NewEconomicRevisionIdentity(billing.EconomicQueueProvider, input.HeadKey, input.EvidenceRevision, input.InputSetHash)
		if identityErr != nil || incomingErr != nil || !existingIdentity.Less(incomingIdentity) {
			// A same-revision candidate with a lower input hash is an older
			// deterministic tie-breaker, not a conflict. The valid same-identity
			// conflict path above remains the only way to reject a changed replay.
			return providerCostRevisionStale(input, previous), nil
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
		if executionFenceInserted {
			if err := s.advanceProviderCostExecutionFenceInTx(ctx, tx, executionFence, input.AccountID, input.CallID, executionLineageKey,
				providerCostFenceAuthorityRevision, string(input.Subject.Kind), input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
				fingerprint, operationKey, transactionID); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
		}
		if err := s.economicFault("after_provider_cost_revision"); err != nil {
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
	if executionFenceInserted {
		if err := s.advanceProviderCostExecutionFenceInTx(ctx, tx, executionFence, input.AccountID, input.CallID, executionLineageKey,
			providerCostFenceAuthorityRevision, string(input.Subject.Kind), input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, operationKey, posting.Transaction.ID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	}
	if err := s.economicFault("after_provider_cost_revision"); err != nil {
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
// the legacy LUR writer to the revision writer. The legacy amount is the
// durable prior head; a matching first revision only adopts the new identity,
// while a later revision posts the exact signed delta from that amount.
func (s *DurableStore) applyProviderCostRevisionAfterLegacyFence(ctx context.Context, tx bun.Tx, input billing.ProviderCostRevisionInput, before billing.AccountSnapshot, sourceKey, fingerprint, operationKey, lineageKey string, fence providerCostPostingFenceRow, fenceNeedsInsert bool, head providerCostHeadRow, headFound bool, amount billing.Money) (billing.ProviderCostRevisionResult, error) {
	previous := providerCostFenceAmount(fence)
	if input.EvidenceRevision < uint64(fence.EvidenceRevision) {
		if fenceNeedsInsert {
			if err := s.insertProviderCostPostingFenceInTx(ctx, tx, input.AccountID, input.CallID, lineageKey,
				fence.Authority, fence.HeadKey, fence.EvidenceRevision, fence.InputSetHash, fence.Fingerprint,
				previous, fence.OriginalTransactionID, fence.LastOperationKey, fence.LastTransactionID); err != nil {
				return billing.ProviderCostRevisionResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return billing.ProviderCostRevisionResult{}, fmt.Errorf("billingstore: commit recovered provider cost fence: %w", err)
			}
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
	} else if executionFound && executionFence.Authority == providerCostFenceAuthorityLegacy {
		if err := s.advanceProviderCostExecutionFenceInTx(ctx, tx, executionFence, input.AccountID, input.CallID, executionLineageKey,
			providerCostFenceAuthorityRevision, string(input.Subject.Kind), input.HeadKey, int64(input.EvidenceRevision), input.InputSetHash,
			fingerprint, operationKey, transactionID); err != nil {
			return billing.ProviderCostRevisionResult{}, err
		}
	}
	if err := s.economicFault("after_provider_cost_revision"); err != nil {
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
	now := time.Now().UTC().UnixNano()
	if _, err := tx.NewRaw(`INSERT INTO billing_provider_cost_heads(
			store_id, account_id, call_id, head_key, subject_kind, subject_id, subject_json,
			evidence_revision, input_set_hash, valuation_id, amount_nano, currency, head_version, fence,
			last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.storeID, input.AccountID, input.CallID.String(), input.HeadKey, string(input.Subject.Kind), subjectIDForEconomics(input.Subject), string(subjectJSON),
		input.EvidenceRevision, input.InputSetHash, input.ValuationID, amount.Nano, amount.Currency, 1, 1, operationKey, originalTransactionID, transactionID, now, now).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert provider cost head: %w", err)
	}
	return nil
}

func (s *DurableStore) advanceProviderCostHeadInTx(ctx context.Context, tx bun.Tx, existing providerCostHeadRow, input billing.ProviderCostRevisionInput, amount billing.Money, operationKey, transactionID string) error {
	subjectJSON, err := json.Marshal(input.Subject)
	if err != nil {
		return fmt.Errorf("billingstore: encode provider cost correction subject: %w", err)
	}
	if existing.HeadVersion >= math.MaxInt64 || existing.Fence >= math.MaxInt64 {
		return fmt.Errorf("%w: provider cost head version overflow", billing.ErrProviderCostRevisionInvalid)
	}
	result, err := tx.NewRaw(`UPDATE billing_provider_cost_heads SET
			subject_kind = ?, subject_id = ?, subject_json = ?, evidence_revision = ?, input_set_hash = ?, valuation_id = ?,
			amount_nano = ?, currency = ?, head_version = head_version + 1, fence = fence + 1,
			last_operation_key = ?, original_transaction_id = CASE WHEN original_transaction_id <> '' THEN original_transaction_id WHEN last_transaction_id <> '' THEN last_transaction_id ELSE ? END,
			last_transaction_id = ?, updated_at_unix = ?
			WHERE id = ? AND head_version = ? AND fence = ?`,
		string(input.Subject.Kind), subjectIDForEconomics(input.Subject), string(subjectJSON), input.EvidenceRevision, input.InputSetHash, input.ValuationID,
		amount.Nano, amount.Currency, operationKey, transactionID, transactionID, time.Now().UTC().UnixNano(), existing.ID, existing.HeadVersion, existing.Fence).Exec(ctx)
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
