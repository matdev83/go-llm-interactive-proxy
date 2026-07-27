package product

import "github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"

// bridgeExeCache backs bridge executable PATH lookups for this connector.
// Production code must not use a shared process-global cache across connector
// kinds; this cache is owned by the cursorsdk product package only.
var bridgeExeCache = &acp.ExecutableCache{}

func checkBridgeExecutable(candidate string) (string, bool) {
	return bridgeExeCache.CheckExecutable(candidate)
}

// ResetLookPathCache clears cached LookPath results. Tests that mutate PATH
// must call this before and after the mutation window.
func ResetLookPathCache() {
	bridgeExeCache.Reset()
}
