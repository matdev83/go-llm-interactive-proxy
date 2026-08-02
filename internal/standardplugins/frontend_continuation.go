package standardplugins

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	frontopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	"gopkg.in/yaml.v3"
)

// StandardContinuationWiringFactory creates lifecycle-owned continuation
// resources for the standard OpenResponses frontend. The factory is kept in
// the composition root so the frontend plugin only consumes SDK ports.
func StandardContinuationWiringFactory(_ *config.Config) lipsdk.ContinuationMountWiringFactory {
	return func(frontendID, _ string, n yaml.Node) (lipsdk.ContinuationMountWiring, error) {
		if frontendID != frontopenresponses.ID {
			return lipsdk.ContinuationMountWiring{}, nil
		}
		cfg, err := frontopenresponses.DecodeConfig(n)
		if err != nil {
			return lipsdk.ContinuationMountWiring{}, fmt.Errorf("standard continuation wiring: %w", err)
		}
		depth := cfg.Continuation.MaxChainDepth
		if depth <= 0 {
			depth = frontopenresponses.DefaultMaxChainDepth
		}
		maxBytes := cfg.Continuation.MaxMaterializedBytes
		if maxBytes <= 0 {
			maxBytes = frontopenresponses.DefaultMaxMaterializedBytes
		}
		store := lipcont.NewMemoryStoreWithLimits(lipcont.StorageLimits{
			MaxRecords:     10_000,
			MaxBytes:       maxBytes,
			MaxRecordBytes: 16 << 20,
			MaxChainDepth:  depth,
		})
		return lipsdk.ContinuationMountWiring{
			Store:    store,
			Resolver: lipcont.NewResolver(store, lipcont.Bounds{MaxChainDepth: depth, MaxMaterializedBytes: maxBytes, MaxMaterializedItems: 100_000}),
			Close:    store.Close,
		}, nil
	}
}
