package openrouter

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

func resolveModel(cand routing.AttemptCandidate, call lipapi.Call) string {
	m := strings.TrimSpace(cand.Primary.Model)
	if m != "" {
		return m
	}
	for _, key := range []string{"openailegacy.model", "openairesponses.model"} {
		if call.Extensions != nil {
			raw, ok := call.Extensions[key]
			if ok && len(raw) > 0 {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					s = strings.TrimSpace(s)
					if s != "" {
						return s
					}
				}
			}
		}
	}
	return ""
}

func resolveFlavor(call lipapi.Call) string {
	if call.Extensions != nil {
		f := openrouterwire.GetString(call.Extensions, openrouterwire.ExtUpstreamFlavor)
		if f == openrouterwire.FlavorResponses {
			return openrouterwire.FlavorResponses
		}
		if _, ok := call.Extensions["openairesponses.model"]; ok {
			return openrouterwire.FlavorResponses
		}
		if _, ok := call.Extensions["openailegacy.model"]; ok {
			return openrouterwire.FlavorChat
		}
	}
	return openrouterwire.FlavorChat
}

func buildRequestOptions(call lipapi.Call, cfg Config) []option.RequestOption {
	ext := call.Extensions
	var opts []option.RequestOption

	referer := openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer)
	if referer == "" {
		referer = cfg.StaticReferer
	}
	if referer != "" {
		opts = append(opts, option.WithHeader("HTTP-Referer", referer))
	}

	title := openrouterwire.GetString(ext, openrouterwire.ExtTitle)
	if title == "" {
		title = cfg.StaticTitle
	}
	if title != "" {
		opts = append(opts, option.WithHeader("X-Title", title))
	}

	categories := openrouterwire.GetString(ext, openrouterwire.ExtCategories)
	if categories != "" {
		opts = append(opts, option.WithHeader("X-OpenRouter-Categories", categories))
	}

	metaHeader := openrouterwire.GetString(ext, openrouterwire.ExtMetadataHeader)
	if metaHeader != "" {
		opts = append(opts, option.WithHeader("X-OpenRouter-Metadata", metaHeader))
	}

	setIfPresent := func(jsonPath string, extKey string) {
		raw := openrouterwire.GetRaw(ext, extKey)
		if raw != nil {
			opts = append(opts, option.WithJSONSet(jsonPath, json.RawMessage(raw)))
		}
	}

	setIfPresent("provider", openrouterwire.ExtProvider)
	setIfPresent("models", openrouterwire.ExtModels)
	setIfPresent("route", openrouterwire.ExtRoute)
	setIfPresent("plugins", openrouterwire.ExtPlugins)
	setIfPresent("prediction", openrouterwire.ExtPrediction)
	setIfPresent("debug", openrouterwire.ExtDebug)
	setIfPresent("service_tier", openrouterwire.ExtServiceTier)
	setIfPresent("session_id", openrouterwire.ExtSessionID)
	setIfPresent("stop_server_tools_when", openrouterwire.ExtStopServerToolsWhen)
	setIfPresent("trace", openrouterwire.ExtTrace)
	setIfPresent("include", openrouterwire.ExtInclude)
	setIfPresent("user", openrouterwire.ExtUser)
	setIfPresent("response_format", openrouterwire.ExtResponseFormat)
	setIfPresent("reasoning", openrouterwire.ExtReasoning)

	return opts
}
