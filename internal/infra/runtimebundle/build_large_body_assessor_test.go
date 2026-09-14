package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/stretchr/testify/require"
)

func TestBuildLargeBodyAssessor_FallbackGenID_NotDerivedFromClock(t *testing.T) {
	t.Parallel()

	gen1 := runtimebundle.ResolveLargeBodyAssessorGenIDForTest(nil)
	gen2 := runtimebundle.ResolveLargeBodyAssessorGenIDForTest(nil)

	require.NotEmpty(t, gen1)
	require.NotEmpty(t, gen2)
	require.NotEqual(t, gen1, gen2, "multiple fallback generations must have distinct identities even when SnapshotGeneration is nil")
	require.Contains(t, gen1, "gen-fallback-")
	require.Contains(t, gen2, "gen-fallback-")
}
