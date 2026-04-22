package standardbundle

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// Same OpenCensus global worker as parent pluginreg tests (transitive init).
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)
}
