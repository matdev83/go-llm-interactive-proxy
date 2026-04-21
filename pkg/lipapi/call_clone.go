package lipapi

import (
	"encoding/json"
)

// CloneCall returns a deep copy of c suitable as an immutable baseline for per-attempt derivation.
func CloneCall(c Call) Call {
	out := c
	out.Instructions = cloneMessages(c.Instructions)
	out.Messages = cloneMessages(c.Messages)
	out.Tools = cloneTools(c.Tools)
	out.Options = CloneGenerationOptions(c.Options)
	if len(c.Extensions) > 0 {
		out.Extensions = make(map[string]json.RawMessage, len(c.Extensions))
		for k, v := range c.Extensions {
			out.Extensions[k] = append(json.RawMessage(nil), v...)
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
	}
	return out
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
