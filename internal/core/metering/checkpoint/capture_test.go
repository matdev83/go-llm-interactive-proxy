package checkpoint_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestCaptureFrontendIngress_ClonesCall(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "req-1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Session: lipapi.SessionRef{ALegID: "a-1", ResumeToken: "secret-resume"},
	}
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         call,
		Scope:        scope.PrincipalScopeView{PrincipalID: scope.Known("p1")},
		CheckpointID: "cp-fe-1",
		StreamID:     "stream-req-1",
		Now:          time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Public.Boundary != metering.BoundaryFrontendIngress {
		t.Fatalf("boundary=%q", snap.Public.Boundary)
	}
	if snap.Call.Session.ResumeToken != "" {
		t.Fatal("resume token must be stripped from snapshot call")
	}
	if call.Session.ResumeToken != "secret-resume" {
		t.Fatal("original call resume token must remain unchanged")
	}
	call.Messages[0].Parts[0].Text = "mutated"
	if snap.Call.Messages[0].Parts[0].Text != "hello" {
		t.Fatal("snapshot call must be independent of later mutations")
	}
	if err := snap.Public.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBindScope_DoesNotRewriteCall(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{
			ID: "req-1",
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("x")},
			}},
		},
		CheckpointID: "cp-1",
		StreamID:     "s-1",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := snap.Call.Messages[0].Parts[0].Text
	snap.BindScope(scope.PrincipalScopeView{PrincipalID: scope.Known("later")})
	if snap.Public.Scope.PrincipalID.String() != "later" {
		t.Fatalf("scope=%v", snap.Public.Scope.PrincipalID)
	}
	if snap.Call.Messages[0].Parts[0].Text != before {
		t.Fatal("bind scope must not rewrite call messages")
	}
}

func TestReuseSameFrontendIngressAcrossAttempts(t *testing.T) {
	t.Parallel()
	holder := &checkpoint.RequestHolder{}
	in := checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-1"},
		CheckpointID: "cp-shared",
		StreamID:     "stream-shared",
		Now:          time.Unix(1, 0).UTC(),
	}
	first, err := holder.CaptureOrReuseFrontendIngress(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-1", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("later")}}}},
		CheckpointID: "cp-other",
		StreamID:     "stream-other",
		Now:          time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Public.CheckpointID != second.Public.CheckpointID || first.Public.StreamID != second.Public.StreamID {
		t.Fatal("must reuse one frontend-ingress checkpoint per logical request")
	}
	if len(second.Call.Messages) != 0 {
		t.Fatal("reuse must keep original immutable call, not later attempt derivation")
	}
}

func TestCaptureCreatesNoReservations(t *testing.T) {
	t.Parallel()
	// Capture helpers have no UsageAuthority dependency; this test documents
	// requirement 2.5 by ensuring CaptureFrontendIngress is a pure function.
	_, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req"},
		CheckpointID: "cp",
		StreamID:     "s",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestHolder_StoreBackendIngressConcurrent(t *testing.T) {
	t.Parallel()
	h := &checkpoint.RequestHolder{}
	const n = 32
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("attempt-%d", i)
			_, err := h.StoreBackendIngress(checkpoint.BackendIngressInput{
				Call:         lipapi.Call{ID: "req-race", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}},
				AttemptID:    id,
				BLegID:       id,
				ALegID:       "a-1",
				BackendID:    "b",
				Model:        "m",
				CheckpointID: "cp-" + id,
				StreamID:     "s-" + id,
				Now:          time.Unix(1, 0).UTC(),
			})
			if err != nil {
				errCh <- err
				return
			}
			if got := h.BackendIngressFor(id); got == nil {
				errCh <- fmt.Errorf("missing snapshot for %s", id)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
