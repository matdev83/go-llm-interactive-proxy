package acp

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestBuildPromptParams_includesMessageID(t *testing.T) {
	t.Parallel()
	p := buildPromptParams("sid", []map[string]any{{"type": "text", "text": "a"}}, "mid-1")
	if p["messageId"] != "mid-1" {
		t.Fatalf("%#v", p)
	}
}

func TestMessageIDForCall_fromExtension(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal("fixed-id")
	call := lipapi.Call{
		ID: "x",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Extensions: map[string]json.RawMessage{extMessageIDJSONKey: raw},
	}
	if messageIDForCall(&call) != "fixed-id" {
		t.Fatal(messageIDForCall(&call))
	}
}

func TestMessageIDForCall_fallbackIsDeterministic(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	want := "msg_" + diag.StableCallToken(&call)
	if got := messageIDForCall(&call); got != want {
		t.Fatalf("messageIDForCall() = %q, want %q", got, want)
	}
}
