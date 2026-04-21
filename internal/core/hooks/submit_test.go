package hooks_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func testCall() *lipapi.Call {
	return &lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
}

func TestRunSubmit_rejectsInvalidCallAfterHooks(t *testing.T) {
	t.Parallel()
	h := &stubSubmit{
		id: "inflate", order: 0,
		handle: func(_ context.Context, call *lipapi.Call, _ *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			call.Messages[0].Parts[0].Text = strings.Repeat("x", lipapi.MaxPartTextBytes+1)
			return sdk.SubmitDecision{}, nil
		},
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{h}})
	err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}})
	if err == nil {
		t.Fatal("expected validation error after hook inflated message")
	}
	if !strings.Contains(err.Error(), "submit hooks") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunSubmit_zeroHooks(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	call := testCall()
	meta := &sdk.SubmitMeta{Annotations: map[string]string{}}
	if err := b.RunSubmit(context.Background(), call, meta); err != nil {
		t.Fatal(err)
	}
}

func TestRunSubmit_orderingStableByIDWhenOrderEqual(t *testing.T) {
	t.Parallel()
	var order []string
	a := &stubSubmit{id: "a", order: 1, fn: func() { order = append(order, "a") }}
	b := &stubSubmit{id: "b", order: 1, fn: func() { order = append(order, "b") }}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{b, a}})
	if err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("expected id tie-break a then b, got %v", order)
	}
}

func TestRunSubmit_ordersByOrderField(t *testing.T) {
	t.Parallel()
	var order []string
	second := &stubSubmit{id: "second", order: 2, fn: func() { order = append(order, "second") }}
	first := &stubSubmit{id: "first", order: 1, fn: func() { order = append(order, "first") }}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{second, first}})
	if err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("unexpected order %v", order)
	}
}

func TestRunSubmit_annotationAndRewrite(t *testing.T) {
	t.Parallel()
	h := &stubSubmit{
		id: "ann", order: 0,
		fn: func() {},
		handle: func(_ context.Context, call *lipapi.Call, meta *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			meta.Annotations["k"] = "v"
			call.ID = "set-by-hook"
			return sdk.SubmitDecision{}, nil
		},
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{h}})
	meta := &sdk.SubmitMeta{Annotations: map[string]string{}}
	call := testCall()
	if err := bus.RunSubmit(context.Background(), call, meta); err != nil {
		t.Fatal(err)
	}
	if call.ID != "set-by-hook" || meta.Annotations["k"] != "v" {
		t.Fatalf("call/meta not updated: %#v %#v", call, meta.Annotations)
	}
}

func TestRunSubmit_reject(t *testing.T) {
	t.Parallel()
	h := &stubSubmit{
		id: "gate", order: 0,
		handle: func(context.Context, *lipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			return sdk.SubmitDecision{Reject: true, Reason: "nope"}, nil
		},
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{h}})
	err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}})
	if !sdk.IsSubmitReject(err) {
		t.Fatalf("expected submit reject, got %v", err)
	}
}

func TestRunSubmit_failOpen_skipsHookError(t *testing.T) {
	t.Parallel()
	var ranSecond bool
	first := &stubSubmit{
		id: "boom", order: 1, explicitMode: true, mode: sdk.FailOpen,
		handle: func(context.Context, *lipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			return sdk.SubmitDecision{}, errors.New("ignored")
		},
	}
	second := &stubSubmit{
		id: "after", order: 2,
		fn: func() { ranSecond = true },
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{first, second}})
	if err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if !ranSecond {
		t.Fatal("expected second hook to run after fail-open error")
	}
}

func TestRunSubmit_failClosed_stopsChain(t *testing.T) {
	t.Parallel()
	var ranSecond bool
	first := &stubSubmit{
		id: "boom", order: 1, explicitMode: true, mode: sdk.FailClosed,
		handle: func(context.Context, *lipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			return sdk.SubmitDecision{}, errors.New("fatal")
		},
	}
	second := &stubSubmit{
		id: "after", order: 2,
		fn: func() { ranSecond = true },
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{first, second}})
	err := bus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}})
	if err == nil || err.Error() == "" {
		t.Fatal("expected error")
	}
	if ranSecond {
		t.Fatal("expected second hook not to run")
	}
}

type stubSubmit struct {
	id           string
	order        int
	explicitMode bool
	mode         sdk.FailureMode
	fn           func()
	handle       func(context.Context, *lipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error)
}

func (s *stubSubmit) ID() string { return s.id }
func (s *stubSubmit) Order() int { return s.order }
func (s *stubSubmit) FailureMode() sdk.FailureMode {
	if s.explicitMode {
		return s.mode
	}
	return sdk.FailClosed
}

func (s *stubSubmit) Handle(ctx context.Context, call *lipapi.Call, meta *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	if s.fn != nil {
		s.fn()
	}
	if s.handle != nil {
		return s.handle(ctx, call, meta)
	}
	return sdk.SubmitDecision{}, nil
}
