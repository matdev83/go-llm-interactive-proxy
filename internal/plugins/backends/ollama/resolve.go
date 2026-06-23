package ollama

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func resolveModelForMode(mode backendMode) func(cand routing.AttemptCandidate, call lipapi.Call) string {
	return func(cand routing.AttemptCandidate, call lipapi.Call) string {
		return upstreamModelID(mode, modelFromCall(cand, call))
	}
}

func resolveModel(cand routing.AttemptCandidate, call lipapi.Call) string {
	return upstreamModelID(backendModeLocal, modelFromCall(cand, call))
}

func modelFromCall(cand routing.AttemptCandidate, call lipapi.Call) string {
	m := strings.TrimSpace(cand.Primary.Model)
	if m == "" {
		for _, key := range []string{"openailegacy.model", "openairesponses.model"} {
			if call.Extensions != nil {
				raw, ok := call.Extensions[key]
				if ok && len(raw) > 0 {
					var s string
					if json.Unmarshal(raw, &s) == nil {
						s = strings.TrimSpace(s)
						if s != "" {
							m = s
							break
						}
					}
				}
			}
		}
	}
	return m
}

func upstreamModelID(mode backendMode, model string) string {
	model = strings.TrimSpace(model)
	if mode == backendModeCloud {
		if native, ok := strings.CutPrefix(model, "ollama-cloud/"); ok {
			model = native
		} else if native, ok := strings.CutPrefix(model, "ollama-cloud:"); ok {
			model = native
		}
		if _, after, ok := strings.Cut(model, "/"); ok {
			model = after
		}
		return ensureCloudSuffix(model)
	}
	if native, ok := strings.CutPrefix(model, "ollama/"); ok {
		model = native
	}
	if native, ok := strings.CutPrefix(model, "ollama:"); ok {
		model = native
	}
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	return model
}

func ensureCloudSuffix(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return model
	}
	if strings.HasSuffix(model, "-cloud") {
		return model
	}
	return model + "-cloud"
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

func nativeModelID(mode backendMode, model string) string {
	return upstreamModelID(mode, model)
}
