package secretsguard_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func BenchmarkMatcher_ScanNoMatchLargeInput(b *testing.B) {
	cat := benchCatalog(b, 64)
	m := secretsguard.NewMatcher(cat)
	input := []byte(strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 4096))
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		_ = m.ScanBytes(input)
	}
}

func BenchmarkMatcher_ScanManyPatterns(b *testing.B) {
	cat := benchCatalog(b, 512)
	m := secretsguard.NewMatcher(cat)
	input := []byte(strings.Repeat("nomatch-payload-block-", 256))
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		_ = m.ScanBytes(input)
	}
}

func BenchmarkMatcher_ScanManyMatches(b *testing.B) {
	const n = 64
	inputs := make([]secretsguard.CatalogInput, 0, n)
	var bld strings.Builder
	for i := range n {
		val := fmt.Sprintf("sg-bench-match-value-%04d-xxxx", i)
		inputs = append(inputs, secretsguard.CatalogInput{
			Name:           fmt.Sprintf("BENCH_MATCH_%04d", i),
			Value:          val,
			SourceCategory: secretguard.SourceCategoryProxyEnv,
		})
		bld.WriteString(val)
		bld.WriteByte('|')
	}
	cat, err := secretsguard.BuildCatalog(inputs, 8)
	if err != nil {
		b.Fatal(err)
	}
	m := secretsguard.NewMatcher(cat)
	input := []byte(bld.String())
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		_ = m.ScanBytes(input)
	}
}

func BenchmarkMatcher_ScanLargeToolResult(b *testing.B) {
	cat := benchCatalog(b, 64)
	m := secretsguard.NewMatcher(cat)
	// Large tool_result-shaped payload with no matches.
	payload := []byte(`{"result":"` + strings.Repeat("tool-output-chunk-", 8192) + `"}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for b.Loop() {
		_ = m.ScanBytes(payload)
	}
}

func benchCatalog(b *testing.B, n int) *secretsguard.Catalog {
	b.Helper()
	inputs := make([]secretsguard.CatalogInput, 0, n)
	for i := range n {
		inputs = append(inputs, secretsguard.CatalogInput{
			Name:           fmt.Sprintf("BENCH_SECRET_%04d", i),
			Value:          fmt.Sprintf("sg-bench-pattern-value-%04d-xxxx", i),
			SourceCategory: secretguard.SourceCategoryProxyEnv,
		})
	}
	cat, err := secretsguard.BuildCatalog(inputs, 8)
	if err != nil {
		b.Fatal(err)
	}
	return cat
}
