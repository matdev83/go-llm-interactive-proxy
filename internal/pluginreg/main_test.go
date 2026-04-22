package pluginreg

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain guards this package. standardbundle uses the same OpenCensus ignore in its TestMain.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// OpenCensus registers a global stats worker via init; not owned by this package.
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)
}
