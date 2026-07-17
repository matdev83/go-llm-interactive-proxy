package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type recordingMeter struct {
	facts []metering.Fact
}

func (r *recordingMeter) Append(ctx context.Context, fact metering.Fact) error {
	_ = ctx
	r.facts = append(r.facts, fact)
	return nil
}

func TestAppendMeteringFact_NilRecorderNoPanic(t *testing.T) {
	t.Parallel()
	ex := &Executor{}
	if err := ex.appendMeteringFact(context.Background(), metering.Fact{FactID: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestEmitEgressFacts_OrderedViaRecorder(t *testing.T) {
	t.Parallel()
	rec := &recordingMeter{}
	ex := &Executor{}
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-eg"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: lipapi.Call{
			ID:       "req-eg",
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}},
		AttemptID:    "b-leg-1",
		BLegID:       "b-leg-1",
		CheckpointID: "be",
		StreamID:     "be-stream",
		Now:          time.Unix(2, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	stream := &retryRecvStream{
		executor: ex,
		traceID:  "req-eg",
		bleg:     b2bua.BLegRecord{BLegID: "b-leg-1"}}
	usageEv := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 1, TotalTokens: 4}
	stream.emitBackendEgressMeteringFact(ctx, metering.AttemptOutcomeWinner, metering.SurfacedYes, usageEv)
	stream.emitFrontendEgressMeteringFact(ctx, usageEv)
	if len(rec.facts) != 2 {
		t.Fatalf("facts=%d", len(rec.facts))
	}
	if rec.facts[0].Boundary != metering.BoundaryBackendEgress {
		t.Fatal(rec.facts[0].Boundary)
	}
	if rec.facts[1].Boundary != metering.BoundaryFrontendEgress {
		t.Fatal(rec.facts[1].Boundary)
	}
}

func TestEmitBackendEgress_LoserFailedCanceledOutcomes(t *testing.T) {
	t.Parallel()
	rec := &recordingMeter{}
	ex := &Executor{}
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-out"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: lipapi.Call{
			ID:       "req-out",
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}},
		AttemptID:    "b-leg-out",
		BLegID:       "b-leg-out",
		CheckpointID: "be",
		StreamID:     "be-stream",
		Now:          time.Unix(2, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	usageEv := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 0, TotalTokens: 1}

	cases := []struct {
		name     string
		outcome  metering.AttemptOutcome
		surfaced metering.SurfacedState
	}{
		{"loser", metering.AttemptOutcomeLoser, metering.SurfacedNo},
		{"failed_swallowed", metering.AttemptOutcomeFailed, metering.SurfacedNo},
		{"failed_surfaced", metering.AttemptOutcomeFailed, metering.SurfacedYes},
		{"canceled", metering.AttemptOutcomeCanceled, metering.SurfacedNo}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rec.facts)
			ex.emitBackendEgressMeteringFact(ctx, "b-leg-out", tc.outcome, tc.surfaced, usageEv)
			if len(rec.facts) != before+1 {
				t.Fatalf("facts=%d want %d", len(rec.facts), before+1)
			}
			got := rec.facts[len(rec.facts)-1]
			if got.Boundary != metering.BoundaryBackendEgress {
				t.Fatalf("boundary=%s", got.Boundary)
			}
			if got.AttemptOutcome != tc.outcome {
				t.Fatalf("outcome=%s want %s", got.AttemptOutcome, tc.outcome)
			}
			if got.Surfaced != tc.surfaced {
				t.Fatalf("surfaced=%s want %s", got.Surfaced, tc.surfaced)
			}
		})
	}
}

func TestEmitFrontendEgress_WithoutWinnerFinalize(t *testing.T) {
	t.Parallel()
	rec := &recordingMeter{}
	ex := &Executor{}
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(50, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-fe-term"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ex.emitFrontendEgressMeteringFact(ctx, "req-fe-term", lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 1, TotalTokens: 3})
	if len(rec.facts) != 1 {
		t.Fatalf("facts=%d", len(rec.facts))
	}
	if rec.facts[0].Boundary != metering.BoundaryFrontendEgress {
		t.Fatal(rec.facts[0].Boundary)
	}
}
