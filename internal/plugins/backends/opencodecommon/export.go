package opencodecommon

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/opencodecommon/catalog"
)

type (
	BackendKind    = catalog.BackendKind
	ModelEntry     = catalog.ModelEntry
	VendorResolver = modelcatalog.VendorResolver
	Flavor         = catalog.Flavor
)

const (
	BackendGo  = catalog.BackendGo
	BackendZen = catalog.BackendZen
)

const (
	FlavorOpenAIChat        = catalog.FlavorOpenAIChat
	FlavorOpenAIResponses   = catalog.FlavorOpenAIResponses
	FlavorAnthropicMessages = catalog.FlavorAnthropicMessages
	FlavorGoogleGemini      = catalog.FlavorGoogleGemini
)

var (
	ErrUnknownModel           = catalog.ErrUnknownModel
	NewOpenCodeVendorResolver = catalog.NewOpenCodeVendorResolver
	WirePrefix                = catalog.WirePrefix
)
