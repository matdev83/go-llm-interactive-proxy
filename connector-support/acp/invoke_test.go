package acp

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestValidateACPCall_rejectsTools(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "x",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "n", Parameters: json.RawMessage(`{}`)}},
	}
	if err := validateACPCall(&call); err == nil {
		t.Fatal("expected error")
	}
}

func TestSessionIDFromExtensions(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal("sess_ref_42")
	call := lipapi.Call{
		ID: "x",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Extensions: map[string]json.RawMessage{
			extSessionJSONKey: raw,
		},
	}
	if sessionIDFromExtensions(&call) != "sess_ref_42" {
		t.Fatal("session id")
	}
}

func TestPromptBlocksForCall_textAndResource(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "x",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("hi"),
				{Kind: lipapi.PartFileRef, FileRef: "file:///tmp/a.py", FileMIME: "text/x-python", FileName: "x"},
			},
		}},
	}
	blocks, err := promptBlocksForCall(&call)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) < 2 {
		t.Fatalf("blocks: %#v", blocks)
	}
	if blocks[1]["type"] != "resource" {
		t.Fatalf("expected resource block: %#v", blocks[1])
	}
}
