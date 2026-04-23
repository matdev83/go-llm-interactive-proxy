package workspace_test

import (
	"context"
	"errors"
	"testing"

	corews "github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type stubRes struct {
	v lipworkspace.WorkspaceView
	e error
}

func (s stubRes) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return s.v, s.e
}

func TestResolverChain_failOpenSkipsErrors(t *testing.T) {
	t.Parallel()
	chain := corews.NewResolverChain([]lipworkspace.Resolver{
		stubRes{e: errors.New("boom")},
		stubRes{v: lipworkspace.WorkspaceView{ProjectRoot: "/ok"}},
	})
	got, err := chain.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectRoot != "/ok" {
		t.Fatalf("root %q", got.ProjectRoot)
	}
}

func TestResolverChain_emptyUsesDisabled(t *testing.T) {
	t.Parallel()
	chain := corews.NewResolverChain(nil)
	_, err := chain.Resolve(context.Background())
	if !errors.Is(err, lipworkspace.ErrResolverNotConfigured) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolverChain_mergeMarkersAndLabels(t *testing.T) {
	t.Parallel()
	chain := corews.NewResolverChain([]lipworkspace.Resolver{
		stubRes{v: lipworkspace.WorkspaceView{Markers: []string{"a"}, Labels: map[string]string{"k": "1"}}},
		stubRes{v: lipworkspace.WorkspaceView{Markers: []string{"a", "b"}, Labels: map[string]string{"k": "2", "x": "y"}, DirtyTree: true}},
	})
	got, err := chain.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Markers) != 2 || got.Markers[0] != "a" || got.Markers[1] != "b" {
		t.Fatalf("markers %+v", got.Markers)
	}
	if got.Labels["k"] != "2" || got.Labels["x"] != "y" {
		t.Fatalf("labels %+v", got.Labels)
	}
	if !got.DirtyTree {
		t.Fatal("expected dirty")
	}
}
