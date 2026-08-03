package contract

import (
	"fmt"
	"net/http"
	"strings"
)

// RouteKind identifies the protocol surface owning a method/path pair.
type RouteKind string

const (
	RouteKindOpenResponsesCreate    RouteKind = "openresponses_create"
	RouteKindOpenResponsesCompact   RouteKind = "openresponses_compact"
	RouteKindOpenResponsesWebSocket RouteKind = "openresponses_websocket"
	RouteKindOpenAIResponsesCreate  RouteKind = "openai_responses_create"
	RouteKindOpenAIResponsesCancel  RouteKind = "openai_responses_cancel"
	RouteKindOpenAIChatCompletions  RouteKind = "openai_chat_completions"
	RouteKindAnthropicMessages      RouteKind = "anthropic_messages"
	RouteKindGeminiGenerate         RouteKind = "gemini_generate"
)

// DefaultOpenResponsesBasePath is the non-colliding default mount prefix.
const DefaultOpenResponsesBasePath = "/openresponses/v1"

// CanonicalLegacyBasePath is the shared /v1 prefix used by existing frontends.
const CanonicalLegacyBasePath = "/v1"

// RouteClaim registers one normalized HTTP route owner before serving.
type RouteClaim struct {
	OwnerID string
	Method  string
	Path    string
	Kind    RouteKind
}

// NormalizeMethod uppercases and trims an HTTP method.
func NormalizeMethod(method string) (string, error) {
	m := strings.TrimSpace(strings.ToUpper(method))
	if m == "" {
		return "", fmt.Errorf("route claim: empty method")
	}
	if strings.ContainsAny(m, "\r\n\t") {
		return "", fmt.Errorf("route claim: method contains control characters")
	}
	if m != http.MethodGet && m != http.MethodPost && m != http.MethodPut &&
		m != http.MethodPatch && m != http.MethodDelete && m != http.MethodOptions {
		return "", fmt.Errorf("route claim: unsupported method %q", method)
	}
	return m, nil
}

// NormalizePath canonicalizes a mount path for deterministic ownership checks.
func NormalizePath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", fmt.Errorf("route claim: empty path")
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("route claim: path must be absolute")
	}
	if strings.ContainsAny(p, "?#*\\") {
		return "", fmt.Errorf("route claim: path contains invalid characters (query/fragment/wildcard/backslash)")
	}
	if strings.Contains(p, "//") {
		return "", fmt.Errorf("route claim: path contains double slash")
	}
	segments := strings.Split(p, "/")
	for _, seg := range segments {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("route claim: path contains traversal segment %q", seg)
		}
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		p = "/"
	}
	return p, nil
}

// NormalizedClaim returns a copy with normalized method and path.
func (c RouteClaim) NormalizedClaim() (RouteClaim, error) {
	method, err := NormalizeMethod(c.Method)
	if err != nil {
		return RouteClaim{}, err
	}
	path, err := NormalizePath(c.Path)
	if err != nil {
		return RouteClaim{}, err
	}
	if strings.TrimSpace(c.OwnerID) == "" {
		return RouteClaim{}, fmt.Errorf("route claim: empty owner id")
	}
	if c.Kind == "" {
		return RouteClaim{}, fmt.Errorf("route claim: empty kind")
	}
	out := c
	out.Method = method
	out.Path = path
	out.OwnerID = strings.TrimSpace(c.OwnerID)
	return out, nil
}

// OpenResponsesDefaultClaims returns the default non-colliding OpenResponses routes.
func OpenResponsesDefaultClaims(ownerID string) ([]RouteClaim, error) {
	base, err := NormalizePath(DefaultOpenResponsesBasePath)
	if err != nil {
		return nil, err
	}
	create, err := RouteClaim{OwnerID: ownerID, Method: http.MethodPost, Path: base + "/responses", Kind: RouteKindOpenResponsesCreate}.NormalizedClaim()
	if err != nil {
		return nil, err
	}
	compact, err := RouteClaim{OwnerID: ownerID, Method: http.MethodPost, Path: base + "/responses/compact", Kind: RouteKindOpenResponsesCompact}.NormalizedClaim()
	if err != nil {
		return nil, err
	}
	ws, err := RouteClaim{OwnerID: ownerID, Method: http.MethodGet, Path: base + "/responses", Kind: RouteKindOpenResponsesWebSocket}.NormalizedClaim()
	if err != nil {
		return nil, err
	}
	return []RouteClaim{create, compact, ws}, nil
}

// OpenAIResponsesDefaultClaims returns the existing OpenAI Responses frontend routes at /v1.
func OpenAIResponsesDefaultClaims(ownerID string) ([]RouteClaim, error) {
	base, err := NormalizePath(CanonicalLegacyBasePath)
	if err != nil {
		return nil, err
	}
	create, err := RouteClaim{OwnerID: ownerID, Method: http.MethodPost, Path: base + "/responses", Kind: RouteKindOpenAIResponsesCreate}.NormalizedClaim()
	if err != nil {
		return nil, err
	}
	cancel, err := RouteClaim{OwnerID: ownerID, Method: http.MethodPost, Path: base + "/responses/{id}/cancel", Kind: RouteKindOpenAIResponsesCancel}.NormalizedClaim()
	if err != nil {
		return nil, err
	}
	return []RouteClaim{create, cancel}, nil
}
