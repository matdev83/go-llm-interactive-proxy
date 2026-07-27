package catalog_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/catalog"
)

func TestLoad_DisabledUsesShippedFallback(t *testing.T) {
	t.Parallel()
	c, src, err := catalog.Load(context.Background(), catalog.LoadOptions{Enabled: false})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src != catalog.SourceShippedFallback {
		t.Fatalf("source = %q, want %q", src, catalog.SourceShippedFallback)
	}
	if len(c.RoutableSlugs()) == 0 {
		t.Fatal("shipped fallback has no routable slugs")
	}
}

func TestLoad_DiscoveryFailureFallsBackToShipped(t *testing.T) {
	t.Parallel()
	// Pre-cancel the context so discovery fails regardless of whether codex is
	// installed on the host (CI runners lack it; dev machines have it on PATH).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, src, err := catalog.Load(ctx, catalog.LoadOptions{Enabled: true, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src != catalog.SourceShippedFallback {
		t.Fatalf("source = %q, want %q", src, catalog.SourceShippedFallback)
	}
	if len(c.RoutableSlugs()) == 0 {
		t.Fatal("fallback has no routable slugs")
	}
}

func TestLoad_DiscoveryFailureFallsBackToOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, sampleRawCatalog(), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, src, err := catalog.Load(ctx, catalog.LoadOptions{
		Enabled:      true,
		FallbackPath: path,
		Timeout:      time.Second,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src != catalog.SourceOverrideFallback {
		t.Fatalf("source = %q, want %q", src, catalog.SourceOverrideFallback)
	}
	want := []string{"gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5"}
	if !slices.Equal(c.RoutableSlugs(), want) {
		t.Fatalf("RoutableSlugs = %v, want %v", c.RoutableSlugs(), want)
	}
}
