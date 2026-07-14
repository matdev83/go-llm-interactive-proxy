package domain

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// AuthorityNamespace isolates persisted limit/decision identity (Phase 7).
const (
	NamespaceLegacy  = "legacy"
	NamespaceDefault = "usage-authority/v2"
)

// MeteringBasis identifies which metering boundary or compatibility path a rule
// uses when selecting amounts (requirements 1.3, 9.2, 9.6).
type MeteringBasis string

const (
	// BasisLegacyProviderPreferredAttempt is the explicit compatibility basis
	// for pre-Phase-7 rules. Perspective/lifecycle may be empty when this is set.
	BasisLegacyProviderPreferredAttempt MeteringBasis = "legacy_provider_preferred_attempt"

	BasisFrontendIngress MeteringBasis = MeteringBasis(metering.BoundaryFrontendIngress)
	BasisBackendIngress  MeteringBasis = MeteringBasis(metering.BoundaryBackendIngress)
	BasisBackendEgress   MeteringBasis = MeteringBasis(metering.BoundaryBackendEgress)
	BasisFrontendEgress  MeteringBasis = MeteringBasis(metering.BoundaryFrontendEgress)
)

func (b MeteringBasis) IsKnown() bool {
	switch b {
	case BasisLegacyProviderPreferredAttempt,
		BasisFrontendIngress, BasisBackendIngress, BasisBackendEgress, BasisFrontendEgress:
		return true
	default:
		return strings.HasPrefix(string(b), "derived:") && len(strings.TrimSpace(string(b))) > len("derived:")
	}
}

func (b MeteringBasis) IsLegacyCompatibility() bool {
	return b == BasisLegacyProviderPreferredAttempt
}

// IsDualPlaneConfigured reports whether the rule carries explicit dual-plane fields.
func (r Rule) IsDualPlaneConfigured() bool {
	return strings.TrimSpace(string(r.Perspective)) != "" ||
		strings.TrimSpace(string(r.LifecycleScope)) != "" ||
		strings.TrimSpace(string(r.Basis)) != "" ||
		strings.TrimSpace(r.Namespace) != "" ||
		strings.TrimSpace(r.Version) != ""
}

// validatePerspectiveLifecycleBasis enforces Phase 7 rule metadata (1.3, 1.5, 9.6).
func (r Rule) validatePerspectiveLifecycleBasis() error {
	persp := metering.EconomicPerspective(strings.TrimSpace(string(r.Perspective)))
	life := metering.LifecycleScope(strings.TrimSpace(string(r.LifecycleScope)))
	basis := MeteringBasis(strings.TrimSpace(string(r.Basis)))
	ns := strings.TrimSpace(r.Namespace)
	ver := strings.TrimSpace(r.Version)

	allEmpty := persp == "" && life == "" && basis == "" && ns == "" && ver == ""
	if allEmpty {
		return fmt.Errorf("%w: rule %q missing perspective/lifecycle_scope/basis; set basis %q for legacy compatibility or declare dual-plane fields",
			ErrInvalidRule, r.ID, BasisLegacyProviderPreferredAttempt)
	}

	if basis.IsLegacyCompatibility() {
		if persp != "" && !persp.IsKnown() {
			return fmt.Errorf("%w: invalid perspective %q", ErrInvalidRule, persp)
		}
		if life != "" && !life.IsKnown() {
			return fmt.Errorf("%w: invalid lifecycle_scope %q", ErrInvalidRule, life)
		}
		if ns != "" && !isSafeAuthorityNamespace(ns) {
			return fmt.Errorf("%w: invalid namespace %q", ErrInvalidRule, ns)
		}
		return nil
	}

	if basis == "" || persp == "" || life == "" {
		return fmt.Errorf("%w: rule %q requires perspective, lifecycle_scope, and basis (or basis %q alone for legacy)",
			ErrInvalidRule, r.ID, BasisLegacyProviderPreferredAttempt)
	}
	if !basis.IsKnown() {
		return fmt.Errorf("%w: unknown basis %q", ErrInvalidRule, basis)
	}
	if !persp.IsKnown() || persp == metering.PerspectiveNone {
		return fmt.Errorf("%w: invalid perspective %q for enforceable rule", ErrInvalidRule, persp)
	}
	if !life.IsKnown() {
		return fmt.Errorf("%w: invalid lifecycle_scope %q", ErrInvalidRule, life)
	}
	if err := validatePerspectiveLifecycleCombo(persp, life); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRule, err)
	}
	if ns == "" {
		return fmt.Errorf("%w: rule %q requires namespace for dual-plane rules", ErrInvalidRule, r.ID)
	}
	if !isSafeAuthorityNamespace(ns) {
		return fmt.Errorf("%w: invalid namespace %q", ErrInvalidRule, ns)
	}
	if ver != "" && !isSafeRuleVersion(ver) {
		return fmt.Errorf("%w: invalid version %q", ErrInvalidRule, ver)
	}
	return nil
}

func validatePerspectiveLifecycleCombo(persp metering.EconomicPerspective, life metering.LifecycleScope) error {
	switch {
	case persp == metering.PerspectiveCustomer && (life == metering.LifecycleLogicalRequest || life == metering.LifecycleAuxiliaryRequest):
		return nil
	case persp == metering.PerspectiveOperator && (life == metering.LifecycleBackendAttempt || life == metering.LifecycleAuxiliaryRequest):
		return nil
	default:
		return fmt.Errorf("illegal perspective/lifecycle combination %q/%q", persp, life)
	}
}

func isSafeAuthorityNamespace(ns string) bool {
	if ns == "" || len(ns) > 128 {
		return false
	}
	for _, r := range ns {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '/' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func isSafeRuleVersion(ver string) bool {
	if ver == "" || len(ver) > 64 {
		return false
	}
	for _, r := range ver {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
