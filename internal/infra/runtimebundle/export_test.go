package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

func NewGenerationBundleForTest(models *modelregistry.Runtime, catalog *modelcatalog.CatalogRuntime) *GenerationBundle {
	return &GenerationBundle{
		models: generationModelViews{
			models:  models,
			catalog: catalog,
		},
	}
}

func NewGenerationBundleWithLedgerForTest(ledger *ResourceLedger) *GenerationBundle {
	return &GenerationBundle{ledger: ledger}
}

func NewGenerationBundleWithPublicationForTest(auth []httpauth.Provider, regs []lipsdk.Registration) *GenerationBundle {
	return newGenerationBundle(generationBundleInput{
		httpAuth:      auth,
		registrations: regs,
	})
}

func TransferLedgerOwnershipForTest(c *CandidateRuntime) *ResourceLedger {
	return c.transferLedgerOwnership()
}

func NewCandidateRuntimeForTest(ledger *ResourceLedger) *CandidateRuntime {
	return &CandidateRuntime{Ledger: ledger}
}
