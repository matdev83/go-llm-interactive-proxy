package archtest

import (
	"testing"
)

// TestFrontendsDoNotImportBackends keeps plugin package zones separate: frontend
// production code must not import internal/plugins/backends (openai-responses-reasoning-
// preservation independent review finding 3). Test packages are excluded via -test=false.
func TestFrontendsDoNotImportBackends(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		sub, msg string
	}{
		{
			"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends",
			"internal/plugins/frontends must not import internal/plugins/backends",
		},
	}
	assertGoListImportsExclude(t, "./internal/plugins/frontends/...", forbidden)
}
