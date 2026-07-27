package upstream

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type Router struct {
	kind    catalog.BackendKind
	baseURL string
	apiKey  string
	hc      *http.Client
	mu      sync.Mutex
	openAI  map[string]*openaicompat.Client
}

func NewRouter(kind catalog.BackendKind, baseURL, apiKey string, hc *http.Client) *Router {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Router{
		kind:    kind,
		baseURL: baseURL,
		apiKey:  apiKey,
		hc:      hc,
		openAI:  make(map[string]*openaicompat.Client),
	}
}

func (r *Router) Open(ctx context.Context, call lipapi.Call, resolved catalog.ResolvedModel) (lipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("opencode: %w", lipapi.ErrNilContext)
	}
	baseURL := catalog.EndpointBaseURL(resolved.Entry, r.baseURL, resolved.Flavor)
	switch resolved.Flavor {
	case catalog.FlavorAnthropicMessages:
		return openAnthropic(ctx, r.hc, baseURL, r.apiKey, call, resolved.WireModel)
	case catalog.FlavorGoogleGemini:
		return openGemini(ctx, r.hc, baseURL, r.apiKey, call, resolved.WireModel)
	case catalog.FlavorOpenAIResponses:
		return r.openAIClient(baseURL).Open(ctx, call, resolved.WireModel, openaicompat.FlavorResponses)
	default:
		return r.openAIClient(baseURL).Open(ctx, call, resolved.WireModel, openaicompat.FlavorChat)
	}
}

func (r *Router) openAIClient(baseURL string) *openaicompat.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cl, ok := r.openAI[baseURL]; ok {
		return cl
	}
	cl := &openaicompat.Client{
		BaseURL:    baseURL,
		APIKey:     r.apiKey,
		HTTPClient: r.hc,
		Transport:  openaicompat.TransportChatAndResponses,
	}
	r.openAI[baseURL] = cl
	return cl
}
