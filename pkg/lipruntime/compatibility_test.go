package lipruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
)

func TestBuildCompatibility_StrictFixturesReject(t *testing.T) {
	t.Parallel()
	secret := "sk-public-build-secret"
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	raw := []byte("server: [\napi_key: " + secret + "\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := lipruntime.Build(context.Background(), lipruntime.Options{ConfigPath: path})
	if err == nil {
		t.Fatal("public Build must reject malformed config")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("Build error leaked secret: %q", msg)
	}
	if !strings.Contains(msg, string(configsource.CategoryMalformedYAML)) {
		t.Fatalf("error=%q want category %q", msg, configsource.CategoryMalformedYAML)
	}
	cat, ok := configsource.CategoryOf(err)
	if !ok || cat != configsource.CategoryMalformedYAML {
		t.Fatalf("CategoryOf=%q ok=%v want %q", cat, ok, configsource.CategoryMalformedYAML)
	}
}

func TestBuildCompatibility_ValidExampleReady(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "config", "examples", "dogfood-local-stub.yaml")
	rt, err := lipruntime.Build(context.Background(), lipruntime.Options{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close(context.Background()) }()
	if !rt.Ready() || rt.ExecutorView() == nil {
		t.Fatal("expected ready runtime with executor view")
	}
}
