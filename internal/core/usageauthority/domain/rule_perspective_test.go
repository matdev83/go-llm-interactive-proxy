package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestRuleValidation_RequiresDualPlaneOrLegacyBasis(t *testing.T) {
	t.Parallel()

	base := domain.Rule{
		ID:    "quota-1",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Window: domain.WindowSpec{
			Algorithm: domain.WindowAlgorithmFixed,
			Size:      time.Hour,
			Anchor:    time.Unix(0, 0).UTC(),
		},
	}

	t.Run("ambiguous empty fields rejected", func(t *testing.T) {
		t.Parallel()
		err := base.Validate()
		if err == nil {
			t.Fatal("expected migration error")
		}
		if !errors.Is(err, domain.ErrInvalidRule) {
			t.Fatalf("err=%v", err)
		}
		if !strings.Contains(err.Error(), "legacy_provider_preferred_attempt") {
			t.Fatalf("want actionable migration hint, got %v", err)
		}
	})

	t.Run("legacy basis alone allowed", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Basis = domain.BasisLegacyProviderPreferredAttempt
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dual-plane customer logical_request", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Perspective = metering.PerspectiveCustomer
		r.LifecycleScope = metering.LifecycleLogicalRequest
		r.Basis = domain.BasisFrontendIngress
		r.Namespace = domain.NamespaceDefault
		r.Version = "1"
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dual-plane operator backend_attempt", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Perspective = metering.PerspectiveOperator
		r.LifecycleScope = metering.LifecycleBackendAttempt
		r.Basis = domain.BasisBackendIngress
		r.Namespace = domain.NamespaceDefault
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("illegal combo customer+backend_attempt", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Perspective = metering.PerspectiveCustomer
		r.LifecycleScope = metering.LifecycleBackendAttempt
		r.Basis = domain.BasisBackendIngress
		r.Namespace = domain.NamespaceDefault
		if err := r.Validate(); err == nil {
			t.Fatal("expected illegal combo rejection")
		}
	})

	t.Run("partial dual-plane rejected", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Perspective = metering.PerspectiveCustomer
		if err := r.Validate(); err == nil {
			t.Fatal("expected rejection for incomplete dual-plane fields")
		}
	})

	t.Run("dual-plane missing namespace rejected", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Perspective = metering.PerspectiveOperator
		r.LifecycleScope = metering.LifecycleBackendAttempt
		r.Basis = domain.BasisBackendIngress
		if err := r.Validate(); err == nil {
			t.Fatal("expected namespace required")
		}
	})
}
