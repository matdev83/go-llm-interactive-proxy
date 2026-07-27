package pluginreg

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"

// GenericBackendFactoryDeps is the host dependency surface for essential
// in-process backend factories. It must not name Codex, OpenCode, ACP-product,
// or other provider-specific collaborators.
//
// External executable plugins do not receive this type: they receive opaque
// YAML, secrets, and pkg/lipsdk/backendplugin.RuntimePolicy only.
type GenericBackendFactoryDeps struct {
	Identity identity.Config
}
