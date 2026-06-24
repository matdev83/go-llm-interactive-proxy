package openaifamily

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type TransportPolicy int

const (
	TransportChatOnly TransportPolicy = iota
	TransportChatAndResponses
)

func ChatOnlyTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}

func ChatAndResponsesTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIResponses,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}

func TransportCaps(policy TransportPolicy) lipapi.BackendTransportCaps {
	switch policy {
	case TransportChatAndResponses:
		return ChatAndResponsesTransportCaps()
	default:
		return ChatOnlyTransportCaps()
	}
}
