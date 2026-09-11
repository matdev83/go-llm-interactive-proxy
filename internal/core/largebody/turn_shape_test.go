package largebody_test

import (
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
