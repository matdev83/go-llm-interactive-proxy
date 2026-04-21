package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// assistantMediaEventsFromContentBlockStart maps assistant image/document
// content_block_start payloads to canonical ref events. The streaming union
// may not expose typed image/document variants; we key off Type + RawJSON.
func assistantMediaEventsFromContentBlockStart(cb anthropic.ContentBlockStartEventContentBlockUnion) []lipapi.Event {
	switch cb.Type {
	case "image", "document":
	default:
		return nil
	}
	raw := cb.RawJSON()
	if raw == "" {
		return nil
	}
	var probe struct {
		Type   string          `json:"type"`
		Source json.RawMessage `json:"source"`
		Title  string          `json:"title"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil || probe.Source == nil {
		return nil
	}
	switch probe.Type {
	case "image":
		ref, mime := anthropicImageSourceRef(probe.Source)
		if ref == "" {
			return nil
		}
		return []lipapi.Event{{
			Kind:          lipapi.EventAssistantImageRef,
			AssistantRef:  ref,
			AssistantMIME: mime,
		}}
	case "document":
		ref, mime, name := anthropicDocumentSourceRef(probe.Source)
		if ref == "" {
			return nil
		}
		if probe.Title != "" {
			name = probe.Title
		}
		ev := lipapi.Event{
			Kind:          lipapi.EventAssistantFileRef,
			AssistantRef:  ref,
			AssistantMIME: mime,
		}
		if name != "" {
			ev.AssistantName = name
		}
		return []lipapi.Event{ev}
	default:
		return nil
	}
}

func anthropicImageSourceRef(src json.RawMessage) (ref, mime string) {
	var u struct {
		Type      string `json:"type"`
		URL       string `json:"url"`
		Data      string `json:"data"`
		MediaType string `json:"media_type"`
	}
	if err := json.Unmarshal(src, &u); err != nil {
		return "", ""
	}
	switch u.Type {
	case "url":
		ref = strings.TrimSpace(u.URL)
		if ref == "" {
			return "", ""
		}
		return ref, sniffImageMIME(ref)
	case "base64":
		if u.Data == "" {
			return "", ""
		}
		mt := u.MediaType
		if mt == "" {
			mt = "application/octet-stream"
		}
		return "data:" + mt + ";base64," + u.Data, mt
	default:
		return "", ""
	}
}

func anthropicDocumentSourceRef(src json.RawMessage) (ref, mime, title string) {
	var u struct {
		Type      string `json:"type"`
		URL       string `json:"url"`
		Data      string `json:"data"`
		MediaType string `json:"media_type"`
		Title     string `json:"title"`
	}
	if err := json.Unmarshal(src, &u); err != nil {
		return "", "", ""
	}
	switch u.Type {
	case "url":
		ref = strings.TrimSpace(u.URL)
		if ref == "" {
			return "", "", ""
		}
		mt := u.MediaType
		if mt == "" {
			mt = "application/octet-stream"
		}
		return ref, mt, u.Title
	case "base64":
		if u.Data == "" {
			return "", "", ""
		}
		mt := u.MediaType
		if mt == "" {
			mt = "application/pdf"
		}
		return "data:" + mt + ";base64," + u.Data, mt, u.Title
	default:
		return "", "", ""
	}
}

func sniffImageMIME(ref string) string {
	lower := strings.ToLower(ref)
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
