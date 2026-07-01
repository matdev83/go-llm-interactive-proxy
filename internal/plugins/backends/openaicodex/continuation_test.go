package openaicodex

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestWSContinuationStore_slicesCompatiblePayload(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(time.Minute, 8)
	call := lipapi.Call{
		ID: "call-1",
		Session: lipapi.SessionRef{
			ClientSessionID: "session-1",
		},
		Extensions: map[string]json.RawMessage{
			"agent": json.RawMessage(`"opencode"`),
		},
	}
	cfg := &Config{AccountID: "acct-1"}
	first := Payload{
		Model:          "gpt-5.4-mini",
		Instructions:   "instructions",
		PromptCacheKey: "session-1",
		Input: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "inspect"},
			functionCallItem{Type: "function_call", CallID: "call_1", Name: "bash", Arguments: "{}"},
			functionCallOutputItem{Type: "function_call_output", CallID: "call_1", Output: "ok"},
		},
		Tools: []toolPayload{{Type: "function", Name: "bash"}},
	}
	store.record(cfg, call, first, "resp_1")

	next := first
	next.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if !store.prepare(context.Background(), cfg, call, &next) {
		t.Fatal("expected continuation delta")
	}
	if next.PreviousResponseID != "resp_1" {
		t.Fatalf("previous_response_id = %q", next.PreviousResponseID)
	}
	if len(next.Input) != 1 {
		t.Fatalf("delta input len = %d", len(next.Input))
	}
	if len(next.Tools) != 1 {
		t.Fatalf("continued payload tools len = %d, want preserved tool schema", len(next.Tools))
	}
	msg, ok := next.Input[0].(textMessageItem)
	if !ok || msg.Content != "continue" {
		t.Fatalf("delta input = %#v", next.Input)
	}
}

func TestWSContinuationStore_slicesAfterPreviousOutputItems(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(time.Minute, 8)
	call := lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "session-1",
		},
		Extensions: map[string]json.RawMessage{
			"agent": json.RawMessage(`"opencode"`),
		},
	}
	cfg := &Config{AccountID: "acct-1"}
	first := Payload{
		Model:          "gpt-5.4-mini",
		Instructions:   "instructions",
		PromptCacheKey: "session-1",
		Input: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "inspect"},
		},
		Tools: []toolPayload{{Type: "function", Name: "bash"}},
	}
	assistantCall := functionCallItem{
		Type:      "function_call",
		ID:        "fc_1",
		CallID:    "call_fc_1",
		Name:      "bash",
		Arguments: `{"cmd":"pwd"}`,
	}
	store.record(cfg, call, first, "resp_1", assistantCall)

	next := first
	next.Input = append(append([]inputItem(nil), first.Input...),
		assistantCall,
		functionCallOutputItem{Type: "function_call_output", CallID: "call_fc_1", Output: "ok"},
		textMessageItem{Type: "message", Role: "user", Content: "continue"},
	)
	if !store.prepare(context.Background(), cfg, call, &next) {
		t.Fatal("expected continuation delta")
	}
	if len(next.Input) != 2 {
		t.Fatalf("delta input len = %d, input=%#v", len(next.Input), next.Input)
	}
	if _, ok := next.Input[0].(functionCallOutputItem); !ok {
		t.Fatalf("first delta item = %#v, want function call output", next.Input[0])
	}
	if msg, ok := next.Input[1].(textMessageItem); !ok || msg.Content != "continue" {
		t.Fatalf("second delta item = %#v, want continue message", next.Input[1])
	}
}

func TestWSContinuationStore_slicesAfterReplayedOutputItemsWithDifferentShape(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(time.Minute, 8)
	call := lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "session-1",
		},
		Extensions: map[string]json.RawMessage{
			"agent": json.RawMessage(`"opencode"`),
		},
	}
	cfg := &Config{AccountID: "acct-1"}
	first := Payload{
		Model:          "gpt-5.4-mini",
		Instructions:   "instructions",
		PromptCacheKey: "session-1",
		Input: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "inspect"},
		},
		Tools: []toolPayload{{Type: "function", Name: "bash"}},
	}
	store.record(cfg, call, first, "resp_1", functionCallItem{
		Type:      "function_call",
		ID:        "fc_1",
		CallID:    "call_fc_1",
		Name:      "bash",
		Arguments: `{"cmd":"pwd"}`,
	})

	next := first
	next.Input = append(append([]inputItem(nil), first.Input...),
		functionCallItem{Type: "function_call", CallID: "call_fc_1", Name: "bash", Arguments: `{"cmd":"pwd"}`},
		functionCallOutputItem{Type: "function_call_output", CallID: "call_fc_1", Output: "ok"},
		textMessageItem{Type: "message", Role: "user", Content: "continue"},
	)
	if !store.prepare(context.Background(), cfg, call, &next) {
		t.Fatal("expected continuation delta")
	}
	if len(next.Input) != 2 {
		t.Fatalf("delta input len = %d, input=%#v", len(next.Input), next.Input)
	}
	if _, ok := next.Input[0].(functionCallOutputItem); !ok {
		t.Fatalf("first delta item = %#v, want function call output", next.Input[0])
	}
	if msg, ok := next.Input[1].(textMessageItem); !ok || msg.Content != "continue" {
		t.Fatalf("second delta item = %#v, want continue message", next.Input[1])
	}
}

func TestWSContinuationStore_rejectsConcurrentReuseOfPreviousResponseID(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(time.Minute, 8)
	call := lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "session-1",
		},
		Extensions: map[string]json.RawMessage{
			"agent": json.RawMessage(`"opencode"`),
		},
	}
	cfg := &Config{AccountID: "acct-1"}
	first := Payload{
		Model:          "gpt-5.4-mini",
		Instructions:   "instructions",
		PromptCacheKey: "session-1",
		Input: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "inspect"},
		},
		Tools: []toolPayload{{Type: "function", Name: "bash"}},
	}
	store.record(cfg, call, first, "resp_1")

	next := first
	next.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if !store.prepare(context.Background(), cfg, call, &next) {
		t.Fatal("expected first continuation delta")
	}
	if next.PreviousResponseID != "resp_1" {
		t.Fatalf("previous_response_id = %q", next.PreviousResponseID)
	}

	duplicate := first
	duplicate.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if store.prepare(context.Background(), cfg, call, &duplicate) {
		t.Fatal("duplicate in-flight continuation unexpectedly reused previous_response_id")
	}
	if duplicate.PreviousResponseID != "" {
		t.Fatalf("duplicate previous_response_id = %q", duplicate.PreviousResponseID)
	}

	child := first
	child.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	store.record(cfg, call, child, "resp_2")

	afterChild := child
	afterChild.Input = append(append([]inputItem(nil), child.Input...), textMessageItem{Type: "message", Role: "user", Content: "next"})
	if !store.prepare(context.Background(), cfg, call, &afterChild) {
		t.Fatal("expected continuation after child response recorded")
	}
	if afterChild.PreviousResponseID != "resp_2" {
		t.Fatalf("previous_response_id after child = %q", afterChild.PreviousResponseID)
	}
}

func TestWSContinuationStore_invalidatePreparedContinuationClearsInFlight(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(time.Minute, 8)
	call := lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "session-1",
		},
		Extensions: map[string]json.RawMessage{
			"agent": json.RawMessage(`"opencode"`),
		},
	}
	cfg := &Config{AccountID: "acct-1"}
	first := Payload{
		Model:          "gpt-5.4-mini",
		Instructions:   "instructions",
		PromptCacheKey: "session-1",
		Input: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "inspect"},
		},
		Tools: []toolPayload{{Type: "function", Name: "bash"}},
	}
	store.record(cfg, call, first, "resp_1")

	next := first
	next.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if !store.prepare(context.Background(), cfg, call, &next) {
		t.Fatal("expected continuation delta")
	}
	store.invalidate(cfg, call, &first)

	afterFailure := first
	afterFailure.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if store.prepare(context.Background(), cfg, call, &afterFailure) {
		t.Fatal("invalidated continuation entry must not remain reusable")
	}

	store.record(cfg, call, first, "resp_1")
	afterRecord := first
	afterRecord.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if !store.prepare(context.Background(), cfg, call, &afterRecord) {
		t.Fatal("newly recorded continuation should be usable after invalidation")
	}
}

func TestWSContinuationStore_usesAuthoritativeSessionBeforeClientHint(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(time.Minute, 8)
	cfg := &Config{}
	first := Payload{
		Model:          "gpt-5.4-mini",
		Instructions:   "instructions",
		PromptCacheKey: "lip-gpt-5.4-mini-stable",
		Input: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "initial task"},
			textMessageItem{Type: "message", Role: "assistant", Content: "working"},
		},
	}
	store.record(cfg, lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID:        "client-session-1",
			AuthoritativeSessionID: "proxy-session-1",
		},
	}, first, "resp_1")

	next := first
	next.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if !store.prepare(context.Background(), cfg, lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID:        "client-session-2",
			AuthoritativeSessionID: "proxy-session-1",
		},
	}, &next) {
		t.Fatal("expected continuation despite changed client hint")
	}
	if next.PreviousResponseID != "resp_1" {
		t.Fatalf("previous_response_id = %q", next.PreviousResponseID)
	}

	otherSession := first
	otherSession.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if store.prepare(context.Background(), cfg, lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID:        "client-session-1",
			AuthoritativeSessionID: "proxy-session-2",
		},
	}, &otherSession) {
		t.Fatal("changed authoritative session must not reuse previous_response_id")
	}
}

func TestWSContinuationStore_rejectsStaticFingerprintDrift(t *testing.T) {
	t.Parallel()
	store := newWSContinuationStore(time.Minute, 8)
	call := lipapi.Call{Session: lipapi.SessionRef{ClientSessionID: "session-1"}}
	cfg := &Config{}
	first := Payload{
		Model:          "gpt-5.4-mini",
		Instructions:   "instructions",
		PromptCacheKey: "session-1",
		Input:          []inputItem{textMessageItem{Type: "message", Role: "user", Content: "inspect"}},
		Tools:          []toolPayload{{Type: "function", Name: "bash"}},
	}
	store.record(cfg, call, first, "resp_1")

	next := first
	next.Tools = []toolPayload{{Type: "function", Name: "grep"}}
	next.Input = append(append([]inputItem(nil), first.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	if store.prepare(context.Background(), cfg, call, &next) {
		t.Fatal("unexpected continuation delta after tools drift")
	}
	if next.PreviousResponseID != "" {
		t.Fatalf("previous_response_id = %q", next.PreviousResponseID)
	}
}
