// Package standardbundle exposes explicit standard plugin bundle composition helpers.
package standardbundle

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// InstallOn registers all standard distribution factories on reg.
// keys supplies default upstream API keys when plugin YAML omits api_key (see [pluginreg.InstallStandardBundleOn]).
func InstallOn(reg *pluginreg.Registry, keys pluginreg.UpstreamAPIKeys) error {
	if reg == nil {
		return fmt.Errorf("standardbundle: nil registry")
	}
	return pluginreg.InstallStandardBundleOn(reg, keys)
}

// Bundle returns the concrete standard distribution bundle without backend registrations.
// Backends bind environment/default keys, so callers compose them with [BackendBundle].
func Bundle() pluginreg.Bundle {
	return pluginreg.StandardBundle()
}

// BackendBundle returns the concrete standard backend registrations with keys bound.
func BackendBundle(keys pluginreg.UpstreamAPIKeys) pluginreg.Bundle {
	return pluginreg.StandardBackendBundle(keys)
}
