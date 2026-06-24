package openaifamily

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type ModelResolutionPolicy int

const (
	ModelResolutionDirect ModelResolutionPolicy = iota
	ModelResolutionStripBackendPrefix
)

func ResolveModel(policy ModelResolutionPolicy, prefix string, cand routing.AttemptCandidate, call lipapi.Call) string {
	if policy == ModelResolutionStripBackendPrefix {
		return ResolveModelForPrefix(prefix, cand, call)
	}
	return ModelFromCall(cand, call)
}

func ResolveModelForPrefix(prefix string, cand routing.AttemptCandidate, call lipapi.Call) string {
	model := ModelFromCall(cand, call)
	if native, ok := strings.CutPrefix(model, prefix+"/"); ok {
		model = native
	} else if native, ok := strings.CutPrefix(model, prefix+":"); ok {
		model = native
	}
	return model
}

func ModelFromCall(cand routing.AttemptCandidate, call lipapi.Call) string {
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
