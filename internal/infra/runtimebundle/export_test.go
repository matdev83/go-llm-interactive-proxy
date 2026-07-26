package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
)

// NewGenerationBundleForTest builds a minimal GenerationBundle for binder tests.
func NewGenerationBundleForTest(models *modelregistry.Runtime, catalog *modelcatalog.CatalogRuntime) *GenerationBundle {
	return &GenerationBundle{
		models:  models,
		catalog: catalog,
	}
}
