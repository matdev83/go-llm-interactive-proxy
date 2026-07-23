package runtimebundle

import (
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// NewGenerationBundleForTest builds a minimal GenerationBundle for binder tests.
func NewGenerationBundleForTest(models *modelregistry.Runtime, catalog *modelcatalog.CatalogRuntime) *GenerationBundle {
	b := &GenerationBundle{
		models: generationModelViews{
			models:  models,
			catalog: catalog,
		},
	}
	b.ownership.cond = sync.NewCond(&b.ownership.mu)
	return b
}

// NewGenerationBundleWithLedgerForTest builds a GenerationBundle that owns ledger
// directly (Task 3.3 lifecycle / ownership tests).
func NewGenerationBundleWithLedgerForTest(ledger *ResourceLedger) *GenerationBundle {
	b := &GenerationBundle{
		ownership: generationOwnership{ledger: ledger},
	}
	b.ownership.cond = sync.NewCond(&b.ownership.mu)
	return b
}

// NewGenerationBundleWithPublicationForTest builds a GenerationBundle with
// publication snapshots for defensive-copy assertions (auth + registrations).
func NewGenerationBundleWithPublicationForTest(auth []httpauth.Provider, regs []lipsdk.Registration) *GenerationBundle {
	return newGenerationBundle(generationBundleInput{
		httpAuth:      auth,
		registrations: regs,
	})
}

// TransferLedgerOwnershipForTest exposes package-private ledger transfer for
// external-package ownership tests. Production API stays unexported.
func TransferLedgerOwnershipForTest(c *CandidateRuntime) *ResourceLedger {
	return c.transferLedgerOwnership()
}

// NewCandidateRuntimeForTest builds a minimal candidate bound to ledger
// (tests). Test-only: the ledger is the sole generation-owned resource on
// CandidateRuntime (task 4.2); there is no legacy closer bag to seed.
func NewCandidateRuntimeForTest(ledger *ResourceLedger) *CandidateRuntime {
	return &CandidateRuntime{Ledger: ledger}
}
