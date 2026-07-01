package openaicodex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type inputItem interface {
	inputItem()
}

type textMessageItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (textMessageItem) inputItem() {}

type richMessageItem struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

func (richMessageItem) inputItem() {}

type functionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func (functionCallOutputItem) inputItem() {}

type functionCallItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (functionCallItem) inputItem() {}

type contentBlock interface {
	contentBlock()
}

type inputTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (inputTextPart) contentBlock() {}

type inputImagePart struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}

func (inputImagePart) contentBlock() {}

type inputFilePart struct {
	Type     string `json:"type"`
	FileData string `json:"file_data"`
	Filename string `json:"filename"`
}

func (inputFilePart) contentBlock() {}

// resolveCodexInstructions builds the Codex `instructions` field. The Codex Responses API
// rejects system-role items in `input`, so system messages from the conversation are folded
// into instructions (deduplicated against explicit instructions, e.g. the codex-client-compat
// bridge). Falls back to the default Codex instruction only when no system content is present.
func resolveCodexInstructions(call *lipapi.Call) string {
	instructions := joinInstructionText(call.Instructions)
	for _, sysText := range systemMessageTexts(call.Messages) {
		if sysText == "" || instructionHasBlock(instructions, sysText) {
			continue
		}
		if instructions != "" {
			instructions += "\n\n" + sysText
		} else {
			instructions = sysText
		}
	}
	if strings.TrimSpace(instructions) == "" {
		return defaultCodexInstruction
	}
	return instructions
}

// instructionHasBlock reports whether instructions already contains block as a complete
// \n\n-delimited block. Substring containment is intentionally NOT used: a short system
// message that happens to be a substring of a longer instruction block (e.g. "Be concise."
// within "Be concise and helpful.") must still be merged rather than silently dropped. Only
// an exact full-block match (e.g. the codex-client-compat bridge re-sent verbatim) is a dup.
func instructionHasBlock(instructions, block string) bool {
	if block == "" {
		return false
	}
	for part := range strings.SplitSeq(instructions, "\n\n") {
		if part == block {
			return true
		}
	}
	return false
}

func systemMessageTexts(msgs []lipapi.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != lipapi.RoleSystem {
			continue
		}
		var b strings.Builder
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartText || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	return out
}

func joinInstructionText(insts []lipapi.Message) string {
	var b strings.Builder
	for _, m := range insts {
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartText {
				continue
			}
			if strings.TrimSpace(p.Text) == "" {
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

func buildInputItems(call *lipapi.Call) ([]inputItem, error) {
	out := make([]inputItem, 0, len(call.Messages))
	hasTools := len(call.Tools) > 0
	for _, m := range call.Messages {
		if m.Role == lipapi.RoleSystem {
			continue
		}
		if m.Role == lipapi.RoleTool {
			for _, p := range m.Parts {
				if p.Kind != lipapi.PartToolResult {
					return nil, fmt.Errorf("%s: unsupported tool part kind %q", ID, p.Kind)
				}
				if !hasTools {
					// No-tools requests are a real client state, not malformed history:
					// OpenCode can resend prior tool transcripts while asking the model
					// to continue without exposing callable tools. Codex must see those
					// records as plain conversation text, because a function_call_output
					// without matching tools can stall the turn or make the model emit raw
					// tool protocol back to the user.
					out = append(out, textMessageItem{
						Type:    "message",
						Role:    "user",
						Content: noToolsToolResultText(p),
					})
					continue
				}
				out = append(out, functionCallOutputItem{
					Type:   "function_call_output",
					CallID: p.ToolCallID,
					Output: toolResultString(p),
				})
			}
			continue
		}
		if m.Role == lipapi.RoleAssistant && len(m.Parts) > 0 {
			items, ok, err := assistantFunctionCallItems(m.Parts, hasTools)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, items...)
				continue
			}
		}
		item, err := messageToInputItem(m)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func assistantFunctionCallItems(parts []lipapi.Part, hasTools bool) ([]inputItem, bool, error) {
	out := make([]inputItem, 0, len(parts))
	contentParts := make([]lipapi.Part, 0, len(parts))
	sawFunctionCall := false
	flushContent := func() error {
		if len(contentParts) == 0 {
			return nil
		}
		item, err := messageToInputItem(lipapi.Message{Role: lipapi.RoleAssistant, Parts: contentParts})
		if err != nil {
			return err
		}
		out = append(out, item)
		contentParts = contentParts[:0]
		return nil
	}
	for _, p := range parts {
		item, ok, err := partToFunctionCallItem(p)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			contentParts = append(contentParts, p)
			continue
		}
		if !hasTools {
			sawFunctionCall = true
			// Preserve the fact that a prior assistant action happened, but do not
			// send Codex a structured function_call when this request has no tool
			// schema. The structured form is reserved for tool-enabled turns where
			// the backend can safely continue the protocol.
			contentParts = append(contentParts, lipapi.TextPart(noToolsFunctionCallText(item)))
			continue
		}
		if err := flushContent(); err != nil {
			return nil, false, err
		}
		sawFunctionCall = true
		out = append(out, item)
	}
	if !sawFunctionCall {
		return nil, false, nil
	}
	if err := flushContent(); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func noToolsFunctionCallText(item inputItem) string {
	fc, ok := item.(functionCallItem)
	if !ok {
		return "Prior assistant tool call (tools unavailable in this request)."
	}
	var b strings.Builder
	b.WriteString("Prior assistant tool call (tools unavailable in this request).")
	if strings.TrimSpace(fc.CallID) != "" {
		b.WriteString(" call_id=")
		b.WriteString(strings.TrimSpace(fc.CallID))
		b.WriteByte('.')
	}
	if strings.TrimSpace(fc.Name) != "" {
		b.WriteString(" name=")
		b.WriteString(strings.TrimSpace(fc.Name))
		b.WriteByte('.')
	}
	if strings.TrimSpace(fc.Arguments) != "" {
		b.WriteString(" arguments=")
		b.WriteString(strings.TrimSpace(fc.Arguments))
	}
	return b.String()
}

func noToolsToolResultText(p lipapi.Part) string {
	var b strings.Builder
	b.WriteString("Prior tool output (tools unavailable in this request).")
	if id := strings.TrimSpace(p.ToolCallID); id != "" {
		b.WriteString(" call_id=")
		b.WriteString(id)
		b.WriteByte('.')
	}
	if len(p.Content) > 0 {
		b.WriteByte('\n')
		b.WriteString(toolResultDisplayText(p.Content))
	}
	return b.String()
}

func toolResultDisplayText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

func partToFunctionCallItem(p lipapi.Part) (inputItem, bool, error) {
	if p.Kind != lipapi.PartJSON || len(p.Content) == 0 {
		return nil, false, nil
	}
	var v struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Function  struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(p.Content, &v); err != nil {
		return nil, false, nil
	}
	t := strings.TrimSpace(v.Type)
	// Accept Responses-style ("function_call" or empty) and Chat Completions-style
	// ("function") assistant tool calls. Any other concrete type is not a function call.
	if t != "" && t != "function_call" && t != "function" {
		return nil, false, nil
	}
	// Chat Completions carries the call id as "id" and the name/arguments under
	// "function"; Responses carries them at the top level as "call_id"/"name".
	callID := strings.TrimSpace(v.CallID)
	if callID == "" {
		callID = strings.TrimSpace(v.ID)
	}
	name := strings.TrimSpace(v.Name)
	args := v.Arguments
	if name == "" {
		name = strings.TrimSpace(v.Function.Name)
		args = v.Function.Arguments
	}
	if callID == "" || name == "" {
		return nil, false, fmt.Errorf("%s: function_call requires call_id and name", ID)
	}
	argStr := "{}"
	if jsonpresence.IsPresentNonNullJSON(args) {
		switch args[0] {
		case '"':
			var s string
			if err := json.Unmarshal(args, &s); err != nil {
				return nil, false, fmt.Errorf("%s: function_call arguments: %w", ID, err)
			}
			argStr = s
		default:
			argStr = string(args)
		}
	}
	item := functionCallItem{
		Type:      "function_call",
		CallID:    callID,
		Name:      name,
		Arguments: argStr,
	}
	// Preserve the Responses-style item id only when it is distinct from the call_id
	// (i.e. a separate call_id was supplied). For Chat Completions the id IS the call
	// id, so do not duplicate it as the item id.
	if strings.TrimSpace(v.ID) != "" && strings.TrimSpace(v.CallID) != "" {
		item.ID = strings.TrimSpace(v.ID)
	}
	return item, true, nil
}

func toolResultString(p lipapi.Part) string {
	output := ""
	if len(p.Content) > 0 {
		output = string(p.Content)
	}
	outputRaw, err := json.Marshal(output)
	if err != nil {
		return output
	}
	payload := map[string]json.RawMessage{"output": outputRaw}
	var existing map[string]json.RawMessage
	if json.Unmarshal(p.Content, &existing) == nil {
		if raw, ok := existing["exit_code"]; ok {
			payload["exit_code"] = raw
		}
		if raw, ok := existing["workdir"]; ok {
			payload["workdir"] = raw
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return output
	}
	return string(raw)
}

func messageToInputItem(m lipapi.Message) (inputItem, error) {
	role := roleString(m.Role)
	if len(m.Parts) == 1 && m.Parts[0].Kind == lipapi.PartText {
		return textMessageItem{
			Type:    "message",
			Role:    role,
			Content: m.Parts[0].Text,
		}, nil
	}
	content, err := partsToContentList(m.Parts)
	if err != nil {
		return nil, err
	}
	return richMessageItem{
		Type:    "message",
		Role:    role,
		Content: content,
	}, nil
}

func roleString(r lipapi.Role) string {
	switch r {
	case lipapi.RoleUser:
		return "user"
	case lipapi.RoleAssistant:
		return "assistant"
	case lipapi.RoleSystem:
		return "system"
	default:
		return "user"
	}
}

func partsToContentList(parts []lipapi.Part) ([]contentBlock, error) {
	out := make([]contentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, inputTextPart{Type: "input_text", Text: p.Text})
		case lipapi.PartImageRef:
			out = append(out, inputImagePart{
				Type:     "input_image",
				ImageURL: p.ImageRef,
			})
		case lipapi.PartFileRef:
			b64, fname, err := fileDataFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, inputFilePart{
				Type:     "input_file",
				FileData: b64,
				Filename: fname,
			})
		default:
			return nil, fmt.Errorf("%s: unsupported part kind %q", ID, p.Kind)
		}
	}
	return out, nil
}

func fileDataFromPart(p lipapi.Part) (dataB64, filename string, err error) {
	filename = strings.TrimSpace(p.FileName)
	ref := p.FileRef
	if strings.HasPrefix(ref, "data:") {
		_, b64, ok := stripDataURLBase64(ref)
		if !ok {
			return "", "", fmt.Errorf("%s: invalid data URL in file part", ID)
		}
		return b64, filename, nil
	}
	return "", "", fmt.Errorf("%s: file part requires a data URL", ID)
}

func stripDataURLBase64(dataURL string) (mime, b64 string, ok bool) {
	rest, ok := strings.CutPrefix(dataURL, "data:")
	if !ok {
		return "", "", false
	}
	mime, enc, found := strings.Cut(rest, ";")
	if !found {
		return "", "", false
	}
	const prefix = "base64,"
	encBody, ok := strings.CutPrefix(enc, prefix)
	if !ok {
		return "", "", false
	}
	return mime, encBody, true
}
