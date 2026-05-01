package hooks

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ValidateCallAfterRequestHooks re-validates the canonical call after request-part hooks.
func ValidateCallAfterRequestHooks(hookID string, call *lipapi.Call) error {
	if call == nil {
		return &lipapi.HookMutationError{HookID: hookID, Details: "nil call"}
	}
	if err := call.Validate(); err != nil {
		return &lipapi.HookMutationError{
			HookID:  hookID,
			Details: "call failed validation after request part hook",
			Cause:   err,
		}
	}
	return nil
}

// ValidateEventAfterResponseHook checks a single event remains structurally legal after a response hook.
func ValidateEventAfterResponseHook(hookID string, ev *lipapi.Event) error {
	if ev == nil {
		return &lipapi.HookMutationError{HookID: hookID, Details: "nil event"}
	}
	switch ev.Kind {
	case lipapi.EventResponseStarted, lipapi.EventMessageStarted,
		lipapi.EventTextDelta, lipapi.EventReasoningDelta,
		lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished,
		lipapi.EventAssistantImageRef, lipapi.EventAssistantFileRef,
		lipapi.EventUsageDelta, lipapi.EventWarning, lipapi.EventError, lipapi.EventResponseFinished:
	default:
		return &lipapi.HookMutationError{HookID: hookID, Details: fmt.Sprintf("unknown event kind %q", ev.Kind)}
	}
	switch ev.Kind {
	case lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished:
		if ev.ToolCallID == "" {
			return &lipapi.HookMutationError{
				HookID:  hookID,
				Details: fmt.Sprintf("%s requires a non-empty tool call id", ev.Kind),
			}
		}
	}
	if err := lipapi.ValidateEventEnvelope(ev); err != nil {
		return &lipapi.HookMutationError{HookID: hookID, Details: err.Error(), Cause: err}
	}
	return nil
}

// ValidateToolEventAfterPolicy checks a tool-policy decision has not produced an illegal tool event.
func ValidateToolEventAfterPolicy(policyID string, te *lipapi.ToolEvent) error {
	if te == nil {
		return &lipapi.HookMutationError{HookID: policyID, Details: "nil tool event"}
	}
	switch te.Kind {
	case lipapi.ToolEventStarted, lipapi.ToolEventArgsDelta, lipapi.ToolEventFinished:
	default:
		return &lipapi.HookMutationError{HookID: policyID, Details: fmt.Sprintf("unknown tool event kind %q", te.Kind)}
	}
	if te.ToolCallID == "" {
		return &lipapi.HookMutationError{
			HookID:  policyID,
			Details: fmt.Sprintf("%s requires a non-empty tool call id", te.Kind),
		}
	}
	env := &lipapi.Event{
		Kind:       eventKindFromToolEventKind(te.Kind),
		ToolCallID: te.ToolCallID,
		ToolName:   te.ToolName,
		Delta:      te.ArgsDelta,
	}
	if err := lipapi.ValidateEventEnvelope(env); err != nil {
		return &lipapi.HookMutationError{HookID: policyID, Details: err.Error(), Cause: err}
	}
	return nil
}

func eventKindFromToolEventKind(k lipapi.ToolEventKind) lipapi.EventKind {
	switch k {
	case lipapi.ToolEventStarted:
		return lipapi.EventToolCallStarted
	case lipapi.ToolEventArgsDelta:
		return lipapi.EventToolCallArgsDelta
	case lipapi.ToolEventFinished:
		return lipapi.EventToolCallFinished
	default:
		return lipapi.EventKind(k)
	}
}
