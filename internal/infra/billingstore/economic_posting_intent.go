package billingstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

// economicPostingIntentForWork derives durable delivery intent for one
// normalized work envelope. Monetary requires provider queue +
// provider_rating kind + valid B-leg lineage (never queue name alone).
// Evidence-only covers customer rating, reconciliation, shadow, and queues
// without a posting adapter.
func economicPostingIntentForWork(work billing.EconomicRevisionWork) (providerPosting bool, postingOwner string) {
	if !billing.IsMonetaryEconomicRevisionWork(work) {
		return false, ""
	}
	return true, billing.EffectiveEconomicPostingOwner(work)
}

// economicPostingIntentForWorkWithOwner derives intent with an explicit owner
// override for V2 monetary work. Empty owner preserves V1 inference;
// "v2" requires monetary work and marks explicit V2 intent.
func economicPostingIntentForWorkWithOwner(work billing.EconomicRevisionWork, owner string) (bool, string, error) {
	if owner == "" {
		p, o := economicPostingIntentForWork(work)
		return p, o, nil
	}
	if owner != billing.PostingOwnerV1 && owner != billing.PostingOwnerV2 {
		return false, "", fmt.Errorf("%w: unknown economic posting owner %q", billing.ErrInvalidEconomicRevision, owner)
	}
	if !billing.IsMonetaryEconomicRevisionWork(work) {
		return false, "", fmt.Errorf("%w: explicit %q posting requires monetary provider work", billing.ErrInvalidEconomicRevision, owner)
	}
	return true, owner, nil
}

// ResolveEconomicRevisionPostingOwner returns the call-scoped durable
// posting owner admitted for one monetary work item, preferring the admitted
// customer pin for its account/call over any unbound global marker read.
// When no admitted pin exists it falls back to the current marker (V2 in
// v2_active, else V1); absent markers default to V1. Non-monetary shapes
// return "" (no posting owner). Operational failures fail closed.
func (s *DurableStore) ResolveEconomicRevisionPostingOwner(ctx context.Context, work billing.EconomicRevisionWork) (string, error) {
	if err := s.validateContext(ctx); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	normalized, err := work.Normalize()
	if err != nil {
		return "", err
	}
	if !billing.IsMonetaryEconomicShape(normalized) {
		return "", nil
	}
	// Call-scoped durable ownership: the admitted customer pin for this
	// account/call, never a fresh global reclassification. Subject carries
	// the trusted B-leg lineage; account/call come from there.
	accountID := strings.TrimSpace(normalized.Subject.AccountID)
	callRaw := strings.TrimSpace(normalized.Subject.BillingCallID)
	if callRaw == "" {
		callRaw = strings.TrimSpace(normalized.Subject.CallID)
	}
	if accountID != "" && callRaw != "" {
		if callID, perr := billing.ParseBillingCallID(callRaw); perr == nil {
			if opKey, kerr := billing.CustomerPostingOperationKey(accountID, callID); kerr == nil {
				if row, found, lerr := s.loadPostingOwnershipPin(ctx, s.db, billing.PostingOperationCustomerSettlement, opKey); lerr != nil {
					return "", lerr
				} else if found {
					if pin, perr := postingOwnershipRowToPin(row); perr == nil {
						if pin.Owner == billing.PostingOwnerV1 || pin.Owner == billing.PostingOwnerV2 {
							return pin.Owner, nil
						}
					} else {
						return "", perr
					}
				}
			}
		}
	}
	marker, merr := s.GetAccountingCutover(ctx)
	if merr != nil {
		// Absent marker (legacy store): V1 default.
		return billing.PostingOwnerV1, nil
	}
	if billing.IsV2NewWorkAuthorized(marker.State) {
		return billing.PostingOwnerV2, nil
	}
	return billing.PostingOwnerV1, nil
}

// ensureEconomicPostingColumnsBestEffort keeps classification/counts operable
// when the F2B migration has not yet run (e.g. legacy test DBs): it probes for
// the new columns and skips intent persistence, falling back to inference.
func (s *DurableStore) economicPostingColumnsPresent(ctx context.Context, q bun.IDB) bool {
	var n int
	if err := q.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_economic_revision_work_state') WHERE name = 'provider_posting'`).Scan(ctx, &n); err == nil && n == 1 {
		return true
	}
	// PostgreSQL probe.
	var col string
	if err := q.NewRaw(`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_economic_revision_work_state' AND column_name = 'provider_posting' LIMIT 1`).Scan(ctx, &col); err == nil && col == "provider_posting" {
		return true
	}
	return false
}
