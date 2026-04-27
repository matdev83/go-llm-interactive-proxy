//go:build integration

package conformance

import (
	"strings"
	"testing"
)

func TestParity_ACP_subsetDocumentedInMatrix(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if cell.Backend != "acp" {
			continue
		}
		if !strings.Contains(cell.Meta.SubsetJustification, "ACP") {
			t.Fatalf("ACP cell FE=%s missing subset justification: %q", cell.Frontend, cell.Meta.SubsetJustification)
		}
		if cell.Meta.ToolsViable || cell.Meta.MultimodalViable {
			t.Fatalf("ACP cell FE=%s expected tools/multimodal deferred", cell.Frontend)
		}
	}
}
