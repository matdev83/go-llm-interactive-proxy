package codexclientcompat

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func openCodeAgentMatch(in compatInput) bool {
	for _, candidate := range in.agents {
		if strings.Contains(strings.ToLower(candidate), "opencode") {
			return true
		}
	}
	return false
}

func openCodePromptMatch(in compatInput) bool {
	lower := strings.ToLower(in.prompt)
	if strings.Contains(lower, "opencode") {
		if strings.Contains(lower, "compatibility") || strings.Contains(lower, "harness") || strings.Contains(lower, "tool") {
			return true
		}
	}
	return false
}

func isOpenCodeHarnessText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "opencode") && strings.Contains(lower, "tool")
}

func applyOpenCodeToolHistoryCompat(call *lipapi.Call) {
	convertOrphanedToolResults(call)
}

func hasStructuredToolTranscript(msgs []lipapi.Message) bool {
	for _, m := range msgs {
		if m.Role == lipapi.RoleTool {
			for _, p := range m.Parts {
				if p.Kind == lipapi.PartToolResult {
					return true
				}
			}
		}
		if m.Role != lipapi.RoleAssistant {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartJSON {
				continue
			}
			if isFunctionCallPart(p) {
				return true
			}
		}
	}
	return false
}

func isFunctionCallPart(p lipapi.Part) bool {
	if len(p.Content) == 0 {
		return false
	}
	var fc struct {
		Type     string `json:"type"`
		CallID   string `json:"call_id"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(p.Content, &fc) != nil {
		return false
	}
	if !strings.EqualFold(fc.Type, "function_call") && !strings.EqualFold(fc.Type, "function") {
		return false
	}
	id := firstNonEmpty(fc.CallID, fc.ID)
	name := strings.TrimSpace(fc.Name)
	if name == "" && fc.Function != nil {
		name = strings.TrimSpace(fc.Function.Name)
	}
	return strings.TrimSpace(id) != "" && name != ""
}

func convertOrphanedToolResults(call *lipapi.Call) {
	known := collectKnownToolCallIDs(call.Messages)
	out := make([]lipapi.Message, 0, len(call.Messages))
	for _, m := range call.Messages {
		if m.Role != lipapi.RoleTool {
			out = append(out, m)
			continue
		}
		kept := make([]lipapi.Part, 0, len(m.Parts))
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartToolResult {
				kept = append(kept, p)
				continue
			}
			callID := strings.TrimSpace(p.ToolCallID)
			if callID != "" {
				if _, ok := known[callID]; ok {
					kept = append(kept, p)
					continue
				}
			}
			out = append(out, convertOrphanedToolResult(p))
		}
		if len(kept) > 0 {
			out = append(out, lipapi.Message{Role: lipapi.RoleTool, Parts: kept})
		}
	}
	call.Messages = out
}

func collectKnownToolCallIDs(msgs []lipapi.Message) map[string]struct{} {
	known := make(map[string]struct{})
	for _, m := range msgs {
		if m.Role != lipapi.RoleAssistant {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartJSON {
				continue
			}
			var fc struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				ID     string `json:"id"`
			}
			if json.Unmarshal(p.Content, &fc) != nil {
				continue
			}
			// Accept Responses-style ("function_call") and Chat Completions-style
			// ("function") assistant tool calls so matching tool results are preserved.
			if !strings.EqualFold(fc.Type, "function_call") && !strings.EqualFold(fc.Type, "function") {
				continue
			}
			id := strings.TrimSpace(fc.CallID)
			if id == "" {
				id = strings.TrimSpace(fc.ID)
			}
			if id != "" {
				known[id] = struct{}{}
			}
		}
	}
	return known
}

func convertOrphanedToolResult(p lipapi.Part) lipapi.Message {
	rendered := string(p.Content)
	if len(p.Content) == 0 {
		rendered = ""
	}
	header := "Prior tool output (original tool call reference unavailable)."
	if id := strings.TrimSpace(p.ToolCallID); id != "" {
		header += " call_id=" + id + "."
	}
	return lipapi.Message{
		Role:  lipapi.RoleSystem,
		Parts: []lipapi.Part{lipapi.TextPart(header + "\n" + rendered)},
	}
}

func buildOpenCodeBridge(hasTools bool) string {
	var b strings.Builder
	b.WriteString(openCodeBridgeMarker)
	b.WriteString(":\n")
	if hasTools {
		// Keep this guidance generic. OpenCode tool names and schemas vary by
		// installation, plugin, and session; the structured tool list is the only
		// authoritative source of callable names. Duplicating names in prose makes
		// random session-specific tools look universal and can bias the model toward
		// tools the current request did not actually expose.
		b.WriteString("- Prefer the available client shell tool when command execution is needed.\n")
	} else {
		b.WriteString("- No callable client tools are available in this request. Do not attempt tool calls; respond in plain text or ask the user/client to provide tools.\n")
	}
	b.WriteString("- Never emit textual tool-call syntax such as `to=functions.<name>` or JSON tool calls in assistant content; use structured tool calls only when tools are available.\n")
	if !hasTools {
		// No tools are exposed, so do not append criticalInstruction("OpenCode"):
		// it tells the model to use agent-provided tools, contradicting the
		// "no callable client tools" guidance above and risking spurious tool calls.
		return b.String()
	}
	b.WriteString(
		"- For bash-style tools, arguments MUST be a JSON object with string " +
			"`command` and string `description`.\n" +
			"- Bash-style tools MAY include numeric `timeout` in milliseconds " +
			"and string `workdir` when the client schema exposes them.\n" +
			"- Never emit array-valued `command` arguments for shell execution.\n" +
			"- Do not use `apply_patch`; use the client's native file editing tools instead.\n" +
			"- Do not use `update_plan` or `read_plan`; use the client's task tools instead.\n" +
			"- If you need a working directory, prefer `workdir` over `cd` commands " +
			"or embedding cwd text in `description`.\n" +
			"\n" +
			criticalInstruction("OpenCode"),
	)
	return b.String()
}
