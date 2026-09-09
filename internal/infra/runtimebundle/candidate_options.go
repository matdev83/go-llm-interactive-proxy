package runtimebundle

import (
	"slices"

	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// mergeCandidateBuildOptions overlays generation-owned FeatureLifecycles and
// Extensions onto a shallow copy of process options without mutating Process.
// ReplaceCandidateSurface true replaces FeatureLifecycles/Extensions even when
// nil/empty; false means nil overlay fields are "no override".
func mergeCandidateBuildOptions(process *BuildOptions, overlay *BuildOptions) *BuildOptions {
	if process == nil && overlay == nil {
		return nil
	}
	if process == nil {
		out := *overlay
		out.FeatureLifecycles = slices.Clone(overlay.FeatureLifecycles)
		out.Extensions = cloneExtensionsOptions(overlay.Extensions)
		out.ReplaceCandidateSurface = false
		return &out
	}
	out := *process
	out.Extensions = cloneExtensionsOptions(process.Extensions)
	if overlay != nil {
		if overlay.ReplaceCandidateSurface {
			out.FeatureLifecycles = slices.Clone(overlay.FeatureLifecycles)
			out.Extensions = cloneExtensionsOptions(overlay.Extensions)
			out.FeaturePlanes = overlay.FeaturePlanes
			out.CorePorts = overlay.CorePorts
		} else {
			if overlay.FeatureLifecycles != nil {
				out.FeatureLifecycles = slices.Clone(overlay.FeatureLifecycles)
			}
			if hasExtensionOverlay(overlay.Extensions) {
				out.Extensions = cloneExtensionsOptions(overlay.Extensions)
			}
			if !overlay.FeaturePlanes.IsZero() {
				out.FeaturePlanes = overlay.FeaturePlanes
			}
			if overlay.CorePorts.InterleavedProcessor != nil {
				out.CorePorts.InterleavedProcessor = overlay.CorePorts.InterleavedProcessor
			}
			if overlay.CorePorts.ConversationReader != nil {
				out.CorePorts.ConversationReader = overlay.CorePorts.ConversationReader
			}
			if overlay.CorePorts.CompactionDetector != nil {
				out.CorePorts.CompactionDetector = overlay.CorePorts.CompactionDetector
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
	// Extensions carry no overlay surfaces: every concrete feature state
	// flows via ordinary planes, lifecycles, or fixed consumer ports.
	return false
}

func cloneExtensionsOptions(in ExtensionsOptions) ExtensionsOptions {
	return ExtensionsOptions{}
}

func prependGeneratedLifecycles(gen, overlay []lipplugin.Lifecycle) []lipplugin.Lifecycle {
	if gen == nil && overlay == nil {
		return nil
	}
	out := make([]lipplugin.Lifecycle, 0, len(gen)+len(overlay))
	out = append(out, gen...)
	out = append(out, overlay...)
	return out
}
