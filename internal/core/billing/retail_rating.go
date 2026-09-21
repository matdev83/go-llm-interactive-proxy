package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	// RetailChargeKindInferenceUsage identifies lines priced from the frozen
	// policy-selected B-leg quantity set.
	RetailChargeKindInferenceUsage = "inference_usage"
	// RetailChargeKindCommercialFee identifies fixed customer charges whose
	// trusted scope is call or submission rather than a B-leg quantity.
	RetailChargeKindCommercialFee = "commercial_fee"
	// RetailChargeKindProxyService identifies an explicit customer-boundary
	// service meter. It is never a substitute for inference usage.
	RetailChargeKindProxyService = "proxy_service"
)

var (
	ErrRetailRatingInvalid             = errors.New("billing: invalid retail rating input")
	ErrRetailRateIncomplete            = errors.New("billing: retail rating is incomplete")
	ErrRetailSubmissionIdentityMissing = errors.New("billing: trusted submission identity is required")
	ErrRetailFixedScopeUnsupported     = errors.New("billing: retail fixed-fee scope is unsupported")
	ErrRetailProxyScopeMismatch        = errors.New("billing: proxy-service observation is outside customer boundary scope")
)

// RetailProxyServiceInput is an explicit customer-boundary service charge
// input. These observations remain separate from policy-selected B-leg
// inference quantities and are rated with their own customer tariff.
type RetailProxyServiceInput struct {
	Tariff          economics.TariffSnapshot
	Observations    []metering.Observation
	ObservationRefs []metering.ObservationRef
	Qualifiers      []metering.Dimension
	Scope           string
}

// RetailRatingInput binds one frozen B-leg selection to an independent
// customer tariff. ModelTariffs, when present, are route-specific customer
// tariffs; they never contain or resolve supplier rates.
type RetailRatingInput struct {
	Call                 CallUsageRecord
	Legs                 []CallLegUsageRecord
	Selection            RetailSelectionResult
	Policy               ChargePolicy
	Tariff               economics.TariffSnapshot
	ModelTariffs         []ModelCustomerTariff
	Qualifiers           []metering.Dimension
	QualifierSnapshotRef *economics.SnapshotContentRef
	Payer                metering.PaymentParty
	AsOf                 time.Time
	ProxyService         *RetailProxyServiceInput
	// ProviderCost is accepted only for the explicit cost-pass-through basis.
	// Independent retail ignores supplier cost entirely and never reaches this
	// branch.
	ProviderCost *CostPassThroughProviderCost
}

// RetailRatingResult contains a customer-facing composite valuation and the
// independently retained inference, commercial, and proxy-service views.
// Supplier/COGS valuations are intentionally not embedded here.
type RetailRatingResult struct {
	CallID                BillingCallID
	SubmissionID          string
	CustomerCharge        Money
	Fingerprint           string
	Valuation             economics.Valuation
	InferenceValuation    economics.Valuation
	CommercialValuation   economics.Valuation
	ProxyServiceValuation *economics.Valuation
	CostPassThrough       *CostPassThroughSettlement
}

// RateSelectedRetailBLegs rates only the immutable B-leg references carried
// by RetailSelectionResult. Quantity lines are evaluated in one pass per
// selected tariff; call and submission fees are evaluated once at their own
// trusted scope. A partial valuation is returned with a typed error whenever
// a required rate, qualifier, quantity, or identity is unavailable.
func RateSelectedRetailBLegs(ctx context.Context, in RetailRatingInput) (RetailRatingResult, error) {
	var result RetailRatingResult
	if err := retailContextErr(ctx); err != nil {
		return result, err
	}
	call, err := in.Call.Seal()
	if err != nil {
		return result, fmt.Errorf("%w: call: %v", ErrRetailRatingInvalid, err)
	}
	result.CallID = call.CallID
	result.SubmissionID = call.SubmissionID
	if err := validateRetailRatingInput(in, call); err != nil {
		return result, err
	}
	policy := in.Policy.Clone()
	retailPolicy, err := ResolveRetailSelectionPolicy(policy)
	if err != nil {
		return result, fmt.Errorf("%w: policy: %v", ErrRetailRatingInvalid, err)
	}
	selection := in.Selection.Clone()
	if selection.CallID == "" {
		selection, err = SelectRetailBLegEvidence(RetailSelectionInput{Call: call, Legs: in.Legs, Policy: policy})
		if err != nil {
			return result, err
		}
	}
	if err := validateFrozenRetailSelection(selection, call, policy, retailPolicy); err != nil {
		return result, err
	}
	sealedLegs, legsByID, err := sealRetailRatingLegs(call, in.Legs)
	if err != nil {
		return result, err
	}
	observations, refs, storeID, err := selectedRetailObservations(selection, call, legsByID)
	if err != nil {
		return result, err
	}
	if retailPolicy.Basis == RetailBasisCostPassThrough {
		return rateCostPassThrough(ctx, call, policy, retailPolicy, selection, in.ProviderCost, refs)
	}
	baseTariff, err := in.Tariff.Canonical()
	if err != nil {
		return result, fmt.Errorf("%w: customer tariff: %v", ErrRetailRatingInvalid, err)
	}
	if err := validateRetailTariffBinding(baseTariff, policy); err != nil {
		return result, err
	}
	modelTariffs, err := canonicalModelRetailTariffs(in.ModelTariffs, baseTariff, selectedRetailRoutes(observations, sealedLegs))
	if err != nil {
		return result, err
	}
	if err := validateRetailFixedScopes(baseTariff, call); err != nil {
		return result, err
	}

	groups, err := retailQuantityGroups(observations, refs, sealedLegs, modelTariffs, baseTariff)
	if err != nil {
		return result, err
	}
	var ratingErrs []error
	for _, group := range groups {
		input := retailEconomicsInput(call, policy, group.tariff, group.observations, group.refs, in.Qualifiers, in.QualifierSnapshotRef, in.Payer, in.AsOf, "call:"+call.CallID.String(), metering.SubjectBillingCall)
		valuation, rateErr := rateRetailValuation(ctx, group.tariff, input, isRetailQuantityObservation, false, "")
		markRetailLines(&valuation, RetailChargeKindInferenceUsage)
		if valuation.ID != "" {
			combineRetailValuation(&result.InferenceValuation, valuation)
		}
		if rateErr != nil {
			ratingErrs = append(ratingErrs, fmt.Errorf("%s: %w", RetailChargeKindInferenceUsage, rateErr))
		}
	}

	// Fixed fees are evaluated from refs only, once for each declared trusted
	// scope. No selected B-leg loop surrounds these calls.
	for _, scope := range []economics.FixedFeeScope{economics.FixedFeeScopeCall, economics.FixedFeeScopeSubmission} {
		if !retailTariffHasFixedScope(baseTariff, scope) {
			continue
		}
		feeSubject := metering.SubjectBillingCall
		feeScope := "call:" + call.CallID.String()
		if scope == economics.FixedFeeScopeSubmission {
			if call.SubmissionID == "" {
				return result, ErrRetailSubmissionIdentityMissing
			}
			feeSubject = metering.SubjectSubmission
			feeScope = "submission:" + call.SubmissionID
		}
		input := retailEconomicsInput(call, policy, baseTariff, nil, refs, in.Qualifiers, in.QualifierSnapshotRef, in.Payer, in.AsOf, feeScope, feeSubject)
		valuation, rateErr := rateRetailValuation(ctx, baseTariff, input, nil, true, string(scope))
		markRetailLines(&valuation, RetailChargeKindCommercialFee)
		if valuation.ID != "" {
			combineRetailValuation(&result.CommercialValuation, valuation)
		}
		if rateErr != nil {
			ratingErrs = append(ratingErrs, fmt.Errorf("%s fee: %w", scope, rateErr))
		}
	}

	if in.ProxyService != nil {
		proxyValuation, proxyErr := rateRetailProxyService(ctx, call, policy, baseTariff.Currency, storeID, in.Payer, in.AsOf, in.ProxyService)
		if proxyValuation.ID != "" {
			markRetailLines(&proxyValuation, RetailChargeKindProxyService)
			proxyCopy := proxyValuation.Clone()
			result.ProxyServiceValuation = &proxyCopy
		}
		if proxyErr != nil {
			ratingErrs = append(ratingErrs, fmt.Errorf("%s: %w", RetailChargeKindProxyService, proxyErr))
		}
	}

	composeRetailValuation(&result, call, refs, baseTariff, policy, storeID)
	if len(ratingErrs) != 0 {
		ratingErrs = append(ratingErrs, ErrRetailRateIncomplete)
		return result, errors.Join(ratingErrs...)
	}
	if result.Valuation.ID == "" {
		return result, fmt.Errorf("%w: no payable retail lines", ErrRetailRateIncomplete)
	}
	if err := result.Valuation.Validate(); err != nil {
		return result, fmt.Errorf("%w: composite valuation: %v", ErrRetailRateIncomplete, err)
	}
	charge, err := retailValuationMoney(result.Valuation)
	if err != nil {
		return result, fmt.Errorf("%w: summary: %v", ErrRetailRateIncomplete, err)
	}
	result.CustomerCharge = charge
	result.Fingerprint = result.Valuation.Fingerprint()
	return result, nil
}

func rateCostPassThrough(ctx context.Context, call CallUsageRecord, policy ChargePolicy, retail RetailSelectionPolicy, selection RetailSelectionResult, provider *CostPassThroughProviderCost, refs []metering.ObservationRef) (RetailRatingResult, error) {
	var result RetailRatingResult
	if err := retailContextErr(ctx); err != nil {
		return result, err
	}
	if retail.CostPassThrough == nil {
		return result, fmt.Errorf("%w: explicit cost-pass-through policy is required", ErrCostPassThroughPolicyInvalid)
	}
	passPolicy := retail.CostPassThrough.Clone()
	if err := passPolicy.Validate(); err != nil {
		return result, err
	}
	bound := *passPolicy.SafeBound
	amount := Money{Nano: 0, Currency: bound.Currency}
	status := CostPassThroughSettlementPending
	var accepted *CostPassThroughProviderCost
	if provider != nil {
		candidate := provider.Clone()
		candidate.LURKey = strings.TrimSpace(candidate.LURKey)
		candidate.ValuationID = strings.TrimSpace(candidate.ValuationID)
		if err := candidate.Validate(bound.Currency); err != nil {
			return result, err
		}
		if candidate.Amount.Nano > bound.Nano {
			return result, fmt.Errorf("%w: %d is greater than %d", ErrCostPassThroughBoundExceeded, candidate.Amount.Nano, bound.Nano)
		}
		amount = candidate.Amount
		status = CostPassThroughSettlementFinal
		accepted = &candidate
	} else if passPolicy.MissingCost == CostPassThroughMissingCostProvisional {
		amount = bound
		if passPolicy.AllowLateAdjustment {
			status = CostPassThroughSettlementProvisional
		} else {
			// A provisional amount without a permitted adjustment is a frozen
			// final contract, not an implicitly revisable estimate.
			status = CostPassThroughSettlementFinal
		}
	}
	state := CostPassThroughSettlement{
		PolicyRef: policy.Ref, Policy: passPolicy, Status: status,
		SafeBound: bound, PostedAmount: amount, ProviderCost: accepted,
	}
	if err := state.Validate(bound.Currency); err != nil {
		return result, err
	}
	fingerprint, err := costPassThroughRatingFingerprint(call, policy, retail, selection, refs, state)
	if err != nil {
		return result, err
	}
	stateCopy := state.Clone()
	result.CallID = call.CallID
	result.SubmissionID = call.SubmissionID
	result.CustomerCharge = amount
	result.Fingerprint = fingerprint
	result.CostPassThrough = &stateCopy
	return result, nil
}

func costPassThroughRatingFingerprint(call CallUsageRecord, policy ChargePolicy, retail RetailSelectionPolicy, selection RetailSelectionResult, refs []metering.ObservationRef, state CostPassThroughSettlement) (string, error) {
	stateFingerprint, err := state.SemanticFingerprint()
	if err != nil {
		return "", err
	}
	selected := make([]string, 0, len(selection.SelectedBLegs))
	for _, leg := range selection.SelectedBLegs {
		selected = append(selected, leg.BLegID+":"+string(leg.Outcome))
	}
	sort.Strings(selected)
	refIDs := make([]string, 0, len(refs))
	for _, ref := range refs {
		refIDs = append(refIDs, retailObservationRefIdentity(ref))
	}
	sort.Strings(refIDs)
	payload, err := json.Marshal(struct {
		Version      string
		CallID       BillingCallID
		PolicyRef    VersionRef
		Basis        RetailCommercialBasis
		Mode         RetailSelectionMode
		Selected     []string
		Observations []string
		State        string
	}{
		Version: "cost-pass-through-rating:v1", CallID: call.CallID, PolicyRef: policy.Ref,
		Basis: retail.Basis, Mode: retail.Mode, Selected: selected, Observations: refIDs, State: stateFingerprint,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "cost-pass-through-rating:v1:" + hex.EncodeToString(digest[:]), nil
}

func validateRetailRatingInput(in RetailRatingInput, call CallUsageRecord) error {
	if err := in.Policy.Validate(); err != nil {
		return fmt.Errorf("%w: policy: %w", ErrRetailRatingInvalid, err)
	}
	if call.CustomerPricingRef != in.Policy.PricingRef || call.ChargePolicyRef != in.Policy.Ref {
		return fmt.Errorf("%w: call and policy snapshots differ", ErrRetailRatingInvalid)
	}
	if err := in.Payer.Validate(); err != nil {
		return fmt.Errorf("%w: payer: %v", ErrRetailRatingInvalid, err)
	}
	if err := validateRetailDimensions(in.Qualifiers); err != nil {
		return fmt.Errorf("%w: qualifiers: %v", ErrRetailRatingInvalid, err)
	}
	return nil
}

func validateRetailDimensions(dimensions []metering.Dimension) error {
	seen := make(map[string]struct{}, len(dimensions))
	for i, dimension := range dimensions {
		if err := dimension.Validate(); err != nil {
			return fmt.Errorf("qualifier %d: %v", i, err)
		}
		if _, exists := seen[dimension.Name]; exists {
			return fmt.Errorf("duplicate qualifier %q", dimension.Name)
		}
		seen[dimension.Name] = struct{}{}
	}
	return nil
}

func validateRetailTariffBinding(tariff economics.TariffSnapshot, policy ChargePolicy) error {
	if tariff.Ref.ID != policy.PricingRef.ID || tariff.Ref.Version != policy.PricingRef.Version {
		return fmt.Errorf("%w: tariff %s@%s does not match pricing snapshot %s@%s", ErrRatingSnapshotMismatch, tariff.Ref.ID, tariff.Ref.Version, policy.PricingRef.ID, policy.PricingRef.Version)
	}
	return nil
}

func validateFrozenRetailSelection(selection RetailSelectionResult, call CallUsageRecord, policy ChargePolicy, retail RetailSelectionPolicy) error {
	if selection.CallID != call.CallID || selection.PolicyRef != policy.Ref {
		return fmt.Errorf("%w: selection call/policy identity differs", ErrRetailSelectionScopeMismatch)
	}
	if selection.Completeness != RetailSelectionComplete || selection.Capability != RetailCapabilityV2BLegObservations {
		return fmt.Errorf("%w: selection is not complete V2 B-leg evidence", ErrRetailSelectionIncomplete)
	}
	if selection.Mode != retail.Mode || selection.Basis != retail.Basis {
		return fmt.Errorf("%w: selection policy differs from frozen customer policy", ErrRetailSelectionScopeMismatch)
	}
	if len(selection.SelectedBLegs) == 0 || len(selection.ObservationRefs) == 0 {
		return ErrRetailSelectionEmpty
	}
	seen := make(map[string]struct{}, len(selection.ObservationRefs))
	for _, ref := range selection.ObservationRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: selection reference: %v", ErrRetailSelectionIncomplete, err)
		}
		key := retailObservationRefIdentity(ref)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: %s", ErrRetailSelectionDuplicate, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func sealRetailRatingLegs(call CallUsageRecord, legs []CallLegUsageRecord) ([]CallLegUsageRecord, map[string]CallLegUsageRecord, error) {
	sealed := make([]CallLegUsageRecord, 0, len(legs))
	byID := make(map[string]CallLegUsageRecord, len(legs))
	for _, source := range legs {
		leg, err := source.Seal()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: leg: %v", ErrRetailRatingInvalid, err)
		}
		if leg.CallID != call.CallID || !containsExpectedLeg(call.ExpectedBLegIDs, leg.BLegID) || leg.ALegID != call.ALegID {
			return nil, nil, fmt.Errorf("%w: leg %q is outside call scope", ErrRetailSelectionScopeMismatch, leg.BLegID)
		}
		if leg.SubmissionID != "" && leg.SubmissionID != call.SubmissionID {
			return nil, nil, fmt.Errorf("%w: leg %q submission identity differs", ErrRetailSelectionScopeMismatch, leg.BLegID)
		}
		if _, exists := byID[leg.BLegID]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate B-leg %q", ErrRetailSelectionDuplicate, leg.BLegID)
		}
		sealed = append(sealed, leg)
		byID[leg.BLegID] = leg
	}
	return sealed, byID, nil
}

func selectedRetailObservations(selection RetailSelectionResult, call CallUsageRecord, legs map[string]CallLegUsageRecord) ([]metering.Observation, []metering.ObservationRef, string, error) {
	observations := make([]metering.Observation, 0, len(selection.ObservationRefs))
	refs := make([]metering.ObservationRef, 0, len(selection.ObservationRefs))
	seen := make(map[string]struct{}, len(selection.ObservationRefs))
	stores := make(map[string]struct{})
	for _, selected := range selection.SelectedBLegs {
		if selected.CallID != call.CallID {
			return nil, nil, "", fmt.Errorf("%w: selected leg %q call mismatch", ErrRetailSelectionScopeMismatch, selected.BLegID)
		}
		leg, ok := legs[selected.BLegID]
		if !ok {
			return nil, nil, "", fmt.Errorf("%w: selected leg %q is not durable", ErrRetailSelectionIncomplete, selected.BLegID)
		}
		if selected.ALegID != leg.ALegID || selected.AttemptSeq != leg.AttemptSeq || selected.Outcome != leg.Outcome || selected.Surfaced != leg.Surfaced {
			return nil, nil, "", fmt.Errorf("%w: selected leg %q outcome identity differs", ErrRetailSelectionUntrusted, selected.BLegID)
		}
		for _, wanted := range selected.ObservationRefs {
			key := retailObservationRefIdentity(wanted)
			if _, exists := seen[key]; exists {
				return nil, nil, "", fmt.Errorf("%w: selected observation %s", ErrRetailSelectionDuplicate, wanted.ObservationID)
			}
			var found *metering.Observation
			for i := range leg.Observations {
				candidate := leg.Observations[i]
				ref, err := candidate.Ref(candidate.Subject.StoreID)
				if err != nil {
					return nil, nil, "", fmt.Errorf("%w: observation %q reference: %v", ErrRetailSelectionIncomplete, candidate.ID, err)
				}
				if ref.Equal(wanted) {
					copy := candidate.Clone()
					found = &copy
					break
				}
			}
			if found == nil {
				return nil, nil, "", fmt.Errorf("%w: selected observation %q is not loaded", ErrRetailSelectionIncomplete, wanted.ObservationID)
			}
			if !isRetailBLegObservation(*found) {
				return nil, nil, "", fmt.Errorf("%w: selected observation %q is not a backend B-leg quantity", ErrRetailSelectionScopeMismatch, found.ID)
			}
			seen[key] = struct{}{}
			stores[found.Subject.StoreID] = struct{}{}
			observations = append(observations, *found)
			refs = append(refs, wanted)
		}
	}
	if len(refs) != len(selection.ObservationRefs) {
		return nil, nil, "", fmt.Errorf("%w: selection references do not match selected B-legs", ErrRetailSelectionIncomplete)
	}
	for _, ref := range selection.ObservationRefs {
		if _, exists := seen[retailObservationRefIdentity(ref)]; !exists {
			return nil, nil, "", fmt.Errorf("%w: selection reference %q is not owned by a selected B-leg", ErrRetailSelectionScopeMismatch, ref.ObservationID)
		}
	}
	if len(stores) != 1 {
		return nil, nil, "", fmt.Errorf("%w: selected observations must share one store", ErrRetailSelectionScopeMismatch)
	}
	var storeID string
	for store := range stores {
		storeID = store
	}
	sort.Slice(refs, func(i, j int) bool {
		return retailObservationRefIdentity(refs[i]) < retailObservationRefIdentity(refs[j])
	})
	sort.Slice(observations, func(i, j int) bool {
		return retailObservationRefIdentityMust(observations[i]) < retailObservationRefIdentityMust(observations[j])
	})
	return observations, refs, storeID, nil
}

func retailObservationRefIdentity(ref metering.ObservationRef) string {
	return fmt.Sprintf("%s\x00%s\x00%020d\x00%s", ref.StoreID, ref.ObservationID, ref.Revision, ref.PayloadHash)
}

func retailObservationRefIdentityMust(observation metering.Observation) string {
	ref, err := observation.Ref(observation.Subject.StoreID)
	if err != nil {
		return observation.ID
	}
	return retailObservationRefIdentity(ref)
}

func canonicalModelRetailTariffs(cards []ModelCustomerTariff, defaultTariff economics.TariffSnapshot, selectedRoutes map[string]struct{}) ([]ModelCustomerTariff, error) {
	if len(cards) == 0 {
		return nil, nil
	}
	result := make([]ModelCustomerTariff, 0, len(cards))
	seen := make(map[string]struct{}, len(cards))
	for _, card := range cards {
		backendID := strings.TrimSpace(card.BackendID)
		modelID := strings.TrimSpace(card.ModelID)
		routeKey := backendID + "\x00" + modelID
		if len(selectedRoutes) != 0 {
			if _, selected := selectedRoutes[routeKey]; !selected {
				continue
			}
		}
		if backendID == "" || modelID == "" {
			return nil, fmt.Errorf("%w: model customer tariff route identity is required", ErrRetailRatingInvalid)
		}
		tariff, err := card.Tariff.Canonical()
		if err != nil {
			return nil, fmt.Errorf("%w: model customer tariff: %v", ErrRetailRatingInvalid, err)
		}
		if tariff.Currency != defaultTariff.Currency {
			return nil, ErrRatingCurrencyMismatch
		}
		key := routeKey
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: duplicate model customer tariff %s", ErrRetailRatingInvalid, key)
		}
		seen[key] = struct{}{}
		result = append(result, ModelCustomerTariff{BackendID: backendID, ModelID: modelID, Tariff: tariff})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].BackendID != result[j].BackendID {
			return result[i].BackendID < result[j].BackendID
		}
		return result[i].ModelID < result[j].ModelID
	})
	return result, nil
}

func selectedRetailRoutes(observations []metering.Observation, legs []CallLegUsageRecord) map[string]struct{} {
	selectedBLegs := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		selectedBLegs[observation.Subject.BLegID] = struct{}{}
	}
	routes := make(map[string]struct{})
	for _, leg := range legs {
		if _, selected := selectedBLegs[leg.BLegID]; !selected {
			continue
		}
		routes[strings.TrimSpace(leg.BackendID)+"\x00"+strings.TrimSpace(leg.ModelID)] = struct{}{}
	}
	return routes
}

type retailQuantityGroup struct {
	tariff       economics.TariffSnapshot
	observations []metering.Observation
	refs         []metering.ObservationRef
}

func retailQuantityGroups(observations []metering.Observation, refs []metering.ObservationRef, legs []CallLegUsageRecord, cards []ModelCustomerTariff, defaultTariff economics.TariffSnapshot) ([]retailQuantityGroup, error) {
	selectedBLegs := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		selectedBLegs[observation.Subject.BLegID] = struct{}{}
	}
	legTariffs := make(map[string]economics.TariffSnapshot, len(legs))
	for _, leg := range legs {
		if _, selected := selectedBLegs[leg.BLegID]; !selected {
			continue
		}
		tariff := defaultTariff
		if len(cards) != 0 {
			found := false
			for _, card := range cards {
				if card.BackendID == leg.BackendID && card.ModelID == leg.ModelID {
					tariff = card.Tariff
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("%w: customer tariff for %s/%s", ErrRetailRateIncomplete, leg.BackendID, leg.ModelID)
			}
		}
		legTariffs[leg.BLegID] = tariff
	}
	groupsByKey := make(map[string]*retailQuantityGroup)
	for _, observation := range observations {
		tariff, ok := legTariffs[observation.Subject.BLegID]
		if !ok {
			return nil, fmt.Errorf("%w: selected observation %q has no B-leg tariff", ErrRetailRateIncomplete, observation.ID)
		}
		key := tariff.Ref.ID + "\x00" + tariff.Ref.Version + "\x00" + tariff.ContentHash()
		group := groupsByKey[key]
		if group == nil {
			group = &retailQuantityGroup{tariff: tariff}
			groupsByKey[key] = group
		}
		ref, err := observation.Ref(observation.Subject.StoreID)
		if err != nil {
			return nil, fmt.Errorf("%w: observation %q reference: %v", ErrRetailRateIncomplete, observation.ID, err)
		}
		group.observations = append(group.observations, observation)
		group.refs = append(group.refs, ref)
	}
	// Keep refs parameter authoritative for deterministic selection identity and
	// ensure no observation outside the frozen selection entered a group.
	if len(refs) != len(observations) {
		return nil, fmt.Errorf("%w: selected quantity reference count differs from evidence", ErrRetailSelectionIncomplete)
	}
	keys := make([]string, 0, len(groupsByKey))
	for key := range groupsByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	groups := make([]retailQuantityGroup, 0, len(keys))
	for _, key := range keys {
		group := *groupsByKey[key]
		sort.Slice(group.observations, func(i, j int) bool {
			return retailObservationRefIdentityMust(group.observations[i]) < retailObservationRefIdentityMust(group.observations[j])
		})
		sort.Slice(group.refs, func(i, j int) bool {
			return retailObservationRefIdentity(group.refs[i]) < retailObservationRefIdentity(group.refs[j])
		})
		groups = append(groups, group)
	}
	return groups, nil
}

func retailEconomicsInput(call CallUsageRecord, policy ChargePolicy, tariff economics.TariffSnapshot, observations []metering.Observation, refs []metering.ObservationRef, qualifiers []metering.Dimension, qualifierRef *economics.SnapshotContentRef, payer metering.PaymentParty, asOf time.Time, scope string, subjectKind metering.SubjectKind) economics.PostUsageRatingInput {
	if asOf.IsZero() {
		asOf = call.FinishedAt
	}
	storeID := ""
	if len(observations) != 0 {
		storeID = observations[0].Subject.StoreID
	} else if len(refs) != 0 {
		storeID = refs[0].StoreID
	}
	subject := metering.SubjectRef{Kind: subjectKind, StoreID: storeID}
	if subjectKind == metering.SubjectSubmission {
		subject.SubmissionID = call.SubmissionID
	} else {
		subject.BillingCallID = call.CallID.String()
	}
	policyRef := economics.PolicySnapshotRef{
		VersionRef: economics.VersionRef{ID: policy.Ref.ID, Version: policy.Ref.Version, EffectiveAt: policy.Ref.EffectiveAt, FetchedAt: policy.Ref.FetchedAt},
		PolicyID:   policy.Ref.ID,
	}
	policyContent := retailPolicyContent(policy)
	if qualifierRef == nil {
		qualifierRef = retailQualifierContent(tariff, qualifiers)
	}
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisCustomerPolicy,
		Subject: subject, Scope: scope, Payer: payer, EffectiveQualifiers: append([]metering.Dimension(nil), qualifiers...),
		Rater: tariff.Ref, Tariff: tariff.Ref, Policy: policyRef,
		RaterContent: cloneRetailContent(&tariff.Content), TariffContent: cloneRetailContent(&tariff.Content),
		PolicyContent: policyContent, QualifierSnapshotRef: cloneRetailContent(qualifierRef), AsOf: asOf,
	}
	if len(observations) != 0 {
		input.Observations = make([]metering.Observation, len(observations))
		for i, observation := range observations {
			input.Observations[i] = observation.Clone()
		}
	} else {
		input.ObservationRefs = append([]metering.ObservationRef(nil), refs...)
	}
	return input
}

func cloneRetailContent(in *economics.SnapshotContentRef) *economics.SnapshotContentRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func retailPolicyContent(policy ChargePolicy) *economics.SnapshotContentRef {
	retail, _ := ResolveRetailSelectionPolicy(policy)
	parts := []string{
		policy.Ref.ID, policy.Ref.Version, string(policy.Scope),
		fmt.Sprint(policy.IncludeInputTokens), fmt.Sprint(policy.IncludeOutputTokens),
		fmt.Sprint(policy.IncludeFixedCharges), fmt.Sprint(policy.IncludeResourceCharges),
		string(retail.Mode), string(retail.Basis),
	}
	for _, outcome := range retail.OutcomeSubset {
		parts = append(parts, string(outcome))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return &economics.SnapshotContentRef{
		ContentRef:  "retail-policy:v1://" + policy.Ref.ID + "/" + policy.Ref.Version,
		ContentHash: hex.EncodeToString(sum[:]),
	}
}

func retailQualifierContent(tariff economics.TariffSnapshot, qualifiers []metering.Dimension) *economics.SnapshotContentRef {
	parts := []string{tariff.Ref.ID, tariff.Ref.Version, tariff.ContentHash()}
	canonical := append([]metering.Dimension(nil), qualifiers...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Name != canonical[j].Name {
			return canonical[i].Name < canonical[j].Name
		}
		return canonical[i].Value < canonical[j].Value
	})
	for _, qualifier := range canonical {
		parts = append(parts, qualifier.Name, qualifier.Value)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return &economics.SnapshotContentRef{
		ContentRef:  "retail-qualifiers:v1://" + tariff.Ref.ID + "/" + tariff.Ref.Version,
		ContentHash: hex.EncodeToString(sum[:]),
	}
}

func rateRetailValuation(ctx context.Context, tariff economics.TariffSnapshot, input economics.PostUsageRatingInput, predicate func(metering.Observation) bool, includeFixed bool, fixedScope string) (economics.Valuation, error) {
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		return economics.Valuation{}, err
	}
	if err := retailContextErr(ctx); err != nil {
		return economics.Valuation{}, err
	}
	if err := input.Validate(); err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: %v", ErrRetailRatingInvalid, err)
	}
	if err := rater.validateSnapshotBinding(input); err != nil {
		return economics.Valuation{}, err
	}
	var incompleteRefs []metering.ObservationRef
	if input.Basis == economics.BasisCustomerPolicy && predicate != nil && len(input.Observations) != 0 {
		for _, observation := range input.Observations {
			if isRetailBLegObservation(observation) && !isRetailQuantityObservation(observation) {
				ref, refErr := observation.Ref(observation.Subject.StoreID)
				if refErr != nil {
					return economics.Valuation{}, refErr
				}
				incompleteRefs = appendUniqueRetailRefs(incompleteRefs, ref)
			}
		}
	}
	if input.Basis == economics.BasisCustomerPolicy && predicate != nil {
		// Keep all B-leg envelopes in the immutable input set, including
		// charge-only/unavailable observations. The rating predicate below
		// excludes those from arithmetic, while the retained refs let the
		// result report a typed incomplete valuation instead of silently
		// treating a missing quantity as zero.
		input = inputForPlane(input, func(observation metering.Observation) bool {
			return isRetailBLegObservation(observation) || predicate(observation)
		})
	}
	input, err = canonicalizeRatingInput(input)
	if err != nil {
		return economics.Valuation{}, err
	}
	refs, err := inputObservationRefs(input)
	if err != nil {
		return economics.Valuation{}, err
	}
	input.InputSetHash, err = verifyInputSetHash(input.Basis, input.InputSetHash, refs)
	if err != nil {
		return economics.Valuation{}, err
	}
	qualifiers, err := effectiveQualifiers(rater.snapshot.EffectiveQualifiers, input.EffectiveQualifiers)
	if err != nil {
		return economics.Valuation{}, err
	}
	valuation := newValuation(input, rater.snapshot, refs, qualifiers)
	if includeFixed && fixedScope != "" {
		valuation, err = rater.rateMeasuresForFixedScope(input, valuation, fixedScope)
	} else if includeFixed {
		valuation, err = rater.rateMeasures(input, valuation)
	} else {
		valuation, err = rater.rateMeasuresWithPredicate(input, valuation, false, predicate)
	}
	if len(incompleteRefs) != 0 {
		valuation.Completeness = economics.CompletenessPartial
		valuation.MissingObservations = appendUniqueRetailRefs(valuation.MissingObservations, incompleteRefs...)
		if err == nil {
			err = fmt.Errorf("%w: selected B-leg quantity is unavailable", ErrQuantityIncomplete)
		}
	}
	return valuation, err
}

func isRetailProxyServiceObservation(observation metering.Observation) bool {
	if observation.Origin != metering.OriginLocal || observation.Perspective != metering.PerspectiveCustomer {
		return false
	}
	if observation.Boundary != metering.BoundaryFrontendIngress && observation.Boundary != metering.BoundaryFrontendEgress {
		return false
	}
	if observation.Lifecycle == metering.LifecycleBackendAttempt || len(observation.Measures) == 0 {
		return false
	}
	if observation.Subject.BLegID != "" || observation.Correlation.BLegID != "" {
		return false
	}
	return true
}

func rateRetailProxyService(ctx context.Context, call CallUsageRecord, policy ChargePolicy, baseCurrency, storeID string, payer metering.PaymentParty, asOf time.Time, in *RetailProxyServiceInput) (economics.Valuation, error) {
	if in == nil {
		return economics.Valuation{}, nil
	}
	tariff, err := in.Tariff.Canonical()
	if err != nil {
		return economics.Valuation{}, fmt.Errorf("%w: proxy tariff: %v", ErrRetailRatingInvalid, err)
	}
	if tariff.Currency == "" {
		return economics.Valuation{}, fmt.Errorf("%w: proxy tariff currency is required", ErrRetailRatingInvalid)
	}
	if baseCurrency != "" && tariff.Currency != baseCurrency {
		return economics.Valuation{}, fmt.Errorf("%w: proxy tariff currency %q differs from retail currency %q", ErrRatingCurrencyMismatch, tariff.Currency, baseCurrency)
	}
	observations := make([]metering.Observation, 0, len(in.Observations))
	refs := append([]metering.ObservationRef(nil), in.ObservationRefs...)
	providedRefs := make(map[string]metering.ObservationRef, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return economics.Valuation{}, fmt.Errorf("%w: proxy observation ref: %v", ErrRetailRateIncomplete, err)
		}
		key := retailObservationRefIdentity(ref)
		if _, exists := providedRefs[key]; exists {
			return economics.Valuation{}, fmt.Errorf("%w: duplicate proxy observation ref %q", ErrRetailSelectionDuplicate, ref.ObservationID)
		}
		providedRefs[key] = ref
	}
	payloadRefs := make(map[string]struct{}, len(in.Observations))
	for _, source := range in.Observations {
		observation := source.Clone()
		if err := observation.Validate(); err != nil {
			return economics.Valuation{}, fmt.Errorf("%w: proxy observation: %v", ErrRetailRateIncomplete, err)
		}
		if !isRetailProxyServiceObservation(observation) {
			return economics.Valuation{}, fmt.Errorf("%w: observation %q", ErrRetailProxyScopeMismatch, observation.ID)
		}
		if observation.Subject.StoreID != storeID {
			return economics.Valuation{}, fmt.Errorf("%w: observation %q store differs", ErrRetailProxyScopeMismatch, observation.ID)
		}
		if !retailObservationCallMatches(observation, call.CallID) {
			return economics.Valuation{}, fmt.Errorf("%w: observation %q call differs", ErrRetailProxyScopeMismatch, observation.ID)
		}
		ref, err := observation.Ref(observation.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, fmt.Errorf("%w: proxy observation ref: %v", ErrRetailRateIncomplete, err)
		}
		key := retailObservationRefIdentity(ref)
		if _, exists := payloadRefs[key]; exists {
			return economics.Valuation{}, fmt.Errorf("%w: duplicate proxy observation %q", ErrRetailSelectionDuplicate, observation.ID)
		}
		payloadRefs[key] = struct{}{}
		observations = append(observations, observation)
	}
	if len(observations) != 0 {
		for key, supplied := range providedRefs {
			if _, exists := payloadRefs[key]; !exists {
				return economics.Valuation{}, fmt.Errorf("%w: proxy observation ref %q is not loaded", ErrRetailRateIncomplete, supplied.ObservationID)
			}
		}
		refs = refs[:0]
		for _, observation := range observations {
			ref, refErr := observation.Ref(observation.Subject.StoreID)
			if refErr != nil {
				return economics.Valuation{}, fmt.Errorf("%w: proxy observation ref: %v", ErrRetailRateIncomplete, refErr)
			}
			refs = append(refs, ref)
		}
	}
	if len(observations) == 0 && len(refs) == 0 {
		return economics.Valuation{}, fmt.Errorf("%w: proxy observations are required", ErrRetailRateIncomplete)
	}
	scope := in.Scope
	if scope == "" {
		scope = "call:" + call.CallID.String()
	}
	qualifierRef := (*economics.SnapshotContentRef)(nil)
	input := retailEconomicsInput(call, policy, tariff, observations, refs, in.Qualifiers, qualifierRef, payer, asOf, scope, metering.SubjectBillingCall)
	return rateRetailValuation(ctx, tariff, input, isRetailProxyServiceObservation, false, "")
}

func retailObservationCallMatches(observation metering.Observation, callID BillingCallID) bool {
	want := callID.String()
	for _, candidate := range []string{observation.Subject.BillingCallID, observation.Subject.CallID, observation.Correlation.BillingCallID, observation.Correlation.CallID} {
		if candidate != "" {
			return candidate == want
		}
	}
	return false
}

func validateRetailFixedScopes(tariff economics.TariffSnapshot, call CallUsageRecord) error {
	for _, rule := range tariff.Rules {
		if rule.FixedAmount == nil {
			continue
		}
		switch rule.FixedScope {
		case economics.FixedFeeScopeCall:
		case economics.FixedFeeScopeSubmission:
			if call.SubmissionID == "" {
				return ErrRetailSubmissionIdentityMissing
			}
		case economics.FixedFeeScopePeriod:
			return fmt.Errorf("%w: period rule %q", ErrRetailFixedScopeUnsupported, rule.ID)
		default:
			return fmt.Errorf("%w: rule %q", ErrRetailFixedScopeUnsupported, rule.ID)
		}
	}
	return nil
}

func retailTariffHasFixedScope(tariff economics.TariffSnapshot, scope economics.FixedFeeScope) bool {
	for _, rule := range tariff.Rules {
		if rule.FixedAmount != nil && rule.FixedScope == scope {
			return true
		}
	}
	return false
}

func markRetailLines(valuation *economics.Valuation, kind string) {
	if valuation == nil {
		return
	}
	for i := range valuation.Lines {
		if valuation.Lines[i].Component != nil || valuation.Lines[i].FixedFee != nil {
			valuation.Lines[i].ChargeKind = kind
		}
	}
}

func combineRetailValuation(dst *economics.Valuation, src economics.Valuation) {
	if dst == nil || src.ID == "" {
		return
	}
	if dst.ID == "" {
		*dst = src.Clone()
		return
	}
	dst.Lines = appendRetailLines(dst.Lines, src)
	dst.InputObservations = appendUniqueRetailRefs(dst.InputObservations, src.InputObservations...)
	dst.MissingObservations = appendUniqueRetailRefs(dst.MissingObservations, src.MissingObservations...)
	dst.CoverageRefs = append([]metering.ChargeCoverageRef(nil), dst.CoverageRefs...)
	for _, ref := range src.CoverageRefs {
		found := false
		for _, prior := range dst.CoverageRefs {
			if prior == ref {
				found = true
				break
			}
		}
		if !found {
			dst.CoverageRefs = append(dst.CoverageRefs, ref)
		}
	}
	if src.Completeness != economics.CompletenessComplete {
		dst.Completeness = src.Completeness
	}
	if dst.Completeness == "" {
		dst.Completeness = economics.CompletenessComplete
	}
	if currency := retailValuationCurrency(*dst); currency != "" {
		dst.Totals, _ = totalsFromLines(dst.Lines, currency)
	}
}

func cloneRetailLines(lines []economics.LineItem) []economics.LineItem {
	out := make([]economics.LineItem, len(lines))
	for i, line := range lines {
		out[i] = line.Clone()
	}
	return out
}

// appendRetailLines keeps one stable line identity per commercial context.
// The generic rater deliberately names a component line by its canonical key;
// when two selected B-legs use different customer model tariffs, those
// otherwise-equal component lines must remain distinct in the combined retail
// valuation rather than making the whole call invalid for a duplicate ID.
func appendRetailLines(dst []economics.LineItem, src economics.Valuation) []economics.LineItem {
	seen := make(map[string]struct{}, len(dst)+len(src.Lines))
	for _, line := range dst {
		seen[line.ID] = struct{}{}
	}
	for _, source := range src.Lines {
		line := source.Clone()
		if _, exists := seen[line.ID]; exists {
			base := line.ID
			suffix := scopeDigest(src.ID + "\x00" + base)
			line.ID = base + ":" + suffix
			for sequence := 2; ; sequence++ {
				if _, exists := seen[line.ID]; !exists {
					break
				}
				line.ID = fmt.Sprintf("%s:%s:%d", base, suffix, sequence)
			}
		}
		seen[line.ID] = struct{}{}
		dst = append(dst, line)
	}
	return dst
}

func appendUniqueRetailRefs(dst []metering.ObservationRef, refs ...metering.ObservationRef) []metering.ObservationRef {
	for _, ref := range refs {
		found := false
		for _, prior := range dst {
			if prior.Equal(ref) {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, ref)
		}
	}
	return dst
}

func composeRetailValuation(result *RetailRatingResult, call CallUsageRecord, refs []metering.ObservationRef, tariff economics.TariffSnapshot, policy ChargePolicy, storeID string) {
	if result == nil {
		return
	}
	var composite economics.Valuation
	combineRetailValuation(&composite, result.InferenceValuation)
	combineRetailValuation(&composite, result.CommercialValuation)
	if result.ProxyServiceValuation != nil {
		combineRetailValuation(&composite, *result.ProxyServiceValuation)
		composite.InputObservations = appendUniqueRetailRefs(composite.InputObservations, result.ProxyServiceValuation.InputObservations...)
	}
	if composite.ID == "" {
		return
	}
	if storeID != "" {
		composite.Subject = metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: storeID, BillingCallID: call.CallID.String()}
	}
	composite.Scope = "call:" + call.CallID.String()
	composite.Policy = economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: policy.Ref.ID, Version: policy.Ref.Version, EffectiveAt: policy.Ref.EffectiveAt, FetchedAt: policy.Ref.FetchedAt}, PolicyID: policy.Ref.ID}
	composite.PolicyContent = retailPolicyContent(policy)
	composite.Tariff = tariff.Ref
	composite.TariffContent = cloneRetailContent(&tariff.Content)
	composite.Rater = tariff.Ref
	composite.RaterContent = cloneRetailContent(&tariff.Content)
	if hash, err := economics.CanonicalValuationInputSetHash(composite.Basis, composite.InputObservations, composite.AllocationCoverageRefs); err == nil {
		composite.InputSetHash = hash
	}
	composite.Totals, _ = totalsFromLines(composite.Lines, tariff.Currency)
	proxyFingerprint := ""
	if result.ProxyServiceValuation != nil {
		proxyFingerprint = result.ProxyServiceValuation.Fingerprint()
	}
	composite.ID = retailCompositeIdentity(composite, proxyFingerprint)
	result.Valuation = composite
}

func retailCompositeIdentity(valuation economics.Valuation, proxyFingerprint string) string {
	// The composite identity must change when an immutable tariff context or a
	// route-specific model tariff changes. Line IDs alone are insufficient: two
	// tariffs can price the same canonical component key differently while
	// retaining the same rule ID. Canonical JSON provides a bounded, deterministic
	// representation of all line amounts, units, refs and charge kinds.
	lines, _ := json.Marshal(valuation.Lines)
	parts := []string{string(valuation.Basis), valuation.Scope, valuation.InputSetHash, valuation.ContextHash(), valuation.Tariff.ID, valuation.Tariff.Version, valuation.Policy.ID, valuation.Policy.Version, proxyFingerprint, string(lines)}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "retail-valuation:" + hex.EncodeToString(sum[:])
}

func retailValuationMoney(valuation economics.Valuation) (Money, error) {
	if len(valuation.Totals) != 1 {
		return Money{}, fmt.Errorf("expected one currency total, got %d", len(valuation.Totals))
	}
	total := valuation.Totals[0].RoundedAmount
	if !total.Present {
		return Money{}, fmt.Errorf("rounded customer total is unavailable")
	}
	return Money{Nano: total.NanoUnits, Currency: total.Currency}, nil
}

func retailValuationCurrency(valuation economics.Valuation) string {
	for _, total := range valuation.Totals {
		if total.Currency != "" {
			return total.Currency
		}
	}
	for _, line := range valuation.Lines {
		if line.RoundedAmount != nil && line.RoundedAmount.Present {
			return line.RoundedAmount.Currency
		}
	}
	return ""
}

func retailContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
