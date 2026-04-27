package pluginreg

import (
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"gopkg.in/yaml.v3"
)

func TestBackendSecurityProfile_roundTrip(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := reg.RegisterBackendWithProfile("oauth", func(yaml.Node, *http.Client) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}, BackendSecurityProfile{CredentialMode: CredentialOAuthUser}); err != nil {
		t.Fatal(err)
	}
	profile, ok := reg.BackendSecurityProfile("oauth")
	if !ok {
		t.Fatal("expected profile")
	}
	if profile.CredentialMode != CredentialOAuthUser {
		t.Fatalf("credential mode: got %q", profile.CredentialMode)
	}
}

func TestRegisterBackend_defaultsUnknownCredentialMode(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := reg.RegisterBackend("legacy", func(yaml.Node, *http.Client) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	profile, ok := reg.BackendSecurityProfile("legacy")
	if !ok {
		t.Fatal("expected profile")
	}
	if profile.CredentialMode != CredentialUnknown {
		t.Fatalf("credential mode: got %q", profile.CredentialMode)
	}
}

func TestBackendSecurityProfile_unregisteredFactoryIsNotFound(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	_, ok := reg.BackendSecurityProfile("factory-never-registered")
	if ok {
		t.Fatal("expected no profile for factory id that was never registered")
	}
}
