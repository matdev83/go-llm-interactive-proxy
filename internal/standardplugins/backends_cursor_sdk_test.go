package standardplugins

import (
	"testing"
)

func TestResolveUpstreamAPIKeysFromEnv_cursorAPIKey(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("CURSOR_API_KEY", "cursor-env-secret")
	t.Setenv("CURSOR_API_KEY_2", "must-not-be-read")
	got := ResolveUpstreamAPIKeysFromEnv()
	if got.Cursor != "cursor-env-secret" {
		t.Fatalf("Cursor = %q, want cursor-env-secret", got.Cursor)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_cursorAPIKeyEmpty(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("CURSOR_API_KEY", "")
	got := ResolveUpstreamAPIKeysFromEnv()
	if got.Cursor != "" {
		t.Fatalf("Cursor = %q, want empty", got.Cursor)
	}
}
