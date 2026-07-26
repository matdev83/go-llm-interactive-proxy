package cursorsdk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var (
	ErrUnsupportedPrompt = errors.New("cursor_sdk_capability_unsupported")
	ErrPromptTooLarge    = errors.New("cursor_sdk_prompt_too_large")
	ErrPromptInvalid     = errors.New("cursor_sdk_prompt_invalid")
)

type EncodedPrompt struct {
	FullPrompt   string
	SuffixPrompt string
	View         TranscriptView
}

type promptLine struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type limitedBuffer struct {
	max int
	buf strings.Builder
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max >= 0 && b.buf.Len()+len(p) > b.max {
		return 0, ErrPromptTooLarge
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) WriteString(s string) (int, error) {
	if b.max >= 0 && b.buf.Len()+len(s) > b.max {
		return 0, ErrPromptTooLarge
	}
	return b.buf.WriteString(s)
}

func (b *limitedBuffer) WriteByte(c byte) error {
	if b.max >= 0 && b.buf.Len()+1 > b.max {
		return ErrPromptTooLarge
	}
	return b.buf.WriteByte(c)
}

func (b *limitedBuffer) String() string { return b.buf.String() }
func (b *limitedBuffer) Len() int       { return b.buf.Len() }

func EncodePrompt(call *lipapi.Call, headCount int) (EncodedPrompt, error) {
	if call == nil {
		return EncodedPrompt{}, fmt.Errorf("%w: nil call", ErrPromptInvalid)
	}
	if err := validatePromptCall(call); err != nil {
		return EncodedPrompt{}, err
	}
	if headCount < 0 || headCount > len(call.Messages) {
		return EncodedPrompt{}, fmt.Errorf("%w: headCount %d out of range", ErrPromptInvalid, headCount)
	}

	full, err := encodeTranscript(call.Instructions, call.Messages, MaxPromptBytes)
	if err != nil {
		return EncodedPrompt{}, err
	}

	var suffix string
	switch {
	case headCount == len(call.Messages):
		suffix = extractLastUserText(call.Messages)
		if strings.TrimSpace(suffix) == "" {
			return EncodedPrompt{}, fmt.Errorf("%w: retry suffix empty", ErrEmptyPrompt)
		}
		if len(suffix) > MaxPromptBytes {
			return EncodedPrompt{}, fmt.Errorf("%w: retry suffix %d bytes", ErrPromptTooLarge, len(suffix))
		}
	case headCount == 0:
		suffix = full
	default:
		suffix, err = encodeTranscript(nil, call.Messages[headCount:], MaxPromptBytes)
		if err != nil {
			return EncodedPrompt{}, err
		}
		if strings.TrimSpace(suffix) == "" {
			return EncodedPrompt{}, fmt.Errorf("%w: incremental suffix empty", ErrEmptyPrompt)
		}
	}

	view := TranscriptView{
		MessageCount: len(call.Messages),
		PrefixHash:   hashTranscriptPrefix(call.Instructions, call.Messages, len(call.Messages)),
		LastTurnID:   lastTurnID(call.Messages),
	}
	if headCount > 0 {
		view.HeadPrefixHash = hashTranscriptPrefix(call.Instructions, call.Messages, headCount)
	}

	return EncodedPrompt{FullPrompt: full, SuffixPrompt: suffix, View: view}, nil
}

func validatePromptCall(call *lipapi.Call) error {
	if len(call.Messages) == 0 {
		return fmt.Errorf("%w: messages required", ErrPromptInvalid)
	}
	if len(call.Tools) > 0 {
		return fmt.Errorf("%w: client tools", ErrUnsupportedPrompt)
	}
	if err := validatePromptToolChoice(call.ToolChoice); err != nil {
		return err
	}
	if call.Options.ParallelToolCalls != nil && *call.Options.ParallelToolCalls {
		return fmt.Errorf("%w: parallel tool calls", ErrUnsupportedPrompt)
	}
	if strings.TrimSpace(call.Options.ResponseMIMEType) != "" {
		return fmt.Errorf("%w: structured output", ErrUnsupportedPrompt)
	}
	for i, m := range call.Instructions {
		if err := validatePromptMessage(m, fmt.Sprintf("Instructions[%d]", i), true); err != nil {
			return err
		}
	}
	for i, m := range call.Messages {
		if err := validatePromptMessage(m, fmt.Sprintf("Messages[%d]", i), false); err != nil {
			return err
		}
	}
	return nil
}

func validatePromptToolChoice(tc lipapi.ToolChoice) error {
	switch tc.Mode {
	case "", lipapi.ToolChoiceAuto, lipapi.ToolChoiceNone:
		if tc.Name != "" {
			return fmt.Errorf("%w: tool choice name", ErrUnsupportedPrompt)
		}
		return nil
	case lipapi.ToolChoiceAny, lipapi.ToolChoiceRequired:
		return fmt.Errorf("%w: tool choice %s", ErrUnsupportedPrompt, tc.Mode)
	default:
		return fmt.Errorf("%w: tool choice mode %q", ErrUnsupportedPrompt, tc.Mode)
	}
}

func validatePromptMessage(m lipapi.Message, field string, instruction bool) error {
	switch m.Role {
	case lipapi.RoleSystem, lipapi.RoleUser, lipapi.RoleAssistant:
	case lipapi.RoleTool:
		return fmt.Errorf("%w: %s tool role", ErrUnsupportedPrompt, field)
	case "":
		return fmt.Errorf("%w: %s role required", ErrPromptInvalid, field)
	default:
		return fmt.Errorf("%w: %s role %q", ErrUnsupportedPrompt, field, m.Role)
	}
	if instruction && m.Role != lipapi.RoleSystem && m.Role != lipapi.RoleUser {
		return fmt.Errorf("%w: %s instruction role %q", ErrUnsupportedPrompt, field, m.Role)
	}
	if len(m.Parts) == 0 {
		return fmt.Errorf("%w: %s parts required", ErrPromptInvalid, field)
	}
	for j, p := range m.Parts {
		switch p.Kind {
		case lipapi.PartText:
			if p.Text == "" {
				return fmt.Errorf("%w: %s.Parts[%d] empty text", ErrPromptInvalid, field, j)
			}
		case lipapi.PartImageRef:
			return fmt.Errorf("%w: %s.Parts[%d] image", ErrUnsupportedPrompt, field, j)
		case lipapi.PartFileRef:
			return fmt.Errorf("%w: %s.Parts[%d] file", ErrUnsupportedPrompt, field, j)
		case lipapi.PartToolResult:
			return fmt.Errorf("%w: %s.Parts[%d] tool_result", ErrUnsupportedPrompt, field, j)
		case lipapi.PartJSON:
			return fmt.Errorf("%w: %s.Parts[%d] json", ErrUnsupportedPrompt, field, j)
		default:
			return fmt.Errorf("%w: %s.Parts[%d] kind %q", ErrUnsupportedPrompt, field, j, p.Kind)
		}
	}
	return nil
}

func encodeTranscript(instructions, messages []lipapi.Message, maxBytes int) (string, error) {
	var b limitedBuffer
	b.max = maxBytes
	for _, m := range instructions {
		if err := writePromptLine(&b, m); err != nil {
			return "", err
		}
	}
	for _, m := range messages {
		if err := writePromptLine(&b, m); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func writePromptLine(b *limitedBuffer, m lipapi.Message) error {
	var body strings.Builder
	for i, p := range m.Parts {
		if p.Kind != lipapi.PartText {
			return fmt.Errorf("%w: non-text part", ErrUnsupportedPrompt)
		}
		if i > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(p.Text)
	}
	raw, err := json.Marshal(promptLine{Role: string(m.Role), Text: body.String()})
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrPromptInvalid, err)
	}
	if _, err := b.Write(raw); err != nil {
		return err
	}
	if err := b.WriteByte('\n'); err != nil {
		return err
	}
	return nil
}

func extractLastUserText(messages []lipapi.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != lipapi.RoleUser {
			continue
		}
		var parts []string
		for _, p := range messages[i].Parts {
			if p.Kind == lipapi.PartText {
				parts = append(parts, p.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func hashTranscriptPrefix(instructions, messages []lipapi.Message, messageEndExclusive int) string {
	if messageEndExclusive < 0 {
		messageEndExclusive = 0
	}
	if messageEndExclusive > len(messages) {
		messageEndExclusive = len(messages)
	}
	h := sha256.New()
	_, _ = h.Write([]byte("instructions\n"))
	for _, m := range instructions {
		writeMessageHash(h, m)
	}
	_, _ = h.Write([]byte("messages\n"))
	for i := range messageEndExclusive {
		writeMessageHash(h, messages[i])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeMessageHash(h interface{ Write([]byte) (int, error) }, m lipapi.Message) {
	_, _ = fmt.Fprintf(h, "%s|", m.Role)
	for _, p := range m.Parts {
		if p.Kind == lipapi.PartText {
			_, _ = fmt.Fprintf(h, "text:%s|", p.Text)
		}
	}
	_, _ = h.Write([]byte{'\n'})
}

func lastTurnID(messages []lipapi.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != lipapi.RoleUser {
			continue
		}
		h := hashTranscriptPrefix(nil, messages[i:i+1], 1)
		if len(h) < 16 {
			return fmt.Sprintf("%d:%s", i, h)
		}
		return fmt.Sprintf("%d:%s", i, h[:16])
	}
	if len(messages) == 0 {
		return ""
	}
	i := len(messages) - 1
	h := hashTranscriptPrefix(nil, messages[i:i+1], 1)
	if len(h) < 16 {
		return fmt.Sprintf("%d:%s", i, h)
	}
	return fmt.Sprintf("%d:%s", i, h[:16])
}
