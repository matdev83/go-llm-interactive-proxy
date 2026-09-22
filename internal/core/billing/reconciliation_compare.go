package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 12.1 pure reconciliation comparator. It joins independent local and
// provider quantity evidence on the full economic identity and reports exact
// signed/absolute deltas. It performs no rating, monetary decomposition,
// persistence or policy selection; those remain later parent tasks.

var (
	// ErrReconciliationInput identifies malformed, cross-store or empty
	// comparison input. It is never returned for a legitimately incompatible
	// evidence pair, which is reported as an incomparable result instead.
	ErrReconciliationInput = errors.New("billing: invalid reconciliation comparison input")
	// ErrReconciliationBoundExceeded identifies an input or result that exceeds
	// the bounded comparison cardinality. Exceeding a bound fails closed and
	// never truncates silently.
	ErrReconciliationBoundExceeded = errors.New("billing: reconciliation comparison bound exceeded")
	// ErrReconciliationArithmetic identifies an exact delta that cannot be
	// represented by the bounded decimal contract. No float fallback exists.
	ErrReconciliationArithmetic = errors.New("billing: reconciliation arithmetic exceeds bounded decimal")
)

const (
	// MaxReconciliationObservations bounds one side's frozen observation set.
	MaxReconciliationObservations = 1024
	// MaxReconciliationMeasures bounds one side's total component measures.
	MaxReconciliationMeasures = 8192
	// MaxReconciliationItems bounds the comparison result cardinality.
	MaxReconciliationItems = 4096
)

// ReconciliationComparisonStatus is the comparison outcome for one component.
// It is deliberately separate from cost selection or posting state.
type ReconciliationComparisonStatus string

const (
	ReconciliationStatusMatched         ReconciliationComparisonStatus = "matched"
	ReconciliationStatusDiscrepant      ReconciliationComparisonStatus = "discrepant"
	ReconciliationStatusPartial         ReconciliationComparisonStatus = "partial"
	ReconciliationStatusIncomparable    ReconciliationComparisonStatus = "incomparable"
	ReconciliationStatusMissingLocal    ReconciliationComparisonStatus = "missing_local"
	ReconciliationStatusMissingProvider ReconciliationComparisonStatus = "missing_provider"
	ReconciliationStatusConflict        ReconciliationComparisonStatus = "conflict"
)

// ReconciliationComparisonReason is the typed explanation for a non-compared
// component. An empty reason accompanies matched/discrepant items.
type ReconciliationComparisonReason string

const (
	ReconciliationReasonNone                     ReconciliationComparisonReason = ""
	ReconciliationReasonSubjectMismatch          ReconciliationComparisonReason = "subject_mismatch"
	ReconciliationReasonPeriodMismatch           ReconciliationComparisonReason = "period_mismatch"
	ReconciliationReasonPayerMismatch            ReconciliationComparisonReason = "payer_mismatch"
	ReconciliationReasonCurrencyMismatch         ReconciliationComparisonReason = "currency_mismatch"
	ReconciliationReasonScopeMismatch            ReconciliationComparisonReason = "scope_mismatch"
	ReconciliationReasonChargeMismatch           ReconciliationComparisonReason = "charge_mismatch"
	ReconciliationReasonCoverageMismatch         ReconciliationComparisonReason = "coverage_mismatch"
	ReconciliationReasonContextMismatch          ReconciliationComparisonReason = "measurement_context_mismatch"
	ReconciliationReasonSchemaMismatch           ReconciliationComparisonReason = "schema_mismatch"
	ReconciliationReasonQualifierMismatch        ReconciliationComparisonReason = "qualifier_mismatch"
	ReconciliationReasonTokenizerMismatch        ReconciliationComparisonReason = "tokenizer_mismatch"
	ReconciliationReasonTokenizerMissing         ReconciliationComparisonReason = "tokenizer_missing"
	ReconciliationReasonTokenizerRequired        ReconciliationComparisonReason = "tokenizer_required"
	ReconciliationReasonSemanticsMismatch        ReconciliationComparisonReason = "semantics_mismatch"
	ReconciliationReasonValueUnavailableLocal    ReconciliationComparisonReason = "value_unavailable_local"
	ReconciliationReasonValueUnavailableProvider ReconciliationComparisonReason = "value_unavailable_provider"
	ReconciliationReasonValueUnavailableBoth     ReconciliationComparisonReason = "value_unavailable_both"
	ReconciliationReasonDuplicateLocal           ReconciliationComparisonReason = "duplicate_local"
	ReconciliationReasonDuplicateProvider        ReconciliationComparisonReason = "duplicate_provider"
	ReconciliationReasonConflictingLocal         ReconciliationComparisonReason = "conflicting_local"
	ReconciliationReasonConflictingProvider      ReconciliationComparisonReason = "conflicting_provider"
	// ReconciliationReasonBasisMismatch identifies a valuation basis that is
	// known but not independently comparable as local-versus-provider
	// evidence (for example retail/customer-policy selected from provider
	// data). It is an explicit incomparable cause, never a match.
	ReconciliationReasonBasisMismatch ReconciliationComparisonReason = "basis_mismatch"
)

// IsKnown reports whether the reason is part of the supported reconciliation
// vocabulary across the Phase12 comparison, tolerance and aggregation surfaces.
func (r ReconciliationComparisonReason) IsKnown() bool {
	switch r {
	case ReconciliationReasonNone,
		ReconciliationReasonSubjectMismatch, ReconciliationReasonPeriodMismatch,
		ReconciliationReasonPayerMismatch, ReconciliationReasonCurrencyMismatch,
		ReconciliationReasonScopeMismatch, ReconciliationReasonChargeMismatch,
		ReconciliationReasonCoverageMismatch, ReconciliationReasonContextMismatch,
		ReconciliationReasonSchemaMismatch, ReconciliationReasonQualifierMismatch,
		ReconciliationReasonTokenizerMismatch, ReconciliationReasonTokenizerMissing,
		ReconciliationReasonTokenizerRequired, ReconciliationReasonSemanticsMismatch,
		ReconciliationReasonValueUnavailableLocal, ReconciliationReasonValueUnavailableProvider,
		ReconciliationReasonValueUnavailableBoth, ReconciliationReasonDuplicateLocal,
		ReconciliationReasonDuplicateProvider, ReconciliationReasonConflictingLocal,
		ReconciliationReasonConflictingProvider, ReconciliationReasonBasisMismatch,
		ReconciliationReasonZeroDenominator, ReconciliationReasonUnitMismatch,
		ReconciliationReasonTolerancePolicyMissing, ReconciliationReasonEstimatedNotExact:
		return true
	default:
		return false
	}
}

// ReconciliationEvidenceSet is one frozen side of a quantity comparison. The
// side-level fields are the join context; observations supply component
// measures. It retains no account balance, journal handle or provider SDK.
// Tokenizer is the explicitly declared effective tokenizer semantics identity.
// A declaration on one side and none on the other is incomparable
// (tokenizer_missing), two different declarations are incomparable
// (tokenizer_mismatch) and a token-measured component with no declaration on
// either side is incomparable (tokenizer_required); only non-token native units
// stay comparable without declarations. Per-measure method references remain
// informational labels because local and provider measurement pipelines are
// expected to differ.
type ReconciliationEvidenceSet struct {
	Subject             metering.SubjectRef          `json:"subject"`
	Payer               metering.PaymentParty        `json:"payer,omitzero"`
	Currency            string                       `json:"currency,omitempty"`
	Scope               string                       `json:"scope,omitempty"`
	PeriodID            string                       `json:"period_id,omitempty"`
	ChargeItemID        string                       `json:"charge_item_id,omitempty"`
	Tokenizer           string                       `json:"tokenizer,omitempty"`
	Coverage            []metering.ChargeCoverageRef `json:"coverage,omitempty"`
	EffectiveQualifiers []metering.Dimension         `json:"effective_qualifiers,omitempty"`
	Observations        []metering.Observation       `json:"observations,omitempty"`
}

// ReconciliationQuantityEvidence retains one side's component quantity with
// its certainty label and immutable source observation reference.
type ReconciliationQuantityEvidence struct {
	Key         metering.ComponentKey   `json:"key"`
	Value       *metering.Decimal       `json:"value,omitempty"`
	Quality     string                  `json:"quality"`
	MethodRef   string                  `json:"method_ref,omitempty"`
	Observation metering.ObservationRef `json:"observation"`
}

// ComponentQuantityComparisonItem is one component outcome. Local/Provider
// carry every source retained for the component; conflict and duplicate
// outcomes keep all sources instead of selecting one silently.
type ComponentQuantityComparisonItem struct {
	Key           metering.ComponentKey            `json:"key"`
	Status        ReconciliationComparisonStatus   `json:"status"`
	Reason        ReconciliationComparisonReason   `json:"reason,omitempty"`
	Local         []ReconciliationQuantityEvidence `json:"local,omitempty"`
	Provider      []ReconciliationQuantityEvidence `json:"provider,omitempty"`
	SignedDelta   *metering.Decimal                `json:"signed_delta,omitempty"`
	AbsoluteDelta *metering.Decimal                `json:"absolute_delta,omitempty"`
}

// ComponentQuantityComparison is the bounded, deterministically ordered
// quantity comparison result for one economic subject context.
type ComponentQuantityComparison struct {
	Status   ReconciliationComparisonStatus    `json:"status"`
	Reason   ReconciliationComparisonReason    `json:"reason,omitempty"`
	Complete bool                              `json:"complete"`
	Items    []ComponentQuantityComparisonItem `json:"items"`
}

// CompareComponentQuantities joins local and provider quantity evidence on the
// full economic subject, payer/account, charge, coverage, currency, period,
// effective measurement context and full component key. It computes
// exact signed/absolute provider-minus-local deltas only for compatible
// evidence. Incompatible contexts are reported as typed incomparable/partial
// outcomes, never as matches or zeros.
func CompareComponentQuantities(local, provider ReconciliationEvidenceSet) (ComponentQuantityComparison, error) {
	localSide, err := normalizeReconciliationEvidenceSet(local, "local")
	if err != nil {
		return ComponentQuantityComparison{}, err
	}
	providerSide, err := normalizeReconciliationEvidenceSet(provider, "provider")
	if err != nil {
		return ComponentQuantityComparison{}, err
	}
	if localSide.set.Subject.StoreID != providerSide.set.Subject.StoreID {
		return ComponentQuantityComparison{}, fmt.Errorf("%w: cross-store comparison local=%q provider=%q", ErrReconciliationInput, localSide.set.Subject.StoreID, providerSide.set.Subject.StoreID)
	}
	if len(localSide.set.Observations) == 0 && len(providerSide.set.Observations) == 0 {
		return ComponentQuantityComparison{}, fmt.Errorf("%w: observation evidence required", ErrReconciliationInput)
	}

	contextReason, compatible := compareReconciliationContext(localSide, providerSide)
	collector := &reconciliationItemCollector{}
	for _, base := range reconciliationBaseUnion(localSide, providerSide) {
		var itemErr error
		if !compatible {
			itemErr = collector.add(reconciliationContextMismatchItem(base, localSide, providerSide, contextReason))
		} else {
			itemErr = collector.compareBase(base, localSide, providerSide)
		}
		if itemErr != nil {
			return ComponentQuantityComparison{}, itemErr
		}
	}
	sort.SliceStable(collector.items, func(i, j int) bool {
		left, right := collector.items[i], collector.items[j]
		if leftKey, rightKey := left.Key.CanonicalKey(), right.Key.CanonicalKey(); leftKey != rightKey {
			return leftKey < rightKey
		}
		return left.Status < right.Status
	})

	result := ComponentQuantityComparison{Items: collector.items}
	if !compatible {
		result.Status = ReconciliationStatusIncomparable
		result.Reason = contextReason
		result.Complete = false
		return result, nil
	}
	result.Status, result.Complete = summarizeComponentQuantityComparison(collector.items)
	return result, nil
}

// reconciliationSource is one measure extracted from a side observation.
type reconciliationSource struct {
	base      string
	full      string
	key       metering.ComponentKey
	value     *metering.Decimal
	quality   string
	method    string
	semantics string
	tokenizer string
	ref       metering.ObservationRef
}

type reconciliationNormalizedSide struct {
	set          ReconciliationEvidenceSet
	currency     string
	period       string
	tokenizer    string
	subjectCore  string
	coverageKey  string
	qualifierKey string
	// groups is base identity -> canonical full key -> retained sources.
	groups map[string]map[string][]reconciliationSource
}

func normalizeReconciliationEvidenceSet(set ReconciliationEvidenceSet, label string) (reconciliationNormalizedSide, error) {
	if err := set.Subject.Validate(); err != nil {
		return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s subject: %v", ErrReconciliationInput, label, err)
	}
	if err := set.Payer.Validate(); err != nil {
		return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s payer: %v", ErrReconciliationInput, label, err)
	}
	side := reconciliationNormalizedSide{set: set, groups: map[string]map[string][]reconciliationSource{}}
	if set.Currency != "" {
		currency, err := economics.NormalizeCurrency(set.Currency)
		if err != nil {
			return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s currency: %v", ErrReconciliationInput, label, err)
		}
		side.currency = currency
	}
	if set.Scope != "" {
		if err := economics.ValidateSafeRef("reconciliation comparison scope", set.Scope); err != nil {
			return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s scope: %v", ErrReconciliationInput, label, err)
		}
	}
	if set.ChargeItemID != "" {
		if err := economics.ValidateSafeRef("reconciliation comparison charge item", set.ChargeItemID); err != nil {
			return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s charge item: %v", ErrReconciliationInput, label, err)
		}
	}
	if set.Tokenizer != "" {
		if err := economics.ValidateSafeRef("reconciliation comparison tokenizer", set.Tokenizer); err != nil {
			return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s tokenizer: %v", ErrReconciliationInput, label, err)
		}
		side.tokenizer = set.Tokenizer
	}
	coverageKey, err := canonicalReconciliationCoverageKey(set.Coverage)
	if err != nil {
		return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s coverage: %v", ErrReconciliationInput, label, err)
	}
	side.coverageKey = coverageKey
	qualifierKey, err := canonicalReconciliationQualifierKey(set.EffectiveQualifiers)
	if err != nil {
		return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s measurement context: %v", ErrReconciliationInput, label, err)
	}
	side.qualifierKey = qualifierKey
	side.period = reconciliationPeriodIdentity(set.Subject, set.PeriodID)
	side.subjectCore = reconciliationSubjectCore(set.Subject)

	if len(set.Observations) > MaxReconciliationObservations {
		return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s observations=%d max=%d", ErrReconciliationBoundExceeded, label, len(set.Observations), MaxReconciliationObservations)
	}
	measures := 0
	for i, observation := range set.Observations {
		if err := observation.Validate(); err != nil {
			return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s observation %d: %v", ErrReconciliationInput, label, i, err)
		}
		if !sameSubject(observation.Subject, set.Subject) {
			return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s observation %d subject does not match comparison subject", ErrReconciliationInput, label, i)
		}
		ref, err := observation.Ref(set.Subject.StoreID)
		if err != nil {
			return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s observation %d reference: %v", ErrReconciliationInput, label, i, err)
		}
		for _, measure := range observation.Measures {
			measures++
			if measures > MaxReconciliationMeasures {
				return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s measures=%d max=%d", ErrReconciliationBoundExceeded, label, measures, MaxReconciliationMeasures)
			}
			key, err := measure.Key.Normalize()
			if err != nil {
				return reconciliationNormalizedSide{}, fmt.Errorf("%w: %s observation %d measure: %v", ErrReconciliationInput, label, i, err)
			}
			source := reconciliationSource{
				base:      reconciliationBaseIdentity(key),
				full:      key.CanonicalKey(),
				key:       key,
				quality:   measure.Quality,
				method:    measure.MethodRef,
				semantics: observation.Semantics,
				tokenizer: set.Tokenizer,
				ref:       ref,
			}
			if measure.Value != nil {
				value := *measure.Value
				source.value = &value
			}
			if side.groups[source.base] == nil {
				side.groups[source.base] = map[string][]reconciliationSource{}
			}
			side.groups[source.base][source.full] = append(side.groups[source.base][source.full], source)
		}
	}
	return side, nil
}

// reconciliationBaseIdentity is the coarse meter identity used to detect that
// both sides report the same component before compatibility checks.
func reconciliationBaseIdentity(key metering.ComponentKey) string {
	return string(key.Direction) + "\x00" + key.Component + "\x00" + key.Unit
}

// reconciliationTokenizerSensitive reports whether one component is measured in
// tokens and therefore requires a declared tokenizer identity on both sides.
// Native non-token units (seconds, images, bytes, counts) do not depend on a
// tokenizer for comparability.
func reconciliationTokenizerSensitive(key metering.ComponentKey) bool {
	return key.Unit == metering.UnitToken
}

func reconciliationBaseUnion(local, provider reconciliationNormalizedSide) []string {
	seen := make(map[string]struct{}, len(local.groups)+len(provider.groups))
	for base := range local.groups {
		seen[base] = struct{}{}
	}
	for base := range provider.groups {
		seen[base] = struct{}{}
	}
	bases := make([]string, 0, len(seen))
	for base := range seen {
		bases = append(bases, base)
	}
	sort.Strings(bases)
	return bases
}

func compareReconciliationContext(local, provider reconciliationNormalizedSide) (ReconciliationComparisonReason, bool) {
	switch {
	case local.subjectCore != provider.subjectCore:
		return ReconciliationReasonSubjectMismatch, false
	case local.period != provider.period:
		return ReconciliationReasonPeriodMismatch, false
	case local.set.Payer != provider.set.Payer:
		return ReconciliationReasonPayerMismatch, false
	case local.currency != provider.currency:
		return ReconciliationReasonCurrencyMismatch, false
	case local.set.Scope != provider.set.Scope:
		return ReconciliationReasonScopeMismatch, false
	case local.set.ChargeItemID != provider.set.ChargeItemID:
		return ReconciliationReasonChargeMismatch, false
	case (local.tokenizer == "") != (provider.tokenizer == ""):
		// One side asserts a tokenizer identity and the other supplies none.
		// Missing semantic identity is not equivalence and no mapping exists.
		return ReconciliationReasonTokenizerMissing, false
	case local.tokenizer != provider.tokenizer:
		return ReconciliationReasonTokenizerMismatch, false
	case local.coverageKey != provider.coverageKey:
		return ReconciliationReasonCoverageMismatch, false
	case local.qualifierKey != provider.qualifierKey:
		return ReconciliationReasonContextMismatch, false
	default:
		return ReconciliationReasonNone, true
	}
}

func reconciliationSubjectCore(subject metering.SubjectRef) string {
	core := subject
	core.PeriodID = ""
	core.WindowID = ""
	core.ResetAt = time.Time{}
	encoded, err := json.Marshal(core)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func reconciliationPeriodIdentity(subject metering.SubjectRef, explicit string) string {
	parts := make([]string, 0, 4)
	if explicit != "" {
		parts = append(parts, "explicit="+explicit)
	}
	if subject.PeriodID != "" {
		parts = append(parts, "period="+subject.PeriodID)
	}
	if subject.WindowID != "" {
		parts = append(parts, "window="+subject.WindowID)
		parts = append(parts, "reset="+subject.ResetAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
	}
	return strings.Join(parts, "\x00")
}

func canonicalReconciliationCoverageKey(in []metering.ChargeCoverageRef) (string, error) {
	if len(in) == 0 {
		return "", nil
	}
	out := append([]metering.ChargeCoverageRef(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		return reconciliationCoverageSortKey(out[i]) < reconciliationCoverageSortKey(out[j])
	})
	seen := make(map[string]struct{}, len(out))
	for i, edge := range out {
		if err := edge.Validate(); err != nil {
			return "", fmt.Errorf("edge %d: %w", i, err)
		}
		key := reconciliationCoverageSortKey(edge)
		if _, exists := seen[key]; exists {
			return "", fmt.Errorf("duplicate coverage edge %s", key)
		}
		seen[key] = struct{}{}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func reconciliationCoverageSortKey(edge metering.ChargeCoverageRef) string {
	return strings.Join([]string{
		edge.Ref.StoreID, edge.Ref.ObservationID, fmt.Sprint(edge.Ref.Revision), edge.Ref.ChargeItemID, string(edge.Relation),
	}, "\x00")
}

func canonicalReconciliationQualifierKey(in []metering.Dimension) (string, error) {
	if len(in) == 0 {
		return "", nil
	}
	if len(in) > metering.MaxDimensions {
		return "", fmt.Errorf("qualifiers exceed %d", metering.MaxDimensions)
	}
	out := append([]metering.Dimension(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Value < out[j].Value
	})
	seen := make(map[string]struct{}, len(out))
	for i, dimension := range out {
		if err := dimension.Validate(); err != nil {
			return "", fmt.Errorf("qualifier %d: %w", i, err)
		}
		if _, exists := seen[dimension.Name]; exists {
			return "", fmt.Errorf("duplicate qualifier %q", dimension.Name)
		}
		seen[dimension.Name] = struct{}{}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

type reconciliationItemCollector struct {
	items []ComponentQuantityComparisonItem
}

func (c *reconciliationItemCollector) add(item ComponentQuantityComparisonItem) error {
	if len(c.items)+1 > MaxReconciliationItems {
		return fmt.Errorf("%w: result items exceed %d", ErrReconciliationBoundExceeded, MaxReconciliationItems)
	}
	c.items = append(c.items, item)
	return nil
}

func (c *reconciliationItemCollector) compareBase(base string, local, provider reconciliationNormalizedSide) error {
	localGroups := local.groups[base]
	providerGroups := provider.groups[base]
	localFulls := sortedReconciliationFullKeys(localGroups)
	providerFulls := sortedReconciliationFullKeys(providerGroups)

	localReason, localConflict := reconciliationConflictReason(localFulls, localGroups, ReconciliationReasonDuplicateLocal, ReconciliationReasonConflictingLocal)
	providerReason, providerConflict := reconciliationConflictReason(providerFulls, providerGroups, ReconciliationReasonDuplicateProvider, ReconciliationReasonConflictingProvider)
	if localConflict || providerConflict {
		reason := localReason
		if !localConflict {
			reason = providerReason
		}
		return c.add(ComponentQuantityComparisonItem{
			Key:      firstReconciliationSourceKey(localGroups, localFulls, providerGroups, providerFulls),
			Status:   ReconciliationStatusConflict,
			Reason:   reason,
			Local:    reconciliationEvidenceForGroups(localGroups, localFulls),
			Provider: reconciliationEvidenceForGroups(providerGroups, providerFulls),
		})
	}

	if len(localFulls) == 1 && len(providerFulls) == 1 && localFulls[0] != providerFulls[0] {
		localSource := localGroups[localFulls[0]][0]
		providerSource := providerGroups[providerFulls[0]][0]
		return c.add(ComponentQuantityComparisonItem{
			Key:      localSource.key,
			Status:   ReconciliationStatusIncomparable,
			Reason:   reconciliationKeyIncompatibility(localSource, providerSource),
			Local:    []ReconciliationQuantityEvidence{reconciliationEvidence(localSource)},
			Provider: []ReconciliationQuantityEvidence{reconciliationEvidence(providerSource)},
		})
	}

	for _, full := range reconciliationFullUnion(localFulls, providerFulls) {
		localSources := localGroups[full]
		providerSources := providerGroups[full]
		switch {
		case len(localSources) == 1 && len(providerSources) == 1:
			item, err := compareReconciliationPair(localSources[0], providerSources[0])
			if err != nil {
				return err
			}
			if err := c.add(item); err != nil {
				return err
			}
		case len(localSources) == 0:
			if err := c.add(ComponentQuantityComparisonItem{
				Key:      providerSources[0].key,
				Status:   ReconciliationStatusMissingLocal,
				Provider: reconciliationEvidenceForSources(providerSources),
			}); err != nil {
				return err
			}
		case len(providerSources) == 0:
			if err := c.add(ComponentQuantityComparisonItem{
				Key:    localSources[0].key,
				Status: ReconciliationStatusMissingProvider,
				Local:  reconciliationEvidenceForSources(localSources),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func compareReconciliationPair(local, provider reconciliationSource) (ComponentQuantityComparisonItem, error) {
	item := ComponentQuantityComparisonItem{
		Key:      local.key,
		Local:    []ReconciliationQuantityEvidence{reconciliationEvidence(local)},
		Provider: []ReconciliationQuantityEvidence{reconciliationEvidence(provider)},
	}
	if local.semantics != provider.semantics {
		item.Status = ReconciliationStatusIncomparable
		item.Reason = ReconciliationReasonSemanticsMismatch
		return item, nil
	}
	if local.tokenizer == "" && provider.tokenizer == "" && reconciliationTokenizerSensitive(local.key) {
		// Token-measured components require a declared tokenizer identity on
		// both sides; an undeclared pair cannot be compared.
		item.Status = ReconciliationStatusIncomparable
		item.Reason = ReconciliationReasonTokenizerRequired
		return item, nil
	}
	switch {
	case local.value != nil && provider.value != nil:
		signed, absolute, comparison, err := reconciliationExactDelta(*local.value, *provider.value)
		if err != nil {
			return ComponentQuantityComparisonItem{}, err
		}
		item.SignedDelta = &signed
		item.AbsoluteDelta = &absolute
		if comparison == 0 {
			item.Status = ReconciliationStatusMatched
		} else {
			item.Status = ReconciliationStatusDiscrepant
		}
		return item, nil
	case local.value == nil && provider.value == nil:
		item.Status = ReconciliationStatusPartial
		item.Reason = ReconciliationReasonValueUnavailableBoth
	case local.value == nil:
		item.Status = ReconciliationStatusPartial
		item.Reason = ReconciliationReasonValueUnavailableLocal
	default:
		item.Status = ReconciliationStatusPartial
		item.Reason = ReconciliationReasonValueUnavailableProvider
	}
	return item, nil
}

// reconciliationExactDelta is the authoritative exact provider-minus-local
// arithmetic shared by the live comparator and durable retention validation.
// It returns the signed delta, its absolute magnitude and the signed delta's
// comparison against zero (negative, zero or positive).
func reconciliationExactDelta(local, provider metering.Decimal) (signed metering.Decimal, absolute metering.Decimal, comparison int, err error) {
	signed, err = decimalSub(provider, local)
	if err != nil {
		return metering.Decimal{}, metering.Decimal{}, 0, fmt.Errorf("%w: provider-local quantity delta: %v", ErrReconciliationArithmetic, err)
	}
	comparison, err = decimalCompare(signed, metering.Decimal{Coefficient: "0"})
	if err != nil {
		return metering.Decimal{}, metering.Decimal{}, 0, fmt.Errorf("%w: quantity delta comparison: %v", ErrReconciliationArithmetic, err)
	}
	absolute = signed
	if comparison < 0 {
		absolute, err = decimalSub(metering.Decimal{Coefficient: "0"}, signed)
		if err != nil {
			return metering.Decimal{}, metering.Decimal{}, 0, fmt.Errorf("%w: absolute quantity delta: %v", ErrReconciliationArithmetic, err)
		}
	}
	return signed, absolute, comparison, nil
}

func reconciliationKeyIncompatibility(local, provider reconciliationSource) ReconciliationComparisonReason {
	if local.key.SchemaID != provider.key.SchemaID {
		return ReconciliationReasonSchemaMismatch
	}
	return ReconciliationReasonQualifierMismatch
}

func reconciliationConflictReason(fulls []string, groups map[string][]reconciliationSource, duplicate, conflicting ReconciliationComparisonReason) (ReconciliationComparisonReason, bool) {
	for _, full := range fulls {
		sources := groups[full]
		if len(sources) <= 1 {
			continue
		}
		payload := reconciliationSourcePayload(sources[0])
		same := true
		for _, source := range sources[1:] {
			if reconciliationSourcePayload(source) != payload {
				same = false
				break
			}
		}
		if same {
			return duplicate, true
		}
		return conflicting, true
	}
	return ReconciliationReasonNone, false
}

// reconciliationSourcePayload classifies duplicate versus conflicting
// evidence. Two sources with one meter identity but different values,
// qualities, methods or observation semantics conflict; identical payloads are
// exact duplicates. The side-level tokenizer identity is constant for every
// source of one side and is included so the payload names the full declared
// measurement identity.
func reconciliationSourcePayload(source reconciliationSource) string {
	value := ""
	if source.value != nil {
		value = source.value.CanonicalString()
	}
	return value + "\x00" + source.quality + "\x00" + source.method + "\x00" + source.semantics + "\x00" + source.tokenizer
}

func sortedReconciliationFullKeys(groups map[string][]reconciliationSource) []string {
	fulls := make([]string, 0, len(groups))
	for full := range groups {
		fulls = append(fulls, full)
	}
	sort.Strings(fulls)
	return fulls
}

func reconciliationFullUnion(local, provider []string) []string {
	seen := make(map[string]struct{}, len(local)+len(provider))
	for _, full := range local {
		seen[full] = struct{}{}
	}
	for _, full := range provider {
		seen[full] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for full := range seen {
		out = append(out, full)
	}
	sort.Strings(out)
	return out
}

func firstReconciliationSourceKey(localGroups map[string][]reconciliationSource, localFulls []string, providerGroups map[string][]reconciliationSource, providerFulls []string) metering.ComponentKey {
	if sources := reconciliationEvidenceForGroups(localGroups, localFulls); len(sources) > 0 {
		return sources[0].Key
	}
	if sources := reconciliationEvidenceForGroups(providerGroups, providerFulls); len(sources) > 0 {
		return sources[0].Key
	}
	return metering.ComponentKey{}
}

func reconciliationEvidenceForGroups(groups map[string][]reconciliationSource, fulls []string) []ReconciliationQuantityEvidence {
	var out []ReconciliationQuantityEvidence
	for _, full := range fulls {
		out = append(out, reconciliationEvidenceForSources(groups[full])...)
	}
	return out
}

func reconciliationEvidenceForSources(sources []reconciliationSource) []ReconciliationQuantityEvidence {
	out := make([]ReconciliationQuantityEvidence, 0, len(sources))
	for _, source := range sources {
		out = append(out, reconciliationEvidence(source))
	}
	return out
}

func reconciliationEvidence(source reconciliationSource) ReconciliationQuantityEvidence {
	evidence := ReconciliationQuantityEvidence{
		Key: source.key.Clone(), Quality: source.quality, MethodRef: source.method, Observation: source.ref,
	}
	if source.value != nil {
		value := *source.value
		evidence.Value = &value
	}
	return evidence
}

func reconciliationContextMismatchItem(base string, local, provider reconciliationNormalizedSide, reason ReconciliationComparisonReason) ComponentQuantityComparisonItem {
	localFulls := sortedReconciliationFullKeys(local.groups[base])
	providerFulls := sortedReconciliationFullKeys(provider.groups[base])
	return ComponentQuantityComparisonItem{
		Key:      firstReconciliationSourceKey(local.groups[base], localFulls, provider.groups[base], providerFulls),
		Status:   ReconciliationStatusIncomparable,
		Reason:   reason,
		Local:    reconciliationEvidenceForGroups(local.groups[base], localFulls),
		Provider: reconciliationEvidenceForGroups(provider.groups[base], providerFulls),
	}
}

// summarizeComponentQuantityComparison rolls item outcomes up without ever
// promoting missing, partial, incomparable or conflicting evidence to
// complete/matched.
func summarizeComponentQuantityComparison(items []ComponentQuantityComparisonItem) (ReconciliationComparisonStatus, bool) {
	if len(items) == 0 {
		return ReconciliationStatusPartial, false
	}
	var hasConflict, hasIncomparable, hasPartial, hasMissing, hasDiscrepant bool
	for _, item := range items {
		switch item.Status {
		case ReconciliationStatusConflict:
			hasConflict = true
		case ReconciliationStatusIncomparable:
			hasIncomparable = true
		case ReconciliationStatusPartial:
			hasPartial = true
		case ReconciliationStatusMissingLocal, ReconciliationStatusMissingProvider:
			hasMissing = true
		case ReconciliationStatusDiscrepant:
			hasDiscrepant = true
		}
	}
	switch {
	case hasConflict:
		return ReconciliationStatusConflict, false
	case hasIncomparable:
		return ReconciliationStatusIncomparable, false
	case hasPartial || hasMissing:
		return ReconciliationStatusPartial, false
	case hasDiscrepant:
		return ReconciliationStatusDiscrepant, true
	default:
		return ReconciliationStatusMatched, true
	}
}
