// Package standardbundle documents the explicit standard plugin bundle.
//
// Factory tables live in package pluginreg (standard_table.go, InstallStandardBundleOn).
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
