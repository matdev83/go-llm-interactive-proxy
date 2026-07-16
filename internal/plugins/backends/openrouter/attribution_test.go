package openrouter

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
)

func TestResolveAppURL_modeMatrix(t *testing.T) {
	t.Parallel()
	const captured = "https://client.example/app"
	cases := []struct {
		name     string
		cfg      Config
		captured string
		want     string
	}{
		{
			name:     "proxy_literal",
			cfg:      Config{AppURL: identity.FieldPolicy{Mode: identity.ModeProxy}},
			captured: captured,
			want:     identity.DefaultProjectURL,
		},
		{
			name:     "passthrough_present",
			cfg:      Config{AppURL: identity.FieldPolicy{Mode: identity.ModePassthrough}},
			captured: captured,
			want:     captured,
		},
		{
			name:     "passthrough_missing",
			cfg:      Config{AppURL: identity.FieldPolicy{Mode: identity.ModePassthrough}},
			captured: "",
			want:     "",
		},
		{
			name:     "custom",
			cfg:      Config{AppURL: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://custom.example/"}},
			captured: captured,
			want:     "https://custom.example/",
		},
		{
			name:     "drop",
			cfg:      Config{AppURL: identity.FieldPolicy{Mode: identity.ModeDrop}},
			captured: captured,
			want:     "",
		},
		{
			name: "legacy_client_first",
			cfg: Config{
				LegacyAppURL:  true,
				StaticReferer: "https://static.example/",
			},
			captured: captured,
			want:     captured,
		},
		{
			name: "legacy_static_fallback",
			cfg: Config{
				LegacyAppURL:  true,
				StaticReferer: "https://static.example/",
			},
			captured: "",
			want:     "https://static.example/",
		},
		{
			name: "legacy_ignores_custom_policy_when_capture_present",
			cfg: Config{
				LegacyAppURL:  true,
				StaticReferer: "https://static.example/",
				AppURL:        identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://custom-ignored.example/"},
			},
			captured: captured,
			want:     captured,
		},
		{
			name: "legacy_invalid_capture_falls_back_to_static",
			cfg: Config{
				LegacyAppURL:  true,
				StaticReferer: "https://static.example/",
				AppURL:        identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://custom-ignored.example/"},
			},
			captured: "not-a-url",
			want:     "https://static.example/",
		},
		{
			name:     "empty_mode_means_proxy",
			cfg:      Config{},
			captured: captured,
			want:     identity.DefaultProjectURL,
		},
		{
			name:     "passthrough_invalid_omitted",
			cfg:      Config{AppURL: identity.FieldPolicy{Mode: identity.ModePassthrough}},
			captured: "not-a-url",
			want:     "",
		},
		{
			name: "title_policy_does_not_affect_url",
			cfg: Config{
				AppURL:   identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://url-only.example/"},
				AppTitle: identity.FieldPolicy{Mode: identity.ModeDrop},
			},
			captured: captured,
			want:     "https://url-only.example/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveAppURL(tc.cfg, tc.captured); got != tc.want {
				t.Fatalf("resolveAppURL=%q want %q", got, tc.want)
			}
		})
	}
}

func TestResolveAppTitle_modeMatrix(t *testing.T) {
	t.Parallel()
	const captured = "ClientTitle"
	cases := []struct {
		name     string
		cfg      Config
		captured string
		want     string
	}{
		{
			name:     "proxy_literal",
			cfg:      Config{AppTitle: identity.FieldPolicy{Mode: identity.ModeProxy}},
			captured: captured,
			want:     identity.DefaultProductName,
		},
		{
			name:     "passthrough_present",
			cfg:      Config{AppTitle: identity.FieldPolicy{Mode: identity.ModePassthrough}},
			captured: captured,
			want:     captured,
		},
		{
			name:     "passthrough_missing",
			cfg:      Config{AppTitle: identity.FieldPolicy{Mode: identity.ModePassthrough}},
			captured: "",
			want:     "",
		},
		{
			name:     "custom",
			cfg:      Config{AppTitle: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "CustomTitle"}},
			captured: captured,
			want:     "CustomTitle",
		},
		{
			name:     "drop",
			cfg:      Config{AppTitle: identity.FieldPolicy{Mode: identity.ModeDrop}},
			captured: captured,
			want:     "",
		},
		{
			name: "legacy_client_first",
			cfg: Config{
				LegacyAppTitle: true,
				StaticTitle:    "StaticTitle",
			},
			captured: captured,
			want:     captured,
		},
		{
			name: "legacy_static_fallback",
			cfg: Config{
				LegacyAppTitle: true,
				StaticTitle:    "StaticTitle",
			},
			captured: "",
			want:     "StaticTitle",
		},
		{
			name: "legacy_ignores_custom_policy_when_capture_present",
			cfg: Config{
				LegacyAppTitle: true,
				StaticTitle:    "StaticTitle",
				AppTitle:       identity.FieldPolicy{Mode: identity.ModeCustom, Value: "CustomIgnored"},
			},
			captured: captured,
			want:     captured,
		},
		{
			name: "legacy_invalid_capture_falls_back_to_static",
			cfg: Config{
				LegacyAppTitle: true,
				StaticTitle:    "StaticTitle",
				AppTitle:       identity.FieldPolicy{Mode: identity.ModeCustom, Value: "CustomIgnored"},
			},
			captured: "Bad\nTitle",
			want:     "StaticTitle",
		},
		{
			name:     "empty_mode_means_proxy",
			cfg:      Config{},
			captured: captured,
			want:     identity.DefaultProductName,
		},
		{
			name:     "passthrough_control_omitted",
			cfg:      Config{AppTitle: identity.FieldPolicy{Mode: identity.ModePassthrough}},
			captured: "Bad\nTitle",
			want:     "",
		},
		{
			name: "url_policy_independent_of_title",
			cfg: Config{
				AppURL:   identity.FieldPolicy{Mode: identity.ModeDrop},
				AppTitle: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "OnlyTitle"},
			},
			captured: captured,
			want:     "OnlyTitle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveAppTitle(tc.cfg, tc.captured); got != tc.want {
				t.Fatalf("resolveAppTitle=%q want %q", got, tc.want)
			}
		})
	}
}
