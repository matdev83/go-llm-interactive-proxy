//go:build integration

package conformance

import (
	"slices"
	"testing"
)

func TestMatrixIsCompleteCartesianProduct(t *testing.T) {
	t.Parallel()
	cells := AllCells()
	fe := BundledFrontendIDs()
	be := BundledBackendIDs()
	want := len(fe) * len(be)
	if len(cells) != want {
		t.Fatalf("expected %d matrix cells, got %d", want, len(cells))
	}
	if want != 45 {
		t.Fatalf("authoritative matrix must be exactly 5×9=45 cells, got %d", want)
	}
	seen := map[string]struct{}{}
	for _, c := range cells {
		key := c.Frontend + "\x00" + c.Backend
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate cell %q × %q", c.Frontend, c.Backend)
		}
		seen[key] = struct{}{}
		if !slices.Contains(fe, c.Frontend) {
			t.Fatalf("unknown frontend %q", c.Frontend)
		}
		if !slices.Contains(be, c.Backend) {
			t.Fatalf("unknown backend %q", c.Backend)
		}
		if c.Driver == "" {
			t.Fatalf("cell %q × %q: missing matrix cell driver classification", c.Frontend, c.Backend)
		}
		switch c.Driver {
		case DriverBase, DriverConnectorHost:
		default:
			t.Fatalf("cell %q × %q: unknown driver %q", c.Frontend, c.Backend, c.Driver)
		}
		if (c.Backend == "openrouter" || c.Backend == "nvidia") && c.Driver != DriverConnectorHost {
			t.Fatalf("cell %q × %q: OpenRouter/NVIDIA must use the connector-host driver, got %q", c.Frontend, c.Backend, c.Driver)
		}
		if c.Backend != "openrouter" && c.Backend != "nvidia" && c.Driver != DriverBase {
			t.Fatalf("cell %q × %q: constructible backend must use the base driver, got %q", c.Frontend, c.Backend, c.Driver)
		}
		if !c.Meta.TextViable {
			t.Fatalf("cell %q × %q: TextViable must be true (degenerate text subset must still be justified explicitly)", c.Frontend, c.Backend)
		}
		if !c.Meta.ToolsViable || !c.Meta.MultimodalViable {
			if c.Meta.SubsetJustification == "" {
				t.Fatalf("cell %q × %q: missing SubsetJustification for limited subset (tools=%v multimodal=%v)",
					c.Frontend, c.Backend, c.Meta.ToolsViable, c.Meta.MultimodalViable)
			}
		}
	}
	for _, f := range fe {
		for _, b := range be {
			key := f + "\x00" + b
			if _, ok := seen[key]; !ok {
				t.Fatalf("missing matrix cell for %q × %q", f, b)
			}
		}
	}
}

func TestBundledProtocolsMustMatchMatrixAuthoritativeLists(t *testing.T) {
	t.Parallel()
	wantFE := BundledFrontendIDs()
	wantBE := BundledBackendIDs()
	for _, c := range AllCells() {
		if !slices.Contains(wantFE, c.Frontend) {
			t.Fatalf("matrix references frontend %q not in BundledFrontendIDs — update the authoritative list", c.Frontend)
		}
		if !slices.Contains(wantBE, c.Backend) {
			t.Fatalf("matrix references backend %q not in BundledBackendIDs — update the authoritative list", c.Backend)
		}
	}
}

// TestGeneralMatrix_Exactly32CellsNoOpenResponses pins the general matrix cells
// (Task 8.5): AllCells minus the OpenResponses frontend row (FE=openresponses)
// and OpenResponses backend column (BE=openresponses). 5×9 = 45, the excluded
// union is 9 + 5 − 1 = 13 (openresponses×openresponses is the shared overlap),
// so exactly 4×8 = 32 cells remain. A reviewer note claimed 24; the actual
// count is 32 and is pinned here so the arithmetic can never drift.
func TestGeneralMatrix_Exactly32CellsNoOpenResponses(t *testing.T) {
	t.Parallel()
	cells := GeneralMatrixCells()
	if len(cells) != 32 {
		t.Fatalf("GeneralMatrixCells() = %d cells, want exactly 32 (45 − (9 + 5 − 1))", len(cells))
	}
	seen := map[string]struct{}{}
	for _, c := range cells {
		if c.Frontend == FrontendOpenResponses || c.Backend == BackendOpenResponses {
			t.Fatalf("general cell %s × %s must not include the OpenResponses row/column", c.Frontend, c.Backend)
		}
		key := c.Frontend + "\x00" + c.Backend
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate general cell %s × %s", c.Frontend, c.Backend)
		}
		seen[key] = struct{}{}
	}
	// The union of the row and column must be exactly the complement of the
	// general cells (45 = 32 + 13).
	if len(AllCells())-len(cells) != 13 {
		t.Fatalf("OpenResponses row/column union = %d cells, want 13", len(AllCells())-len(cells))
	}
	for _, f := range []string{FrontendOpenAIResponses, FrontendOpenAILegacy, FrontendAnthropic, FrontendGemini} {
		for _, b := range []string{BackendOpenAIResponses, BackendOpenAILegacy, BackendAnthropic, BackendGemini, BackendBedrock, BackendACP, BackendOpenRouter, BackendNVIDIA} {
			key := f + "\x00" + b
			if _, ok := seen[key]; !ok {
				t.Fatalf("missing general cell %s × %s", f, b)
			}
		}
	}
}
