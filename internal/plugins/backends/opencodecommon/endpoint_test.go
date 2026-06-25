package opencodecommon

import "testing"

func TestEndpointBaseURL_flavorSpecificBases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		entry    ModelEntry
		flavor   Flavor
		defaultB string
		want     string
	}{
		{
			name:     "openai chat keeps v1 prefix",
			entry:    ModelEntry{Endpoint: "https://opencode.ai/zen/v1/chat/completions"},
			flavor:   FlavorOpenAIChat,
			defaultB: "https://fallback.test",
			want:     "https://opencode.ai/zen/v1",
		},
		{
			name:     "anthropic strips v1 messages suffix",
			entry:    ModelEntry{Endpoint: "https://opencode.ai/zen/v1/messages"},
			flavor:   FlavorAnthropicMessages,
			defaultB: "https://fallback.test",
			want:     "https://opencode.ai/zen",
		},
		{
			name:     "google uses origin only",
			entry:    ModelEntry{Endpoint: "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-pro"},
			flavor:   FlavorGoogleGemini,
			defaultB: "https://fallback.test",
			want:     "https://generativelanguage.googleapis.com",
		},
		{
			name:     "missing endpoint uses default base for openai chat",
			entry:    ModelEntry{RawID: "kimi-k2.7-code"},
			flavor:   FlavorOpenAIChat,
			defaultB: "https://opencode.ai/zen",
			want:     "https://opencode.ai/zen/v1",
		},
		{
			name:     "missing endpoint does not double v1 default base",
			entry:    ModelEntry{RawID: "kimi-k2.7-code"},
			flavor:   FlavorOpenAIChat,
			defaultB: "https://opencode.ai/zen/v1",
			want:     "https://opencode.ai/zen/v1",
		},
		{
			name:     "missing google endpoint does not double v1beta default base",
			entry:    ModelEntry{RawID: "gemini-3.1-pro"},
			flavor:   FlavorGoogleGemini,
			defaultB: "https://opencode.ai/zen/v1beta",
			want:     "https://opencode.ai/zen",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := EndpointBaseURL(tc.entry, tc.defaultB, tc.flavor); got != tc.want {
				t.Fatalf("EndpointBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
