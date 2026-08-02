package lipapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestOrderedCanonicalItems_TableValidation(t *testing.T) {
	tests := []struct {
		name    string
		call    lipapi.Call
		wantErr bool
		errSub  string
	}{
		{
			name: "valid message item",
			call: lipapi.Call{
				ID: "call-1",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindMessage,
						ID:     "item-1",
						Status: lipapi.ItemStatusCompleted,
						Role:   lipapi.RoleUser,
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "Hello world"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid tool call and result identity items",
			call: lipapi.Call{
				ID: "call-tools",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindToolCall,
						ID:     "tc-1",
						Status: lipapi.ItemStatusCompleted,
						ToolCall: &lipapi.ToolCallItem{
							CallID:    "call_abc123",
							Name:      "get_weather",
							Arguments: json.RawMessage(`{"location":"San Francisco"}`),
						},
					},
					{
						Kind:   lipapi.ItemKindToolResult,
						ID:     "tr-1",
						Status: lipapi.ItemStatusCompleted,
						ToolResult: &lipapi.ToolResultItem{
							CallID: "call_abc123",
							Name:   "get_weather",
							Output: `{"temperature": 68}`,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid item reference and compaction",
			call: lipapi.Call{
				ID: "call-ref-compact",
				Items: []lipapi.Item{
					{
						Kind:      lipapi.ItemKindItemReference,
						ID:        "ref-1",
						Status:    lipapi.ItemStatusCompleted,
						Reference: &lipapi.ItemReference{ID: "msg-prev-456"},
					},
					{
						Kind:   lipapi.ItemKindCompaction,
						ID:     "cmp-1",
						Status: lipapi.ItemStatusCompleted,
						Compaction: &lipapi.CompactionItem{
							EncapsulatedID: "enc-789",
							Dialect:        "compact.v1",
							Opaque:         json.RawMessage(`{"compressed":true}`),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid reasoning item",
			call: lipapi.Call{
				ID: "call-reasoning",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindReasoning,
						ID:     "rs-1",
						Status: lipapi.ItemStatusCompleted,
						Reasoning: &lipapi.ReasoningItem{
							Reasoning: &lipapi.ReasoningPart{
								Dialect: "standard",
								Text:    "Thinking steps...",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "malformed message item - missing role",
			call: lipapi.Call{
				ID: "call-err-1",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindMessage,
						Status: lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "No role"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "role is required",
		},
		{
			name: "malformed message item - empty content",
			call: lipapi.Call{
				ID: "call-err-2",
				Items: []lipapi.Item{
					{
						Kind:    lipapi.ItemKindMessage,
						Role:    lipapi.RoleUser,
						Status:  lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{},
					},
				},
			},
			wantErr: true,
			errSub:  "content part is required",
		},
		{
			name: "malformed tool call item - missing tool call struct",
			call: lipapi.Call{
				ID: "call-err-3",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindToolCall,
						Status: lipapi.ItemStatusCompleted,
					},
				},
			},
			wantErr: true,
			errSub:  "tool call data is required",
		},
		{
			name: "malformed tool call item - missing CallID",
			call: lipapi.Call{
				ID: "call-err-4",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindToolCall,
						Status: lipapi.ItemStatusCompleted,
						ToolCall: &lipapi.ToolCallItem{
							Name:      "get_weather",
							Arguments: json.RawMessage(`{}`),
						},
					},
				},
			},
			wantErr: true,
			errSub:  "call ID is required",
		},
		{
			name: "malformed item reference - missing reference struct or ID",
			call: lipapi.Call{
				ID: "call-err-5",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindItemReference,
						Status: lipapi.ItemStatusCompleted,
					},
				},
			},
			wantErr: true,
			errSub:  "reference ID is required",
		},
		{
			name: "malformed extension item - missing namespace",
			call: lipapi.Call{
				ID: "call-err-6",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindExtension,
						Status: lipapi.ItemStatusCompleted,
						Extension: &lipapi.OpaqueExtension{
							Namespace: "",
							Type:      "my_ext",
							Data:      json.RawMessage(`{}`),
						},
					},
				},
			},
			wantErr: true,
			errSub:  "namespace is required",
		},
		{
			name: "malformed extension item - invalid JSON data",
			call: lipapi.Call{
				ID: "call-err-7",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindExtension,
						Status: lipapi.ItemStatusCompleted,
						Extension: &lipapi.OpaqueExtension{
							Namespace: "org.lip",
							Type:      "custom",
							Data:      json.RawMessage(`invalid json`),
						},
					},
				},
			},
			wantErr: true,
			errSub:  "must be valid JSON",
		},
		{
			name: "variant violation - message item with tool_call field",
			call: lipapi.Call{
				ID: "call-var-1",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindMessage,
						Role:   lipapi.RoleUser,
						Status: lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "hi"},
						},
						ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "t1"},
					},
				},
			},
			wantErr: true,
			errSub:  "must not contain non-message variant fields",
		},
		{
			name: "variant violation - tool_call item with content",
			call: lipapi.Call{
				ID: "call-var-2",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindToolCall,
						Status: lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "invalid"},
						},
						ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "t1"},
					},
				},
			},
			wantErr: true,
			errSub:  "must not contain non-tool_call variant fields",
		},
		{
			name: "invalid enum - role",
			call: lipapi.Call{
				ID: "call-enum-1",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindMessage,
						Role:   lipapi.Role("superadmin"),
						Status: lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "hi"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "invalid role",
		},
		{
			name: "invalid enum - status",
			call: lipapi.Call{
				ID: "call-enum-2",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindMessage,
						Role:   lipapi.RoleUser,
						Status: lipapi.ItemStatus("pending"),
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "hi"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "invalid item status",
		},
		{
			name: "invalid phase on user message",
			call: lipapi.Call{
				ID: "call-phase-1",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindMessage,
						Role:   lipapi.RoleUser,
						Phase:  lipapi.AssistantPhaseCommentary,
						Status: lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "hi"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "phase is only allowed on assistant message items",
		},
		{
			name: "invalid phase on tool_call item",
			call: lipapi.Call{
				ID: "call-phase-2",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindToolCall,
						Phase:  lipapi.AssistantPhaseFinalAnswer,
						Status: lipapi.ItemStatusCompleted,
						ToolCall: &lipapi.ToolCallItem{
							CallID: "c1",
							Name:   "fn",
						},
					},
				},
			},
			wantErr: true,
			errSub:  "phase is only allowed on assistant message items",
		},
		{
			name: "tool result ambiguity - both output and parts set",
			call: lipapi.Call{
				ID: "call-ambiguity-1",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindToolCall,
						ID:     "tc-1",
						Status: lipapi.ItemStatusCompleted,
						ToolCall: &lipapi.ToolCallItem{
							CallID: "call-amb",
							Name:   "calc",
						},
					},
					{
						Kind:   lipapi.ItemKindToolResult,
						ID:     "tr-1",
						Status: lipapi.ItemStatusCompleted,
						ToolResult: &lipapi.ToolResultItem{
							CallID: "call-amb",
							Name:   "calc",
							Output: "42",
							Parts: []lipapi.ContentPart{
								{Kind: lipapi.ContentPartText, Text: "42"},
							},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "tool result must specify output or parts, not both",
		},
		{
			name: "tool result empty - neither output nor parts set",
			call: lipapi.Call{
				ID: "call-ambiguity-2",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindToolCall,
						ID:     "tc-1",
						Status: lipapi.ItemStatusCompleted,
						ToolCall: &lipapi.ToolCallItem{
							CallID: "call-amb2",
							Name:   "calc",
						},
					},
					{
						Kind:   lipapi.ItemKindToolResult,
						ID:     "tr-1",
						Status: lipapi.ItemStatusCompleted,
						ToolResult: &lipapi.ToolResultItem{
							CallID: "call-amb2",
							Name:   "calc",
						},
					},
				},
			},
			wantErr: true,
			errSub:  "tool result must specify output or parts",
		},
		{
			name: "extension namespace whitespace",
			call: lipapi.Call{
				ID: "call-ext-space",
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindExtension,
						Status: lipapi.ItemStatusCompleted,
						Extension: &lipapi.OpaqueExtension{
							Namespace: "invalid namespace",
							Type:      "t1",
							Data:      json.RawMessage(`{}`),
						},
					},
				},
			},
			wantErr: true,
			errSub:  "must not contain whitespace",
		},
		{
			name: "trajectory violation - duplicate item IDs",
			call: lipapi.Call{
				ID: "call-dup-id",
				Items: []lipapi.Item{
					{
						Kind:    lipapi.ItemKindMessage,
						ID:      "item-dup",
						Role:    lipapi.RoleUser,
						Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "1"}},
					},
					{
						Kind:    lipapi.ItemKindMessage,
						ID:      "item-dup",
						Role:    lipapi.RoleAssistant,
						Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "2"}},
					},
				},
			},
			wantErr: true,
			errSub:  "duplicate item ID",
		},
		{
			name: "trajectory violation - forward item reference",
			call: lipapi.Call{
				ID: "call-forward-ref",
				Items: []lipapi.Item{
					{
						Kind:      lipapi.ItemKindItemReference,
						ID:        "ref-1",
						Reference: &lipapi.ItemReference{ID: "future-item"},
					},
					{
						Kind:    lipapi.ItemKindMessage,
						ID:      "future-item",
						Role:    lipapi.RoleUser,
						Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "future"}},
					},
				},
			},
			wantErr: true,
			errSub:  "forward item reference",
		},
		{
			name: "trajectory violation - duplicate call ID in tool calls",
			call: lipapi.Call{
				ID: "call-dup-callid",
				Items: []lipapi.Item{
					{
						Kind:     lipapi.ItemKindToolCall,
						ID:       "tc-1",
						ToolCall: &lipapi.ToolCallItem{CallID: "c-dup", Name: "tool1"},
					},
					{
						Kind:     lipapi.ItemKindToolCall,
						ID:       "tc-2",
						ToolCall: &lipapi.ToolCallItem{CallID: "c-dup", Name: "tool2"},
					},
				},
			},
			wantErr: true,
			errSub:  "duplicate call ID",
		},
		{
			name: "trajectory violation - orphan tool result",
			call: lipapi.Call{
				ID: "call-orphan-result",
				Items: []lipapi.Item{
					{
						Kind:       lipapi.ItemKindToolResult,
						ID:         "tr-1",
						ToolResult: &lipapi.ToolResultItem{CallID: "unmatched-call", Output: "res"},
					},
				},
			},
			wantErr: true,
			errSub:  "orphan tool result",
		},
		{
			name: "empty items slice in item authority",
			call: lipapi.Call{
				ID:    "call-empty-items",
				Items: []lipapi.Item{},
			},
			wantErr: true,
			errSub:  "at least one item is required unless previous_response_id is set",
		},
		{
			name: "conflicting authority - items + messages",
			call: lipapi.Call{
				ID: "call-conflict-1",
				Messages: []lipapi.Message{
					{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("msg")}},
				},
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindMessage,
						Role:   lipapi.RoleUser,
						Status: lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "item"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "conflicting raw item and legacy message authorities",
		},
		{
			name: "conflicting authority - items + instructions",
			call: lipapi.Call{
				ID: "call-conflict-2",
				Instructions: []lipapi.Message{
					{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("sys")}},
				},
				Items: []lipapi.Item{
					{
						Kind:   lipapi.ItemKindMessage,
						Role:   lipapi.RoleUser,
						Status: lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{
							{Kind: lipapi.ContentPartText, Text: "item"},
						},
					},
				},
			},
			wantErr: true,
			errSub:  "conflicting raw item and legacy message authorities",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errSub) {
					t.Fatalf("expected error containing %q, got %v", tt.errSub, err)
				}
			}
		})
	}
}

func TestJSONDepthLimit(t *testing.T) {
	// Build JSON with 70 nested braces
	var sb strings.Builder
	for i := 0; i < 70; i++ {
		sb.WriteString(`{"a":`)
	}
	sb.WriteString("1")
	for i := 0; i < 70; i++ {
		sb.WriteString("}")
	}

	call := lipapi.Call{
		ID: "json-depth-test",
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindToolCall,
				Status: lipapi.ItemStatusCompleted,
				ToolCall: &lipapi.ToolCallItem{
					CallID:    "c1",
					Name:      "test",
					Arguments: json.RawMessage(sb.String()),
				},
			},
		},
	}

	err := call.Validate()
	if err == nil {
		t.Fatal("expected error for excessive JSON depth, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum JSON depth") {
		t.Fatalf("expected JSON depth error, got: %v", err)
	}
}

func TestHasItemAuthority(t *testing.T) {
	callLegacy := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	if callLegacy.HasItemAuthority() {
		t.Fatal("nil Items slice must be legacy message authority")
	}

	callItemEmpty := lipapi.Call{
		Items: []lipapi.Item{},
	}
	if !callItemEmpty.HasItemAuthority() {
		t.Fatal("non-nil Items slice must be item authority")
	}
}

func TestOrderedCanonicalItems_Bounds(t *testing.T) {
	tooManyItems := make([]lipapi.Item, lipapi.MaxItems+1)
	for i := range tooManyItems {
		tooManyItems[i] = lipapi.Item{
			Kind:   lipapi.ItemKindMessage,
			Role:   lipapi.RoleUser,
			Status: lipapi.ItemStatusCompleted,
			Content: []lipapi.ContentPart{
				{Kind: lipapi.ContentPartText, Text: fmt.Sprintf("item-%d", i)},
			},
		}
	}

	call := lipapi.Call{
		ID:    "bound-test",
		Items: tooManyItems,
	}

	err := call.Validate()
	if err == nil {
		t.Fatal("expected error for exceeding MaxItems, got nil")
	}
	if !strings.Contains(err.Error(), "at most 4096 items") {
		t.Fatalf("expected MaxItems error message, got: %v", err)
	}
}

func TestWalkers_OpaqueDataInspection(t *testing.T) {
	call := lipapi.Call{
		ID: "opaque-walk-test",
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindExtension,
				Status: lipapi.ItemStatusCompleted,
				Extension: &lipapi.OpaqueExtension{
					Namespace: "com.acme",
					Type:      "secret_payload",
					Data:      json.RawMessage(`{"secret":"api_key_12345"}`),
				},
			},
			{
				Kind:   lipapi.ItemKindToolCall,
				Status: lipapi.ItemStatusCompleted,
				ToolCall: &lipapi.ToolCallItem{
					CallID:    "call-1",
					Name:      "vault_read",
					Arguments: json.RawMessage(`{"key":"db_password"}`),
				},
			},
		},
	}

	var visitedOpaque []string
	err := lipapi.WalkCallOpaqueData(call, func(field string, data []byte) error {
		visitedOpaque = append(visitedOpaque, string(data))
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error walking opaque data: %v", err)
	}

	if len(visitedOpaque) != 2 {
		t.Fatalf("expected 2 opaque data visits, got %d: %v", len(visitedOpaque), visitedOpaque)
	}

	if !strings.Contains(visitedOpaque[0], "api_key_12345") || !strings.Contains(visitedOpaque[1], "db_password") {
		t.Fatalf("sensitive opaque data was not visited: %v", visitedOpaque)
	}
}

func TestCallReasoningPayloadBytes_ItemAuthority(t *testing.T) {
	call := lipapi.Call{
		ID: "reasoning-bytes-test",
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindReasoning,
				Status: lipapi.ItemStatusCompleted,
				Reasoning: &lipapi.ReasoningItem{
					Reasoning: &lipapi.ReasoningPart{
						Dialect:   "standard",
						Text:      "think1",                            // 6 bytes
						Signature: "sig1",                              // 4 bytes
						Opaque:    json.RawMessage(`{"opaque":"val"}`), // 16 bytes
					},
				},
			},
		},
	}

	bytesCount := lipapi.CallReasoningPayloadBytes(&call)
	expected := int64(6 + 4 + 16)
	if bytesCount != expected {
		t.Fatalf("expected CallReasoningPayloadBytes = %d, got %d", expected, bytesCount)
	}
}

func TestCloneCall_ItemAuthority(t *testing.T) {
	orig := lipapi.Call{
		ID: "clone-test",
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindExtension,
				Status: lipapi.ItemStatusCompleted,
				Extension: &lipapi.OpaqueExtension{
					Namespace: "ns.test",
					Type:      "type1",
					Data:      json.RawMessage(`{"a":1}`),
				},
			},
		},
	}

	cloned := lipapi.CloneCall(orig)

	// Mutate original data
	orig.Items[0].Extension.Data[0] = 'X'

	if string(cloned.Items[0].Extension.Data) != `{"a":1}` {
		t.Fatalf("cloned extension Data was mutated, got: %s", string(cloned.Items[0].Extension.Data))
	}
}
