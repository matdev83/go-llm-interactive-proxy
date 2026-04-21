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
