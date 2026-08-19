package metering_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	lipmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestProjectWorkloadIdentityDistinguishesPrimaryAndAuxiliaryFacts(t *testing.T) {
	t.Parallel()
	primary, err := coremetering.ProjectWorkloadIdentity(lipmetering.Fact{
		Lifecycle: lipmetering.LifecycleBackendAttempt,
		Scope:     scope.PrincipalScopeView{Origin: scope.OriginClient},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if primary.Class != billing.WorkloadClassPrimary || primary.Role != "" {
		t.Fatalf("primary identity = %+v", primary)
	}
	auxiliary, err := coremetering.ProjectWorkloadIdentity(lipmetering.Fact{
		Lifecycle: lipmetering.LifecycleBackendAttempt,
		Scope:     scope.PrincipalScopeView{Origin: scope.OriginInternal},
	}, string(billing.WorkloadRoleCompactionContinuityExtractor))
	if err != nil {
		t.Fatal(err)
	}
	if auxiliary.Class != billing.WorkloadClassAuxiliary || auxiliary.Role != billing.WorkloadRole(billing.WorkloadRoleCompactionContinuityExtractor) {
		t.Fatalf("auxiliary identity = %+v", auxiliary)
	}
}

func TestProjectWorkloadIdentityRejectsUntrustedRoleOrPrimaryRole(t *testing.T) {
	t.Parallel()
	auxFact := lipmetering.Fact{Lifecycle: lipmetering.LifecycleAuxiliaryRequest}
	for _, role := range []string{"", "user prompt", "compaction_continuity_extractor:raw"} {
		if _, err := coremetering.ProjectWorkloadIdentity(auxFact, role); !errors.Is(err, billing.ErrInvalidWorkloadIdentity) {
			t.Fatalf("role %q error = %v, want ErrInvalidWorkloadIdentity", role, err)
		}
	}
	primaryFact := lipmetering.Fact{Lifecycle: lipmetering.LifecycleBackendAttempt, Scope: scope.PrincipalScopeView{Origin: scope.OriginClient}}
	if _, err := coremetering.ProjectWorkloadIdentity(primaryFact, string(billing.WorkloadRoleCompactionContinuityExtractor)); err == nil {
		t.Fatal("primary fact accepted auxiliary role")
	}
}
