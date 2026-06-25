package openairesponses

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/responses"
)

// emitOutputMediaFromResponse maps assistant message content items (input_image,
// input_file) from a completed Responses payload into canonical assistant ref events.
// The OpenAI wire uses the same content types as request items; the Go SDK union may
// not type every variant, so we parse RawJSON per content element.
func emitOutputMediaFromResponse(s *sdkStream, resp responses.Response) error {
	m := s.eventMapper()
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		msg := item.AsMessage()
		for _, c := range msg.Content {
			raw := c.RawJSON()
			if raw == "" {
				continue
			}
			var probe struct {
				Type     string          `json:"type"`
				ImageURL json.RawMessage `json:"image_url"`
				FileID   string          `json:"file_id"`
			}
			if err := json.Unmarshal([]byte(raw), &probe); err != nil {
				continue
			}
			switch probe.Type {
			case "input_image":
				url := extractImageURL(probe.ImageURL)
				if url == "" {
					continue
				}
				if err := m.EnsureResponseStarted(); err != nil {
					return err
				}
				if err := m.EnsureMessageStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{
					Kind:          lipapi.EventAssistantImageRef,
					AssistantRef:  url,
					AssistantMIME: sniffImageMIME(url),
				}); err != nil {
					return err
				}
			case "input_file":
				if strings.TrimSpace(probe.FileID) == "" {
					continue
				}
				if err := m.EnsureResponseStarted(); err != nil {
					return err
				}
				if err := m.EnsureMessageStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{
					Kind:          lipapi.EventAssistantFileRef,
					AssistantRef:  probe.FileID,
					AssistantMIME: "application/octet-stream",
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func extractImageURL(raw json.RawMessage) string {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.URL != "" {
		return obj.URL
	}
	return ""
}

func sniffImageMIME(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.Contains(lower, ".png"), strings.Contains(lower, "image/png"):
		return "image/png"
	case strings.Contains(lower, ".jpg"), strings.Contains(lower, ".jpeg"), strings.Contains(lower, "image/jpeg"):
		return "image/jpeg"
	case strings.Contains(lower, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}
