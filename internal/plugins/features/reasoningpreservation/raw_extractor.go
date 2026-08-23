package reasoningpreservation

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// HardRawOutputCeiling is the hard implementation byte limit distinct from configured max_output_bytes.
// It is a defense-in-depth cap applied before JSON decode regardless of operator config.
const HardRawOutputCeiling = 512 * 1024 // 512 KiB

var (
	ErrRawOversize       = errors.New(ID + ": raw_oversize")
	ErrRawInvalidChannel = errors.New(ID + ": raw_invalid_channel")
)

// ExtractBoundedRaw extracts raw bytes from a collected canonical response with strict bounding.
// It rejects tool calls / non-text channels first, then enforces max_output_bytes and the hard ceiling
// without invoking JSON decode on oversize and without constructing a string beyond the bound.
func ExtractBoundedRaw(collected lipapi.Collected, maxOutputBytes int) ([]byte, error) {
	// Reject non-text channels first per design §9.
	if len(collected.ToolArgs) > 0 || len(collected.ToolNames) > 0 || len(collected.ToolCallOrder) > 0 {
		return nil, fmt.Errorf("%w: tool calls present", ErrRawInvalidChannel)
	}
	if collected.Reasoning.Len() > 0 || len(collected.ReasoningParts) > 0 {
		return nil, fmt.Errorf("%w: reasoning channel present", ErrRawInvalidChannel)
	}
	if len(collected.AssistantMedia) > 0 {
		return nil, fmt.Errorf("%w: assistant media present", ErrRawInvalidChannel)
	}
	if len(collected.Warnings) > 0 {
		// warnings are not part of raw compressor schema; treat as invalid channel if present beyond text
		// For strictness we allow empty warnings to pass but non-empty is not expected for compressor.
		// We keep permissive: ignore warnings content, not reject.
	}
	// Byte limit checks before materializing beyond bound.
	n := collected.Text.Len()
	limit := maxOutputBytes
	if limit <= 0 {
		limit = HardRawOutputCeiling
	}
	// Enforce configured limit and hard ceiling both.
	if n > limit {
		return nil, fmt.Errorf("%w: collected %d > max_output_bytes %d", ErrRawOversize, n, limit)
	}
	if n > HardRawOutputCeiling {
		return nil, fmt.Errorf("%w: collected %d > hard ceiling %d", ErrRawOversize, n, HardRawOutputCeiling)
	}
	// Materialize bounded bytes only now.
	raw := []byte(collected.Text.String())
	if len(raw) > limit {
		return nil, fmt.Errorf("%w: raw %d > max_output_bytes %d", ErrRawOversize, len(raw), limit)
	}
	if len(raw) > HardRawOutputCeiling {
		return nil, fmt.Errorf("%w: raw %d > hard ceiling %d", ErrRawOversize, len(raw), HardRawOutputCeiling)
	}
	return raw, nil
}
