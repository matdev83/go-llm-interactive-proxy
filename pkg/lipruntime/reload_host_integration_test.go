package lipruntime_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
)

func TestBuild_BindsCoordinatorAndStableExecutorView(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	if rt.ReloadControl() == nil {
		t.Fatal("expected coordinator-bound ReloadControl")
	}
	st := rt.ReloadStatus()
	if st.ActiveGeneration != 1 {
		t.Fatalf("active generation=%d want 1", st.ActiveGeneration)
	}
	view := rt.ExecutorView()
	if view == nil {
		t.Fatal("expected stable ExecutorView")
	}
	// Pre-reload facade identity must survive a no-op / busy status query.
	view2 := rt.ExecutorView()
	if view != view2 {
		t.Fatal("ExecutorView must be a stable facade handle")
	}
	res := rt.Reload(ctx, lipruntime.ReloadTrigger{
		Kind:       lipruntime.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "test",
	})
	if res.Category != lipruntime.ResultNoop && res.Category != lipruntime.ResultPublished {
		t.Fatalf("reload category=%q reason=%q", res.Category, res.ReasonCategory)
	}
	if rt.ExecutorView() != view {
		t.Fatal("ExecutorView identity must survive reload")
	}
}

func TestBuild_ExecutorViewAcquireWithoutActiveFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{ConfigPath: repoConfigPath(t)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	view := rt.ExecutorView()
	_ = rt.Close(ctx)
	_, err = view.Execute(ctx, &lipapi.Call{})
	if err == nil {
		t.Fatal("expected Execute failure after Close")
	}
}

func writeAtomicConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestReload_PublishedCandidateUsesSameCompiler(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := repoConfigPath(t)
	dir := t.TempDir()
	body := mustReadFile(t, src)
	path := writeAtomicConfig(t, dir, "cfg.yaml", body)

	rt, err := lipruntime.Build(ctx, lipruntime.Options{ConfigPath: path, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	before := rt.ReloadStatus().ActiveGeneration

	// Touch-equivalent: rewrite identical content via atomic rename (no-op expected).
	_ = writeAtomicConfig(t, dir, "cfg.yaml", body)
	res := rt.Reload(ctx, lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI, SafeActor: "test"})
	if res.Category != lipruntime.ResultNoop && res.Category != lipruntime.ResultPublished {
		t.Fatalf("reload=%+v", res)
	}
	st := rt.ReloadStatus()
	if st.ActiveGeneration < before {
		t.Fatalf("generation regressed: %d -> %d", before, st.ActiveGeneration)
	}
}

func localStubCall() *lipapi.Call {
	return &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "dogfood-local:stub-default"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
}

func localStubConfigVersion(t *testing.T, version string) string {
	t.Helper()
	body := mustReadFile(t, filepath.Join(filepath.Dir(repoConfigPath(t)), "examples", "dogfood-local-stub.yaml"))
	const original = `text: "[dogfood] local stub"`
	replacement := `text: "` + version + `"`
	if !strings.Contains(body, original) {
		t.Fatalf("local-stub fixture missing %q", original)
	}
	return strings.Replace(body, original, replacement, 1)
}

func publishLocalStubVersion(t *testing.T, rt *lipruntime.Runtime, dir, body string) lipruntime.ReloadResult {
	t.Helper()
	_ = writeAtomicConfig(t, dir, "cfg.yaml", body)
	res := rt.Reload(context.Background(), lipruntime.ReloadTrigger{
		Kind:      lipruntime.TriggerAPI,
		SafeActor: "public-facade-test",
	})
	if res.Category != lipruntime.ResultPublished {
		t.Fatalf("reload category=%q reason=%q", res.Category, res.ReasonCategory)
	}
	return res
}

func TestExecutorView_PreReloadHandlePinsOldStreamAndRoutesNewCalls(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v1 := localStubConfigVersion(t, "generation-one")
	path := writeAtomicConfig(t, dir, "cfg.yaml", v1)
	rt, err := lipruntime.Build(context.Background(), lipruntime.Options{ConfigPath: path, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })

	view := rt.ExecutorView()
	oldStream, err := view.Execute(context.Background(), localStubCall())
	if err != nil {
		t.Fatalf("generation-one Execute: %v", err)
	}
	t.Cleanup(func() { _ = oldStream.Close() })
	before := rt.ReloadStatus().ActiveGeneration
	res := publishLocalStubVersion(t, rt, dir, localStubConfigVersion(t, "generation-two"))
	if res.ActiveGeneration <= before {
		t.Fatalf("generation did not advance: before=%d after=%d", before, res.ActiveGeneration)
	}
	if rt.ExecutorView() != view {
		t.Fatal("public ExecutorView identity changed across publication")
	}

	oldCollected, err := lipapi.Collect(context.Background(), oldStream)
	if err != nil {
		t.Fatalf("collect old stream after publication: %v", err)
	}
	if got := oldCollected.Text.String(); got != "generation-one" {
		t.Fatalf("old stream mixed generations: text=%q", got)
	}

	newStream, err := view.Execute(context.Background(), localStubCall())
	if err != nil {
		t.Fatalf("generation-two Execute: %v", err)
	}
	newCollected, err := lipapi.Collect(context.Background(), newStream)
	if err != nil {
		t.Fatalf("collect new stream: %v", err)
	}
	if got := newCollected.Text.String(); got != "generation-two" {
		t.Fatalf("new call did not route to active generation: text=%q", got)
	}
}
