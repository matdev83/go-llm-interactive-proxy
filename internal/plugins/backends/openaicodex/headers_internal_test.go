package openaicodex

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type unmarshalableInputItem struct {
	Ch chan int `json:"ch"`
}

func (unmarshalableInputItem) inputItem() {}

func TestConversationIDForPayload_shortFingerprintDoesNotPanic(t *testing.T) {
	t.Parallel()
	got := conversationIDForPayload(lipapi.Call{}, "gpt-test", Payload{
		Input: []inputItem{unmarshalableInputItem{Ch: make(chan int)}},
	})
	if got != "lip-gpt-test-" {
		t.Fatalf("conversation id = %q, want empty fingerprint suffix without panic", got)
	}
}

func TestConversationIDForPayload_truncatesFingerprint(t *testing.T) {
	t.Parallel()
	got := conversationIDForPayload(lipapi.Call{}, "gpt-test", Payload{
		Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "hello"}},
	})
	if !strings.HasPrefix(got, "lip-gpt-test-") {
		t.Fatalf("conversation id = %q, want lip-gpt-test prefix", got)
	}
	if suffix := strings.TrimPrefix(got, "lip-gpt-test-"); len(suffix) != 16 {
		t.Fatalf("fingerprint suffix length = %d, want 16", len(suffix))
	}
}

func TestConversationID_precedence(t *testing.T) {
	t.Parallel()
	const (
		genID  = "call_deadbeefdeadbeef"
		userID = "user-req-123"
	)
	tests := []struct {
		name  string
		call  lipapi.Call
		model string
		want  string
	}{
		{
			name:  "continuity key wins",
			call:  lipapi.Call{ID: userID, Session: lipapi.SessionRef{ContinuityKey: "ck", AuthoritativeSessionID: "auth"}},
			model: "gpt-test",
			want:  "ck",
		},
		{
			name:  "correlation id wins when no continuity",
			call:  lipapi.Call{ID: userID, Session: lipapi.SessionRef{AuthoritativeSessionID: "auth"}},
			model: "gpt-test",
			want:  "auth",
		},
		{
			name:  "non-generated call id wins",
			call:  lipapi.Call{ID: userID},
			model: "gpt-test",
			want:  userID,
		},
		{
			name:  "generated call id skipped falls back to model",
			call:  lipapi.Call{ID: genID},
			model: "gpt-test",
			want:  "lip-gpt-test",
		},
		{
			name:  "empty model defaults to codex suffix",
			call:  lipapi.Call{ID: genID},
			model: "",
			want:  "lip-codex",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversationID(tt.call, tt.model)
			if got != tt.want {
				t.Errorf("conversationID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConversationIDForPayload_precedence(t *testing.T) {
	t.Parallel()
	const (
		genID  = "call_deadbeefdeadbeef"
		userID = "user-req-123"
	)
	withInput := Payload{Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "hello"}}}
	tests := []struct {
		name    string
		call    lipapi.Call
		model   string
		payload Payload
		want    string
	}{
		{
			name:    "continuity key wins over fingerprint",
			call:    lipapi.Call{ID: genID, Session: lipapi.SessionRef{ContinuityKey: "ck"}},
			model:   "gpt-test",
			payload: withInput,
			want:    "ck",
		},
		{
			name:    "correlation id wins over fingerprint",
			call:    lipapi.Call{ID: genID, Session: lipapi.SessionRef{AuthoritativeSessionID: "auth"}},
			model:   "gpt-test",
			payload: withInput,
			want:    "auth",
		},
		{
			name:    "non-generated call id wins over fingerprint",
			call:    lipapi.Call{ID: userID},
			model:   "gpt-test",
			payload: withInput,
			want:    userID,
		},
		{
			name:    "generated call id with no input delegates to conversationID",
			call:    lipapi.Call{ID: genID},
			model:   "gpt-test",
			payload: Payload{},
			want:    "lip-gpt-test",
		},
		{
			name:    "generated call id no input empty model delegates to conversationID",
			call:    lipapi.Call{ID: genID},
			model:   "",
			payload: Payload{},
			want:    "lip-codex",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversationIDForPayload(tt.call, tt.model, tt.payload)
			if got != tt.want {
				t.Errorf("conversationIDForPayload = %q, want %q", got, tt.want)
			}
		})
	}
}
