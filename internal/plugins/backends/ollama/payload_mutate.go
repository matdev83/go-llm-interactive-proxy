package ollama

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

func requestOptions(call lipapi.Call) []option.RequestOption {
	var opts []option.RequestOption

	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		opts = append(opts, option.WithJSONSet("max_tokens", *call.Options.MaxOutputTokens))
		opts = append(opts, option.WithJSONDel("max_completion_tokens"))
	}

	return opts
}
