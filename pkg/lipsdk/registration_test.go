package lipsdk_test

import (
	"errors"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestValidateRegistrationsRejectsDuplicatePluginIDs(t *testing.T) {
	t.Parallel()

	err := lipsdk.ValidateRegistrations([]lipsdk.Registration{
		{ID: "openai-responses", Kind: lipsdk.PluginKindFrontend},
		{ID: "openai-responses", Kind: lipsdk.PluginKindFrontend},
	}, nil)

	if err == nil {
		t.Fatal("expected duplicate registration error")
	}

	var duplicateErr *lipsdk.DuplicateRegistrationError
	if !errors.As(err, &duplicateErr) {
		t.Fatalf("expected DuplicateRegistrationError, got %T", err)
	}
	if !errors.Is(err, lipsdk.ErrDuplicateRegistration) {
		t.Fatal("expected error to unwrap to ErrDuplicateRegistration")
	}
}

func TestValidateRegistrationsRejectsMissingMandatoryPlugin(t *testing.T) {
	t.Parallel()

	err := lipsdk.ValidateRegistrations([]lipsdk.Registration{
		{ID: "openai-responses", Kind: lipsdk.PluginKindFrontend},
	}, []lipsdk.Requirement{{Kind: lipsdk.PluginKindBackend, ID: "anthropic"}})

	if err == nil {
		t.Fatal("expected missing requirement error")
	}

	var missingErr *lipsdk.MissingRequirementError
	if !errors.As(err, &missingErr) {
		t.Fatalf("expected MissingRequirementError, got %T", err)
	}
}

func TestValidateRegistrationsAcceptsOpaqueConfigPayloads(t *testing.T) {
	t.Parallel()

	node := yaml.Node{Kind: yaml.MappingNode}
	err := lipsdk.ValidateRegistrations([]lipsdk.Registration{
		{
			ID:   "submit-noop",
			Kind: lipsdk.PluginKindFeature,
			Config: lipsdk.ConfigPayload{
				Node: node,
			},
		},
	}, []lipsdk.Requirement{{Kind: lipsdk.PluginKindFeature, ID: "submit-noop"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
