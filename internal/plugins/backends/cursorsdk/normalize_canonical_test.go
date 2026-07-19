package cursorsdk

import (
	"strings"
	"testing"
)

func TestCanonicalIDForNative_MatchesCursorCLIACPStripRule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		native string
		want   string
	}{
		{"bare_gpt", "gpt-5.3-codex", "cursor/gpt-5.3-codex"},
		{"bare_composer", "composer-2-fast", "cursor/composer-2-fast"},
		{"cursor_dash", "cursor-composer-2", "cursor/composer-2"},
		{"already_canonical", "cursor/composer-2-fast", "cursor/composer-2-fast"},
		{"trimmed_dash", "  cursor-agent  ", "cursor/agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := canonicalIDForNative(tc.native); got != tc.want {
				t.Fatalf("canonicalIDForNative(%q) = %q, want %q", tc.native, got, tc.want)
			}
			acpGot := acpCanonicalIDForNative(tc.native)
			if strings.Contains(strings.TrimSpace(tc.native), "/") {
				if acpGot == tc.want {
					t.Fatalf("ACP-equivalent unexpectedly matched SDK short-circuit for slash-native %q", tc.native)
				}
				return
			}
			if acpGot != tc.want {
				t.Fatalf("ACP-equivalent(%q) = %q, want %q", tc.native, acpGot, tc.want)
			}
		})
	}
}

func acpCanonicalIDForNative(native string) string {
	path := strings.TrimSpace(native)
	if after, ok := strings.CutPrefix(path, "cursor-"); ok {
		path = after
	}
	return "cursor/" + path
}
