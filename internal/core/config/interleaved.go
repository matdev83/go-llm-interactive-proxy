package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InterleavedConfig controls interleaved thinking (`[thinker]` selectors).
//
// The feature is disabled by default. When disabled, the runtime must not
// mutate requests, responses, or session state because of interleaved thinking.
// When enabled, validation applies fail-closed defaults and rejects operator
// settings that cannot safely serve traffic.
type InterleavedConfig struct {
	// Enabled turns on interleaved thinking. Disabled by default.
	Enabled bool `yaml:"enabled"`
	// InstructionsFile is an operator-supplied path to thinker planning
	// instructions loaded at thinker-turn shaping time. Optional; when empty
	// the runtime uses built-in defaults. Must not contain NUL.
	InstructionsFile string `yaml:"instructions_file"`
	// StreamToClient is the visibility mode: "hidden" (default) or "visible".
	// Normalized to lowercase by Validate when enabled.
	StreamToClient string `yaml:"stream_to_client"`
	// RegularTurnsRemaining is the memo injection budget: how many subsequent
	// executor turns may receive the captured memo. Defaults to 2 when enabled
	// and zero. Must be >= 1 when enabled.
	RegularTurnsRemaining int `yaml:"regular_turns_remaining"`
	// MaxMemoBytes bounds stored memo content. Defaults to a bounded value when
	// enabled and zero. Must be > 0 when enabled.
	MaxMemoBytes int `yaml:"max_memo_bytes"`
}

// Default interleaved thinking values applied by Validate when enabled.
const (
	DefaultInterleavedStreamToClient       = "hidden"
	DefaultInterleavedRegularTurns         = 2
	DefaultInterleavedMaxMemoBytes         = 16 * 1024
	DefaultInterleavedMaxInstructionsBytes = 64 * 1024
)

// DefaultInterleavedInstructions is the built-in thinker prompt used when
// interleaved thinking is enabled and instructions_file is empty.
const DefaultInterleavedInstructions = `You now become a thinker.

Produce a compact planning memo for the next executor model. Reflect on session progress, constraints, and the best next action. Do not ask questions, call tools, or produce final user-facing work.

When ready, return only this block:

<proxy_thinker_memo>
Goal: {goal_here}
Current state: {current state}
Constraints and risks: {constraints_and_risks}
Recommended next step: {recommended_next_step}
Reason: {reason}
</proxy_thinker_memo>`

// ResolveInterleavedInstructions returns the thinker instructions text for an
// enabled interleaved config. Disabled config yields an empty string without error.
func ResolveInterleavedInstructions(cfg *Config) (string, error) {
	if cfg == nil || !cfg.Interleaved.Enabled {
		return "", nil
	}
	path := strings.TrimSpace(cfg.Interleaved.InstructionsFile)
	if path == "" {
		return DefaultInterleavedInstructions, nil
	}
	resolved, err := resolveInterleavedInstructionsPath(cfg, path)
	if err != nil {
		return "", err
	}
	b, err := readInterleavedInstructionsFile(resolved)
	if err != nil {
		return "", fmt.Errorf("interleaved.instructions_file: %w", err)
	}
	content := strings.TrimSpace(string(b))
	if content == "" {
		return "", fmt.Errorf("interleaved.instructions_file: file is empty")
	}
	return content, nil
}

// EffectiveStreamToClient returns the visibility mode, defaulting to hidden when unset.
func (i InterleavedConfig) EffectiveStreamToClient() string {
	if v := strings.ToLower(strings.TrimSpace(i.StreamToClient)); v != "" {
		return v
	}
	return DefaultInterleavedStreamToClient
}

// EffectiveRegularTurnsRemaining returns the configured budget or the default when zero.
func (i InterleavedConfig) EffectiveRegularTurnsRemaining() int {
	if i.RegularTurnsRemaining > 0 {
		return i.RegularTurnsRemaining
	}
	return DefaultInterleavedRegularTurns
}

// EffectiveMaxMemoBytes returns the configured memo size limit or the default when zero.
func (i InterleavedConfig) EffectiveMaxMemoBytes() int {
	if i.MaxMemoBytes > 0 {
		return i.MaxMemoBytes
	}
	return DefaultInterleavedMaxMemoBytes
}

// validateInterleaved applies defaults and fail-closed validation for interleaved thinking.
// When the feature is disabled, no field is inspected so existing behavior is preserved.
func validateInterleaved(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	i := &cfg.Interleaved
	if !i.Enabled {
		return nil
	}
	vis := strings.ToLower(strings.TrimSpace(i.StreamToClient))
	switch vis {
	case "":
		vis = DefaultInterleavedStreamToClient
	case "hidden", "visible":
	default:
		return fmt.Errorf("interleaved.stream_to_client: want hidden or visible, got %q", i.StreamToClient)
	}
	i.StreamToClient = vis

	if strings.Contains(i.InstructionsFile, "\x00") {
		return fmt.Errorf("interleaved.instructions_file: must not contain NUL")
	}
	if strings.TrimSpace(i.InstructionsFile) != "" {
		if _, err := resolveInterleavedInstructionsPath(cfg, i.InstructionsFile); err != nil {
			return err
		}
	}

	if i.RegularTurnsRemaining < 0 {
		return fmt.Errorf("interleaved.regular_turns_remaining: must be >= 0, got %d", i.RegularTurnsRemaining)
	}
	if i.RegularTurnsRemaining == 0 {
		i.RegularTurnsRemaining = DefaultInterleavedRegularTurns
	}

	if i.MaxMemoBytes < 0 {
		return fmt.Errorf("interleaved.max_memo_bytes: must be >= 0, got %d", i.MaxMemoBytes)
	}
	if i.MaxMemoBytes == 0 {
		i.MaxMemoBytes = DefaultInterleavedMaxMemoBytes
	}
	return nil
}

func resolveInterleavedInstructionsPath(cfg *Config, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("interleaved.instructions_file: empty path")
	}
	base := strings.TrimSpace(cfg.ConfigDir)
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("interleaved.instructions_file: getwd: %w", err)
		}
		base = wd
	}
	absBase, err := filepath.Abs(filepath.Clean(base))
	if err != nil {
		return "", fmt.Errorf("interleaved.instructions_file: resolve base: %w", err)
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absBase, candidate)
	}
	absPath, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("interleaved.instructions_file: resolve path: %w", err)
	}
	if err := ensurePathUnderBase(absBase, absPath, raw); err != nil {
		return "", err
	}
	if _, err := os.Stat(absPath); err != nil {
		if os.IsNotExist(err) {
			return absPath, nil
		}
		return "", fmt.Errorf("interleaved.instructions_file: stat: %w", err)
	}
	resolvedBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", fmt.Errorf("interleaved.instructions_file: resolve config directory: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("interleaved.instructions_file: resolve path: %w", err)
	}
	if err := ensurePathUnderBase(resolvedBase, resolvedPath, raw); err != nil {
		return "", err
	}
	return resolvedPath, nil
}

func ensurePathUnderBase(absBase, absPath, label string) error {
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return fmt.Errorf("interleaved.instructions_file: path outside config directory")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("interleaved.instructions_file: path %q escapes config directory", label)
	}
	return nil
}

func readInterleavedInstructionsFile(path string) ([]byte, error) {
	max := DefaultInterleavedMaxInstructionsBytes
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > int64(max) {
		return nil, fmt.Errorf("file exceeds %d bytes", max)
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("file exceeds %d bytes", max)
	}
	return b, nil
}
