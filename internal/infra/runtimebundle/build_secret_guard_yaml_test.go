package runtimebundle

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard/engine"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

func TestComposeRuntimeConfig_decodedYAMLControlsSingleUserOptions(t *testing.T) {
	t.Parallel()
	raw := mustYAMLNode(t, `
action: redact
min_secret_bytes: 12
audit_failure_policy: best_effort
single_user:
  include_popular_env: false
  include_env: [LIP_TEST_SECRETGUARD_INCLUDE]
  exclude_env: [OPENAI_API_KEY]
redaction:
  mask_byte: "X"
  preserve_known_prefixes: false
`)
	regs := []lipsdk.Registration{{
		Kind:        lipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: raw},
	}}
	runtimeCfg, err := featuresg.ComposeRuntimeConfig("single_user", regs)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeCfg.IncludePopularEnv {
		t.Fatal("include_popular_env want false from YAML")
	}
	if runtimeCfg.MinSecretBytes != 12 {
		t.Fatalf("min_secret_bytes: got %d want 12", runtimeCfg.MinSecretBytes)
	}
	if len(runtimeCfg.IncludeEnv) != 1 || runtimeCfg.IncludeEnv[0] != "LIP_TEST_SECRETGUARD_INCLUDE" {
		t.Fatalf("include_env: %#v", runtimeCfg.IncludeEnv)
	}
	if len(runtimeCfg.ExcludeEnv) != 1 || runtimeCfg.ExcludeEnv[0] != "OPENAI_API_KEY" {
		t.Fatalf("exclude_env: %#v", runtimeCfg.ExcludeEnv)
	}
	if runtimeCfg.MaskByte != 'X' {
		t.Fatalf("mask_byte: got %q want X", runtimeCfg.MaskByte)
	}
	if runtimeCfg.PreserveKnownPrefixes {
		t.Fatal("preserve_known_prefixes want false from YAML")
	}
	if runtimeCfg.AuditFailurePolicy != string(sdk.AuditBestEffort) {
		t.Fatalf("audit policy: %q", runtimeCfg.AuditFailurePolicy)
	}
	if runtimeCfg.Action != "redact" {
		t.Fatalf("action: %q", runtimeCfg.Action)
	}
	if runtimeCfg.AuditConfigVersion == "" {
		t.Fatal("config_version must be stamped from decoded YAML")
	}

	su := composeSecretGuardSingleUser(runtimeCfg, SecretGuardInputs{})
	if !su.MatcherConfigured || su.Matcher.MaskByte != 'X' {
		t.Fatalf("composed matcher: %#v", su.Matcher)
	}
	if su.Matcher.PreserveKnownPrefixes {
		t.Fatal("composed preserve_known_prefixes want false")
	}
	if su.MinSecretBytes != 12 {
		t.Fatalf("composed min_secret_bytes: %d", su.MinSecretBytes)
	}
}

func TestComposeRuntimeConfig_multiUserRejectsSingleUserKey(t *testing.T) {
	t.Parallel()
	raw := mustYAMLNode(t, "action: block\nsingle_user:\n  include_env: [FOO]")
	regs := []lipsdk.Registration{{
		Kind:        lipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: raw},
	}}
	_, err := featuresg.ComposeRuntimeConfig("multi_user", regs)
	if err == nil {
		t.Fatal("expected multi_user + single_user key rejection")
	}
	if !strings.Contains(err.Error(), "single_user is invalid in multi_user mode") {
		t.Fatalf("error: %v", err)
	}
}

func TestBuildSecretGuardRuntime_multiUserZeroEnvEvenWithMalformedSingleUser(t *testing.T) {
	t.Parallel()
	// Composition security boundary: multi-user source construction must never call Environment.
	env := &panicSGEnv{}
	opts := &BuildOptions{Extensions: ExtensionsOptions{
		SecretGuardEnvironment: env,
		SecretGuardInputs: SecretGuardInputs{
			SingleUser: engine.SingleUserOptions{
				IncludePopularEnv: true,
				IncludeEnv:        []string{"OPENAI_API_KEY"},
				MinSecretBytes:    8,
			},
		},
	}}
	regs := []lipsdk.Registration{{
		Kind:        lipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: block\n")},
	}}
	cfg := &config.Config{Access: config.AccessConfig{Mode: "multi_user"}}
	if _, err := buildSecretGuardRuntime(cfg, nilDiscardLogger(), opts, regs); err != nil {
		t.Fatal(err)
	}
	if env.calls != 0 {
		t.Fatalf("env calls=%d want 0", env.calls)
	}
}

func TestBuildSecretGuardRuntime_decodedIncludeEnvAffectsCatalog(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticDuplicateValueAliasA
	env := &mapSGEnv{vals: map[string]string{
		"LIP_TEST_SECRETGUARD_INCLUDE": secret,
		"OPENAI_API_KEY":               testkit.SyntheticOpenAIAPIKey,
	}}
	raw := mustYAMLNode(t, `
action: log
min_secret_bytes: 8
single_user:
  include_popular_env: false
  include_env: [LIP_TEST_SECRETGUARD_INCLUDE]
  exclude_env: [OPENAI_API_KEY]
`)
	regs := []lipsdk.Registration{{
		Kind:        lipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      lipsdk.ConfigPayload{Node: raw},
	}}
	opts := &BuildOptions{Extensions: ExtensionsOptions{
		SecretGuardEnvironment: env,
	}}
	rt, err := buildSecretGuardRuntime(&config.Config{}, nilDiscardLogger(), opts, regs)
	if err != nil {
		t.Fatal(err)
	}
	m, err := rt.Plane.MatcherResolver.Resolve(t.Context())
	if err != nil || m == nil {
		t.Fatalf("matcher: m=%v err=%v", m, err)
	}
	findInclude, err := m.ScanString(t.Context(), "x="+secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(findInclude) == 0 {
		t.Fatal("include_env secret must be in catalog")
	}
	findExcluded, err := m.ScanString(t.Context(), "y="+testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(findExcluded) != 0 {
		t.Fatal("exclude_env secret must not be in catalog")
	}
	if rt.Inventory == nil || rt.Inventory.SecretGuardCatalogEntryCount < 1 {
		t.Fatalf("catalog entry count: %#v", rt.Inventory)
	}
	if len(rt.Inventory.SecretGuardSourceCategories) == 0 {
		t.Fatal("source categories must be reported for inventory")
	}
	for _, s := range testkit.AllSyntheticSecretGuardValues() {
		for _, cat := range rt.Inventory.SecretGuardSourceCategories {
			if strings.Contains(cat, s) {
				t.Fatalf("source category leaked secret material (len=%d)", len(cat))
			}
		}
	}
}

func TestComposeSecretGuardSingleUser_matcherOverrideWinsOverYAML(t *testing.T) {
	t.Parallel()
	runtimeCfg := featuresg.RuntimeConfig{
		Enabled:               true,
		PreserveKnownPrefixes: true,
		MaskByte:              '*',
		MinSecretBytes:        8,
	}
	inputs := SecretGuardInputs{
		SingleUser: engine.SingleUserOptions{
			Matcher:           engine.MatcherOptions{PreserveKnownPrefixes: false, MaskByte: 'X'},
			MatcherConfigured: true,
		},
	}
	su := composeSecretGuardSingleUser(runtimeCfg, inputs)
	if su.Matcher.MaskByte != 'X' || su.Matcher.PreserveKnownPrefixes {
		t.Fatalf("matcher override lost: %#v", su.Matcher)
	}
}

func nilDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
}

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
