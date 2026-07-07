package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestWriteStreamSSE_thinkingSignatureEmitted(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-plan"},
		{Kind: lipapi.EventTextDelta, Delta: "ans"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_sig"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var thinkingIdx int
	var gotStartSignature string
	signatureDeltaIdx := -1
	var signatureDeltaValue string
	sigDeltaPos, stopPos := -1, -1
	for i, fr := range frames {
		switch fr.Event {
		case "content_block_start":
			var v struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type      string `json:"type"`
					Signature string `json:"signature"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.ContentBlock.Type == "thinking" {
				thinkingIdx = v.Index
				gotStartSignature = v.ContentBlock.Signature
			}
		case "content_block_delta":
			var v struct {
				Index int `json:"index"`
				Delta struct {
					Type      string `json:"type"`
					Signature string `json:"signature"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Delta.Type == "signature_delta" {
				signatureDeltaIdx = v.Index
				signatureDeltaValue = v.Delta.Signature
				sigDeltaPos = i
			}
		case "content_block_stop":
			var v struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Index == thinkingIdx {
				stopPos = i
			}
		}
	}
	if gotStartSignature != "" {
		t.Fatalf("thinking content_block_start signature should be empty on start, got %q", gotStartSignature)
	}
	if signatureDeltaIdx == -1 {
		t.Fatal("missing signature_delta")
	}
	if signatureDeltaIdx != thinkingIdx {
		t.Fatalf("signature_delta index %d != thinking block index %d", signatureDeltaIdx, thinkingIdx)
	}
	if signatureDeltaValue != "sig-plan" {
		t.Fatalf("signature_delta value %q, want sig-plan", signatureDeltaValue)
	}
	if sigDeltaPos == -1 || stopPos == -1 || sigDeltaPos >= stopPos {
		t.Fatalf("signature_delta must precede thinking content_block_stop; sigPos=%d stopPos=%d", sigDeltaPos, stopPos)
	}
}

func TestWriteStreamSSE_thinkingSignatureAccumulated(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-chunk-1"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-chunk-2"},
		{Kind: lipapi.EventTextDelta, Delta: "ans"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_sig_acc"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var signatureDeltas []string
	for _, fr := range frames {
		if fr.Event != "content_block_delta" {
			continue
		}
		var v struct {
			Delta struct {
				Type      string `json:"type"`
				Signature string `json:"signature"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		if v.Delta.Type == "signature_delta" {
			signatureDeltas = append(signatureDeltas, v.Delta.Signature)
		}
	}
	if len(signatureDeltas) != 1 {
		t.Fatalf("want exactly one signature_delta, got %v", signatureDeltas)
	}
	if signatureDeltas[0] != "sig-chunk-1sig-chunk-2" {
		t.Fatalf("signature_delta value %q, want concatenated signature", signatureDeltas[0])
	}
}

func TestWriteStreamSSE_thinkingSignatureAccumulationBounded(t *testing.T) {
	t.Parallel()
	chunk := strings.Repeat("s", lipapi.MaxRefStringBytes/2+1)
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: chunk},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: chunk},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_sig_bound"})
	if err == nil {
		t.Fatal("expected error when accumulated signature exceeds MaxRefStringBytes")
	}
	if !strings.Contains(err.Error(), "thinking signature") {
		t.Fatalf("expected thinking signature error, got %v", err)
	}
}

func TestWriteStreamSSE_thinkingSignatureNoSignatureEvent(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventTextDelta, Delta: "ans"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_nosig"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	for _, fr := range frames {
		if fr.Event != "content_block_delta" {
			continue
		}
		var v struct {
			Delta struct {
				Type string `json:"type"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		if v.Delta.Type == "signature_delta" {
			t.Fatal("unexpected signature_delta when no EventReasoningSignatureDelta arrived")
		}
	}
}

func TestWriteStreamSSE_thinkingSignatureMultiBlock(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan-a"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-a"},
		{Kind: lipapi.EventTextDelta, Delta: "bridge"},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan-b"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-b"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_multi"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var thinkingIdx []int
	var sigDeltas []struct {
		Index     int
		Signature string
	}
	for _, fr := range frames {
		switch fr.Event {
		case "content_block_start":
			var v struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.ContentBlock.Type == "thinking" {
				thinkingIdx = append(thinkingIdx, v.Index)
			}
		case "content_block_delta":
			var v struct {
				Index int `json:"index"`
				Delta struct {
					Type      string `json:"type"`
					Signature string `json:"signature"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Delta.Type == "signature_delta" {
				sigDeltas = append(sigDeltas, struct {
					Index     int
					Signature string
				}{Index: v.Index, Signature: v.Delta.Signature})
			}
		}
	}
	if len(thinkingIdx) != 2 {
		t.Fatalf("want 2 thinking blocks, got %v", thinkingIdx)
	}
	if len(sigDeltas) != 2 {
		t.Fatalf("want 2 signature_delta events, got %+v", sigDeltas)
	}
	for i, sig := range sigDeltas {
		wantSig := []string{"sig-a", "sig-b"}[i]
		wantIdx := thinkingIdx[i]
		if sig.Index != wantIdx {
			t.Fatalf("signature_delta %d index %d != thinking block index %d", i, sig.Index, wantIdx)
		}
		if sig.Signature != wantSig {
			t.Fatalf("signature_delta %d value %q, want %q", i, sig.Signature, wantSig)
		}
	}
}
