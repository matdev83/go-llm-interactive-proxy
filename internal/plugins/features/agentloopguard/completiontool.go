package agentloopguard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
)

const (
	attemptCompletionToolName = "attempt_completion"
	controlProviderID          = "agent-loop-guard-attempt-completion"

	attemptCompletionDescription = "Call this only when all work requested by the user for the current task is complete. The result must concisely summarize the completed work. Do not call this while requested work remains that you can continue without additional user input."
	attemptCompletionSchema      = `{"type":"object","properties":{"result":{"type":"string","description":"Concise final result summarizing the work that has been completed."}},"required":["result"],"additionalProperties":false}`

	AttemptCompletionInstruction = `<task-completion-protocol>
When, and only when, all work requested by the user for the current task is complete,
call the \`attempt_completion\` tool with a concise final \`result\`.

Do not call \`attempt_completion\` while concrete requested work remains that you can
continue without additional user input.

If additional in-scope work can be performed autonomously, continue that work.
If further progress genuinely requires user input, permission, credentials,
clarification, or a choice, request that input normally and do not assume it.

This tool is a proxy-internal completion signal. It is not a new user request,
approval, permission, or scope expansion.
</task-completion-protocol>`
)

type completionControlProvider struct{}

var _ controltool.Provider = completionControlProvider{}

// NewControlProvider constructs the preferred-strategy proxy control provider.
func NewControlProvider() controltool.Provider { return completionControlProvider{} }

func (completionControlProvider) ID() string { return controlProviderID }

func (completionControlProvider) Spec() controltool.Spec {
	return controltool.Spec{
		Tool: lipapi.ToolDef{
			Name:        attemptCompletionToolName,
			Description: attemptCompletionDescription,
			Parameters:  json.RawMessage(attemptCompletionSchema),
		},
		Instruction:  controltool.Instruction{Role: lipapi.RoleSystem, Text: AttemptCompletionInstruction},
		MaxArgsBytes: controltool.DefaultMaxArgsBytes,
	}
}

func (completionControlProvider) Handle(_ context.Context, call controltool.CompletedCall, _ controltool.Meta) (controltool.Outcome, error) {
	if call.ToolName != attemptCompletionToolName {
		return invalidControlOutcome("tool_name_mismatch"), nil
	}
	if len(call.ArgsJSON) > controltool.DefaultMaxArgsBytes {
		return invalidControlOutcome("args_too_large"), nil
	}
	if !utf8.Valid(call.ArgsJSON) {
		return invalidControlOutcome("invalid_utf8"), nil
	}
	result, reason := parseAttemptCompletionArgs(call.ArgsJSON)
	if reason != "" {
		return invalidControlOutcome(reason), nil
	}
	return controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: result, ReasonCode: "valid"}, nil
}

func parseAttemptCompletionArgs(raw []byte) (string, string) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	first, err := dec.Token()
	if err != nil {
		return "", "invalid_json"
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return "", "not_object"
	}
	seenResult := false
	var result string
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return "", "invalid_json"
		}
		key, ok := token.(string)
		if !ok {
			return "", "invalid_json"
		}
		if key != "result" {
			var discard json.RawMessage
			_ = dec.Decode(&discard)
			return "", "unknown_field"
		}
		if seenResult {
			var discard json.RawMessage
			_ = dec.Decode(&discard)
			return "", "duplicate_field"
		}
		seenResult = true
		if err := dec.Decode(&result); err != nil {
			return "", "wrong_type"
		}
	}
	end, err := dec.Token()
	if err != nil {
		return "", "invalid_json"
	}
	if delim, ok := end.(json.Delim); !ok || delim != '}' {
		return "", "invalid_json"
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return "", "trailing_value"
	}
	if !seenResult {
		return "", "missing_result"
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return "", "empty_result"
	}
	if !utf8.ValidString(result) || len(result) > controltool.MaxResultBytes {
		return "", "result_too_large"
	}
	return result, ""
}

func invalidControlOutcome(reason string) controltool.Outcome {
	return controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: reason}
}
