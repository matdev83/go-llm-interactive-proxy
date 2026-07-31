//go:build integration

package conformance

import (
	"testing"
)

func TestParity_ACP_retiredFromStaticMatrix(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if cell.Backend == "acp" {
			t.Fatalf("ACP cell FE=%s found in static matrix; acp must be retired from static BundledBackendIDs()", cell.Frontend)
		}
	}
}
