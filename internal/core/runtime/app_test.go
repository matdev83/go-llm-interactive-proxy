package runtime_test

import (
	"errors"
	"testing"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestNewRequiresConfig(t *testing.T) {
	t.Parallel()

	_, err := runtime.New(runtime.Options{})
	if err == nil {
		t.Fatal("expected error for missing config")
	}
	if !errors.Is(err, runtime.ErrNilConfig) {
		t.Fatalf("expected errors.Is(err, ErrNilConfig), got %v", err)
	}
}

func TestNewAcceptsNilLogger(t *testing.T) {
	t.Parallel()

	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Logger: nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app == nil {
		t.Fatal("expected app instance")
	}
}

func TestNewAcceptsMinimalConfig(t *testing.T) {
	t.Parallel()

	app, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if app == nil {
		t.Fatal("expected app instance")
	}
}

func TestNewRejectsDuplicatePluginRegistrations(t *testing.T) {
	t.Parallel()

	_, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Registrations: []lipsdk.Registration{
			{ID: "dup", Kind: lipsdk.PluginKindFrontend},
			{ID: "dup", Kind: lipsdk.PluginKindFrontend},
		},
		Mandatory: nil,
	})
	if err == nil {
		t.Fatal("expected duplicate registration error")
	}
	if !errors.Is(err, lipsdk.ErrDuplicateRegistration) {
		t.Fatalf("expected ErrDuplicateRegistration, got %v", err)
	}
}

func TestNewRejectsMissingMandatoryPlugin(t *testing.T) {
	t.Parallel()

	_, err := runtime.New(runtime.Options{
		Config: &coreconfig.Config{
			Server: coreconfig.ServerConfig{Address: ":8080"},
		},
		Registrations: []lipsdk.Registration{
			{ID: "only-one", Kind: lipsdk.PluginKindFrontend},
		},
		Mandatory: []lipsdk.Requirement{
			{Kind: lipsdk.PluginKindBackend, ID: "missing-backend"},
		},
	})
	if err == nil {
		t.Fatal("expected missing mandatory plugin error")
	}

	var missing *lipsdk.MissingRequirementError
	if !errors.As(err, &missing) {
		t.Fatalf("expected MissingRequirementError, got %v", err)
	}
}
