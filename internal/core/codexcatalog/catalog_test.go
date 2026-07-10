package codexcatalog_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
)

// sampleRawCatalog is a minimal `codex debug models` payload covering the
// routable/CLI-only/hidden edge cases.
func sampleRawCatalog() []byte {
	return []byte(`{
  "models": [
    {
      "slug": "gpt-5.6-sol",
      "default_reasoning_level": "low",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"},
        {"effort": "max", "description": "d"},
        {"effort": "ultra", "description": "d"}
      ],
      "visibility": "list",
      "supported_in_api": true,
      "context_window": 372000,
      "max_context_window": 372000
    },
    {
      "slug": "gpt-5.6-luna",
      "default_reasoning_level": "medium",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"},
        {"effort": "max", "description": "d"}
      ],
      "visibility": "list",
      "supported_in_api": true,
      "context_window": 372000,
      "max_context_window": 372000
    },
    {
      "slug": "gpt-5.5",
      "default_reasoning_level": "medium",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"}
      ],
      "visibility": "list",
      "supported_in_api": true,
      "context_window": 272000,
      "max_context_window": 272000
    },
    {
      "slug": "gpt-5.3-codex-spark",
      "default_reasoning_level": "high",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"}
      ],
      "visibility": "list",
      "supported_in_api": false,
      "context_window": 128000,
      "max_context_window": 128000
    },
    {
      "slug": "codex-auto-review",
      "default_reasoning_level": "medium",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"}
      ],
      "visibility": "hide",
      "supported_in_api": true,
      "context_window": 272000,
      "max_context_window": 1000000
    }
  ]
}`)
}

func TestParse_RoutableSlugsExcludeCLIOnlyAndHidden(t *testing.T) {
	t.Parallel()
	c, err := codexcatalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5"}
	got := c.RoutableSlugs()
	if !slices.Equal(got, want) {
		t.Fatalf("RoutableSlugs = %v, want %v", got, want)
	}
}

func TestParse_EffortOrderDerivedFromWidestModel(t *testing.T) {
	t.Parallel()
	c, err := codexcatalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	if !slices.Equal(c.ReasoningEffortOrder(), want) {
		t.Fatalf("ReasoningEffortOrder = %v, want %v", c.ReasoningEffortOrder(), want)
	}
	if c.DefaultReasoningEffort() != "medium" {
		t.Fatalf("DefaultReasoningEffort = %q, want %q", c.DefaultReasoningEffort(), "medium")
	}
}

func TestParse_IsSupported(t *testing.T) {
	t.Parallel()
	c, err := codexcatalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, slug := range []string{"gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5", "GPT-5.6-SOL"} {
		if !c.IsSupported(slug) {
			t.Fatalf("IsSupported(%q) = false, want true", slug)
		}
	}
	for _, slug := range []string{"gpt-5.3-codex-spark", "codex-auto-review", "gpt-5.1-codex", "claude-3"} {
		if c.IsSupported(slug) {
			t.Fatalf("IsSupported(%q) = true, want false", slug)
		}
	}
}

func TestParse_ProfilePerModel(t *testing.T) {
	t.Parallel()
	c, err := codexcatalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sol, ok := c.Profile("gpt-5.6-sol")
	if !ok {
		t.Fatal("Profile(gpt-5.6-sol) not found")
	}
	if sol.DefaultReasoningLevel != "low" {
		t.Fatalf("DefaultReasoningLevel = %q, want %q", sol.DefaultReasoningLevel, "low")
	}
	if !sol.APIAccepted() {
		t.Fatal("APIAccepted = false, want true")
	}
	if sol.ContextWindow != 372000 {
		t.Fatalf("ContextWindow = %d, want 372000", sol.ContextWindow)
	}
	spark, ok := c.Profile("gpt-5.3-codex-spark")
	if !ok {
		t.Fatal("Profile(gpt-5.3-codex-spark) not found")
	}
	if spark.APIAccepted() {
		t.Fatal("spark APIAccepted = true, want false (CLI-only)")
	}
}

func TestParse_ModelsSupporting(t *testing.T) {
	t.Parallel()
	c, err := codexcatalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.ModelsSupporting("ultra"); !slices.Equal(got, []string{"gpt-5.6-sol"}) {
		t.Fatalf("ModelsSupporting(ultra) = %v, want [gpt-5.6-sol]", got)
	}
	if got := c.ModelsSupporting("max"); !slices.Equal(got, []string{"gpt-5.6-sol", "gpt-5.6-luna"}) {
		t.Fatalf("ModelsSupporting(max) = %v, want [gpt-5.6-sol gpt-5.6-luna]", got)
	}
}

func TestParse_SkipsMalformedEntries(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"models":[
		{"default_reasoning_level":"medium"},
		"not-an-object",
		{"slug":"gpt-5.5","supported_reasoning_levels":[{"description":"no effort"}]},
		{"slug":"gpt-5.6-sol","supported_reasoning_levels":[{"effort":"low","description":"d"}],"supported_in_api":true,"visibility":"list"}
	]}`)
	c, err := codexcatalog.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.RoutableSlugs(); !slices.Equal(got, []string{"gpt-5.6-sol"}) {
		t.Fatalf("RoutableSlugs = %v, want [gpt-5.6-sol]", got)
	}
}

func TestParse_EmptyAndMissingModels(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{[]byte(`{"models":[]}`), []byte(`{}`)} {
		c, err := codexcatalog.Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if len(c.RoutableSlugs()) != 0 {
			t.Fatalf("RoutableSlugs = %v, want empty", c.RoutableSlugs())
		}
	}
}

func TestParse_InvalidJSONReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := codexcatalog.Parse([]byte("not json {")); err == nil {
		t.Fatal("Parse(invalid) = nil error, want error")
	}
}

func TestParse_DefaultsVisibilityAndSupportedInAPI(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"models":[{"slug":"gpt-5.5","supported_reasoning_levels":[{"effort":"low","description":"d"}]}]}`)
	c, err := codexcatalog.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p, ok := c.Profile("gpt-5.5")
	if !ok {
		t.Fatal("Profile(gpt-5.5) not found")
	}
	if p.Visibility != "list" {
		t.Fatalf("Visibility = %q, want %q", p.Visibility, "list")
	}
	if !p.SupportedInAPI {
		t.Fatal("SupportedInAPI = false, want true (default)")
	}
	if p.DefaultReasoningLevel != "medium" {
		t.Fatalf("DefaultReasoningLevel = %q, want %q", p.DefaultReasoningLevel, "medium")
	}
}
