package config

import (
	"strings"
	"time"
)

// DefaultCodexModelCatalogTimeout is the default timeout for `codex debug models`.
const DefaultCodexModelCatalogTimeout = 10 * time.Second

// CodexModelCatalogConfig controls auto-discovery of the Codex model catalog
// (`codex debug models`) at startup and the shipped/override fallback snapshot
// shared by the openai-codex and codex app-server connectors. No model slugs
// are hardcoded in the connectors; the routable slug list comes from this
// catalog.
type CodexModelCatalogConfig struct {
	// Enabled controls whether `codex debug models` is run at startup. Defaults
	// to true (nil = enabled). When false (or discovery fails) the fallback
	// snapshot is used.
	Enabled *bool `yaml:"enabled"`
	// FallbackPath overrides the shipped embedded snapshot path. Empty = the
	// shipped snapshot embedded in the codexcatalog package.
	FallbackPath string `yaml:"fallback_path"`
	// CodexBinaryPath is an explicit codex binary path. Empty = resolve via
	// CODEX_BIN env / PATH / npm-global locations.
	CodexBinaryPath string `yaml:"codex_binary_path"`
	// Timeout is a Go duration string (e.g. "10s") for the discovery subprocess.
	// Empty or invalid = DefaultCodexModelCatalogTimeout.
	Timeout string `yaml:"timeout"`
}

// EffectiveEnabled reports whether discovery is enabled (defaults to true).
func (c CodexModelCatalogConfig) EffectiveEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// TimeoutDuration parses Timeout, falling back to DefaultCodexModelCatalogTimeout.
func (c CodexModelCatalogConfig) TimeoutDuration() time.Duration {
	s := strings.TrimSpace(c.Timeout)
	if s == "" {
		return DefaultCodexModelCatalogTimeout
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return DefaultCodexModelCatalogTimeout
	}
	return d
}
