package conversationview_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestMessageIdentity_FormatAndParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         string
		wantValid   bool
		wantVersion string
		wantDigest  string
	}{
		{
			name:        "valid v1 identity",
			raw:         "v1:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantValid:   true,
			wantVersion: "v1",
			wantDigest:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:      "empty string",
			raw:       "",
			wantValid: false,
		},
		{
			name:      "missing prefix",
			raw:       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantValid: false,
		},
		{
			name:      "unsupported version",
			raw:       "v2:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			wantValid: false,
		},
		{
			name:      "uppercase hex rejected",
			raw:       "v1:E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855",
			wantValid: false,
		},
		{
			name:      "too short digest",
			raw:       "v1:e3b0c44298fc1c14",
			wantValid: false,
		},
		{
			name:      "invalid hex characters",
			raw:       "v1:g3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85z",
			wantValid: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := conversationview.MessageIdentity(tc.raw)
			if tc.wantValid {
				require.NoError(t, id.Validate())
				assert.True(t, id.IsValid())
				assert.Equal(t, tc.wantVersion, id.Version())
				assert.Equal(t, tc.wantDigest, id.Digest())
				assert.Equal(t, tc.raw, id.String())

				parsed, err := conversationview.ParseMessageIdentity(tc.raw)
				require.NoError(t, err)
				assert.Equal(t, id, parsed)
			} else {
				assert.Error(t, id.Validate())
				assert.False(t, id.IsValid())

				_, err := conversationview.ParseMessageIdentity(tc.raw)
				assert.Error(t, err)
			}
		})
	}
}

func TestMessageIdentity_LegacyAndItemAuthorityEquivalence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		legacy lipapi.Message
		item   lipapi.Item
		role   lipapi.Role
	}{
		{
			name: "plain text user message",
			legacy: lipapi.Message{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "Hello, world!"},
				},
			},
			item: lipapi.Item{
				Kind:   lipapi.ItemKindMessage,
				ID:     "item-12345",
				Status: lipapi.ItemStatusCompleted,
				Role:   lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "Hello, world!"},
				},
			},
		},
		{
			name: "system instructions message",
			legacy: lipapi.Message{
				Role: lipapi.RoleSystem,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "You are a helpful coding assistant."},
				},
			},
			item: lipapi.Item{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleSystem,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "You are a helpful coding assistant."},
				},
			},
		},
		{
			name: "assistant commentary message with multiple text parts",
			legacy: lipapi.Message{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "Thinking step 1..."},
					{Kind: lipapi.PartText, Text: "Concluding response."},
				},
			},
			item: lipapi.Item{
				Kind:  lipapi.ItemKindMessage,
				Role:  lipapi.RoleAssistant,
				Phase: lipapi.AssistantPhaseCommentary,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "Thinking step 1..."},
					{Kind: lipapi.ContentPartText, Text: "Concluding response."},
				},
			},
		},
		{
			name: "file ref message",
			legacy: lipapi.Message{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartFileRef, FileRef: "file-ref-1", FileMIME: "text/plain", FileName: "doc.txt"},
				},
			},
			item: lipapi.Item{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartFileRef, FileRef: "file-ref-1", FileMIME: "text/plain", FileName: "doc.txt"},
				},
			},
		},
		{
			name: "tool result message",
			legacy: lipapi.Message{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartToolResult, ToolCallID: "call_abc", ToolName: "calc", Text: "42"},
				},
			},
			item: lipapi.Item{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleTool,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartToolResult, Text: "42"},
				},
			},
		},
		{
			name: "json content message",
			legacy: lipapi.Message{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartJSON, Content: json.RawMessage(`{"status":"ok","code":200}`)},
				},
			},
			item: lipapi.Item{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{
						Kind:       lipapi.ContentPartJSON,
						Text:       `{"code":200,"status":"ok"}`,
						Annotation: &lipapi.AnnotationPart{Type: "json_content", Data: json.RawMessage(`{"code":200,"status":"ok"}`)},
					},
				},
			},
		},
		{
			name: "reasoning part assistant message",
			legacy: lipapi.Message{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{
						Kind: lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect:   "standard",
							Text:      "internal thinking",
							Signature: "sig_abc",
						},
					},
				},
			},
			item: lipapi.Item{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleAssistant,
				Content: []lipapi.ContentPart{
					{
						Kind: lipapi.ContentPartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect:   "standard",
							Text:      "internal thinking",
							Signature: "sig_abc",
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacyID, err := conversationview.MessageIdentityOf(tc.legacy)
			require.NoError(t, err)
			require.True(t, legacyID.IsValid(), "legacy identity must be valid v1 identity")

			itemID, err := conversationview.ItemIdentityOf(tc.item)
			require.NoError(t, err)
			require.True(t, itemID.IsValid(), "item identity must be valid v1 identity")

			assert.Equal(t, legacyID, itemID, "legacy message and item message must produce identical semantic identity")
		})
	}
}

func TestMessageIdentity_LineEndingNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		crlfMsg     lipapi.Message
		lfMsg       lipapi.Message
		crMsg       lipapi.Message
		distinctMsg lipapi.Message
	}{
		{
			name: "text with CRLF vs LF vs CR",
			crlfMsg: lipapi.Message{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Line 1\r\nLine 2\r\nLine 3"}},
			},
			lfMsg: lipapi.Message{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Line 1\nLine 2\nLine 3"}},
			},
			crMsg: lipapi.Message{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Line 1\rLine 2\rLine 3"}},
			},
			distinctMsg: lipapi.Message{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Line 1 Line 2 Line 3"}},
			},
		},
		{
			name: "multiline reasoning text with CRLF vs LF",
			crlfMsg: lipapi.Message{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{
						Kind: lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect: "standard",
							Text:    "First line\r\nSecond line\r\n",
						},
					},
				},
			},
			lfMsg: lipapi.Message{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{
						Kind: lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect: "standard",
							Text:    "First line\nSecond line\n",
						},
					},
				},
			},
			crMsg: lipapi.Message{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{
						Kind: lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect: "standard",
							Text:    "First line\rSecond line\r",
						},
					},
				},
			},
			distinctMsg: lipapi.Message{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{
						Kind: lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{
							Dialect: "standard",
							Text:    "First line Second line",
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			crlfID, err := conversationview.MessageIdentityOf(tc.crlfMsg)
			require.NoError(t, err)

			lfID, err := conversationview.MessageIdentityOf(tc.lfMsg)
			require.NoError(t, err)

			crID, err := conversationview.MessageIdentityOf(tc.crMsg)
			require.NoError(t, err)

			distinctID, err := conversationview.MessageIdentityOf(tc.distinctMsg)
			require.NoError(t, err)

			assert.Equal(t, lfID, crlfID, "CRLF and LF must yield identical identity")
			assert.Equal(t, lfID, crID, "CR and LF must yield identical identity")
			assert.NotEqual(t, lfID, distinctID, "distinct text must yield distinct identity")
		})
	}
}

func TestMessageIdentity_WhitespaceAndUnicodePreservation(t *testing.T) {
	t.Parallel()

	// Whitespace other than CRLF/CR must be preserved strictly
	msg1 := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Hello "}}}
	msg2 := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Hello"}}}
	msg3 := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Hello\tWorld"}}}
	msg4 := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Hello World"}}}

	id1, err := conversationview.MessageIdentityOf(msg1)
	require.NoError(t, err)
	id2, err := conversationview.MessageIdentityOf(msg2)
	require.NoError(t, err)
	id3, err := conversationview.MessageIdentityOf(msg3)
	require.NoError(t, err)
	id4, err := conversationview.MessageIdentityOf(msg4)
	require.NoError(t, err)

	assert.NotEqual(t, id1, id2, "trailing whitespace difference must produce distinct identities")
	assert.NotEqual(t, id3, id4, "tab vs space must produce distinct identities")

	// Unicode characters preserved identically
	unicodeMsg1 := lipapi.Message{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "你好，世界 🌍 Übergröße"}},
	}
	unicodeMsg2 := lipapi.Message{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "你好，世界 🌍 Übergröße"}},
	}
	uID1, err := conversationview.MessageIdentityOf(unicodeMsg1)
	require.NoError(t, err)
	uID2, err := conversationview.MessageIdentityOf(unicodeMsg2)
	require.NoError(t, err)

	assert.Equal(t, uID1, uID2, "identical unicode messages must produce identical identities")
}

func TestMessageIdentity_StructuredJSONCanonicalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw1 string
		raw2 string
	}{
		{
			name: "flat object with reversed keys",
			raw1: `{"b": 2, "a": 1}`,
			raw2: `{"a": 1, "b": 2}`,
		},
		{
			name: "nested object with whitespace differences",
			raw1: `{"outer": {"z": true, "a": [1, 2]}, "count": 10}`,
			raw2: "{\n  \"count\": 10,\n  \"outer\": {\n    \"a\": [1, 2],\n    \"z\": true\n  }\n}",
		},
		{
			name: "deeply nested objects",
			raw1: `{"d":{"c":{"b":{"a":100}}}}`,
			raw2: `{"d": { "c": { "b": { "a": 100 } } } }`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg1 := lipapi.Message{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartJSON, Content: json.RawMessage(tc.raw1)},
				},
			}
			msg2 := lipapi.Message{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartJSON, Content: json.RawMessage(tc.raw2)},
				},
			}

			id1, err := conversationview.MessageIdentityOf(msg1)
			require.NoError(t, err)
			id2, err := conversationview.MessageIdentityOf(msg2)
			require.NoError(t, err)

			assert.Equal(t, id1, id2, "semantically equivalent JSON with different key orders or whitespace must produce identical identity")
		})
	}
}

func TestMessageIdentity_TransientFieldExclusion(t *testing.T) {
	t.Parallel()

	// Base message
	baseItem := lipapi.Item{
		Kind:   lipapi.ItemKindMessage,
		ID:     "transport-item-1",
		Status: lipapi.ItemStatusInProgress,
		Phase:  lipapi.AssistantPhaseCommentary,
		Role:   lipapi.RoleAssistant,
		Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: "Assistant response"},
		},
	}

	// Message with different transport ID, completed status, final answer phase
	variantItem := lipapi.Item{
		Kind:   lipapi.ItemKindMessage,
		ID:     "different-generated-id-999",
		Status: lipapi.ItemStatusCompleted,
		Phase:  lipapi.AssistantPhaseFinalAnswer,
		Role:   lipapi.RoleAssistant,
		Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: "Assistant response"},
		},
	}

	idBase, err := conversationview.ItemIdentityOf(baseItem)
	require.NoError(t, err)

	idVariant, err := conversationview.ItemIdentityOf(variantItem)
	require.NoError(t, err)

	assert.Equal(t, idBase, idVariant, "transient carriers (ID, Status, Phase) must be excluded from semantic identity")

	// Legacy message with proxy metadata
	legacy1 := lipapi.Message{
		Role:     lipapi.RoleUser,
		Parts:    []lipapi.Part{{Kind: lipapi.PartText, Text: "Hello"}},
		Metadata: map[string]string{"proxy_trace_id": "tr-123", "b_leg": "bleg-456"},
	}
	legacy2 := lipapi.Message{
		Role:     lipapi.RoleUser,
		Parts:    []lipapi.Part{{Kind: lipapi.PartText, Text: "Hello"}},
		Metadata: nil,
	}

	idLeg1, err := conversationview.MessageIdentityOf(legacy1)
	require.NoError(t, err)
	idLeg2, err := conversationview.MessageIdentityOf(legacy2)
	require.NoError(t, err)

	assert.Equal(t, idLeg1, idLeg2, "Message.Metadata must be excluded from semantic identity")
}

func TestMessageIdentity_RoleAndOrderingSensitivity(t *testing.T) {
	t.Parallel()

	// Role difference
	userMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "ping"}}}
	sysMsg := lipapi.Message{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "ping"}}}
	asstMsg := lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "ping"}}}

	uID, err := conversationview.MessageIdentityOf(userMsg)
	require.NoError(t, err)
	sID, err := conversationview.MessageIdentityOf(sysMsg)
	require.NoError(t, err)
	aID, err := conversationview.MessageIdentityOf(asstMsg)
	require.NoError(t, err)

	assert.NotEqual(t, uID, sID, "different roles must produce different identities")
	assert.NotEqual(t, uID, aID, "different roles must produce different identities")
	assert.NotEqual(t, sID, aID, "different roles must produce different identities")

	// Part ordering sensitivity
	order1 := lipapi.Message{
		Role: lipapi.RoleUser,
		Parts: []lipapi.Part{
			{Kind: lipapi.PartText, Text: "Part 1"},
			{Kind: lipapi.PartText, Text: "Part 2"},
		},
	}
	order2 := lipapi.Message{
		Role: lipapi.RoleUser,
		Parts: []lipapi.Part{
			{Kind: lipapi.PartText, Text: "Part 2"},
			{Kind: lipapi.PartText, Text: "Part 1"},
		},
	}

	oID1, err := conversationview.MessageIdentityOf(order1)
	require.NoError(t, err)
	oID2, err := conversationview.MessageIdentityOf(order2)
	require.NoError(t, err)

	assert.NotEqual(t, oID1, oID2, "part ordering difference must produce different identities")
}

func TestMessageIdentity_NegativesAndRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		item      lipapi.Item
		wantErrIs error
	}{
		{
			name: "tool call item rejected",
			item: lipapi.Item{
				Kind: lipapi.ItemKindToolCall,
				ToolCall: &lipapi.ToolCallItem{
					CallID:    "call_1",
					Name:      "test_tool",
					Arguments: json.RawMessage(`{}`),
				},
			},
			wantErrIs: conversationview.ErrNonMessageItem,
		},
		{
			name: "tool result item rejected",
			item: lipapi.Item{
				Kind: lipapi.ItemKindToolResult,
				ToolResult: &lipapi.ToolResultItem{
					CallID: "call_1",
					Name:   "test_tool",
					Output: "result",
				},
			},
			wantErrIs: conversationview.ErrNonMessageItem,
		},
		{
			name: "reasoning item rejected",
			item: lipapi.Item{
				Kind: lipapi.ItemKindReasoning,
				Reasoning: &lipapi.ReasoningItem{
					Reasoning: &lipapi.ReasoningPart{
						Dialect: "standard",
						Text:    "reasoning",
					},
				},
			},
			wantErrIs: conversationview.ErrNonMessageItem,
		},
		{
			name: "compaction item rejected",
			item: lipapi.Item{
				Kind: lipapi.ItemKindCompaction,
				Compaction: &lipapi.CompactionItem{
					EncapsulatedID: "comp-1",
				},
			},
			wantErrIs: conversationview.ErrNonMessageItem,
		},
		{
			name: "extension item rejected",
			item: lipapi.Item{
				Kind: lipapi.ItemKindExtension,
				Extension: &lipapi.OpaqueExtension{
					Namespace: "acme",
					Type:      "acme:ext",
				},
			},
			wantErrIs: conversationview.ErrNonMessageItem,
		},
		{
			name: "item reference rejected",
			item: lipapi.Item{
				Kind:      lipapi.ItemKindItemReference,
				Reference: &lipapi.ItemReference{ID: "msg-1"},
			},
			wantErrIs: conversationview.ErrNonMessageItem,
		},
		{
			name: "empty item kind rejected",
			item: lipapi.Item{
				Kind: "",
			},
			wantErrIs: conversationview.ErrNonMessageItem,
		},
		{
			name: "message item with empty content rejected",
			item: lipapi.Item{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleUser,
				Content: []lipapi.ContentPart{},
			},
			wantErrIs: conversationview.ErrEmptyMessage,
		},
		{
			name: "message item with missing role rejected",
			item: lipapi.Item{
				Kind: lipapi.ItemKindMessage,
				Role: "",
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "hello"},
				},
			},
			wantErrIs: conversationview.ErrInvalidRole,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := conversationview.ItemIdentityOf(tc.item)
			require.Error(t, err)
			if tc.wantErrIs != nil {
				assert.ErrorIs(t, err, tc.wantErrIs)
			}
		})
	}

	// Legacy empty message rejected
	_, err := conversationview.MessageIdentityOf(lipapi.Message{Role: lipapi.RoleUser, Parts: nil})
	require.ErrorIs(t, err, conversationview.ErrEmptyMessage)

	// Legacy empty role rejected
	_, err = conversationview.MessageIdentityOf(lipapi.Message{Role: "", Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}})
	require.ErrorIs(t, err, conversationview.ErrInvalidRole)
}

func TestMessageIdentity_NoPlaintextLeakedInErrorsOrString(t *testing.T) {
	t.Parallel()

	secretText := "SUPER_SECRET_PLAINTEXT_TOKEN_XYZ123"
	msg := lipapi.Message{
		Role: lipapi.RoleUser,
		Parts: []lipapi.Part{
			{Kind: lipapi.PartText, Text: secretText},
		},
	}

	id, err := conversationview.MessageIdentityOf(msg)
	require.NoError(t, err)

	idStr := id.String()
	assert.False(t, strings.Contains(idStr, secretText), "MessageIdentity.String() must not contain plaintext")
	assert.True(t, strings.HasPrefix(idStr, "v1:"), "MessageIdentity.String() must start with v1:")
}

func TestMessageIdentity_UnknownPartKindRejected(t *testing.T) {
	t.Parallel()
	// Unknown kind with non-empty Text must now be rejected explicitly, not coerced to text.
	secret := "SUPER_SECRET_PLAINTEXT_SHOULD_NOT_LEAK_12345"
	bogusKind := lipapi.PartKind("bogus_unknown_kind")
	msg := lipapi.Message{
		Role: lipapi.RoleUser,
		Parts: []lipapi.Part{
			{Kind: bogusKind, Text: secret},
		},
	}
	_, err := conversationview.MessageIdentityOf(msg)
	require.Error(t, err)
	// Error must contain bounded kind, not plaintext.
	assert.Contains(t, err.Error(), string(bogusKind), "error must contain bounded kind")
	assert.False(t, strings.Contains(err.Error(), secret), "error must not leak plaintext")
	// Also via AtomOfMessage directly.
	_, err2 := conversationview.AtomOfMessage(msg)
	require.Error(t, err2)
	assert.Contains(t, err2.Error(), string(bogusKind))
	assert.False(t, strings.Contains(err2.Error(), secret))

	// Unknown kind with empty Text also rejects with same shape.
	msgEmpty := lipapi.Message{
		Role: lipapi.RoleUser,
		Parts: []lipapi.Part{
			{Kind: lipapi.PartKind("another_bogus"), Text: ""},
		},
	}
	_, err3 := conversationview.MessageIdentityOf(msgEmpty)
	require.Error(t, err3)
	assert.Contains(t, err3.Error(), "another_bogus")
	assert.False(t, strings.Contains(err3.Error(), secret))

	// Valid kinds remain unchanged – control that we didn't break semantic identity format.
	validMsg := lipapi.Message{
		Role: lipapi.RoleUser,
		Parts: []lipapi.Part{
			{Kind: lipapi.PartText, Text: "hello valid"},
		},
	}
	id, err := conversationview.MessageIdentityOf(validMsg)
	require.NoError(t, err)
	assert.True(t, id.IsValid(), "valid part kind must still produce valid identity")
	// Same for item authority with valid content part.
	item := lipapi.Item{
		Kind:    lipapi.ItemKindMessage,
		Role:    lipapi.RoleUser,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hello valid"}},
	}
	itemID, err := conversationview.ItemIdentityOf(item)
	require.NoError(t, err)
	assert.Equal(t, id, itemID, "legacy and item valid text identities must remain equivalent")
}
