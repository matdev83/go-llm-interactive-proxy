package stdhttp

import (
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
)

const openAIModelsPath = "/v1/models"

// ModelRegistryHandler serves OpenAI-compatible GET /v1/models from the live
// model-registry Runtime. Nil/unavailable runtime yields an empty list with 200.
type ModelRegistryHandler struct {
	rt *modelregistry.Runtime
}

var _ http.Handler = (*ModelRegistryHandler)(nil)

// NewModelRegistryHandler returns a concrete handler for GET /v1/models.
func NewModelRegistryHandler(rt *modelregistry.Runtime) *ModelRegistryHandler {
	return &ModelRegistryHandler{rt: rt}
}

func (h *ModelRegistryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, generation, ok := ([]byte)(nil), "", false
	if h != nil && h.rt != nil {
		body, generation, ok = h.rt.ModelsJSON()
	}
	if !ok || body == nil {
		body = []byte(`{"object":"list","data":[]}`)
		generation = ""
	}
	etag := quoteETag(generation)
	if etag != "" {
		w.Header().Set("ETag", etag)
		if match := strings.TrimSpace(r.Header.Get("If-None-Match")); match != "" && etagMatches(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func quoteETag(generation string) string {
	generation = strings.TrimSpace(generation)
	if generation == "" {
		return ""
	}
	return `"` + generation + `"`
}

func etagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "*" {
		return true
	}
	for part := range strings.SplitSeq(ifNoneMatch, ",") {
		part = strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(part, "W/"); ok {
			part = strings.TrimSpace(after)
		}
		if part == etag {
			return true
		}
	}
	return false
}
