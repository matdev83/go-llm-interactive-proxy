package billing

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3 F2B: monetary economic revision intent.
//
// Only provider-queue provider-rating work can create provider payable/cost/
// exclusion through the production EconomicRevisionWorker with a provider-cost
// adapter. Customer rating, reconciliation jobs (any queue), shadow
// observations/valuations (no-post path), and queues without a posting adapter
// are evidence-only and must never be classified, pinned, or fenced as
// monetary work.
//
// Intent is inferred from the immutable work envelope (queue + kind +
// B-leg/provider-charge subject lineage), never from queue name alone:
//   - queue must be provider,
//   - kind must be provider_rating (legacy empty normalizes to provider_rating),
//   - subject must be B-leg or provider-charge with a valid store/account/call
//     lineage capable of deriving a canonical ProviderPostingOperationKey.
//
// Adapter presence is explicit at composition (process_billing wires
// provider-cost only for the provider queue); the durable classifier counts
// only this narrow set. Explicit evidence-only overrides (e.g. shadow) are
// recorded in queue delivery state (provider_posting=0), not in the immutable
// work identity, so legacy payloads remain byte-compatible.
func IsMonetaryEconomicRevisionWork(work EconomicRevisionWork) bool {
	if work.EvidenceOnly {
		return false
	}
	queue := work.Queue
	kind := work.Kind
	if kind == "" {
		kind = EconomicWorkKindForQueue(queue)
	}
	if queue != EconomicQueueProvider {
		return false
	}
	if kind != EconomicWorkKindProviderRating {
		return false
	}
	subject := work.Subject
	if subject.Kind != metering.SubjectBLeg && subject.Kind != metering.SubjectProviderCharge {
		return false
	}
	if strings.TrimSpace(subject.StoreID) == "" || strings.TrimSpace(subject.AccountID) == "" {
		return false
	}
	callRaw := strings.TrimSpace(subject.BillingCallID)
	if callRaw == "" {
		callRaw = strings.TrimSpace(subject.CallID)
	}
	if callRaw == "" {
		return false
	}
	if _, err := ParseBillingCallID(callRaw); err != nil {
		return false
	}
	if strings.TrimSpace(subject.BLegID) == "" {
		return false
	}
	return true
}

// MonetaryEconomicPostingKey derives the canonical revision-specific provider
// operation identity for one monetary economic revision (F5+F7). It fails
// closed for evidence-only work so classifiers never invent pins from
// customer/reconciliation envelopes. The key is the immutable revision pin
// (ProviderRevisionPostingOperationKeyForWork using the allocation-aware full
// hash), not merely B-leg lineage: base legacy charges keep lineage pins,
// each revision outcome is a distinct pin while heads/fences order lineage.
func MonetaryEconomicPostingKey(storeID string, work EconomicRevisionWork) (accountID string, callID BillingCallID, operationKey string, err error) {
	normalized, err := work.Normalize()
	if err != nil {
		return "", "", "", err
	}
	if !IsMonetaryEconomicRevisionWork(normalized) {
		return "", "", "", ErrInvalidEconomicRevision
	}
	callRaw := strings.TrimSpace(normalized.Subject.BillingCallID)
	if callRaw == "" {
		callRaw = strings.TrimSpace(normalized.Subject.CallID)
	}
	callID, err = ParseBillingCallID(callRaw)
	if err != nil {
		return "", "", "", err
	}
	accountID = strings.TrimSpace(normalized.Subject.AccountID)
	identity, err := normalized.Identity()
	if err != nil {
		return "", "", "", err
	}
	fullHash := identity.DerivationHash
	if fullHash == "" {
		fullHash = identity.InputSetHash
	}
	operationKey, err = ProviderRevisionPostingOperationKeyForWork(accountID, callID, normalized.HeadKey, normalized.EvidenceRevision, fullHash)
	if err != nil {
		return "", "", "", err
	}
	_ = storeID
	return accountID, callID, operationKey, nil
}

// IsMonetaryEconomicShape reports whether one work envelope has monetary
// provider-rating shape (queue/kind/subject lineage) ignoring delivery
// intent (EvidenceOnly/PostingOwner). Delivery state (provider_posting)
// decides whether that shape is actually monetary; the immutable payload
// alone is never authoritative after R2.
func IsMonetaryEconomicShape(work EconomicRevisionWork) bool {
	queue := work.Queue
	kind := work.Kind
	if kind == "" {
		kind = EconomicWorkKindForQueue(queue)
	}
	if queue != EconomicQueueProvider {
		return false
	}
	if kind != EconomicWorkKindProviderRating {
		return false
	}
	subject := work.Subject
	if subject.Kind != metering.SubjectBLeg && subject.Kind != metering.SubjectProviderCharge {
		return false
	}
	if strings.TrimSpace(subject.StoreID) == "" || strings.TrimSpace(subject.AccountID) == "" {
		return false
	}
	callRaw := strings.TrimSpace(subject.BillingCallID)
	if callRaw == "" {
		callRaw = strings.TrimSpace(subject.CallID)
	}
	if callRaw == "" {
		return false
	}
	if _, err := ParseBillingCallID(callRaw); err != nil {
		return false
	}
	if strings.TrimSpace(subject.BLegID) == "" {
		return false
	}
	return true
}

// AuthoritativeIsMonetary reports the single authoritative delivery intent
// for one immutable payload plus its mutable queue delivery state:
// monetary iff the payload has monetary shape AND state says
// provider_posting. Payload EvidenceOnly/PostingOwner alone never decides.
func AuthoritativeIsMonetary(work EconomicRevisionWork, providerPosting bool) bool {
	return providerPosting && IsMonetaryEconomicShape(work)
}

// OverlayAuthoritativeEconomicWork returns the authoritative work view for
// production list/claim/worker consumption: immutable evidence identity is
// preserved (queue/head/revision/input hash), while delivery intent comes
// from mutable state. Monetary state overlays owner and clears any stale
// evidence-only flag so split payload/state can never disagree; evidence
// state returns the stored payload unchanged (still non-posting).
func OverlayAuthoritativeEconomicWork(work EconomicRevisionWork, providerPosting bool, postingOwner string) EconomicRevisionWork {
	if !providerPosting || !IsMonetaryEconomicShape(work) {
		return work
	}
	out := work
	out.EvidenceOnly = false
	owner := strings.TrimSpace(postingOwner)
	if owner != PostingOwnerV1 && owner != PostingOwnerV2 {
		owner = PostingOwnerV1
	}
	out.PostingOwner = owner
	return out
}

// EffectiveEconomicPostingOwner returns the stable posting owner for one
// monetary work envelope: explicit V1/V2 when set, otherwise the legacy V1
// default. Evidence-only work has no posting owner. Retained for
// payload-only callers; production list/claim/worker must use the
// authoritative overlay above so state and payload cannot disagree.
func EffectiveEconomicPostingOwner(work EconomicRevisionWork) string {
	if work.EvidenceOnly {
		return ""
	}
	if work.PostingOwner == PostingOwnerV1 || work.PostingOwner == PostingOwnerV2 {
		return work.PostingOwner
	}
	return PostingOwnerV1
}
