package domain

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidAmount            = errors.New("invalid amount")
	ErrInvalidDimension         = errors.New("invalid dimension")
	ErrInvalidWindow            = errors.New("invalid window")
	ErrInvalidRule              = errors.New("invalid rule")
	ErrInvalidStatus            = errors.New("invalid status")
	ErrUnsupportedAlgorithm     = errors.New("unsupported window algorithm")
	ErrUnsupportedRuleUnit      = errors.New("unsupported rule unit")
	ErrRetiredMonetaryAuthority = errors.New("retired monetary usage authority rule; migration required")
	ErrUnsupportedAuthority     = errors.New("unsupported authority requirement")
	ErrUnsupportedFailurePolicy = errors.New("unsupported failure behavior")
)

func (a Amount) Validate() error {
	if strings.EqualFold(string(a.Unit), "money_nano") || strings.EqualFold(string(a.Unit), "money-nano") || strings.EqualFold(string(a.Unit), "moneynano") {
		return fmt.Errorf("%w: unit %q", ErrRetiredMonetaryAuthority, a.Unit)
	}
	if !a.Unit.IsKnown() {
		return fmt.Errorf("%w: unsupported unit %q", ErrInvalidAmount, a.Unit)
	}
	if a.Value < 0 {
		return fmt.Errorf("%w: negative value %d", ErrInvalidAmount, a.Value)
	}
	return nil
}

func validateDimensionsMatcher(m DimensionsMatcher) error {
	for key := range m.Labels {
		if !IsSafeLabelKey(key) {
			return fmt.Errorf("%w: invalid policy label key %q", ErrInvalidDimension, key)
		}
	}
	return nil
}

func (r Rule) Validate() error {
	kind := strings.ToLower(strings.TrimSpace(string(r.Kind)))
	unitName := strings.ToLower(strings.TrimSpace(string(r.Unit)))
	if kind == "budget" || kind == "spend_cap" || kind == "spend-cap" || unitName == "money_nano" || unitName == "money-nano" || unitName == "moneynano" {
		return fmt.Errorf("%w: kind=%q unit=%q", ErrRetiredMonetaryAuthority, r.Kind, r.Unit)
	}
	if !isSafeRuleID(r.ID) {
		return fmt.Errorf("%w: invalid id %q", ErrInvalidRule, r.ID)
	}
	if !r.Kind.IsKnown() {
		return fmt.Errorf("%w: invalid kind %q", ErrInvalidRule, r.Kind)
	}
	if !r.Mode.IsKnown() {
		return fmt.Errorf("%w: invalid mode %q", ErrInvalidRule, r.Mode)
	}
	if !r.AuthorityRequirement.IsKnown() {
		return fmt.Errorf("%w: invalid authority requirement %q", ErrInvalidRule, r.AuthorityRequirement)
	}
	if !r.FailureBehavior.IsKnown() {
		return fmt.Errorf("%w: invalid failure behavior %q", ErrInvalidRule, r.FailureBehavior)
	}
	if r.Unit != "" && !r.Unit.IsKnown() {
		return fmt.Errorf("%w: %q", ErrUnsupportedRuleUnit, r.Unit)
	}
	if err := r.Limit.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRule, err)
	}
	unit := r.Unit
	if unit == "" {
		unit = r.Limit.Unit
	}
	if unit != r.Limit.Unit {
		return fmt.Errorf("%w: rule unit %q does not match limit unit %q", ErrInvalidRule, unit, r.Limit.Unit)
	}
	if err := validateDimensionsMatcher(r.Match); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRule, err)
	}
	if r.Window.configured() {
		if _, err := r.Window.Bounds(r.Window.Anchor); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRule, err)
		}
	}

	switch r.Kind {
	case RuleKindQuota, RuleKindRate:
		// Quantity-only rules have no currency-bearing limits.
	default:
		return fmt.Errorf("%w: invalid kind %q", ErrInvalidRule, r.Kind)
	}

	if err := r.validatePerspectiveLifecycleBasis(); err != nil {
		return err
	}

	return nil
}

func (s AuthorityStatus) Validate() error {
	if !s.State.IsKnown() {
		return fmt.Errorf("%w: unknown state %q", ErrInvalidStatus, s.State)
	}
	if !s.Reason.IsKnown() {
		return fmt.Errorf("%w: unknown reason %q", ErrInvalidStatus, s.Reason)
	}
	switch s.State {
	case AuthorityStateReady:
		if s.Reason != StatusReasonNone {
			return fmt.Errorf("%w: ready state must use none reason", ErrInvalidStatus)
		}
	case AuthorityStateDisabled:
		if s.Reason != StatusReasonNone && s.Reason != StatusReasonDisabledByConfig {
			return fmt.Errorf("%w: disabled state reason %q is not safe", ErrInvalidStatus, s.Reason)
		}
	case AuthorityStateAdvisoryOnly:
		if s.Reason != StatusReasonAdvisoryOnly && s.Reason != StatusReasonNone {
			return fmt.Errorf("%w: advisory-only state reason %q is not safe", ErrInvalidStatus, s.Reason)
		}
	case AuthorityStateUnavailable:
		if s.Reason != StatusReasonBackingUnavailable && s.Reason != StatusReasonValidationFailed && s.Reason != StatusReasonNone {
			return fmt.Errorf("%w: unavailable state reason %q is not safe", ErrInvalidStatus, s.Reason)
		}
	case AuthorityStateDegraded:
		if s.Reason != StatusReasonBackingDegraded && s.Reason != StatusReasonValidationFailed && s.Reason != StatusReasonNone {
			return fmt.Errorf("%w: degraded state reason %q is not safe", ErrInvalidStatus, s.Reason)
		}
	}
	return nil
}

func (c AuthorityConfig) Validate() error {
	if !c.Backing.IsKnown() {
		return fmt.Errorf("%w: unknown backing capability %q", ErrInvalidStatus, c.Backing)
	}
	for _, rule := range c.Rules {
		if err := rule.Validate(); err != nil {
			return err
		}
		if rule.Mode == RuleModeStrict && !c.Backing.StrictCapable() {
			return fmt.Errorf("%w: strict rules require atomic backing", ErrInvalidStatus)
		}
	}
	if !c.Status().State.IsKnown() {
		return fmt.Errorf("%w: unsupported status state", ErrInvalidStatus)
	}
	return nil
}

func isSafeRuleID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.:-", r) {
			continue
		}
		return false
	}
	return true
}
