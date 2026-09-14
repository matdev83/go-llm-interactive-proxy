package billing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/replay"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

var (
	// These errors classify post-usage rating failures. They are deliberately
	// separate from stream/transport errors so callers can retain an
	// incomplete valuation without mistaking it for a zero-cost result.
	ErrRateMissing     = errors.New("billing: rating rate is missing")
	ErrRateUnsupported = errors.New("billing: rating rate is unsupported")
	ErrRatePrecision   = errors.New("billing: rating precision is unsupported")
	// Reuse the established scalar-rating sentinels so callers can classify
	// legacy and component rating failures uniformly.
	ErrRateCurrencyMismatch  = ErrRatingCurrencyMismatch
	ErrQuantityIncomplete    = errors.New("billing: rating quantity is incomplete")
	ErrQualifierMissing      = errors.New("billing: required rating qualifier is missing")
	ErrQualifierConflict     = errors.New("billing: rating qualifiers conflict")
	ErrCoverageInvalid       = errors.New("billing: charge coverage is invalid")
	ErrPeriodScopeRequired   = errors.New("billing: period-scoped rule requires period valuation")
	ErrFixedFeeScopeMismatch = errors.New("billing: fixed fee scope does not match valuation scope")
	ErrTariffInvalid         = errors.New("billing: invalid tariff snapshot")
	ErrTariffImmutable       = errors.New("billing: tariff snapshot is immutable")
	// Input-set identity is a public economics trust-boundary contract. Keep a
	// billing alias so internal callers can classify direct-rater failures
	// without importing a second sentinel.
	ErrInputSetHashMismatch = economics.ErrInputSetHashMismatch
)

// Compatibility aliases make the classification explicit at call sites while
// retaining one sentinel for errors.Is checks.
var (
	ErrMissingRate          = ErrRateMissing
	ErrUnsupportedRate      = ErrRateUnsupported
	ErrUnsupportedPrecision = ErrRatePrecision
	ErrCurrencyMismatch     = ErrRateCurrencyMismatch
	ErrIncompleteQuantity   = ErrQuantityIncomplete
	ErrRatingPrecision      = ErrRatePrecision
	ErrRateInvalid          = ErrRatingInvalid
)

// PostUsageRater is the billing-owned seam for deterministic monetary rating.
// It is intentionally not part of pkg/lipsdk/economics: stream-time SDK
// consumers must not acquire a public financial rater contract.
type PostUsageRater interface {
	Rate(context.Context, economics.PostUsageRatingInput) (economics.Valuation, error)
}

// Rater is a concise internal alias retained for billing composition. Its
// package location makes post-usage ownership visible to architecture checks.
type Rater = PostUsageRater

// IndependentValuations retains the locally-derived expected (E),
// provider-quantity local (Q), and provider-reported (P) planes separately.
// A nil plane means its source evidence did not exist; it is never an
// implicit zero valuation.
type IndependentValuations struct {
	Expected         *economics.Valuation
	ProviderQuantity *economics.Valuation
	ProviderReported *economics.Valuation
}

// RateIndependentValuations invokes each applicable plane independently. The
// provider's monetary charge cannot suppress E or Q, and missing provider
// money cannot manufacture P.
func RateIndependentValuations(ctx context.Context, rater PostUsageRater, base economics.PostUsageRatingInput) (IndependentValuations, error) {
	if rater == nil {
		return IndependentValuations{}, fmt.Errorf("%w: post-usage rater is nil", ErrTariffInvalid)
	}
	canonicalBase, canonicalErr := canonicalizeRatingInput(base)
	if canonicalErr != nil {
		return IndependentValuations{}, fmt.Errorf("%w: %v", ErrRatingInvalid, canonicalErr)
	}
	base = canonicalBase
	baseRefs, refErr := inputObservationRefs(base)
	if refErr != nil {
		return IndependentValuations{}, fmt.Errorf("%w: %v", ErrRatingInvalid, refErr)
	}
	verifiedBaseHash, hashErr := verifyInputSetHash(base.Basis, base.InputSetHash, baseRefs)
	if hashErr != nil {
		return IndependentValuations{}, fmt.Errorf("%w: %w", ErrRatingInvalid, hashErr)
	}
	base.InputSetHash = verifiedBaseHash
	if err := base.Validate(); err != nil {
		return IndependentValuations{}, fmt.Errorf("%w: %v", ErrRatingInvalid, err)
	}
	var out IndependentValuations
	var ratingErrs []error
	for _, plane := range []struct {
		basis economics.ValuationBasis
		dst   **economics.Valuation
		ok    func(metering.Observation) bool
	}{
		{economics.BasisLocalExpected, &out.Expected, isLocalQuantityObservation},
		{economics.BasisProviderQuantityLocal, &out.ProviderQuantity, isProviderQuantityObservation},
		{economics.BasisProviderReported, &out.ProviderReported, isProviderChargeObservation},
	} {
		if !hasApplicableObservation(base.Observations, plane.ok) {
			continue
		}
		input := base.Clone()
		input.Basis = plane.basis
		// Keep each valuation's immutable input set scoped to the evidence
		// plane it represents. A shared source observation may participate in
		// more than one plane (for example provider quantity plus provider
		// charge), but unrelated local/provider refs must not appear as if they
		// supported the independent claim.
		input = inputForPlane(input, plane.ok)
		input, refErr := canonicalizeRatingInput(input)
		if refErr != nil {
			ratingErrs = append(ratingErrs, fmt.Errorf("%s: %w", plane.basis, refErr))
			continue
		}
		refs, refErr := inputObservationRefs(input)
		if refErr != nil {
			ratingErrs = append(ratingErrs, fmt.Errorf("%s: %w", plane.basis, refErr))
			continue
		}
		sortObservationRefs(refs)
		// Each independent valuation owns the verified hash of its filtered
		// evidence plane rather than inheriting the full mixed-source base hash.
		input.InputSetHash = hashPlaneObservationRefs(plane.basis, refs)
		// Direct provider claims do not use tariff arithmetic. The shared input
		// still carries its immutable context for validation, while the P
		// implementation strips incidental local rater/tariff labels.
		valuation, err := rater.Rate(ctx, input)
		if valuation.ID != "" {
			copy := valuation.Clone()
			*plane.dst = &copy
		}
		if err != nil {
			ratingErrs = append(ratingErrs, fmt.Errorf("%s: %w", plane.basis, err))
			continue
		}
	}
	return out, errors.Join(ratingErrs...)
}

func hashPlaneObservationRefs(basis economics.ValuationBasis, refs []metering.ObservationRef) string {
	hash, err := economics.CanonicalInputSetHash(basis, refs)
	if err != nil {
		return ""
	}
	return hash
}

func verifyInputSetHash(basis economics.ValuationBasis, supplied string, refs []metering.ObservationRef) (string, error) {
	expected, err := economics.CanonicalInputSetHash(basis, refs)
	if err != nil {
		return "", err
	}
	if supplied != "" && supplied != expected {
		return "", fmt.Errorf("%w: basis=%q supplied=%q expected=%q", ErrInputSetHashMismatch, basis, supplied, expected)
	}
	return expected, nil
}

func hasApplicableObservation(observations []metering.Observation, predicate func(metering.Observation) bool) bool {
	for _, observation := range observations {
		if predicate(observation) {
			return true
		}
	}
	return false
}

func inputForPlane(input economics.PostUsageRatingInput, predicate func(metering.Observation) bool) economics.PostUsageRatingInput {
	if len(input.Observations) == 0 {
		return input
	}
	selected := make([]metering.Observation, 0, len(input.Observations))
	for _, observation := range input.Observations {
		if predicate(observation) {
			selected = append(selected, observation)
		}
	}
	input.Observations = selected
	input.ObservationRefs = nil
	return input
}

// canonicalizeRatingInput applies the shared replay identity boundary before
// a valuation or its plane hash is constructed. Exact duplicate deliveries
// disappear, while a genuine revision (or a conflicting immutable payload)
// remains distinguishable.
func canonicalizeRatingInput(input economics.PostUsageRatingInput) (economics.PostUsageRatingInput, error) {
	if len(input.Observations) != 0 {
		replayed, err := replay.Deduplicate(input.Observations)
		if err != nil {
			return economics.PostUsageRatingInput{}, fmt.Errorf("%w: replay canonicalization: %v", ErrRatingEvidenceMissing, err)
		}
		input.Observations = replayed.Observations
		// Observation refs are derived from canonical observations whenever the
		// envelope is present. This prevents a stale caller-provided ref set from
		// reintroducing an exact replay after filtering to a plane.
		input.ObservationRefs = nil
	}
	refs, err := inputObservationRefs(input)
	if err != nil {
		return economics.PostUsageRatingInput{}, err
	}
	refs, err = canonicalizeObservationRefs(refs)
	if err != nil {
		return economics.PostUsageRatingInput{}, err
	}
	if len(input.Observations) == 0 {
		input.ObservationRefs = refs
	}
	return input, nil
}

func isLocalQuantityObservation(observation metering.Observation) bool {
	return observation.Origin == metering.OriginLocal && (len(observation.Measures) != 0 || observation.Authority == metering.AuthorityUnavailableClaim)
}

func isProviderQuantityObservation(observation metering.Observation) bool {
	return observation.Origin == metering.OriginProvider && (len(observation.Measures) != 0 || observation.Authority == metering.AuthorityUnavailableClaim)
}

func isProviderChargeObservation(observation metering.Observation) bool {
	return observation.Origin == metering.OriginProvider && (len(observation.Charges) != 0 || observation.Authority == metering.AuthorityUnavailableClaim)
}

type ratingObservationRef struct {
	observation metering.Observation
	ref         metering.ObservationRef
}

func inputObservationRefs(input economics.PostUsageRatingInput) ([]metering.ObservationRef, error) {
	if len(input.ObservationRefs) != 0 {
		return canonicalizeObservationRefs(input.ObservationRefs)
	}
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for i, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return nil, fmt.Errorf("%w: observation %d reference: %v", ErrRatingEvidenceMissing, i, err)
		}
		refs = append(refs, ref)
	}
	return canonicalizeObservationRefs(refs)
}

func canonicalizeObservationRefs(refs []metering.ObservationRef) ([]metering.ObservationRef, error) {
	ordered := append([]metering.ObservationRef(nil), refs...)
	sortObservationRefs(ordered)
	out := ordered[:0]
	seen := make(map[string]metering.ObservationRef, len(ordered))
	for _, ref := range ordered {
		identity := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + fmt.Sprint(ref.Revision)
		if prior, exists := seen[identity]; exists {
			if prior.PayloadHash != ref.PayloadHash {
				return nil, fmt.Errorf("%w: conflicting payload hashes for observation %s", ErrRatingEvidenceMissing, identity)
			}
			continue
		}
		seen[identity] = ref
		out = append(out, ref)
	}
	return out, nil
}

func sortObservationRefs(refs []metering.ObservationRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].StoreID != refs[j].StoreID {
			return refs[i].StoreID < refs[j].StoreID
		}
		if refs[i].ObservationID != refs[j].ObservationID {
			return refs[i].ObservationID < refs[j].ObservationID
		}
		if refs[i].Revision != refs[j].Revision {
			return refs[i].Revision < refs[j].Revision
		}
		return refs[i].PayloadHash < refs[j].PayloadHash
	})
}

func ratingObservationRefKey(ref metering.ObservationRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + fmt.Sprint(ref.Revision) + "\x00" + ref.PayloadHash
}

func componentIdentityKey(key metering.ComponentKey) string { return key.CanonicalKey() }

func ruleComponentMatches(rule, measure metering.ComponentKey) bool {
	if rule.Direction != measure.Direction || rule.Component != measure.Component || rule.Unit != measure.Unit || rule.SchemaID != measure.SchemaID {
		return false
	}
	if len(rule.Dimensions) == 0 {
		return true
	}
	for _, wanted := range rule.Dimensions {
		found := false
		for _, actual := range measure.Dimensions {
			if wanted.Name == actual.Name && wanted.Value == actual.Value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func decimalDisplay(d *metering.Decimal) string {
	if d == nil {
		return ""
	}
	return d.CanonicalString()
}

func safeRuleComponent(rule *economics.RatingRule) string {
	if rule == nil || rule.Component == nil {
		return ""
	}
	return rule.Component.CanonicalKey()
}

func qualifierMap(dimensions []metering.Dimension) (map[string]string, error) {
	out := make(map[string]string, len(dimensions))
	for _, dimension := range dimensions {
		if prior, exists := out[dimension.Name]; exists && prior != dimension.Value {
			return nil, fmt.Errorf("%w: qualifier %q has values %q and %q", ErrQualifierConflict, dimension.Name, prior, dimension.Value)
		}
		out[dimension.Name] = dimension.Value
	}
	return out, nil
}

func scopeIsPeriod(scope string) bool {
	scope = strings.ToLower(strings.TrimSpace(scope))
	return strings.HasPrefix(scope, "period:") || scope == "period"
}
