package standardplugins

import (
	"errors"
	"net/http"
	"testing"

	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"gopkg.in/yaml.v3"
)

func routeClaimsNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestStandardFrontendRouteClaims_openResponsesOwnerAware proves the standard
// claims seam returns owner-aware OpenResponses claims derived from config
// (base_path and WebSocket toggles) for one instance.
func TestStandardFrontendRouteClaims_openResponsesOwnerAware(t *testing.T) {
	t.Parallel()
	providers := StandardFrontendRouteClaims()
	provider, ok := providers["openresponses"]
	if !ok {
		t.Fatal("openresponses frontend must declare a claims provider")
	}
	claims, err := provider("or-fe", routeClaimsNode(t, `base_path: /openresponses/v1
websocket:
  enabled: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 3 {
		t.Fatalf("claims=%d, want 3 (create/compact/ws)", len(claims))
	}
	for _, c := range claims {
		if c.OwnerID != "or-fe" {
			t.Fatalf("claim owner=%q, want or-fe", c.OwnerID)
		}
		if c.Method != http.MethodPost && c.Method != http.MethodGet {
			t.Fatalf("claim method=%q", c.Method)
		}
	}
}

// TestStandardFrontendRouteClaims_canonicalTakeoverRejected proves the
// openai-responses default /v1 claims plus an OpenResponses instance configured
// at base_path=/v1 conflict with both owners named and an unchanged registry
// (atomic candidate failure at the provider-seam level).
func TestStandardFrontendRouteClaims_canonicalTakeoverRejected(t *testing.T) {
	t.Parallel()
	providers := StandardFrontendRouteClaims()
	openAIProvider, ok := providers["openai-responses"]
	if !ok {
		t.Fatal("openai-responses frontend must declare a claims provider")
	}
	openAI, err := openAIProvider("openai-responses", routeClaimsNode(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	reg := httpcontract.NewRouteRegistry()
	if err := reg.RegisterAll(openAI); err != nil {
		t.Fatal(err)
	}
	before := len(reg.Claims())

	orProvider, ok := providers["openresponses"]
	if !ok {
		t.Fatal("openresponses frontend must declare a claims provider")
	}
	orClaims, err := orProvider("or-inst", routeClaimsNode(t, `base_path: /v1
websocket:
  enabled: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateCanonicalPathTakeover(httpcontract.CanonicalLegacyBasePath, orClaims); err == nil {
		t.Fatal("expected canonical takeover conflict")
	}
	err = reg.RegisterAll(orClaims)
	if err == nil {
		t.Fatal("expected canonical takeover conflict")
	}
	var detail httpcontract.RouteConflictDetail
	if !errors.As(err, &detail) {
		t.Fatalf("want RouteConflictDetail, got %T: %v", err, err)
	}
	if detail.ExistingOwner != "openai-responses" || detail.NewOwner != "or-inst" {
		t.Fatalf("owners=%q vs %q", detail.ExistingOwner, detail.NewOwner)
	}
	if len(reg.Claims()) != before {
		t.Fatalf("failed registration mutated the registry: before=%d after=%d", before, len(reg.Claims()))
	}
}

// TestStandardFrontendRouteClaims_disabledWebSocketOmitsWSC claim keeps the
// seam consistent with diagnostics: disabling WebSocket drops the WS claim.
func TestStandardFrontendRouteClaims_disabledWebSocketOmitsWS(t *testing.T) {
	t.Parallel()
	providers := StandardFrontendRouteClaims()
	provider, ok := providers["openresponses"]
	if !ok {
		t.Fatal("openresponses frontend must declare a claims provider")
	}
	claims, err := provider("or-fe", routeClaimsNode(t, `base_path: /openresponses/v1
websocket:
  enabled: false
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 {
		t.Fatalf("claims=%d, want 2 without websocket", len(claims))
	}
	for _, c := range claims {
		if c.Kind == httpcontract.RouteKindOpenResponsesWebSocket {
			t.Fatalf("ws claim must be omitted when disabled: %+v", c)
		}
	}
}
