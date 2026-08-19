package metering

import (
	"fmt"
	"strings"

	corebilling "github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	lipmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// ProjectWorkloadIdentity maps trusted auxiliary lineage onto the same bounded
// billing identity used by normal terminal/report records. The fact itself
// remains the existing metering contract: lifecycle and safe scope correlation
// carry classification, while the role is supplied from trusted lineage.
func ProjectWorkloadIdentity(f lipmetering.Fact, auxiliaryRole string) (corebilling.WorkloadIdentity, error) {
	auxiliary := f.Lifecycle == lipmetering.LifecycleAuxiliaryRequest || f.Scope.Origin == scope.OriginInternal
	auxiliaryRole = strings.TrimSpace(auxiliaryRole)
	if !auxiliary {
		if auxiliaryRole != "" {
			return corebilling.WorkloadIdentity{}, fmt.Errorf("metering: primary fact cannot carry auxiliary workload role")
		}
		return corebilling.WorkloadIdentity{Class: corebilling.WorkloadClassPrimary}, nil
	}
	return corebilling.WorkloadIdentityFromAuxiliaryRole(auxiliaryRole)
}
