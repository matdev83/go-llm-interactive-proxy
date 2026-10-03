package controltool

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func projectionClientTool() lipapi.ToolDef {
	return lipapi.ToolDef{
		Name:        "client_tool",
		Description: "Client-declared tool.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
	}
}

func projectionToolsCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(lipapi.CapabilityTools)
}

func projectionMessageCall() lipapi.Call {
	return lipapi.Call{
		ID: "call-1",
		Session: lipapi.SessionRef{
			ALegID: "a-leg-1",
		},
		Instructions: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("client system prompt")}},
		},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("do the work")}},
		},
		Tools: []lipapi.ToolDef{projectionClientTool()},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIResponses,
			DeliveryMode: lipapi.DeliveryModeStreaming,
		},
		PromptCacheKey: "cache-1",
	}
}

func projectionItemCall() lipapi.Call {
	return lipapi.Call{
		ID: "call-2",
		Items: []lipapi.Item{
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleUser,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "do the work"}},
			},
		},
		Tools: []lipapi.ToolDef{projectionClientTool()},
	}
}

// projectionPrefixedItemCall is item authority whose trajectory already starts
// with the client's own leading system/developer instruction items, so the
// frozen instruction prefix owns more than the single stable anchor.
func projectionPrefixedItemCall() lipapi.Call {
	call := projectionItemCall()
	call.ID = "call-4"
	leading := []lipapi.Item{
		{
			Kind:    lipapi.ItemKindMessage,
			Role:    lipapi.RoleSystem,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "client system prefix"}},
		},
		{
			Kind:    lipapi.ItemKindMessage,
			Role:    lipapi.RoleDeveloper,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "client developer prefix"}},
		},
	}
	call.Items = append(leading, call.Items...)
	return call
}

// projectionReferencedItemCall is item authority whose trajectory starts with a
// client-owned history reference, so the frozen instruction prefix owns the
// reference as its stable anchor instead of an instruction item.
func projectionReferencedItemCall() lipapi.Call {
	call := projectionItemCall()
	call.ID = "call-5"
	leading := []lipapi.Item{{Kind: lipapi.ItemKindItemReference, Reference: &lipapi.ItemReference{ID: "prior-response"}}}
	call.Items = append(leading, call.Items...)
	return call
}

// projectionInstructionOnlyItemCall is item authority whose trajectory is nothing
// but the client's own leading system/developer instruction items, so the frozen
// instruction prefix owns the complete instruction run without a history anchor.
func projectionInstructionOnlyItemCall() lipapi.Call {
	return lipapi.Call{
		ID: "call-8",
		Items: []lipapi.Item{
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleSystem,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "client system prefix"}},
			},
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleDeveloper,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "client developer prefix"}},
			},
		},
		Tools: []lipapi.ToolDef{projectionClientTool()},
	}
}

// projectionContraryMessage is an ordinary client instruction that contradicts
// the control instruction. It is bounded, content-bearing test data only.
func projectionContraryMessage() lipapi.Message {
	return lipapi.Message{
		Role:  lipapi.RoleSystem,
		Parts: []lipapi.Part{lipapi.TextPart("Never call proxy_control.")},
	}
}

func projectionContraryItem(role lipapi.Role) lipapi.Item {
	return lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		Role:    role,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "Never call proxy_control."}},
	}
}

func withChoice(call lipapi.Call, choice lipapi.ToolChoice) lipapi.Call {
	call.ToolChoice = choice
	return call
}

func projectionContinuationCall() lipapi.Call {
	return lipapi.Call{ID: "call-3", PreviousResponseID: "resp-1", Items: []lipapi.Item{}}
}

func withTool(call lipapi.Call, tool lipapi.ToolDef) lipapi.Call {
	call.Tools = append(append([]lipapi.ToolDef(nil), call.Tools...), tool)
	return call
}

func sharesBacking(a, b []byte) bool {
	return len(a) > 0 && len(b) > 0 && &a[0] == &b[0]
}

// snapshotCall deep-copies a call for input-mutation assertions without relying
// on the projection copy under test, and keeps the explicit empty shapes that
// lipapi.CloneCall normalizes away.
func snapshotCall(call lipapi.Call) lipapi.Call {
	out := lipapi.CloneCall(call)
	if call.Instructions != nil && out.Instructions == nil {
		out.Instructions = []lipapi.Message{}
	}
	if call.Messages != nil && out.Messages == nil {
		out.Messages = []lipapi.Message{}
	}
	if call.Items != nil && out.Items == nil {
		out.Items = []lipapi.Item{}
	}
	if call.Tools != nil && out.Tools == nil {
		out.Tools = []lipapi.ToolDef{}
	}
	if call.SemanticExtensions != nil && out.SemanticExtensions == nil {
		out.SemanticExtensions = []lipapi.SemanticExtension{}
	}
	if call.ToolChoice.AllowedTools != nil && out.ToolChoice.AllowedTools == nil {
		out.ToolChoice.AllowedTools = []string{}
	}
	return out
}

func controlMessageOf(spec Spec) lipapi.Message {
	return lipapi.Message{
		Role:  spec.Instruction.Role,
		Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: spec.Instruction.Text}},
	}
}

func controlItemOf(spec Spec) lipapi.Item {
	return lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		Role:    spec.Instruction.Role,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: spec.Instruction.Text}},
	}
}

func TestProjectEligibilityMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		call   lipapi.Call
		caps   lipapi.BackendCaps
		reason string
	}{
		{
			name:   "default tool choice is eligible",
			call:   projectionMessageCall(),
			caps:   projectionToolsCaps(),
			reason: ReasonActive,
		},
		{
			name:   "explicit auto is eligible",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}),
			caps:   projectionToolsCaps(),
			reason: ReasonActive,
		},
		{
			name:   "item authority default is eligible",
			call:   projectionItemCall(),
			caps:   projectionToolsCaps(),
			reason: ReasonActive,
		},
		{
			name:   "no declared client tools is eligible",
			call:   lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("work")}}}},
			caps:   projectionToolsCaps(),
			reason: ReasonActive,
		},
		{
			name:   "explicitly empty allowed tools subset is eligible",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{AllowedTools: []string{}}),
			caps:   projectionToolsCaps(),
			reason: ReasonActive,
		},
		{
			name:   "backend without tool capability",
			call:   projectionMessageCall(),
			caps:   lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			reason: ReasonBackendToolsUnsupported,
		},
		{
			name:   "nil backend capabilities",
			call:   projectionMessageCall(),
			caps:   nil,
			reason: ReasonBackendToolsUnsupported,
		},
		{
			name:   "tool choice none",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}),
			caps:   projectionToolsCaps(),
			reason: ReasonToolChoiceNone,
		},
		{
			name:   "tool choice any",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}),
			caps:   projectionToolsCaps(),
			reason: ReasonToolChoiceConstrained,
		},
		{
			name:   "required named client tool",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "client_tool"}),
			caps:   projectionToolsCaps(),
			reason: ReasonToolChoiceRequired,
		},
		{
			name:   "required without name is malformed and constrained",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired}),
			caps:   projectionToolsCaps(),
			reason: ReasonToolChoiceRequired,
		},
		{
			name:   "named requirement on auto mode is constrained",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto, Name: "client_tool"}),
			caps:   projectionToolsCaps(),
			reason: ReasonToolChoiceRequired,
		},
		{
			name:   "unknown tool choice mode is constrained",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{Mode: lipapi.ToolChoiceMode("guarded")}),
			caps:   projectionToolsCaps(),
			reason: ReasonToolChoiceConstrained,
		},
		{
			name:   "allowed tools subset is constrained",
			call:   withChoice(projectionMessageCall(), lipapi.ToolChoice{AllowedTools: []string{"client_tool"}}),
			caps:   projectionToolsCaps(),
			reason: ReasonAllowedToolsConstrained,
		},
		{
			name:   "byte identical client tool name is a collision",
			call:   withTool(projectionMessageCall(), validSpec().Tool),
			caps:   projectionToolsCaps(),
			reason: ReasonToolNameCollision,
		},
		{
			name: "different client tool name is a collision",
			call: withTool(projectionMessageCall(), lipapi.ToolDef{
				Name:        validSpec().Tool.Name,
				Description: "Client-owned look-alike.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`),
			}),
			caps:   projectionToolsCaps(),
			reason: ReasonToolNameCollision,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := validSpec()
			before := lipapi.CloneCall(test.call)

			got, projection, err := Project(test.call, spec, test.caps)
			if err != nil {
				t.Fatalf("Project() error = %v, want no error for ordinary ineligibility", err)
			}
			if projection.Reason() != test.reason {
				t.Fatalf("Projection.Reason() = %q, want %q", projection.Reason(), test.reason)
			}
			if projection.Active() != (test.reason == ReasonActive) {
				t.Fatalf("Projection.Active() = %v for reason %q", projection.Active(), test.reason)
			}
			if !reflect.DeepEqual(test.call, before) {
				t.Fatal("Project() mutated the input call")
			}
			if test.reason == ReasonActive {
				if projection.ToolName() != spec.Tool.Name {
					t.Fatalf("Projection.ToolName() = %q, want %q", projection.ToolName(), spec.Tool.Name)
				}
				if projection.MaxArgsBytes() != spec.MaxArgsBytes {
					t.Fatalf("Projection.MaxArgsBytes() = %d, want %d", projection.MaxArgsBytes(), spec.MaxArgsBytes)
				}
				if !reflect.DeepEqual(projection.Instruction(), spec.Instruction) {
					t.Fatalf("Projection.Instruction() = %+v, want %+v", projection.Instruction(), spec.Instruction)
				}
				if !reflect.DeepEqual(projection.ToolChoice(), test.call.ToolChoice) {
					t.Fatalf("Projection.ToolChoice() = %+v, want %+v", projection.ToolChoice(), test.call.ToolChoice)
				}
				if got.ToolChoice.AllowedTools == nil != (test.call.ToolChoice.AllowedTools == nil) {
					t.Fatal("ToolChoice.AllowedTools empty-vs-null shape changed")
				}
				if err := got.Validate(); err != nil {
					t.Fatalf("projected call is canonically invalid: %v", err)
				}
				return
			}
			if err := validateReasonCode(projection.Reason()); err != nil {
				t.Fatalf("inactive reason %q is not bounded/content-free: %v", projection.Reason(), err)
			}
			if !reflect.DeepEqual(got, test.call) {
				t.Fatal("ineligible projection changed the candidate call")
			}
			if !reflect.DeepEqual(got.ToolChoice, test.call.ToolChoice) {
				t.Fatalf("ineligible projection rewrote ToolChoice: got %+v want %+v", got.ToolChoice, test.call.ToolChoice)
			}
			if !reflect.DeepEqual(projection.Tool(), lipapi.ToolDef{}) {
				t.Fatalf("inactive projection carries a tool: %+v", projection.Tool())
			}
			if projection.MaxArgsBytes() != 0 {
				t.Fatalf("inactive projection carries an args budget: %d", projection.MaxArgsBytes())
			}
		})
	}
}

func TestProjectMessageAuthority(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	in := projectionMessageCall()
	before := lipapi.CloneCall(in)

	got, projection, err := Project(in, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if !projection.Active() {
		t.Fatalf("Projection inactive: %q", projection.Reason())
	}
	if len(got.Instructions) != 2 {
		t.Fatalf("Instructions length = %d, want 2", len(got.Instructions))
	}
	if want := controlMessageOf(spec); !reflect.DeepEqual(got.Instructions[0], want) {
		t.Fatalf("control instruction = %+v, want %+v", got.Instructions[0], want)
	}
	if !reflect.DeepEqual(got.Instructions[1], before.Instructions[0]) {
		t.Fatal("existing client instruction was not retained after the control prefix")
	}
	if !reflect.DeepEqual(got.Messages, before.Messages) {
		t.Fatal("client message history was modified")
	}
	if len(got.Tools) != 2 {
		t.Fatalf("Tools length = %d, want 2", len(got.Tools))
	}
	if !reflect.DeepEqual(got.Tools[0], before.Tools[0]) {
		t.Fatal("client tool order was not preserved")
	}
	if got.Tools[1].Name != spec.Tool.Name || got.Tools[1].Description != spec.Tool.Description {
		t.Fatalf("projected tool = %+v, want exactly spec.Tool", got.Tools[1])
	}
	if !bytes.Equal(got.Tools[1].Parameters, spec.Tool.Parameters) {
		t.Fatalf("projected schema bytes = %s, want %s", got.Tools[1].Parameters, spec.Tool.Parameters)
	}
	if !reflect.DeepEqual(got.ToolChoice, in.ToolChoice) {
		t.Fatalf("ToolChoice = %+v, want unchanged %+v", got.ToolChoice, in.ToolChoice)
	}
	if !reflect.DeepEqual(got.ToolChoice.AllowedTools, in.ToolChoice.AllowedTools) {
		t.Fatal("ToolChoice.AllowedTools empty-vs-null shape changed")
	}
	if !reflect.DeepEqual(got.Invocation, in.Invocation) || got.PromptCacheKey != in.PromptCacheKey {
		t.Fatal("projection wrote into client-visible invocation metadata")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("projected call is canonically invalid: %v", err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatal("Project() mutated the input call")
	}
}

func TestProjectItemAuthority(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	spec.Instruction.Role = lipapi.RoleDeveloper
	in := projectionItemCall()
	before := lipapi.CloneCall(in)

	got, projection, err := Project(in, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if !projection.Active() {
		t.Fatalf("Projection inactive: %q", projection.Reason())
	}
	if !got.HasItemAuthority() {
		t.Fatal("projected call lost item authority")
	}
	if len(got.Items) != 2 {
		t.Fatalf("Items length = %d, want 2", len(got.Items))
	}
	if want := controlItemOf(spec); !reflect.DeepEqual(got.Items[0], want) {
		t.Fatalf("control instruction item = %+v, want %+v", got.Items[0], want)
	}
	if got.Items[0].ID != "" || got.Items[0].Status != "" || got.Items[0].Phase != "" {
		t.Fatalf("control instruction item invented identity/status/phase: %+v", got.Items[0])
	}
	if !reflect.DeepEqual(got.Items[1:], before.Items) {
		t.Fatal("existing item history was not retained after the control prefix")
	}
	if len(got.Messages) != 0 || len(got.Instructions) != 0 {
		t.Fatal("item-authority projection populated the legacy message authorities")
	}
	if got.Tools[1].Name != spec.Tool.Name || !bytes.Equal(got.Tools[1].Parameters, spec.Tool.Parameters) {
		t.Fatalf("projected tool = %+v, want exactly spec.Tool", got.Tools[1])
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("projected call is canonically invalid: %v", err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatal("Project() mutated the input call")
	}
}

func TestProjectKeepsEmptyItemAuthorityWithPreviousResponseID(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	in := lipapi.Call{ID: "call-3", PreviousResponseID: "resp-1", Items: []lipapi.Item{}}

	got, projection, err := Project(in, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if !projection.Active() {
		t.Fatalf("Projection inactive: %q", projection.Reason())
	}
	if !got.HasItemAuthority() {
		t.Fatal("empty non-nil Items lost item authority")
	}
	if len(got.Items) != 1 || !reflect.DeepEqual(got.Items[0], controlItemOf(spec)) {
		t.Fatalf("Items = %+v, want exactly the control instruction item", got.Items)
	}
	if len(got.Messages) != 0 || len(got.Instructions) != 0 {
		t.Fatal("empty item authority fell back to the legacy message authorities")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("projected continuation call is canonically invalid: %v", err)
	}
	if in.Items == nil || len(in.Items) != 0 {
		t.Fatalf("input Items = %+v, want non-nil empty", in.Items)
	}
}

func TestProjectionOwnsItsBytes(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	in := projectionMessageCall()

	got, projection, err := Project(in, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	projected := got.Tools[len(got.Tools)-1]
	if sharesBacking(projected.Parameters, spec.Tool.Parameters) {
		t.Fatal("projected tool schema aliases the frozen spec schema")
	}
	if sharesBacking(projected.Parameters, in.Tools[0].Parameters) {
		t.Fatal("projected tool schema aliases the input call")
	}
	frozen := projection.Tool()
	if sharesBacking(frozen.Parameters, spec.Tool.Parameters) {
		t.Fatal("projection schema aliases the frozen spec schema")
	}
	if sharesBacking(frozen.Parameters, projected.Parameters) {
		t.Fatal("projection schema aliases the projected call")
	}

	in.Tools[0].Parameters[0] = 'X'
	spec.Tool.Parameters[0] = 'Y'
	if got.Tools[0].Parameters[0] != '{' {
		t.Fatalf("projected client tool schema changed with the caller: %q", got.Tools[0].Parameters)
	}
	if projected.Parameters[0] != '{' {
		t.Fatalf("projected control schema changed with the spec owner: %q", projected.Parameters)
	}
	if got.Tools[1].Parameters[0] != '{' {
		t.Fatalf("projected control schema changed with the spec owner: %q", got.Tools[1].Parameters)
	}
	if !bytes.Equal(projection.Tool().Parameters, validSpec().Tool.Parameters) {
		t.Fatal("projection schema changed with the spec owner")
	}
}

func TestProjectRejectsInvalidSpecWithoutMutatingInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mutateSpec func(*Spec)
	}{
		{name: "empty tool name", mutateSpec: func(s *Spec) { s.Tool.Name = "" }},
		{name: "invalid schema", mutateSpec: func(s *Spec) { s.Tool.Parameters = json.RawMessage(`{`) }},
		{name: "unsupported instruction role", mutateSpec: func(s *Spec) { s.Instruction.Role = lipapi.RoleUser }},
		{name: "empty instruction text", mutateSpec: func(s *Spec) { s.Instruction.Text = "" }},
		{name: "missing args budget", mutateSpec: func(s *Spec) { s.MaxArgsBytes = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := validSpec()
			test.mutateSpec(&spec)
			in := projectionMessageCall()
			before := lipapi.CloneCall(in)

			got, projection, err := Project(in, spec, projectionToolsCaps())
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Project() error = %v, want ErrInvalidSpec", err)
			}
			if !reflect.DeepEqual(got, in) {
				t.Fatal("failed projection changed the candidate call")
			}
			if projection.Active() || projection.Reason() != "" || projection.ToolName() != "" {
				t.Fatalf("failed projection returned state: %+v", projection)
			}
			if !reflect.DeepEqual(in, before) {
				t.Fatal("failed projection mutated the input call")
			}
		})
	}
}

func TestReassertIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, call := range []lipapi.Call{
		projectionMessageCall(),
		projectionItemCall(),
		projectionPrefixedItemCall(),
		projectionReferencedItemCall(),
	} {
		spec := validSpec()
		projected, projection, err := Project(call, spec, projectionToolsCaps())
		if err != nil {
			t.Fatalf("Project() error = %v", err)
		}
		once, err := Reassert(projected, projection)
		if err != nil {
			t.Fatalf("Reassert() error = %v", err)
		}
		twice, err := Reassert(once, projection)
		if err != nil {
			t.Fatalf("Reassert() second error = %v", err)
		}
		if !reflect.DeepEqual(once, projected) {
			t.Fatal("Reassert() mutated an already projected call")
		}
		if !reflect.DeepEqual(twice, projected) {
			t.Fatal("Reassert(Reassert(...)) is not stable")
		}
	}
}

// TestReassertRestoresGenuineRemoval covers the only removals reassertion may
// undo: a legitimate final reconstruction that dropped the control instruction
// and/or the control tool while the frozen original instruction prefix and the
// frozen ordinary tool catalog are still exactly the approved ones.
func TestReassertRestoresGenuineRemoval(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	tests := []struct {
		name   string
		call   lipapi.Call
		remove func(lipapi.Call) lipapi.Call
	}{
		{
			name: "message authority lost the control instruction",
			call: projectionMessageCall(),
			remove: func(call lipapi.Call) lipapi.Call {
				call.Instructions = call.Instructions[1:]
				return call
			},
		},
		{
			name: "message authority lost the control tool",
			call: projectionMessageCall(),
			remove: func(call lipapi.Call) lipapi.Call {
				call.Tools = call.Tools[:len(call.Tools)-1]
				return call
			},
		},
		{
			name: "message authority lost the whole control projection",
			call: projectionMessageCall(),
			remove: func(call lipapi.Call) lipapi.Call {
				call.Instructions = call.Instructions[1:]
				call.Tools = call.Tools[:len(call.Tools)-1]
				return call
			},
		},
		{
			name: "item authority lost the control instruction item",
			call: projectionItemCall(),
			remove: func(call lipapi.Call) lipapi.Call {
				call.Items = call.Items[1:]
				return call
			},
		},
		{
			name: "item authority lost the control tool",
			call: projectionItemCall(),
			remove: func(call lipapi.Call) lipapi.Call {
				call.Tools = call.Tools[:len(call.Tools)-1]
				return call
			},
		},
		{
			name: "item authority lost the whole control projection",
			call: projectionItemCall(),
			remove: func(call lipapi.Call) lipapi.Call {
				call.Items = call.Items[1:]
				call.Tools = call.Tools[:len(call.Tools)-1]
				return call
			},
		},
		{
			name: "item authority continuation lost the whole control projection",
			call: projectionContinuationCall(),
			remove: func(call lipapi.Call) lipapi.Call {
				call.Items = []lipapi.Item{}
				call.Tools = call.Tools[:len(call.Tools)-1]
				return call
			},
		},
		{
			name: "item authority with a client instruction prefix lost the whole control projection",
			call: projectionPrefixedItemCall(),
			remove: func(call lipapi.Call) lipapi.Call {
				call.Items = call.Items[1:]
				call.Tools = call.Tools[:len(call.Tools)-1]
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected, projection, err := Project(test.call, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			stripped := test.remove(projected)
			before := snapshotCall(stripped)

			restored, err := Reassert(stripped, projection)
			if err != nil {
				t.Fatalf("Reassert() error = %v, want the approved projection restored", err)
			}
			if !reflect.DeepEqual(restored, projected) {
				t.Fatalf("Reassert() output = %+v, want the approved projection %+v", restored, projected)
			}
			if err := restored.Validate(); err != nil {
				t.Fatalf("restored call is canonically invalid: %v", err)
			}
			if !reflect.DeepEqual(stripped, before) {
				t.Fatal("Reassert() mutated its input")
			}
			twice, err := Reassert(restored, projection)
			if err != nil {
				t.Fatalf("Reassert() second error = %v", err)
			}
			if !reflect.DeepEqual(twice, restored) {
				t.Fatal("Reassert(Reassert(...)) is not stable after a genuine removal")
			}
		})
	}
}

func TestReassertFailsClosedOnDrift(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	newProjection := func(t *testing.T) (lipapi.Call, Projection) {
		t.Helper()
		projected, projection, err := Project(projectionMessageCall(), spec, projectionToolsCaps())
		if err != nil {
			t.Fatalf("Project() error = %v", err)
		}
		return projected, projection
	}

	tests := []struct {
		name   string
		mutate func(lipapi.Call) lipapi.Call
	}{
		{
			name: "duplicated control tool",
			mutate: func(call lipapi.Call) lipapi.Call {
				duplicated := append([]lipapi.ToolDef(nil), call.Tools...)
				call.Tools = append(duplicated, call.Tools[len(call.Tools)-1])
				return call
			},
		},
		{
			name: "redefined control tool",
			mutate: func(call lipapi.Call) lipapi.Call {
				tools := append([]lipapi.ToolDef(nil), call.Tools...)
				last := tools[len(tools)-1]
				last.Description = "Rewritten control description."
				tools[len(tools)-1] = last
				call.Tools = tools
				return call
			},
		},
		{
			name: "reordered ordinary client tools",
			mutate: func(call lipapi.Call) lipapi.Call {
				tools := append([]lipapi.ToolDef(nil), call.Tools...)
				tools[0], tools[1] = tools[1], tools[0]
				call.Tools = tools
				return call
			},
		},
		{
			name: "rewritten ordinary client tool description",
			mutate: func(call lipapi.Call) lipapi.Call {
				tools := append([]lipapi.ToolDef(nil), call.Tools...)
				tools[0].Description = "Rewritten client description."
				call.Tools = tools
				return call
			},
		},
		{
			name: "rewritten ordinary client tool schema",
			mutate: func(call lipapi.Call) lipapi.Call {
				tools := append([]lipapi.ToolDef(nil), call.Tools...)
				tools[0].Parameters = json.RawMessage(`{"type":"object","properties":{"rewritten":{"type":"string"}}}`)
				call.Tools = tools
				return call
			},
		},
		{
			name: "ordinary client tool appended after the control tool",
			mutate: func(call lipapi.Call) lipapi.Call {
				tools := append([]lipapi.ToolDef(nil), call.Tools...)
				tools = append(tools, lipapi.ToolDef{Name: "late_client_tool", Description: "Added after approval."})
				call.Tools = tools
				return call
			},
		},
		{
			name: "control tool moved in front of the ordinary client tools",
			mutate: func(call lipapi.Call) lipapi.Call {
				tools := append([]lipapi.ToolDef(nil), call.Tools...)
				control := tools[len(tools)-1]
				copy(tools[1:], tools[:len(tools)-1])
				tools[0] = control
				call.Tools = tools
				return call
			},
		},
		{
			name: "client-owned look-alike in the ordinary catalog",
			mutate: func(call lipapi.Call) lipapi.Call {
				tools := append([]lipapi.ToolDef(nil), call.Tools...)
				tools[0] = lipapi.ToolDef{
					Name:        validSpec().Tool.Name,
					Description: "Client-owned look-alike.",
					Parameters:  json.RawMessage(`{"type":"object"}`),
				}
				call.Tools = tools
				return call
			},
		},
		{
			name: "control tool removed while the ordinary catalog changed",
			mutate: func(call lipapi.Call) lipapi.Call {
				tools := append([]lipapi.ToolDef(nil), call.Tools[:len(call.Tools)-1]...)
				tools[0].Description = "Rewritten client description."
				call.Tools = tools
				return call
			},
		},
		{
			name: "relocated control instruction",
			mutate: func(call lipapi.Call) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				instructions[0], instructions[1] = instructions[1], instructions[0]
				call.Instructions = instructions
				return call
			},
		},
		{
			name: "changed tool choice",
			mutate: func(call lipapi.Call) lipapi.Call {
				call.ToolChoice.Mode = lipapi.ToolChoiceNone
				return call
			},
		},
		{
			name: "omitted tool choice mode became explicit auto",
			mutate: func(call lipapi.Call) lipapi.Call {
				call.ToolChoice.Mode = lipapi.ToolChoiceAuto
				return call
			},
		},
		{
			name: "added allowed tools subset",
			mutate: func(call lipapi.Call) lipapi.Call {
				call.ToolChoice.AllowedTools = []string{"client_tool"}
				return call
			},
		},
		{
			name: "named tool choice appeared",
			mutate: func(call lipapi.Call) lipapi.Call {
				call.ToolChoice.Name = "client_tool"
				return call
			},
		},
		{
			name: "switched trajectory authority",
			mutate: func(call lipapi.Call) lipapi.Call {
				call.Messages = nil
				call.Items = projectionItemCall().Items
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected, projection := newProjection(t)
			drifted := test.mutate(projected)
			before := snapshotCall(drifted)

			got, err := Reassert(drifted, projection)
			if !errors.Is(err, ErrProjectionConflict) {
				t.Fatalf("Reassert() error = %v, want ErrProjectionConflict", err)
			}
			if !reflect.DeepEqual(got, drifted) {
				t.Fatal("failed reassertion changed the candidate call")
			}
			if !reflect.DeepEqual(drifted, before) {
				t.Fatal("failed reassertion mutated its input")
			}
		})
	}
}

// TestReassertFailsClosedOnDuplicatedControlProjection proves reassertion scans
// the whole trajectory: a second exact copy of the approved control instruction
// is never tolerated, neither at the tail nor directly behind the approved one.
func TestReassertFailsClosedOnDuplicatedControlProjection(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	tests := []struct {
		name   string
		call   lipapi.Call
		mutate func(lipapi.Call, Spec) lipapi.Call
	}{
		{
			name: "message authority duplicate at the tail",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				call.Instructions = append(append([]lipapi.Message(nil), call.Instructions...), controlMessageOf(spec))
				return call
			},
		},
		{
			name: "message authority duplicate right behind the approved instruction",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				call.Instructions = slices.Insert(instructions, 1, controlMessageOf(spec))
				return call
			},
		},
		{
			name: "item authority duplicate at the tail",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				call.Items = append(append([]lipapi.Item(nil), call.Items...), controlItemOf(spec))
				return call
			},
		},
		{
			name: "item authority duplicate right behind the approved item",
			call: projectionPrefixedItemCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				call.Items = slices.Insert(items, 1, controlItemOf(spec))
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected, projection, err := Project(test.call, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			duplicated := test.mutate(projected, spec)
			before := snapshotCall(duplicated)

			got, err := Reassert(duplicated, projection)
			if !errors.Is(err, ErrProjectionConflict) {
				t.Fatalf("Reassert() error = %v, want ErrProjectionConflict", err)
			}
			if !reflect.DeepEqual(got, duplicated) {
				t.Fatal("failed reassertion changed the candidate call")
			}
			if !reflect.DeepEqual(duplicated, before) {
				t.Fatal("failed reassertion mutated its input")
			}
		})
	}
}

// TestReassertFailsClosedOnAlteredControlPrefix proves reassertion never keeps an
// altered or contradictory instruction prefix and never adopts a client-shaped
// look-alike: both authorities are checked against the small frozen original
// prefix, and anything that cannot be proved unchanged fails closed.
func TestReassertFailsClosedOnAlteredControlPrefix(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	tests := []struct {
		name   string
		call   lipapi.Call
		mutate func(lipapi.Call, Spec) lipapi.Call
	}{
		{
			name: "message authority control text was altered",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				instructions[0] = lipapi.Message{
					Role:  spec.Instruction.Role,
					Parts: []lipapi.Part{lipapi.TextPart("reconstructed system prompt")},
				}
				call.Instructions = instructions
				return call
			},
		},
		{
			name: "message authority control text arrived under a client role",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				instructions[0] = lipapi.Message{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{lipapi.TextPart(spec.Instruction.Text)},
				}
				call.Instructions = instructions
				return call
			},
		},
		{
			name: "message authority control instruction was split into two parts",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				instructions[0] = lipapi.Message{
					Role: spec.Instruction.Role,
					Parts: []lipapi.Part{
						{Kind: lipapi.PartText, Text: spec.Instruction.Text},
						lipapi.TextPart("client follow-up"),
					},
				}
				call.Instructions = instructions
				return call
			},
		},
		{
			name: "message authority client instruction was prepended before the control instruction",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				call.Instructions = slices.Insert(instructions, 0, lipapi.Message{
					Role:  lipapi.RoleSystem,
					Parts: []lipapi.Part{lipapi.TextPart("late client system prompt")},
				})
				return call
			},
		},
		{
			name: "message authority original client instruction was dropped",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				call.Instructions = call.Instructions[:1]
				return call
			},
		},
		{
			name: "item authority control text was altered",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				items[0] = controlItemOf(spec)
				items[0].Content[0].Text = "reconstructed control instruction"
				call.Items = items
				return call
			},
		},
		{
			name: "item authority control item carries a client item reference",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				items[0] = controlItemOf(spec)
				items[0].Reference = &lipapi.ItemReference{ID: "client-item"}
				call.Items = items
				return call
			},
		},
		{
			name: "item authority control item arrived under a client role",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				items[0] = controlItemOf(spec)
				items[0].Role = lipapi.RoleUser
				call.Items = items
				return call
			},
		},
		{
			name: "item authority client system item was prepended before the control item",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				call.Items = slices.Insert(items, 0, lipapi.Item{
					Kind:    lipapi.ItemKindMessage,
					Role:    lipapi.RoleSystem,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "late client system prompt"}},
				})
				return call
			},
		},
		{
			name: "item authority stable history anchor was dropped",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				call.Items = call.Items[:1]
				return call
			},
		},
		{
			name: "item authority client instruction prefix was reordered behind the control item",
			call: projectionPrefixedItemCall(),
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				items[1], items[2] = items[2], items[1]
				call.Items = items
				return call
			},
		},
		{
			name: "item authority client instruction prefix lost its system item",
			call: projectionPrefixedItemCall(),
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				call.Items = slices.Delete(append([]lipapi.Item(nil), call.Items...), 1, 2)
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected, projection, err := Project(test.call, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			altered := test.mutate(projected, spec)
			before := snapshotCall(altered)

			got, err := Reassert(altered, projection)
			if !errors.Is(err, ErrProjectionConflict) {
				t.Fatalf("Reassert() error = %v, want ErrProjectionConflict", err)
			}
			if !reflect.DeepEqual(got, altered) {
				t.Fatal("failed reassertion changed the candidate call")
			}
			if !reflect.DeepEqual(altered, before) {
				t.Fatal("failed reassertion mutated its input")
			}
		})
	}
}

// TestReassertFailsClosedOnToolChoiceShapeDrift proves the approved client
// tool-choice representation, including the nil-versus-empty shape of the
// allowed-tools subset, is compared exactly and never normalized.
func TestReassertFailsClosedOnToolChoiceShapeDrift(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	tests := []struct {
		name   string
		call   lipapi.Call
		mutate func(lipapi.Call) lipapi.Call
	}{
		{
			name: "absent allowed-tools subset became an explicit empty subset",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				call.ToolChoice.AllowedTools = []string{}
				return call
			},
		},
		{
			name: "explicit empty allowed-tools subset became absent",
			call: withChoice(projectionMessageCall(), lipapi.ToolChoice{AllowedTools: []string{}}),
			mutate: func(call lipapi.Call) lipapi.Call {
				call.ToolChoice.AllowedTools = nil
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected, projection, err := Project(test.call, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			drifted := test.mutate(projected)

			got, err := Reassert(drifted, projection)
			if !errors.Is(err, ErrProjectionConflict) {
				t.Fatalf("Reassert() error = %v, want ErrProjectionConflict", err)
			}
			if !reflect.DeepEqual(got, drifted) {
				t.Fatal("failed reassertion changed the candidate call")
			}
		})
	}
}

// projectionShapeCall exercises every nil-versus-empty shape the projection
// candidate copy must preserve: an explicitly empty instruction history, an
// empty-but-non-nil schema and metadata map, empty content part and raw-message
// carriers, and empty slices that still carry backing-array capacity.
func projectionShapeCall() lipapi.Call {
	temperature := 0.5
	return lipapi.Call{
		ID: "call-shape",
		Session: lipapi.SessionRef{
			ALegID:                 "a-leg-1",
			AuthoritativeSessionID: "sess-1",
			Metadata:               map[string]string{},
		},
		Instructions: []lipapi.Message{
			{
				Role: lipapi.RoleSystem,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "client system prompt", Content: make(json.RawMessage, 0, 4)},
				},
				Metadata: map[string]string{},
			},
		},
		Messages: make([]lipapi.Message, 0, 4),
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{
						Kind:       lipapi.ContentPartText,
						Text:       "do the work",
						Annotation: &lipapi.AnnotationPart{Type: "note", Data: json.RawMessage{}},
						Extension:  &lipapi.ExtensionContentPart{Namespace: "acme", Type: "input_file", Data: json.RawMessage{}},
					},
					{Kind: lipapi.ContentPartText, Text: "and keep going"},
				},
			},
			{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: make([]lipapi.ContentPart, 0, 4)},
			{Kind: lipapi.ItemKindToolCall, ToolCall: &lipapi.ToolCallItem{CallID: "call-1", Name: "client_tool", Arguments: json.RawMessage{}}},
			{Kind: lipapi.ItemKindToolResult, ToolResult: &lipapi.ToolResultItem{CallID: "call-1", Name: "client_tool", Parts: make([]lipapi.ContentPart, 0, 4)}},
			{Kind: lipapi.ItemKindCompaction, Compaction: &lipapi.CompactionItem{EncapsulatedID: "cmp-1", Opaque: json.RawMessage{}}},
			{Kind: lipapi.ItemKindExtension, Extension: &lipapi.OpaqueExtension{Namespace: "acme", Type: "state", Data: json.RawMessage{}}},
		},
		SemanticExtensions: []lipapi.SemanticExtension{{
			Namespace:   "acme",
			Type:        "state",
			Implementor: "proxy",
			Direction:   "request",
			Presence:    lipapi.SemanticExtensionValue,
			Data:        json.RawMessage{},
		}},
		Tools:      []lipapi.ToolDef{{Name: "client_tool", Description: "Client-declared tool.", Parameters: json.RawMessage{}}},
		ToolChoice: lipapi.ToolChoice{AllowedTools: []string{}},
		Options:    lipapi.GenerationOptions{Temperature: &temperature},
		Extensions: map[string]json.RawMessage{},
	}
}

// TestCloneForProjectionPreservesNilAndEmptyShapes proves the projection
// candidate copy is a value-identical copy of every untouched shape, and that no
// map, byte carrier, or spare-capacity backing array is shared with the input.
func TestCloneForProjectionPreservesNilAndEmptyShapes(t *testing.T) {
	t.Parallel()
	in := projectionShapeCall()
	before := projectionShapeCall()

	out := cloneForProjection(in)
	shapes := []struct {
		name string
		got  any
		want any
	}{
		{"Instructions", out.Instructions, in.Instructions},
		{"Instructions[0].Parts[0].Content", out.Instructions[0].Parts[0].Content, in.Instructions[0].Parts[0].Content},
		{"Instructions[0].Metadata", out.Instructions[0].Metadata, in.Instructions[0].Metadata},
		{"Messages", out.Messages, in.Messages},
		{"Items", out.Items, in.Items},
		{"Items[0].Content", out.Items[0].Content, in.Items[0].Content},
		{"Items[0].Content[0].Annotation.Data", out.Items[0].Content[0].Annotation.Data, in.Items[0].Content[0].Annotation.Data},
		{"Items[0].Content[0].Extension.Data", out.Items[0].Content[0].Extension.Data, in.Items[0].Content[0].Extension.Data},
		{"Items[1].Content", out.Items[1].Content, in.Items[1].Content},
		{"Items[2].ToolCall.Arguments", out.Items[2].ToolCall.Arguments, in.Items[2].ToolCall.Arguments},
		{"Items[3].ToolResult.Parts", out.Items[3].ToolResult.Parts, in.Items[3].ToolResult.Parts},
		{"Items[4].Compaction.Opaque", out.Items[4].Compaction.Opaque, in.Items[4].Compaction.Opaque},
		{"Items[5].Extension.Data", out.Items[5].Extension.Data, in.Items[5].Extension.Data},
		{"SemanticExtensions", out.SemanticExtensions, in.SemanticExtensions},
		{"SemanticExtensions[0].Data", out.SemanticExtensions[0].Data, in.SemanticExtensions[0].Data},
		{"Tools", out.Tools, in.Tools},
		{"Tools[0].Parameters", out.Tools[0].Parameters, in.Tools[0].Parameters},
		{"ToolChoice.AllowedTools", out.ToolChoice.AllowedTools, in.ToolChoice.AllowedTools},
		{"Session.Metadata", out.Session.Metadata, in.Session.Metadata},
		{"Extensions", out.Extensions, in.Extensions},
	}
	for _, shape := range shapes {
		if !reflect.DeepEqual(shape.got, shape.want) {
			t.Errorf("cloneForProjection() %s = %#v, want %#v", shape.name, shape.got, shape.want)
		}
	}

	out.Extensions["leak"] = json.RawMessage(`1`)
	out.Session.Metadata["leak"] = "leak"
	out.Instructions[0].Metadata["leak"] = "leak"
	out.Instructions[0].Parts[0].Content = append(out.Instructions[0].Parts[0].Content, 'x')
	out.Items[0].Content[0].Annotation.Data = append(out.Items[0].Content[0].Annotation.Data, 'x')
	out.Items[0].Content[0].Extension.Data = append(out.Items[0].Content[0].Extension.Data, 'x')
	out.Items[1].Content = append(out.Items[1].Content, lipapi.ContentPart{Text: "leak"})
	out.Items[2].ToolCall.Arguments = append(out.Items[2].ToolCall.Arguments, 'x')
	out.Items[3].ToolResult.Parts = append(out.Items[3].ToolResult.Parts, lipapi.ContentPart{Text: "leak"})
	out.Items[4].Compaction.Opaque = append(out.Items[4].Compaction.Opaque, 'x')
	out.Items[5].Extension.Data = append(out.Items[5].Extension.Data, 'x')
	out.Tools[0].Parameters = append(out.Tools[0].Parameters, 'x')
	out.ToolChoice.AllowedTools = append(out.ToolChoice.AllowedTools, "leak")
	out.SemanticExtensions[0].Data = append(out.SemanticExtensions[0].Data, 'x')
	out.Messages = append(out.Messages, lipapi.Message{Role: lipapi.RoleUser})

	// Appends must not reach through the copy into spare capacity the input
	// still owns, so the untouched window of every spare-capacity carrier in the
	// input must stay zeroed.
	windows := []struct {
		name  string
		got   any
		clear any
	}{
		{"Messages", in.Messages[:cap(in.Messages)], make([]lipapi.Message, cap(in.Messages))},
		{"Items[1].Content", in.Items[1].Content[:cap(in.Items[1].Content)], make([]lipapi.ContentPart, cap(in.Items[1].Content))},
		{"Items[3].ToolResult.Parts", in.Items[3].ToolResult.Parts[:cap(in.Items[3].ToolResult.Parts)], make([]lipapi.ContentPart, cap(in.Items[3].ToolResult.Parts))},
		{"Instructions[0].Parts[0].Content", in.Instructions[0].Parts[0].Content[:cap(in.Instructions[0].Parts[0].Content)], make(json.RawMessage, cap(in.Instructions[0].Parts[0].Content))},
	}
	for _, window := range windows {
		if !reflect.DeepEqual(window.got, window.clear) {
			t.Errorf("appending to the copied %s clobbered the input backing array: %#v", window.name, window.got)
		}
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("mutating the projection candidate copy changed the input call:\ngot  %+v\nwant %+v", in, before)
	}
}

// TestProjectionPreservesUntouchedClientShapes proves the shapes survive the
// real projection points, not just the copy helper: an explicitly empty client
// extension map stays non-nil and independent, an empty-but-non-nil ordinary tool
// schema is never normalized to nil, and an explicitly empty item slice with
// spare capacity keeps its own backing array.
func TestProjectionPreservesUntouchedClientShapes(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	in := projectionMessageCall()
	in.Extensions = map[string]json.RawMessage{}
	in.Tools = []lipapi.ToolDef{
		{Name: "client_tool", Description: "Client-declared tool.", Parameters: json.RawMessage{}},
		projectionClientTool(),
	}

	projected, projection, err := Project(in, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if projected.Extensions == nil {
		t.Error("Project() normalized the explicitly empty client extension map to nil")
	}
	if projected.Tools[0].Parameters == nil {
		t.Error("Project() normalized an empty ordinary tool schema to nil")
	}
	if err := projected.Validate(); err != nil {
		t.Fatalf("projected call is canonically invalid: %v", err)
	}

	restored, err := Reassert(projected, projection)
	if err != nil {
		t.Fatalf("Reassert() error = %v", err)
	}
	if !reflect.DeepEqual(restored, projected) {
		t.Fatalf("Reassert() output = %+v, want the approved projection %+v", restored, projected)
	}

	projected.Extensions["leak"] = json.RawMessage(`1`)
	projected.Tools[0].Parameters = append(projected.Tools[0].Parameters, 'x')
	if len(in.Extensions) != 0 {
		t.Fatalf("mutating the projected call changed the input extension map: %+v", in.Extensions)
	}
	if in.Tools[0].Parameters == nil || len(in.Tools[0].Parameters) != 0 {
		t.Fatalf("mutating the projected call changed the input tool schema: %q", in.Tools[0].Parameters)
	}

	continuation := projectionContinuationCall()
	continuation.Items = make([]lipapi.Item, 0, 4)
	continuationProjected, continuationProjection, err := Project(continuation, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() continuation error = %v", err)
	}
	continuationRestored, err := Reassert(continuationProjected, continuationProjection)
	if err != nil {
		t.Fatalf("Reassert() continuation error = %v", err)
	}
	continuationRestored.Items = append(continuationRestored.Items, lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		Role:    lipapi.RoleUser,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "late history"}},
	})
	if !reflect.DeepEqual(continuation.Items[:cap(continuation.Items)], make([]lipapi.Item, cap(continuation.Items))) {
		t.Fatalf("appending to the reasserted trajectory changed the input backing array: %+v", continuation.Items[:cap(continuation.Items)])
	}

	// A populated client extension map must be owned byte for byte too, including
	// the nil shape of an explicitly valueless key.
	populated := projectionMessageCall()
	populated.Extensions = map[string]json.RawMessage{"client.state": json.RawMessage(`{"k":1}`), "client.absent": nil}
	populatedProjected, _, err := Project(populated, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() populated extensions error = %v", err)
	}
	populatedProjected.Extensions["client.state"][0] = 'X'
	populatedProjected.Extensions["added"] = json.RawMessage(`1`)
	if !bytes.Equal(populated.Extensions["client.state"], []byte(`{"k":1}`)) {
		t.Fatalf("mutating the projected extension value changed the input: %q", populated.Extensions["client.state"])
	}
	if len(populated.Extensions) != 2 {
		t.Fatalf("mutating the projected extension map changed the input: %+v", populated.Extensions)
	}
	if _, present := populatedProjected.Extensions["client.absent"]; !present || populatedProjected.Extensions["client.absent"] != nil {
		t.Fatalf("projected extension nil shape changed: %+v", populatedProjected.Extensions)
	}
}

func TestSameToolChoiceRequiresExactRepresentation(t *testing.T) {
	t.Parallel()
	omitted := lipapi.ToolChoice{}
	auto := lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}
	tests := []struct {
		name string
		got  lipapi.ToolChoice
		want lipapi.ToolChoice
		same bool
	}{
		{name: "identical omitted choices agree", got: omitted, want: omitted, same: true},
		{name: "identical explicit auto choices agree", got: auto, want: auto, same: true},
		{name: "omitted mode is not explicit auto", got: omitted, want: auto, same: false},
		{name: "explicit auto is not an omitted mode", got: auto, want: omitted, same: false},
		{name: "nil subset is not an explicit empty subset", got: lipapi.ToolChoice{AllowedTools: []string{}}, want: omitted, same: false},
		{name: "explicit empty subset is not nil", got: omitted, want: lipapi.ToolChoice{AllowedTools: []string{}}, same: false},
		{name: "identical subsets agree", got: lipapi.ToolChoice{AllowedTools: []string{"a", "b"}}, want: lipapi.ToolChoice{AllowedTools: []string{"a", "b"}}, same: true},
		{name: "subset order matters", got: lipapi.ToolChoice{AllowedTools: []string{"a", "b"}}, want: lipapi.ToolChoice{AllowedTools: []string{"b", "a"}}, same: false},
		{name: "subset members must match", got: lipapi.ToolChoice{AllowedTools: []string{"a", "b"}}, want: lipapi.ToolChoice{AllowedTools: []string{"a", "c"}}, same: false},
		{name: "subset length must match", got: lipapi.ToolChoice{AllowedTools: []string{"a"}}, want: lipapi.ToolChoice{AllowedTools: []string{"a", "b"}}, same: false},
		{name: "named requirement differs", got: lipapi.ToolChoice{Name: "client_tool"}, want: omitted, same: false},
		{name: "mode differs", got: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, want: auto, same: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := sameToolChoice(test.got, test.want); got != test.same {
				t.Fatalf("sameToolChoice(%+v, %+v) = %v, want %v", test.got, test.want, got, test.same)
			}
		})
	}
}

func TestReassertFailsClosedOnRelocatedItemAuthority(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	projected, projection, err := Project(projectionItemCall(), spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	relocated := projected
	relocated.Items = append([]lipapi.Item(nil), projected.Items...)
	relocated.Items[0], relocated.Items[1] = relocated.Items[1], relocated.Items[0]

	got, err := Reassert(relocated, projection)
	if !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("Reassert() error = %v, want ErrProjectionConflict", err)
	}
	if !reflect.DeepEqual(got, relocated) {
		t.Fatal("failed reassertion changed the candidate call")
	}
}

func TestReassertRejectsInactiveProjection(t *testing.T) {
	t.Parallel()
	in := projectionMessageCall()
	before := lipapi.CloneCall(in)

	_, inactive, err := Project(in, validSpec(), lipapi.NewBackendCaps(lipapi.CapabilityStreaming))
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if inactive.Reason() != ReasonBackendToolsUnsupported {
		t.Fatalf("Projection.Reason() = %q, want %q", inactive.Reason(), ReasonBackendToolsUnsupported)
	}
	got, err := Reassert(in, inactive)
	if !errors.Is(err, ErrProjectionInactive) {
		t.Fatalf("Reassert() error = %v, want ErrProjectionInactive", err)
	}
	if !reflect.DeepEqual(got, in) || !reflect.DeepEqual(in, before) {
		t.Fatal("failed reassertion changed the candidate call")
	}

	got, err = Reassert(in, Projection{})
	if !errors.Is(err, ErrProjectionInactive) {
		t.Fatalf("Reassert(Projection{}) error = %v, want ErrProjectionInactive", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatal("failed reassertion changed the candidate call")
	}
}

func TestProjectRejectsFalseClientSameNameCollision(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	in := withTool(projectionMessageCall(), spec.Tool)
	before := lipapi.CloneCall(in)

	got, projection, err := Project(in, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if projection.Active() || projection.Reason() != ReasonToolNameCollision {
		t.Fatalf("Projection = active %v reason %q, want inactive %q", projection.Active(), projection.Reason(), ReasonToolNameCollision)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatal("client-owned same-name tool was adopted or mutated")
	}
	if len(got.Tools) != 2 {
		t.Fatalf("Tools length = %d, want the two untouched client tools", len(got.Tools))
	}
	if len(got.Instructions) != 1 {
		t.Fatalf("Instructions length = %d, want no control instruction", len(got.Instructions))
	}

	// Idempotence belongs to Reassert: a fresh initial projection never adopts an
	// existing same-named declaration, not even one it produced itself.
	projected, first, err := Project(projectionMessageCall(), spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() first error = %v", err)
	}
	if !first.Active() {
		t.Fatalf("first projection inactive: %q", first.Reason())
	}
	again, second, err := Project(projected, spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() second error = %v", err)
	}
	if second.Active() || second.Reason() != ReasonToolNameCollision {
		t.Fatalf("second projection = active %v reason %q, want inactive %q", second.Active(), second.Reason(), ReasonToolNameCollision)
	}
	if !reflect.DeepEqual(again, projected) {
		t.Fatal("second projection duplicated the control tool or instruction")
	}
	once, err := Reassert(projected, first)
	if err != nil {
		t.Fatalf("Reassert() error = %v", err)
	}
	if !reflect.DeepEqual(once, projected) {
		t.Fatal("Reassert() of a first projection is not idempotent")
	}
}

// TestReassertFailsClosedOnNewOrContradictingInstruction proves the projected
// instruction envelope is proved in full, not by a leading-window check: an
// instruction inserted anywhere in the envelope, or replacing the envelope
// entirely, must fail closed even when the approved control message is still
// present and at the head.
func TestReassertFailsClosedOnNewOrContradictingInstruction(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	tests := []struct {
		name   string
		call   lipapi.Call
		mutate func(lipapi.Call) lipapi.Call
	}{
		{
			name: "message authority contradicting instruction appended behind the control message",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				call.Instructions = append(instructions, projectionContraryMessage())
				return call
			},
		},
		{
			name: "item authority new leading system item inserted behind the control item",
			call: projectionPrefixedItemCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				call.Items = slices.Insert(items, 1, projectionContraryItem(lipapi.RoleSystem))
				return call
			},
		},
		{
			name: "item authority contradicting developer item appended after the control item",
			call: projectionInstructionOnlyItemCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				call.Items = append(items, projectionContraryItem(lipapi.RoleDeveloper))
				return call
			},
		},
		{
			name: "empty item continuation gained a contradicting system item",
			call: projectionContinuationCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				call.Items = append(items, projectionContraryItem(lipapi.RoleSystem))
				return call
			},
		},
		{
			name: "empty item continuation lost the control item and gained a contradicting system item",
			call: projectionContinuationCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				call.Items = []lipapi.Item{projectionContraryItem(lipapi.RoleSystem)}
				return call
			},
		},
		{
			name: "instruction-only item prefix lost its developer item for a contradicting one",
			call: projectionInstructionOnlyItemCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items[1:]...)
				call.Items = append(items, projectionContraryItem(lipapi.RoleDeveloper))
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected, projection, err := Project(test.call, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if !projection.Active() {
				t.Fatalf("Projection inactive: %q", projection.Reason())
			}
			drifted := test.mutate(projected)
			before := snapshotCall(drifted)

			got, err := Reassert(drifted, projection)
			if !errors.Is(err, ErrProjectionConflict) {
				t.Fatalf("Reassert() error = %v, want ErrProjectionConflict", err)
			}
			if !reflect.DeepEqual(got, drifted) {
				t.Fatal("failed reassertion changed the candidate call")
			}
			if !reflect.DeepEqual(drifted, before) {
				t.Fatal("failed reassertion mutated its input")
			}
		})
	}
}

// TestReassertFailsClosedOnRewrittenOriginalInstructions proves the frozen
// original instruction envelope itself is proof, not just a window: replacing
// the client's own instructions, with or without the control instruction
// surviving, can never be proved equivalent and therefore fails closed.
func TestReassertFailsClosedOnRewrittenOriginalInstructions(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	tests := []struct {
		name   string
		call   lipapi.Call
		mutate func(lipapi.Call) lipapi.Call
	}{
		{
			name: "message authority original instruction rewritten behind the control message",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				instructions[1] = lipapi.Message{
					Role:  lipapi.RoleSystem,
					Parts: []lipapi.Part{lipapi.TextPart("reconstructed client system prompt")},
				}
				call.Instructions = instructions
				return call
			},
		},
		{
			name: "message authority original instruction rewritten after the control message was removed",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				call.Instructions = []lipapi.Message{{
					Role:  lipapi.RoleSystem,
					Parts: []lipapi.Part{lipapi.TextPart("reconstructed client system prompt")},
				}}
				return call
			},
		},
		{
			name: "item authority original system item rewritten behind the control item",
			call: projectionPrefixedItemCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				items[1].Content = []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "reconstructed client system prefix"}}
				call.Items = items
				return call
			},
		},
		{
			name: "item authority original instruction run replaced after the control item was removed",
			call: projectionInstructionOnlyItemCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				call.Items = []lipapi.Item{projectionContraryItem(lipapi.RoleSystem)}
				return call
			},
		},
		{
			name: "item authority original anchor replaced after the control item was removed",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call) lipapi.Call {
				call.Items = []lipapi.Item{{
					Kind:    lipapi.ItemKindMessage,
					Role:    lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "reconstructed user turn"}},
				}}
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected, projection, err := Project(test.call, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if !projection.Active() {
				t.Fatalf("Projection inactive: %q", projection.Reason())
			}
			drifted := test.mutate(projected)
			before := snapshotCall(drifted)

			got, err := Reassert(drifted, projection)
			if !errors.Is(err, ErrProjectionConflict) {
				t.Fatalf("Reassert() error = %v, want ErrProjectionConflict", err)
			}
			if !reflect.DeepEqual(got, drifted) {
				t.Fatal("failed reassertion changed the candidate call")
			}
			if !reflect.DeepEqual(drifted, before) {
				t.Fatal("failed reassertion mutated its input")
			}
		})
	}
}

// TestReassertRestoresMissingControlMessage proves a missing control message is
// restored when the entire original instruction envelope is still provably the
// client's own, under both nil and explicitly empty original instructions.
func TestReassertRestoresMissingControlMessage(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	emptyInstructions := func(id string, explicit bool) lipapi.Call {
		call := projectionMessageCall()
		call.ID = id
		if explicit {
			call.Instructions = []lipapi.Message{}
			return call
		}
		call.Instructions = nil
		return call
	}

	tests := []struct {
		name string
		call lipapi.Call
	}{
		{name: "absent original instructions", call: emptyInstructions("call-9", false)},
		{name: "explicitly empty original instructions", call: emptyInstructions("call-10", true)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			projected, projection, err := Project(test.call, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			stripped := projected
			stripped.Instructions = nil
			if test.name == "explicitly empty original instructions" {
				stripped.Instructions = []lipapi.Message{}
			}

			restored, err := Reassert(stripped, projection)
			if err != nil {
				t.Fatalf("Reassert() error = %v, want the approved control message restored", err)
			}
			if !reflect.DeepEqual(restored.Instructions, projected.Instructions) {
				t.Fatalf("Reassert() Instructions = %+v, want the approved control message %+v", restored.Instructions, projected.Instructions)
			}
			if !reflect.DeepEqual(restored, projected) {
				t.Fatalf("Reassert() output = %+v, want the approved projection %+v", restored, projected)
			}
			if err := restored.Validate(); err != nil {
				t.Fatalf("restored call is canonically invalid: %v", err)
			}
		})
	}
}

// TestReassertRestoresMissingControlItemForEmptyContinuation proves an empty
// item-authority continuation is only restorable while its own Items slice is
// still empty, and that a reconstructed trailing history behind the retained
// control item stays legal because it is history, not instruction scope.
func TestReassertRestoresMissingControlItemForEmptyContinuation(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	projected, projection, err := Project(projectionContinuationCall(), spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}

	restored, err := Reassert(projected, projection)
	if err != nil {
		t.Fatalf("Reassert() error = %v, want the approved continuation projection", err)
	}
	if !reflect.DeepEqual(restored, projected) {
		t.Fatalf("Reassert() output = %+v, want the approved projection %+v", restored, projected)
	}

	// Ordinary non-instruction history behind the retained control item is
	// reconstructible, so it must not be rejected and must not be adopted.
	trailing := lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		Role:    lipapi.RoleUser,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "reconstructed turn"}},
	}
	extended := projected
	extended.Items = append(append([]lipapi.Item(nil), projected.Items...), trailing)
	restored, err = Reassert(extended, projection)
	if err != nil {
		t.Fatalf("Reassert() error = %v, want reconstructed history accepted", err)
	}
	if !reflect.DeepEqual(restored.Items, extended.Items) {
		t.Fatalf("Reassert() Items = %+v, want the unchanged trajectory %+v", restored.Items, extended.Items)
	}
	if err := restored.Validate(); err != nil {
		t.Fatalf("restored call is canonically invalid: %v", err)
	}
}

// TestProjectStaysInactiveOnExactClientControlInstruction proves the initial
// projection treats an exact client declaration of the spec control instruction
// as ambiguous ownership rather than adopting it or duplicating it. A
// role/text/metadata look-alike stays ordinary client data, and a look-alike in
// the later trajectory is not a trusted match either.
func TestProjectStaysInactiveOnExactClientControlInstruction(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	tests := []struct {
		name   string
		call   lipapi.Call
		mutate func(lipapi.Call, Spec) lipapi.Call
	}{
		{
			name: "message authority already declares the exact control instruction",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				call.Instructions = append(instructions, controlMessageOf(spec))
				return call
			},
		},
		{
			name: "item authority already declares the exact control item",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				call.Items = append(items, controlItemOf(spec))
				return call
			},
		},
		{
			name: "item authority declares the exact control item in the later trajectory",
			call: projectionPrefixedItemCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				call.Items = append(items, controlItemOf(spec))
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			in := test.mutate(test.call, spec)
			before := snapshotCall(in)

			got, projection, err := Project(in, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if projection.Active() {
				t.Fatalf("Projection is active for a client-owned control instruction")
			}
			if projection.Reason() != ReasonInstructionCollision {
				t.Fatalf("Projection.Reason() = %q, want %q", projection.Reason(), ReasonInstructionCollision)
			}
			if err := validateReasonCode(projection.Reason()); err != nil {
				t.Fatalf("inactive reason %q is not bounded/content-free: %v", projection.Reason(), err)
			}
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("Project() changed the candidate call: got %+v want %+v", got, before)
			}
			if !reflect.DeepEqual(projection.Tool(), lipapi.ToolDef{}) || projection.MaxArgsBytes() != 0 {
				t.Fatalf("inactive projection carries approved state: %+v", projection)
			}
			if _, err := Reassert(got, projection); !errors.Is(err, ErrProjectionInactive) {
				t.Fatalf("Reassert() error = %v, want ErrProjectionInactive", err)
			}
		})
	}
}

// TestProjectActivatesForClientInstructionLookAlike proves only an exact
// declaration is a collision: a client instruction that merely resembles the
// control instruction in text, role, or metadata is ordinary client data, so the
// optional control feature still activates and the look-alike is never adopted.
func TestProjectActivatesForClientInstructionLookAlike(t *testing.T) {
	t.Parallel()
	spec := validSpec()

	tests := []struct {
		name   string
		call   lipapi.Call
		mutate func(lipapi.Call, Spec) lipapi.Call
	}{
		{
			name: "client instruction under a different role",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				call.Instructions = append(instructions, lipapi.Message{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{lipapi.TextPart(spec.Instruction.Text)},
				})
				return call
			},
		},
		{
			name: "client instruction with extended control text",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				call.Instructions = append(instructions, lipapi.Message{
					Role:  spec.Instruction.Role,
					Parts: []lipapi.Part{lipapi.TextPart(spec.Instruction.Text + " Always.")},
				})
				return call
			},
		},
		{
			name: "client instruction with control text under client metadata",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				declared := controlMessageOf(spec)
				declared.Metadata = map[string]string{"client.owner": "acme"}
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				call.Instructions = append(instructions, declared)
				return call
			},
		},
		{
			name: "client instruction with control text in two parts",
			call: projectionMessageCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				instructions := append([]lipapi.Message(nil), call.Instructions...)
				call.Instructions = append(instructions, lipapi.Message{
					Role: spec.Instruction.Role,
					Parts: []lipapi.Part{
						{Kind: lipapi.PartText, Text: spec.Instruction.Text},
						lipapi.TextPart("Always."),
					},
				})
				return call
			},
		},
		{
			name: "client item with control text and a client item reference",
			call: projectionItemCall(),
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				items := append([]lipapi.Item(nil), call.Items...)
				call.Items = append(items, lipapi.Item{
					Kind:    lipapi.ItemKindMessage,
					Role:    spec.Instruction.Role,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: spec.Instruction.Text}},
					Reference: &lipapi.ItemReference{
						ID: "client-item",
					},
				})
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			in := test.mutate(test.call, spec)
			before := snapshotCall(in)

			got, projection, err := Project(in, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if !projection.Active() {
				t.Fatalf("Projection inactive for a client-owned look-alike: %q", projection.Reason())
			}
			if got.HasItemAuthority() != in.HasItemAuthority() {
				t.Fatal("projection changed the trajectory authority")
			}
			restored, err := Reassert(got, projection)
			if err != nil {
				t.Fatalf("Reassert() error = %v", err)
			}
			if !reflect.DeepEqual(restored, got) {
				t.Fatalf("Reassert() output = %+v, want the approved projection %+v", restored, got)
			}
			if !reflect.DeepEqual(in, before) {
				t.Fatal("Project() mutated the input call")
			}
		})
	}
}

// projectionSpecWithRole returns the shared valid spec with the control
// instruction declared under the requested model-visible message-authority role.
func projectionSpecWithRole(role lipapi.Role) Spec {
	spec := validSpec()
	spec.Instruction.Role = role
	return spec
}

// projectionControlHistoryCall is message authority whose mutable history already
// carries an exact client copy of the spec control instruction, either at the
// head of the history or behind ordinary turns. Both system and developer are
// model-visible message-authority roles, so such a copy is as ambiguous as the
// same copy in Instructions.
func projectionControlHistoryCall(id string, spec Spec, later bool) lipapi.Call {
	call := projectionMessageCall()
	call.ID = id
	control := controlMessageOf(spec)
	if later {
		call.Messages = append(append([]lipapi.Message(nil), call.Messages...), control)
		return call
	}
	call.Messages = append([]lipapi.Message{control}, call.Messages...)
	return call
}

// TestProjectStaysInactiveOnExactClientControlMessageInHistory proves the
// instruction-collision scan covers both message authorities: an exact client
// declaration of the spec control instruction in the message history is an
// ambiguous ownership condition, not a place where a second model-visible copy
// may be appended.
func TestProjectStaysInactiveOnExactClientControlMessageInHistory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		role  lipapi.Role
		later bool
	}{
		{name: "system control copy at the head of the history", role: lipapi.RoleSystem},
		{name: "system control copy later in the history", role: lipapi.RoleSystem, later: true},
		{name: "developer control copy at the head of the history", role: lipapi.RoleDeveloper},
		{name: "developer control copy later in the history", role: lipapi.RoleDeveloper, later: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := projectionSpecWithRole(test.role)
			in := projectionControlHistoryCall("call-history", spec, test.later)
			if err := in.Validate(); err != nil {
				t.Fatalf("candidate fixture is canonically invalid: %v", err)
			}
			before := snapshotCall(in)

			got, projection, err := Project(in, spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v, want no error for an ambiguous client declaration", err)
			}
			if projection.Active() {
				t.Fatal("Projection is active for a client-owned control instruction in the message history")
			}
			if projection.Reason() != ReasonInstructionCollision {
				t.Fatalf("Projection.Reason() = %q, want %q", projection.Reason(), ReasonInstructionCollision)
			}
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("Project() changed the candidate call: got %+v want %+v", got, before)
			}
			if !reflect.DeepEqual(in, before) {
				t.Fatal("Project() mutated the input call")
			}
		})
	}
}

// TestReassertFailsClosedOnControlMessageInHistory proves reassertion scans the
// mutable message history for the owned control instruction before it restores
// or replays anything: a copy relocated out of Instructions or duplicated into
// the history is a second model-visible projection and fails closed.
func TestReassertFailsClosedOnControlMessageInHistory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		role   lipapi.Role
		mutate func(lipapi.Call, Spec) lipapi.Call
	}{
		{
			name: "system control instruction relocated into the message history",
			role: lipapi.RoleSystem,
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				relocated := call.Instructions[0]
				call.Instructions = append([]lipapi.Message(nil), call.Instructions[1:]...)
				call.Messages = append([]lipapi.Message{relocated}, call.Messages...)
				return call
			},
		},
		{
			name: "system control instruction duplicated into the message history",
			role: lipapi.RoleSystem,
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				call.Messages = append(append([]lipapi.Message(nil), call.Messages...), controlMessageOf(spec))
				return call
			},
		},
		{
			name: "developer control instruction relocated into the message history",
			role: lipapi.RoleDeveloper,
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				relocated := call.Instructions[0]
				call.Instructions = append([]lipapi.Message(nil), call.Instructions[1:]...)
				call.Messages = append([]lipapi.Message{relocated}, call.Messages...)
				return call
			},
		},
		{
			name: "developer control instruction duplicated into the message history",
			role: lipapi.RoleDeveloper,
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				call.Messages = append(append([]lipapi.Message(nil), call.Messages...), controlMessageOf(spec))
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := projectionSpecWithRole(test.role)
			projected, projection, err := Project(projectionMessageCall(), spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if !projection.Active() {
				t.Fatalf("Projection inactive: %q", projection.Reason())
			}
			drifted := test.mutate(projected, spec)
			if err := drifted.Validate(); err != nil {
				t.Fatalf("drifted candidate fixture is canonically invalid: %v", err)
			}
			before := snapshotCall(drifted)

			got, err := Reassert(drifted, projection)
			if !errors.Is(err, ErrProjectionConflict) {
				t.Fatalf("Reassert() error = %v, want ErrProjectionConflict", err)
			}
			if !reflect.DeepEqual(got, drifted) {
				t.Fatal("failed reassertion changed the candidate call")
			}
			if !reflect.DeepEqual(drifted, before) {
				t.Fatal("failed reassertion mutated its input")
			}
		})
	}
}

// TestReassertKeepsOrdinaryMessageHistoryReconstructible proves the message
// history stays the client's to reconstruct: ordinary turns may be appended,
// reordered, or rewritten, a non-exact control-text look-alike stays ordinary
// data, and only a genuine removal of the control instruction from both message
// containers is restored, under both control-instruction roles.
func TestReassertKeepsOrdinaryMessageHistoryReconstructible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		role     lipapi.Role
		restores bool
		mutate   func(lipapi.Call, Spec) lipapi.Call
	}{
		{
			name: "ordinary history appended",
			role: lipapi.RoleSystem,
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				call.Messages = append(append([]lipapi.Message(nil), call.Messages...),
					lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("done so far")}})
				return call
			},
		},
		{
			name: "ordinary history reordered",
			role: lipapi.RoleSystem,
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				messages := append([]lipapi.Message(nil), call.Messages...)
				messages[0], messages[len(messages)-1] = messages[len(messages)-1], messages[0]
				call.Messages = messages
				return call
			},
		},
		{
			name: "ordinary history rewritten",
			role: lipapi.RoleSystem,
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				messages := append([]lipapi.Message(nil), call.Messages...)
				messages[0] = lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("do the rest")}}
				call.Messages = messages
				return call
			},
		},
		{
			name: "control text look-alike in the history under a client role",
			role: lipapi.RoleDeveloper,
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				call.Messages = append(append([]lipapi.Message(nil), call.Messages...), lipapi.Message{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{lipapi.TextPart(spec.Instruction.Text)},
				})
				return call
			},
		},
		{
			name: "control text look-alike in the history under client metadata",
			role: lipapi.RoleDeveloper,
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				declared := controlMessageOf(spec)
				declared.Metadata = map[string]string{"client.owner": "acme"}
				call.Messages = append(append([]lipapi.Message(nil), call.Messages...), declared)
				return call
			},
		},
		{
			name: "control text look-alike in the history with extended text",
			role: lipapi.RoleSystem,
			mutate: func(call lipapi.Call, spec Spec) lipapi.Call {
				call.Messages = append(append([]lipapi.Message(nil), call.Messages...), lipapi.Message{
					Role:  spec.Instruction.Role,
					Parts: []lipapi.Part{lipapi.TextPart(spec.Instruction.Text + " Always.")},
				})
				return call
			},
		},
		{
			name:     "genuine control instruction removed from both message containers",
			role:     lipapi.RoleSystem,
			restores: true,
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				call.Instructions = append([]lipapi.Message(nil), call.Instructions[1:]...)
				return call
			},
		},
		{
			name:     "genuine developer control instruction removed from both message containers",
			role:     lipapi.RoleDeveloper,
			restores: true,
			mutate: func(call lipapi.Call, _ Spec) lipapi.Call {
				call.Instructions = append([]lipapi.Message(nil), call.Instructions[1:]...)
				return call
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := projectionSpecWithRole(test.role)
			projected, projection, err := Project(projectionMessageCall(), spec, projectionToolsCaps())
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if !projection.Active() {
				t.Fatalf("Projection inactive: %q", projection.Reason())
			}
			drifted := test.mutate(projected, spec)
			if err := drifted.Validate(); err != nil {
				t.Fatalf("drifted candidate fixture is canonically invalid: %v", err)
			}
			before := snapshotCall(drifted)

			got, err := Reassert(drifted, projection)
			if err != nil {
				t.Fatalf("Reassert() error = %v, want the approved projection replayed", err)
			}
			want := drifted
			if test.restores {
				want = projected
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Reassert() output = %+v, want %+v", got, want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("reasserted call is canonically invalid: %v", err)
			}
			if !reflect.DeepEqual(drifted, before) {
				t.Fatal("Reassert() mutated its input")
			}
		})
	}
}

func TestProjectedInstructionCarriesNoPerTurnData(t *testing.T) {
	t.Parallel()
	spec := validSpec()
	first, _, err := Project(projectionMessageCall(), spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	second, _, err := Project(projectionMessageCall(), spec, projectionToolsCaps())
	if err != nil {
		t.Fatalf("Project() second error = %v", err)
	}
	if first.Instructions[0].Parts[0].Text != second.Instructions[0].Parts[0].Text {
		t.Fatal("projected instruction is not byte-stable across equivalent turns")
	}
	if first.Instructions[0].Metadata != nil {
		t.Fatalf("projected instruction carries metadata: %+v", first.Instructions[0].Metadata)
	}
	if len(first.Extensions) != 0 || len(first.SemanticExtensions) != 0 {
		t.Fatal("projection wrote ownership tags into client-writable call metadata")
	}
}
