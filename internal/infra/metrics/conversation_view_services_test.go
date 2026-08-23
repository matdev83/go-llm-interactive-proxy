package metrics

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNewConversationViewServicesWithMetrics_WiresObserver(t *testing.T) {
	reg := prometheus.NewRegistry()
	b := &Bundle{
		Registry:         reg,
		ConversationView: RegisterConversationViewProm(reg),
	}
	b.conversationSink = NewConversationViewSink(b.ConversationView)
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "svc-metrics-aleg"
	if err := store.CreateALeg(ctx, aLegID); err != nil {
		t.Fatalf("create aleg: %v", err)
	}
	_, writer, err := NewConversationViewServicesWithMetrics(store, aLegID, nil, b)
	if err != nil {
		t.Fatalf("services: %v", err)
	}
	_, err = writer.Put(ctx, steering.PutRequest{
		OverlayID:           "ov1",
		Message:             steering.Message{Role: lipapi.RoleSystem, Text: "hello"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "test_reason",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "lip_conversation_view_steering_mutations_total" {
			for _, m := range mf.Metric {
				if m.GetCounter().GetValue() > 0 {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected mutation metric incremented via composition helper")
	}
}

func TestNewSteeringWriterWithMetrics_PanicIsolated(t *testing.T) {
	reg := prometheus.NewRegistry()
	b := &Bundle{
		Registry:         reg,
		ConversationView: RegisterConversationViewProm(reg),
	}
	b.conversationSink = NewConversationViewSink(b.ConversationView)
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegID := "svc-panic-aleg"
	_ = store.CreateALeg(ctx, aLegID)
	w, err := NewSteeringWriterWithMetrics(store, aLegID, nil, b)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	_ = w
}
