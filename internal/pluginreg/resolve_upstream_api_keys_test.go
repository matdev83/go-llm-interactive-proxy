package pluginreg

import (
	"fmt"
	"reflect"
	"testing"
)

func clearNumberedEnv(t *testing.T, prefix string) {
	t.Helper()
	for i := 2; i <= maxNumberedAPIKeysEnv; i++ {
		t.Setenv(fmt.Sprintf("%s_%d", prefix, i), "")
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

func TestEffectiveAPIKeys_yamlOverridesEnvList(t *testing.T) {
	t.Parallel()
	got := EffectiveAPIKeys("from-yaml", nil, []string{"env1", "env2"})
	want := []string{"from-yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
