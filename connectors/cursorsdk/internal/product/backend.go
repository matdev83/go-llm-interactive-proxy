package product

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// Backend is the connector-owned adapter surface consumed by the executable-plugin
// service layer. It replaces the root execbackend.Backend type for this module.
type Backend struct {
	BackendPrefixes         []string
	EnforcesMaxOutputTokens bool
	ModelInventory          modelinventory.Provider
	ResolveCaps             func(ctx context.Context, call lipapi.Call, cand AttemptCandidate) lipapi.BackendCaps
	Open                    func(ctx context.Context, call lipapi.Call, cand AttemptCandidate) (lipapi.ManagedEventStream, error)
	Close                   func() error
}
