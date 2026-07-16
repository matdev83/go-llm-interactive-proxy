package openrouter

import (
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

func buildRequestOptions(call lipapi.Call, cand routing.AttemptCandidate, cfg Config) []option.RequestOption {
	ext := call.Extensions
	var opts []option.RequestOption

	referer := resolveAppURL(cfg, openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer))
	if referer != "" {
		opts = append(opts, option.WithHeader("HTTP-Referer", referer))
	}

	title := resolveAppTitle(cfg, openrouterwire.GetString(ext, openrouterwire.ExtTitle))
	if title != "" {
		opts = append(opts, option.WithHeader("X-OpenRouter-Title", title))
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

// resolveAppURL applies effective AppURL policy, or legacy client-first+static
// when LegacyAppURL is set. Empty Mode means ModeProxy.
func resolveAppURL(cfg Config, captured string) string {
	captured = acceptCapturedAppURL(captured)
	if cfg.LegacyAppURL {
		if captured != "" {
			return captured
		}
		return cfg.StaticReferer
	}
	mode := cfg.AppURL.Mode
	if mode == "" {
		mode = identity.ModeProxy
	}
	switch mode {
	case identity.ModeProxy:
		return identity.DefaultProjectURL
	case identity.ModePassthrough:
		return captured
	case identity.ModeCustom:
		return cfg.AppURL.Value
	case identity.ModeDrop:
		return ""
	default:
		return ""
	}
}

// resolveAppTitle applies effective AppTitle policy, or legacy client-first+static
// when LegacyAppTitle is set. Empty Mode means ModeProxy. Emits modern
// X-OpenRouter-Title values only.
func resolveAppTitle(cfg Config, captured string) string {
	captured = acceptCapturedAppTitle(captured)
	if cfg.LegacyAppTitle {
		if captured != "" {
			return captured
		}
		return cfg.StaticTitle
	}
	mode := cfg.AppTitle.Mode
	if mode == "" {
		mode = identity.ModeProxy
	}
	switch mode {
	case identity.ModeProxy:
		return identity.DefaultProductName
	case identity.ModePassthrough:
		return captured
	case identity.ModeCustom:
		return cfg.AppTitle.Value
	case identity.ModeDrop:
		return ""
	default:
		return ""
	}
}

func acceptCapturedAppURL(raw string) string {
	if v, ok := identity.AcceptClientAppURL(raw); ok {
		return v
	}
	return ""
}

func acceptCapturedAppTitle(raw string) string {
	if v, ok := identity.AcceptClientAppTitle(raw); ok {
		return v
	}
	return ""
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
