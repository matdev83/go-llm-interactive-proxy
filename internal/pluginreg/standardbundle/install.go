// Package standardbundle documents the explicit standard plugin bundle.
//
// Registration is implemented in package pluginreg ([pluginreg.RegisterStandardBundle]);
// factory tables live in pluginreg/standard_table.go to avoid import cycles.
package standardbundle

import "github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"

// Install registers all standard distribution factories; safe to call multiple times (sync.Once inside).
func Install() error {
	return pluginreg.RegisterStandardBundle()
}
