package lipruntime

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type recordingCrossGenerationBLeg struct {
	canceled atomic.Bool
	closed   atomic.Bool
}

func (b *recordingCrossGenerationBLeg) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, context.Canceled
}

func (b *recordingCrossGenerationBLeg) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	b.canceled.Store(true)
	return lipapi.CancelResult{Mode: lipapi.CancelModeProvider}
}

func (b *recordingCrossGenerationBLeg) Close() error {
	b.closed.Store(true)
	return nil
}

func atomicReplaceTestConfig(t *testing.T, path, body string) {
	t.Helper()
	tmp := path + ".replacement"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorView_PrePublicationALegReachesProcessOwnedWorkAfterReload(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(testConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	const original = `text: "[dogfood] local stub"`
	if !strings.Contains(string(raw), original) {
		t.Fatalf("fixture missing %q", original)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	atomicReplaceTestConfig(t, path, string(raw))

	rt, err := Build(context.Background(), Options{ConfigPath: path, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	view := rt.ExecutorView() // handle obtained before publication
	if view == nil || rt.host == nil || rt.host.Process == nil || rt.host.Process.ALegLifecycle == nil {
		t.Fatal("incomplete public runtime/process lifecycle composition")
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
	if call.Session.ALegID == "" {
		t.Fatal("public Execute did not assign an A-leg ID")
	}

	work := &recordingCrossGenerationBLeg{}
	aLeg := rt.host.Process.ALegLifecycle.StartALeg(call.Session.ALegID)
	if err := aLeg.RegisterBLeg(context.Background(), leglifecycle.BLegHandle{
		ID:      "active-backend-work",
		Attempt: work,
	}); err != nil {
		t.Fatalf("RegisterBLeg: %v", err)
	}

	changed := strings.Replace(string(raw), original, `text: "generation-two"`, 1)
	atomicReplaceTestConfig(t, path, changed)
	res := rt.Reload(context.Background(), ReloadTrigger{Kind: TriggerAPI, SafeActor: "facade-cancel-test"})
	if res.Category != ResultPublished {
		t.Fatalf("reload category=%q reason=%q", res.Category, res.ReasonCategory)
	}
	if rt.ExecutorView() != view {
		t.Fatal("ExecutorView identity changed across publication")
	}

	if err := view.CancelALeg(context.Background(), lipapi.ALegCancelRequest{
		ALegID: call.Session.ALegID,
		Reason: "cross-generation-public-facade-test",
	}); err != nil {
		t.Fatalf("CancelALeg: %v", err)
	}
	if !work.canceled.Load() {
		t.Fatal("pre-publication active backend work did not receive cancellation")
	}
	if !work.closed.Load() {
		t.Fatal("pre-publication active backend work was not closed")
	}
}
