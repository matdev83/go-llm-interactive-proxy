package codexclientcompat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestDecodeConfig_emptyDocumentNode(t *testing.T) {
	t.Parallel()
	cfg, err := DecodeConfig(yaml.Node{Kind: yaml.DocumentNode})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Order != nil {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestDetectOpenCodeFromExtensionAgent(t *testing.T) {
	t.Parallel()
	in := detectCompatInput(&lipapi.Call{
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode/1.2.26"`),
		},
	})
	if !openCodeAgentMatch(in) {
		t.Fatal("expected OpenCode detection from extension agent")
	}
}

func TestDetectPiFromPromptMarkers(t *testing.T) {
	t.Parallel()
	prompt := "You are an expert coding assistant operating inside pi, a coding agent harness.\nAvailable tools:\n- bash: Execute bash commands\nGuidelines:\nbe concise"
	in := detectCompatInput(&lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart(prompt)},
		}},
	})
	if !piPromptMatch(in) {
		t.Fatal("expected Pi detection from prompt markers")
	}
}

func TestDetectDroidIgnoresToolListOnly(t *testing.T) {
	t.Parallel()
	in := detectCompatInput(&lipapi.Call{
		Tools: []lipapi.ToolDef{
			{Name: "Read"},
			{Name: "Execute"},
			{Name: "TodoWrite"},
		},
	})
	if droidAgentMatch(in) || droidPromptMatch(in) {
		t.Fatal("tool names alone must not trigger Droid detection")
	}
}

func TestApplyCompat_openCodeWinsOverDroidTools(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Tools: []lipapi.ToolDef{
			{Name: "Read"},
			{Name: "Execute"},
			{Name: "TodoWrite"},
		},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode/1.2.26"`),
		},
	}
	runHook(t, call, targetBackendID)
	instructions := joinInstructionText(call.Instructions)
	if !strings.Contains(instructions, openCodeBridgeMarker) {
		t.Fatalf("expected OpenCode bridge, instructions: %q", instructions)
	}
	if strings.Contains(instructions, droidBridgeMarker) {
		t.Fatalf("Droid bridge must not stack with OpenCode, instructions: %q", instructions)
	}
}

func TestApplyCompat_piWinsOverDroidTools(t *testing.T) {
	t.Parallel()
	piPrompt := "You are an expert coding assistant operating inside pi, a coding agent harness.\nAvailable tools:\n- bash: Execute bash commands\nGuidelines:\nbe concise"
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Tools: []lipapi.ToolDef{
			{Name: "Read"},
			{Name: "Execute"},
			{Name: "TodoWrite"},
		},
		Extensions: map[string]json.RawMessage{
			extUserAgentKey: json.RawMessage(`"@mariozechner/pi-coding-agent/0.55.3"`),
		},
	}
	runHook(t, call, targetBackendID)
	instructions := joinInstructionText(call.Instructions)
	if !strings.Contains(instructions, piBridgeMarker) {
		t.Fatalf("expected Pi bridge, instructions: %q", instructions)
	}
	if strings.Contains(instructions, droidBridgeMarker) {
		t.Fatalf("Droid bridge must not stack with Pi, instructions: %q", instructions)
	}

	call2 := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart(piPrompt)},
		}, {
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Tools: []lipapi.ToolDef{
			{Name: "Read"},
			{Name: "Execute"},
			{Name: "TodoWrite"},
		},
	}
	runHook(t, call2, targetBackendID)
	instructions2 := joinInstructionText(call2.Instructions)
	if !strings.Contains(instructions2, piBridgeMarker) {
		t.Fatalf("expected Pi bridge from prompt, instructions: %q", instructions2)
	}
	if strings.Contains(instructions2, droidBridgeMarker) {
		t.Fatalf("Droid bridge must not stack with Pi prompt, instructions: %q", instructions2)
	}
}

func TestApplyCompat_genericToolsDoNotTriggerDroid(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Tools: []lipapi.ToolDef{
			{Name: "Read"},
			{Name: "Execute"},
			{Name: "TodoWrite"},
		},
	}
	runHook(t, call, targetBackendID)
	instructions := joinInstructionText(call.Instructions)
	if strings.Contains(instructions, droidBridgeMarker) {
		t.Fatalf("generic tool names must not trigger Droid bridge, instructions: %q", instructions)
	}
}

func TestTargetBackendIDMatchesOpenAICodexID(t *testing.T) {
	t.Parallel()
	if targetBackendID != openaicodex.ID {
		t.Fatalf("targetBackendID = %q, want %q", targetBackendID, openaicodex.ID)
	}
}

func TestRequestPartHook_noopWhenBackendNotOpenAICodex(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode"`),
		},
	}
	before := joinInstructionText(call.Instructions)
	runHook(t, call, "openai-responses")
	if after := joinInstructionText(call.Instructions); after != before {
		t.Fatalf("instructions changed for non-codex backend: before=%q after=%q", before, after)
	}
	if strings.Contains(joinInstructionText(call.Messages), "OpenCode compatibility mode") {
		t.Fatal("expected no bridge message for non-codex backend")
	}
}

func TestRequestPartHook_mutatesWhenBackendIsOpenAICodex(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode"`),
		},
	}
	runHook(t, call, targetBackendID)
	if !strings.Contains(joinInstructionText(call.Instructions), "OpenCode compatibility mode") {
		t.Fatalf("instructions: %q", joinInstructionText(call.Instructions))
	}
}

func TestApplyOpenCodeCompat_dedupBridgeOrphanToolOutput(t *testing.T) {
	t.Parallel()
	// The bridge is intentionally generic: OpenCode deployments can expose any
	// combination of built-in, MCP, or plugin tools. Prompt text must not duplicate
	// request-specific names such as "bash" because the structured tool schema is
	// the real availability contract and prose can outlive the current request.
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleSystem,
				Parts: []lipapi.Part{lipapi.TextPart("OpenCode tool environment prompt for bash and edit tools")},
			},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "missing-call",
					Content:    json.RawMessage(`{"status":"ok"}`),
				}},
			},
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hello")},
			},
		},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode"`),
		},
	}
	runHook(t, call, targetBackendID)

	raw, _ := json.Marshal(call)
	if strings.Contains(string(raw), "OpenCode tool environment prompt") {
		t.Fatal("expected duplicate OpenCode harness prompt removed")
	}
	instructions := joinInstructionText(call.Instructions)
	if !strings.Contains(instructions, "OpenCode compatibility mode") {
		t.Fatal("expected bridge appended to instructions")
	}
	if strings.Count(instructions, "OpenCode compatibility mode") != 1 {
		t.Fatalf("instructions: %q", instructions)
	}
	if call.Messages[0].Role != lipapi.RoleSystem || !strings.Contains(messageText(call.Messages[0]), "OpenCode compatibility mode") {
		t.Fatalf("expected bridge system message first: %#v", call.Messages[0])
	}
	foundOrphan := false
	for _, m := range call.Messages {
		if strings.Contains(messageText(m), "Prior tool output") {
			foundOrphan = true
		}
	}
	if !foundOrphan {
		t.Fatal("expected orphaned tool result converted to system message")
	}
	for _, want := range []string{
		"string `command` and string `description`",
		"Never emit array-valued `command`",
		"Never emit textual tool-call syntax",
		"Do not use `apply_patch`",
		"Do not use `update_plan` or `read_plan`",
		"prefer `workdir`",
		"CRITICAL INSTRUCTION:",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("bridge missing %q", want)
		}
	}
	for _, unwanted := range []string{"Use only tool names that are available in this request", "`bash`"} {
		if strings.Contains(instructions, unwanted) {
			t.Fatalf("OpenCode bridge must not duplicate request-specific tool names: %q", instructions)
		}
	}
}

func TestApplyOpenCodeCompat_keepsToolResultForChatCompletionsToolCall(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("OpenCode tool environment prompt for bash and edit tools")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("run it")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: json.RawMessage(`{"id":"call_abc","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo pong\"}"}}`),
			}}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "call_abc",
				Content:    json.RawMessage(`{"status":"ok"}`),
			}}},
		},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode"`),
		},
	}
	runHook(t, call, targetBackendID)

	preserved := false
	for _, m := range call.Messages {
		if m.Role != lipapi.RoleTool {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind == lipapi.PartToolResult && p.ToolCallID == "call_abc" {
				preserved = true
			}
		}
	}
	if !preserved {
		t.Fatalf("expected tool result for call_abc preserved as RoleTool: %#v", call.Messages)
	}
	for _, m := range call.Messages {
		if strings.Contains(messageText(m), "Prior tool output") {
			t.Fatalf("tool result matching a chat-completions tool call must not be treated as orphaned: %#v", call.Messages)
		}
	}
}

func TestApplyOpenCodeCompat_preservesMatchedToolProtocolWithTools(t *testing.T) {
	t.Parallel()
	// When tools are present, even old matched tool calls/results must remain
	// structured. WebSocket continuation records prior output items and then sends
	// only the delta input on the next turn; flattening matched history here would
	// destroy the protocol lineage needed for previous_response_id continuation.
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("old request")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: json.RawMessage(`{"id":"old_call","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"old\"}"}}`),
			}}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "old_call",
				Content:    json.RawMessage(`{"old":true}`),
			}}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("current request")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: json.RawMessage(`{"id":"active_call","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"active\"}"}}`),
			}}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "active_call",
				Content:    json.RawMessage(`{"active":true}`),
			}}},
		},
		Tools: []lipapi.ToolDef{{Name: "grep"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode"`),
		},
	}
	runHook(t, call, targetBackendID)

	payload, err := openaicodex.PayloadForCall(call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, openaicodex.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	payloadJSON := string(raw)
	if got := strings.Count(payloadJSON, `"type":"function_call",`); got != 2 {
		t.Fatalf("matched function calls should remain structured, got %d: %s", got, payloadJSON)
	}
	if got := strings.Count(payloadJSON, `"type":"function_call_output"`); got != 2 {
		t.Fatalf("matched function outputs should remain structured, got %d: %s", got, payloadJSON)
	}
	if !strings.Contains(payloadJSON, `"call_id":"old_call"`) {
		t.Fatalf("matched stale tool call should remain protocol-shaped for WS continuation: %s", payloadJSON)
	}
	if !strings.Contains(payloadJSON, `"call_id":"active_call"`) {
		t.Fatalf("active tool call must remain protocol-shaped: %s", payloadJSON)
	}
}

func TestApplyOpenCodeCompat_flattensNoToolsToolHistoryAsConversation(t *testing.T) {
	t.Parallel()
	// This is the no-tools counterpart to the structured-history test above. The
	// client may resend a transcript containing tool history while exposing zero
	// callable tools. In that mode the safest universal representation is ordinary
	// conversation text: it preserves context, avoids backend protocol mismatch, and
	// prevents the model from continuing with raw textual tool-call syntax.
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("inspect logs")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: json.RawMessage(`{"id":"call_abc","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"error\"}"}}`),
			}}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "call_abc",
				Content:    json.RawMessage(`{"matches":100}`),
			}}},
		},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode"`),
		},
	}
	runHook(t, call, targetBackendID)

	for _, m := range call.Messages {
		if strings.Contains(messageText(m), "Prior tool output") && m.Role != lipapi.RoleUser {
			t.Fatalf("flattened tool output must remain conversation history, got role %q in %#v", m.Role, call.Messages)
		}
	}
	payload, err := openaicodex.PayloadForCall(call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, openaicodex.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload.Instructions, "Prior tool output") {
		t.Fatalf("tool history must not be folded into Codex instructions: %q", payload.Instructions)
	}
	raw, _ := json.Marshal(payload)
	if strings.Contains(string(raw), `"type":"function_call"`) || strings.Contains(string(raw), `"type":"function_call_output"`) {
		t.Fatalf("no-tools history must not remain protocol-shaped: %s", raw)
	}
	if !strings.Contains(string(raw), "Prior assistant tool call") || !strings.Contains(string(raw), "Prior tool output") {
		t.Fatalf("no-tools history should be rendered as conversation text: %s", raw)
	}
}

func TestApplyOpenCodeCompat_noToolsAddsTextualToolCallGuard(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("inspect logs")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: json.RawMessage(`{"id":"call_abc","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"error\"}"}}`),
			}}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "call_abc",
				Content:    json.RawMessage(`{"matches":100}`),
			}}},
		},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"opencode"`),
		},
	}
	runHook(t, call, targetBackendID)

	instructions := joinInstructionText(call.Instructions)
	for _, want := range []string{
		openCodeBridgeMarker,
		"No callable client tools are available",
		"Never emit textual tool-call syntax",
		"to=functions.<name>",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q: %q", want, instructions)
		}
	}
	if call.Messages[0].Role != lipapi.RoleSystem || !strings.Contains(messageText(call.Messages[0]), openCodeBridgeMarker) {
		t.Fatalf("expected bridge system message first: %#v", call.Messages[0])
	}
}

func TestApplyCompat_noMarkerNoToolsToolHistoryAddsTextualToolCallGuard(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("inspect logs")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: json.RawMessage(`{"id":"call_abc","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"error\"}"}}`),
			}}},
			{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "call_abc",
				Content:    json.RawMessage(`{"matches":100}`),
			}}},
		},
	}
	runHook(t, call, targetBackendID)

	instructions := joinInstructionText(call.Instructions)
	for _, want := range []string{
		openCodeBridgeMarker,
		"No callable client tools are available",
		"Never emit textual tool-call syntax",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q: %q", want, instructions)
		}
	}
}

func TestApplyCompat_noMarkerNoToolsPlainChatDoesNotAddOpenCodeGuard(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	runHook(t, call, targetBackendID)
	if instructions := joinInstructionText(call.Instructions); strings.Contains(instructions, openCodeBridgeMarker) {
		t.Fatalf("plain no-tools chat must not get OpenCode guard: %q", instructions)
	}
}

func TestApplyPiCompat_dedupBridgeNotDuplicated(t *testing.T) {
	t.Parallel()
	piPrompt := "You are an expert coding assistant operating inside pi, a coding agent harness.\nAvailable tools:\n- bash: Execute bash commands\nGuidelines:\nbe concise"
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleSystem,
				Parts: []lipapi.Part{lipapi.TextPart(piPrompt)},
			},
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hello")},
			},
		},
		Tools: []lipapi.ToolDef{{Name: "bash"}, {Name: "read"}},
	}
	runHook(t, call, targetBackendID)
	runHook(t, call, targetBackendID)

	raw, _ := json.Marshal(call)
	if strings.Contains(strings.ToLower(string(raw)), "operating inside pi") {
		t.Fatal("expected duplicate Pi harness prompt removed")
	}
	instructions := joinInstructionText(call.Instructions)
	if strings.Count(instructions, "Pi compatibility mode") != 1 {
		t.Fatalf("instructions: %q", instructions)
	}
	for _, want := range []string{
		"Use only tools exposed by the pi client",
		"Use `bash` for terminal execution",
		"Do not emit `shell`",
		"Do not use `apply_patch`",
		"CRITICAL INSTRUCTION:",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("bridge missing %q", want)
		}
	}
}

func TestApplyDroidCompat_bridgeFromTools(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleSystem,
				Parts: []lipapi.Part{lipapi.TextPart("You are Factory Droid. Use Execute and TodoWrite for tasks.")},
			},
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hello")},
			},
		},
		Tools: []lipapi.ToolDef{{Name: "Read"}, {Name: "Execute"}},
	}
	runHook(t, call, targetBackendID)

	raw, _ := json.Marshal(call)
	if strings.Contains(strings.ToLower(string(raw)), "factory droid. use execute") {
		t.Fatal("expected duplicate Droid harness prompt removed")
	}
	instructions := joinInstructionText(call.Instructions)
	if strings.Count(instructions, "Factory Droid compatibility mode") != 1 {
		t.Fatalf("instructions: %q", instructions)
	}
	for _, want := range []string{
		"`Read`",
		"`Execute`",
		"`TodoWrite`",
		"Do not emit Codex-native tool names",
		"`bash`",
		"`apply_patch`",
		"CRITICAL INSTRUCTION:",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("bridge missing %q", want)
		}
	}
}

func TestRequestPartHook_openCodeCompatPayloadShape(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{extAgentKey: json.RawMessage(`"opencode"`)},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
	}
	runHook(t, &call, targetBackendID)
	payload, err := openaicodex.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, openaicodex.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Instructions, "OpenCode compatibility mode") {
		t.Fatalf("instructions: %q", payload.Instructions)
	}
}

func TestApplyCompat_setsIgnoreUnsupportedGenParamsExt(t *testing.T) {
	t.Parallel()
	maxTok := 512
	call := &lipapi.Call{
		Extensions: map[string]json.RawMessage{extAgentKey: json.RawMessage(`"opencode"`)},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
	}
	runHook(t, call, targetBackendID)
	raw, ok := call.Extensions[extCodexIgnoreUnsupportedGenParamsKey]
	if !ok {
		t.Fatal("expected ignore_unsupported_gen_params extension")
	}
	var ignore bool
	if err := json.Unmarshal(raw, &ignore); err != nil || !ignore {
		t.Fatalf("ignore_unsupported_gen_params = %s, want true", raw)
	}
	if _, err := openaicodex.PayloadForCall(call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, openaicodex.Config{}); err != nil {
		t.Fatalf("payload with compat ext: %v", err)
	}
}

func TestRequestPartHook_codexBackendSetsIgnoreUnsupportedGenParamsWithoutClientMarker(t *testing.T) {
	t.Parallel()
	maxTok := 512
	call := &lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("summarize prior context")},
		}},
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
	}
	runHook(t, call, targetBackendID)
	raw, ok := call.Extensions[extCodexIgnoreUnsupportedGenParamsKey]
	if !ok {
		t.Fatal("expected ignore_unsupported_gen_params extension")
	}
	var ignore bool
	if err := json.Unmarshal(raw, &ignore); err != nil || !ignore {
		t.Fatalf("ignore_unsupported_gen_params = %s, want true", raw)
	}
	if _, err := openaicodex.PayloadForCall(call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, openaicodex.Config{}); err != nil {
		t.Fatalf("payload with codex compat ext: %v", err)
	}
}

func TestRequestPartHook_nonCodexBackendDoesNotSetIgnoreUnsupportedGenParamsWithoutClientMarker(t *testing.T) {
	t.Parallel()
	maxTok := 512
	call := &lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
	}
	runHook(t, call, "openai-responses")
	if _, ok := call.Extensions[extCodexIgnoreUnsupportedGenParamsKey]; ok {
		t.Fatal("did not expect ignore_unsupported_gen_params extension for non-Codex backend")
	}
}

func TestDetectHermesFromExtensionAgent(t *testing.T) {
	t.Parallel()
	in := detectCompatInput(&lipapi.Call{
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"hermes-agent/1.0 NousResearch"`),
		},
	})
	if !hermesAgentMatch(in) {
		t.Fatal("expected Hermes detection from extension agent")
	}
}

func TestDetectHermesFromExactIdentityPrompt(t *testing.T) {
	t.Parallel()
	identity := "You are Hermes Agent, an intelligent AI assistant created by Nous Research."
	in := detectCompatInput(&lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart(identity + " Be helpful.")},
		}},
	})
	if !hermesPromptMatch(in) {
		t.Fatal("expected Hermes detection from exact identity prompt")
	}
}

func TestDetectHermesIgnoresGenericMention(t *testing.T) {
	t.Parallel()
	in := detectCompatInput(&lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("You are a helpful assistant. Mention Hermes in your reply.")},
		}},
	})
	if hermesAgentMatch(in) || hermesPromptMatch(in) {
		t.Fatal("generic Hermes mention must not trigger detection")
	}
}

func TestRequestPartHook_noopForHermesOnNonCodexBackend(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"hermes-agent/1.0"`),
		},
	}
	before := joinInstructionText(call.Instructions)
	runHook(t, call, "openai-responses")
	if after := joinInstructionText(call.Instructions); after != before {
		t.Fatalf("instructions changed for non-codex backend: before=%q after=%q", before, after)
	}
	if _, ok := call.Extensions[extCodexToolStrictKey]; ok {
		t.Fatal("tool_strict extension must not be set for non-codex backend")
	}
	if strings.Contains(joinInstructionText(call.Messages), hermesBridgeMarker) {
		t.Fatal("expected no Hermes bridge message for non-codex backend")
	}
}

func TestApplyHermesCompat_idempotentBridgeCountOne(t *testing.T) {
	t.Parallel()
	identity := "You are Hermes Agent, an intelligent AI assistant created by Nous Research."
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart(identity)},
		}, {
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"hermes-agent/1.0"`),
		},
	}
	runHook(t, call, targetBackendID)
	runHook(t, call, targetBackendID)

	instructions := joinInstructionText(call.Instructions)
	if strings.Count(instructions, hermesBridgeMarker) != 1 {
		t.Fatalf("expected exactly one Hermes bridge, instructions: %q", instructions)
	}
	for _, m := range call.Messages {
		if strings.Contains(messageText(m), identity) {
			return
		}
	}
	t.Fatal("expected Hermes identity system prompt to be preserved in Messages")
}

func TestApplyHermesCompat_preservesInstructionIdentity(t *testing.T) {
	t.Parallel()
	identity := "You are Hermes Agent, an intelligent AI assistant created by Nous Research."
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart(identity)},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
	}
	runHook(t, call, targetBackendID)
	runHook(t, call, targetBackendID)

	instructions := joinInstructionText(call.Instructions)
	if !strings.Contains(instructions, identity) {
		t.Fatalf("expected Hermes identity preserved in instructions: %q", instructions)
	}
	if strings.Count(instructions, hermesBridgeMarker) != 1 {
		t.Fatalf("expected exactly one Hermes bridge, instructions: %q", instructions)
	}
}

func TestApplyHermesCompat_payloadShapeIncludesBridgeAndToolStrictFalse(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("Base instructions")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			extAgentKey: json.RawMessage(`"hermes-agent/1.0"`),
		},
	}
	runHook(t, call, targetBackendID)

	raw, ok := call.Extensions[extCodexToolStrictKey]
	if !ok {
		t.Fatal("expected openai_codex.tool_strict extension set for Hermes")
	}
	var strict bool
	if err := json.Unmarshal(raw, &strict); err != nil {
		t.Fatal(err)
	}
	if strict {
		t.Fatalf("tool_strict = true, want false: %s", raw)
	}

	payload, err := openaicodex.PayloadForCall(call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, openaicodex.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Instructions, hermesBridgeMarker) {
		t.Fatalf("instructions missing Hermes bridge: %q", payload.Instructions)
	}
	pjson, _ := json.Marshal(payload)
	if !strings.Contains(string(pjson), `"strict":false`) {
		t.Fatalf("expected tool strict=false for Hermes: %s", pjson)
	}
	if payload.ParallelToolCalls == nil || !*payload.ParallelToolCalls {
		t.Fatalf("expected parallel_tool_calls=true for Hermes: %+v", payload.ParallelToolCalls)
	}
}

func runHook(t *testing.T, call *lipapi.Call, backendID string) {
	t.Helper()
	hook := NewRequestPartHook(Config{})
	if err := hook.HandleRequestParts(context.Background(), call, sdk.PartMeta{BackendID: backendID}); err != nil {
		t.Fatal(err)
	}
}
