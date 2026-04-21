package lipapi_test

import (
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
}
