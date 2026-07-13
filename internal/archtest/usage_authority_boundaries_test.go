package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUsageAuthorityDomainAndAppStayInfrastructureFree keeps the new bounded
// context honest: the pure domain and app packages must not pull SQL, Bun,
// HTTP, provider SDKs, concrete plugins, or runtimebundle/composition-root
// packages into the core authority seam.
func TestUsageAuthorityDomainAndAppStayInfrastructureFree(t *testing.T) {
	t.Parallel()

	rules := []forbiddenDep{
		{Substr: "/internal/infra/", ErrMsg: "usage authority core must not depend on internal/infra"},
		{Substr: "/internal/plugins/", ErrMsg: "usage authority core must not depend on concrete plugins"},
		{Substr: "/internal/stdhttp", ErrMsg: "usage authority core must not depend on stdhttp"},
		{Substr: "/internal/infra/runtimebundle", ErrMsg: "usage authority core must not depend on runtimebundle"},
		{Substr: "database/sql", ErrMsg: "usage authority core must not depend on database/sql"},
		{Substr: "uptrace/bun", ErrMsg: "usage authority core must not depend on Bun"},
		{Substr: "modernc.org/sqlite", ErrMsg: "usage authority core must not depend on sqlite driver"},
		{Substr: "net/http", ErrMsg: "usage authority core must not depend on net/http"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "usage authority core must not depend on OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "usage authority core must not depend on Anthropic SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "usage authority core must not depend on Gemini SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "usage authority core must not depend on AWS SDK"},
	}
	assertDepsExcludeForbidden(t, []string{"./internal/core/usageauthority/domain", "./internal/core/usageauthority/app"}, rules)
}

// TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata protects
// the proxy-level authority seam from drifting toward provider-local quota
// headers, cooldowns, or retry-after fields without an explicit safe mapping.
func TestUsageAuthoritySourceDoesNotReferenceProviderLocalQuotaMetadata(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "usageauthority")
	forbidden := []string{
		"QuotaHeader",
		"Cooldown",
		"ProviderCooldown",
		"ProviderQuota",
		"RetryAfter",
		"RateLimitHeader",
		"X-RateLimit",
	}

	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		low := strings.ToLower(string(src))
		for _, term := range forbidden {
			if strings.Contains(low, strings.ToLower(term)) {
				bad = append(bad, path+" ("+term+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("usage authority source must not reference provider-local quota/cooldown metadata without an explicit safe config mapping:\n%s", strings.Join(bad, "\n"))
	}
}
