package openaicodex

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestEstimateUsage_textRequest_positiveTotalsAndMetadata(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello codex")},
		}},
	}
	ev, err := est.estimateUsage(context.Background(), call, "gpt-5.3-codex", "world")
	if err != nil {
		t.Fatal(err)
	}
	if ev.InputTokens <= 0 || ev.OutputTokens <= 0 {
		t.Fatalf("tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	if ev.TotalTokens != ev.InputTokens+ev.OutputTokens {
		t.Fatalf("total=%d want %d", ev.TotalTokens, ev.InputTokens+ev.OutputTokens)
	}
	if ev.Accounting.Source != lipapi.UsageSourceLocalTokenizer {
		t.Fatalf("source=%q", ev.Accounting.Source)
	}
	if ev.Accounting.Authority != lipapi.UsageAuthorityEstimated {
		t.Fatalf("authority=%q", ev.Accounting.Authority)
	}
	if ev.Accounting.Plane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("plane=%q", ev.Accounting.Plane)
	}
	if ev.Accounting.Tokenizer.Type != "tiktoken" || ev.Accounting.Tokenizer.ID != "o200k_base" {
		t.Fatalf("tokenizer=%+v", ev.Accounting.Tokenizer)
	}
	if ev.Accounting.Tokenizer.Source != "github.com/tiktoken-go/tokenizer" {
		t.Fatalf("tokenizer source=%q", ev.Accounting.Tokenizer.Source)
	}
	if ev.Accounting.Tokenizer.ModelUsed != "gpt-5.3-codex" {
		t.Fatalf("model used=%q", ev.Accounting.Tokenizer.ModelUsed)
	}
}

func TestEstimateUsage_imageRefURL_usesConservativeDefault(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("describe"),
				lipapi.Part{Kind: lipapi.PartImageRef, ImageRef: "https://example.com/image.png"},
			},
		}},
	}
	ev, err := est.estimateUsage(context.Background(), call, "gpt-5.3-codex", "done")
	if err != nil {
		t.Fatal(err)
	}
	if ev.InputTokens <= 0 || ev.OutputTokens <= 0 || ev.TotalTokens != ev.InputTokens+ev.OutputTokens {
		t.Fatalf("usage: %+v", ev)
	}
}
