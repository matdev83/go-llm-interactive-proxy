package auth

import (
	"strings"
	"testing"
)

func TestSanitizePublicChallengeSummary(t *testing.T) {
	t.Parallel()
	fb := "fallback-msg"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty uses fallback", "", fb},
		{"whitespace uses fallback", "   \t  ", fb},
		{"plain short unchanged", "Sign in to continue.", "Sign in to continue."},
		{"collapses internal space", "a  \tb", "a b"},
		{"strips controls", "line1\x00line2", "line1 line2"},
		{"bearer triggers fallback", "Please use Bearer sk-abc", fb},
		{"access_token triggers fallback", "Open https://x?access_token=secret", fb},
		{"long truncates", strings.Repeat("x", 400), strings.Repeat("x", 254) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizePublicChallengeSummary(tc.in, fb, 255)
			if got != tc.want {
				t.Fatalf("SanitizePublicChallengeSummary(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
