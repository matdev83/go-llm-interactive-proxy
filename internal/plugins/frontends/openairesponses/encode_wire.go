package openairesponses

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type wireResponse struct {
	ID        string     `json:"id"`
	Object    string     `json:"object"`
	CreatedAt int64      `json:"created_at"`
	Status    string     `json:"status"`
	Model     string     `json:"model"`
	Output    []any      `json:"output"`
	Usage     *wireUsage `json:"usage,omitempty"`
}

type wireUsage struct {
	InputTokens         int                      `json:"input_tokens"`
	OutputTokens        int                      `json:"output_tokens"`
	TotalTokens         int                      `json:"total_tokens,omitempty"`
	InputTokensDetails  *wireInputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *wireOutputTokensDetails `json:"output_tokens_details,omitempty"`
	CostNanoUnits       int64                    `json:"x_lip_cost_nano_units,omitempty"`
	Currency            string                   `json:"x_lip_currency,omitempty"`
	CostSource          string                   `json:"x_lip_cost_source,omitempty"`
}

type wireInputTokensDetails struct {
	CachedTokens   int `json:"cached_tokens,omitempty"`
	UncachedTokens int `json:"x_lip_uncached_tokens,omitempty"`
	CacheWrite     int `json:"x_lip_cache_write_tokens,omitempty"`
}

type wireOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

func wireResponsesUsage(col lipapi.Collected, exposeExt bool) *wireUsage {
	if col.InputTokens == 0 && col.OutputTokens == 0 && col.CacheReadTokens == 0 && col.CacheWriteTokens == 0 && col.ReasoningTokens == 0 && col.TotalTokens == 0 && col.CostNanoUnits == 0 {
		return nil
	}
	u := &wireUsage{
		InputTokens:  col.InputTokens,
		OutputTokens: col.OutputTokens,
		TotalTokens:  col.TotalOrDerived(),
	}
	if col.CacheReadTokens > 0 || col.CacheWriteTokens > 0 {
		u.InputTokensDetails = &wireInputTokensDetails{CachedTokens: col.CacheReadTokens}
		if exposeExt {
			u.InputTokensDetails.UncachedTokens = col.UncachedInputTokens()
			u.InputTokensDetails.CacheWrite = col.CacheWriteTokens
		}
	}
	if col.ReasoningTokens > 0 {
		u.OutputTokensDetails = &wireOutputTokensDetails{ReasoningTokens: col.ReasoningTokens}
	}
	if exposeExt {
		u.CostNanoUnits = col.CostNanoUnits
		u.Currency = col.Currency
		u.CostSource = col.CostSource
	}
	return u
}

func fcItemID(callID string) string {
	return "fc_" + strings.ReplaceAll(callID, ":", "_")
}

// wireMessageContentParts builds Responses API message.content items (output_text plus optional input_image / input_file refs).
func wireMessageContentParts(text string, media []lipapi.Part) []any {
	out := []any{map[string]any{"type": "output_text", "text": text}}
	for _, p := range media {
		switch p.Kind {
		case lipapi.PartImageRef:
			out = append(out, map[string]any{"type": "input_image", "image_url": p.ImageRef})
		case lipapi.PartFileRef:
			m := map[string]any{"type": "input_file", "file_id": p.FileRef}
			if p.FileName != "" {
				m["filename"] = p.FileName
			}
			out = append(out, m)
		}
	}
	return out
}

type wireStreamEnvelope struct {
	Type           string       `json:"type"`
	SequenceNumber int          `json:"sequence_number"`
	Response       wireResponse `json:"response"`
}
