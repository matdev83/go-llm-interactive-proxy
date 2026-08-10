package contract_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
)

func TestNormalizeMethodAndPath(t *testing.T) {
	t.Parallel()
	method, err := httpcontract.NormalizeMethod(" post ")
	if err != nil || method != http.MethodPost {
		t.Fatalf("method=%q err=%v", method, err)
	}
	path, err := httpcontract.NormalizePath("/openresponses/v1/responses/")
	if err != nil || path != "/openresponses/v1/responses" {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

func TestNormalizePathRejections(t *testing.T) {
	t.Parallel()
	invalidPaths := []string{
		"/openresponses/v1/responses?query=1",
		"/openresponses/v1/responses#fragment",
		"http://example.com/openresponses/v1/responses",
		"https://example.com/openresponses/v1/responses",
		"/openresponses/v1/../v1/responses",
		"/openresponses/v1/./responses",
		"/openresponses/v1\\responses",
		"/openresponses/v1/*/responses",
		"/openresponses//v1/responses",
	}
	for _, p := range invalidPaths {
		if norm, err := httpcontract.NormalizePath(p); err == nil {
			t.Errorf("NormalizePath(%q) should fail, got %q", p, norm)
		}
	}
}

func TestRouteRegistryDuplicateOwnerAllowed(t *testing.T) {
	t.Parallel()
	reg := httpcontract.NewRouteRegistry()
	claim := httpcontract.RouteClaim{
		OwnerID: "openresponses",
		Method:  http.MethodPost,
		Path:    "/openresponses/v1/responses",
		Kind:    "openresponses_create",
	}
	if err := reg.Register(claim); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(claim); err != nil {
		t.Fatalf("same owner re-register: %v", err)
	}
}

// TestRouteRegistry_RegisterAllIsAtomic proves a multi-claim registration that
// fails partway leaves the registry unchanged (atomic candidate failure): the
// conflict error names both owners and no earlier claim from the failed set is
// observable afterwards.
func TestRouteRegistry_RegisterAllIsAtomic(t *testing.T) {
	t.Parallel()
	reg := httpcontract.NewRouteRegistry()
	openai, err := httpcontract.OpenAIResponsesDefaultClaims("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(openai); err != nil {
		t.Fatal(err)
	}
	before := len(reg.Claims())

	conflicting := []httpcontract.RouteClaim{
		{OwnerID: "or-inst", Method: http.MethodPost, Path: "/v1/responses", Kind: "openresponses_create"},
		{OwnerID: "or-inst", Method: http.MethodPost, Path: "/v1/responses/compact", Kind: "openresponses_compact"},
	}
	err = reg.RegisterAll(conflicting)
	if err == nil {
		t.Fatal("expected conflict")
	}
	var detail httpcontract.RouteConflictDetail
	if !errors.As(err, &detail) {
		t.Fatalf("want RouteConflictDetail, got %T: %v", err, err)
	}
	if detail.ExistingOwner != "openai-responses" || detail.NewOwner != "or-inst" {
		t.Fatalf("owners=%q vs %q", detail.ExistingOwner, detail.NewOwner)
	}
	if len(reg.Claims()) != before {
		t.Fatalf("RegisterAll was not atomic: before=%d after=%d", before, len(reg.Claims()))
	}
	if _, ok := reg.OwnerOf(http.MethodPost, "/v1/responses/compact"); ok {
		t.Fatal("a later claim from the failed set was registered despite the conflict")
	}
}

func TestRouteRegistryDuplicateOwnersConflict(t *testing.T) {
	t.Parallel()
	reg := httpcontract.NewRouteRegistry()
	openai, err := httpcontract.OpenAIResponsesDefaultClaims("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(openai); err != nil {
		t.Fatal(err)
	}
	orClaims, err := httpcontract.OpenResponsesDefaultClaims("openresponses")
	if err != nil {
		t.Fatal(err)
	}
	remapped, err := httpcontract.RemapBasePath(orClaims, httpcontract.DefaultOpenResponsesBasePath, httpcontract.CanonicalLegacyBasePath)
	if err != nil {
		t.Fatal(err)
	}
	err = reg.Register(remapped[0])
	if err == nil {
		t.Fatal("expected conflict")
	}
	var detail httpcontract.RouteConflictDetail
	if !errors.As(err, &detail) {
		t.Fatalf("want RouteConflictDetail, got %T: %v", err, err)
	}
	if detail.ExistingOwner != "openai-responses" || detail.NewOwner != "openresponses" {
		t.Fatalf("owners=%q vs %q", detail.ExistingOwner, detail.NewOwner)
	}
	if !strings.Contains(err.Error(), "POST") || !strings.Contains(err.Error(), "/v1/responses") {
		t.Fatalf("conflict message=%q", err.Error())
	}
}

func TestOpenResponsesDefaultClaimsNonCollidingWithOpenAI(t *testing.T) {
	t.Parallel()
	reg := httpcontract.NewRouteRegistry()
	openai, err := httpcontract.OpenAIResponsesDefaultClaims("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	orClaims, err := httpcontract.OpenResponsesDefaultClaims("openresponses")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(openai); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(orClaims); err != nil {
		t.Fatalf("default paths should not collide: %v", err)
	}
}

func TestValidateCanonicalPathTakeover(t *testing.T) {
	t.Parallel()
	reg := httpcontract.NewRouteRegistry()
	openai, err := httpcontract.OpenAIResponsesDefaultClaims("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(openai); err != nil {
		t.Fatal(err)
	}
	orClaims, err := httpcontract.OpenResponsesDefaultClaims("openresponses")
	if err != nil {
		t.Fatal(err)
	}
	remapped, err := httpcontract.RemapBasePath(orClaims, httpcontract.DefaultOpenResponsesBasePath, httpcontract.CanonicalLegacyBasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateCanonicalPathTakeover(httpcontract.CanonicalLegacyBasePath, remapped); err == nil {
		t.Fatal("expected takeover conflict")
	}
}

func TestRouteDiagnosticsSanitized(t *testing.T) {
	t.Parallel()
	reg := httpcontract.NewRouteRegistry()
	claims, err := httpcontract.OpenResponsesDefaultClaims("openresponses\nsecret")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(claims); err != nil {
		t.Fatal(err)
	}
	diags := httpcontract.RouteDiagnosticsFromRegistry(reg)
	if len(diags) != 3 {
		t.Fatalf("diags=%d", len(diags))
	}
	for _, d := range diags {
		if strings.Contains(d.OwnerID, "\n") {
			t.Fatalf("unsanitized owner=%q", d.OwnerID)
		}
		if d.Transport == "" {
			t.Fatalf("missing transport for %+v", d)
		}
	}
}
