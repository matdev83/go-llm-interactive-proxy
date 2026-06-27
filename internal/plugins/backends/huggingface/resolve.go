package huggingface

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaifamily"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// resolveModelWithProvider resolves the model via the configured openaifamily
// policy, then appends the route selector `?provider=<slug>` query param as a
// Hugging Face router provider suffix (`model:<slug>`) when applicable.
func resolveModelWithProvider(policy openaifamily.ModelResolutionPolicy, cand routing.AttemptCandidate, call lipapi.Call) string {
	model := openaifamily.ResolveModel(policy, ID, cand, call)
	return applyProviderSuffix(model, cand)
}

// applyProviderSuffix appends `:slug` to model when the candidate carries a
// non-empty `provider` query param and the model's last segment does not
// already carry a `:` suffix. Hugging Face selects inference providers via the
// model string suffix; no `provider` JSON field is sent.
func applyProviderSuffix(model string, cand routing.AttemptCandidate) string {
	if model == "" {
		return model
	}
	slug := cand.Primary.TrimmedParam("provider")
	if slug == "" {
		return model
	}
	last := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		last = model[i+1:]
	}
	if strings.Contains(last, ":") {
		return model
	}
	return model + ":" + slug
}
