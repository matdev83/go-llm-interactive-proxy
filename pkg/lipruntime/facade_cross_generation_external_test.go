package lipruntime_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
)

func TestExecutorView_PrePublicationALegCancelSurvivesReload(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const original = `text: "[dogfood] local stub"`
	if !strings.Contains(string(raw), original) {
		t.Fatalf("fixture missing %q", original)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	rt, err := lipruntime.Build(context.Background(), lipruntime.Options{ConfigPath: path, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	view := rt.ExecutorView()
	if view == nil {
		t.Fatal("expected ExecutorView")
	}

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "dogfood-local:stub-default"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("create pre-publication A-leg")},
		}},
	}
	oldStream, err := view.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("pre-publication Execute: %v", err)
	}
	t.Cleanup(func() { _ = oldStream.Close() })
	oldALeg := call.Session.ALegID
	if oldALeg == "" {
		t.Fatal("public Execute did not assign an A-leg ID")
	}

	changed := strings.Replace(string(raw), original, `text: "generation-two"`, 1)
	tmp := path + ".replacement"
	if err := os.WriteFile(tmp, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	res := rt.Reload(context.Background(), lipruntime.ReloadTrigger{Kind: lipruntime.TriggerAPI, SafeActor: "facade-cancel-test"})
	if res.Category != lipruntime.ResultPublished {
		t.Fatalf("reload category=%q reason=%q", res.Category, res.ReasonCategory)
	}
	if rt.ExecutorView() != view {
		t.Fatal("ExecutorView identity changed across publication")
	}

	// Cancel before Collect so stream completion cannot race the cancel path.
	if err := view.CancelALeg(context.Background(), lipapi.ALegCancelRequest{
		ALegID: oldALeg,
		Reason: "cross-generation-public-facade-test",
	}); err != nil {
		t.Fatalf("CancelALeg: %v", err)
	}
	oldCollected, collectErr := lipapi.Collect(context.Background(), oldStream)
	if collectErr == nil {
		if got := oldCollected.Text.String(); got != "" && got != "[dogfood] local stub" {
			t.Fatalf("pre-publication stream mixed generations after cancel: %q", got)
		}
	}

	// Same ExecutorView must still reach process-owned dispatch for the new generation.
	call2 := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "dogfood-local:stub-default"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("post-reload generation")},
		}},
	}
	newStream, err := view.Execute(context.Background(), call2)
	if err != nil {
		t.Fatalf("post-reload Execute: %v", err)
	}
	t.Cleanup(func() { _ = newStream.Close() })
	newCollected, err := lipapi.Collect(context.Background(), newStream)
	if err != nil {
		t.Fatalf("post-reload Collect: %v", err)
	}
	if got := newCollected.Text.String(); got != "generation-two" {
		t.Fatalf("ExecutorView must reach process-owned work across generations; got %q", got)
	}
}
