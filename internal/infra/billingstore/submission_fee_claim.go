package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

type submissionFeeClaimRow struct {
	ClaimKey           string `bun:"claim_key"`
	StoreID            string `bun:"store_id"`
	AccountID          string `bun:"account_id"`
	SubmissionID       string `bun:"submission_id"`
	SourceCallID       string `bun:"source_call_id"`
	TariffID           string `bun:"tariff_id"`
	TariffVersion      string `bun:"tariff_version"`
	TariffContentRef   string `bun:"tariff_content_ref"`
	TariffContentHash  string `bun:"tariff_content_hash"`
	PolicyID           string `bun:"policy_id"`
	PolicyVersion      string `bun:"policy_version"`
	PolicyContentRef   string `bun:"policy_content_ref"`
	PolicyContentHash  string `bun:"policy_content_hash"`
	ContextFingerprint string `bun:"context_fingerprint"`
	AmountNano         int64  `bun:"amount_nano"`
	Currency           string `bun:"currency"`
}

func loadSubmissionFeeClaim(ctx context.Context, tx bun.Tx, storeID, accountID, submissionID string) (submissionFeeClaimRow, bool, error) {
	var row submissionFeeClaimRow
	err := tx.NewRaw(`SELECT claim_key, store_id, account_id, submission_id, source_call_id, tariff_id, tariff_version, tariff_content_ref, tariff_content_hash, policy_id, policy_version, policy_content_ref, policy_content_hash, context_fingerprint, amount_nano, currency FROM billing_submission_fee_claims WHERE store_id = ? AND account_id = ? AND submission_id = ?`, storeID, accountID, submissionID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return submissionFeeClaimRow{}, false, nil
	}
	if err != nil {
		return submissionFeeClaimRow{}, false, fmt.Errorf("billingstore: load submission fee claim: %w", err)
	}
	return row, true, nil
}

func submissionFeeClaimMatches(row submissionFeeClaimRow, claim billing.SubmissionFeeClaim) bool {
	return row.SubmissionID == claim.SubmissionID &&
		row.TariffID == claim.TariffID && row.TariffVersion == claim.TariffVersion &&
		row.TariffContentRef == claim.TariffContentRef && row.TariffContentHash == claim.TariffContentHash &&
		row.PolicyID == claim.PolicyID && row.PolicyVersion == claim.PolicyVersion &&
		row.PolicyContentRef == claim.PolicyContentRef && row.PolicyContentHash == claim.PolicyContentHash &&
		row.ContextFingerprint == claim.ContextFingerprint && row.AmountNano == claim.Amount.Nano && row.Currency == claim.Amount.Currency
}

// submissionFeeClaimBelongsToCall reports whether the call that created the
// immutable claim is the call being settled. The owner call keeps its
// submission fee in the original settlement; sibling calls deduct the already
// claimed fee from their own customer charge.
func submissionFeeClaimBelongsToCall(row submissionFeeClaimRow, callID string) bool {
	return row.SourceCallID == callID
}

func insertSubmissionFeeClaim(ctx context.Context, tx bun.Tx, storeID, accountID, sourceCallID string, claim billing.SubmissionFeeClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	claimKey := billing.ScopedOperationKey("customer_submission_fee_claim", storeID, accountID+"\x00"+claim.SubmissionID)
	_, err := tx.NewRaw(`INSERT INTO billing_submission_fee_claims (claim_key, store_id, account_id, submission_id, source_call_id, tariff_id, tariff_version, tariff_content_ref, tariff_content_hash, policy_id, policy_version, policy_content_ref, policy_content_hash, context_fingerprint, amount_nano, currency, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`, claimKey, storeID, accountID, claim.SubmissionID, sourceCallID, claim.TariffID, claim.TariffVersion, claim.TariffContentRef, claim.TariffContentHash, claim.PolicyID, claim.PolicyVersion, claim.PolicyContentRef, claim.PolicyContentHash, claim.ContextFingerprint, claim.Amount.Nano, claim.Amount.Currency).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: insert submission fee claim: %w", err)
	}
	return nil
}
