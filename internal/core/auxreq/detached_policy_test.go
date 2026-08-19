package auxreq_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

type detachedCaptureRunner struct {
	ctx        context.Context
	call       lipapi.Call
	opens      int
	mutateCall func(*lipapi.Call)
}

func (r *detachedCaptureRunner) Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	r.opens++
	r.ctx = ctx
	if r.mutateCall != nil {
		r.mutateCall(call)
	}
	if call != nil {
		r.call = lipapi.CloneCall(*call)
	}
	return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
}

func TestClient_StreamProjectsDetachedRoleIntoTrustedContext(t *testing.T) {
	t.Parallel()

	runner := &detachedCaptureRunner{mutateCall: func(call *lipapi.Call) {
		call.Extensions["lip.aux.lineage.v1"] = json.RawMessage(`{"role":"attacker"}`)
	}}
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return runner })
	call := &lipapi.Call{}
	_, err := client.Stream(context.Background(), auxiliary.Request{
		Call:        call,
		SessionMode: auxiliary.SessionModeDetached,
		Role:        "  compaction_continuity_extractor  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := execctx.DetachedSessionFromContext(runner.ctx)
	if !ok {
		t.Fatal("missing detached session metadata")
	}
	if meta.AuxiliaryRole != "compaction_continuity_extractor" {
		t.Fatalf("trusted detached role=%q", meta.AuxiliaryRole)
	}
	if !strings.Contains(string(runner.call.Extensions["lip.aux.lineage.v1"]), "attacker") {
		t.Fatal("test runner did not replace observational canonical lineage")
	}
}

func TestClient_Stream_marksTrustedDetachedPolicyAndParentLineage(t *testing.T) {
	t.Parallel()

	runner := &detachedCaptureRunner{}
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return runner })
	call := &lipapi.Call{Session: lipapi.SessionRef{
		AuthoritativeSessionID: "parent-session",
		ALegID:                 "parent-a-leg",
		ContinuityKey:          "parent-branch",
	}}
	_, err := client.Stream(context.Background(), auxiliary.Request{
		Call:                call,
		SessionMode:         auxiliary.SessionModeDetached,
		Role:                "compaction_continuity_extractor",
		Visibility:          "private",
		ParentTraceID:       "parent-trace",
		ParentALegID:        "parent-a-leg",
		ParentBranchBinding: "parent-branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	mode, ok := execctx.SessionModeFromContext(runner.ctx)
	if !ok || mode != execctx.SessionModeDetached {
		t.Fatalf("session mode: ok=%v mode=%v; want trusted detached", ok, mode)
	}
	meta, ok := execctx.DetachedSessionFromContext(runner.ctx)
	if !ok {
		t.Fatal("missing detached session metadata")
	}
	if meta.ParentSessionID != "parent-session" || meta.ParentALegID != "parent-a-leg" || meta.ParentTraceID != "parent-trace" || meta.ParentBranchBinding != "parent-branch" {
		t.Fatalf("detached lineage: %+v", meta)
	}
}

func TestClient_StreamZeroValueRequestRetainsNormalSessionMode(t *testing.T) {
	t.Parallel()

	runner := &detachedCaptureRunner{}
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return runner })
	if _, err := client.Stream(context.Background(), auxiliary.Request{Call: &lipapi.Call{}, Role: "ordinary"}); err != nil {
		t.Fatal(err)
	}
	if mode, marked := execctx.SessionModeFromContext(runner.ctx); marked || mode != execctx.SessionModeNormal {
		t.Fatalf("zero-value auxiliary request installed detached mode: marked=%v mode=%v", marked, mode)
	}
	if _, marked := execctx.DetachedSessionFromContext(runner.ctx); marked {
		t.Fatal("zero-value auxiliary request installed detached role metadata")
	}
}

func TestClient_StreamRejectsInvalidDetachedRoleBeforeExecutorOpen(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		role string
	}{
		{name: "whitespace-only", role: " \t\n "},
		{name: "control-character", role: "compaction\ncontinuity"},
		{name: "embedded-space", role: "compaction continuity"},
		{name: "oversized", role: strings.Repeat("r", auxreq.MaxDetachedAuxiliaryRoleBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &detachedCaptureRunner{}
			client := auxreq.NewClient(func() auxreq.ExecutorRunner { return runner })
			_, err := client.Stream(context.Background(), auxiliary.Request{
				Call:        &lipapi.Call{},
				SessionMode: auxiliary.SessionModeDetached,
				Role:        tc.role,
			})
			if err == nil {
				t.Fatal("invalid detached role unexpectedly accepted")
			}
			if runner.opens != 0 {
				t.Fatalf("executor opened %d times after invalid role", runner.opens)
			}
		})
	}
}

func TestClient_StreamDoesNotExposeDetachedControlAsCanonicalCallField(t *testing.T) {
	t.Parallel()

	runner := &detachedCaptureRunner{}
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return runner })
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "extractor:model"}}
	if _, err := client.Stream(context.Background(), auxiliary.Request{
		Call:                call,
		SessionMode:         auxiliary.SessionModeDetached,
		ParentBranchBinding: "private-branch-binding",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.call.Extensions["lip.aux.session_mode"]; ok {
		t.Fatal("detached session mode must not be encoded as a canonical/provider-visible field")
	}
	if strings.Contains(string(runner.call.Extensions["lip.aux.lineage.v1"]), "private-branch-binding") {
		t.Fatal("parent branch binding must not be encoded into provider-visible lineage")
	}
}

func TestClient_StreamPreservesCapturedParentBranchBinding(t *testing.T) {
	t.Parallel()

	runner := &detachedCaptureRunner{}
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return runner })
	ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{
		ParentSessionID:     "captured-session",
		ParentALegID:        "captured-a-leg",
		ParentTraceID:       "captured-trace",
		ParentBranchBinding: "captured-branch-binding",
	})
	call := &lipapi.Call{Session: lipapi.SessionRef{
		AuthoritativeSessionID: "call-session",
		ALegID:                 "call-a-leg",
		ContinuityKey:          "call-branch-hint",
	}}
	if _, err := client.Stream(ctx, auxiliary.Request{Call: call, SessionMode: auxiliary.SessionModeDetached}); err != nil {
		t.Fatal(err)
	}
	meta, ok := execctx.DetachedSessionFromContext(runner.ctx)
	if !ok {
		t.Fatal("missing detached session metadata")
	}
	if meta.ParentBranchBinding != "captured-branch-binding" {
		t.Fatalf("captured branch binding replaced: %+v", meta)
	}
}

func TestClient_StreamDoesNotDeriveParentBranchBindingFromCallHint(t *testing.T) {
	t.Parallel()

	runner := &detachedCaptureRunner{}
	client := auxreq.NewClient(func() auxreq.ExecutorRunner { return runner })
	call := &lipapi.Call{Session: lipapi.SessionRef{ContinuityKey: "client-call-hint"}}
	if _, err := client.Stream(context.Background(), auxiliary.Request{
		Call:        call,
		SessionMode: auxiliary.SessionModeDetached,
	}); err != nil {
		t.Fatal(err)
	}
	meta, ok := execctx.DetachedSessionFromContext(runner.ctx)
	if !ok {
		t.Fatal("missing detached session metadata")
	}
	if meta.ParentBranchBinding != "" {
		t.Fatalf("client call hint became parent branch authority: %+v", meta)
	}
}
