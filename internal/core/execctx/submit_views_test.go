package execctx_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestViewsFromSubmit_sessionAndTrace(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	aLeg := b2bua.ALegRecord{
		ALegID:        "aleg-1",
		ContinuityKey: "ck",
		CreatedAt:     now,
		LastSeenAt:    now,
	}
	call := lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "client-sess"},
	}
	v := execctx.ViewsFromSubmit("trace-1", aLeg, call, map[string]string{"k": "v"})
	if v.Session.SessionID != "client-sess" || v.Session.ALegID != "aleg-1" {
		t.Fatalf("session: %+v", v.Session)
	}
	if !v.Session.IsNew {
		t.Fatal("want IsNew when CreatedAt equals LastSeenAt")
	}
	if v.Attempt.TraceID != "trace-1" {
		t.Fatalf("attempt trace: %q", v.Attempt.TraceID)
	}
	if v.Annotations["k"] != "v" {
		t.Fatalf("annotations: %v", v.Annotations)
	}
}
