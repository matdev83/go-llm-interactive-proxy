package lipapi_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCallValidate_requiresMessages(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{}
	err := call.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	var v *lipapi.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if !errors.Is(err, lipapi.ErrInvalidCall) {
		t.Fatalf("expected wrap of ErrInvalidCall: %v", err)
	}
}

func TestCallValidate_messageRoleAndParts(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{}},
		},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for empty parts")
	}

	call = lipapi.Call{
		Messages: []lipapi.Message{
			{Role: "", Parts: []lipapi.Part{lipapi.TextPart("hi")}},
		},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for missing role")
	}
}

func TestCallValidate_textPartInvariant(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartText,
				Text: "",
			}},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestCallValidate_toolChoiceNoneWithToolsRejected(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "x"}},
		ToolChoice: lipapi.ToolChoice{
			Mode: lipapi.ToolChoiceNone,
		},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected unsupported combination error")
	}
}

func TestCallValidate_toolChoiceRequiredMissingTool(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "alpha"}},
		ToolChoice: lipapi.ToolChoice{
			Mode: lipapi.ToolChoiceRequired,
			Name: "beta",
		},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for unknown tool name")
	}
}

func TestCallValidate_generationOptionBounds(t *testing.T) {
	t.Parallel()

	temp := 2.1
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{Temperature: &temp},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected temperature bound error")
	}

	temp = 0.5
	topP := 1.1
	call.Options = lipapi.GenerationOptions{Temperature: &temp, TopP: &topP}
	if err := call.Validate(); err == nil {
		t.Fatal("expected top_p bound error")
	}

	tooBig := math.MaxInt32 + 1
	call.Options = lipapi.GenerationOptions{MaxOutputTokens: &tooBig}
	if err := call.Validate(); err == nil {
		t.Fatal("expected max_output_tokens overflow error")
	}
}

func TestCallValidate_jsonPartMustBeValidJSON(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: json.RawMessage(`{`),
			}},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected invalid json error")
	}
}

func TestCallValidate_routeSelectorMaxSize(t *testing.T) {
	t.Parallel()
	sel := strings.Repeat("a", lipapi.MaxRouteSelectorBytes+1)
	call := lipapi.Call{
		Route: lipapi.RouteIntent{Selector: sel},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for oversized selector")
	}
}

func TestCallValidate_toolParametersMustBeValidJSONWhenSet(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{
			Name:       "x",
			Parameters: json.RawMessage(`not-json`),
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected invalid tool parameters JSON")
	}
}

func TestCallValidate_validMinimalCall(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		ID: "req-1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "do",
			Description: "d",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "do"},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
