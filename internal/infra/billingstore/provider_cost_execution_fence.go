package billingstore

import (
	"context"
	"database/sql"
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

// providerCostExecutionFenceRow is deliberately amount-free. It is the
// durable writer gate shared by the legacy aggregate B-leg lineage and every
// V2 provider-charge child lineage. Monetary deltas remain owned by the
// existing per-head posting fences and heads.
type providerCostExecutionFenceRow struct {
	ID                int64  `bun:"id"`
	StoreID           string `bun:"store_id"`
	AccountID         string `bun:"account_id"`
	CallID            string `bun:"call_id"`
	ExecutionLineage  string `bun:"execution_lineage_key"`
	Authority         string `bun:"authority"`
	OwnerSubjectKind  string `bun:"owner_subject_kind"`
	OwnerHeadKey      string `bun:"owner_head_key"`
	OwnerRevision     int64  `bun:"owner_revision"`
	OwnerInputSetHash string `bun:"owner_input_set_hash"`
	OwnerFingerprint  string `bun:"owner_fingerprint"`
	Fence             int64  `bun:"fence"`
	LastOperationKey  string `bun:"last_operation_key"`
	LastTransactionID string `bun:"last_transaction_id"`
	CreatedAt         int64  `bun:"created_at_unix"`
	UpdatedAt         int64  `bun:"updated_at_unix"`
}

const providerCostExecutionFenceSelect = `SELECT id, store_id, account_id, call_id, execution_lineage_key, authority, owner_subject_kind, owner_head_key, owner_revision, owner_input_set_hash, owner_fingerprint, fence, last_operation_key, last_transaction_id, created_at_unix, updated_at_unix FROM billing_provider_cost_execution_fences`

func providerCostExecutionLineageKey(callID billing.BillingCallID, bLegID string) (string, error) {
	return providerCostLineageKey(callID, bLegID)
}

func (s *DurableStore) loadProviderCostExecutionFence(ctx context.Context, q bun.IDB, accountID string, callID billing.BillingCallID, lineageKey string, forUpdate bool) (providerCostExecutionFenceRow, bool, error) {
	query := providerCostExecutionFenceSelect + ` WHERE store_id = ? AND account_id = ? AND call_id = ? AND execution_lineage_key = ? LIMIT 1`
	if forUpdate && q.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var row providerCostExecutionFenceRow
	err := q.NewRaw(query, s.storeID, accountID, callID.String(), lineageKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return providerCostExecutionFenceRow{}, false, nil
	}
	if err != nil {
		return providerCostExecutionFenceRow{}, false, fmt.Errorf("billingstore: load provider cost execution fence: %w", err)
	}
	if err := validateProviderCostExecutionFence(row); err != nil {
		return providerCostExecutionFenceRow{}, false, err
	}
	return row, true, nil
}

func validateProviderCostExecutionFence(row providerCostExecutionFenceRow) error {
	if err := validateProviderCostFenceAuthority(row.Authority); err != nil {
		return err
	}
	if row.OwnerRevision <= 0 || row.Fence <= 0 || strings.TrimSpace(row.ExecutionLineage) == "" ||
		strings.TrimSpace(row.OwnerSubjectKind) == "" || strings.TrimSpace(row.OwnerHeadKey) == "" ||
		strings.TrimSpace(row.OwnerInputSetHash) == "" || strings.TrimSpace(row.OwnerFingerprint) == "" ||
		strings.TrimSpace(row.LastOperationKey) == "" {
		return fmt.Errorf("billingstore: invalid provider cost execution fence %s", row.ExecutionLineage)
	}
	return nil
}

func validateProviderCostExecutionOwner(authority, subjectKind, headKey, inputSetHash, fingerprint string, revision int64) error {
	if err := validateProviderCostFenceAuthority(authority); err != nil {
		return err
	}
	if strings.TrimSpace(subjectKind) == "" || strings.TrimSpace(headKey) == "" || strings.TrimSpace(inputSetHash) == "" || strings.TrimSpace(fingerprint) == "" || revision <= 0 {
		return fmt.Errorf("billingstore: incomplete provider cost execution fence owner")
	}
	if subjectKind != string(metering.SubjectBLeg) && subjectKind != string(metering.SubjectProviderCharge) {
		return fmt.Errorf("billingstore: invalid provider cost execution fence subject kind %q", subjectKind)
	}
	return nil
}

func (s *DurableStore) insertProviderCostExecutionFenceInTx(ctx context.Context, tx bun.Tx, accountID string, callID billing.BillingCallID, lineageKey, authority, subjectKind, headKey string, revision int64, inputSetHash, fingerprint, operationKey, transactionID string) error {
	if err := validateProviderCostExecutionOwner(authority, subjectKind, headKey, inputSetHash, fingerprint, revision); err != nil {
		return err
	}
	if strings.TrimSpace(lineageKey) == "" || strings.TrimSpace(operationKey) == "" {
		return fmt.Errorf("billingstore: incomplete provider cost execution fence")
	}
	now := time.Now().UTC().UnixNano()
	if _, err := tx.NewRaw(`INSERT INTO billing_provider_cost_execution_fences(
		store_id, account_id, call_id, execution_lineage_key, authority, owner_subject_kind,
		owner_head_key, owner_revision, owner_input_set_hash, owner_fingerprint, fence,
		last_operation_key, last_transaction_id, created_at_unix, updated_at_unix
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.storeID, accountID, callID.String(), lineageKey, authority, subjectKind, headKey,
		revision, inputSetHash, fingerprint, 1, operationKey, transactionID, now, now).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert provider cost execution fence: %w", err)
	}
	return nil
}

func (s *DurableStore) advanceProviderCostExecutionFenceInTx(ctx context.Context, tx bun.Tx, existing providerCostExecutionFenceRow, accountID string, callID billing.BillingCallID, lineageKey, authority, subjectKind, headKey string, revision int64, inputSetHash, fingerprint, operationKey, transactionID string) error {
	if err := validateProviderCostExecutionOwner(authority, subjectKind, headKey, inputSetHash, fingerprint, revision); err != nil {
		return err
	}
	if existing.ID <= 0 || existing.Fence <= 0 || existing.Fence == math.MaxInt64 || existing.ExecutionLineage != lineageKey {
		return fmt.Errorf("billingstore: invalid provider cost execution fence transition")
	}
	result, err := tx.NewRaw(`UPDATE billing_provider_cost_execution_fences SET
		authority = ?, owner_subject_kind = ?, owner_head_key = ?, owner_revision = ?,
		owner_input_set_hash = ?, owner_fingerprint = ?, fence = fence + 1,
		last_operation_key = ?, last_transaction_id = ?, updated_at_unix = ?
		WHERE id = ? AND store_id = ? AND account_id = ? AND call_id = ? AND execution_lineage_key = ? AND fence = ?`,
		authority, subjectKind, headKey, revision, inputSetHash, fingerprint, operationKey,
		transactionID, time.Now().UTC().UnixNano(), existing.ID, s.storeID, accountID,
		callID.String(), lineageKey, existing.Fence).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: advance provider cost execution fence: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("billingstore: provider cost execution fence rows affected: %w", affectedErr)
	} else if affected != 1 {
		return fmt.Errorf("%w: provider cost execution fence %s", billing.ErrProviderCostRevisionFence, lineageKey)
	}
	return nil
}

func providerCostExecutionFenceOwnerFromPostingFence(fence providerCostPostingFenceRow, subjectKind string) (string, string, int64, string, string, string, error) {
	if err := validateProviderCostFenceAuthority(fence.Authority); err != nil {
		return "", "", 0, "", "", "", err
	}
	if subjectKind == "" {
		subjectKind = string(metering.SubjectBLeg)
	}
	return fence.Authority, subjectKind, fence.EvidenceRevision, fence.InputSetHash, fence.Fingerprint, fence.LastOperationKey, nil
}

func (s *DurableStore) seedProviderCostExecutionFenceFromPostingFenceInTx(ctx context.Context, tx bun.Tx, accountID string, callID billing.BillingCallID, executionLineage string, postingFence providerCostPostingFenceRow, subjectKind string) (providerCostExecutionFenceRow, error) {
	authority, ownerKind, revision, inputHash, fingerprint, operationKey, err := providerCostExecutionFenceOwnerFromPostingFence(postingFence, subjectKind)
	if err != nil {
		return providerCostExecutionFenceRow{}, err
	}
	if err := s.insertProviderCostExecutionFenceInTx(ctx, tx, accountID, callID, executionLineage, authority, ownerKind, postingFence.HeadKey, revision, inputHash, fingerprint, operationKey, postingFence.LastTransactionID); err != nil {
		return providerCostExecutionFenceRow{}, err
	}
	return providerCostExecutionFenceRow{
		StoreID: s.storeID, AccountID: accountID, CallID: callID.String(), ExecutionLineage: executionLineage,
		Authority: authority, OwnerSubjectKind: ownerKind, OwnerHeadKey: postingFence.HeadKey,
		OwnerRevision: revision, OwnerInputSetHash: inputHash, OwnerFingerprint: fingerprint,
		Fence: 1, LastOperationKey: operationKey, LastTransactionID: postingFence.LastTransactionID,
	}, nil
}

func (s *DurableStore) seedProviderCostExecutionFenceFromHeadInTx(ctx context.Context, tx bun.Tx, input billing.ProviderCostRevisionInput, executionLineage string, head providerCostHeadRow) (providerCostExecutionFenceRow, error) {
	fingerprint := "provider-cost-head:v1:" + head.InputSetHash
	if err := s.insertProviderCostExecutionFenceInTx(ctx, tx, input.AccountID, input.CallID, executionLineage,
		providerCostFenceAuthorityRevision, string(input.Subject.Kind), head.HeadKey, head.EvidenceRevision,
		head.InputSetHash, fingerprint, head.LastOperationKey, head.LastTransactionID); err != nil {
		return providerCostExecutionFenceRow{}, err
	}
	return providerCostExecutionFenceRow{
		StoreID: s.storeID, AccountID: input.AccountID, CallID: input.CallID.String(), ExecutionLineage: executionLineage,
		Authority: providerCostFenceAuthorityRevision, OwnerSubjectKind: string(input.Subject.Kind), OwnerHeadKey: head.HeadKey,
		OwnerRevision: head.EvidenceRevision, OwnerInputSetHash: head.InputSetHash, OwnerFingerprint: fingerprint,
		Fence: 1, LastOperationKey: head.LastOperationKey, LastTransactionID: head.LastTransactionID,
	}, nil
}
