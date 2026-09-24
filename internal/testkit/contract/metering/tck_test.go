package metering_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/aggregate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/normalize"
	contract "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/metering"
)

func TestTCK_ActualNormalizerAndReducer(t *testing.T) {
	t.Parallel()
	contract.Run(t, contract.Env{
		Normalize: normalize.Normalize,
		Reduce:    aggregate.ApplyObservations,
	})
}
