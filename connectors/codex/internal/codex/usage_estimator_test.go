package codex

import (
	"context"
	"io"
	"strings"
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
				{Kind: lipapi.PartImageRef, ImageRef: "https://example.com/image.png"},
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

func TestEstimateUsage_toolsContributeToInput(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	base := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("use tools")},
		}},
	}
	withTools := base
	withTools.Tools = []lipapi.ToolDef{{
		Name:        "lookup",
		Description: "look up a value",
		Parameters:  []byte(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}}
	baseEv, err := est.estimateUsage(context.Background(), base, "gpt-5.3-codex", "ok")
	if err != nil {
		t.Fatal(err)
	}
	toolEv, err := est.estimateUsage(context.Background(), withTools, "gpt-5.3-codex", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if toolEv.InputTokens <= baseEv.InputTokens {
		t.Fatalf("tools should increase input: base=%d tools=%d", baseEv.InputTokens, toolEv.InputTokens)
	}
}

func TestUsageEstimatingStream_estimationFailureReturnsFinishedEvent(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	stream := &usageEstimatingStream{
		base: &usageEstimateTestStream{events: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
			{Kind: lipapi.EventResponseFinished},
		}},
		est: est,
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := stream.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	ev, err := stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("kind = %q", ev.Kind)
	}
}

func TestUsageEstimatingStream_skipsEstimationWhenOutputBufferExceedsLimit(t *testing.T) {
	t.Parallel()
	est := &usageEstimator{}
	stream := &usageEstimatingStream{
		base: &usageEstimateTestStream{events: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: strings.Repeat("x", maxUsageEstimateOutputBytes+1)},
			{Kind: lipapi.EventResponseFinished},
		}},
		est: est,
	}
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if stream.text.Len() != 0 || !stream.textFull {
		t.Fatalf("buffer len=%d full=%v", stream.text.Len(), stream.textFull)
	}
}

func TestUsageEstimatingStream_skipsWhenProviderAlreadySentUsage(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	stream := newUsageEstimatingStream(&usageEstimateTestStream{events: []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "hi"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 1, TotalTokens: 4},
		{Kind: lipapi.EventResponseFinished},
	}}, est, lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}}, "gpt-5.3-codex")
	var kinds []lipapi.EventKind
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
	}
	usageCount := 0
	for _, k := range kinds {
		if k == lipapi.EventUsageDelta {
			usageCount++
		}
	}
	if usageCount != 1 {
		t.Fatalf("kinds=%v usageCount=%d", kinds, usageCount)
	}
}

type usageEstimateTestStream struct {
	events []lipapi.Event
	idx    int
}

func (s *usageEstimateTestStream) Recv(context.Context) (lipapi.Event, error) {
	if s.idx >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *usageEstimateTestStream) Close() error { return nil }

func (s *usageEstimateTestStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
}
