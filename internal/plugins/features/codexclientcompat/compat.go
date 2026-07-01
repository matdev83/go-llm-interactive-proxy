package codexclientcompat

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	openCodeBridgeMarker = "OpenCode compatibility mode"
	piBridgeMarker       = "Pi compatibility mode"
	droidBridgeMarker    = "Factory Droid compatibility mode"
	hermesBridgeMarker   = "Hermes Agent compatibility mode"

	extAgentKey                           = "agent"
	extUserAgentKey                       = "user_agent"
	extCodexAgentKey                      = "openai_codex.agent"
	extHeadersKey                         = "headers"
	extCodexToolStrictKey                 = "openai_codex.tool_strict"
	extCodexIgnoreUnsupportedGenParamsKey = "openai_codex.ignore_unsupported_gen_params"

	codexDefaultInstruction = "You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer."
)

type compatInput struct {
	agents []string
	prompt string
	tools  []string
}

type compatBridge struct {
	marker        string
	matchesAgent  func(compatInput) bool
	matchesPrompt func(compatInput) bool
	filter        func(string) bool
	build         func(*lipapi.Call) string
	beforeApply   func(*lipapi.Call)
}

var compatBridges = []compatBridge{
	{
		marker:        openCodeBridgeMarker,
		matchesAgent:  openCodeAgentMatch,
		matchesPrompt: openCodePromptMatch,
		filter:        isOpenCodeHarnessText,
		build:         func(call *lipapi.Call) string { return buildOpenCodeBridge(len(call.Tools) > 0) },
		beforeApply:   applyOpenCodeToolHistoryCompat,
	},
	{
		marker:        piBridgeMarker,
		matchesAgent:  piAgentMatch,
		matchesPrompt: piPromptMatch,
		filter:        isPiHarnessText,
		build:         func(*lipapi.Call) string { return buildPiBridge() },
	},
	{
		marker:        droidBridgeMarker,
		matchesAgent:  droidAgentMatch,
		matchesPrompt: droidPromptMatch,
		filter:        isDroidHarnessText,
		build:         func(call *lipapi.Call) string { return buildDroidBridge(collectCallToolNames(call)) },
	},
	{
		marker:        hermesBridgeMarker,
		matchesAgent:  hermesAgentMatch,
		matchesPrompt: hermesPromptMatch,
		filter:        isHermesBridgeText,
		build:         func(*lipapi.Call) string { return buildHermesBridge() },
		beforeApply:   applyHermesToolStrict,
	},
}

func ApplyCompat(call *lipapi.Call) {
	if call == nil {
		return
	}
	in := detectCompatInput(call)
	bridge := selectCompatBridge(in)
	if bridge == nil {
		bridge = fallbackCompatBridge(call)
		if bridge == nil {
			return
		}
	}
	applyIgnoreUnsupportedGenParams(call)
	hasTools := len(call.Tools) > 0
	call.Messages = filterHarnessMessages(call.Messages, bridge.filter)
	call.Instructions = filterHarnessMessages(call.Instructions, bridge.filter)
	if bridge.beforeApply != nil {
		bridge.beforeApply(call)
	}
	if !hasTools && bridge.marker != openCodeBridgeMarker {
		return
	}
	block := bridge.build(call)
	appendBridgeInstructions(call, bridge.marker, block)
	prependBridgeMessage(call, bridge.marker, block)
}

func selectCompatBridge(in compatInput) *compatBridge {
	for i := range compatBridges {
		if compatBridges[i].matchesAgent(in) {
			return &compatBridges[i]
		}
	}
	for i := range compatBridges {
		if compatBridges[i].matchesPrompt(in) {
			return &compatBridges[i]
		}
	}
	return nil
}

func fallbackCompatBridge(call *lipapi.Call) *compatBridge {
	if call == nil || len(call.Tools) > 0 || !hasStructuredToolTranscript(call.Messages) {
		return nil
	}
	for i := range compatBridges {
		if compatBridges[i].marker == openCodeBridgeMarker {
			return &compatBridges[i]
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func detectCompatInput(call *lipapi.Call) compatInput {
	return compatInput{
		agents: collectAgentCandidates(call),
		prompt: collectPromptText(call),
		tools:  collectCallToolNames(call),
	}
}

func collectAgentCandidates(call *lipapi.Call) []string {
	out := make([]string, 0, 4)
	for _, key := range []string{extAgentKey, extUserAgentKey, extCodexAgentKey} {
		if raw, ok := call.Extensions[key]; ok {
			var agent string
			if json.Unmarshal(raw, &agent) == nil && strings.TrimSpace(agent) != "" {
				out = append(out, agent)
			}
		}
	}
	if raw, ok := call.Extensions[extHeadersKey]; ok {
		var headers map[string]string
		if json.Unmarshal(raw, &headers) == nil {
			for _, key := range []string{"user-agent", "User-Agent"} {
				if ua := strings.TrimSpace(headers[key]); ua != "" {
					out = append(out, ua)
				}
			}
		}
	}
	return out
}

func collectPromptText(call *lipapi.Call) string {
	var b strings.Builder
	for _, m := range call.Instructions {
		appendMessageText(&b, m)
	}
	for _, m := range call.Messages {
		appendMessageText(&b, m)
	}
	return b.String()
}

func appendMessageText(b *strings.Builder, m lipapi.Message) {
	for _, p := range m.Parts {
		if p.Kind != lipapi.PartText || strings.TrimSpace(p.Text) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p.Text)
	}
}

func collectCallToolNames(call *lipapi.Call) []string {
	out := make([]string, 0, len(call.Tools))
	for _, t := range call.Tools {
		if name := strings.TrimSpace(t.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func filterHarnessMessages(msgs []lipapi.Message, isHarness func(string) bool) []lipapi.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]lipapi.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == lipapi.RoleSystem && isHarness(messageText(m)) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func messageText(m lipapi.Message) string {
	var b strings.Builder
	appendMessageText(&b, m)
	return b.String()
}

func joinInstructionText(insts []lipapi.Message) string {
	var b strings.Builder
	for _, m := range insts {
		appendMessageText(&b, m)
	}
	return strings.TrimSpace(b.String())
}

func appendBridgeInstructions(call *lipapi.Call, marker, block string) {
	raw := joinInstructionText(call.Instructions)
	current := raw
	if current == "" {
		current = codexDefaultInstruction
	}
	updated := appendCompatInstructions(current, marker, block)
	if updated == current {
		return
	}
	if raw == "" {
		call.Instructions = []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart(updated)},
		}}
		return
	}
	call.Instructions = append(call.Instructions, lipapi.Message{
		Role:  lipapi.RoleSystem,
		Parts: []lipapi.Part{lipapi.TextPart("\n\n" + block)},
	})
}

func prependBridgeMessage(call *lipapi.Call, marker, block string) {
	if hasBridgeMessage(call.Messages, marker) {
		return
	}
	call.Messages = append([]lipapi.Message{{
		Role:  lipapi.RoleSystem,
		Parts: []lipapi.Part{lipapi.TextPart(block)},
	}}, call.Messages...)
}

func hasBridgeMessage(msgs []lipapi.Message, marker string) bool {
	for _, m := range msgs {
		if strings.Contains(messageText(m), marker) {
			return true
		}
	}
	return false
}

func appendCompatInstructions(instructions, marker, block string) string {
	if strings.Contains(instructions, marker) {
		return instructions
	}
	if strings.TrimSpace(instructions) != "" {
		return strings.TrimRight(instructions, " \t\n\r") + "\n\n" + block
	}
	return block
}

func criticalInstruction(agentName string) string {
	return "CRITICAL INSTRUCTION:\n" +
		"(a) NEVER run cat inside a bash command to create a file or append to an " +
		"existing file. Use respective tools provided by the " + agentName + " agent instead.\n" +
		"(b) DO NOT use bash commands like ls for listing, cat for viewing, grep for " +
		"string matching. Use respective tools provided by the " + agentName + " agent instead."
}

func applyIgnoreUnsupportedGenParams(call *lipapi.Call) {
	if call.Extensions == nil {
		call.Extensions = map[string]json.RawMessage{}
	}
	if _, ok := call.Extensions[extCodexIgnoreUnsupportedGenParamsKey]; ok {
		return
	}
	call.Extensions[extCodexIgnoreUnsupportedGenParamsKey] = json.RawMessage("true")
}
