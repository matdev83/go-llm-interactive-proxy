package lipapi

type Operation string

const (
	// OperationOpenAIChatCompletions identifies OpenAI-compatible Chat Completions requests.
	OperationOpenAIChatCompletions Operation = "openai.chat_completions"
	// OperationOpenAIResponses identifies OpenAI Responses API requests.
	OperationOpenAIResponses Operation = "openai.responses"
)

// DeliveryMode records whether the client requested streaming or non-streaming delivery.
type DeliveryMode string

const (
	// DeliveryModeStreaming means the client requested incremental response delivery.
	DeliveryModeStreaming DeliveryMode = "streaming"
	// DeliveryModeNonStreaming means the client requested one completed response body.
	DeliveryModeNonStreaming DeliveryMode = "non_streaming"
)

// Invocation carries protocol operation, delivery, and selected transport metadata from driving adapters to backends.
type Invocation struct {
	Operation     Operation
	DeliveryMode  DeliveryMode
	TransportMode TransportMode
}

// DeliveryModeFromClientStream converts protocol stream flags into canonical delivery mode metadata.
func DeliveryModeFromClientStream(stream bool) DeliveryMode {
	if stream {
		return DeliveryModeStreaming
	}
	return DeliveryModeNonStreaming
}
