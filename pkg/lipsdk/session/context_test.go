package session_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

func TestSessionViewContextDefensivelyCopiesLabels(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"feature": "enabled"}
	ctx := session.WithSessionView(context.Background(), session.SessionView{
		AuthoritativeSessionID: "session-authoritative",
		Labels:                 labels,
	})
	labels["feature"] = "mutated"

	got, ok := session.SessionViewFromContext(ctx)
	if !ok {
		t.Fatal("session view missing")
	}
	if got.AuthoritativeSessionID != "session-authoritative" || got.Labels["feature"] != "enabled" {
		t.Fatalf("session view = %+v", got)
	}
	got.Labels["feature"] = "mutated-again"
	gotAgain, _ := session.SessionViewFromContext(ctx)
	if gotAgain.Labels["feature"] != "enabled" {
		t.Fatal("retrieved session view aliases stored labels")
	}
}

func TestSecureTurnPolicyContextCarriesOnlyContentFreePolicy(t *testing.T) {
	t.Parallel()

	ctx := session.WithSecureTurnPolicy(context.Background(), session.SecureTurnPolicyView{TranscriptEnabled: true})
	got, ok := session.SecureTurnPolicyFromContext(ctx)
	if !ok || !got.TranscriptEnabled {
		t.Fatalf("secure turn policy = %+v ok=%v", got, ok)
	}
}
