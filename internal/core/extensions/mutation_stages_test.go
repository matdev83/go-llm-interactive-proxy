package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
)

type rtxFail struct{}

func (rtxFail) ID() string                        { return "x" }
func (rtxFail) Order() int                        { return 0 }
func (rtxFail) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (rtxFail) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return errors.New("boom")
}

func TestRunRequestTransformStage_failClosedStops(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartText,
				Text: "hi",
			}},
		}},
	}
	err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtxFail{}}, &call, request.RequestMeta{}, request.Services{
		State: state.DisabledStore{},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

type rtxAppend struct{}

func (rtxAppend) ID() string                        { return "append" }
func (rtxAppend) Order() int                        { return 0 }
func (rtxAppend) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (rtxAppend) Handle(_ context.Context, call *lipapi.Call, _ request.RequestMeta, _ request.Services) error {
	call.Messages[0].Parts[0].Text += "!"
	return nil
}

func TestRunRequestTransformStage_mutatesCall(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartText,
				Text: "hi",
			}},
		}},
	}
	if err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtxAppend{}}, &call, request.RequestMeta{}, request.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hi!" {
		t.Fatalf("got %q", call.Messages[0].Parts[0].Text)
	}
}

type catDrop struct{}

func (catDrop) ID() string                        { return "drop" }
func (catDrop) Order() int                        { return 0 }
func (catDrop) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (catDrop) Handle(_ context.Context, call *lipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	call.Tools = call.Tools[:0]
	return nil
}

func TestRunToolCatalogFilterStage_reconcilesToolChoice(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind: lipapi.PartText,
				Text: "hi",
			}},
		}},
		Tools: []lipapi.ToolDef{{Name: "x"}},
		ToolChoice: lipapi.ToolChoice{
			Mode: lipapi.ToolChoiceRequired,
			Name: "x",
		},
	}
	if err := extensions.RunToolCatalogFilterStage(context.Background(), nil, nil, []toolcatalog.Filter{catDrop{}}, &call, toolcatalog.CatalogMeta{}, toolcatalog.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if len(call.Tools) != 0 {
		t.Fatalf("tools %d", len(call.Tools))
	}
	if call.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("choice mode %q", call.ToolChoice.Mode)
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
}
