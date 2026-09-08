package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
)

// secretGuardFromPlanes reconstructs the generation-composed secret-guard
// engine posture and diagnostics inventory purely from the ordinary frozen
// plane surface (PlaneSecretGuardExecution). Guards are intentionally not
// read here: the request snapshot overlays guards from its feature planes on
// every access (SecretGuardPlane/SecretGuardExecutionPlane), so carrying them
// in the plane would be dead weight. It returns a zero plane and nil
// inventory when the generation carries no composed execution config
// (secret guard disabled).
func secretGuardFromPlanes(frozen lipfeature.FrozenPlaneSet) (extensions.SecretGuardPlane, *diag.InventoryExtras) {
	execCfg := lipfeature.Get(frozen, lipfeature.PlaneSecretGuardExecution)
	if execCfg == nil || execCfg.IsZero() {
		return extensions.SecretGuardPlane{}, nil
	}
	var categories []string
	if len(execCfg.SourceCategories) > 0 {
		categories = append([]string(nil), execCfg.SourceCategories...)
	}
	return extensions.SecretGuardPlane{
			MatcherResolver:    execCfg.MatcherResolver,
			DecisionObserver:   execCfg.DecisionObserver,
			AuditFailurePolicy: execCfg.AuditFailurePolicy,
			AccessMode:         execCfg.AccessMode,
			ConfigVersion:      execCfg.ConfigVersion,
		}, &diag.InventoryExtras{
			SecretGuardCatalogEntryCount: execCfg.CatalogEntryCount,
			SecretGuardSourceCategories:  categories,
			SecretGuardAccessMode:        execCfg.AccessMode,
			SecretGuardAction:            execCfg.CatalogAction,
		}
}
