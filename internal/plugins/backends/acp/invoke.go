package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func sessionIDFromExtensions(call *lipapi.Call) string {
	if call == nil || call.Extensions == nil {
		return ""
	}
	raw, ok := call.Extensions[extSessionJSONKey]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func validateACPCall(call *lipapi.Call) error {
	if call == nil {
		return fmt.Errorf("acp: nil call")
	}
	if len(call.Tools) > 0 {
		return fmt.Errorf("acp: tools are not supported in the v1 prompt-turn subset")
	}
	return nil
}

// promptBlocksForCall maps the canonical call to ACP prompt content blocks (prompt-turn subset).
func promptBlocksForCall(call *lipapi.Call) ([]map[string]any, error) {
	if call == nil {
		return nil, fmt.Errorf("acp: nil call")
	}
	var blocks []map[string]any
	if t := joinInstructionText(call.Instructions); t != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": t})
	}
	for _, m := range call.Messages {
		switch m.Role {
		case lipapi.RoleSystem:
			for _, p := range m.Parts {
				if p.Kind != lipapi.PartText || strings.TrimSpace(p.Text) == "" {
					continue
				}
				blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
			}
		case lipapi.RoleUser:
			bs, err := userPartsToPromptBlocks(m.Parts)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, bs...)
		case lipapi.RoleAssistant:
			bs, err := assistantPartsToPromptBlocks(m.Parts)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, bs...)
		case lipapi.RoleTool:
			return nil, fmt.Errorf("acp: tool result messages are not supported in the v1 subset")
		default:
			return nil, fmt.Errorf("acp: unsupported message role %q", m.Role)
		}
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("acp: empty prompt after mapping")
	}
	return blocks, nil
}

func joinInstructionText(insts []lipapi.Message) string {
	var b strings.Builder
	for _, m := range insts {
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartText || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func userPartsToPromptBlocks(parts []lipapi.Part) ([]map[string]any, error) {
	var out []map[string]any
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case lipapi.PartImageRef:
			res := map[string]any{"uri": p.ImageRef}
			if p.ImageMIME != "" {
				res["mimeType"] = p.ImageMIME
			}
			out = append(out, map[string]any{"type": "resource", "resource": res})
		case lipapi.PartFileRef:
			res := map[string]any{"uri": p.FileRef}
			if p.FileMIME != "" {
				res["mimeType"] = p.FileMIME
			}
			if p.FileName != "" {
				res["text"] = p.FileName
			}
			out = append(out, map[string]any{"type": "resource", "resource": res})
		case lipapi.PartJSON, lipapi.PartToolResult:
			return nil, fmt.Errorf("acp: unsupported user part kind %q in v1 subset", p.Kind)
		default:
			return nil, fmt.Errorf("acp: unsupported user part kind %q", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("acp: user message has no mappable parts")
	}
	return out, nil
}

func assistantPartsToPromptBlocks(parts []lipapi.Part) ([]map[string]any, error) {
	var out []map[string]any
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case lipapi.PartJSON:
			return nil, fmt.Errorf("acp: assistant json parts are not supported in the v1 subset")
		default:
			return nil, fmt.Errorf("acp: unsupported assistant part kind %q", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("acp: assistant message has no mappable text parts")
	}
	return out, nil
}
