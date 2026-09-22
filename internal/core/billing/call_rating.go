package billing

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type CallRatingInput struct {
	Call              CallUsageRecord
	Legs              []CallLegUsageRecord
	MaxCustomerCharge Money
	CustomerPricing   PricingSnapshot
	CustomerPolicy    ChargePolicy
	// ModelPricing carries the effective per backend/model customer pricing
	// cards resolved for the call legs. An empty set means no route/model
	// override exists and the configured default pricing applies to every
	// selected leg. When overrides exist, each selected leg must resolve its
	// own card; a missing applicable card fails rating explicitly rather than
	// silently substituting an unrelated model or the default price.
	//
	// Operator-rate data is deliberately absent from this customer type: it
	// belongs to provider COGS processing only, so provider-cost readiness can
	// never couple into customer settlement.
	ModelPricing []ModelCustomerPricing
	// CustomerTariff and ModelTariffs carry the immutable generic customer
	// tariff material for V2 B-leg retail rating. A legacy-tagged tariff
	// materialized from a scalar pricing card is explicitly mapped component
	// material: V2-owned work rates it through the component path from
	// canonical V2 quantities wherever those semantics are supported, and
	// fails closed where they are not. It never selects the scalar live
	// engine for V2-owned work.
	CustomerTariff economics.TariffSnapshot
	ModelTariffs   []ModelCustomerTariff
	// ProviderCost is used only by an explicit cost-pass-through customer
	// policy. It is never consulted by independent retail rating.
	ProviderCost *CostPassThroughProviderCost
	// PostingOwner selects the durable B1 pin owner this rating is produced
	// for (PostingOwnerV1/PostingOwnerV2). Empty preserves the legacy
	// historical-replay default: V1 scalar for V1-classified legs, V2
	// component (or fail-closed) when V2 quantity evidence is present.
	// Production workers/resolvers must supply the explicit claim owner so
	// V2-owned work can never silently select the scalar live engine and V1
	// drain work stays isolated behind durable V1 ownership.
	PostingOwner string
}
type CallRatingResult struct {
	CallID            BillingCallID
	CustomerCharge    Money
	Fingerprint       string
	CustomerValuation economics.Valuation
	// CustomerUnitOperation is an optional customer-owned allowance debit or
	// reservation plan. When supplied, the durable settlement adapter applies it
	// in the same transaction as the monetary customer posting.
	CustomerUnitOperation *CustomerUnitOperation
	// CustomerUnitFallbackCharge is the concrete charge for uncovered units. It
	// is required when the atomic unit result reports a bounded fallback and is
	// checked against that operation's bound before settlement commits.
	CustomerUnitFallbackCharge *Money
	CostPassThrough            *CostPassThroughSettlement
	// RouteTariffs carries the route tariff bindings actually used to rate
	// this call (one entry per rated route). The terminal settlement compares
	// them against the admitted frozen binding before posting. Empty on the
	// scalar legacy and cost-pass-through paths, which use no tariff material.
	RouteTariffs []RouteTariffBinding
}
type ApplyCallBillingInput struct {
	Call          CallUsageRecord
	Exposure      CallExposure
	Result        CallRatingResult
	OperationKind string
	// PostingOwner selects the B1 pin owner for the customer settlement fence.
	// Empty preserves the legacy V1 default for backward compatibility.
	// Draining forbids new pins; v2_active permits only V2.
	PostingOwner string
	// Claim carries the B2a worker-claim metadata (owner/epoch) captured at
	// claim time. When present, settlement validates it against the current
	// marker and pin to close TOCTOU between claim and posting. Nil preserves
	// legacy direct calls in v1_active/shadow; draining/active still fence
	// unpinned/stale work even without a claim.
	Claim *CutoverClaimMetadata
}
type CallSettlement struct {
	CallID             BillingCallID
	Customer           Posting
	CustomerUnitResult *CustomerUnitOperationResult
	CostPassThrough    *CostPassThroughSettlement
	Replayed           bool
	// Breached reports that the posted customer charge exceeded the admitted
	// exposure maximum. The actual incurred amount is retained in the posting;
	// it is never truncated to the quote. OverrunNano carries the exact
	// excess and is zero when not breached.
	Breached    bool
	OverrunNano int64
}

// OwnerAwareCallRatingResolver is the production customer-rating port that
// carries the durable B1 pin owner into rating selection. Workers must use
// it with the claim owner so V2-owned work can never silently select the
// scalar live engine. Production/cutover V2 requires this port: a resolver
// exposing only the legacy CallRatingResolver fails closed for V2 before
// any money. The legacy port survives only for V1 drain and test-only
// non-cutover paths.
type OwnerAwareCallRatingResolver interface {
	ResolveCallRatingForOwner(ctx context.Context, complete CompleteCall, exposure CallExposure, owner string) (CallRatingResult, error)
}

// ValidateCallRatingResultForOwner is the posting-boundary companion to
// owner-aware selection: V2-owned money must carry a complete,
// structurally valid customer component valuation (or an explicit
// cost-pass-through settlement under its own complete contract). An ID-only
// valuation, a scalar result (empty valuation, no pass-through), or a
// malformed component/basis valuation is rejected for V2 before any journal,
// balance, or exposure effect. V1 drain and legacy empty owners preserve
// historical scalar replay.
//
// This is the structural gate: it reuses the approved economics.Valuation
// contract (Validate plus V2 customer R-plane completeness) but cannot bind
// account/call/currency/amount without settlement context. Production
// worker/store/resolver boundaries must use
// ValidateCallRatingResultForSettlement with the settled call and exposure
// so wrong subject/account/call, wrong currency/amount, or mismatched result
// identity also fails closed before money.
func ValidateCallRatingResultForOwner(result CallRatingResult, owner string) error {
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" || trimmed == PostingOwnerV1 {
		return nil
	}
	if trimmed != PostingOwnerV2 {
		return fmt.Errorf("%w: unknown rating owner %q", ErrPostingOwnershipInvalid, owner)
	}
	if result.CostPassThrough != nil {
		if err := result.CostPassThrough.Validate(""); err != nil {
			return fmt.Errorf("%w: V2-owned pass-through is incomplete: %w", ErrRetailRateIncomplete, err)
		}
		if strings.TrimSpace(result.CustomerValuation.ID) != "" {
			return fmt.Errorf("%w: V2-owned pass-through must not carry a component valuation", ErrRetailRateIncomplete)
		}
		return nil
	}
	v := result.CustomerValuation
	if strings.TrimSpace(v.ID) == "" {
		return fmt.Errorf("%w: V2-owned rating requires a component valuation", ErrRetailRateIncomplete)
	}
	if err := v.Validate(); err != nil {
		return fmt.Errorf("%w: V2-owned component valuation is incomplete: %w", ErrRetailRateIncomplete, err)
	}
	if v.Version != economics.ValuationVersionV2 {
		return fmt.Errorf("%w: V2-owned valuation version %d is not %d", ErrRetailRateIncomplete, v.Version, economics.ValuationVersionV2)
	}
	if v.Perspective != metering.PerspectiveCustomer {
		return fmt.Errorf("%w: V2-owned valuation perspective %q is not customer", ErrRetailRateIncomplete, v.Perspective)
	}
	if v.Basis != economics.BasisCustomerPolicy {
		return fmt.Errorf("%w: V2-owned valuation basis %q is not customer_policy", ErrRetailRateIncomplete, v.Basis)
	}
	if v.Completeness != economics.CompletenessComplete {
		return fmt.Errorf("%w: V2-owned valuation completeness %q is not complete", ErrRetailRateIncomplete, v.Completeness)
	}
	if len(v.Lines) == 0 {
		return fmt.Errorf("%w: V2-owned valuation has no lines", ErrRetailRateIncomplete)
	}
	hasComponent := false
	for _, line := range v.Lines {
		if line.Component != nil {
			hasComponent = true
			break
		}
	}
	if !hasComponent {
		return fmt.Errorf("%w: V2-owned valuation has no B-leg component line", ErrRetailRateIncomplete)
	}
	if len(v.Totals) == 0 {
		return fmt.Errorf("%w: V2-owned valuation has no totals", ErrRetailRateIncomplete)
	}
	return nil
}

// ValidateCallRatingResultForSettlement is the generic V2 settlement
// boundary: it enforces the complete customer valuation contract bound to
// the exact settlement subject/scope, currency, and rated amount/result
// identity, reusing the approved domain validators. It must be called at the
// generic worker before Apply and at the billingstore transactional boundary
// (including direct Apply) so ID-only, wrong account/call/subject, wrong
// currency/amount, malformed component/basis, or mismatched result fails
// closed with zero balance/journal/exposure/pin effects. V1 drain and legacy
// empty owners preserve historical scalar replay. Explicit cost-pass-through
// remains a separate valid path only under its existing complete contract
// (policy/amount/currency bound to the settled call); it never serves as a
// generic bypass.
func ValidateCallRatingResultForSettlement(result CallRatingResult, call CallUsageRecord, exposure CallExposure, owner string) error {
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" || trimmed == PostingOwnerV1 {
		return nil
	}
	if trimmed != PostingOwnerV2 {
		return fmt.Errorf("%w: unknown rating owner %q", ErrPostingOwnershipInvalid, owner)
	}
	if err := result.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: V2-owned result call identity: %w", ErrRetailRateIncomplete, err)
	}
	if result.CallID != call.CallID {
		return fmt.Errorf("%w: V2-owned result call %q differs from settled call %q", ErrRetailRateIncomplete, result.CallID.String(), call.CallID.String())
	}
	if strings.TrimSpace(call.AccountID) == "" {
		return fmt.Errorf("%w: V2-owned settlement call account is required", ErrRetailRateIncomplete)
	}
	if strings.TrimSpace(exposure.AccountID) != "" && exposure.AccountID != call.AccountID {
		return fmt.Errorf("%w: V2-owned exposure account %q differs from settled account %q", ErrRetailRateIncomplete, exposure.AccountID, call.AccountID)
	}
	if strings.TrimSpace(exposure.CallID) != "" && exposure.CallID != call.CallID.String() {
		return fmt.Errorf("%w: V2-owned exposure call %q differs from settled call %q", ErrRetailRateIncomplete, exposure.CallID, call.CallID.String())
	}
	if err := result.CustomerCharge.Validate(); err != nil {
		return fmt.Errorf("%w: V2-owned customer charge: %w", ErrRetailRateIncomplete, err)
	}
	if result.CustomerCharge.Nano < 0 {
		return fmt.Errorf("%w: V2-owned customer charge cannot be negative", ErrRetailRateIncomplete)
	}
	if strings.TrimSpace(exposure.Max.Currency) != "" && result.CustomerCharge.Currency != exposure.Max.Currency {
		return fmt.Errorf("%w: V2-owned charge currency %q differs from admitted %q", ErrRetailRateIncomplete, result.CustomerCharge.Currency, exposure.Max.Currency)
	}
	if result.CostPassThrough != nil {
		if strings.TrimSpace(result.CustomerValuation.ID) != "" {
			return fmt.Errorf("%w: V2-owned pass-through must not carry a component valuation", ErrRetailRateIncomplete)
		}
		state := result.CostPassThrough
		if err := state.Validate(result.CustomerCharge.Currency); err != nil {
			return fmt.Errorf("%w: V2-owned pass-through is incomplete: %w", ErrRetailRateIncomplete, err)
		}
		if state.PolicyRef.ID != call.ChargePolicyRef.ID || state.PolicyRef.Version != call.ChargePolicyRef.Version {
			return fmt.Errorf("%w: V2-owned pass-through policy %s@%s differs from settled %s@%s", ErrRetailRateIncomplete, state.PolicyRef.ID, state.PolicyRef.Version, call.ChargePolicyRef.ID, call.ChargePolicyRef.Version)
		}
		if state.PostedAmount.Nano != result.CustomerCharge.Nano || state.PostedAmount.Currency != result.CustomerCharge.Currency {
			return fmt.Errorf("%w: V2-owned pass-through posted amount %+v differs from charge %+v", ErrRetailRateIncomplete, state.PostedAmount, result.CustomerCharge)
		}
		return nil
	}
	if err := ValidateCallRatingResultForOwner(result, owner); err != nil {
		return err
	}
	v := result.CustomerValuation
	if v.Subject.Kind != metering.SubjectBillingCall {
		return fmt.Errorf("%w: V2-owned valuation subject kind %q is not billing_call", ErrRetailRateIncomplete, v.Subject.Kind)
	}
	if v.Subject.BillingCallID != call.CallID.String() {
		return fmt.Errorf("%w: V2-owned valuation subject call %q differs from settled call %q", ErrRetailRateIncomplete, v.Subject.BillingCallID, call.CallID.String())
	}
	if strings.TrimSpace(v.Subject.StoreID) == "" {
		return fmt.Errorf("%w: V2-owned valuation subject store is required", ErrRetailRateIncomplete)
	}
	if v.Scope != "call:"+call.CallID.String() {
		return fmt.Errorf("%w: V2-owned valuation scope %q is not %q", ErrRetailRateIncomplete, v.Scope, "call:"+call.CallID.String())
	}
	if v.Payer.Kind != metering.PaymentPartyCustomer {
		return fmt.Errorf("%w: V2-owned valuation payer kind %q is not customer", ErrRetailRateIncomplete, v.Payer.Kind)
	}
	if v.Payer.ID != call.AccountID {
		return fmt.Errorf("%w: V2-owned valuation payer %q differs from settled account %q", ErrRetailRateIncomplete, v.Payer.ID, call.AccountID)
	}
	if len(v.Totals) != 1 {
		return fmt.Errorf("%w: V2-owned valuation has %d totals, want exactly one currency total", ErrRetailRateIncomplete, len(v.Totals))
	}
	total := v.Totals[0]
	if total.Currency != result.CustomerCharge.Currency {
		return fmt.Errorf("%w: V2-owned valuation currency %q differs from charge %q", ErrRetailRateIncomplete, total.Currency, result.CustomerCharge.Currency)
	}
	if !total.RoundedAmount.Present {
		return fmt.Errorf("%w: V2-owned valuation rounded total is absent", ErrRetailRateIncomplete)
	}
	if total.RoundedAmount.Currency != result.CustomerCharge.Currency || total.RoundedAmount.NanoUnits != result.CustomerCharge.Nano {
		return fmt.Errorf("%w: V2-owned valuation rounded %+v differs from charge %+v", ErrRetailRateIncomplete, total.RoundedAmount, result.CustomerCharge)
	}
	if v.Policy.ID != call.ChargePolicyRef.ID || v.Policy.Version != call.ChargePolicyRef.Version || v.Policy.PolicyID != call.ChargePolicyRef.ID {
		return fmt.Errorf("%w: V2-owned valuation policy %s@%s/%s differs from settled %s@%s", ErrRetailRateIncomplete, v.Policy.ID, v.Policy.Version, v.Policy.PolicyID, call.ChargePolicyRef.ID, call.ChargePolicyRef.Version)
	}
	if v.Tariff.ID != call.CustomerPricingRef.ID || v.Tariff.Version != call.CustomerPricingRef.Version {
		return fmt.Errorf("%w: V2-owned valuation tariff %s@%s differs from settled %s@%s", ErrRetailRateIncomplete, v.Tariff.ID, v.Tariff.Version, call.CustomerPricingRef.ID, call.CustomerPricingRef.Version)
	}
	if v.Rater.ID != v.Tariff.ID || v.Rater.Version != v.Tariff.Version {
		return fmt.Errorf("%w: V2-owned valuation rater %s@%s differs from tariff %s@%s", ErrRetailRateIncomplete, v.Rater.ID, v.Rater.Version, v.Tariff.ID, v.Tariff.Version)
	}
	expectedHash, err := economics.CanonicalValuationInputSetHash(v.Basis, v.InputObservations, v.AllocationCoverageRefs)
	if err != nil {
		return fmt.Errorf("%w: V2-owned valuation input refs: %w", ErrRetailRateIncomplete, err)
	}
	if v.InputSetHash != expectedHash {
		return fmt.Errorf("%w: V2-owned valuation input hash %q differs from canonical %q", ErrRetailRateIncomplete, v.InputSetHash, expectedHash)
	}
	if strings.TrimSpace(result.Fingerprint) == "" {
		return fmt.Errorf("%w: V2-owned result fingerprint is required", ErrRetailRateIncomplete)
	}
	if fp := v.Fingerprint(); fp == "" {
		return fmt.Errorf("%w: V2-owned valuation fingerprint is unavailable", ErrRetailRateIncomplete)
	} else if result.Fingerprint != fp {
		return fmt.Errorf("%w: V2-owned result fingerprint %q differs from valuation %q", ErrRetailRateIncomplete, result.Fingerprint, fp)
	}
	return nil
}

func RateCall(in CallRatingInput) (CallRatingResult, error) {
	call, err := in.Call.Seal()
	if err != nil {
		return CallRatingResult{}, err
	}
	if err := in.MaxCustomerCharge.Validate(); err != nil {
		return CallRatingResult{}, err
	}
	policy := in.CustomerPolicy.Clone()
	// The already admitted maximum is an acceptable compatibility source for a
	// legacy explicit pass-through policy that omitted its serialized bound.
	if policy.Retail != nil && policy.Retail.Basis == RetailBasisCostPassThrough && policy.Retail.CostPassThrough != nil && policy.Retail.CostPassThrough.SafeBound == nil {
		retail := policy.Retail.Clone()
		pass := retail.CostPassThrough.Clone()
		bound := in.MaxCustomerCharge
		pass.SafeBound = &bound
		retail.CostPassThrough = &pass
		policy.Retail = &retail
	}
	retailPolicy, policyErr := ResolveRetailSelectionPolicy(policy)
	if policyErr != nil {
		return CallRatingResult{}, policyErr
	}
	passThrough := retailPolicy.Basis == RetailBasisCostPassThrough
	if !passThrough && in.MaxCustomerCharge.Currency != in.CustomerPricing.Currency {
		return CallRatingResult{}, ErrRatingCurrencyMismatch
	}
	if in.CustomerPolicy.Ref != call.ChargePolicyRef || in.CustomerPolicy.PricingRef != call.CustomerPricingRef {
		return CallRatingResult{}, ErrRatingSnapshotMismatch
	}
	if !passThrough && in.CustomerPricing.Ref != call.CustomerPricingRef {
		return CallRatingResult{}, ErrRatingSnapshotMismatch
	}
	if err := policy.Validate(); err != nil {
		return CallRatingResult{}, err
	}
	sealedLegs := make([]CallLegUsageRecord, 0, len(in.Legs))
	legFingerprints := make([]string, 0, len(in.Legs))
	for _, source := range in.Legs {
		leg, sealErr := source.Seal()
		if sealErr != nil {
			return CallRatingResult{}, sealErr
		}
		if leg.CallID != call.CallID || !containsExpectedLeg(call.ExpectedBLegIDs, leg.BLegID) {
			return CallRatingResult{}, fmt.Errorf("%w: leg %q is not expected for call", ErrRatingSnapshotMismatch, leg.BLegID)
		}
		sealedLegs = append(sealedLegs, leg)
		legFingerprints = append(legFingerprints, leg.Fingerprint)
	}
	if passThrough {
		retail, retailErr := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
			Call: call, Legs: sealedLegs, Policy: policy, ProviderCost: in.ProviderCost,
			Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		})
		if retailErr != nil {
			return CallRatingResult{CallID: call.CallID, CustomerValuation: retail.Valuation, CostPassThrough: retail.CostPassThrough}, retailErr
		}
		if retail.CustomerCharge.Currency != in.MaxCustomerCharge.Currency {
			return CallRatingResult{}, ErrRatingCurrencyMismatch
		}
		return CallRatingResult{
			CallID: call.CallID, CustomerCharge: retail.CustomerCharge,
			Fingerprint: retail.Fingerprint, CustomerValuation: retail.Valuation,
			CostPassThrough: retail.CostPassThrough,
		}, nil
	}
	// Owner-aware customer rating selection (Phase 18 blocker 1, Migration
	// Strategy step 8, req 15.6).
	//
	// New V2-owned work must use component/V2 rating/valuation semantics. A
	// legacy tariff materialized from a scalar pricing card is explicitly
	// mapped component material: where its input/output/fixed semantics cover
	// the canonical V2 quantities it rates through the component path, and
	// where they do not (or V2 evidence is absent) it fails closed before
	// money. The legacy tag never silently selects the scalar live engine
	// for V2-owned work.
	//
	// The scalar engine survives only as an explicitly authorized historical
	// V1 drain/replay path: durable V1 posting ownership selects the frozen
	// scalar writer. Additive V2/shadow observations may coexist on the same
	// durable legs (boundary capture is additive while the V1 writer remains
	// authoritative); they are preserved durably and never stripped or
	// relabelled, but they do not alter the frozen V1 charge. It is
	// unreachable for V2 owner/token and new live V2 calls.
	owner := effectiveCallRatingOwner(in.PostingOwner, sealedLegs)
	if trimmed := strings.TrimSpace(in.PostingOwner); trimmed != "" && trimmed != PostingOwnerV1 && trimmed != PostingOwnerV2 {
		return CallRatingResult{CallID: call.CallID}, fmt.Errorf("%w: unknown rating owner %q", ErrPostingOwnershipInvalid, in.PostingOwner)
	}
	if owner == PostingOwnerV2 {
		return rateV2ComponentCall(call, sealedLegs, policy, in)
	}
	// Durable V1 ownership authorizes frozen legacy drain/replay even when
	// additive V2/shadow observations are present. Evidence envelope format
	// never overrides the durable monetary owner.
	return rateV1DrainCall(call, sealedLegs, in, legFingerprints)
}

// effectiveCallRatingOwner resolves the durable ownership contract for one
// rating. An explicit V1/V2 owner is authoritative. Empty preserves the
// legacy historical-replay default but stays safe: V2 quantity evidence
// infers V2 (component or fail-closed, never scalar), otherwise V1 drain.
// Unknown owners fail closed.
func effectiveCallRatingOwner(owner string, legs []CallLegUsageRecord) string {
	trimmed := strings.TrimSpace(owner)
	if trimmed == PostingOwnerV1 || trimmed == PostingOwnerV2 {
		return trimmed
	}
	if trimmed != "" {
		return trimmed
	}
	if hasV2RetailQuantityEvidence(legs) {
		return PostingOwnerV2
	}
	return PostingOwnerV1
}

// rateV2ComponentCall rates V2-owned work exclusively through the component
// path, including explicitly mapped legacy tariff semantics where supported.
// Missing V2 evidence, a missing tariff, or any component incompleteness
// fails closed before money; there is no scalar fallback on this path.
func rateV2ComponentCall(call CallUsageRecord, sealedLegs []CallLegUsageRecord, policy ChargePolicy, in CallRatingInput) (CallRatingResult, error) {
	if !hasV2RetailQuantityEvidence(sealedLegs) {
		return CallRatingResult{CallID: call.CallID}, fmt.Errorf("%w: V2-owned rating requires V2 B-leg quantity evidence", ErrRetailRateIncomplete)
	}
	if in.CustomerTariff.Ref.ID == "" {
		return CallRatingResult{CallID: call.CallID}, fmt.Errorf("%w: generic customer tariff is required for V2 B-leg evidence", ErrRetailRateIncomplete)
	}
	retail, retailErr := RateSelectedRetailBLegs(context.Background(), RetailRatingInput{
		Call: call, Legs: sealedLegs, Policy: policy,
		Tariff: in.CustomerTariff, ModelTariffs: in.ModelTariffs,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if retailErr != nil {
		return CallRatingResult{CallID: call.CallID, CustomerValuation: retail.Valuation}, retailErr
	}
	if err := in.MaxCustomerCharge.Validate(); err != nil {
		return CallRatingResult{}, err
	}
	if retail.CustomerCharge.Currency != in.MaxCustomerCharge.Currency {
		return CallRatingResult{}, ErrRatingCurrencyMismatch
	}
	// Legacy-mapped tariffs settle against scalar admission (empty
	// RouteTariffs) during migration: the component valuation proves V2
	// semantics while empty bindings stay compatible with the admitted
	// pricing/policy references. V2-native tariffs attest rich bindings.
	var bindings []RouteTariffBinding
	if !isLegacyScalarTariff(in.CustomerTariff) {
		var err error
		bindings, err = ratedRouteTariffBindings(sealedLegs, retail.Valuation, in.CustomerTariff, in.ModelTariffs)
		if err != nil {
			return CallRatingResult{CallID: call.CallID, CustomerValuation: retail.Valuation}, err
		}
	}
	return CallRatingResult{
		CallID: call.CallID, CustomerCharge: retail.CustomerCharge,
		Fingerprint: retail.Fingerprint, CustomerValuation: retail.Valuation,
		RouteTariffs: bindings,
	}, nil
}

// rateV1DrainCall is the isolated historical scalar path for already-pinned
// V1 draining/replay. Durable V1 posting ownership selects this frozen
// writer, not the evidence envelope format: additive V2/shadow observations
// may coexist on the same durable legs and are preserved verbatim (never
// stripped, never relabelled), but only the original V1 scalar Evidence
// selects money via rateCustomerCharge. The tariff must be empty (legacy
// callers without tariff material) or explicitly legacy-mapped; a V2 owner
// or V2-native tariff fails closed here. Exact settled charges for
// historical money are preserved; divergent additive V2 capture leaves the
// V1 charge stable.
func rateV1DrainCall(call CallUsageRecord, sealedLegs []CallLegUsageRecord, in CallRatingInput, legFingerprints []string) (CallRatingResult, error) {
	if trimmed := strings.TrimSpace(in.PostingOwner); trimmed != "" && trimmed != PostingOwnerV1 {
		return CallRatingResult{CallID: call.CallID}, fmt.Errorf("%w: V1 drain rating rejects owner %q", ErrPostingOwnershipInvalid, in.PostingOwner)
	}
	if in.CustomerTariff.Ref.ID != "" && !isLegacyScalarTariff(in.CustomerTariff) {
		return CallRatingResult{CallID: call.CallID}, fmt.Errorf("%w: V1 drain requires a legacy scalar tariff", ErrRatingSnapshotMismatch)
	}
	if err := in.CustomerPricing.Validate(in.MaxCustomerCharge.Currency); err != nil {
		return CallRatingResult{}, err
	}
	customer, err := rateCustomerCharge(sealedLegs, call.Outcome, in.CustomerPricing, in.CustomerPolicy, in.ModelPricing)
	if err != nil {
		return CallRatingResult{}, err
	}
	fp, err := callRatingFingerprint(call, customer, in.MaxCustomerCharge, legFingerprints)
	if err != nil {
		return CallRatingResult{}, err
	}
	return CallRatingResult{CallID: call.CallID, CustomerCharge: customer, Fingerprint: fp}, nil
}

func isLegacyScalarTariff(tariff economics.TariffSnapshot) bool {
	return tariff.Ref.RaterID == LegacyScalarRaterID && tariff.LegacySemantics == LegacyScalarSemantics
}

func hasV2RetailQuantityEvidence(legs []CallLegUsageRecord) bool {
	for _, leg := range legs {
		if leg.EvidenceVersion < EvidenceFormatVersionV2 {
			continue
		}
		if slices.ContainsFunc(leg.Observations, isRetailBLegObservation) {
			return true
		}
	}
	return false
}

func containsExpectedLeg(ids []string, id string) bool {
	id = strings.TrimSpace(id)
	for _, candidate := range ids {
		if strings.TrimSpace(candidate) == id {
			return true
		}
	}
	return false
}

func callRatingFingerprint(call CallUsageRecord, amount Money, max Money, legFingerprints []string) (string, error) {
	sorted := append([]string(nil), legFingerprints...)
	sort.Strings(sorted)
	return fmt.Sprintf(
		"call-rating:v2:%s:%d:%s:max=%d:%s:pricing=%s@%s:policy=%s@%s:legs=%s",
		call.CallID.String(),
		amount.Nano,
		amount.Currency,
		max.Nano,
		max.Currency,
		call.CustomerPricingRef.ID,
		call.CustomerPricingRef.Version,
		call.ChargePolicyRef.ID,
		call.ChargePolicyRef.Version,
		strings.Join(sorted, ","),
	), nil
}
