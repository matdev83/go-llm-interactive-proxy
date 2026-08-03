package conformance

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

// TestBaselineAdapterMatrixLock locks the authoritative FE×BE conformance matrix.
// Code truth: 5 bundled frontends × 9 backend compatibility identities = 45 Cartesian
// cells (spec Requirement 13.5 / design.md "Authoritative Lists"). OpenRouter and NVIDIA
// are authoritative compatibility identities driven through the actual connector
// executables via the backendplugin host adapter and remain optional connectors (never
// essential backend kinds).
func TestBaselineAdapterMatrixLock(t *testing.T) {
	t.Parallel()

	fe := BundledFrontendIDs()
	be := BundledBackendIDs()
	cells := AllCells()

	expectedFE := []string{"openai-responses", "openai-legacy", "anthropic", "gemini", "openresponses"}
	expectedBE := []string{"openai-responses", "openai-legacy", "anthropic", "gemini", "bedrock", "acp", "openresponses", "openrouter", "nvidia"}

	if len(fe) != 5 {
		t.Fatalf("frontend count drift: want 5, got %d (%v)", len(fe), fe)
	}
	if !slices.Equal(fe, expectedFE) {
		t.Fatalf("frontend list drift: want %v, got %v", expectedFE, fe)
	}

	if len(be) != 9 {
		t.Fatalf("backend count drift: want 9, got %d (%v)", len(be), be)
	}
	if !slices.Equal(be, expectedBE) {
		t.Fatalf("backend list drift: want %v, got %v", expectedBE, be)
	}

	wantCells := 5 * 9
	if len(cells) != wantCells {
		t.Fatalf("matrix cells count drift: want %d, got %d", wantCells, len(cells))
	}

	// ACP exclusions stay locked (tools rejected before network; multimodal is
	// viable via ACP resource prompt block projection); openrouter/nvidia use the
	// connector-host driver and reject tools/multimodal before network (the
	// connectors advertise streaming-only capabilities).
	for _, c := range cells {
		switch c.Backend {
		case "acp":
			if c.Meta.ToolsViable {
				t.Fatalf("ACP cell %s x %s must have ToolsViable=false", c.Frontend, c.Backend)
			}
			if !c.Meta.MultimodalViable {
				t.Fatalf("ACP cell %s x %s must have MultimodalViable=true (resource prompt block projection)", c.Frontend, c.Backend)
			}
			if c.Meta.SubsetJustification == "" {
				t.Fatalf("ACP cell %s x %s must have non-empty SubsetJustification", c.Frontend, c.Backend)
			}
			if c.Driver != DriverBase {
				t.Fatalf("ACP cell %s x %s must use the base driver, got %q", c.Frontend, c.Backend, c.Driver)
			}
		case "openrouter", "nvidia":
			if c.Driver != DriverConnectorHost {
				t.Fatalf("%s cell %s x %s must use the connector-host driver, got %q", c.Backend, c.Frontend, c.Backend, c.Driver)
			}
			if c.Meta.ToolsViable || c.Meta.MultimodalViable {
				t.Fatalf("%s cell %s x %s must reject tools and multimodal before network (streaming-only connector capabilities), tools=%v multimodal=%v", c.Backend, c.Frontend, c.Backend, c.Meta.ToolsViable, c.Meta.MultimodalViable)
			}
			if c.Meta.SubsetJustification == "" {
				t.Fatalf("%s cell %s x %s must carry the connector-host justification", c.Backend, c.Frontend, c.Backend)
			}
		default:
			if c.Driver != DriverBase {
				t.Fatalf("cell %s x %s: expected base driver, got %q", c.Frontend, c.Backend, c.Driver)
			}
			if !c.Meta.ToolsViable || !c.Meta.MultimodalViable {
				t.Fatalf("cell %s x %s: expected fully viable tools and multimodal", c.Frontend, c.Backend)
			}
		}
	}
}

// TestExistingAdapterFixturesCharacterization ensures existing frontend & backend ID boundaries remain locked.
func TestExistingAdapterFixturesCharacterization(t *testing.T) {
	t.Parallel()

	existingFE := []string{"openai-responses", "openai-legacy", "anthropic", "gemini"}
	for _, id := range existingFE {
		if !slices.Contains(BundledFrontendIDs(), id) {
			t.Fatalf("expected existing frontend %q in BundledFrontendIDs", id)
		}
	}

	existingBE := []string{"openai-responses", "openai-legacy", "anthropic", "gemini", "bedrock", "acp"}
	for _, id := range existingBE {
		if !slices.Contains(BundledBackendIDs(), id) {
			t.Fatalf("expected existing backend %q in BundledBackendIDs", id)
		}
	}
}

// TestOpenRouterNVIDIAIdentityStaysOptional proves the OpenRouter/NVIDIA columns remain
// optional connector identities: the authoritative matrix references them (Requirement
// 13.5) but they are never promoted to essential backend kinds.
func TestOpenRouterNVIDIAIdentityStaysOptional(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"openrouter", "nvidia"} {
		if !slices.Contains(BundledBackendIDs(), id) {
			t.Fatalf("authoritative backend list must include compatibility identity %q", id)
		}
		if standardplugins.IsEssentialBackendKind(id) {
			t.Fatalf("optional connector %q must not be an essential backend kind", id)
		}
		for _, c := range AllCells() {
			if c.Backend == id && c.Driver != DriverConnectorHost {
				t.Fatalf("cell %s x %s must use the connector-host driver", c.Frontend, c.Backend)
			}
		}
	}
}
