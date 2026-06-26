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
			"agent": json.RawMessage(`"opencode/1.2.26"`),
		},
	})
	if !isOpenCode(in) {
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
	if !isPi(in) {
		t.Fatal("expected Pi detection from prompt markers")
	}
}

func TestDetectDroidFromToolList(t *testing.T) {
	t.Parallel()
	in := detectCompatInput(&lipapi.Call{
		Tools: []lipapi.ToolDef{
			{Name: "Read"},
			{Name: "Execute"},
			{Name: "TodoWrite"},
		},
	})
	if !isDroid(in) {
		t.Fatal("expected Droid detection from native tool names")
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
			"agent": json.RawMessage(`"opencode"`),
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
			"agent": json.RawMessage(`"opencode"`),
		},
	}
	runHook(t, call, targetBackendID)
	if !strings.Contains(joinInstructionText(call.Instructions), "OpenCode compatibility mode") {
		t.Fatalf("instructions: %q", joinInstructionText(call.Instructions))
	}
}

func TestApplyOpenCodeCompat_dedupBridgeOrphanToolOutput(t *testing.T) {
	t.Parallel()
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
			"agent": json.RawMessage(`"opencode"`),
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
		"Do not use `apply_patch`",
		"Do not use `update_plan` or `read_plan`",
		"prefer `workdir`",
		"CRITICAL INSTRUCTION:",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("bridge missing %q", want)
		}
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
		Extensions: map[string]json.RawMessage{"agent": json.RawMessage(`"opencode"`)},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
	}
	runHook(t, &call, targetBackendID)
	payload, err := openaicodex.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	}, openaicodex.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Instructions, "OpenCode compatibility mode") {
		t.Fatalf("instructions: %q", payload.Instructions)
	}
}

func runHook(t *testing.T, call *lipapi.Call, backendID string) {
	t.Helper()
	hook := NewRequestPartHook(Config{})
	if err := hook.HandleRequestParts(context.Background(), call, sdk.PartMeta{BackendID: backendID}); err != nil {
		t.Fatal(err)
	}
}
