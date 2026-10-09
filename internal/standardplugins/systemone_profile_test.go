package standardplugins_test

import (
	"net/http"
	"testing"

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
