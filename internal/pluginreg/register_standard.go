package pluginreg

import "sync"

var (
	registerStd sync.Once
	registerErr error
)

// RegisterStandardBundle installs backend, frontend, and feature factories for the standard
// distribution (same set as [github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk.StandardDistributionRequirements]).
// Concrete wiring lives in backends_install.go, frontends_install.go, and features_install.go in this package.
// Safe to call multiple times (tests, main).
func RegisterStandardBundle() error {
	registerStd.Do(func() {
		registerErr = InstallStandardBundleOn(Default)
	})
	return registerErr
}
