package openaicompat

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicaps"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// HostedCaps exposes the standard hosted capabilities for test suites.
func HostedCaps() lipapi.BackendCaps {
	return openaicaps.HostedFull
}

// AttachWireProofForTest exposes attachWireProof for testing wire fast-path behavior.
func AttachWireProofForTest(be execbackend.Backend, spec BackendSpec, pool *credpool.Pool, inv modelinventory.Provider) execbackend.Backend {
	return attachWireProof(be, spec, pool, inv)
}
