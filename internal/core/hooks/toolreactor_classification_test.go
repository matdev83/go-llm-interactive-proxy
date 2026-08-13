package hooks_test

import (
	"context"
	"testing"

	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestApplyToolReactors_renameReclassifiesForNextReactor(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "read"}
	renamer := &stubTool{
		id: "rename", order: 1,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolRewrite, lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "bash"}, nil
		},
	}
	observer := &stubTool{
		id: "observe", order: 2,
		fn: func(_ context.Context, cur lipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			if cur.Category != lipapi.ToolCategoryOSCommand || !cur.MayMutateLocalFS {
				t.Fatalf("renamed event = (%q,%v), want (os_command,true)", cur.Category, cur.MayMutateLocalFS)
			}
			return sdk.ToolPass, lipapi.ToolEvent{}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{renamer, observer}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.Category != lipapi.ToolCategoryOSCommand || !out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want (os_command,true)", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_sameIDNamelessRewritePreservesClassification(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", Category: lipapi.ToolCategoryFileRead, MayMutateLocalFS: false}
	rewriter := &stubTool{
		id: "rw", order: 1,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolRewrite, lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "y"}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{rewriter}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.Category != lipapi.ToolCategoryFileRead || out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want preserved (file_read,false)", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_changedIDNamelessReplacementIsUnknown(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "read", Category: lipapi.ToolCategoryFileRead, MayMutateLocalFS: false}
	replacer := &stubTool{
		id: "rep", order: 1,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolReplace, lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c2"}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{replacer}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.ToolCallID != "c2" {
		t.Fatalf("ToolCallID=%q, want c2", out.Event.ToolCallID)
	}
	if out.Event.Category != lipapi.ToolCategoryUnknown || !out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want (unknown,true)", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_whitespaceOnlyNamePreservesClassification(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{
		Kind:             lipapi.ToolEventArgsDelta,
		ToolCallID:       "c1",
		ToolName:         "read",
		Category:         lipapi.ToolCategoryFileRead,
		MayMutateLocalFS: false,
	}
	rewriter := &stubTool{
		id: "rw", order: 1,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolRewrite, lipapi.ToolEvent{
				Kind:       lipapi.ToolEventArgsDelta,
				ToolCallID: "c1",
				ToolName:   "   ",
				ArgsDelta:  "y",
			}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{rewriter}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.Category != lipapi.ToolCategoryFileRead || out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want preserved (file_read,false) for whitespace-only name", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_reactorCannotOverrideDerivedFromName(t *testing.T) {
	t.Parallel()
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "read"}
	liar := &stubTool{
		id: "liar", order: 1,
		fn: func(context.Context, lipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
			return sdk.ToolRewrite, lipapi.ToolEvent{
				Kind:             lipapi.ToolEventStarted,
				ToolCallID:       "c1",
				ToolName:         "bash",
				Category:         lipapi.ToolCategoryFileRead,
				MayMutateLocalFS: false,
			}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{liar}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.Category != lipapi.ToolCategoryOSCommand || !out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want (os_command,true) derived from name", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}
