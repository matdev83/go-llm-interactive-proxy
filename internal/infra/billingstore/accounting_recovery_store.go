package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Durable rollback/recovery reads for Task 17.4 (Migration Strategy step 7,
// requirements 10.5, 11.6, 17.5, 18.4).
//
// HasV2MonetaryPostings reports whether durable V2 financial authority exists
// for this store boundary: any V2-owned posting-ownership pin (pinned or
// completed, including admission pins that precede money movement) or any
// V2-owned monetary economic work-state row. Every 17.3 production posting
// (customer settlement, provider charge, financial adjustment, synchronous
// adjustment, monetary economic revision) acquires a posting pin under a
// mandatory cutover claim token, so V2 pins are the necessary and sufficient
// durable signal for V2 financial authority. Shadow V2 capture
// (observations/valuations/reconciliations), evidence-only work, and pending
// provider evidence carry no V2 pin and never count: they are capture, not
// postings, and must survive rollback/recovery without authorizing an old
// monetary writer.
//
// Optional raw-evidence contract: privileged raw-transport/capture bytes
// (traffic.RawCaptureSink, DisabledRawCapture by default) are deliberately
// absent/unsupported in durable accounting and are never read here.
// Canonical metering observations persist as bounded allowlisted
// SafeEvidenceField lexemes with canonical observation refs/payload hashes;
// the operational provider-cost work queue holds canonical pending-work
// keys, not raw bodies. Pruning processed queue rows (pruneProcessedProviderCostWork)
// is operational retention only and is not raw-evidence expiry. Compatible
// recovery with the raw artifact unavailable preserves canonical
// observations/evidence identity, balances, journal/adjustment references,
// and pending work; no raw retention subsystem is introduced.
//
// GetAccountingRecoverySnapshot returns the durable per-store recovery input
// (marker plus V2 posting presence) for the core rollback/startup policy.
// MarkerFound is false for legacy stores that predate the cutover marker;
// the core policy converges those on the explicit V1 floor.
//
// VerifyAccountingRecovery combines the snapshot with the serving binary
// capability: compatible binaries receive the snapshot, incompatible ones
// receive ErrAccountingStaleBinary. All three methods are strictly
// read-only; verification never writes, claims, posts, or drains.
//
// No globals, no provider SDKs, no manual operator SQL: queries are bounded
// (LIMIT 1) and store-scoped.

// HasV2MonetaryPostings reports durable V2 financial authority for this
// store boundary. It returns false (nil error) on fresh, V1-only, and
// capture/pending-evidence-only stores.
func (s *DurableStore) HasV2MonetaryPostings(ctx context.Context) (bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return false, err
	}
	var one int
	err := s.db.NewRaw(`SELECT 1 FROM billing_posting_ownership_pins WHERE store_id = ? AND owner = ? LIMIT 1`,
		s.storeID, billing.PostingOwnerV2).Scan(ctx, &one)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("billingstore: detect V2 posting pins: %w", err)
	}
	if err == nil {
		return true, nil
	}
	err = s.db.NewRaw(`SELECT 1 FROM billing_economic_revision_work_state WHERE store_id = ? AND provider_posting = 1 AND posting_owner = ? LIMIT 1`,
		s.storeID, billing.PostingOwnerV2).Scan(ctx, &one)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("billingstore: detect V2 monetary economic work: %w", err)
	}
	return err == nil, nil
}

// GetAccountingRecoverySnapshot loads the durable per-store recovery
// snapshot: the cutover marker when present plus V2 posting presence.
func (s *DurableStore) GetAccountingRecoverySnapshot(ctx context.Context) (billing.AccountingRecoverySnapshot, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	snapshot := billing.AccountingRecoverySnapshot{StoreID: s.storeID}
	row, found, err := s.loadAccountingCutover(ctx, s.db)
	if err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	if found {
		marker, err := accountingCutoverRowToMarker(row)
		if err != nil {
			return billing.AccountingRecoverySnapshot{}, err
		}
		snapshot.MarkerFound = true
		snapshot.Marker = marker
	}
	hasV2, err := s.HasV2MonetaryPostings(ctx)
	if err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	snapshot.HasV2MonetaryPosting = hasV2
	if err := snapshot.Validate(); err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	return snapshot, nil
}

// VerifyAccountingRecovery enforces the serving-binary capability against
// the durable snapshot before the process admits or posts anything.
// Compatible binaries receive the snapshot; stale binaries receive
// ErrAccountingStaleBinary with zero writes, claims, postings, or drains.
func (s *DurableStore) VerifyAccountingRecovery(ctx context.Context, capability billing.AccountingBinaryCapability) (billing.AccountingRecoverySnapshot, error) {
	if err := s.validateContext(ctx); err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	if err := capability.Validate(); err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	snapshot, err := s.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	if err := billing.CheckAccountingStartup(snapshot, capability); err != nil {
		return billing.AccountingRecoverySnapshot{}, err
	}
	return snapshot, nil
}
