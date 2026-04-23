package runtimebundle

import coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"

// BootstrapApp is the standard-distribution bootstrap application from [NewBootstrapApp]
// ([coreruntime.App]).
type BootstrapApp = coreruntime.App

// BootstrapOptions is [coreruntime.Options] for explicit root wiring.
type BootstrapOptions = coreruntime.Options

// NewBootstrapApp delegates to [coreruntime.New] so cmd/lipstd can call bootstrap without
// a direct import of internal/core/runtime.
func NewBootstrapApp(opts BootstrapOptions) (*BootstrapApp, error) {
	return coreruntime.New(opts)
}
