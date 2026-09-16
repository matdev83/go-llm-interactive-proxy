package billingstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const (
	providerCostFenceAuthorityLegacy   = "legacy"
	providerCostFenceAuthorityRevision = "revision"
)

type providerCostPostingFenceRow struct {
	ID                    int64  `bun:"id"`
	StoreID               string `bun:"store_id"`
	AccountID             string `bun:"account_id"`
	CallID                string `bun:"call_id"`
	LineageKey            string `bun:"lineage_key"`
	Authority             string `bun:"authority"`
	HeadKey               string `bun:"head_key"`
	EvidenceRevision      int64  `bun:"evidence_revision"`
	InputSetHash          string `bun:"input_set_hash"`
	Fingerprint           string `bun:"fingerprint"`
	AmountNano            int64  `bun:"amount_nano"`
	Currency              string `bun:"currency"`
	Fence                 int64  `bun:"fence"`
	LastOperationKey      string `bun:"last_operation_key"`
	OriginalTransactionID string `bun:"original_transaction_id"`
	LastTransactionID     string `bun:"last_transaction_id"`
	CreatedAt             int64  `bun:"created_at_unix"`
	UpdatedAt             int64  `bun:"updated_at_unix"`
}

const providerCostPostingFenceSelect = `SELECT id, store_id, account_id, call_id, lineage_key, authority, head_key, evidence_revision, input_set_hash, fingerprint, amount_nano, currency, fence, last_operation_key, original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix FROM billing_provider_cost_posting_fences`

func providerCostLineageKey(callID billing.BillingCallID, bLegID string) (string, error) {
	key, err := billing.CallLegUsageKey(callID, bLegID)
	if err != nil {
		return "", fmt.Errorf("billingstore: provider cost lineage: %w", err)
	}
	return key, nil
}

func providerCostRevisionLineageKey(input billing.ProviderCostRevisionInput) (string, error) {
	if input.Subject.Kind == metering.SubjectProviderCharge && strings.TrimSpace(input.Subject.ProviderChargeID) != "" {
		legKey, err := providerCostLineageKey(input.CallID, input.Subject.BLegID)
		if err != nil {
			return "", err
		}
		return legKey + ":provider-charge:" + strings.TrimSpace(input.Subject.ProviderChargeID), nil
	}
	return providerCostLineageKey(input.CallID, input.Subject.BLegID)
}

func legacyProviderCostFenceInputHash(lineageKey string) string {
	digest := sha256.Sum256([]byte("legacy-provider-cost-fence:v1:" + lineageKey))
	return hex.EncodeToString(digest[:])
}

func (s *DurableStore) loadProviderCostPostingFence(ctx context.Context, q bun.IDB, accountID string, callID billing.BillingCallID, lineageKey string, forUpdate bool) (providerCostPostingFenceRow, bool, error) {
	query := providerCostPostingFenceSelect + ` WHERE store_id = ? AND account_id = ? AND call_id = ? AND lineage_key = ? LIMIT 1`
	if forUpdate && q.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var row providerCostPostingFenceRow
	err := q.NewRaw(query, s.storeID, accountID, callID.String(), lineageKey).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return providerCostPostingFenceRow{}, false, nil
	}
	if err != nil {
		return providerCostPostingFenceRow{}, false, fmt.Errorf("billingstore: load provider cost posting fence: %w", err)
	}
	if row.EvidenceRevision <= 0 || row.Fence <= 0 || row.AmountNano < 0 {
		return providerCostPostingFenceRow{}, false, fmt.Errorf("billingstore: invalid provider cost posting fence %s", lineageKey)
	}
	return row, true, nil
}

// loadProviderCostProviderChargeFence finds a previously committed V2 child
// fence for one B-leg. It is used only for upgrade/restart recovery when the
// execution-level fence has not yet been materialized. The prefix comparison
// deliberately uses substr rather than LIKE so opaque B-leg identifiers that
// contain SQL wildcard characters cannot widen the recovery scope.
func (s *DurableStore) loadProviderCostProviderChargeFence(ctx context.Context, q bun.IDB, accountID string, callID billing.BillingCallID, executionLineage string, forUpdate bool) (providerCostPostingFenceRow, bool, error) {
	prefix := executionLineage + ":provider-charge:"
	prefixLength := utf8.RuneCountInString(prefix)
	query := providerCostPostingFenceSelect + ` WHERE store_id = ? AND account_id = ? AND call_id = ? AND substr(lineage_key, 1, ?) = ? AND length(lineage_key) > ? ORDER BY id LIMIT 1`
	if forUpdate && q.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var row providerCostPostingFenceRow
	err := q.NewRaw(query, s.storeID, accountID, callID.String(), prefixLength, prefix, prefixLength).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return providerCostPostingFenceRow{}, false, nil
	}
	if err != nil {
		return providerCostPostingFenceRow{}, false, fmt.Errorf("billingstore: load provider charge posting fence: %w", err)
	}
	if row.EvidenceRevision <= 0 || row.Fence <= 0 || row.AmountNano < 0 {
		return providerCostPostingFenceRow{}, false, fmt.Errorf("billingstore: invalid provider charge posting fence %s", row.LineageKey)
	}
	return row, true, nil
}

// ClaimProviderCostWorkForRevision atomically retires one legacy queue item
// when a durable revision fence already owns the same B-leg lineage. The
// resolver is intentionally bypassed in this case: its result is stale by
// construction and must not create a competing diagnostic or journal path.
func (s *DurableStore) ClaimProviderCostWorkForRevision(ctx context.Context, work billing.ProviderCostWork) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("billingstore: nil store")
	}
	if err := work.CallID.Validate(); err != nil {
		return false, err
	}
	leg, err := work.Leg.Seal()
	if err != nil {
		return false, err
	}
	accountID := strings.TrimSpace(work.AccountID)
	if accountID == "" || leg.CallID != work.CallID {
		return false, fmt.Errorf("%w: provider-cost cutover identity mismatch", billing.ErrSettlementInvalid)
	}
	var claimed bool
	err = withAccountTxErr(ctx, accountTxRetry{Attempts: 40}, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("billingstore: begin provider cost cutover claim: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		executionLineageKey, err := providerCostExecutionLineageKey(work.CallID, leg.BLegID)
		if err != nil {
			return err
		}
		fence, found, err := s.loadProviderCostExecutionFence(ctx, tx, accountID, work.CallID, executionLineageKey, true)
		if err != nil {
			return err
		}
		if !found {
			// Upgrade/restart recovery: an older V2 writer may have committed a
			// provider-charge child fence before the execution gate was added.
			// Materialize that gate here so the stale legacy resolver is bypassed
			// even when this queue item is the first post-upgrade touch.
			childFence, childFound, childErr := s.loadProviderCostProviderChargeFence(ctx, tx, accountID, work.CallID, executionLineageKey, true)
			if childErr != nil {
				return childErr
			}
			if childFound {
				if childFence.Authority != providerCostFenceAuthorityRevision {
					return fmt.Errorf("%w: provider charge fence authority", billing.ErrProviderCostRevisionFence)
				}
				if _, err := s.seedProviderCostExecutionFenceFromPostingFenceInTx(ctx, tx, accountID, work.CallID, executionLineageKey, childFence, string(metering.SubjectProviderCharge)); err != nil {
					return err
				}
				fence, found, err = s.loadProviderCostExecutionFence(ctx, tx, accountID, work.CallID, executionLineageKey, true)
				if err != nil {
					return err
				}
			}
		}
		if !found || fence.Authority != providerCostFenceAuthorityRevision {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("billingstore: commit provider cost cutover miss: %w", err)
			}
			return nil
		}
		if err := markProviderCostWorkProcessed(ctx, tx, leg.Key); err != nil {
			return err
		}
		claimed = true
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingstore: commit provider cost cutover claim: %w", err)
		}
		return nil
	})
	return claimed, err
}

func providerCostFenceAmount(row providerCostPostingFenceRow) billing.Money {
	return billing.Money{Nano: row.AmountNano, Currency: row.Currency}
}

func validateProviderCostFenceAuthority(authority string) error {
	if authority != providerCostFenceAuthorityLegacy && authority != providerCostFenceAuthorityRevision {
		return fmt.Errorf("billingstore: unknown provider cost posting authority %q", authority)
	}
	return nil
}

func (s *DurableStore) insertProviderCostPostingFenceInTx(ctx context.Context, tx bun.Tx, accountID string, callID billing.BillingCallID, lineageKey, authority, headKey string, evidenceRevision int64, inputSetHash, fingerprint string, amount billing.Money, originalTransactionID, operationKey, transactionID string) error {
	if err := validateProviderCostFenceAuthority(authority); err != nil {
		return err
	}
	if evidenceRevision <= 0 {
		return fmt.Errorf("billingstore: invalid provider cost posting fence revision %d", evidenceRevision)
	}
	if strings.TrimSpace(lineageKey) == "" || strings.TrimSpace(headKey) == "" || strings.TrimSpace(inputSetHash) == "" || strings.TrimSpace(fingerprint) == "" || strings.TrimSpace(operationKey) == "" {
		return fmt.Errorf("billingstore: incomplete provider cost posting fence")
	}
	if err := amount.Validate(); err != nil {
		return fmt.Errorf("billingstore: provider cost posting fence amount: %w", err)
	}
	now := time.Now().UTC().UnixNano()
	if _, err := tx.NewRaw(`INSERT INTO billing_provider_cost_posting_fences(
			store_id, account_id, call_id, lineage_key, authority, head_key, evidence_revision,
			input_set_hash, fingerprint, amount_nano, currency, fence, last_operation_key,
			original_transaction_id, last_transaction_id, created_at_unix, updated_at_unix
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.storeID, accountID, callID.String(), lineageKey, authority, headKey, evidenceRevision,
		inputSetHash, fingerprint, amount.Nano, amount.Currency, 1, operationKey, originalTransactionID, transactionID, now, now).Exec(ctx); err != nil {
		return fmt.Errorf("billingstore: insert provider cost posting fence: %w", err)
	}
	return nil
}

func (s *DurableStore) advanceProviderCostPostingFenceInTx(ctx context.Context, tx bun.Tx, existing providerCostPostingFenceRow, accountID string, callID billing.BillingCallID, lineageKey, authority, headKey string, evidenceRevision int64, inputSetHash, fingerprint string, amount billing.Money, operationKey, transactionID string) error {
	if err := validateProviderCostFenceAuthority(authority); err != nil {
		return err
	}
	if existing.EvidenceRevision <= 0 || existing.Fence <= 0 || existing.Fence >= math.MaxInt64 || evidenceRevision <= 0 {
		return fmt.Errorf("billingstore: invalid provider cost posting fence transition")
	}
	if err := amount.Validate(); err != nil {
		return fmt.Errorf("billingstore: provider cost posting fence amount: %w", err)
	}
	result, err := tx.NewRaw(`UPDATE billing_provider_cost_posting_fences SET
			authority = ?, head_key = ?, evidence_revision = ?, input_set_hash = ?, fingerprint = ?,
			amount_nano = ?, currency = ?, fence = fence + 1, last_operation_key = ?,
			original_transaction_id = CASE WHEN original_transaction_id <> '' THEN original_transaction_id WHEN last_transaction_id <> '' THEN last_transaction_id ELSE ? END,
			last_transaction_id = ?, updated_at_unix = ?
			WHERE id = ? AND store_id = ? AND account_id = ? AND call_id = ? AND lineage_key = ? AND fence = ?`,
		authority, headKey, evidenceRevision, inputSetHash, fingerprint, amount.Nano, amount.Currency,
		operationKey, transactionID, transactionID, time.Now().UTC().UnixNano(), existing.ID, s.storeID, accountID,
		callID.String(), lineageKey, existing.Fence).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingstore: advance provider cost posting fence: %w", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("billingstore: provider cost posting fence rows affected: %w", affectedErr)
	} else if affected != 1 {
		return fmt.Errorf("%w: provider cost posting fence %s", billing.ErrProviderCostRevisionFence, lineageKey)
	}
	return nil
}

// recoverLegacyProviderCostFence imports an already committed pre-fence
// legacy operation marker. This keeps an upgrade/restart from treating an old
// provider journal as an unclaimed revision and posting it a second time.
func (s *DurableStore) recoverLegacyProviderCostFence(ctx context.Context, tx bun.Tx, accountID string, callID billing.BillingCallID, legKey string, currency string) (providerCostPostingFenceRow, bool, error) {
	legacySource, err := billing.ProviderCostSourceKey(legKey)
	if err != nil {
		return providerCostPostingFenceRow{}, false, err
	}
	marker, found, err := loadOperationSnapshot(ctx, tx, accountID, "provider_call_cogs", legKey)
	if err != nil || !found {
		return providerCostPostingFenceRow{}, found, err
	}
	amount := billing.Money{Currency: currency}
	transactionID := ""
	if journal, journalFound, journalErr := lookupJournalBySource(ctx, tx, accountID, billing.JournalBookFinancial, legacySource); journalErr != nil {
		return providerCostPostingFenceRow{}, false, journalErr
	} else if journalFound {
		transactionID = journal.ID
		for _, entry := range journal.Entries {
			if entry.LedgerAccount != "inference_provider_cogs" || entry.Side != billing.JournalDebit {
				continue
			}
			if amount.Currency == "" {
				amount.Currency = entry.Amount.Currency
			}
			if entry.Amount.Currency != amount.Currency {
				return providerCostPostingFenceRow{}, false, billing.ErrMoneyCurrencyMismatch
			}
			updated, addErr := amount.Add(entry.Amount)
			if addErr != nil {
				return providerCostPostingFenceRow{}, false, addErr
			}
			amount = updated
		}
	}
	if amount.Currency == "" {
		return providerCostPostingFenceRow{}, false, fmt.Errorf("billingstore: legacy provider cost marker has no currency")
	}
	return providerCostPostingFenceRow{
		StoreID: s.storeID, AccountID: accountID, CallID: callID.String(), LineageKey: legKey,
		Authority: providerCostFenceAuthorityLegacy, HeadKey: legKey, EvidenceRevision: 1,
		InputSetHash: legacyProviderCostFenceInputHash(legKey), Fingerprint: marker.Fingerprint,
		AmountNano: amount.Nano, Currency: amount.Currency, Fence: 1, OriginalTransactionID: transactionID,
		LastOperationKey: marker.OperationKey, LastTransactionID: transactionID,
	}, true, nil
}
