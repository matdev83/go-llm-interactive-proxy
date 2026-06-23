package lmstudio

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func resolveModel(cand routing.AttemptCandidate, call lipapi.Call) string {
	model := modelFromCall(cand, call)
	if native, ok := strings.CutPrefix(model, "lmstudio/"); ok {
		model = native
	} else if native, ok := strings.CutPrefix(model, "lmstudio:"); ok {
		model = native
	}
	return model
}

func modelFromCall(cand routing.AttemptCandidate, call lipapi.Call) string {
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
