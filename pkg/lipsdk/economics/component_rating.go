package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	// MaxRatingRules bounds the immutable rule material accepted from a source
	// or a host configuration. The bound keeps publication and replay work
	// deterministic without imposing a provider-specific catalog.
	MaxRatingRules             = 1024
	MaxRatingTiers             = 128
	MaxRatingConditions        = 32
	LegacyScalarSemanticsV1    = "legacy_scalar_per_million_tokens_v1"
	TariffSnapshotContentRefV1 = "tariff-snapshot:v1"
)

var (
	ErrInvalidRatingRule       = errors.New("economics: invalid rating rule")
	ErrInvalidTariffSnapshot   = errors.New("economics: invalid tariff snapshot")
	ErrTariffSnapshotConflict  = errors.New("economics: tariff snapshot content conflict")
	ErrRatingRuleOverlap       = errors.New("economics: overlapping rating rules")
	ErrRatingRuleNonConserving = errors.New("economics: non-conserving rating allocation")
)

// RatingRuleKind describes the bounded operations supported by the reference
// post-usage evaluator. A rule may combine a linear price with block and
// minimum modifiers; fixed rules use FixedAmount and FixedScope instead.
type RatingRuleKind string

const (
	RatingRuleLinear     RatingRuleKind = "linear"
	RatingRuleFixed      RatingRuleKind = "fixed"
	RatingRuleBlock      RatingRuleKind = "block"
	RatingRuleMinimum    RatingRuleKind = "minimum"
	RatingRuleAllUnits   RatingRuleKind = "all_units"
	RatingRuleGraduated  RatingRuleKind = "graduated"
	RatingRuleConversion RatingRuleKind = "conversion"
)

func (k RatingRuleKind) IsKnown() bool {
	switch k {
	case "", RatingRuleLinear, RatingRuleFixed, RatingRuleBlock, RatingRuleMinimum, RatingRuleAllUnits, RatingRuleGraduated, RatingRuleConversion:
		return true
	default:
		return false
	}
}

// RatingSelectionScope controls which quantity drives threshold/tier
// selection. It is intentionally separate from the quantity that is charged:
// whole-context rules can select a rate from aggregate context while pricing
// only a declared billable component.
type RatingSelectionScope string

const (
	SelectionBillableQuantity RatingSelectionScope = "billable_quantity"
	SelectionWholeContext     RatingSelectionScope = "whole_context"
	SelectionPeriod           RatingSelectionScope = "period"
)

func (s RatingSelectionScope) IsKnown() bool {
	switch s {
	case "", SelectionBillableQuantity, SelectionWholeContext, SelectionPeriod:
		return true
	default:
		return false
	}
}

// TierMode controls whether the selected tier price is applied to all units
// or each graduated slice independently.
type TierMode string

const (
	TierAllUnits  TierMode = "all_units"
	TierGraduated TierMode = "graduated"
)

func (m TierMode) IsKnown() bool {
	return m == "" || m == TierAllUnits || m == TierGraduated
}

// QualifierCondition is an exact match against one effective qualifier. A
// missing qualifier never matches the condition; the billing domain turns
// that miss into an explicit fail-closed diagnostic.
type QualifierCondition struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (c QualifierCondition) Validate() error {
	if err := (metering.Dimension{Name: c.Name, Value: c.Value}).Validate(); err != nil {
		return fmt.Errorf("%w: qualifier: %v", ErrInvalidRatingRule, err)
	}
	return nil
}

// RatingTier is one threshold/price pair. A nil UpTo is the final unbounded
// tier. UnitPrice is the amount for PricePer units; a nil PricePer means one
// unit. RateNumerator/RateDenominator provide an exact rational alternative
// when a terminating decimal is not available.
type RatingTier struct {
	UpTo            *metering.Decimal `json:"up_to,omitempty"`
	UnitPrice       *metering.Decimal `json:"unit_price,omitempty"`
	PricePer        *metering.Decimal `json:"price_per,omitempty"`
	RateNumerator   *metering.Decimal `json:"rate_numerator,omitempty"`
	RateDenominator *metering.Decimal `json:"rate_denominator,omitempty"`
}

func (t RatingTier) Validate() error {
	if t.UpTo != nil {
		if err := validateNonNegativeDecimal("tier up_to", t.UpTo); err != nil {
			return err
		}
	}
	if t.UnitPrice == nil && (t.RateNumerator == nil || t.RateDenominator == nil) {
		return fmt.Errorf("%w: tier price required", ErrInvalidRatingRule)
	}
	if t.UnitPrice != nil && (t.RateNumerator != nil || t.RateDenominator != nil) {
		return fmt.Errorf("%w: tier unit price and rational rate are mutually exclusive", ErrInvalidRatingRule)
	}
	if t.UnitPrice != nil {
		if err := validateNonNegativeDecimal("tier unit price", t.UnitPrice); err != nil {
			return err
		}
	}
	if t.RateNumerator != nil {
		if err := validateNonNegativeDecimal("tier rate numerator", t.RateNumerator); err != nil {
			return err
		}
	}
	if t.RateDenominator != nil {
		if err := validatePositiveDecimal("tier rate denominator", t.RateDenominator); err != nil {
			return err
		}
	}
	if t.PricePer != nil {
		if err := validatePositiveDecimal("tier price_per", t.PricePer); err != nil {
			return err
		}
	}
	return nil
}

func (t RatingTier) clone() RatingTier {
	out := t
	clone := func(in *metering.Decimal) *metering.Decimal {
		if in == nil {
			return nil
		}
		v := *in
		return &v
	}
	out.UpTo = clone(t.UpTo)
	out.UnitPrice = clone(t.UnitPrice)
	out.PricePer = clone(t.PricePer)
	out.RateNumerator = clone(t.RateNumerator)
	out.RateDenominator = clone(t.RateDenominator)
	return out
}

// RatingRule is provider-neutral immutable tariff material. Component rules
// require Component; fixed rules require FixedAmount and FixedScope. A rule's
// Currency is explicit even when it matches the containing tariff so currency
// mismatch cannot silently become a zero charge.
type RatingRule struct {
	ID               string                 `json:"id"`
	Kind             RatingRuleKind         `json:"kind,omitempty"`
	Component        *metering.ComponentKey `json:"component,omitempty"`
	Currency         string                 `json:"currency"`
	UnitPrice        *metering.Decimal      `json:"unit_price,omitempty"`
	PricePer         *metering.Decimal      `json:"price_per,omitempty"`
	RateNumerator    *metering.Decimal      `json:"rate_numerator,omitempty"`
	RateDenominator  *metering.Decimal      `json:"rate_denominator,omitempty"`
	FixedAmount      *metering.Decimal      `json:"fixed_amount,omitempty"`
	FixedScope       FixedFeeScope          `json:"fixed_scope,omitempty"`
	BlockSize        *metering.Decimal      `json:"block_size,omitempty"`
	MinimumAmount    *metering.Decimal      `json:"minimum_amount,omitempty"`
	RoundingScope    RoundingScope          `json:"rounding_scope,omitempty"`
	RoundingPolicy   RoundingPolicy         `json:"rounding_policy,omitempty"`
	SelectionScope   RatingSelectionScope   `json:"selection_scope,omitempty"`
	TierMode         TierMode               `json:"tier_mode,omitempty"`
	Conditions       []QualifierCondition   `json:"conditions,omitempty"`
	Tiers            []RatingTier           `json:"tiers,omitempty"`
	ConversionSchema string                 `json:"conversion_schema,omitempty"`
	IncludedUnit     bool                   `json:"included_unit,omitempty"`
}

// ComponentRatingRule and Rule are descriptive aliases used by adapters that
// prefer the domain vocabulary. They preserve one canonical representation.
type (
	ComponentRatingRule = RatingRule
	Rule                = RatingRule
)

func (r RatingRule) Validate(tariffCurrency string) error {
	if err := validatePublicRef("rating rule id", r.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRatingRule, err)
	}
	if !r.Kind.IsKnown() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidRatingRule, r.Kind)
	}
	if err := validateRatingRuleKindFields(r); err != nil {
		return err
	}
	if err := NormalizeCurrencyRequired(r.Currency); err != nil {
		return fmt.Errorf("%w: currency: %v", ErrInvalidRatingRule, err)
	}
	// A source may intentionally carry a mixed-currency rule while it is being
	// staged. Publication preserves the mismatch; the billing evaluator
	// reports it as ErrRateCurrencyMismatch instead of treating the line as
	// absent or zero.
	if r.Component == nil && r.FixedAmount == nil {
		return fmt.Errorf("%w: component or fixed amount required", ErrInvalidRatingRule)
	}
	if r.Component != nil {
		if err := r.Component.Validate(); err != nil {
			return fmt.Errorf("%w: component: %v", ErrInvalidRatingRule, err)
		}
		if r.FixedAmount != nil || r.FixedScope != "" {
			return fmt.Errorf("%w: component rule cannot carry fixed fee fields", ErrInvalidRatingRule)
		}
	}
	if r.FixedAmount != nil {
		if err := validateNonNegativeDecimal("fixed amount", r.FixedAmount); err != nil {
			return err
		}
		if !r.FixedScope.IsKnown() || r.FixedScope == "" {
			return fmt.Errorf("%w: fixed scope required", ErrInvalidRatingRule)
		}
	}
	if r.UnitPrice != nil && (r.RateNumerator != nil || r.RateDenominator != nil) {
		return fmt.Errorf("%w: unit price and rational rate are mutually exclusive", ErrInvalidRatingRule)
	}
	if r.UnitPrice == nil && (r.RateNumerator != nil || r.RateDenominator != nil) {
		if r.RateNumerator == nil || r.RateDenominator == nil {
			return fmt.Errorf("%w: rate numerator and denominator must be supplied together", ErrInvalidRatingRule)
		}
	}
	if r.UnitPrice != nil {
		if err := validateNonNegativeDecimal("unit price", r.UnitPrice); err != nil {
			return err
		}
	}
	if r.RateNumerator != nil {
		if err := validateNonNegativeDecimal("rate numerator", r.RateNumerator); err != nil {
			return err
		}
	}
	if r.RateDenominator != nil {
		if err := validatePositiveDecimal("rate denominator", r.RateDenominator); err != nil {
			return err
		}
	}
	if r.PricePer != nil {
		if err := validatePositiveDecimal("price_per", r.PricePer); err != nil {
			return err
		}
	}
	if r.BlockSize != nil {
		if err := validatePositiveDecimal("block size", r.BlockSize); err != nil {
			return err
		}
	}
	if r.MinimumAmount != nil {
		if err := validateNonNegativeDecimal("minimum amount", r.MinimumAmount); err != nil {
			return err
		}
	}
	if r.RoundingScope != "" && !r.RoundingScope.IsKnown() {
		return fmt.Errorf("%w: unknown rounding scope %q", ErrInvalidRatingRule, r.RoundingScope)
	}
	if !r.RoundingPolicy.IsKnown() {
		return fmt.Errorf("%w: unknown rounding policy %q", ErrInvalidRatingRule, r.RoundingPolicy)
	}
	if r.RoundingScope != "" && r.RoundingPolicy == RoundingUnspecified {
		return fmt.Errorf("%w: rounding policy required with scope", ErrInvalidRatingRule)
	}
	if r.RoundingPolicy != RoundingUnspecified && r.RoundingScope == "" {
		return fmt.Errorf("%w: rounding scope required with policy", ErrInvalidRatingRule)
	}
	if !r.SelectionScope.IsKnown() {
		return fmt.Errorf("%w: unknown selection scope %q", ErrInvalidRatingRule, r.SelectionScope)
	}
	if !r.TierMode.IsKnown() {
		return fmt.Errorf("%w: unknown tier mode %q", ErrInvalidRatingRule, r.TierMode)
	}
	if len(r.Conditions) > MaxRatingConditions || len(r.Tiers) > MaxRatingTiers {
		return fmt.Errorf("%w: rule bound exceeded", ErrInvalidRatingRule)
	}
	seen := make(map[string]struct{}, len(r.Conditions))
	for i, condition := range r.Conditions {
		if err := condition.Validate(); err != nil {
			return fmt.Errorf("%w: conditions[%d]: %v", ErrInvalidRatingRule, i, err)
		}
		if _, exists := seen[condition.Name]; exists {
			return fmt.Errorf("%w: duplicate condition %q", ErrInvalidRatingRule, condition.Name)
		}
		seen[condition.Name] = struct{}{}
	}
	if len(r.Tiers) > 0 {
		if r.Component == nil {
			return fmt.Errorf("%w: tiers require component rule", ErrInvalidRatingRule)
		}
		if r.FixedAmount != nil {
			return fmt.Errorf("%w: tiers cannot combine with fixed fee", ErrInvalidRatingRule)
		}
		if r.TierMode == "" {
			return fmt.Errorf("%w: tier mode required", ErrInvalidRatingRule)
		}
		var prior *metering.Decimal
		for i, tier := range r.Tiers {
			if err := tier.Validate(); err != nil {
				return fmt.Errorf("%w: tiers[%d]: %v", ErrInvalidRatingRule, i, err)
			}
			if tier.UpTo != nil && prior != nil {
				p, err := prior.ToRat()
				if err != nil {
					return fmt.Errorf("%w: tier threshold: %v", ErrInvalidRatingRule, err)
				}
				u, err := tier.UpTo.ToRat()
				if err != nil || u.Cmp(p) <= 0 {
					return fmt.Errorf("%w: tier thresholds must increase", ErrInvalidRatingRule)
				}
			}
			if tier.UpTo != nil {
				if i == len(r.Tiers)-1 {
					return fmt.Errorf("%w: final tier must be unbounded", ErrInvalidRatingRule)
				}
				n := *tier.UpTo
				prior = &n
			} else if i != len(r.Tiers)-1 {
				return fmt.Errorf("%w: only final tier may be unbounded", ErrInvalidRatingRule)
			}
		}
	}
	if r.Kind == RatingRuleConversion && strings.TrimSpace(r.ConversionSchema) == "" {
		return fmt.Errorf("%w: conversion schema required", ErrInvalidRatingRule)
	}
	return nil
}

// validateRatingRuleKindFields keeps the declared operation authoritative.
// Empty Kind is the only legacy inference mode: fixed material infers fixed,
// tier material infers the declared TierMode, and ordinary component material
// infers linear (including the historical block/minimum modifiers). Explicit
// kinds may not rely on a conflicting field being silently preferred by the
// evaluator.
func validateRatingRuleKindFields(r RatingRule) error {
	if r.Kind == "" {
		if strings.TrimSpace(r.ConversionSchema) != "" {
			return fmt.Errorf("%w: legacy rule %q cannot carry conversion schema without explicit conversion kind", ErrInvalidRatingRule, r.ID)
		}
		if r.TierMode != "" && len(r.Tiers) == 0 {
			return fmt.Errorf("%w: legacy rule %q cannot infer tier operation without tiers", ErrInvalidRatingRule, r.ID)
		}
		if len(r.Tiers) != 0 {
			if r.TierMode != TierAllUnits && r.TierMode != TierGraduated {
				return fmt.Errorf("%w: legacy rule %q requires a tier mode with tiers", ErrInvalidRatingRule, r.ID)
			}
			if r.UnitPrice != nil || r.PricePer != nil || r.RateNumerator != nil || r.RateDenominator != nil || r.BlockSize != nil || r.MinimumAmount != nil {
				return fmt.Errorf("%w: legacy tier rule %q cannot carry direct rate or modifier fields", ErrInvalidRatingRule, r.ID)
			}
		}
		return nil
	}
	hasBaseRate := r.UnitPrice != nil || r.RateNumerator != nil || r.RateDenominator != nil
	hasTierMaterial := len(r.Tiers) != 0 || r.TierMode != ""
	switch r.Kind {
	case RatingRuleFixed:
		if r.FixedAmount == nil || r.FixedScope == "" {
			return fmt.Errorf("%w: fixed rule %q requires fixed amount and scope", ErrInvalidRatingRule, r.ID)
		}
		if r.Component != nil || hasBaseRate || r.PricePer != nil || r.BlockSize != nil || r.MinimumAmount != nil || hasTierMaterial || strings.TrimSpace(r.ConversionSchema) != "" {
			return fmt.Errorf("%w: fixed rule %q cannot carry component, rate, modifier, tier or conversion fields", ErrInvalidRatingRule, r.ID)
		}
		return nil
	case RatingRuleLinear:
		if r.Component == nil || !hasBaseRate {
			return fmt.Errorf("%w: linear rule %q requires component and unit rate", ErrInvalidRatingRule, r.ID)
		}
		if hasTierMaterial || strings.TrimSpace(r.ConversionSchema) != "" {
			return fmt.Errorf("%w: linear rule %q cannot carry tier or conversion fields", ErrInvalidRatingRule, r.ID)
		}
	case RatingRuleBlock:
		if r.Component == nil || !hasBaseRate || r.BlockSize == nil {
			return fmt.Errorf("%w: block rule %q requires component, unit rate and block size", ErrInvalidRatingRule, r.ID)
		}
		if r.MinimumAmount != nil || hasTierMaterial || strings.TrimSpace(r.ConversionSchema) != "" {
			return fmt.Errorf("%w: block rule %q cannot carry minimum, tier or conversion fields", ErrInvalidRatingRule, r.ID)
		}
	case RatingRuleMinimum:
		if r.Component == nil || !hasBaseRate || r.MinimumAmount == nil {
			return fmt.Errorf("%w: minimum rule %q requires component, unit rate and minimum amount", ErrInvalidRatingRule, r.ID)
		}
		if r.BlockSize != nil || hasTierMaterial || strings.TrimSpace(r.ConversionSchema) != "" {
			return fmt.Errorf("%w: minimum rule %q cannot carry block, tier or conversion fields", ErrInvalidRatingRule, r.ID)
		}
	case RatingRuleAllUnits:
		if r.Component == nil || r.TierMode != TierAllUnits || len(r.Tiers) == 0 {
			return fmt.Errorf("%w: all_units rule %q requires all_units tier mode and tiers", ErrInvalidRatingRule, r.ID)
		}
		if hasBaseRate || r.PricePer != nil || r.BlockSize != nil || r.MinimumAmount != nil || strings.TrimSpace(r.ConversionSchema) != "" {
			return fmt.Errorf("%w: all_units rule %q cannot carry direct rate, modifier or conversion fields", ErrInvalidRatingRule, r.ID)
		}
	case RatingRuleGraduated:
		if r.Component == nil || r.TierMode != TierGraduated || len(r.Tiers) == 0 {
			return fmt.Errorf("%w: graduated rule %q requires graduated tier mode and tiers", ErrInvalidRatingRule, r.ID)
		}
		if hasBaseRate || r.PricePer != nil || r.BlockSize != nil || r.MinimumAmount != nil || strings.TrimSpace(r.ConversionSchema) != "" {
			return fmt.Errorf("%w: graduated rule %q cannot carry direct rate, modifier or conversion fields", ErrInvalidRatingRule, r.ID)
		}
	case RatingRuleConversion:
		if r.Component == nil || strings.TrimSpace(r.ConversionSchema) == "" {
			return fmt.Errorf("%w: conversion rule %q requires component and conversion schema", ErrInvalidRatingRule, r.ID)
		}
		if hasBaseRate || r.PricePer != nil || r.FixedAmount != nil || r.FixedScope != "" || r.BlockSize != nil || r.MinimumAmount != nil || hasTierMaterial {
			return fmt.Errorf("%w: conversion rule %q cannot carry direct rate, fixed, modifier or tier fields", ErrInvalidRatingRule, r.ID)
		}
	}
	if r.Kind != RatingRuleFixed && (r.FixedAmount != nil || r.FixedScope != "") {
		return fmt.Errorf("%w: %s rule %q cannot carry fixed fee fields", ErrInvalidRatingRule, r.Kind, r.ID)
	}
	return nil
}

func validateNonNegativeDecimal(name string, value *metering.Decimal) error {
	if value == nil {
		return fmt.Errorf("%w: %s required", ErrInvalidRatingRule, name)
	}
	n, err := value.Normalize()
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidRatingRule, name, err)
	}
	if strings.HasPrefix(n.Coefficient, "-") {
		return fmt.Errorf("%w: %s cannot be negative", ErrInvalidRatingRule, name)
	}
	return nil
}

func validatePositiveDecimal(name string, value *metering.Decimal) error {
	if err := validateNonNegativeDecimal(name, value); err != nil {
		return err
	}
	n, _ := value.Normalize()
	if n.Coefficient == "0" {
		return fmt.Errorf("%w: %s must be positive", ErrInvalidRatingRule, name)
	}
	return nil
}

func (r RatingRule) Clone() RatingRule {
	out := r
	cloneDecimal := func(in *metering.Decimal) *metering.Decimal {
		if in == nil {
			return nil
		}
		v := *in
		return &v
	}
	if r.Component != nil {
		key := r.Component.Clone()
		out.Component = &key
	}
	out.UnitPrice = cloneDecimal(r.UnitPrice)
	out.PricePer = cloneDecimal(r.PricePer)
	out.RateNumerator = cloneDecimal(r.RateNumerator)
	out.RateDenominator = cloneDecimal(r.RateDenominator)
	out.FixedAmount = cloneDecimal(r.FixedAmount)
	out.BlockSize = cloneDecimal(r.BlockSize)
	out.MinimumAmount = cloneDecimal(r.MinimumAmount)
	out.Conditions = append([]QualifierCondition(nil), r.Conditions...)
	if r.Tiers != nil {
		out.Tiers = make([]RatingTier, len(r.Tiers))
		for i, tier := range r.Tiers {
			out.Tiers[i] = tier.clone()
		}
	}
	return out
}

// TariffSnapshot is immutable rule material bound to one rating identity. The
// content hash is over canonical tariff fields, excluding ContentRef itself.
// ContentRef is a durable resolver key and can be supplied by a catalog.
type TariffSnapshot struct {
	Ref                 RatingSnapshotRef    `json:"ref"`
	Currency            string               `json:"currency"`
	CatalogVersion      string               `json:"catalog_version,omitempty"`
	Rules               []RatingRule         `json:"rules"`
	EffectiveQualifiers []metering.Dimension `json:"effective_qualifiers,omitempty"`
	LegacySemantics     string               `json:"legacy_semantics,omitempty"`
	Content             SnapshotContentRef   `json:"content"`
}

// NewTariffSnapshot creates canonical tariff material. It is convenient for
// tests and adapters that already validated their source; callers needing a
// publication error should use BuildTariffSnapshot.
func NewTariffSnapshot(ref RatingSnapshotRef, currency string, rules []RatingRule) TariffSnapshot {
	snapshot, _ := BuildTariffSnapshot(ref, currency, rules)
	return snapshot
}

// BuildTariffSnapshot validates and content-addresses one tariff snapshot.
func BuildTariffSnapshot(ref RatingSnapshotRef, currency string, rules []RatingRule) (TariffSnapshot, error) {
	s := TariffSnapshot{Ref: ref, Currency: currency, Rules: append([]RatingRule(nil), rules...)}
	if err := s.Validate(); err != nil {
		return TariffSnapshot{}, err
	}
	return s.withContent(), nil
}

func (s TariffSnapshot) Validate() error {
	if err := validateVersionRef("tariff", s.Ref.VersionRef); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTariffSnapshot, err)
	}
	if err := validatePublicRef("tariff rater_id", s.Ref.RaterID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTariffSnapshot, err)
	}
	if err := NormalizeCurrencyRequired(s.Currency); err != nil {
		return fmt.Errorf("%w: currency: %v", ErrInvalidTariffSnapshot, err)
	}
	if s.CatalogVersion != "" {
		if err := validatePublicRef("catalog version", s.CatalogVersion); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTariffSnapshot, err)
		}
	}
	if s.LegacySemantics != "" {
		if err := validatePublicRef("legacy semantics", s.LegacySemantics); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTariffSnapshot, err)
		}
	}
	if len(s.Rules) > MaxRatingRules {
		return fmt.Errorf("%w: rule bound exceeded", ErrInvalidTariffSnapshot)
	}
	seen := make(map[string]struct{}, len(s.Rules))
	for i, rule := range s.Rules {
		if err := rule.Validate(s.Currency); err != nil {
			return fmt.Errorf("%w: rules[%d]: %w", ErrInvalidTariffSnapshot, i, err)
		}
		if _, exists := seen[rule.ID]; exists {
			return fmt.Errorf("%w: duplicate rule %q", ErrInvalidTariffSnapshot, rule.ID)
		}
		seen[rule.ID] = struct{}{}
	}
	if err := validateDimensions(s.EffectiveQualifiers, ErrInvalidTariffSnapshot); err != nil {
		return err
	}
	if s.Content.ContentRef != "" || s.Content.ContentHash != "" {
		if err := s.Content.Validate(); err != nil {
			return fmt.Errorf("%w: content: %v", ErrInvalidTariffSnapshot, err)
		}
		if s.Content.ContentHash != s.contentHash() {
			return fmt.Errorf("%w: content hash mismatch", ErrTariffSnapshotConflict)
		}
	}
	return nil
}

func NormalizeCurrencyRequired(currency string) error {
	if strings.TrimSpace(currency) == "" {
		return fmt.Errorf("currency required")
	}
	_, err := NormalizeCurrency(currency)
	return err
}

func (s TariffSnapshot) contentHash() string {
	canonical := s.canonicalBody()
	// Effective/fetched timestamps describe publication metadata, not tariff
	// economics. Identity replays with refreshed timestamps must retain the
	// same content address, matching the existing scalar catalog semantics.
	identity := RatingSnapshotRef{
		VersionRef: VersionRef{ID: canonical.Ref.ID, Version: canonical.Ref.Version},
		RaterID:    canonical.Ref.RaterID,
	}
	body := struct {
		Ref                 RatingSnapshotRef    `json:"ref"`
		Currency            string               `json:"currency"`
		CatalogVersion      string               `json:"catalog_version,omitempty"`
		Rules               []RatingRule         `json:"rules"`
		EffectiveQualifiers []metering.Dimension `json:"effective_qualifiers,omitempty"`
		LegacySemantics     string               `json:"legacy_semantics,omitempty"`
	}{identity, canonical.Currency, canonical.CatalogVersion, canonical.Rules, canonical.EffectiveQualifiers, canonical.LegacySemantics}
	b, _ := json.Marshal(body)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func (s TariffSnapshot) withContent() TariffSnapshot {
	out := s.Clone()
	out.Content = SnapshotContentRef{ContentRef: TariffSnapshotContentRefV1 + "://" + out.Ref.ID + "/" + out.Ref.Version, ContentHash: out.contentHash()}
	return out
}

func (s TariffSnapshot) Clone() TariffSnapshot {
	out := s
	out.Rules = make([]RatingRule, len(s.Rules))
	for i, rule := range s.Rules {
		out.Rules[i] = rule.Clone()
	}
	out.EffectiveQualifiers = append([]metering.Dimension(nil), s.EffectiveQualifiers...)
	return out
}

// canonicalBody returns a deep copy with every order-bearing tariff field in
// its canonical order. Content hashing uses this form so publication and
// replay agree even when a source catalog supplied rules in a different
// order.
func (s TariffSnapshot) canonicalBody() TariffSnapshot {
	out := s.Clone()
	slices.SortFunc(out.EffectiveQualifiers, func(a, b metering.Dimension) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Value, b.Value)
	})
	slices.SortFunc(out.Rules, func(a, b RatingRule) int { return strings.Compare(a.ID, b.ID) })
	for i := range out.Rules {
		slices.SortFunc(out.Rules[i].Conditions, func(a, b QualifierCondition) int {
			if a.Name != b.Name {
				return strings.Compare(a.Name, b.Name)
			}
			return strings.Compare(a.Value, b.Value)
		})
	}
	return out
}

// Canonical returns a validated deep copy with deterministic rule/qualifier
// ordering and a content-addressed resolver reference.
func (s TariffSnapshot) Canonical() (TariffSnapshot, error) {
	if err := s.Validate(); err != nil {
		return TariffSnapshot{}, err
	}
	out := s.canonicalBody()
	out.Content.ContentRef = TariffSnapshotContentRefV1 + "://" + out.Ref.ID + "/" + out.Ref.Version
	out.Content.ContentHash = out.contentHash()
	return out, nil
}

func (s TariffSnapshot) ContentHash() string { return s.contentHash() }

// RatingCatalogView is the public snapshot-source payload. Legacy catalogs
// can continue to publish only Currency/CatalogVersion; billing composition
// adapts their scalar cards into named legacy rules.
type ratingCatalogViewFields struct {
	Currency            string               `json:"currency,omitempty"`
	CatalogVersion      string               `json:"catalog_version,omitempty"`
	Rules               []RatingRule         `json:"rules,omitempty"`
	EffectiveQualifiers []metering.Dimension `json:"effective_qualifiers,omitempty"`
	LegacySemantics     string               `json:"legacy_semantics,omitempty"`
}

func (v RatingCatalogView) Clone() RatingCatalogView {
	out := v
	out.Rules = make([]RatingRule, len(v.Rules))
	for i, rule := range v.Rules {
		out.Rules[i] = rule.Clone()
	}
	out.EffectiveQualifiers = append([]metering.Dimension(nil), v.EffectiveQualifiers...)
	return out
}

// Validate validates supplied generic catalog material without requiring a
// snapshot identity; the source envelope supplies ID/version separately.
func (v RatingCatalogView) Validate() error {
	if v.Currency != "" {
		if err := NormalizeCurrencyRequired(v.Currency); err != nil {
			return err
		}
	}
	if len(v.Rules) > MaxRatingRules {
		return fmt.Errorf("%w: rule bound exceeded", ErrInvalidTariffSnapshot)
	}
	seen := make(map[string]struct{}, len(v.Rules))
	for i, rule := range v.Rules {
		if err := rule.Validate(v.Currency); err != nil {
			return fmt.Errorf("%w: rules[%d]: %v", ErrInvalidTariffSnapshot, i, err)
		}
		if _, exists := seen[rule.ID]; exists {
			return fmt.Errorf("%w: duplicate rule %q", ErrInvalidTariffSnapshot, rule.ID)
		}
		seen[rule.ID] = struct{}{}
	}
	if v.LegacySemantics != "" {
		if err := validatePublicRef("legacy semantics", v.LegacySemantics); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTariffSnapshot, err)
		}
	}
	return validateDimensions(v.EffectiveQualifiers, ErrInvalidTariffSnapshot)
}

// Tariff materializes a catalog view under an immutable rating identity.
func (v RatingCatalogView) Tariff(ref RatingSnapshotRef) (TariffSnapshot, error) {
	if err := v.Validate(); err != nil {
		return TariffSnapshot{}, err
	}
	s := TariffSnapshot{Ref: ref, Currency: v.Currency, CatalogVersion: v.CatalogVersion, Rules: v.Rules, EffectiveQualifiers: v.EffectiveQualifiers, LegacySemantics: v.LegacySemantics}
	if err := s.Validate(); err != nil {
		return TariffSnapshot{}, err
	}
	return s.withContent(), nil
}
