package secretsguard_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func FuzzMatcher_ScanRedactRoundTrip(f *testing.F) {
	cat, err := secretsguard.BuildCatalog([]secretsguard.CatalogInput{
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: secretguard.SourceCategoryProxyEnv,
		},
		{
			Name:           "UNICODE_SECRET",
			Value:          testkit.SyntheticUnicodeSecret,
			SourceCategory: secretguard.SourceCategoryOperatorEnv,
		},
	}, 8)
	if err != nil {
		f.Fatal(err)
	}
	m := secretsguard.NewMatcher(cat)

	f.Add([]byte("plain"))
	f.Add([]byte(testkit.SyntheticOpenAIAPIKey))
	f.Add([]byte("x" + testkit.SyntheticUnicodeSecret + "y"))

	f.Fuzz(func(t *testing.T, input []byte) {
		findings := m.ScanBytes(input)
		assertFindingsNeverContainSecretValues(t, findings)
		out, findings2 := m.RedactBytes(input)
		assertFindingsNeverContainSecretValues(t, findings2)
		if len(out) != len(input) {
			t.Fatalf("redact length=%d want %d", len(out), len(input))
		}
		for _, secret := range []string{testkit.SyntheticOpenAIAPIKey, testkit.SyntheticUnicodeSecret} {
			if bytes.Contains(out, []byte(secret)) {
				t.Fatal("redacted output still contains a catalog secret")
			}
			if strings.Contains(string(out), secret) {
				t.Fatal("redacted string still contains a catalog secret")
			}
		}
	})
}
