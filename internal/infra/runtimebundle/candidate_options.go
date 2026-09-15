package runtimebundle

import (
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationprojection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
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
			out.FeaturePlanes, out.CorePorts = overlay.FeaturePlanes, overlay.CorePorts
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
			if p := overlay.CorePorts.InterleavedProcessor; p != nil {
				out.CorePorts.InterleavedProcessor = p
			}
			if r := overlay.CorePorts.ConversationReader; r != nil {
				out.CorePorts.ConversationReader, out.CorePorts.ConversationReaderStockOrigin = r, overlay.CorePorts.ConversationReaderStockOrigin
			}
			if d := overlay.CorePorts.CompactionDetector; d != nil {
				out.CorePorts.CompactionDetector = d
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

func hasExtensionOverlay(e ExtensionsOptions) bool                  { return false }
func cloneExtensionsOptions(in ExtensionsOptions) ExtensionsOptions { return ExtensionsOptions{} }

func prependGeneratedLifecycles(gen, overlay []lipplugin.Lifecycle) []lipplugin.Lifecycle {
	if gen == nil && overlay == nil {
		return nil
	}
	out := make([]lipplugin.Lifecycle, 0, len(gen)+len(overlay))
	out = append(out, gen...)
	out = append(out, overlay...)
	return out
}

// resolveCandidateConvReader resolves the candidate conversation reader and certifies
// stock origin only when the resolved reader is genuinely identical to the standard
// features stock reader. Forged stock origin assertions on non-stock readers are declined.
func resolveCandidateConvReader(ports featurehost.CorePorts, sf *featurehost.Runtime) (conversationprojection.Reader, bool) {
	convReader, stockConvReader := ports.ConversationReader, false
	if sf != nil {
		if stockReader := sf.ConversationReader(); convReader == nil {
			convReader, stockConvReader = stockReader, stockReader != nil
		} else if isSameConversationReader(convReader, stockReader) {
			stockConvReader = ports.ConversationReaderStockOrigin && stockReader != nil
		}
	}
	return convReader, stockConvReader
}

func isSameConversationReader(a, b conversationprojection.Reader) bool {
	defer func() { _ = recover() }()
	return a == b
}
