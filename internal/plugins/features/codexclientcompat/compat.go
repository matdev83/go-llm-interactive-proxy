package codexclientcompat

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	openCodeBridgeMarker = "OpenCode compatibility mode"
	piBridgeMarker       = "Pi compatibility mode"
	droidBridgeMarker    = "Factory Droid compatibility mode"

	extAgentKey      = "agent"
	extUserAgentKey  = "user_agent"
	extCodexAgentKey = "openai_codex.agent"
	extHeadersKey    = "headers"

	// ponytail: mirrors openaicodex default when instructions empty so bridge appends after base Codex prompt.
	codexDefaultInstruction = "You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer."
)

var (
	piPromptMarkers = []string{
		"operating inside pi",
		"coding agent harness",
		"available tools:",
		"in addition to the tools above",
		"guidelines:",
	}
	piUserAgentMarkers = []string{
		"@mariozechner/pi-coding-agent",
		" pi/",
		"pi-coding-agent",
	}
	droidNativeToolNames = map[string]struct{}{
		"Read": {}, "LS": {}, "Execute": {}, "Edit": {}, "Grep": {}, "Glob": {},
		"Create": {}, "TodoWrite": {}, "WebSearch": {}, "FetchUrl": {}, "ExitSpecMode": {},
	}
	droidSystemPromptKeywords = []string{
		"you are droid",
		"droid, an ai",
		"factory droid",
	}
	droidUserAgentTokens = []string{"factory-cli", "factory_cli", "factorydroid", "droid"}
)

type compatInput struct {
	agents []string
	prompt string
	tools  []string
}

type compatBridge struct {
	marker      string
	detect      func(compatInput) bool
	filter      func(string) bool
	build       func(*lipapi.Call) string
	beforeApply func(*lipapi.Call)
}

var compatBridges = []compatBridge{
	{
		marker:      openCodeBridgeMarker,
		detect:      isOpenCode,
		filter:      isOpenCodeHarnessText,
		build:       func(*lipapi.Call) string { return buildOpenCodeBridge() },
		beforeApply: convertOrphanedToolResults,
	},
	{
		marker: piBridgeMarker,
		detect: isPi,
		filter: isPiHarnessText,
		build:  func(*lipapi.Call) string { return buildPiBridge() },
	},
	{
		marker: droidBridgeMarker,
		detect: isDroid,
		filter: isDroidHarnessText,
		build:  func(call *lipapi.Call) string { return buildDroidBridge(collectCallToolNames(call)) },
	},
}

func ApplyCompat(call *lipapi.Call) {
	if call == nil {
		return
	}
	in := detectCompatInput(call)
	hasTools := len(call.Tools) > 0
	for _, bridge := range compatBridges {
		if !bridge.detect(in) {
			continue
		}
		call.Messages = filterHarnessMessages(call.Messages, bridge.filter)
		call.Instructions = filterHarnessMessages(call.Instructions, bridge.filter)
		if bridge.beforeApply != nil {
			bridge.beforeApply(call)
		}
		if !hasTools {
			continue
		}
		block := bridge.build(call)
		appendBridgeInstructions(call, bridge.marker, block)
		prependBridgeMessage(call, bridge.marker, block)
	}
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

func isOpenCode(in compatInput) bool {
	for _, candidate := range in.agents {
		if strings.Contains(strings.ToLower(candidate), "opencode") {
			return true
		}
	}
	lower := strings.ToLower(in.prompt)
	if strings.Contains(lower, "opencode") {
		if strings.Contains(lower, "compatibility") || strings.Contains(lower, "harness") || strings.Contains(lower, "tool") {
			return true
		}
	}
	return false
}

func isPi(in compatInput) bool {
	for _, candidate := range in.agents {
		lower := strings.ToLower(candidate)
		for _, marker := range piUserAgentMarkers {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	lower := strings.ToLower(in.prompt)
	hits := 0
	for _, marker := range piPromptMarkers {
		if strings.Contains(lower, marker) {
			hits++
		}
	}
	return hits >= 2
}

func isDroid(in compatInput) bool {
	if slices.ContainsFunc(in.agents, droidUserAgentMatch) {
		return true
	}
	lower := strings.ToLower(in.prompt)
	for _, keyword := range droidSystemPromptKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	found := 0
	for _, name := range in.tools {
		if _, ok := droidNativeToolNames[name]; ok {
			found++
		}
	}
	return found >= 2
}

func droidUserAgentMatch(userAgent string) bool {
	lower := strings.ToLower(userAgent)
	for _, pattern := range droidUserAgentTokens {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
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

func isOpenCodeHarnessText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "opencode") && strings.Contains(lower, "tool")
}

func isPiHarnessText(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range piPromptMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isDroidHarnessText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "factory droid") && strings.Contains(lower, "execute") && strings.Contains(lower, "todowrite")
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

func convertOrphanedToolResults(call *lipapi.Call) {
	known := collectKnownToolCallIDs(call.Messages)
	out := make([]lipapi.Message, 0, len(call.Messages))
	for _, m := range call.Messages {
		if m.Role != lipapi.RoleTool {
			out = append(out, m)
			continue
		}
		var kept []lipapi.Part
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartToolResult {
				kept = append(kept, p)
				continue
			}
			callID := strings.TrimSpace(p.ToolCallID)
			if callID != "" {
				if _, ok := known[callID]; ok {
					kept = append(kept, p)
					continue
				}
			}
			out = append(out, convertOrphanedToolResult(p))
		}
		if len(kept) > 0 {
			out = append(out, lipapi.Message{Role: lipapi.RoleTool, Parts: kept})
		}
	}
	call.Messages = out
}

func collectKnownToolCallIDs(msgs []lipapi.Message) map[string]struct{} {
	known := make(map[string]struct{})
	for _, m := range msgs {
		if m.Role != lipapi.RoleAssistant {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartJSON {
				continue
			}
			var fc struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				ID     string `json:"id"`
			}
			if json.Unmarshal(p.Content, &fc) != nil {
				continue
			}
			if !strings.EqualFold(fc.Type, "function_call") {
				continue
			}
			id := strings.TrimSpace(fc.CallID)
			if id == "" {
				id = strings.TrimSpace(fc.ID)
			}
			if id != "" {
				known[id] = struct{}{}
			}
		}
	}
	return known
}

func convertOrphanedToolResult(p lipapi.Part) lipapi.Message {
	rendered := string(p.Content)
	if len(p.Content) == 0 {
		rendered = ""
	}
	header := "Prior tool output (original tool call reference unavailable)."
	if id := strings.TrimSpace(p.ToolCallID); id != "" {
		header += " call_id=" + id + "."
	}
	return lipapi.Message{
		Role:  lipapi.RoleSystem,
		Parts: []lipapi.Part{lipapi.TextPart(header + "\n" + rendered)},
	}
}

func buildOpenCodeBridge() string {
	return openCodeBridgeMarker + ":\n" +
		"- Prefer the available client shell tool when command execution is needed.\n" +
		"- For bash-style tools, arguments MUST be a JSON object with string " +
		"`command` and string `description`.\n" +
		"- Bash-style tools MAY include numeric `timeout` in milliseconds " +
		"and string `workdir` when the client schema exposes them.\n" +
		"- Never emit array-valued `command` arguments for shell execution.\n" +
		"- Do not use `apply_patch`; use the client's native file editing tools instead.\n" +
		"- Do not use `update_plan` or `read_plan`; use the client's task tools instead.\n" +
		"- If you need a working directory, prefer `workdir` over `cd` commands " +
		"or embedding cwd text in `description`.\n" +
		"\n" +
		criticalInstruction("OpenCode")
}

func buildPiBridge() string {
	return piBridgeMarker + ":\n" +
		"- Use only tools exposed by the pi client for this session.\n" +
		"- Use `bash` for terminal execution with a JSON object containing string `command` and optional numeric `timeout` in seconds; pi has no default timeout.\n" +
		"- Do not emit `shell`, `local_shell_call`, or array-valued shell commands; pi expects the `bash` tool name.\n" +
		"- Do not use `apply_patch`; use pi's `edit` tool for exact text replacement in a single file.\n" +
		"- For `edit`, pass `path` plus an `edits` array of replacements with `oldText` and `newText`, each matched against the original file.\n" +
		"- For file reads use `read` with `path` and optional `offset`/`limit`; for full rewrites use `write` with `path` and `content`.\n" +
		"- Keep responses concise and show file paths clearly.\n" +
		"\n" +
		criticalInstruction("pi")
}

func buildDroidBridge(availableTools []string) string {
	native := sortedNativeDroidTools()
	available := availableTools
	if len(available) == 0 {
		available = native
	}
	availableText := joinBacktickList(available)
	nativeText := joinBacktickList(native)
	return droidBridgeMarker + ":\n" +
		"- This session is using Factory Droid tools, not Codex-native tools.\n" +
		"- Use only tool names that are actually available in this session: " + availableText + ".\n" +
		"- Prefer the native Factory Droid tool family when available: " + nativeText + ".\n" +
		"- Use Droid argument shapes exactly for the native file/execute tools: `Read(file_path, offset?, limit?)`, `LS(directory_path?)`, `Execute(command, timeout?, cwd?)`, `Edit(file_path, old_str, new_str)`, `Grep(pattern, path?, file_pattern?, max_results?)`, `Glob(pattern, max_results?)`, `Create(file_path, content)`.\n" +
		"- Do not emit Codex-native tool names such as `read`, `read_file`, `bash`, `shell`, `apply_patch`, `grep_files`, or `list_dir`.\n" +
		"- Use `TodoWrite` instead of Codex task-planner tools, `WebSearch` for web search, and `FetchUrl` for direct URL fetches when those tools are available.\n" +
		"- Keep tool arguments as JSON objects; for `Execute`, the `command` value must be a single shell command string, not an array.\n" +
		"\n" +
		criticalInstruction("Droid")
}

func criticalInstruction(agentName string) string {
	return "CRITICAL INSTRUCTION:\n" +
		"(a) NEVER run cat inside a bash command to create a file or append to an " +
		"existing file. Use respective tools provided by the " + agentName + " agent instead.\n" +
		"(b) DO NOT use bash commands like ls for listing, cat for viewing, grep for " +
		"string matching. Use respective tools provided by the " + agentName + " agent instead."
}

func sortedNativeDroidTools() []string {
	out := make([]string, 0, len(droidNativeToolNames))
	for name := range droidNativeToolNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func joinBacktickList(names []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = "`" + name + "`"
	}
	return strings.Join(parts, ", ")
}
