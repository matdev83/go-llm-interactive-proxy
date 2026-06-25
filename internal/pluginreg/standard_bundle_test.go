package pluginreg

import (
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestInstallBundleOnCustomBundleDoesNotTouchOtherRegistries(t *testing.T) {
	t.Parallel()

	custom := Bundle{Backends: []BackendRegistration{{
		ID: "custom-backend",
		Factory: func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
			return execbackend.Backend{Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming)}, nil
		},
		Profile: BackendSecurityProfile{CredentialMode: CredentialWorkload},
	}}}

	withCustom := NewRegistry()
	if err := InstallBundleOn(withCustom, custom); err != nil {
		t.Fatal(err)
	}
	if _, err := withCustom.BuildBackend("custom-backend", yaml.Node{}, nil, BackendFactoryDeps{}); err != nil {
		t.Fatalf("custom bundle backend missing: %v", err)
	}

	empty := NewRegistry()
	if _, err := empty.BuildBackend("custom-backend", yaml.Node{}, nil, BackendFactoryDeps{}); err == nil {
		t.Fatal("custom bundle leaked into another registry")
	}
}

func TestStandardBundleIsValueOriented(t *testing.T) {
	t.Parallel()

	a := StandardBundle()
	b := StandardBundle()
	if len(a.Frontends) == 0 || len(a.Features) == 0 {
		t.Fatal("standard bundle must expose frontend and feature registrations")
	}
	a.Frontends[0].ID = "mutated"
	a.Features[0].ID = "mutated"
	if b.Frontends[0].ID == "mutated" || b.Features[0].ID == "mutated" {
		t.Fatal("standard bundle returned shared mutable slices")
	}
}

func TestStandardBackendBundleIsValueOriented(t *testing.T) {
	t.Parallel()

	a := StandardBackendBundle(UpstreamAPIKeys{})
	b := StandardBackendBundle(UpstreamAPIKeys{})
	if len(a.Backends) == 0 {
		t.Fatal("standard backend bundle must expose backend registrations")
	}
	a.Backends[0].ID = "mutated"
	if b.Backends[0].ID == "mutated" {
		t.Fatal("standard backend bundle returned shared mutable slices")
	}
}

func TestInstallBundleOnNilRegistry(t *testing.T) {
	t.Parallel()
	if err := InstallBundleOn(nil, Bundle{}); err == nil {
		t.Fatal("expected nil registry error")
	}
}
