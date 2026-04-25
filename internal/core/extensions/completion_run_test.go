package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

type gateReplace struct{}

func (gateReplace) ID() string                        { return "rep" }
func (gateReplace) Order() int                        { return 0 }
func (gateReplace) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateReplace) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "x"},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}

func TestApplyCompletionGateChain_replace(t *testing.T) {
	t.Parallel()
	orig := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "a"},
		{Kind: lipapi.EventResponseFinished},
	}
	out, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateReplace{}}, completion.Meta{}, orig, false, completion.Services{
		State: state.DisabledStore{},
		Aux:   auxiliary.DisabledClient{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Delta != "x" {
		t.Fatalf("got %#v", out)
	}
}

func TestApplyCompletionGateChain_replaceIgnoredWhenCommitted(t *testing.T) {
	t.Parallel()
	orig := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "a"},
		{Kind: lipapi.EventResponseFinished},
	}
	out, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateReplace{}}, completion.Meta{}, orig, true, completion.Services{
		State: state.DisabledStore{},
		Aux:   auxiliary.DisabledClient{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Delta != "a" {
		t.Fatalf("expected original, got %#v", out)
	}
}

type gateFailOpenErr struct{}

func (gateFailOpenErr) ID() string                        { return "e" }
func (gateFailOpenErr) Order() int                        { return 0 }
func (gateFailOpenErr) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateFailOpenErr) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{}, errors.New("boom")
}

func TestApplyCompletionGateChain_handlerErrorFailOpen(t *testing.T) {
	t.Parallel()
	orig := []lipapi.Event{{Kind: lipapi.EventResponseFinished}}
	out, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateFailOpenErr{}}, completion.Meta{}, orig, false, completion.Services{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != lipapi.EventResponseFinished {
		t.Fatalf("got %#v", out)
	}
}

type gateFailClosedErr struct{}

func (gateFailClosedErr) ID() string                        { return "fc" }
func (gateFailClosedErr) Order() int                        { return 0 }
func (gateFailClosedErr) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (gateFailClosedErr) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{}, errors.New("closed")
}

func TestApplyCompletionGateChain_handlerErrorFailClosed(t *testing.T) {
	t.Parallel()
	orig := []lipapi.Event{{Kind: lipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateFailClosedErr{}}, completion.Meta{}, orig, false, completion.Services{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

type gateReject struct{}

func (gateReject) ID() string                        { return "rj" }
func (gateReject) Order() int                        { return 0 }
func (gateReject) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateReject) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.RejectOutcome(errors.New("nope")), nil
}

func TestApplyCompletionGateChain_reject(t *testing.T) {
	t.Parallel()
	orig := []lipapi.Event{{Kind: lipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateReject{}}, completion.Meta{}, orig, false, completion.Services{}, nil)
	if err == nil || err.Error() != "nope" {
		t.Fatalf("got %v", err)
	}
}

func TestCompletionGateBufferExceeded(t *testing.T) {
	t.Parallel()
	lim := completion.BufferLimits{MaxEvents: 2}
	if !extensions.CompletionGateBufferExceeded(lim, 3) {
		t.Fatal("overflow")
	}
}

func TestStreamFinished(t *testing.T) {
	t.Parallel()
	if extensions.StreamFinished(nil) {
		t.Fatal("nil")
	}
	if extensions.StreamFinished([]lipapi.Event{{Kind: lipapi.EventTextDelta}}) {
		t.Fatal("not finished")
	}
	if !extensions.StreamFinished([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}) {
		t.Fatal("finished")
	}
}
