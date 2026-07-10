package codexreasoning

import "testing"

func TestStripEmptyHTMLCommentMarkers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty comment marker", "<!-- -->", ""},
		{"inline empty comment", "start<!-- -->end", "startend"},
		{"trailing comment open", "**Plan**\n\n<!--", "**Plan**\n\n"},
		{"leading comment close", " -->text", "text"},
		{"split close only", " -->", ""},
		{"non-empty comment preserved", "<!-- note -->", "<!-- note -->"},
		{"text unchanged", "plain reasoning", "plain reasoning"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := StripEmptyHTMLCommentMarkers(tc.input)
			if got != tc.expected {
				t.Fatalf("StripEmptyHTMLCommentMarkers(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}
