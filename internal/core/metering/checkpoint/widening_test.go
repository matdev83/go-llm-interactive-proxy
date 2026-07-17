package checkpoint_test

import (
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestBillableWidened_DetectsAddedMessage(t *testing.T) {
	t.Parallel()
	base := lipapi.Call{
		ID: "r",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("a")}}}}
	wide := lipapi.CloneCall(base)
	wide.Messages = append(wide.Messages, lipapi.Message{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart("extra")}})
	ok, err := checkpoint.BillableWidened(base, wide)
	if err != nil || !ok {
		t.Fatalf("widened=%v err=%v", ok, err)
	}
	if err := checkpoint.AssertNotWidened(base, wide); !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("err=%v", err)
	}
	same := lipapi.CloneCall(base)
	if err := checkpoint.AssertNotWidened(base, same); err != nil {
		t.Fatal(err)
	}
}

func TestBillableWidened_AllowsMaxOutputNarrowing(t *testing.T) {
	t.Parallel()
	max := 100
	base := lipapi.Call{
		ID: "r",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("a")}}},
		Options: lipapi.GenerationOptions{MaxOutputTokens: &max}}
	narrowed := lipapi.CloneCall(base)
	lower := 40
	narrowed.Options.MaxOutputTokens = &lower
	if err := checkpoint.AssertNotWidened(base, narrowed); err != nil {
		t.Fatalf("narrowing MaxOutputTokens must be allowed: %v", err)
	}
	raised := lipapi.CloneCall(base)
	higher := 200
	raised.Options.MaxOutputTokens = &higher
	if err := checkpoint.AssertNotWidened(base, raised); !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("raising MaxOutputTokens must widen: %v", err)
	}
}

func TestCaptureBackendIngress(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: lipapi.Call{
			ID: "req-1",
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("final")}}},
			Session: lipapi.SessionRef{ALegID: "a-1"}},
		AttemptID:    "att-1",
		BLegID:       "b-1",
		BackendID:    "openai",
		Model:        "gpt",
		CheckpointID: "be-in-1",
		StreamID:     "be-stream-1",
		Now:          time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Public.Boundary != metering.BoundaryBackendIngress {
		t.Fatal(snap.Public.Boundary)
	}
	if snap.Public.Lifecycle != metering.LifecycleBackendAttempt {
		t.Fatal(snap.Public.Lifecycle)
	}
	if snap.Public.Correlation.AttemptID != "att-1" || snap.Public.Correlation.BLegID != "b-1" {
		t.Fatalf("%+v", snap.Public.Correlation)
	}
	if err := snap.Public.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestHolder_ParallelBackendIngressShareFEStream(t *testing.T) {
	t.Parallel()
	h := &checkpoint.RequestHolder{}
	fe, err := h.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-1"},
		CheckpointID: "fe-1",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:         lipapi.Call{ID: "req-1", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("a")}}}},
		AttemptID:    "att-a",
		BLegID:       "b-a",
		CheckpointID: "be-a",
		StreamID:     "be-a",
		Now:          time.Unix(2, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:         lipapi.Call{ID: "req-1", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("b")}}}},
		AttemptID:    "att-b",
		BLegID:       "b-b",
		CheckpointID: "be-b",
		StreamID:     "be-b",
		Now:          time.Unix(3, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if a.Public.StreamID == b.Public.StreamID {
		t.Fatal("parallel legs need independent backend streams")
	}
	if h.FrontendIngress.Public.StreamID != fe.Public.StreamID {
		t.Fatal("FE stream must remain shared")
	}
}
