package secretsguard_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type countingEnvironment struct {
	lookupCalls   int
	snapshotCalls int
	vals          map[string]string
}

func (c *countingEnvironment) Lookup(name string) (string, bool) {
	c.lookupCalls++
	v, ok := c.vals[name]
	return v, ok
}

func (c *countingEnvironment) Snapshot() []string {
	c.snapshotCalls++
	out := make([]string, 0, len(c.vals))
	for k, v := range c.vals {
		out = append(out, k+"="+v)
	}
	return out
}

func TestNewSingleUserSource_sparseProxyNames(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":   testkit.SyntheticOpenAIAPIKey,
		"OPENAI_API_KEY_2": testkit.SyntheticOpenRouterAPIKey,
		"OPENAI_API_KEY_7": testkit.SyntheticGeminiAPIKey,
	}}
	src, err := secretsguard.NewSingleUserSource(env, secretsguard.SingleUserOptions{
		IncludePopularEnv: false,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() < 3 {
		t.Fatalf("want at least 3 sparse proxy entries, got %d", src.EntryCount())
	}
	if env.snapshotCalls == 0 {
		t.Fatal("single-user source must Snapshot the environment")
	}
	if src.AccessMode() != accessmode.ModeSingleUser {
		t.Fatalf("access mode: got %q", src.AccessMode())
	}
}

func TestNewSingleUserSource_minSecretBytesExcludesShort(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":             testkit.SyntheticOpenAIAPIKey,
		"LIP_TEST_SECRETGUARD_SHORT": testkit.SyntheticShortSecret,
	}}
	src, err := secretsguard.NewSingleUserSource(env, secretsguard.SingleUserOptions{
		IncludeEnv:     []string{"LIP_TEST_SECRETGUARD_SHORT"},
		MinSecretBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 1 {
		t.Fatalf("short secrets below min_secret_bytes must be excluded; entry count=%d", src.EntryCount())
	}
}

func TestNewSingleUserSource_duplicateValueAliases(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":     testkit.SyntheticDuplicateValueAliasA,
		"ANTHROPIC_API_KEY":  testkit.SyntheticDuplicateValueAliasB,
		"OPENROUTER_API_KEY": testkit.SyntheticOpenRouterAPIKey,
	}}
	src, err := secretsguard.NewSingleUserSource(env, secretsguard.SingleUserOptions{
		MinSecretBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 2 {
		t.Fatalf("duplicate values must dedupe; EntryCount=%d want 2", src.EntryCount())
	}
}

func TestNewSingleUserSource_PopularIncludeExclude(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"GITHUB_TOKEN":      testkit.SyntheticAnthropicSecretGuardKey,
		"AWS_ACCESS_KEY_ID": "AKIA" + strings.Repeat("X", 16),
		"LIP_TEST_OPERATOR": testkit.SyntheticGeminiAPIKey,
		"NPM_TOKEN":         testkit.SyntheticOpenAIAPIKey,
		"OPENAI_API_KEY":    testkit.SyntheticOpenRouterAPIKey,
	}}
	src, err := secretsguard.NewSingleUserSource(env, secretsguard.SingleUserOptions{
		IncludePopularEnv: true,
		IncludeEnv:        []string{"LIP_TEST_OPERATOR"},
		ExcludeEnv:        []string{"NPM_TOKEN"},
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	// OPENAI_API_KEY + GITHUB_TOKEN + LIP_TEST_OPERATOR; NPM_TOKEN excluded; AWS_ACCESS_KEY_ID not auto-loaded
	if src.EntryCount() != 3 {
		t.Fatalf("EntryCount=%d want 3 (exclude wins; AWS_ACCESS_KEY_ID not popular)", src.EntryCount())
	}
}

func TestNewSingleUserSource_AWSAccessKeyIDNotAutoLoaded(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIA" + strings.Repeat("Y", 16),
		"AWS_SECRET_ACCESS_KEY": testkit.SyntheticOpenAIAPIKey,
	}}
	src, err := secretsguard.NewSingleUserSource(env, secretsguard.SingleUserOptions{
		IncludePopularEnv: true,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 1 {
		t.Fatalf("only AWS_SECRET_ACCESS_KEY should load; EntryCount=%d", src.EntryCount())
	}
}

type includeLookupEnv struct {
	lookupCalls   int
	snapshotCalls int
	snapshotVals  map[string]string
	lookupOnly    map[string]string
}

func (e *includeLookupEnv) Snapshot() []string {
	e.snapshotCalls++
	out := make([]string, 0, len(e.snapshotVals))
	for k, v := range e.snapshotVals {
		out = append(out, k+"="+v)
	}
	return out
}

func (e *includeLookupEnv) Lookup(name string) (string, bool) {
	e.lookupCalls++
	if v, ok := e.snapshotVals[name]; ok {
		return v, true
	}
	v, ok := e.lookupOnly[name]
	return v, ok
}

func TestNewSingleUserSource_IncludeEnvLookupWhenAbsentFromSnapshot(t *testing.T) {
	t.Parallel()
	env := &includeLookupEnv{
		snapshotVals: map[string]string{
			"OPENAI_API_KEY": testkit.SyntheticOpenAIAPIKey,
		},
		lookupOnly: map[string]string{
			"LIP_TEST_SECRETGUARD_INCLUDE": testkit.SyntheticGeminiAPIKey,
		},
	}
	src, err := secretsguard.NewSingleUserSource(env, secretsguard.SingleUserOptions{
		IncludeEnv:     []string{"LIP_TEST_SECRETGUARD_INCLUDE"},
		MinSecretBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 2 {
		t.Fatalf("IncludeEnv via Lookup must add entry; EntryCount=%d", src.EntryCount())
	}
	if env.lookupCalls == 0 {
		t.Fatal("IncludeEnv missing from snapshot must call Lookup")
	}
}

type stubSDKMatcher struct{}

func (stubSDKMatcher) ScanBytes(context.Context, []byte) ([]secretguard.Finding, error) {
	return nil, nil
}

func (stubSDKMatcher) ScanString(context.Context, string) ([]secretguard.Finding, error) {
	return nil, nil
}

func (stubSDKMatcher) RedactBytes(context.Context, []byte) ([]byte, []secretguard.Finding, error) {
	return nil, nil, nil
}

func (stubSDKMatcher) RedactString(context.Context, string) (string, []secretguard.Finding, error) {
	return "", nil, nil
}

func TestNewDisabledSource_neverCallsEnvironment(t *testing.T) {
	t.Parallel()
	src := secretsguard.NewDisabledSource()
	if src.EntryCount() != 0 {
		t.Fatalf("EntryCount=%d want 0", src.EntryCount())
	}
	resolver := src.MatcherResolver()
	if resolver == nil {
		t.Fatal("disabled source must provide a MatcherResolver")
	}
	got, err := resolver.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("disabled resolver must return (nil, nil)")
	}
}

func TestNewMultiUserSource_ContextMatcherResolver(t *testing.T) {
	t.Parallel()
	env := &panicEnvironment{}
	src, err := secretsguard.NewMultiUserSource(env)
	if err != nil {
		t.Fatal(err)
	}
	if src.AccessMode() != accessmode.ModeMultiUser {
		t.Fatalf("access mode: got %q", src.AccessMode())
	}
	if src.EntryCount() != 0 {
		t.Fatalf("EntryCount=%d want 0", src.EntryCount())
	}

	ctx := secretguard.WithRequestMatcher(t.Context(), stubSDKMatcher{})
	got, err := src.MatcherResolver().Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("multi-user resolver must return context matcher")
	}
}

func TestPopularSecretEnvNames_excludesIDsAndPaths(t *testing.T) {
	t.Parallel()
	forbidden := map[string]struct{}{
		"AWS_ACCESS_KEY_ID":              {},
		"AWS_PROFILE":                    {},
		"GOOGLE_APPLICATION_CREDENTIALS": {},
		"CURL_CA_BUNDLE":                 {},
	}
	for _, name := range secretsguard.PopularSecretEnvNames {
		if _, bad := forbidden[name]; bad {
			t.Fatalf("PopularSecretEnvNames must not include %q", name)
		}
	}
	if !slices.Contains(secretsguard.PopularSecretEnvNames, "AWS_SECRET_ACCESS_KEY") {
		t.Fatal("PopularSecretEnvNames must include AWS_SECRET_ACCESS_KEY")
	}
}
