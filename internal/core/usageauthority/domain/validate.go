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
	ErrUnsupportedAuthority     = errors.New("unsupported authority requirement")
	ErrUnsupportedFailurePolicy = errors.New("unsupported failure behavior")
)

func (a Amount) Validate() error {
	if !a.Unit.IsKnown() {
		return fmt.Errorf("%w: unsupported unit %q", ErrInvalidAmount, a.Unit)
	}
	if a.Value < 0 {
		return fmt.Errorf("%w: negative value %d", ErrInvalidAmount, a.Value)
	}
	if a.IsMoney() {
		if strings.TrimSpace(a.Currency) == "" {
			return fmt.Errorf("%w: currency required for money amounts", ErrInvalidAmount)
		}
		return nil
	}
	if a.Currency != "" {
		return fmt.Errorf("%w: currency is only valid for money amounts", ErrInvalidAmount)
	}
	return nil
}

func (m DimensionsMatcher) Validate() error {
	for key := range m.Labels {
		if !isSafeLabelKey(key) {
			return fmt.Errorf("%w: invalid policy label key %q", ErrInvalidDimension, key)
		}
	}
	return nil
}

func (r Rule) Validate() error {
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
	if err := r.Match.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRule, err)
	}
	if r.Window.configured() {
		if _, err := r.Window.Bounds(r.Window.Anchor); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRule, err)
		}
	}

	switch r.Kind {
	case RuleKindBudget, RuleKindSpendCap:
		if r.Unit != AmountUnitMoneyNano || !r.Limit.IsMoney() {
			return fmt.Errorf("%w: budget and spend-cap rules must use money nano-unit amounts with currency", ErrInvalidRule)
		}
		if strings.TrimSpace(r.Currency) == "" || strings.TrimSpace(r.Limit.Currency) == "" {
			return fmt.Errorf("%w: budget and spend-cap rules require currency", ErrInvalidRule)
		}
		if !strings.EqualFold(r.Currency, r.Limit.Currency) {
			return fmt.Errorf("%w: currency mismatch %q != %q", ErrInvalidRule, r.Currency, r.Limit.Currency)
		}
	case RuleKindQuota, RuleKindRate:
		if r.Unit == AmountUnitMoneyNano || r.Limit.IsMoney() || strings.TrimSpace(r.Currency) != "" || strings.TrimSpace(r.Limit.Currency) != "" {
			return fmt.Errorf("%w: quota and rate rules must not use money", ErrInvalidRule)
		}
	default:
		return fmt.Errorf("%w: invalid kind %q", ErrInvalidRule, r.Kind)
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
