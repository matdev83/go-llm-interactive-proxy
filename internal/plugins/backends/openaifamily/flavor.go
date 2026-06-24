package openaifamily

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func ResolveFlavor(call lipapi.Call) openaicompat.Flavor {
	if call.Extensions != nil {
		f := openrouterwire.GetString(call.Extensions, openrouterwire.ExtUpstreamFlavor)
		if f == openrouterwire.FlavorResponses {
			return openaicompat.FlavorResponses
		}
		if _, ok := call.Extensions["openairesponses.model"]; ok {
			return openaicompat.FlavorResponses
		}
		if _, ok := call.Extensions["openailegacy.model"]; ok {
			return openaicompat.FlavorChat
		}
	}
	return openaicompat.FlavorChat
}

func IsResponsesFlavor(call lipapi.Call) bool {
	return ResolveFlavor(call) == openaicompat.FlavorResponses
}
