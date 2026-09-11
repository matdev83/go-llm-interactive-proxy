package openailegacy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	frontendlimits "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/limits"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// memSource wraps a byte slice as largebody.Source for testing.
type memSource struct {
	data []byte
}

func newMemSource(data []byte) *memSource {
	return &memSource{data: data}
}

func (s *memSource) Size() int64 {
	return int64(len(s.data))
}

func (s *memSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func (s *memSource) Close() error {
	return nil
}

var _ largebody.Source = (*memSource)(nil)

func defaultChatProofInput(body []byte, routeSelector string, headers http.Header) frontendpipe.ProofInput {
	if headers == nil {
		headers = make(http.Header)
	}
	return frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              headers,
		URLPath:              "/v1/chat/completions",
		RouteSelector:        routeSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"openai", "stub", "custom"}),
		DefaultRouteSelector: "stub:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(body),
		BodyBytes:            int64(len(body)),
	}
}

// Requirement 13, 17.6: Confirm at implementation time: no legacy full-body resolver; RouteFromBodyModel=true.
func TestOpenAIChatProfile_PrerequisitesAndIdentity(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()
	if prof.ProfileID() != openailegacy.ProfileID {
		t.Fatalf("ProfileID: got %q, want %q", prof.ProfileID(), openailegacy.ProfileID)
	}
	if prof.ProfileID() != "openai_chat_v1" {
		t.Fatalf("ProfileID constant: got %q, want openai_chat_v1", prof.ProfileID())
	}

	// Verify Handler builds pipe with no legacy ResolveRouteSelector and RouteFromBodyModel=true
	h := &openailegacy.Handler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // triggers buildPipe via pipeOnce

	spec := h.Spec()
	if spec == nil {
		t.Fatal("Handler.Spec() returned nil")
	}
	if spec.ResolveRouteSelector != nil {
		t.Fatal("Handler must NOT configure legacy ResolveRouteSelector (Requirement 13.2, 17.6)")
	}
	if !spec.RouteFromBodyModel {
		t.Fatal("Handler must configure RouteFromBodyModel=true (Requirement 4.8, 17.6)")
	}
	if spec.Profile == nil {
		t.Fatal("Handler must wire certified Profile into pipe (Requirement 17.1)")
	}
	if spec.Profile.ProfileID() != "openai_chat_v1" {
		t.Fatalf("Handler pipe profile ID: got %q, want openai_chat_v1", spec.Profile.ProfileID())
	}

	// Custom profile override is honored
	customProf := openailegacy.NewProfile()
	h2 := &openailegacy.Handler{Profile: customProf}
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if h2.Spec().Profile != customProf {
		t.Fatal("Handler must honor explicit Profile override")
	}
}

// Requirement 4, 14, 16, 17: Compile proof for simple user message.
func TestOpenAIChatProfile_CompileProof_SimpleUserMessage(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello world"}]}`)
	prof := openailegacy.NewProfile()
	in := defaultChatProofInput(body, "", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.State.Proof
	if proof.ProfileID != "openai_chat_v1" {
		t.Errorf("Proof.ProfileID: got %q, want openai_chat_v1", proof.ProfileID)
	}
	if proof.Operation != lipapi.OperationOpenAIChatCompletions {
		t.Errorf("Proof.Operation: got %q, want %q", proof.Operation, lipapi.OperationOpenAIChatCompletions)
	}
	if proof.Delivery != lipapi.DeliveryModeNonStreaming {
		t.Errorf("Proof.Delivery: got %v, want NonStreaming", proof.Delivery)
	}
	if proof.ClientModel != "gpt-4o" {
		t.Errorf("Proof.ClientModel: got %q, want gpt-4o", proof.ClientModel)
	}
	if proof.RouteSelector != "stub:default" {
		t.Errorf("Proof.RouteSelector: got %q, want stub:default", proof.RouteSelector)
	}
	if proof.Mode != largebody.BodyModeIdentityJSON {
		t.Errorf("Proof.Mode: got %v, want BodyModeIdentityJSON", proof.Mode)
	}
	if !proof.Rewrite.NeedsModelRewrite() {
		t.Error("Proof.Rewrite: expected NeedsModelRewrite=true")
	}

	// Verify model token span
	if proof.ModelSpan.Length == 0 {
		t.Fatal("Proof.ModelSpan: expected non-zero length")
	}
	spanVal := string(body[proof.ModelSpan.Offset : proof.ModelSpan.Offset+proof.ModelSpan.Length])
	if spanVal != `"gpt-4o"` {
		t.Errorf("Proof.ModelSpan slice: got %q, want %q", spanVal, `"gpt-4o"`)
	}

	// Verify seeds
	seeds := out.State.Seeds
	if seeds.DeterministicToken == "" {
		t.Error("Seeds.DeterministicToken is empty")
	}
	if seeds.CancellationID != "chatcmpl_"+seeds.DeterministicToken {
		t.Errorf("Seeds.CancellationID: got %q, want chatcmpl_%s", seeds.CancellationID, seeds.DeterministicToken)
	}

	// Verify canonical identity parity
	decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:default",
	})
	if err != nil {
		t.Fatalf("DecodeChatRequest: %v", err)
	}
	canonicalDigest := largebody.CanonicalCallIdentity(decoded.Call)
	if proof.Identity != canonicalDigest {
		t.Fatalf("Proof.Identity mismatch:\n  proof:     %s\n  canonical: %s", proof.Identity, canonicalDigest)
	}
}

// Requirement 4, 14, 16, 17: Compile proof with streaming and generation options.
func TestOpenAIChatProfile_CompileProof_StreamingAndOptions(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-4o",
		"stream": true,
		"messages": [
			{"role": "system", "content": "you are a helpful assistant"},
			{"role": "user", "content": "hello"}
		],
		"temperature": 0.7,
		"top_p": 0.9,
		"max_tokens": 150,
		"parallel_tool_calls": true,
		"reasoning_effort": "high",
		"verbosity": "high",
		"stream_options": {"include_usage": true}
	}`)
	prof := openailegacy.NewProfile()
	in := defaultChatProofInput(body, "openai:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.State.Proof
	if proof.Delivery != lipapi.DeliveryModeStreaming {
		t.Errorf("Proof.Delivery: got %v, want Streaming", proof.Delivery)
	}
	if proof.RouteSelector != "openai:gpt-4o" {
		t.Errorf("Proof.RouteSelector: got %q, want openai:gpt-4o", proof.RouteSelector)
	}
	if proof.MaxOutputTokens != 150 {
		t.Errorf("Proof.MaxOutputTokens: got %d, want 150", proof.MaxOutputTokens)
	}

	// Verify canonical identity parity
	decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "openai:gpt-4o",
	})
	if err != nil {
		t.Fatalf("DecodeChatRequest: %v", err)
	}
	canonicalDigest := largebody.CanonicalCallIdentity(decoded.Call)
	if proof.Identity != canonicalDigest {
		t.Fatalf("Proof.Identity mismatch:\n  proof:     %s\n  canonical: %s", proof.Identity, canonicalDigest)
	}
}

// Requirement 4.8, 17.6: Route selector precedence: header > inline prefix > default.
func TestOpenAIChatProfile_RoutePrecedence(t *testing.T) {
	t.Parallel()

	bodyPrefix := []byte(`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	bodyNoPrefix := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	prof := openailegacy.NewProfile()

	// 1. Explicit RouteSelector takes precedence
	in1 := defaultChatProofInput(bodyPrefix, "custom:explicit", nil)
	out1, err := prof.CompileProof(context.Background(), in1)
	if err != nil {
		t.Fatalf("case 1 failed: %v", err)
	}
	if out1.State.Proof.RouteSelector != "custom:explicit" {
		t.Errorf("case 1: got %q, want custom:explicit", out1.State.Proof.RouteSelector)
	}

	// 2. Inline prefix from model
	in2 := defaultChatProofInput(bodyPrefix, "", nil)
	out2, err := prof.CompileProof(context.Background(), in2)
	if err != nil {
		t.Fatalf("case 2 failed: %v", err)
	}
	if out2.State.Proof.RouteSelector != "openai:gpt-4o" {
		t.Errorf("case 2: got %q, want openai:gpt-4o", out2.State.Proof.RouteSelector)
	}

	// 3. Default route selector when model has no recognized prefix
	in3 := defaultChatProofInput(bodyNoPrefix, "", nil)
	out3, err := prof.CompileProof(context.Background(), in3)
	if err != nil {
		t.Fatalf("case 3 failed: %v", err)
	}
	if out3.State.Proof.RouteSelector != "stub:default" {
		t.Errorf("case 3: got %q, want stub:default", out3.State.Proof.RouteSelector)
	}
}

// Requirement 4, 14, 16, 17: Complex conversation with tools, tool results, reasoning_content.
func TestOpenAIChatProfile_ComplexMessagesAndIdentityParity(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "system instructions"},
			{"role": "user", "content": "calculate 2+2"},
			{
				"role": "assistant",
				"content": "thinking about it",
				"reasoning_content": "math reasoning here",
				"tool_calls": [
					{
						"id": "call_abc123",
						"type": "function",
						"function": {
							"name": "calc",
							"arguments": "{\"expr\":\"2+2\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_abc123",
				"content": "4"
			}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "calc",
					"description": "calculator",
					"parameters": {"type": "object", "properties": {"expr": {"type": "string"}}}
				}
			}
		],
		"tool_choice": "auto"
	}`)

	prof := openailegacy.NewProfile()
	in := defaultChatProofInput(body, "stub:default", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	// Verify canonical identity matches 100%
	decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:default",
	})
	if err != nil {
		t.Fatalf("DecodeChatRequest: %v", err)
	}
	canonicalDigest := largebody.CanonicalCallIdentity(decoded.Call)
	if out.State.Proof.Identity != canonicalDigest {
		t.Fatalf("Proof.Identity mismatch:\n  proof:     %s\n  canonical: %s", out.State.Proof.Identity, canonicalDigest)
	}
}

// Requirement 14.1, 14.6: Session headers integration and cancellation ID.
func TestOpenAIChatProfile_SessionHeadersAndCarrier(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	headers := make(http.Header)
	headers.Set("X-LIP-Session-ID", "sess_xyz789")
	headers.Set("X-LIP-A-Leg-ID", "aleg_456")

	prof := openailegacy.NewProfile()
	in := defaultChatProofInput(body, "stub:default", headers)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	seeds := out.State.Seeds
	if seeds.SessionID != "sess_xyz789" {
		t.Errorf("Seeds.SessionID: got %q, want sess_xyz789", seeds.SessionID)
	}
	if seeds.ALegID != "aleg_456" {
		t.Errorf("Seeds.ALegID: got %q, want aleg_456", seeds.ALegID)
	}
	expectedCancel := frontendpipe.FormatOpenAICancellationCarrier("aleg_456", "sess_xyz789")
	if seeds.CancellationID != expectedCancel {
		t.Errorf("Seeds.CancellationID: got %q, want %q", seeds.CancellationID, expectedCancel)
	}
}

// Requirement 4, 13, 14, 16, 17: Decline to canonical tests.
func TestOpenAIChatProfile_DeclinesToCanonical(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()

	cases := []struct {
		name        string
		body        string
		urlPath     string
		source      largebody.Source
		headers     http.Header
		wantErrSub  string
		errIsTarget error
	}{
		{
			name:       "duplicate key in root",
			body:       `{"model":"gpt-4o","model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "json scanner",
		},
		{
			name:       "unknown top-level key",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"unknown_key":"val"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "unsupported or unknown body key",
		},
		{
			name:       "unsupported control store",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"store":false}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "unsupported or unknown body key",
		},
		{
			name:       "unsupported control max_completion_tokens",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":100}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "unsupported or unknown body key",
		},
		{
			name:       "unsupported control stop",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stop":["END"]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "unsupported or unknown body key",
		},
		{
			name:        "body-carried LIP session metadata",
			body:        `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"metadata":{"lip_session_id":"s123"}}`,
			urlPath:     "/v1/chat/completions",
			errIsTarget: largebody.ErrBodySessionMetadataRejected,
		},
		{
			name:       "developer role requires canonical normalization",
			body:       `{"model":"gpt-4o","messages":[{"role":"developer","content":"system prompt"},{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "developer role requires canonical normalization",
		},
		{
			name:       "malformed tool message with empty tool_call_id",
			body:       `{"model":"gpt-4o","messages":[{"role":"tool","tool_call_id":"","content":"result"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "malformed tool message requires canonical repair",
		},
		{
			name:       "non-string tool content requires canonical encoding",
			body:       `{"model":"gpt-4o","messages":[{"role":"tool","tool_call_id":"c1","content":{"status":"ok"}}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "non-string tool content requires canonical encoding",
		},
		{
			name:       "empty assistant message requires canonical drop",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","content":""}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "empty assistant message requires canonical drop",
		},
		{
			name:       "reasoning alias requires canonical normalization",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","content":"ans","reasoning":"my thoughts"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "reasoning alias requires canonical normalization",
		},
		{
			name:       "legacy function_call requires canonical normalization",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","content":"ans","function_call":{"name":"calc","arguments":"{}"}}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "legacy function_call requires canonical normalization",
		},
		{
			name:       "malformed tool_calls entry with empty name",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"","arguments":"{}"}}]}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "malformed tool call requires canonical repair",
		},
		{
			name:       "non-string tool_calls arguments requires canonical encoding",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"calc","arguments":{"a":1}}}]}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "non-string tool_calls arguments requires canonical encoding",
		},
		{
			name:       "missing model",
			body:       `{"messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "model is required",
		},
		{
			name:       "empty messages",
			body:       `{"model":"gpt-4o","messages":[]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "messages is required",
		},
		{
			name:       "unsupported url path",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/other",
			wantErrSub: "unsupported url path",
		},
		{
			name:       "nil source",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			source:     nil,
			wantErrSub: "nil replay source",
		},
		{
			name:       "invalid json",
			body:       `{not json}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "json scanner",
		},
		{
			name:       "session hint requires canonical decode",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			headers:    http.Header{"X-Lip-Session-Hint": []string{"client-hint-123"}},
			wantErrSub: "session hint requires canonical decode",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bodyBytes := []byte(tc.body)
			var src largebody.Source = newMemSource(bodyBytes)
			if tc.name == "nil source" {
				src = nil
			}

			in := frontendpipe.ProofInput{
				Ctx:                  context.Background(),
				Headers:              tc.headers,
				URLPath:              tc.urlPath,
				RouteSelector:        "stub:default",
				RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
				DefaultRouteSelector: "stub:default",
				RouteFromBodyModel:   true,
				Source:               src,
				BodyBytes:            int64(len(bodyBytes)),
			}

			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatal("expected CompileProof to fail and decline to canonical, got nil error")
			}
			if tc.errIsTarget != nil && !errors.Is(err, tc.errIsTarget) {
				t.Fatalf("expected error to match %v, got %v", tc.errIsTarget, err)
			}
			if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
			}
		})
	}
}

// Test limits on messages and metadata.
func TestOpenAIChatProfile_Limits(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()

	// MaxMessages exceeded
	msgs := make([]map[string]string, frontendlimits.MaxMessages+1)
	for i := range msgs {
		msgs[i] = map[string]string{"role": "user", "content": "hi"}
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "gpt-4o",
		"messages": msgs,
	})
	in := defaultChatProofInput(body, "stub:default", nil)
	_, err := prof.CompileProof(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "maximum is") {
		t.Fatalf("expected messages limit error, got %v", err)
	}
}
