package capabilityfacts_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/capabilityfacts"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/require"
)

func TestDeriveRequiredCapabilities(t *testing.T) {
	t.Parallel()

	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		turn     capabilityfacts.TurnShape
		ctrl     capabilityfacts.ControlRequirements
		expected []lipapi.Capability
	}{
		{
			name: "plain streaming text",
			ctrl: capabilityfacts.ControlRequirements{
				Delivery: lipapi.DeliveryModeStreaming,
			},
			expected: []lipapi.Capability{lipapi.CapabilityStreaming},
		},
		{
			name: "tools and parallel tool calls",
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          lipapi.DeliveryModeStreaming,
				HasTools:          true,
				ParallelToolCalls: &trueVal,
			},
			expected: []lipapi.Capability{
				lipapi.CapabilityStreaming,
				lipapi.CapabilityTools,
				lipapi.CapabilityParallelToolCalls,
			},
		},
		{
			name: "parallel tool calls false not included",
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          lipapi.DeliveryModeStreaming,
				HasTools:          true,
				ParallelToolCalls: &falseVal,
			},
			expected: []lipapi.Capability{
				lipapi.CapabilityStreaming,
				lipapi.CapabilityTools,
			},
		},
		{
			name: "reasoning effort and structured outputs",
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          lipapi.DeliveryModeStreaming,
				ReasoningEffort:   "high",
				StructuredOutputs: true,
			},
			expected: []lipapi.Capability{
				lipapi.CapabilityStreaming,
				lipapi.CapabilityStructuredOutputs,
				lipapi.CapabilityReasoning,
			},
		},
		{
			name: "vision and reasoning replay in turn shape",
			turn: capabilityfacts.TurnShape{
				Items: []capabilityfacts.TurnItemShape{
					{
						Role: lipapi.RoleUser,
						Parts: []capabilityfacts.TurnPartShape{
							{Kind: lipapi.ContentPartImageRef},
						},
					},
					{
						Role: lipapi.RoleAssistant,
						Parts: []capabilityfacts.TurnPartShape{
							{Kind: lipapi.ContentPartReasoning},
						},
					},
				},
			},
			ctrl: capabilityfacts.ControlRequirements{
				Delivery: lipapi.DeliveryModeStreaming,
			},
			expected: []lipapi.Capability{
				lipapi.CapabilityStreaming,
				lipapi.CapabilityVision,
				lipapi.CapabilityReasoningReplay,
			},
		},
		{
			name: "all multimedia content parts in tool result and messages",
			turn: capabilityfacts.TurnShape{
				Items: []capabilityfacts.TurnItemShape{
					{
						Kind: lipapi.ItemKindMessage,
						Role: lipapi.RoleUser,
						Parts: []capabilityfacts.TurnPartShape{
							{Kind: lipapi.ContentPartFileRef},
							{Kind: lipapi.ContentPartVideoRef},
						},
					},
					{
						Kind: lipapi.ItemKindToolResult,
						Parts: []capabilityfacts.TurnPartShape{
							{Kind: lipapi.ContentPartImageRef},
							{Kind: lipapi.ContentPartExtension},
						},
					},
				},
			},
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          lipapi.DeliveryModeStreaming,
				ItemAuthoritative: true,
			},
			expected: []lipapi.Capability{
				lipapi.CapabilityStreaming,
				lipapi.CapabilityDocuments,
				lipapi.CapabilityVideoInput,
				lipapi.CapabilityTools,
				lipapi.CapabilityVision,
				lipapi.CapabilityOpaqueExtensions,
				lipapi.CapabilityOrderedItems,
			},
		},
		{
			name: "item references, reasoning items, and extension items",
			turn: capabilityfacts.TurnShape{
				Items: []capabilityfacts.TurnItemShape{
					{Kind: lipapi.ItemKindItemReference},
					{Kind: lipapi.ItemKindReasoning},
					{Kind: lipapi.ItemKindExtension},
				},
			},
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          lipapi.DeliveryModeStreaming,
				ItemAuthoritative: true,
			},
			expected: []lipapi.Capability{
				lipapi.CapabilityStreaming,
				lipapi.CapabilityOrderedItems,
			},
		},
		{
			name: "openresponses item authoritative with tool call",
			turn: capabilityfacts.TurnShape{
				Items: []capabilityfacts.TurnItemShape{
					{
						Kind: lipapi.ItemKindToolCall,
					},
				},
			},
			ctrl: capabilityfacts.ControlRequirements{
				Delivery:          lipapi.DeliveryModeStreaming,
				ItemAuthoritative: true,
			},
			expected: []lipapi.Capability{
				lipapi.CapabilityStreaming,
				lipapi.CapabilityTools,
				lipapi.CapabilityOrderedItems,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := capabilityfacts.DeriveRequiredCapabilities(tc.turn, tc.ctrl)
			for _, exp := range tc.expected {
				require.True(t, slices.Contains(got, exp), "expected capability %s in got %v", exp, got)
			}
			require.Equal(t, len(tc.expected), len(got), "expected len %d, got %v", len(tc.expected), got)
		})
	}
}

func TestCapabilitiesDigest(t *testing.T) {
	t.Parallel()

	d1 := capabilityfacts.CapabilitiesDigest([]lipapi.Capability{lipapi.CapabilityStreaming, lipapi.CapabilityTools})
	d2 := capabilityfacts.CapabilitiesDigest([]lipapi.Capability{lipapi.CapabilityTools, lipapi.CapabilityStreaming})
	require.Equal(t, d1, d2, "digest must be order-independent")

	dEmpty1 := capabilityfacts.CapabilitiesDigest(nil)
	dEmpty2 := capabilityfacts.CapabilitiesDigest([]lipapi.Capability{})
	require.Equal(t, dEmpty1, dEmpty2, "nil and empty must match")
	require.NotEqual(t, [32]byte{}, dEmpty1, "empty digest must be non-zero")
	require.NotEqual(t, d1, dEmpty1, "different caps must produce different digests")
}

func TestValidateCapabilities(t *testing.T) {
	t.Parallel()

	require.NoError(t, capabilityfacts.ValidateCapabilities([]lipapi.Capability{lipapi.CapabilityStreaming, lipapi.CapabilityTools}, 1024))
	require.Error(t, capabilityfacts.ValidateCapabilities([]lipapi.Capability{""}, 1024))
	require.Error(t, capabilityfacts.ValidateCapabilities([]lipapi.Capability{"   "}, 1024))
	require.Error(t, capabilityfacts.ValidateCapabilities([]lipapi.Capability{"long-capability-name"}, 5))
}
