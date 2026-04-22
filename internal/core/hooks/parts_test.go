package hooks_test

import (
	"context"
	"errors"
	"testing"

	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestRunRequestPartHooks_zeroHooks(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	call := testCall()
	if err := b.RunRequestPartHooks(context.Background(), call, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRequestPartHooks_nilCall_isErrInvalidCall(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	err := b.RunRequestPartHooks(context.Background(), nil, sdk.PartMeta{})
	if !errors.Is(err, lipapi.ErrInvalidCall) {
		t.Fatalf("expected ErrInvalidCall, got %v", err)
	}
}

func TestRunRequestPartHooks_nilContext(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	err := b.RunRequestPartHooks(nil, testCall(), sdk.PartMeta{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestRunResponsePartHooks_nilEvent_isErrInvalidCall(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	err := b.RunResponsePartHooks(context.Background(), nil, sdk.PartMeta{})
	if !errors.Is(err, lipapi.ErrInvalidCall) {
		t.Fatalf("expected ErrInvalidCall, got %v", err)
	}
}

func TestRunResponsePartHooks_nilContext(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	ev := &lipapi.Event{Kind: lipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	err := b.RunResponsePartHooks(nil, ev, sdk.PartMeta{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestRunRequestPartHooks_validMutation(t *testing.T) {
	t.Parallel()
	h := &stubReqPart{
		id: "m", order: 0,
		fn: func(_ context.Context, call *lipapi.Call, _ sdk.PartMeta) error {
			call.Messages[0].Parts[0] = lipapi.TextPart("hello")
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{h}})
	call := testCall()
	if err := b.RunRequestPartHooks(context.Background(), call, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hello" {
		t.Fatalf("expected rewritten text, got %q", call.Messages[0].Parts[0].Text)
	}
}

func TestRunRequestPartHooks_invalidMutationTypedError(t *testing.T) {
	t.Parallel()
	h := &stubReqPart{
		id: "bad", order: 0,
		fn: func(_ context.Context, call *lipapi.Call, _ sdk.PartMeta) error {
			call.Messages[0].Parts[0] = lipapi.Part{Kind: lipapi.PartText, Text: ""}
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{h}})
	err := b.RunRequestPartHooks(context.Background(), testCall(), sdk.PartMeta{})
	if !lipapi.IsHookMutation(err) {
		t.Fatalf("expected hook mutation error, got %v", err)
	}
}

func TestRunResponsePartHooks_zeroHooks(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	ev := &lipapi.Event{Kind: lipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	if err := b.RunResponsePartHooks(context.Background(), ev, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunResponsePartHooks_invalidEventShape(t *testing.T) {
	t.Parallel()
	h := &stubRespPart{
		id: "bad", order: 0,
		fn: func(_ context.Context, ev *lipapi.Event, _ sdk.PartMeta) error {
			ev.Kind = lipapi.EventKind("unknown")
			return nil
		},
	}
	b := corehooks.New(corehooks.Config{ResponsePartHooks: []sdk.ResponsePartHook{h}})
	ev := &lipapi.Event{Kind: lipapi.EventTextDelta, MessageIndex: 0, Delta: "x"}
	err := b.RunResponsePartHooks(context.Background(), ev, sdk.PartMeta{})
	if !lipapi.IsHookMutation(err) {
		t.Fatalf("expected hook mutation error, got %v", err)
	}
}

type stubReqPart struct {
	id    string
	order int
	fn    func(context.Context, *lipapi.Call, sdk.PartMeta) error
}

func (s *stubReqPart) ID() string                   { return s.id }
func (s *stubReqPart) Order() int                   { return s.order }
func (s *stubReqPart) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *stubReqPart) HandleRequestParts(ctx context.Context, call *lipapi.Call, meta sdk.PartMeta) error {
	return s.fn(ctx, call, meta)
}

type stubRespPart struct {
	id    string
	order int
	fn    func(context.Context, *lipapi.Event, sdk.PartMeta) error
}

func (s *stubRespPart) ID() string                   { return s.id }
func (s *stubRespPart) Order() int                   { return s.order }
func (s *stubRespPart) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *stubRespPart) HandleEvent(ctx context.Context, ev *lipapi.Event, meta sdk.PartMeta) error {
	return s.fn(ctx, ev, meta)
}
