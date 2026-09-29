package controltool

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	ReasonActive                  = "active"
	ReasonBackendToolsUnsupported = "backend_tools_unsupported"
	ReasonToolChoiceNone          = "tool_choice_none"
	ReasonToolChoiceConstrained   = "tool_choice_constrained"
	ReasonToolChoiceRequired      = "tool_choice_required"
	ReasonAllowedToolsConstrained = "allowed_tools_constrained"
	ReasonToolNameCollision       = "tool_name_collision"
)

// Projection is trusted attempt-local state derived from one validated provider spec.
type Projection struct {
	ProviderID       string
	ToolName         string
	MaxArgsBytes     int
	Active           bool
	ReasonCode       string
	ToolIndex        int
	InstructionIndex int
	ItemAuthority    bool

	spec Spec
}

// Project conditionally materializes one validated control tool into a candidate-local call.
func Project(call lipapi.Call, caps lipapi.BackendCaps, provider Provider) (lipapi.Call, Projection, error) {
	id, spec, err := Resolve(provider)
	if err != nil {
		return call, Projection{}, err
	}
	p := Projection{
		ProviderID:       id,
		ToolName:         spec.Tool.Name,
		MaxArgsBytes:     spec.MaxArgsBytes,
		ReasonCode:       ReasonActive,
		ToolIndex:        len(call.Tools),
		ItemAuthority:    call.HasItemAuthority(),
		InstructionIndex: len(call.Instructions),
		spec:             cloneSpec(spec),
	}
	if call.HasItemAuthority() {
		p.InstructionIndex = 0
	}
	if _, ok := caps[lipapi.CapabilityTools]; !ok {
		p.ReasonCode = ReasonBackendToolsUnsupported
		return call, p, nil
	}
	switch call.ToolChoice.Mode {
	case lipapi.ToolChoiceNone:
		p.ReasonCode = ReasonToolChoiceNone
		return call, p, nil
	case lipapi.ToolChoiceAny:
		p.ReasonCode = ReasonToolChoiceConstrained
		return call, p, nil
	case lipapi.ToolChoiceRequired:
		p.ReasonCode = ReasonToolChoiceRequired
		return call, p, nil
	case "", lipapi.ToolChoiceAuto:
	default:
		p.ReasonCode = ReasonToolChoiceConstrained
		return call, p, nil
	}
	if len(call.ToolChoice.AllowedTools) > 0 {
		p.ReasonCode = ReasonAllowedToolsConstrained
		return call, p, nil
	}
	for _, tool := range call.Tools {
		if tool.Name == spec.Tool.Name {
			p.ReasonCode = ReasonToolNameCollision
			return call, p, nil
		}
	}
	out := lipapi.CloneCall(call)
	out.Tools = append(out.Tools, cloneTool(spec.Tool))
	insertInstruction(&out, p, spec.Instruction)
	if err := out.Validate(); err != nil {
		return call, Projection{}, fmt.Errorf("%w: projected call invalid: %v", ErrProjection, err)
	}
	p.Active = true
	return out, p, nil
}

// Reassert restores only the already-approved byte-identical projection.
func Reassert(call lipapi.Call, p Projection) (lipapi.Call, error) {
	if !p.Active {
		return call, nil
	}
	if err := ValidateSpec(p.spec); err != nil {
		return call, fmt.Errorf("%w: active spec invalid: %v", ErrProjection, err)
	}
	if p.ProviderID == "" || p.ToolName != p.spec.Tool.Name || p.MaxArgsBytes != p.spec.MaxArgsBytes {
		return call, fmt.Errorf("%w: activation/spec mismatch", ErrProjection)
	}
	out := lipapi.CloneCall(call)

	toolPresent := false
	if p.ToolIndex >= 0 && p.ToolIndex < len(out.Tools) && sameTool(out.Tools[p.ToolIndex], p.spec.Tool) {
		toolPresent = true
	}
	if !toolPresent {
		for _, tool := range out.Tools {
			if tool.Name == p.ToolName {
				return call, fmt.Errorf("%w: control tool name was replaced or reordered incompatibly", ErrProjection)
			}
		}
		idx := p.ToolIndex
		if idx < 0 || idx > len(out.Tools) {
			return call, fmt.Errorf("%w: control tool insertion point unavailable", ErrProjection)
		}
		out.Tools = slices.Insert(out.Tools, idx, cloneTool(p.spec.Tool))
	}

	if !instructionAt(out, p, p.spec.Instruction) {
		if p.ItemAuthority != out.HasItemAuthority() {
			return call, fmt.Errorf("%w: request authority changed after activation", ErrProjection)
		}
		insertInstruction(&out, p, p.spec.Instruction)
	}
	if err := out.Validate(); err != nil {
		return call, fmt.Errorf("%w: reasserted call invalid: %v", ErrProjection, err)
	}
	return out, nil
}

// Spec returns a defensive copy of the frozen active spec.
func (p Projection) Spec() Spec { return cloneSpec(p.spec) }

func cloneTool(t lipapi.ToolDef) lipapi.ToolDef {
	t.Parameters = append([]byte(nil), t.Parameters...)
	return t
}

func sameTool(a, b lipapi.ToolDef) bool {
	return a.Name == b.Name && a.Description == b.Description && bytes.Equal(a.Parameters, b.Parameters)
}

func instructionAt(call lipapi.Call, p Projection, in Instruction) bool {
	if p.ItemAuthority {
		if p.InstructionIndex < 0 || p.InstructionIndex >= len(call.Items) {
			return false
		}
		item := call.Items[p.InstructionIndex]
		return item.Kind == lipapi.ItemKindMessage && item.Role == in.Role && len(item.Content) == 1 &&
			item.Content[0].Kind == lipapi.ContentPartText && item.Content[0].Text == in.Text
	}
	if p.InstructionIndex < 0 || p.InstructionIndex >= len(call.Instructions) {
		return false
	}
	msg := call.Instructions[p.InstructionIndex]
	return msg.Role == in.Role && len(msg.Parts) == 1 && msg.Parts[0].Kind == lipapi.PartText && msg.Parts[0].Text == in.Text
}

func insertInstruction(call *lipapi.Call, p Projection, in Instruction) {
	if p.ItemAuthority {
		item := lipapi.Item{
			Kind:   lipapi.ItemKindMessage,
			Status: lipapi.ItemStatusCompleted,
			Role:   in.Role,
			Content: []lipapi.ContentPart{{
				Kind: lipapi.ContentPartText,
				Text: in.Text,
			}},
		}
		idx := p.InstructionIndex
		if idx < 0 {
			idx = 0
		}
		if idx > len(call.Items) {
			idx = len(call.Items)
		}
		call.Items = slices.Insert(call.Items, idx, item)
		return
	}
	msg := lipapi.Message{Role: in.Role, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: in.Text}}}
	idx := p.InstructionIndex
	if idx < 0 {
		idx = 0
	}
	if idx > len(call.Instructions) {
		idx = len(call.Instructions)
	}
	call.Instructions = slices.Insert(call.Instructions, idx, msg)
}
