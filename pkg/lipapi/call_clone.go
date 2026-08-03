package lipapi

import (
	"encoding/json"
)

// CloneCall returns a deep copy of c suitable as an immutable baseline for per-attempt derivation.
func CloneCall(c Call) Call {
	out := c
	out.Instructions = cloneMessages(c.Instructions)
	out.Messages = cloneMessages(c.Messages)
	out.Items = cloneItems(c.Items)
	out.Tools = cloneTools(c.Tools)
	out.Options = CloneGenerationOptions(c.Options)
	if len(c.ToolChoice.AllowedTools) > 0 {
		out.ToolChoice.AllowedTools = append([]string(nil), c.ToolChoice.AllowedTools...)
	}
	if len(c.Extensions) > 0 {
		out.Extensions = make(map[string]json.RawMessage, len(c.Extensions))
		for k, v := range c.Extensions {
			out.Extensions[k] = append(json.RawMessage(nil), v...)
		}
	}
	if c.Session.Metadata != nil {
		out.Session.Metadata = make(map[string]string, len(c.Session.Metadata))
		for k, v := range c.Session.Metadata {
			out.Session.Metadata[k] = v
		}
	}
	return out
}

// CloneGenerationOptions returns a copy with independent pointer fields.
func CloneGenerationOptions(o GenerationOptions) GenerationOptions {
	out := o
	if o.Temperature != nil {
		t := *o.Temperature
		out.Temperature = &t
	}
	if o.MaxOutputTokens != nil {
		n := *o.MaxOutputTokens
		out.MaxOutputTokens = &n
	}
	if o.TopP != nil {
		p := *o.TopP
		out.TopP = &p
	}
	if o.ParallelToolCalls != nil {
		b := *o.ParallelToolCalls
		out.ParallelToolCalls = &b
	}
	return out
}

func cloneMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]Message, len(in))
	for i := range in {
		out[i].Role = in[i].Role
		out[i].Parts = cloneParts(in[i].Parts)
	}
	return out
}

func cloneParts(in []Part) []Part {
	if len(in) == 0 {
		return nil
	}
	out := make([]Part, len(in))
	copy(out, in)
	for i := range out {
		if len(in[i].Content) > 0 {
			out[i].Content = append(json.RawMessage(nil), in[i].Content...)
		}
		if in[i].Reasoning != nil {
			out[i].Reasoning = cloneReasoningPart(in[i].Reasoning)
		}
	}
	return out
}

func cloneReasoningPart(in *ReasoningPart) *ReasoningPart {
	if in == nil {
		return nil
	}
	out := *in
	if in.Opaque != nil {
		out.Opaque = append(json.RawMessage{}, in.Opaque...)
	} else {
		out.Opaque = nil
	}
	if in.Summary != nil {
		out.Summary = append(json.RawMessage{}, in.Summary...)
	}
	if in.Content != nil {
		out.Content = append(json.RawMessage{}, in.Content...)
	}
	if in.EncryptedContent != nil {
		out.EncryptedContent = append(json.RawMessage{}, in.EncryptedContent...)
	}
	return &out
}

func cloneTools(in []ToolDef) []ToolDef {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolDef, len(in))
	for i := range in {
		out[i].Name = in[i].Name
		out[i].Description = in[i].Description
		if len(in[i].Parameters) > 0 {
			out[i].Parameters = append(json.RawMessage(nil), in[i].Parameters...)
		}
	}
	return out
}

func cloneItems(in []Item) []Item {
	if len(in) == 0 {
		return nil
	}
	out := make([]Item, len(in))
	for i := range in {
		out[i] = cloneItemValue(in[i])
	}
	return out
}

// cloneItemValue returns a deep copy of one canonical item. It is the shared
// single-item clone used by call cloning and by event carriers that retain an
// item (EventItem).
func cloneItemValue(in Item) Item {
	out := in
	if len(in.Content) > 0 {
		out.Content = cloneContentParts(in.Content)
	}
	if in.Reference != nil {
		ref := *in.Reference
		out.Reference = &ref
	}
	if in.ToolCall != nil {
		tc := *in.ToolCall
		if len(in.ToolCall.Arguments) > 0 {
			tc.Arguments = append(json.RawMessage(nil), in.ToolCall.Arguments...)
		}
		out.ToolCall = &tc
	}
	if in.ToolResult != nil {
		tr := *in.ToolResult
		if len(in.ToolResult.Parts) > 0 {
			tr.Parts = cloneContentParts(in.ToolResult.Parts)
		}
		out.ToolResult = &tr
	}
	if in.Reasoning != nil {
		ri := *in.Reasoning
		if in.Reasoning.Reasoning != nil {
			ri.Reasoning = cloneReasoningPart(in.Reasoning.Reasoning)
		}
		out.Reasoning = &ri
	}
	if in.Compaction != nil {
		ci := *in.Compaction
		if len(in.Compaction.Opaque) > 0 {
			ci.Opaque = append(json.RawMessage(nil), in.Compaction.Opaque...)
		}
		out.Compaction = &ci
	}
	if in.Extension != nil {
		ext := *in.Extension
		if len(in.Extension.Data) > 0 {
			ext.Data = append(json.RawMessage(nil), in.Extension.Data...)
		}
		out.Extension = &ext
	}
	return out
}

func cloneContentParts(in []ContentPart) []ContentPart {
	if len(in) == 0 {
		return nil
	}
	out := make([]ContentPart, len(in))
	copy(out, in)
	for i := range out {
		if in[i].Reasoning != nil {
			out[i].Reasoning = cloneReasoningPart(in[i].Reasoning)
		}
		if in[i].Annotation != nil {
			ann := *in[i].Annotation
			if len(in[i].Annotation.Data) > 0 {
				ann.Data = append(json.RawMessage(nil), in[i].Annotation.Data...)
			}
			out[i].Annotation = &ann
		}
		if in[i].Extension != nil {
			ext := *in[i].Extension
			if len(in[i].Extension.Data) > 0 {
				ext.Data = append(json.RawMessage(nil), in[i].Extension.Data...)
			}
			out[i].Extension = &ext
		}
	}
	return out
}
