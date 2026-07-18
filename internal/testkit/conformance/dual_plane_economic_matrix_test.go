package conformance

import "testing"

func TestDualPlaneEconomicModes_StableVocabulary(t *testing.T) {
	t.Parallel()
	modes := DualPlaneEconomicModes()
	if len(modes) != 5 {
		t.Fatalf("modes=%d want 5", len(modes))
	}
	for _, m := range modes {
		if !m.IsKnown() {
			t.Fatalf("unknown mode %q", m)
		}
	}
	cells := DualPlaneEconomicCells()
	if len(cells) != len(BundledFrontendIDs())*len(modes) {
		t.Fatalf("cells=%d", len(cells))
	}
}
