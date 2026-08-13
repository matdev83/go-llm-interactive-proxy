package secretguard_test

import (
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestBuildCatalog_MinSecretBytesDropsShort(t *testing.T) {
	t.Parallel()
	cat, err := secretguard.BuildCatalog([]secretguard.CatalogInput{
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
		{
			Name:           "LIP_TEST_SECRETGUARD_SHORT",
			Value:          testkit.SyntheticShortSecret,
			SourceCategory: sdk.SourceCategoryOperatorEnv,
		},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if cat.EntryCount() != 1 {
		t.Fatalf("EntryCount=%d want 1 (short secret dropped)", cat.EntryCount())
	}
}

func TestBuildCatalog_MinSecretBytesDefaultEight(t *testing.T) {
	t.Parallel()
	cat, err := secretguard.BuildCatalog([]secretguard.CatalogInput{
		{
			Name:           "LIP_TEST_SECRETGUARD_SHORT",
			Value:          testkit.SyntheticShortSecret,
			SourceCategory: sdk.SourceCategoryOperatorEnv,
		},
		{
			Name:           "OPENAI_API_KEY",
			Value:          testkit.SyntheticOpenAIAPIKey,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cat.EntryCount() != 1 {
		t.Fatalf("EntryCount=%d want 1 when minSecretBytes defaults to 8", cat.EntryCount())
	}
}

func TestBuildCatalog_DedupeIdenticalValuesAliases(t *testing.T) {
	t.Parallel()
	cat, err := secretguard.BuildCatalog([]secretguard.CatalogInput{
		{
			Name:           "ZZZ_SHARED",
			Value:          testkit.SyntheticDuplicateValueAliasA,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		},
		{
			Name:           "AAA_SHARED",
			Value:          testkit.SyntheticDuplicateValueAliasB,
			SourceCategory: sdk.SourceCategoryPopularEnv,
		},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if cat.EntryCount() != 1 {
		t.Fatalf("EntryCount=%d want 1 after value dedupe", cat.EntryCount())
	}

	m := secretguard.NewMatcher(cat)
	findings := m.ScanString("prefix " + testkit.SyntheticDuplicateValueAliasA + " suffix")
	assertFindingsNeverContainSecretValues(t, findings)
	if len(findings) != 1 {
		t.Fatalf("findings len=%d want 1", len(findings))
	}
	if findings[0].SecretRefName != "AAA_SHARED" {
		t.Fatalf("primary ref=%q want AAA_SHARED (lexicographically first)", findings[0].SecretRefName)
	}
	if len(findings[0].Aliases) != 1 || findings[0].Aliases[0] != "ZZZ_SHARED" {
		t.Fatalf("aliases=%v want [ZZZ_SHARED]", findings[0].Aliases)
	}
}

func TestBuildCatalog_EntryCountEmpty(t *testing.T) {
	t.Parallel()
	cat, err := secretguard.BuildCatalog(nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if cat.EntryCount() != 0 {
		t.Fatalf("EntryCount=%d want 0", cat.EntryCount())
	}
	if (*secretguard.Catalog)(nil).EntryCount() != 0 {
		t.Fatal("nil Catalog EntryCount must be 0")
	}
}

func TestCatalog_hasNoValuesOrSecretsMethods(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[*secretguard.Catalog]()
	for m := range rt.Methods() {
		switch m.Name {
		case "Values", "Secrets", "Env", "RawSecrets", "Catalog":
			t.Fatalf("Catalog must not expose raw accessor method %q", m.Name)
		}
	}
	elem := rt.Elem()
	for m := range elem.Methods() {
		switch m.Name {
		case "Values", "Secrets", "Env", "RawSecrets", "Catalog":
			t.Fatalf("Catalog must not expose raw accessor method %q", m.Name)
		}
	}
}
