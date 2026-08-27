package runtimebundle

import lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"

// mergeCandidateBuildOptions overlays generation-owned FeatureLifecycles and
// Extensions onto a shallow copy of process options without mutating Process.
// ReplaceCandidateSurface true replaces FeatureLifecycles/Extensions even when
// nil/empty; false means nil overlay fields are "no override".
func mergeCandidateBuildOptions(process *BuildOptions, overlay *BuildOptions) *BuildOptions {
	if process == nil {
		return overlay
	}
	out := *process
	if overlay != nil {
		if overlay.ReplaceCandidateSurface {
			out.FeatureLifecycles = append([]lipplugin.Lifecycle(nil), overlay.FeatureLifecycles...)
			out.Extensions = overlay.Extensions
			out.FeaturePlanes = overlay.FeaturePlanes
		} else {
			if overlay.FeatureLifecycles != nil {
				out.FeatureLifecycles = append([]lipplugin.Lifecycle(nil), overlay.FeatureLifecycles...)
			}
			if hasExtensionOverlay(overlay.Extensions) {
				out.Extensions = overlay.Extensions
			}
			if !overlay.FeaturePlanes.IsZero() {
				out.FeaturePlanes = overlay.FeaturePlanes
			}
		}
		if overlay.WireModel != nil {
			out.WireModel = overlay.WireModel
		}
	}
	// Always keep process factory catalog / infra / testing / production / auth.
	out.PluginRegistry = process.PluginRegistry
	out.Startup = process.Startup
	out.Infra = process.Infra
	out.Auth = process.Auth
	out.Policy = process.Policy
	out.Diagnostics = process.Diagnostics
	out.Testing = process.Testing
	out.Production = process.Production
	out.ReplaceCandidateSurface = false
	return &out
}

func hasExtensionOverlay(e ExtensionsOptions) bool {
	return len(e.SessionOpeners) > 0 ||
		len(e.WorkspaceResolvers) > 0 ||
		len(e.ToolCatalogFilters) > 0 ||
		len(e.ToolCallPolicies) > 0 ||
		len(e.ToolCallFinalizers) > 0 ||
		e.ToolCallFinalizationMaxArgsBytes > 0 ||
		len(e.RouteHintProviders) > 0 ||
		len(e.CompletionGates) > 0 ||
		len(e.SecretGuards) > 0 ||
		e.SecretGuardEnvironment != nil ||
		e.SecretDecisionObserver != nil
}
