package billing

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// RetailSelectionMode identifies the request-scoped B-leg set used for
// customer inference quantities. It is deliberately independent from
// supplier COGS, which continues to attribute every executed B-leg.
type RetailSelectionMode string

const (
	RetailSelectionSurfacedWinner  RetailSelectionMode = "surfaced_winner"
	RetailSelectionNamedOutcomes   RetailSelectionMode = "named_outcomes"
	RetailSelectionAllAttributable RetailSelectionMode = "all_attributable"
)

// RetailCommercialBasis identifies the commercial interpretation of the
// selected B-leg set. Cost pass-through is explicit; it is never inferred from
// provider cost readiness or from the number of runtime attempts.
type RetailCommercialBasis string

const (
	RetailBasisIndependent     RetailCommercialBasis = "independent_retail"
	RetailBasisCostPassThrough RetailCommercialBasis = "cost_pass_through"
)

// RetailSelectionPolicy is the frozen customer-side B-leg selection portion
// of a ChargePolicy. It does not contain supplier rates or calculate COGS.
type RetailSelectionPolicy struct {
	Mode            RetailSelectionMode
	OutcomeSubset   []LegOutcome
	Basis           RetailCommercialBasis
	CostPassThrough *CostPassThroughPolicy
}

// RetailSelectionInput binds selection to one BillingCallID and its durable
// B-leg records. TenantID is an optional trusted scope constraint; when empty,
// a consistent tenant scope is still required across all selected evidence.
type RetailSelectionInput struct {
	Call     CallUsageRecord
	Legs     []CallLegUsageRecord
	Policy   ChargePolicy
	TenantID string
}

// RetailSelectionCompleteness describes whether the returned reference set is
// complete enough to feed request-scoped customer quantity rating.
type RetailSelectionCompleteness string

const (
	RetailSelectionComplete RetailSelectionCompleteness = "complete"
)

// RetailSelectionCapability identifies the evidence capability used by a
// successful selection. The selector requires source-separated V2 B-leg
// observations and never substitutes customer-boundary measurements.
type RetailSelectionCapability string

const (
	RetailCapabilityV2BLegObservations RetailSelectionCapability = "v2_b_leg_observations"
)

// RetailBLegSelection is an immutable value copy of one selected B-leg and
// its source observation references. Observation payloads are intentionally
// not copied into the result: downstream quantity raters must resolve the
// exact immutable references.
type RetailBLegSelection struct {
	CallID          BillingCallID
	ALegID          string
	BLegID          string
	AttemptSeq      int
	Outcome         LegOutcome
	Surfaced        SurfacedState
	ObservationRefs []metering.ObservationRef
}

// RetailSelectionResult is a frozen selection decision. Clone can be used by
// infrastructure adapters when handing the result to another owner.
type RetailSelectionResult struct {
	CallID          BillingCallID
	PolicyRef       VersionRef
	Mode            RetailSelectionMode
	Basis           RetailCommercialBasis
	Reason          RetailSelectionMode
	Completeness    RetailSelectionCompleteness
	Capability      RetailSelectionCapability
	SelectedBLegs   []RetailBLegSelection
	ObservationRefs []metering.ObservationRef
}

var (
	ErrRetailSelectionInvalid        = errors.New("billing: invalid retail selection")
	ErrRetailSelectionIncomplete     = errors.New("billing: retail selection evidence is incomplete")
	ErrRetailSelectionEmpty          = errors.New("billing: retail selection is empty")
	ErrRetailSelectionAmbiguous      = errors.New("billing: retail selection is ambiguous")
	ErrRetailSelectionOutcomeUnknown = errors.New("billing: retail selection outcome is unknown")
	ErrRetailSelectionDuplicate      = errors.New("billing: retail selection contains a duplicate reference")
	ErrRetailSelectionScopeMismatch  = errors.New("billing: retail selection evidence is outside the call scope")
	ErrRetailSelectionUntrusted      = errors.New("billing: retail selection evidence is not trusted")
)

// Clone returns a detached policy value. ChargePolicy snapshots use this when
// crossing the catalog boundary so a caller cannot mutate a published policy.
func (p RetailSelectionPolicy) Clone() RetailSelectionPolicy {
	p.OutcomeSubset = slices.Clone(p.OutcomeSubset)
	if p.CostPassThrough != nil {
		policy := p.CostPassThrough.Clone()
		p.CostPassThrough = &policy
	}
	return p
}

func (p RetailSelectionPolicy) Validate() error {
	switch p.Mode {
	case RetailSelectionSurfacedWinner, RetailSelectionAllAttributable:
		if len(p.OutcomeSubset) != 0 {
			return fmt.Errorf("%w: outcome subset is only valid for named selection", ErrRetailSelectionInvalid)
		}
	case RetailSelectionNamedOutcomes:
		if len(p.OutcomeSubset) == 0 {
			return fmt.Errorf("%w: named selection requires at least one outcome", ErrRetailSelectionInvalid)
		}
		seen := make(map[LegOutcome]struct{}, len(p.OutcomeSubset))
		for _, outcome := range p.OutcomeSubset {
			if !validLegOutcome(outcome) || outcome == LegOutcomeUnknown {
				return fmt.Errorf("%w: unknown named outcome %q", ErrRetailSelectionOutcomeUnknown, outcome)
			}
			if _, ok := seen[outcome]; ok {
				return fmt.Errorf("%w: named outcome %q", ErrRetailSelectionDuplicate, outcome)
			}
			seen[outcome] = struct{}{}
		}
	default:
		return fmt.Errorf("%w: unsupported selection mode %q", ErrRetailSelectionInvalid, p.Mode)
	}
	if p.Basis != RetailBasisIndependent && p.Basis != RetailBasisCostPassThrough {
		return fmt.Errorf("%w: unsupported commercial basis %q", ErrRetailSelectionInvalid, p.Basis)
	}
	if p.Basis == RetailBasisIndependent && p.CostPassThrough != nil {
		return fmt.Errorf("%w: cost pass-through policy is only valid for cost-pass-through basis", ErrCostPassThroughPolicyInvalid)
	}
	if p.CostPassThrough != nil {
		if err := p.CostPassThrough.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ResolveRetailSelectionPolicy maps the legacy Scope to the explicit retail
// selector when no newer selection mode is present. This preserves the
// existing offer scope during migration while making the new decision visible
// to callers that need B-leg observation references.
func ResolveRetailSelectionPolicy(policy ChargePolicy) (RetailSelectionPolicy, error) {
	var retail RetailSelectionPolicy
	if policy.Retail != nil {
		retail = policy.Retail.Clone()
	}
	if retail.Mode == "" {
		switch policy.Scope {
		case ChargeSurfacedTurn:
			retail.Mode = RetailSelectionSurfacedWinner
		case ChargeAllPotentialLegs:
			retail.Mode = RetailSelectionAllAttributable
		default:
			return RetailSelectionPolicy{}, fmt.Errorf("%w: unsupported legacy charging scope %q", ErrRetailSelectionInvalid, policy.Scope)
		}
	}
	if retail.Basis == "" {
		retail.Basis = RetailBasisIndependent
	}
	if err := retail.Validate(); err != nil {
		return RetailSelectionPolicy{}, err
	}
	return retail.Clone(), nil
}

// Clone returns a detached ChargePolicy snapshot, including its explicit
// retail selection subset.
func (p ChargePolicy) Clone() ChargePolicy {
	if p.Retail != nil {
		retail := p.Retail.Clone()
		p.Retail = &retail
	}
	return p
}

// SelectRetailBLegEvidence resolves one frozen customer-side B-leg set and
// returns only immutable B-leg/observation references. It never consults
// provider rates or costs and never changes supplier COGS attribution.
func SelectRetailBLegEvidence(in RetailSelectionInput) (RetailSelectionResult, error) {
	if err := in.Policy.Validate(); err != nil {
		return RetailSelectionResult{}, err
	}
	retail, err := ResolveRetailSelectionPolicy(in.Policy)
	if err != nil {
		return RetailSelectionResult{}, err
	}
	if !validTurnOutcome(in.Call.Outcome) || in.Call.Outcome == TurnOutcomeUnknown {
		return RetailSelectionResult{}, fmt.Errorf("%w: call outcome is unknown", ErrRetailSelectionOutcomeUnknown)
	}
	if err := in.Call.validate(); err != nil {
		return RetailSelectionResult{}, fmt.Errorf("%w: call: %v", ErrRetailSelectionInvalid, err)
	}
	if in.Call.CustomerPricingRef != in.Policy.PricingRef || in.Call.ChargePolicyRef != in.Policy.Ref {
		return RetailSelectionResult{}, fmt.Errorf("%w: call snapshot references do not match policy", ErrRetailSelectionScopeMismatch)
	}
	if len(in.Legs) == 0 {
		if len(in.Call.ExpectedBLegIDs) != 0 {
			return RetailSelectionResult{}, fmt.Errorf("%w: call has expected B-legs but no durable outcomes", ErrRetailSelectionIncomplete)
		}
		return RetailSelectionResult{}, ErrRetailSelectionEmpty
	}

	infos := make([]retailLegInfo, 0, len(in.Legs))
	seenBLegs := make(map[string]struct{}, len(in.Legs))
	for i, leg := range in.Legs {
		if !validLegOutcome(leg.Outcome) || leg.Outcome == LegOutcomeUnknown {
			return RetailSelectionResult{}, fmt.Errorf("%w: leg %q", ErrRetailSelectionOutcomeUnknown, leg.BLegID)
		}
		if !validSurfacedState(leg.Surfaced) {
			return RetailSelectionResult{}, fmt.Errorf("%w: invalid outcome/surfaced state for leg %q", ErrRetailSelectionInvalid, leg.BLegID)
		}
		if leg.Surfaced == SurfacedUnknown {
			return RetailSelectionResult{}, fmt.Errorf("%w: leg %q surfaced state is unknown", ErrRetailSelectionIncomplete, leg.BLegID)
		}
		if strings.TrimSpace(leg.BLegID) == "" {
			return RetailSelectionResult{}, fmt.Errorf("%w: leg %d has no B-leg identity", ErrRetailSelectionInvalid, i)
		}
		if _, ok := seenBLegs[leg.BLegID]; ok {
			return RetailSelectionResult{}, fmt.Errorf("%w: B-leg %q", ErrRetailSelectionDuplicate, leg.BLegID)
		}
		seenBLegs[leg.BLegID] = struct{}{}
		if leg.CallID != in.Call.CallID || leg.ALegID != in.Call.ALegID {
			return RetailSelectionResult{}, fmt.Errorf("%w: leg %q lineage mismatch", ErrRetailSelectionScopeMismatch, leg.BLegID)
		}
		if err := validateRetailObservationRefs(leg.ObservationRefs); err != nil {
			return RetailSelectionResult{}, fmt.Errorf("%w: leg %q: %w", ErrRetailSelectionInvalid, leg.BLegID, err)
		}
		if len(leg.EvidenceConflicts) != 0 {
			return RetailSelectionResult{}, fmt.Errorf("%w: leg %q has conflicting evidence", ErrRetailSelectionUntrusted, leg.BLegID)
		}
		if len(leg.Observations) != 0 {
			if _, _, _, err := retailObservationRefs(leg, in.Call, in.TenantID); err != nil {
				return RetailSelectionResult{}, fmt.Errorf("%w: leg %q: %w", errorClass(err), leg.BLegID, err)
			}
		}
		if err := leg.validate(); err != nil {
			return RetailSelectionResult{}, fmt.Errorf("%w: leg %q: %v", ErrRetailSelectionInvalid, leg.BLegID, err)
		}
		infos = append(infos, retailLegInfo{leg: leg})
	}
	if err := validateExpectedRetailBLegs(in.Call.ExpectedBLegIDs, seenBLegs); err != nil {
		return RetailSelectionResult{}, err
	}

	selected, err := selectRetailLegInfosForPolicy(infos, retail, in.Call.Outcome)
	if err != nil {
		return RetailSelectionResult{}, err
	}
	if len(selected) == 0 {
		return RetailSelectionResult{}, fmt.Errorf("%w: mode %s", ErrRetailSelectionEmpty, retail.Mode)
	}

	// Build references only after the policy has frozen the leg set. This lets
	// the normal independent path ignore provider cost/rate readiness entirely.
	result := RetailSelectionResult{
		CallID:        in.Call.CallID,
		PolicyRef:     in.Policy.Ref,
		Mode:          retail.Mode,
		Basis:         retail.Basis,
		Reason:        retail.Mode,
		Completeness:  RetailSelectionComplete,
		Capability:    RetailCapabilityV2BLegObservations,
		SelectedBLegs: make([]RetailBLegSelection, 0, len(selected)),
	}
	var tenantScope, storeScope string
	tenantSet, tenantMissing := false, false
	seenSelectedRefs := make(map[string]struct{})
	for _, info := range selected {
		refs, tenant, store, err := retailObservationRefs(info.leg, in.Call, in.TenantID)
		if err != nil {
			return RetailSelectionResult{}, fmt.Errorf("%w: leg %q: %v", errorClass(err), info.leg.BLegID, err)
		}
		if tenant == "" {
			tenantMissing = true
			if tenantSet {
				return RetailSelectionResult{}, fmt.Errorf("%w: tenant scope differs across selected legs", ErrRetailSelectionScopeMismatch)
			}
		} else {
			if tenantMissing {
				return RetailSelectionResult{}, fmt.Errorf("%w: tenant scope differs across selected legs", ErrRetailSelectionScopeMismatch)
			}
			if !tenantSet {
				tenantScope, tenantSet = tenant, true
			} else if tenantScope != tenant {
				return RetailSelectionResult{}, fmt.Errorf("%w: tenant scope differs across selected legs", ErrRetailSelectionScopeMismatch)
			}
		}
		if storeScope == "" {
			storeScope = store
		} else if storeScope != store {
			return RetailSelectionResult{}, fmt.Errorf("%w: store scope differs across selected legs", ErrRetailSelectionScopeMismatch)
		}
		selectedLeg := RetailBLegSelection{
			CallID: info.leg.CallID, ALegID: info.leg.ALegID, BLegID: info.leg.BLegID,
			AttemptSeq: info.leg.AttemptSeq, Outcome: info.leg.Outcome, Surfaced: info.leg.Surfaced,
			ObservationRefs: refs,
		}
		for _, ref := range refs {
			key := fmt.Sprintf("%s\x00%s\x00%d", ref.StoreID, ref.ObservationID, ref.Revision)
			if _, exists := seenSelectedRefs[key]; exists {
				return RetailSelectionResult{}, fmt.Errorf("%w: observation %q appears on multiple selected B-legs", ErrRetailSelectionDuplicate, ref.ObservationID)
			}
			seenSelectedRefs[key] = struct{}{}
		}
		result.SelectedBLegs = append(result.SelectedBLegs, selectedLeg)
		result.ObservationRefs = append(result.ObservationRefs, refs...)
	}
	return result.Clone(), nil
}

// Clone returns a detached result, including all selected reference slices.
func (r RetailSelectionResult) Clone() RetailSelectionResult {
	out := r
	out.SelectedBLegs = make([]RetailBLegSelection, len(r.SelectedBLegs))
	for i, leg := range r.SelectedBLegs {
		out.SelectedBLegs[i] = leg
		out.SelectedBLegs[i].ObservationRefs = slices.Clone(leg.ObservationRefs)
	}
	out.ObservationRefs = slices.Clone(r.ObservationRefs)
	return out
}

type retailLegInfo struct {
	leg CallLegUsageRecord
}

// selectRetailLegInfosForPolicy is the one policy-to-candidate dispatch used
// by both V2 observation selection and the V1 scalar compatibility adapter.
// The adapters differ only in evidence validation; the frozen commercial
// policy must never select a different B-leg set by evidence format.
func selectRetailLegInfosForPolicy(infos []retailLegInfo, policy RetailSelectionPolicy, outcome TurnOutcome) ([]retailLegInfo, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Mode == RetailSelectionNamedOutcomes {
		return selectNamedRetailLegInfos(infos, policy.OutcomeSubset), nil
	}
	return selectRetailLegInfos(infos, policy.Mode, outcome)
}

// selectRetailBLegsForPolicy adapts accepted legacy scalar evidence into the
// shared canonical selector. It intentionally leaves V2 observation
// validation to SelectRetailBLegEvidence; this function only owns the V1
// evidence-availability compatibility filter and selection policy dispatch.
func selectRetailBLegsForPolicy(legs []CallLegUsageRecord, policy RetailSelectionPolicy, outcome TurnOutcome) ([]CallLegUsageRecord, error) {
	accepted := acceptedCustomerLegs(legs)
	infos := make([]retailLegInfo, 0, len(accepted))
	for _, leg := range accepted {
		infos = append(infos, retailLegInfo{leg: leg})
	}
	selected, err := selectRetailLegInfosForPolicy(infos, policy, outcome)
	if err != nil {
		return nil, err
	}
	out := make([]CallLegUsageRecord, 0, len(selected))
	for _, info := range selected {
		out = append(out, info.leg)
	}
	return out, nil
}

func selectRetailLegInfos(infos []retailLegInfo, mode RetailSelectionMode, outcome TurnOutcome) ([]retailLegInfo, error) {
	eligible := make([]retailLegInfo, 0, len(infos))
	for _, info := range infos {
		if info.leg.Outcome == LegOutcomeNeverStarted || info.leg.Outcome == LegOutcomeRejected {
			continue
		}
		eligible = append(eligible, info)
	}
	switch mode {
	case RetailSelectionAllAttributable:
		return sortRetailLegInfos(eligible), nil
	case RetailSelectionSurfacedWinner:
		if outcome != TurnOutcomeCompleted {
			return selectInterruptedRetailLegInfos(eligible)
		}
		return selectSurfacedWinner(eligible)
	default:
		return nil, fmt.Errorf("%w: unsupported selection mode %q", ErrRetailSelectionInvalid, mode)
	}
}

// selectInterruptedRetailLegInfos preserves the legacy explicit failure rule
// for the default surfaced/winner offer: an interrupted call can charge one
// logical accepted attempt, preferring a uniquely surfaced leg and otherwise
// selecting the latest durable attempt. Unknown ordering is never guessed.
func selectInterruptedRetailLegInfos(eligible []retailLegInfo) ([]retailLegInfo, error) {
	if len(eligible) == 0 {
		return eligible, nil
	}
	surfaced := make([]retailLegInfo, 0, 1)
	for _, info := range eligible {
		if info.leg.Surfaced == SurfacedYes {
			surfaced = append(surfaced, info)
		}
	}
	if len(surfaced) > 1 {
		return nil, fmt.Errorf("%w: multiple surfaced B-legs", ErrRetailSelectionAmbiguous)
	}
	if len(surfaced) == 1 {
		return surfaced, nil
	}
	if len(eligible) == 1 {
		return eligible, nil
	}
	for _, info := range eligible {
		if info.leg.AttemptSeq <= 0 {
			return nil, fmt.Errorf("%w: interrupted call has %d accepted legs and requires the latest accepted attempt", ErrBillingAttemptSequenceUnknown, len(eligible))
		}
	}
	latest := eligible[0]
	for _, info := range eligible[1:] {
		if info.leg.AttemptSeq > latest.leg.AttemptSeq {
			latest = info
		}
	}
	return []retailLegInfo{latest}, nil
}

func selectSurfacedWinner(eligible []retailLegInfo) ([]retailLegInfo, error) {
	surfaced := make([]retailLegInfo, 0, len(eligible))
	for _, info := range eligible {
		if info.leg.Surfaced == SurfacedYes {
			surfaced = append(surfaced, info)
		}
	}
	if len(surfaced) > 1 {
		return nil, fmt.Errorf("%w: multiple surfaced B-legs", ErrRetailSelectionAmbiguous)
	}
	if len(surfaced) == 1 {
		return surfaced, nil
	}
	winners := make([]retailLegInfo, 0, 1)
	for _, info := range eligible {
		if info.leg.Outcome == LegOutcomeWinner {
			winners = append(winners, info)
		}
	}
	if len(winners) > 1 {
		return nil, fmt.Errorf("%w: multiple winner B-legs", ErrRetailSelectionAmbiguous)
	}
	return winners, nil
}

func selectNamedRetailLegInfos(infos []retailLegInfo, subset []LegOutcome) []retailLegInfo {
	allowed := make(map[LegOutcome]struct{}, len(subset))
	for _, outcome := range subset {
		allowed[outcome] = struct{}{}
	}
	selected := make([]retailLegInfo, 0, len(infos))
	for _, info := range infos {
		if info.leg.Outcome == LegOutcomeNeverStarted || info.leg.Outcome == LegOutcomeRejected {
			continue
		}
		if _, ok := allowed[info.leg.Outcome]; ok {
			selected = append(selected, info)
		}
	}
	return sortRetailLegInfos(selected)
}

func sortRetailLegInfos(infos []retailLegInfo) []retailLegInfo {
	out := slices.Clone(infos)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].leg, out[j].leg
		if a.AttemptSeq != b.AttemptSeq {
			if a.AttemptSeq == 0 {
				return false
			}
			if b.AttemptSeq == 0 {
				return true
			}
			return a.AttemptSeq < b.AttemptSeq
		}
		return a.BLegID < b.BLegID
	})
	return out
}

func validateExpectedRetailBLegs(expected []string, seen map[string]struct{}) error {
	if len(expected) == 0 {
		return nil
	}
	expectedSet := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		expectedSet[id] = struct{}{}
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("%w: expected B-leg %q has no durable outcome", ErrRetailSelectionIncomplete, id)
		}
	}
	for id := range seen {
		if _, ok := expectedSet[id]; !ok {
			return fmt.Errorf("%w: B-leg %q is not expected for the call", ErrRetailSelectionScopeMismatch, id)
		}
	}
	return nil
}

func validateRetailObservationRefs(refs []metering.ObservationRef) error {
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return err
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", ref.StoreID, ref.ObservationID, ref.Revision)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: %s", ErrRetailSelectionDuplicate, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func retailObservationRefs(leg CallLegUsageRecord, call CallUsageRecord, tenantID string) ([]metering.ObservationRef, string, string, error) {
	if len(leg.Observations) == 0 {
		return nil, "", "", fmt.Errorf("%w: no V2 B-leg observations", ErrRetailSelectionIncomplete)
	}
	refs := make([]metering.ObservationRef, 0, len(leg.Observations))
	seen := make(map[string]struct{}, len(leg.Observations))
	var tenantScope, storeScope string
	tenantMissing := false
	for i, observation := range leg.Observations {
		if observation.Subject.Kind != metering.SubjectBLeg || observation.Lifecycle != metering.LifecycleBackendAttempt ||
			(observation.Boundary != metering.BoundaryBackendIngress && observation.Boundary != metering.BoundaryBackendEgress) {
			return nil, "", "", fmt.Errorf("%w: observation %q is not a backend-attempt B-leg observation", ErrRetailSelectionScopeMismatch, observation.ID)
		}
		if observation.Subject.StoreID != observation.Correlation.StoreID {
			return nil, "", "", fmt.Errorf("%w: observation %q has mismatched store scope", ErrRetailSelectionScopeMismatch, observation.ID)
		}
		if observation.Subject.TenantID != "" && observation.Correlation.TenantID != "" && observation.Subject.TenantID != observation.Correlation.TenantID {
			return nil, "", "", fmt.Errorf("%w: observation %q has mismatched tenant scope", ErrRetailSelectionScopeMismatch, observation.ID)
		}
		if err := observation.Validate(); err != nil {
			return nil, "", "", fmt.Errorf("%w: observation %d: %v", ErrRetailSelectionIncomplete, i, err)
		}
		if observation.Subject.BLegID != leg.BLegID || (observation.Correlation.BLegID != "" && observation.Correlation.BLegID != leg.BLegID) {
			return nil, "", "", fmt.Errorf("%w: observation %q B-leg lineage mismatch", ErrRetailSelectionScopeMismatch, observation.ID)
		}
		if err := requireObservationCallLineage(observation, call); err != nil {
			return nil, "", "", err
		}
		if observation.Subject.ALegID != "" && observation.Subject.ALegID != leg.ALegID {
			return nil, "", "", fmt.Errorf("%w: observation %q A-leg lineage mismatch", ErrRetailSelectionScopeMismatch, observation.ID)
		}
		if observation.Correlation.ALegID != "" && observation.Correlation.ALegID != leg.ALegID {
			return nil, "", "", fmt.Errorf("%w: observation %q correlation A-leg lineage mismatch", ErrRetailSelectionScopeMismatch, observation.ID)
		}
		store := observation.Subject.StoreID
		if storeScope == "" {
			storeScope = store
		} else if storeScope != store {
			return nil, "", "", fmt.Errorf("%w: observations use different stores", ErrRetailSelectionScopeMismatch)
		}
		observationTenant := observation.Subject.TenantID
		if observationTenant == "" {
			observationTenant = observation.Correlation.TenantID
		}
		if tenantID != "" && observationTenant != tenantID {
			return nil, "", "", fmt.Errorf("%w: observation %q tenant mismatch", ErrRetailSelectionScopeMismatch, observation.ID)
		}
		if observationTenant == "" {
			if tenantScope != "" {
				return nil, "", "", fmt.Errorf("%w: observations do not share a complete tenant scope", ErrRetailSelectionScopeMismatch)
			}
			tenantMissing = true
		} else {
			if tenantMissing {
				return nil, "", "", fmt.Errorf("%w: observations do not share a complete tenant scope", ErrRetailSelectionScopeMismatch)
			}
			if tenantScope == "" {
				tenantScope = observationTenant
			} else if tenantScope != observationTenant {
				return nil, "", "", fmt.Errorf("%w: observations use different tenants", ErrRetailSelectionScopeMismatch)
			}
		}
		ref, err := observation.Ref(store)
		if err != nil {
			return nil, "", "", fmt.Errorf("%w: observation %q reference: %v", ErrRetailSelectionIncomplete, observation.ID, err)
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", ref.StoreID, ref.ObservationID, ref.Revision)
		if _, ok := seen[key]; ok {
			return nil, "", "", fmt.Errorf("%w: observation %q", ErrRetailSelectionDuplicate, observation.ID)
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}
	if len(leg.ObservationRefs) != 0 {
		if len(leg.ObservationRefs) != len(refs) {
			return nil, "", "", fmt.Errorf("%w: explicit observation refs do not match loaded observations", ErrRetailSelectionIncomplete)
		}
		for _, supplied := range leg.ObservationRefs {
			matched := false
			for _, derived := range refs {
				if supplied.Equal(derived) {
					matched = true
					break
				}
			}
			if !matched {
				return nil, "", "", fmt.Errorf("%w: explicit observation ref %s does not match loaded observations", ErrRetailSelectionScopeMismatch, supplied.ObservationID)
			}
		}
	}
	if tenantID != "" && tenantScope != "" && tenantScope != tenantID {
		return nil, "", "", fmt.Errorf("%w: tenant scope mismatch", ErrRetailSelectionScopeMismatch)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].StoreID != refs[j].StoreID {
			return refs[i].StoreID < refs[j].StoreID
		}
		if refs[i].ObservationID != refs[j].ObservationID {
			return refs[i].ObservationID < refs[j].ObservationID
		}
		return refs[i].Revision < refs[j].Revision
	})
	return refs, tenantScope, storeScope, nil
}

func requireObservationCallLineage(observation metering.Observation, call CallUsageRecord) error {
	callID := call.CallID.String()
	lineage := []string{
		observation.Subject.BillingCallID, observation.Subject.CallID,
		observation.Correlation.BillingCallID, observation.Correlation.CallID,
	}
	known := false
	for _, candidate := range lineage {
		if candidate == "" {
			continue
		}
		known = true
		if candidate != callID {
			return fmt.Errorf("%w: observation %q call lineage mismatch", ErrRetailSelectionScopeMismatch, observation.ID)
		}
	}
	if !known {
		return fmt.Errorf("%w: observation %q has no call lineage", ErrRetailSelectionScopeMismatch, observation.ID)
	}
	return nil
}

func errorClass(err error) error {
	switch {
	case errors.Is(err, ErrRetailSelectionDuplicate):
		return ErrRetailSelectionDuplicate
	case errors.Is(err, ErrRetailSelectionScopeMismatch):
		return ErrRetailSelectionScopeMismatch
	case errors.Is(err, ErrRetailSelectionUntrusted):
		return ErrRetailSelectionUntrusted
	default:
		return ErrRetailSelectionIncomplete
	}
}
