package lipapi_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestDecisionCall_ExclusiveAuthority(t *testing.T) {
	t.Parallel()
	req := mkDecisionRequest()
	call := lipapi.Call{Decision: &req}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*lipapi.Call){
		"messages": func(c *lipapi.Call) {
			c.Messages = []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("text")}}}
		},
		"instructions": func(c *lipapi.Call) { c.Instructions = []lipapi.Message{{Role: lipapi.RoleSystem}} },
		"items":        func(c *lipapi.Call) { c.Items = []lipapi.Item{} },
		"continuation": func(c *lipapi.Call) { c.PreviousResponseID = "parent" },
		"tools":        func(c *lipapi.Call) { c.Tools = []lipapi.ToolDef{{Name: "tool"}} },
		"tool mode":    func(c *lipapi.Call) { c.ToolChoice.Mode = lipapi.ToolChoiceAuto },
		"tool name":    func(c *lipapi.Call) { c.ToolChoice.Name = "tool" },
		"tool subset":  func(c *lipapi.Call) { c.ToolChoice.AllowedTools = []string{"tool"} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := call
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("mixed decision authority accepted")
			}
		})
	}
	call.Invocation.Operation = lipapi.OperationOpenAIChatCompletions
	if err := call.Validate(); err != nil {
		t.Fatalf("validation depends on nonserialized invocation: %v", err)
	}
	if !slices.Contains(lipapi.RequiredCapabilities(call), lipapi.CapabilityDecisions) {
		t.Fatal("decision capability missing")
	}
	req.Evidence = json.RawMessage(`{`)
	if err := call.Validate(); err == nil {
		t.Fatal("invalid decision payload accepted")
	}
}

func TestDecisionCall_CloneIsolatesAllPayloads(t *testing.T) {
	t.Parallel()
	req := mkDecisionRequest()
	req.Questions[0].FalseCriteria = json.RawMessage(`"false"`)
	req.Questions[1].Options[0].Description = json.RawMessage(`"option"`)
	before := mkDecisionRequest()
	before.Questions[0].FalseCriteria = json.RawMessage(`"false"`)
	before.Questions[1].Options[0].Description = json.RawMessage(`"option"`)
	clone := lipapi.CloneCall(lipapi.Call{Decision: &req})
	clone.Decision.Evidence[0] = ' '
	clone.Decision.Questions[0].ID = "changed"
	clone.Decision.Questions[0].Instructions[0] = ' '
	clone.Decision.Questions[0].TrueCriteria[0] = ' '
	clone.Decision.Questions[0].FalseCriteria[0] = ' '
	clone.Decision.Questions[1].Options[0].Name = "changed"
	clone.Decision.Questions[1].Options[0].Description[0] = ' '
	clone.Decision.Questions[2].Levels[0][0] = ' '
	if !reflect.DeepEqual(req, before) {
		t.Fatal("clone mutated original decision")
	}
}

func TestDecisionEvent_OrderingAndCommitment(t *testing.T) {
	t.Parallel()
	result := mkDecisionResult()
	ev := lipapi.Event{Kind: lipapi.EventDecisionResult, Decision: &result}
	valid := []lipapi.Event{{Kind: lipapi.EventResponseStarted}, ev, {Kind: lipapi.EventUsageDelta}, {Kind: lipapi.EventResponseFinished}}
	if err := lipapi.ValidateEventSequence(valid); err != nil {
		t.Fatal(err)
	}
	if !lipapi.OutputCommitted(ev) || lipapi.OutputCommitted(valid[0]) {
		t.Fatal("result must commit output, lifecycle frame must not")
	}
	for name, events := range map[string][]lipapi.Event{
		"before started": {ev, {Kind: lipapi.EventResponseFinished}},
		"duplicate":      {valid[0], ev, ev, valid[3]},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := lipapi.ValidateEventSequence(events); err == nil {
				t.Fatal("invalid decision sequence accepted")
			}
		})
	}
	if err := lipapi.ValidateEventEnvelope(&ev); err != nil {
		t.Fatal(err)
	}
	ev.Decision = nil
	if err := lipapi.ValidateEventEnvelope(&ev); err == nil {
		t.Fatal("result event without payload accepted")
	}
	ev.Kind = lipapi.EventUsageDelta
	ev.Decision = &result
	if err := lipapi.ValidateEventEnvelope(&ev); err == nil {
		t.Fatal("decision payload on unrelated event accepted")
	}
}
