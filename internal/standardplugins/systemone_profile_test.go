package standardplugins_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/providerprofiles"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSystemOneProfiles_BuildDecisionBackend(t *testing.T) {
	t.Parallel()
	p, err := providerprofiles.EmbeddedProfile("typesafe")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := providerprofiles.CompileProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	be, err := standardplugins.BuildProviderProfileBackend(compiled, "decision", &http.Client{}, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, decision := be.Caps[lipapi.CapabilityDecisions]
	if !decision || len(be.Caps) != 1 || be.ModelInventory == nil {
		t.Fatalf("profile backend lacks decision capability or static inventory: %+v", be)
	}
}

func TestSystemOneProfiles_ForwardValidatedOperatorHeaders(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Provider-Label") != "operator-title" {
			t.Error("profile header missing")
		}
		_, _ = io.WriteString(w, `{"model":"jev-latest","answers":{"q":{"type":"noul","noul":0.7}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	p, err := providerprofiles.EmbeddedProfile("typesafe")
	if err != nil {
		t.Fatal(err)
	}
	p.Endpoint.BaseURL = srv.URL
	p.Headers = []providerprofiles.SafeHeader{{Name: "X-Provider-Label", Value: "operator-title"}}
	compiled, err := providerprofiles.CompileProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	be, err := standardplugins.BuildProviderProfileBackend(compiled, "decision", srv.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	r := lipapi.DecisionRequest{Evidence: []byte(`"x"`), Questions: []lipapi.DecisionQuestion{{ID: "q", Kind: lipapi.DecisionKindNoul}}}
	stream, err := be.Open(t.Context(), lipapi.Call{Decision: &r, Invocation: lipapi.Invocation{Operation: lipapi.OperationDecisionEvaluate}}, routing.AttemptCandidate{Primary: routing.Primary{Model: "jev-latest"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}
