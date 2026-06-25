package pluginreg

import (
	"fmt"
	"reflect"
	"testing"
)

func clearNumberedEnv(t *testing.T, prefix string) {
	t.Helper()
	for i := 1; i <= maxNumberedAPIKeysEnv; i++ {
		t.Setenv(fmt.Sprintf("%s_%d", prefix, i), "")
	}
}

func clearAllProviderEnv(t *testing.T) {
	t.Helper()
	for _, prefix := range []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY",
		"OPENROUTER_API_KEY", "NVIDIA_API_KEY",
		"OPENCODE_GO_API_KEY", "OPENCODE_API_KEY", "OPENCODE_ZEN_API_KEY",
	} {
		t.Setenv(prefix, "")
		clearNumberedEnv(t, prefix)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_numberedOpenAI(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "k1")
	t.Setenv("OPENAI_API_KEY_2", "k2")
	t.Setenv("OPENAI_API_KEY_3", "k3")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"k1", "k2", "k3"}
	if !reflect.DeepEqual(got.OpenAI, want) {
		t.Fatalf("OpenAI keys: %#v want %#v", got.OpenAI, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_stopsAtGap(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "a")
	t.Setenv("OPENAI_API_KEY_2", "")
	t.Setenv("OPENAI_API_KEY_3", "c")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"a"}
	if !reflect.DeepEqual(got.OpenAI, want) {
		t.Fatalf("OpenAI keys: %#v want %#v (gap at _2 stops numbered scan)", got.OpenAI, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_secondaryWithoutPrimary(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY_2", "only2")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"only2"}
	if !reflect.DeepEqual(got.OpenAI, want) {
		t.Fatalf("OpenAI keys: %#v want %#v", got.OpenAI, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_anthropicAndGemini(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("ANTHROPIC_API_KEY", "a1")
	t.Setenv("ANTHROPIC_API_KEY_2", "a2")
	t.Setenv("GEMINI_API_KEY", "g1")
	got := ResolveUpstreamAPIKeysFromEnv()
	if !reflect.DeepEqual(got.Anthropic, []string{"a1", "a2"}) {
		t.Fatalf("Anthropic: %#v", got.Anthropic)
	}
	if !reflect.DeepEqual(got.Gemini, []string{"g1"}) {
		t.Fatalf("Gemini: %#v", got.Gemini)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openRouterBaseKey(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"or-key"}
	if !reflect.DeepEqual(got.OpenRouter, want) {
		t.Fatalf("OpenRouter keys: %#v want %#v", got.OpenRouter, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openRouterNumberedFrom1(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY_1", "or-1")
	t.Setenv("OPENROUTER_API_KEY_2", "or-2")
	t.Setenv("OPENROUTER_API_KEY_3", "or-3")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"or-1", "or-2", "or-3"}
	if !reflect.DeepEqual(got.OpenRouter, want) {
		t.Fatalf("OpenRouter keys: %#v want %#v", got.OpenRouter, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openRouterBaseAnd1Deduplicated(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "or-base")
	t.Setenv("OPENROUTER_API_KEY_1", "or-base")
	t.Setenv("OPENROUTER_API_KEY_2", "or-2")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"or-base", "or-2"}
	if !reflect.DeepEqual(got.OpenRouter, want) {
		t.Fatalf("OpenRouter keys: %#v want %#v (should deduplicate base and _1)", got.OpenRouter, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_openRouterGapStops(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("OPENROUTER_API_KEY_1", "or-1")
	t.Setenv("OPENROUTER_API_KEY_2", "")
	t.Setenv("OPENROUTER_API_KEY_3", "or-3")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"or-1"}
	if !reflect.DeepEqual(got.OpenRouter, want) {
		t.Fatalf("OpenRouter keys: %#v want %#v (gap at _2 stops scan)", got.OpenRouter, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_nvidiaBaseKey(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("NVIDIA_API_KEY", "nvapi-key")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"nvapi-key"}
	if !reflect.DeepEqual(got.Nvidia, want) {
		t.Fatalf("Nvidia keys: %#v want %#v", got.Nvidia, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_nvidiaNumberedFrom1(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("NVIDIA_API_KEY", "")
	t.Setenv("NVIDIA_API_KEY_1", "nv-1")
	t.Setenv("NVIDIA_API_KEY_2", "nv-2")
	t.Setenv("NVIDIA_API_KEY_3", "nv-3")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"nv-1", "nv-2", "nv-3"}
	if !reflect.DeepEqual(got.Nvidia, want) {
		t.Fatalf("Nvidia keys: %#v want %#v", got.Nvidia, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_nvidiaBaseAnd1Deduplicated(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("NVIDIA_API_KEY", "nv-base")
	t.Setenv("NVIDIA_API_KEY_1", "nv-base")
	t.Setenv("NVIDIA_API_KEY_2", "nv-2")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"nv-base", "nv-2"}
	if !reflect.DeepEqual(got.Nvidia, want) {
		t.Fatalf("Nvidia keys: %#v want %#v (should deduplicate base and _1)", got.Nvidia, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_nvidiaGapStops(t *testing.T) {
	clearAllProviderEnv(t)
	t.Setenv("NVIDIA_API_KEY_1", "nv-1")
	t.Setenv("NVIDIA_API_KEY_2", "")
	t.Setenv("NVIDIA_API_KEY_3", "nv-3")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"nv-1"}
	if !reflect.DeepEqual(got.Nvidia, want) {
		t.Fatalf("Nvidia keys: %#v want %#v (gap at _2 stops scan)", got.Nvidia, want)
	}
}

func TestEffectiveAPIKeys_yamlOverridesEnvList(t *testing.T) {
	t.Parallel()
	got := EffectiveAPIKeys("from-yaml", nil, []string{"env1", "env2"})
	want := []string{"from-yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
