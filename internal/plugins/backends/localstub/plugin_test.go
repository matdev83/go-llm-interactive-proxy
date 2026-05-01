package localstub

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_streamCollectsTextAndUsage(t *testing.T) {
	t.Parallel()
	be := New(Config{Text: "hi", InputTokens: 2, OutputTokens: 5})
	es, err := be.Open(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "hi" {
		t.Fatalf("text %q", col.Text.String())
	}
	if col.InputTokens != 2 || col.OutputTokens != 5 {
		t.Fatalf("usage in=%d out=%d", col.InputTokens, col.OutputTokens)
	}
}

func TestNew_withToolName_emitsToolFrames(t *testing.T) {
	t.Parallel()
	be := New(Config{Text: "a", ToolName: "fn", InputTokens: 0, OutputTokens: 0})
	caps := execbackend.EffectiveCaps(context.Background(), be, lipapi.Call{}, routing.AttemptCandidate{})
	if _, has := caps[lipapi.CapabilityTools]; !has {
		t.Fatal("expected tools capability")
	}
	es, err := be.Open(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 || tcs[0].Name != "fn" || tcs[0].Arguments != `{}` {
		t.Fatalf("tools %+v", tcs)
	}
}

func TestNew_withoutToolName_noToolsCapability(t *testing.T) {
	t.Parallel()
	be := New(Config{Text: "only", ToolName: ""})
	caps := execbackend.EffectiveCaps(context.Background(), be, lipapi.Call{}, routing.AttemptCandidate{})
	if _, has := caps[lipapi.CapabilityTools]; has {
		t.Fatal("did not expect tools capability")
	}
	es, err := be.Open(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	col, err := lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.OrderedToolCalls()) != 0 {
		t.Fatalf("expected no tool calls, got %+v", col.OrderedToolCalls())
	}
}

func TestNew_nilContextOpenError(t *testing.T) {
	t.Parallel()
	be := New(Config{})
	_, err := be.Open(nil, lipapi.Call{}, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNew_streamErrorAfterTextDelta_errorsAfterFirstTextDelta(t *testing.T) {
	t.Parallel()
	be := New(Config{
		Text:                      "x",
		InputTokens:               1,
		OutputTokens:              1,
		StreamErrorAfterTextDelta: true,
	})
	es, err := be.Open(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for {
		_, rerr := es.Recv(ctx)
		if rerr != nil {
			if !errors.Is(rerr, errPostTextStream) {
				t.Fatalf("want errPostTextStream, got %v", rerr)
			}
			if lipapi.IsRecoverablePreOutput(rerr) {
				t.Fatal("post-text stub error must not be classified as recoverable pre-output")
			}
			break
		}
	}
	_ = es.Close()
}
