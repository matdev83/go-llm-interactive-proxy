package lipapi_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCloneCall_deepCopiesSlicesAndOptionPointers(t *testing.T) {
	t.Parallel()
	temp := 0.5
	parallel := true
	orig := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "x", Parameters: []byte(`{}`)}},
		Options: lipapi.GenerationOptions{
			Temperature:       &temp,
			ParallelToolCalls: &parallel,
			ReasoningEffort:   "high",
			Verbosity:         lipapi.VerbosityHigh,
		},
	}
	cl := lipapi.CloneCall(orig)
	cl.Messages[0].Parts[0].Text = "mutated"
	*cl.Options.Temperature = 0.1
	*cl.Options.ParallelToolCalls = false
	cl.Tools[0].Name = "y"

	if orig.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("messages mutated")
	}
	if *orig.Options.Temperature != 0.5 {
		t.Fatalf("temperature pointer shared")
	}
	if !*orig.Options.ParallelToolCalls {
		t.Fatalf("parallel pointer shared")
	}
	if orig.Tools[0].Name != "x" {
		t.Fatalf("tools slice shared")
	}
	if orig.Options.Verbosity != lipapi.VerbosityHigh {
		t.Fatalf("verbosity should be copied")
	}
}

func TestCloneCall_preservesNilReasoningOpaque(t *testing.T) {
	t.Parallel()
	orig := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartReasoning,
				Reasoning: &lipapi.ReasoningPart{
					Dialect: lipapi.ReasoningDialectOpenAIChatTextV1,
					Text:    "t",
					Opaque:  nil,
				},
			}},
		}},
	}
	cl := lipapi.CloneCall(orig)
	if cl.Messages[0].Parts[0].Reasoning == nil {
		t.Fatal("cloned Reasoning missing")
	}
	if cl.Messages[0].Parts[0].Reasoning.Opaque != nil {
		t.Fatalf("nil Opaque must stay nil, got %#v", cl.Messages[0].Parts[0].Reasoning.Opaque)
	}
}

func TestCloneCall_preservesEmptyNonNilReasoningOpaque(t *testing.T) {
	t.Parallel()
	empty := json.RawMessage{}
	if empty == nil {
		t.Fatal("precondition: empty non-nil Opaque required")
	}
	orig := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartReasoning,
				Reasoning: &lipapi.ReasoningPart{
					Dialect:   lipapi.ReasoningDialectAnthropicRedactedThinkingV1,
					Signature: "sig",
					Opaque:    empty,
				},
			}},
		}},
	}
	cl := lipapi.CloneCall(orig)
	if cl.Messages[0].Parts[0].Reasoning == nil {
		t.Fatal("clone lost Reasoning")
	}
	if cl.Messages[0].Parts[0].Reasoning.Opaque == nil {
		t.Fatal("clone must preserve empty non-nil Opaque (not coerce to nil)")
	}
	if !reflect.DeepEqual(orig.Messages[0].Parts[0].Reasoning.Opaque, cl.Messages[0].Parts[0].Reasoning.Opaque) {
		t.Fatalf("empty Opaque DeepEqual failed: orig=%#v clone=%#v",
			orig.Messages[0].Parts[0].Reasoning.Opaque, cl.Messages[0].Parts[0].Reasoning.Opaque)
	}
	if !reflect.DeepEqual(orig.Messages[0].Parts[0].Reasoning, cl.Messages[0].Parts[0].Reasoning) {
		t.Fatal("cloned ReasoningPart must DeepEqual original when Opaque is empty non-nil")
	}
}
