package stopguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStopguard_NoExplicitCompletionAliasLiterals ensures stopguard policy does
// not hard-code explicit completion tool name literals or branch on them.
// Adapters own translation via lipapi canonical fact (Requirement 5.7, 10.3).
func TestStopguard_NoExplicitCompletionAliasLiterals(t *testing.T) {
	t.Parallel()
	explicitLiterals := []string{
		`"attempt_completion"`,
		`"attempt_complete"`,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if e.Name() == "explicit_completion_architecture_test.go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		text := strings.ToLower(string(data))
		for _, lit := range explicitLiterals {
			if strings.Contains(text, strings.ToLower(lit)) {
				t.Errorf("stopguard/%s must not hard-code explicit completion tool name %s; adapters own translation via lipapi", e.Name(), lit)
			}
		}
		// Also forbid direct string comparison against explicit names (policy branching).
		if strings.Contains(text, "attempt_completion") || strings.Contains(text, "attempt_complete") {
			t.Errorf("stopguard/%s must not branch on explicit completion alias literals", e.Name())
		}
	}
}
