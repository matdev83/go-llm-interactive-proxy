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

func TestReconcileToolChoiceAfterToolListChange_allowedSubsetDropsRemovedTools(t *testing.T) {
	t.Parallel()
	tools := []lipapi.ToolDef{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	c := testCallWithTools(tools, lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny, AllowedTools: []string{"a", "b", "c"}})
	// A catalog filter removed "a" and "b"; the subset must follow the survivors.
	c.Tools = []lipapi.ToolDef{{Name: "c"}}
	lipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != lipapi.ToolChoiceAny {
		t.Fatalf("mode %q", c.ToolChoice.Mode)
	}
	if len(c.ToolChoice.AllowedTools) != 1 || c.ToolChoice.AllowedTools[0] != "c" {
		t.Fatalf("subset %v", c.ToolChoice.AllowedTools)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileToolChoiceAfterToolListChange_allowedSubsetNoSurvivorsKeepsMode(t *testing.T) {
	t.Parallel()
	c := testCallWithTools([]lipapi.ToolDef{{Name: "a"}}, lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto, AllowedTools: []string{"a"}})
	c.Tools = nil
	lipapi.ReconcileToolChoiceAfterToolListChange(c)
	if c.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("mode %q", c.ToolChoice.Mode)
	}
	if len(c.ToolChoice.AllowedTools) != 0 {
		t.Fatalf("subset should be cleared, got %v", c.ToolChoice.AllowedTools)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
