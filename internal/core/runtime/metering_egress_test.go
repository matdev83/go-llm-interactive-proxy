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
	fe, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-eg"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: lipapi.Call{
			ID:       "req-eg",
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
		},
		AttemptID:    "b-leg-1",
		BLegID:       "b-leg-1",
		CheckpointID: "be",
		StreamID:     "be-stream",
		FEStreamID:   fe.Public.StreamID,
		Now:          time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	stream := &retryRecvStream{
		executor: ex,
		traceID:  "req-eg",
		bleg:     b2bua.BLegRecord{BLegID: "b-leg-1"},
	}
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
