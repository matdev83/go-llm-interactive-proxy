package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

func TestInterleaved_DefaultsDisabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Interleaved.Enabled {
		t.Fatal("interleaved thinking must be disabled by default")
	}
	if cfg.Interleaved.EffectiveStreamToClient() != "hidden" {
		t.Fatalf("default visibility must be hidden, got %q", cfg.Interleaved.EffectiveStreamToClient())
	}
	if got := cfg.Interleaved.EffectiveRegularTurnsRemaining(); got != 2 {
		t.Fatalf("default regular turns must be 2, got %d", got)
	}
	if got := cfg.Interleaved.EffectiveMaxMemoBytes(); got <= 0 {
		t.Fatalf("default max memo bytes must be positive, got %d", got)
	}
}

func TestInterleaved_EnabledValidAppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Interleaved: config.InterleavedConfig{
			Enabled: true,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Interleaved.StreamToClient != "hidden" {
		t.Fatalf("expected normalized visibility hidden, got %q", cfg.Interleaved.StreamToClient)
	}
	if cfg.Interleaved.RegularTurnsRemaining != 2 {
		t.Fatalf("expected default regular turns 2, got %d", cfg.Interleaved.RegularTurnsRemaining)
	}
	if cfg.Interleaved.MaxMemoBytes <= 0 {
		t.Fatalf("expected positive default max memo bytes, got %d", cfg.Interleaved.MaxMemoBytes)
	}
}

func TestInterleaved_EnabledValidExplicitValues(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		ConfigDir: t.TempDir(),
		Interleaved: config.InterleavedConfig{
			Enabled:               true,
			InstructionsFile:      "thinker.txt",
			StreamToClient:        "visible",
			RegularTurnsRemaining: 5,
			MaxMemoBytes:          4096,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Interleaved.StreamToClient != "visible" {
		t.Fatalf("expected visible, got %q", cfg.Interleaved.StreamToClient)
	}
	if cfg.Interleaved.RegularTurnsRemaining != 5 {
		t.Fatalf("expected 5, got %d", cfg.Interleaved.RegularTurnsRemaining)
	}
	if cfg.Interleaved.MaxMemoBytes != 4096 {
		t.Fatalf("expected 4096, got %d", cfg.Interleaved.MaxMemoBytes)
	}
}

func TestInterleaved_FailClosedInvalid(t *testing.T) {
	t.Parallel()
	base := func() *config.Config {
		return &config.Config{
			Interleaved: config.InterleavedConfig{Enabled: true},
			Plugins: config.PluginsConfig{
				Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
			},
		}
	}
	cases := []struct {
		name  string
		mut   func(*config.InterleavedConfig)
		field string
	}{
		{"negative regular turns", func(c *config.InterleavedConfig) { c.RegularTurnsRemaining = -1 }, "regular_turns_remaining"},
		{"negative max memo bytes", func(c *config.InterleavedConfig) { c.MaxMemoBytes = -1 }, "max_memo_bytes"},
		{"zero max memo bytes explicit", func(c *config.InterleavedConfig) { c.MaxMemoBytes = -1 }, "max_memo_bytes"},
		{"invalid visibility", func(c *config.InterleavedConfig) { c.StreamToClient = "loud" }, "stream_to_client"},
		{"instructions file with nul", func(c *config.InterleavedConfig) { c.InstructionsFile = "bad\x00path" }, "instructions_file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			tc.mut(&cfg.Interleaved)
			err := config.Validate(cfg)
			if err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error %q must mention field %q", err.Error(), tc.field)
			}
		})
	}
}

func TestInterleaved_DisabledIgnoresInvalidFields(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Interleaved: config.InterleavedConfig{
			Enabled:        false,
			StreamToClient: "loud",
			MaxMemoBytes:   -1,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("disabled interleaved must not fail validation: %v", err)
	}
}

func TestInterleaved_ResolveInstructionsDisabled(t *testing.T) {
	t.Parallel()
	got, err := config.ResolveInterleavedInstructions(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("disabled config must yield empty instructions, got %q", got)
	}
}

func TestInterleaved_ResolveInstructionsDefault(t *testing.T) {
	t.Parallel()
	got, err := config.ResolveInterleavedInstructions(&config.Config{
		Interleaved: config.InterleavedConfig{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("enabled config with empty instructions_file must use built-in default")
	}
	if !strings.Contains(got, "<proxy_thinker_memo>") {
		t.Fatalf("default instructions must mention memo wrapper, got %q", got)
	}
}

func TestResolveInterleavedInstructions_RejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	secretDir := filepath.Join(root, "secret")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("CONFIDENTIAL_INSTRUCTIONS"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(configDir, "link.txt")
	if err := os.Symlink(secretFile, linkPath); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	cfg := &config.Config{
		ConfigDir: configDir,
		Interleaved: config.InterleavedConfig{
			Enabled:          true,
			InstructionsFile: "link.txt",
		},
	}
	got, err := config.ResolveInterleavedInstructions(cfg)
	if err == nil {
		t.Fatalf("expected symlink escape rejection, read %q", got)
	}
	if !strings.Contains(err.Error(), "instructions_file") {
		t.Fatalf("error %q must mention instructions_file", err.Error())
	}
}

func TestResolveInterleavedInstructions_RejectsOversizedFile(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	path := filepath.Join(configDir, "big.txt")
	max := config.DefaultInterleavedMaxInstructionsBytes
	if err := os.WriteFile(path, []byte(strings.Repeat("x", max+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ConfigDir: configDir,
		Interleaved: config.InterleavedConfig{
			Enabled:          true,
			InstructionsFile: "big.txt",
		},
	}
	got, err := config.ResolveInterleavedInstructions(cfg)
	if err == nil {
		t.Fatalf("expected oversized file rejection, read %d bytes", len(got))
	}
	if !strings.Contains(err.Error(), "instructions_file") {
		t.Fatalf("error %q must mention instructions_file", err.Error())
	}
}

func TestResolveInterleavedInstructions_RejectsPathEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	secretDir := filepath.Join(root, "secret")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("CONFIDENTIAL_INSTRUCTIONS"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ConfigDir: configDir,
		Interleaved: config.InterleavedConfig{
			Enabled:          true,
			InstructionsFile: filepath.Join("..", "secret", "secret.txt"),
		},
	}
	got, err := config.ResolveInterleavedInstructions(cfg)
	if err == nil {
		t.Fatalf("expected path escape rejection, read %q", got)
	}
	if !strings.Contains(err.Error(), "instructions_file") {
		t.Fatalf("error %q must mention instructions_file", err.Error())
	}
}
