package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testCallWithTools(tools []lipapi.ToolDef, tc lipapi.ToolChoice) *lipapi.Call {
	return &lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartText,
				Text: "hi",
			}},
		}},
		Tools:      tools,
		ToolChoice: tc,
	}
}

func TestReconcileToolChoiceAfterToolListChange_noneWithToolsBecomesAuto(t *testing.T) {
	t.Parallel()
	c := testCallWithTools([]lipapi.ToolDef{{Name: "x"}}, lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone})
	lipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("mode %q", c.ToolChoice.Mode)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileToolChoiceAfterToolListChange_requiredMissingNameDowngrades(t *testing.T) {
	t.Parallel()
	c := testCallWithTools([]lipapi.ToolDef{{Name: "a"}}, lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "b"})
	lipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != lipapi.ToolChoiceAuto || c.ToolChoice.Name != "" {
		t.Fatalf("got %+v", c.ToolChoice)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileToolChoiceAfterToolListChange_requiredNoToolsDowngrades(t *testing.T) {
	t.Parallel()
	c := testCallWithTools(nil, lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "x"})
	lipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("mode %q", c.ToolChoice.Mode)
	}
}
