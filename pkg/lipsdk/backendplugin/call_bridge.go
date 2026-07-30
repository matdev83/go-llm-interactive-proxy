package backendplugin

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// CallFromInvocation maps a public plugin Invocation into a canonical lipapi.Call
// for connector-local protocol engines (ACP support, OpenAI-compat mappers, etc.).
func CallFromInvocation(inv Invocation) (lipapi.Call, error) {
	if err := inv.Validate(); err != nil {
		return lipapi.Call{}, err
	}
	instructions, err := messagesToLipapi(inv.Instructions)
	if err != nil {
		return lipapi.Call{}, err
	}
	messages, err := messagesToLipapi(inv.Messages)
	if err != nil {
		return lipapi.Call{}, err
	}
	call := lipapi.Call{
		ID:           strings.TrimSpace(inv.RequestID),
		Instructions: instructions,
		Messages:     messages,
		Tools:        toolsToLipapi(inv.Tools),
		Options:      optionsToLipapi(inv.Options),
		Route: lipapi.RouteIntent{
			Selector: strings.TrimSpace(inv.CanonicalModelID),
		},
		Session: lipapi.SessionRef{
			ALegID: strings.TrimSpace(inv.ALegID),
		},
	}
	RestoreCallWireMetadata(&call, inv.SafeMetadata)
	return call, nil
}

// CanonicalEventFromLipapi maps a canonical stream event into the plugin wire DTO.
func CanonicalEventFromLipapi(ev lipapi.Event) *CanonicalEvent {
	out := &CanonicalEvent{Kind: ev.Kind}
	if ev.MessageIndex != 0 {
		v := int32(ev.MessageIndex)
		out.MessageIndex = &v
	}
	if ev.Delta != "" {
		d := ev.Delta
		out.Delta = &d
	}
	if ev.Signature != "" {
		s := ev.Signature
		out.Signature = &s
	}
	if len(ev.Opaque) > 0 {
		out.Opaque = append([]byte(nil), ev.Opaque...)
	}
	if ev.Reasoning != nil {
		dialect := string(ev.Reasoning.Dialect)
		out.ReasoningDialect = &dialect
		out.ReasoningOpaque = append([]byte(nil), ev.Reasoning.Opaque...)
	}
	if ev.ToolCallID != "" {
		id := ev.ToolCallID
		out.ToolCallID = &id
	}
	if ev.ToolName != "" {
		n := ev.ToolName
		out.ToolName = &n
	}
	if ev.InputTokens != 0 || ev.OutputTokens != 0 {
		in := int64(ev.InputTokens)
		ot := int64(ev.OutputTokens)
		total := in + ot
		out.Usage = &UsageEvidence{
			InputTokens:  &in,
			OutputTokens: &ot,
			TotalTokens:  &total,
			Presence: UsagePresence{
				InputTokens:  ev.InputTokens != 0,
				OutputTokens: ev.OutputTokens != 0,
				TotalTokens:  true,
			},
			RawUsageJSON: RawJSONAbsentValue(),
		}
	}
	if ev.WarningMessage != "" {
		w := ev.WarningMessage
		out.Warning = &w
	}
	if ev.ErrorCode != "" || ev.ErrorMessage != "" {
		out.Error = &PluginError{
			Code:    ErrorCode(ev.ErrorCode),
			Message: ev.ErrorMessage,
		}
	}
	if ev.AssistantRef != "" {
		r := ev.AssistantRef
		switch ev.Kind {
		case lipapi.EventAssistantFileRef:
			out.FileRef = &r
		default:
			out.ImageRef = &r
		}
	}
	return out
}

func messagesToLipapi(in []Message) ([]lipapi.Message, error) {
	out := make([]lipapi.Message, 0, len(in))
	for _, m := range in {
		parts, err := partsToLipapi(m.Parts)
		if err != nil {
			return nil, err
		}
		out = append(out, lipapi.Message{
			Role:  m.Role,
			Parts: parts,
		})
	}
	return out, nil
}

func partsToLipapi(in []Part) ([]lipapi.Part, error) {
	out := make([]lipapi.Part, 0, len(in))
	for _, p := range in {
		switch p.Kind {
		case PartKindText:
			t := ""
			if p.Text != nil {
				t = *p.Text
			}
			out = append(out, lipapi.TextPart(t))
		case PartKindImageRef:
			r := ""
			if p.ImageRef != nil {
				r = *p.ImageRef
			}
			out = append(out, lipapi.Part{Kind: lipapi.PartImageRef, ImageRef: r})
		case PartKindFileRef:
			r := ""
			if p.FileRef != nil {
				r = *p.FileRef
			}
			out = append(out, lipapi.Part{Kind: lipapi.PartFileRef, FileRef: r})
		case PartKindReasoning:
			text := ""
			if p.ReasoningText != nil {
				text = *p.ReasoningText
			}
			reasoning := &lipapi.ReasoningPart{Text: text}
			if p.ReasoningDialect != nil {
				reasoning.Dialect = lipapi.ReasoningDialect(*p.ReasoningDialect)
			}
			if opaque := p.ReasoningOpaque.Bytes(); len(opaque) > 0 {
				reasoning.Opaque = opaque
			}
			out = append(out, lipapi.Part{
				Kind:      lipapi.PartReasoning,
				Reasoning: reasoning,
			})
		case PartKindToolResult:
			part := lipapi.Part{Kind: lipapi.PartToolResult}
			if p.ToolCallID != nil {
				part.ToolCallID = *p.ToolCallID
			}
			if p.ToolName != nil {
				part.ToolName = *p.ToolName
			}
			if b := p.ToolArgsJSON.Bytes(); len(b) > 0 {
				part.Content = b
			}
			out = append(out, part)
		case PartKindJSON:
			b := p.ToolArgsJSON.Bytes()
			if len(b) == 0 {
				return nil, fmt.Errorf("%w: json part requires content", ErrInvalidInvocation)
			}
			out = append(out, lipapi.Part{Kind: lipapi.PartJSON, Content: b})
		default:
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedPartKind, p.Kind)
		}
	}
	return out, nil
}

func toolsToLipapi(in []ToolDef) []lipapi.ToolDef {
	out := make([]lipapi.ToolDef, 0, len(in))
	for _, t := range in {
		td := lipapi.ToolDef{Name: t.Name, Description: t.Description}
		if b := t.ParametersJSON.Bytes(); len(b) > 0 {
			td.Parameters = b
		}
		out = append(out, td)
	}
	return out
}

func optionsToLipapi(o GenerationOptions) lipapi.GenerationOptions {
	out := lipapi.GenerationOptions{}
	if o.MaxOutputTokens != nil {
		v := int(*o.MaxOutputTokens)
		out.MaxOutputTokens = &v
	}
	if o.TemperatureMillis != nil {
		v := float64(*o.TemperatureMillis) / 1000
		out.Temperature = &v
	}
	if o.ReasoningEffort != nil {
		out.ReasoningEffort = strings.TrimSpace(*o.ReasoningEffort)
	}
	if o.ParallelToolCalls != nil {
		v := *o.ParallelToolCalls
		out.ParallelToolCalls = &v
	}
	if o.ResponseMIMEType != nil {
		out.ResponseMIMEType = strings.TrimSpace(*o.ResponseMIMEType)
	}
	return out
}
