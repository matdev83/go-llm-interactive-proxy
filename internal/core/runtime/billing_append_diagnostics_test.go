package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type failingTerminalSink struct{ err error }

func (f failingTerminalSink) AppendCall(context.Context, billing.CallUsageRecord) error { return f.err }
func (f failingTerminalSink) AppendLeg(context.Context, billing.CallLegUsageRecord) error {
	return f.err
}

func TestAppendIndependentCallLegLogsBoundedFailure(t *testing.T) {
	var buf bytes.Buffer
	ex := TestExecutor()
	ex.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ex.TerminalUsageSink = failingTerminalSink{err: errors.New("local spool unavailable")}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	ex.appendIndependentCallLeg(context.Background(), callID, billing.CallLegUsageRecord{
		ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1, BackendID: "be", ProviderID: "be", ModelID: "m",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
		Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo,
		Evidence: billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
	})
	got := buf.String()
	if !strings.Contains(got, "billing call-leg append failed") {
		t.Fatalf("want bounded append warning, got %s", got)
	}
	if strings.Contains(got, "billing_call_leg_append_critical") {
		t.Fatalf("local sink failure must not invoke removed outbox critical path: %s", got)
	}
}
