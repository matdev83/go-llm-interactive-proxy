package hooks_test

import (
	"context"
	"errors"
	"testing"

	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestApplyToolReactors_nilContext(t *testing.T) {
	t.Parallel()
	b := corehooks.New(corehooks.Config{})
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	out := b.ApplyToolReactors(nil, te, sdk.ToolMeta{}) //nolint:staticcheck // deliberate nil ctx
	if out.Emit || out.Err == nil || !errors.Is(out.Err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got emit=%v err=%v", out.Emit, out.Err)
	}
}

func TestApplyToolReactors_passThroughChain(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "a"}
	r := &stubTool{id: "t1", order: 0, fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
		return sdk.ToolPass, lipapi.ToolEvent{}, nil
	}}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{r}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event != te {
		t.Fatalf("expected unchanged pass-through, got %#v", out)
	}
}

func TestApplyToolReactors_rewrite(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "a"}
	rewritten := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "b"}
	r := &stubTool{
		id: "t1", order: 0,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolRewrite, rewritten, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{r}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ArgsDelta != "b" {
		t.Fatalf("expected rewrite, got %#v", out)
	}
}

func TestApplyToolReactors_replace(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "n1"}
	replacement := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c2", ToolName: "n2"}
	r := &stubTool{
		id: "t1", order: 0,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolReplace, replacement, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{r}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ToolCallID != "c2" {
		t.Fatalf("expected replace, got %#v", out)
	}
}

func TestApplyToolReactors_swallow(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "x"}
	r := &stubTool{
		id: "t1", order: 0,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolSwallow, lipapi.ToolEvent{}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{r}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if out.Emit {
		t.Fatalf("expected swallow (no emit), got %#v", out)
	}
}

func TestApplyToolReactors_failOpenOnError(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "orig"}
	bad := &stubTool{
		id: "bad", order: 1,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolRewrite, lipapi.ToolEvent{}, errors.New("boom")
		},
	}
	good := &stubTool{
		id: "good", order: 2,
		fn: func(_ context.Context, cur lipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			if cur.ArgsDelta != "orig" {
				t.Fatalf("expected fail-open to preserve current event, got %#v", cur)
			}
			return sdk.ToolPass, lipapi.ToolEvent{}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{bad, good}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ArgsDelta != "orig" {
		t.Fatalf("expected pass-through after error, got %#v", out)
	}
}

func TestApplyToolReactors_chainedRewrite(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "0"}
	first := &stubTool{
		id: "a", order: 1,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolRewrite, lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "1"}, nil
		},
	}
	second := &stubTool{
		id: "b", order: 2,
		fn: func(_ context.Context, cur lipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolRewrite, lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: cur.ArgsDelta + "2"}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{second, first}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Event.ArgsDelta != "12" {
		t.Fatalf("expected chained rewrite, got %#v", out)
	}
}

type stubTool struct {
	id    string
	order int
	fn    func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error)
}

func (s *stubTool) ID() string { return s.id }
func (s *stubTool) Order() int { return s.order }
func (s *stubTool) HandleToolEvent(ctx context.Context, te lipapi.ToolEvent, meta sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
	return s.fn(ctx, te, meta)
}
