package reasoningpreservation_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
)

func decodeCatalogConfig(t *testing.T, yamlBody string) reasoningpreservation.Config {
	t.Helper()
	return decodeValidConfig(t, yamlBody)
}

func resolveMatch(t *testing.T, cfg reasoningpreservation.Config, cand reasoningpreservation.CandidateIdentity) reasoningpreservation.MatchResult {
	t.Helper()
	got, err := reasoningpreservation.ResolveMatch(cfg, cand)
	redNotImplemented(t, err, "ResolveMatch must be implemented")
	if err != nil {
		t.Fatalf("ResolveMatch: %v", err)
	}
	return got
}

func builtinCatalogEntries(t *testing.T) []reasoningpreservation.BuiltinCatalogEntry {
	t.Helper()
	entries, err := reasoningpreservation.BuiltinCatalogEntries()
	redNotImplemented(t, err, "BuiltinCatalogEntries must be implemented")
	if err != nil {
		t.Fatalf("BuiltinCatalogEntries: %v", err)
	}
	return entries
}

func TestBuiltinCatalogVersion_nonEmpty_contractLock(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(reasoningpreservation.BuiltinCatalogVersion) == "" {
		t.Fatal("BuiltinCatalogVersion must be non-empty")
	}
}

func TestBuiltinCatalogEntries_kimiMoonshotCoverage(t *testing.T) {
	t.Parallel()
	entries := builtinCatalogEntries(t)
	if len(entries) == 0 {
		t.Fatal("RED: BuiltinCatalogEntries must include conservative Kimi/Moonshot coverage")
	}
	var hasKimi, hasMoonshot bool
	for _, e := range entries {
		for _, kw := range e.ModelKeywords {
			k := strings.ToLower(strings.TrimSpace(kw))
			if k == "kimi" {
				hasKimi = true
			}
			if k == "moonshot" {
				hasMoonshot = true
			}
		}
	}
	if !hasKimi || !hasMoonshot {
		t.Fatalf("builtin catalog missing kimi/moonshot keywords: len=%d", len(entries))
	}
}

func TestBuiltinCatalogEntries_openAICompatiblePrefixes(t *testing.T) {
	t.Parallel()
	wantPrefixes := []string{
		"openrouter",
		"openai-legacy",
		"openai-responses",
		"nvidia",
		"huggingface",
		"ollama",
		"ollama-cloud",
		"vllm",
		"lmstudio",
		"llamacpp",
		"opencode-go",
		"opencode-zen",
	}
	entries := builtinCatalogEntries(t)
	prefixSet := make(map[string]struct{})
	for _, e := range entries {
		for _, p := range e.BackendPrefixes {
			prefixSet[p] = struct{}{}
		}
	}
	for _, want := range wantPrefixes {
		if _, ok := prefixSet[want]; !ok {
			t.Fatalf("missing required stable prefix %q across builtin catalog entries", want)
		}
	}
}

func TestResolveMatch_precedenceExplicitDisabledModel(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: disable-kimi
    backend: backend-a
    model_keywords: ["kimi"]
    enabled: false
  - id: enable-kimi
    backend: backend-a
    model_keywords: ["kimi"]
    enabled: true
  - id: enable-wide
    backend: backend-a
    enabled: true
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
		BackendID: "backend-a",
		Model:     "Kimi-K2",
	})
	if got.Kind != reasoningpreservation.MatchExplicitDisabledModel {
		t.Fatalf("kind=%q want %q", got.Kind, reasoningpreservation.MatchExplicitDisabledModel)
	}
	if got.RuleID != "disable-kimi" {
		t.Fatalf("ruleID=%q", got.RuleID)
	}
}

func TestResolveMatch_precedenceExplicitEnabledModel(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: enable-kimi
    backend: backend-a
    model_keywords: ["kimi"]
    enabled: true
  - id: disable-wide
    backend: backend-a
    enabled: false
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
		BackendID: "backend-a",
		Model:     "kimi-chat",
	})
	if got.Kind != reasoningpreservation.MatchExplicitEnabledModel {
		t.Fatalf("kind=%q want %q", got.Kind, reasoningpreservation.MatchExplicitEnabledModel)
	}
	if got.RuleID != "enable-kimi" {
		t.Fatalf("ruleID=%q", got.RuleID)
	}
}

func TestResolveMatch_precedenceExplicitDisabledBackendWide(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: disable-wide
    backend: backend-a
    enabled: false
  - id: enable-kimi
    backend: backend-a
    model_keywords: ["moonshot"]
    enabled: true
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
		BackendID: "backend-a",
		Model:     "other-model",
	})
	if got.Kind != reasoningpreservation.MatchExplicitDisabledBackend {
		t.Fatalf("kind=%q want %q", got.Kind, reasoningpreservation.MatchExplicitDisabledBackend)
	}
}

func TestResolveMatch_precedenceExplicitEnabledBackendWide(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: false
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: enable-wide
    backend: backend-a
    enabled: true
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
		BackendID: "backend-a",
		Model:     "any-model",
	})
	if got.Kind != reasoningpreservation.MatchExplicitEnabledBackend {
		t.Fatalf("kind=%q want %q", got.Kind, reasoningpreservation.MatchExplicitEnabledBackend)
	}
}

func TestResolveMatch_precedenceBuiltinOverNone(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
		BackendID:       "openrouter-custom-id",
		BackendPrefixes: []string{"openrouter"},
		Model:           "moonshot-v1-8k",
	})
	if got.Kind != reasoningpreservation.MatchBuiltin {
		t.Fatalf("kind=%q want %q", got.Kind, reasoningpreservation.MatchBuiltin)
	}
	if got.RuleID == "" {
		t.Fatal("builtin match must identify catalog rule")
	}
}

func TestResolveMatch_precedenceNone(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: false
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
		BackendID: "unknown-backend",
		Model:     "unknown-model",
	})
	if got.Kind != reasoningpreservation.MatchNone {
		t.Fatalf("kind=%q want %q", got.Kind, reasoningpreservation.MatchNone)
	}
}

func TestResolveMatch_builtinPrefixTable(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	cases := []struct {
		name     string
		prefixes []string
		model    string
		wantKind reasoningpreservation.MatchKind
	}{
		{
			name:     "openrouter_kimi",
			prefixes: []string{"openrouter"},
			model:    "kimi-k2",
			wantKind: reasoningpreservation.MatchBuiltin,
		},
		{
			name:     "unrelated_prefix",
			prefixes: []string{"totally-unrelated-prefix"},
			model:    "kimi-k2",
			wantKind: reasoningpreservation.MatchNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
				BackendID:       "arbitrary-instance",
				BackendPrefixes: tc.prefixes,
				Model:           tc.model,
			})
			if got.Kind != tc.wantKind {
				t.Fatalf("kind=%q want %q", got.Kind, tc.wantKind)
			}
			if tc.wantKind == reasoningpreservation.MatchBuiltin && got.RuleID == "" {
				t.Fatal("builtin match must identify catalog rule")
			}
		})
	}
}

func TestResolveMatch_keywordsTrimAndCaseInsensitive(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: false
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: kw-rule
    backend: backend-a
    model_keywords: ["  KiMi  ", " MOONshot "]
    enabled: true
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	for _, model := range []string{"kimi-alpha", "prefix-MOONSHOT-suffix", "KiMi"} {
		got := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
			BackendID: "backend-a",
			Model:     model,
		})
		if got.Kind != reasoningpreservation.MatchExplicitEnabledModel {
			t.Fatalf("model=%q kind=%q", model, got.Kind)
		}
	}
}

func TestResolveMatch_exactBackendInstanceMatch(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: false
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
rules:
  - id: exact-backend
    backend: prod-openrouter-7
    model_keywords: ["kimi"]
    enabled: true
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	noMatch := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
		BackendID: "prod-openrouter-8",
		Model:     "kimi-k2",
	})
	if noMatch.Kind != reasoningpreservation.MatchNone {
		t.Fatalf("non-exact backend must not match, got %q", noMatch.Kind)
	}
	match := resolveMatch(t, cfg, reasoningpreservation.CandidateIdentity{
		BackendID: "prod-openrouter-7",
		Model:     "kimi-k2",
	})
	if match.Kind != reasoningpreservation.MatchExplicitEnabledModel {
		t.Fatalf("exact backend instance must match, got %q", match.Kind)
	}
}

func TestResolveMatch_candidateIdentityUsesBackendPrefixes(t *testing.T) {
	t.Parallel()
	cfg := decodeCatalogConfig(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got, err := reasoningpreservation.ResolveMatch(cfg, reasoningpreservation.CandidateIdentity{
		BackendID:       "arbitrary-instance",
		BackendPrefixes: []string{"openrouter"},
		Model:           "kimi-latest",
	})
	redNotImplemented(t, err, "ResolveMatch must use BackendPrefixes for built-ins")
	if err != nil {
		t.Fatalf("ResolveMatch: %v", err)
	}
	if got.Kind != reasoningpreservation.MatchBuiltin {
		t.Fatalf("expected builtin match via prefixes, got %q", got.Kind)
	}
}
