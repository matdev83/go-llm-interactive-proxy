package nvidia

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

// requestOptions returns SDK request options that apply NVIDIA-specific
// payload mutations:
//   - strip stream_options (hosted NIM rejects include_usage as extra_forbidden)
//   - remap max_completion_tokens to max_tokens (hosted NIM strict schema)
//   - inject extra_body extension fields from Call.Extensions
func requestOptions(call lipapi.Call) []option.RequestOption {
	capEstimate := 1
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		capEstimate += 2
	}
	if call.Extensions != nil {
		capEstimate += len(call.Extensions)
	}

	opts := make([]option.RequestOption, 0, capEstimate)

	opts = append(opts, option.WithJSONDel("stream_options"))

	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		opts = append(opts, option.WithJSONSet("max_tokens", *call.Options.MaxOutputTokens))
		opts = append(opts, option.WithJSONDel("max_completion_tokens"))
	}

	if call.Extensions != nil {
		for key, raw := range call.Extensions {
			if strings.HasPrefix(key, openrouterwire.ExtraBodyExtPrefix) {
				fieldName := key[len(openrouterwire.ExtraBodyExtPrefix):]
				if openrouterwire.ValidExtraBodyFieldName(fieldName) && openrouterwire.ExtraBodyValueWithinBounds(raw) {
					opts = append(opts, option.WithJSONSet(fieldName, json.RawMessage(raw)))
				}
			}
		}
	}

	return opts
}
