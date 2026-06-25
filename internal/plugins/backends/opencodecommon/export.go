package opencodecommon

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon/catalog"
)

type BackendKind = catalog.BackendKind

const (
	BackendGo  = catalog.BackendGo
	BackendZen = catalog.BackendZen
)

type ModelEntry = catalog.ModelEntry
type VendorResolver = catalog.VendorResolver
type ModelCatalogVendorResolver = catalog.ModelCatalogVendorResolver
type Flavor = catalog.Flavor

const (
	FlavorOpenAIChat        = catalog.FlavorOpenAIChat
	FlavorOpenAIResponses   = catalog.FlavorOpenAIResponses
	FlavorAnthropicMessages = catalog.FlavorAnthropicMessages
	FlavorGoogleGemini      = catalog.FlavorGoogleGemini
)

var (
	ErrUnknownModel               = catalog.ErrUnknownModel
	NewModelCatalogVendorResolver = catalog.NewModelCatalogVendorResolver
	NewOpenCodeVendorResolver     = catalog.NewOpenCodeVendorResolver
	WirePrefix                    = catalog.WirePrefix
)
