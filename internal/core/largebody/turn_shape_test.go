package largebody_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestClientTurnShapeFromCall_NoPromptText(t *testing.T) {
	const secretPrompt = "SUPER_CONFIDENTIAL_PROMPT_DO_NOT_STORE_OR_LOG_98765"

	call := &lipapi.Call{
		Instructions: []lipapi.Message{
			{
				Role: lipapi.RoleSystem,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "system prompt: " + secretPrompt},
				},
			},
		},
		Messages: []lipapi.Message{
			{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: secretPrompt},
					{Kind: lipapi.PartImageRef, ImageRef: "https://example.com/img.png"},
				},
			},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "assistant reply: " + secretPrompt},
					{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Text: "reasoning text: " + secretPrompt}},
				},
			},
		},
	}

	shape, err := largebody.ClientTurnShapeFromCall(call, 64*1024)
	if err != nil {
		t.Fatalf("ClientTurnShapeFromCall failed: %v", err)
	}

	// 1. Verify item count: 1 instruction + 2 messages = 3 items
	if len(shape.Items) != 3 {
		t.Fatalf("len(shape.Items) = %d, want 3", len(shape.Items))
	}

	// 2. Verify TotalContentBytes reflects content sizes without retaining text
	if shape.TotalContentBytes <= 0 {
		t.Fatalf("TotalContentBytes = %d, want > 0", shape.TotalContentBytes)
	}

	// 3. String representations, JSON, and fields must NEVER contain secretPrompt
	s := fmt.Sprintf("%+v", shape)
	if strings.Contains(s, secretPrompt) {
		t.Fatalf("ClientTurnShape contains prompt text: %s", s)
	}

	// Inspect all parts in all items
	for i, it := range shape.Items {
		for j, p := range it.Parts {
			typ := reflect.TypeOf(p)
			for f := 0; f < typ.NumField(); f++ {
				field := typ.Field(f)
				if field.Type.Kind() == reflect.String && field.Name != "Kind" {
					t.Fatalf("item %d part %d has unexpected string field %s", i, j, field.Name)
				}
			}
		}
	}
}

func TestClientTurnShapeFromCall_BudgetOverflow(t *testing.T) {
	call := &lipapi.Call{
		Messages: []lipapi.Message{
			{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "hello"},
				},
			},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "world"},
				},
			},
		},
	}

	// Non-positive budget must be rejected
	if _, err := largebody.ClientTurnShapeFromCall(call, 0); err == nil {
		t.Fatal("expected error for budget <= 0, got nil")
	}

	// Tiny budget should trigger budget overflow
	_, err := largebody.ClientTurnShapeFromCall(call, 1)
	if err == nil {
		t.Fatal("expected budget overflow error for tiny budget, got nil")
	}
	if !largebody.IsSemanticFactBudgetExceeded(err) {
		t.Fatalf("expected IsSemanticFactBudgetExceeded(err) = true, got %v", err)
	}
	if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("expected errors.Is(err, ErrSemanticFactBudgetExceeded), got %v", err)
	}
}

func TestClientTurnShape_BudgetOverflowSentinel(t *testing.T) {
	shape := largebody.ClientTurnShape{
		Items: []largebody.ClientTurnItemShape{
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleUser,
				Ordinal: 0,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: lipapi.ContentPartText, ContentBytes: 100},
				},
			},
		},
		TotalContentBytes: 100,
	}

	// Item count exceeds budget
	tinyBudget := int64(1)
	manyItems := shape
	manyItems.Items = make([]largebody.ClientTurnItemShape, 2)
	for i := range manyItems.Items {
		manyItems.Items[i] = shape.Items[0]
	}
	err := manyItems.Validate(tinyBudget)
	if err == nil {
		t.Fatal("expected error for item count > maxFactBytes, got nil")
	}
	if !largebody.IsSemanticFactBudgetExceeded(err) {
		t.Fatalf("expected IsSemanticFactBudgetExceeded(err) = true, got %v", err)
	}

	// Part count exceeds budget
	manyParts := shape
	manyParts.Items[0].Parts = make([]largebody.ClientTurnPartShape, 2)
	manyParts.Items[0].Parts[0] = largebody.ClientTurnPartShape{Kind: lipapi.ContentPartText, ContentBytes: 50}
	manyParts.Items[0].Parts[1] = largebody.ClientTurnPartShape{Kind: lipapi.ContentPartText, ContentBytes: 50}
	err = manyParts.Validate(tinyBudget)
	if err == nil {
		t.Fatal("expected error for part count > maxFactBytes, got nil")
	}
	if !largebody.IsSemanticFactBudgetExceeded(err) {
		t.Fatalf("expected IsSemanticFactBudgetExceeded(err) = true, got %v", err)
	}
}

func TestClientTurnShapeFromMessages_PreservesMediaAndReasoningPartBytes(t *testing.T) {
	t.Parallel()

	msgs := []lipapi.Message{
		{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartText, Text: "analyze this"},
				{Kind: lipapi.PartImageRef, ImageRef: "https://example.com/pic.png", ImageMIME: "image/png"},
				{Kind: lipapi.PartFileRef, FileRef: "file-123", FileMIME: "application/pdf", FileName: "doc.pdf"},
			},
		},
		{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Text: "thought process"}},
				{Kind: lipapi.PartText, Text: "done"},
			},
		},
	}

	call := &lipapi.Call{Messages: msgs}

	shapeFromCall, err := largebody.ClientTurnShapeFromCall(call, 64*1024)
	if err != nil {
		t.Fatalf("ClientTurnShapeFromCall failed: %v", err)
	}

	shapeFromMsgs, err := largebody.ClientTurnShapeFromMessages(msgs, 64*1024)
	if err != nil {
		t.Fatalf("ClientTurnShapeFromMessages failed: %v", err)
	}

	if shapeFromMsgs.TotalContentBytes != shapeFromCall.TotalContentBytes {
		t.Fatalf("TotalContentBytes mismatch: fromMsgs=%d, fromCall=%d",
			shapeFromMsgs.TotalContentBytes, shapeFromCall.TotalContentBytes)
	}

	if len(shapeFromMsgs.Items) != len(shapeFromCall.Items) {
		t.Fatalf("Item count mismatch: %d vs %d", len(shapeFromMsgs.Items), len(shapeFromCall.Items))
	}

	for i := range shapeFromMsgs.Items {
		pMsgs := shapeFromMsgs.Items[i].Parts
		pCall := shapeFromCall.Items[i].Parts
		if len(pMsgs) != len(pCall) {
			t.Fatalf("Item %d part count mismatch: %d vs %d", i, len(pMsgs), len(pCall))
		}
		for j := range pMsgs {
			if pMsgs[j].ContentBytes != pCall[j].ContentBytes {
				t.Fatalf("Item %d part %d ContentBytes mismatch: fromMsgs=%d, fromCall=%d (kind=%s)",
					i, j, pMsgs[j].ContentBytes, pCall[j].ContentBytes, pMsgs[j].Kind)
			}
		}
	}
}

func TestClientTurnShapeFromMessages_CanonicalOracleEquivalence(t *testing.T) {
	t.Parallel()

	largeJSONPayload := json.RawMessage(fmt.Sprintf(`{"query":%q,"results":[%s]}`,
		"search query", strings.Repeat(`{"id":1,"data":"payload chunk"},`, 500)))

	msgs := []lipapi.Message{
		// 1. User message with text, media (image, file), and unknown parts
		{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartText, Text: "analyze these artifacts"},
				{Kind: lipapi.PartImageRef, ImageRef: "https://example.com/spec.png", ImageMIME: "image/png"},
				{Kind: lipapi.PartFileRef, FileRef: "file-987", FileMIME: "application/json", FileName: "doc.json"},
				{Kind: "custom_extension", Text: "visible text fallback"},
				{Kind: "silent_unknown", Text: ""}, // Must be omitted by canonical oracle
			},
		},
		// 2. Assistant message with reasoning and real tool_calls / function_call (PartJSON)
		{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartReasoning, Reasoning: &lipapi.ReasoningPart{Text: "evaluating weather"}},
				{
					Kind:       lipapi.PartJSON,
					ToolCallID: "call_weather_123",
					ToolName:   "get_weather",
					Content:    json.RawMessage(`{"location":"Paris, France","units":"celsius"}`),
				},
				{
					Kind:    lipapi.PartJSON,
					Content: json.RawMessage(`{"name":"legacy_function","arguments":"{}"}`),
				},
				{
					Kind:    lipapi.PartJSON,
					Content: largeJSONPayload,
				},
			},
		},
		// 3. Tool turn with tool result
		{
			Role: lipapi.RoleTool,
			Parts: []lipapi.Part{
				{Kind: lipapi.PartToolResult, ToolCallID: "call_weather_123", Text: `{"temp":21.5,"condition":"sunny"}`},
			},
		},
	}

	call := &lipapi.Call{Messages: msgs}
	budget := int64(1024 * 1024)

	shapeFromCall, err := largebody.ClientTurnShapeFromCall(call, budget)
	if err != nil {
		t.Fatalf("ClientTurnShapeFromCall (canonical oracle) failed: %v", err)
	}

	shapeFromMsgs, err := largebody.ClientTurnShapeFromMessages(msgs, budget)
	if err != nil {
		t.Fatalf("ClientTurnShapeFromMessages failed: %v", err)
	}

	// 1. TotalContentBytes must match exactly
	if shapeFromMsgs.TotalContentBytes != shapeFromCall.TotalContentBytes {
		t.Fatalf("TotalContentBytes mismatch: fromMsgs=%d, fromCall=%d",
			shapeFromMsgs.TotalContentBytes, shapeFromCall.TotalContentBytes)
	}

	// 2. Exact deep equality of all items, roles, ordinals, parts, and content bytes
	if !reflect.DeepEqual(shapeFromMsgs, shapeFromCall) {
		t.Fatalf("ClientTurnShapeFromMessages != ClientTurnShapeFromCall:\nFromMsgs: %+v\nFromCall: %+v",
			shapeFromMsgs, shapeFromCall)
	}

	// 3. Verify PartJSON specifically counted len(p.Content)
	toolPart := shapeFromMsgs.Items[1].Parts[1]
	if toolPart.Kind != lipapi.ContentPartJSON {
		t.Fatalf("expected toolPart.Kind == ContentPartJSON, got %s", toolPart.Kind)
	}
	expectedToolBytes := int64(len(msgs[1].Parts[1].Content))
	if toolPart.ContentBytes != expectedToolBytes {
		t.Fatalf("toolPart ContentBytes = %d, want %d", toolPart.ContentBytes, expectedToolBytes)
	}

	// 4. Verify large JSON part counted exact bytes
	largePart := shapeFromMsgs.Items[1].Parts[3]
	if largePart.ContentBytes != int64(len(largeJSONPayload)) {
		t.Fatalf("largePart ContentBytes = %d, want %d", largePart.ContentBytes, len(largeJSONPayload))
	}

	// 5. Verify silent unknown part was omitted (Parts count should be 4, not 5)
	userItem := shapeFromMsgs.Items[0]
	if len(userItem.Parts) != 4 {
		t.Fatalf("user item part count = %d, want 4 (silent unknown must be omitted)", len(userItem.Parts))
	}
	if userItem.Parts[3].Kind != lipapi.ContentPartText || userItem.Parts[3].ContentBytes != int64(len("visible text fallback")) {
		t.Fatalf("unknown with text must map to ContentPartText with exact length, got %+v", userItem.Parts[3])
	}
}
