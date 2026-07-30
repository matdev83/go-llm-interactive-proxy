package adapter

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// InvocationFromCall maps a core call into the public plugin Invocation DTO.
func InvocationFromCall(call lipapi.Call, cand routing.AttemptCandidate) (backendplugin.Invocation, error) {
	reqID := strings.TrimSpace(call.ID)
	if reqID == "" {
		reqID = "req"
	}
	attemptID := reqID + "-attempt"
	aLeg := strings.TrimSpace(call.Session.ALegID)
	if aLeg == "" {
		aLeg = "aleg"
	}
	bLeg := strings.TrimSpace(cand.Key)
	if bLeg == "" {
		bLeg = strings.TrimSpace(cand.Primary.String())
	}
	if bLeg == "" {
		bLeg = "bleg"
	}
	model := strings.TrimSpace(cand.Primary.Model)
	if model == "" {
		model = strings.TrimSpace(call.Route.Selector)
	}
	if model == "" {
		model = "model"
	}
	instructions, err := mapMessages(call.Instructions)
	if err != nil {
		return backendplugin.Invocation{}, err
	}
	messages, err := mapMessages(call.Messages)
	if err != nil {
		return backendplugin.Invocation{}, err
	}
	inv := backendplugin.Invocation{
		RequestID:        reqID,
		AttemptID:        attemptID,
		ALegID:           aLeg,
		BLegID:           bLeg,
		CanonicalModelID: model,
		NativeModelID:    model,
		Instructions:     instructions,
		Messages:         messages,
		Tools:            mapTools(call.Tools),
		Options:          mapOptions(call.Options),
	}
	routeParams := map[string]string{}
	if cand.Primary.Params != nil {
		for k := range cand.Primary.Params {
			if v := cand.Primary.TrimmedParam(k); v != "" {
				routeParams[k] = v
			}
		}
	}
	backendplugin.ApplyCallWireMetadata(&inv, call, routeParams)
	if err := inv.Validate(); err != nil {
		return backendplugin.Invocation{}, err
	}
	return inv, nil
}

func mapMessages(in []lipapi.Message) ([]backendplugin.Message, error) {
	out := make([]backendplugin.Message, 0, len(in))
	for _, m := range in {
		parts, err := mapParts(m.Parts)
		if err != nil {
			return nil, err
		}
		out = append(out, backendplugin.Message{
			Role:  m.Role,
			Parts: parts,
		})
	}
	return out, nil
}

func mapParts(in []lipapi.Part) ([]backendplugin.Part, error) {
	out := make([]backendplugin.Part, 0, len(in))
	for _, p := range in {
		bp := backendplugin.Part{
			Kind:         backendplugin.PartKind(p.Kind),
			ToolArgsJSON: backendplugin.RawJSONAbsentValue(),
		}
		switch p.Kind {
		case lipapi.PartText:
			t := p.Text
			bp.Kind = backendplugin.PartKindText
			bp.Text = &t
		case lipapi.PartImageRef:
			r := p.ImageRef
			bp.Kind = backendplugin.PartKindImageRef
			bp.ImageRef = &r
		case lipapi.PartFileRef:
			r := p.FileRef
			bp.Kind = backendplugin.PartKindFileRef
			bp.FileRef = &r
		case lipapi.PartReasoning:
			bp.Kind = backendplugin.PartKindReasoning
			if p.Reasoning != nil {
				t := p.Reasoning.Text
				bp.ReasoningText = &t
				dialect := string(p.Reasoning.Dialect)
				bp.ReasoningDialect = &dialect
				if len(p.Reasoning.Opaque) > 0 {
					bp.ReasoningOpaque = backendplugin.RawJSONFromBytes(p.Reasoning.Opaque)
				}
			}
		case lipapi.PartToolResult:
			id := p.ToolCallID
			name := p.ToolName
			bp.Kind = backendplugin.PartKindToolResult
			bp.ToolCallID = &id
			bp.ToolName = &name
			if len(p.Content) > 0 {
				bp.ToolArgsJSON = backendplugin.RawJSONFromBytes(p.Content)
			}
		case lipapi.PartJSON:
			bp.Kind = backendplugin.PartKindJSON
			if len(p.Content) == 0 {
				return nil, fmt.Errorf("%w: json part requires content", backendplugin.ErrInvalidInvocation)
			}
			bp.ToolArgsJSON = backendplugin.RawJSONFromBytes(p.Content)
		default:
			return nil, fmt.Errorf("%w: %q", backendplugin.ErrUnsupportedPartKind, p.Kind)
		}
		out = append(out, bp)
	}
	return out, nil
}

func mapTools(in []lipapi.ToolDef) []backendplugin.ToolDef {
	out := make([]backendplugin.ToolDef, 0, len(in))
	for _, t := range in {
		params := backendplugin.RawJSONAbsentValue()
		if len(t.Parameters) > 0 {
			params = backendplugin.RawJSONFromBytes(t.Parameters)
		}
		out = append(out, backendplugin.ToolDef{
			Name: t.Name, Description: t.Description, ParametersJSON: params,
		})
	}
	return out
}

func mapOptions(o lipapi.GenerationOptions) backendplugin.GenerationOptions {
	out := backendplugin.GenerationOptions{
		ResponseSchemaJSON: backendplugin.RawJSONAbsentValue(),
	}
	if o.MaxOutputTokens != nil && *o.MaxOutputTokens > 0 {
		v := uint32(*o.MaxOutputTokens)
		out.MaxOutputTokens = &v
	}
	if o.Temperature != nil {
		ms := int32(*o.Temperature * 1000)
		out.TemperatureMillis = &ms
	}
	if s := strings.TrimSpace(o.ReasoningEffort); s != "" {
		out.ReasoningEffort = &s
	}
	if o.ParallelToolCalls != nil {
		v := *o.ParallelToolCalls
		out.ParallelToolCalls = &v
	}
	if s := strings.TrimSpace(o.ResponseMIMEType); s != "" {
		out.ResponseMIMEType = &s
	}
	return out
}
