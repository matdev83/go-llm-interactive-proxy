package runtime

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

// isOperationSupportsContinuation reports whether the A-side frontend operation
// can legally stitch a continuation leg onto the same logical response.
// Conservative: empty or unknown operation => false.
// Both streaming and non-streaming are legal if canonical collection supports
// continuation; legality is A-side protocol, not delivery mode.
func isOperationSupportsContinuation(op lipapi.Operation) bool {
	switch op {
	case lipapi.OperationOpenAIChatCompletions,
		lipapi.OperationOpenAIResponses,
		lipapi.OperationOpenResponsesCreate,
		lipapi.OperationAnthropicMessages,
		lipapi.OperationGeminiGenerateContent:
		return true
	default:
		return false
	}
}

func supportsContinuationForCall(call lipapi.Call) bool {
	return isOperationSupportsContinuation(call.Invocation.Operation)
}
