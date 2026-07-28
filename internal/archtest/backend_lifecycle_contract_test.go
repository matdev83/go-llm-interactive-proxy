package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfficialBackendsHaveLifecycleContractTests(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	backendsDir := filepath.Join(root, "internal", "plugins", "backends")
	lifecycleDelegatedToSharedAdapter := map[string]string{
		"anthropic":    filepath.Join("protocols", "anthropicmessages"),
		"gemini":       filepath.Join("protocols", "geminigenerate"),
		"ollama":       "openaicompat",
		"ollama-cloud": "openaicompat",
		"llamacpp":     "openaicompat",
		"lmstudio":     "openaicompat",
		"openrouter":   "openaicompat",
		"nvidia":       "openaicompat",
		"huggingface":  "openaicompat",
		"vllm":         "openaicompat",
		// ACP CLI connectors delegate streaming to the shared acp.promptStream.
		"cursorcliacp": "acp",
		"geminicliacp": "acp",
		"agycliacp":    "acp",
	}
	skipDirs := map[string]struct{}{
		"credpool": {}, "openaicaps": {}, "openaicred": {}, "streampeek": {}, "checkcfg": {},
		"modeldiscover": {},
		"openaicompat":  {},
		"openaifamily":  {},
		"openaiusage":   {},
		"opencodetest":  {},
		"protocols":     {},
		// Shared final-wire User-Agent decorator for approved connectors (not a B-leg itself).
		"httpidentity": {},
		// Shared transport-error classification helper (not a B-leg itself).
		"transporterr": {},
	}
	entries, err := os.ReadDir(backendsDir)
	if err != nil {
		t.Fatalf("read backends: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if _, skip := skipDirs[id]; skip {
			continue
		}
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			lifecycleDir := id
			if delegate, ok := lifecycleDelegatedToSharedAdapter[id]; ok {
				lifecycleDir = delegate
			}
			path := filepath.Join(backendsDir, lifecycleDir, "lifecycle_contract_test.go")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("official backend %q must have lifecycle_contract_test.go asserting leglifecycle.BLegAttempt (dir %q): %v", id, lifecycleDir, err)
			}
			if !strings.Contains(string(b), "leglifecycle.BLegAttempt") {
				t.Fatalf("official backend %q lifecycle contract test must assert leglifecycle.BLegAttempt (dir %q)", id, lifecycleDir)
			}
		})
	}
}
