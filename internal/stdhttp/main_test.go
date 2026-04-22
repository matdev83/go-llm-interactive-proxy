package stdhttp

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// Transitive dependency (e.g. via gRPC/OpenTelemetry exporters) starts this worker at init.
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)
}
