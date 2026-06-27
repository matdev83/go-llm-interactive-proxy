package openrouter

import (
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

func buildRequestOptions(call lipapi.Call, cand routing.AttemptCandidate, cfg Config) []option.RequestOption {
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

	if raw := openrouterwire.GetRaw(ext, openrouterwire.ExtProvider); raw != nil {
		opts = append(opts, option.WithJSONSet("provider", json.RawMessage(raw)))
	} else if raw := providerRawFromCandidate(cand); raw != nil {
		opts = append(opts, option.WithJSONSet("provider", raw))
	}
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

// providerRawFromCandidate builds the OpenRouter provider body field from a route
// selector query param `?provider=<slug>`. Returns nil when no provider param is set.
// The resulting shape is {"order":["<slug>"],"allow_fallbacks":false}.
func providerRawFromCandidate(cand routing.AttemptCandidate) json.RawMessage {
	slug := cand.Primary.TrimmedParam("provider")
	if slug == "" {
		return nil
	}
	body := struct {
		Order          []string `json:"order"`
		AllowFallbacks bool     `json:"allow_fallbacks"`
	}{
		Order:          []string{slug},
		AllowFallbacks: false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	return raw
}
