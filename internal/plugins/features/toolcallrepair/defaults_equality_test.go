package toolcallrepair_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair/repair"
	"gopkg.in/yaml.v3"
)

// TestDefaultSchemaLimitsMatchCore locks YAML package defaults to repair
// DefaultSchemaLimits without importing repair from the production config package.
func TestDefaultSchemaLimitsMatchCore(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := toolcallrepair.DecodeConfig(n)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	want := repair.DefaultSchemaLimits()
	got := cfg.Schema
	if got.MaxSchemaBytes != want.MaxSchemaBytes ||
		got.MaxNestingDepth != want.MaxNestingDepth ||
		got.MaxNodes != want.MaxNodes ||
		got.MaxProperties != want.MaxProperties ||
		got.MaxLocalRefDepth != want.MaxLocalRefDepth ||
		got.MaxCacheEntries != want.MaxCacheEntries ||
		got.MaxCacheBytes != want.MaxCacheBytes {
		t.Fatalf("feature YAML schema defaults %+v != repair DefaultSchemaLimits %+v", got, want)
	}
}

func TestDefaultMaxArgsBytesMatchCore(t *testing.T) {
	t.Parallel()
	if toolcallrepair.DefaultMaxArgsBytes != repair.DefaultMaxArgsBytes {
		t.Fatalf("feature DefaultMaxArgsBytes=%d != repair DefaultMaxArgsBytes=%d",
			toolcallrepair.DefaultMaxArgsBytes, repair.DefaultMaxArgsBytes)
	}
}
