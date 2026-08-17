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

type failingLegAppender struct{ err error }

func (f failingLegAppender) AppendCallLegUsage(context.Context, billing.CallLegUsageRecord) error {
	return f.err
}

type enqueueFailOutbox struct{}

func (enqueueFailOutbox) EnqueueCallUsageAppend(context.Context, billing.CallUsageRecord, string) error {
	return errors.New("outbox down")
}
func (enqueueFailOutbox) EnqueueCallLegUsageAppend(context.Context, billing.CallLegUsageRecord, string) error {
	return errors.New("outbox down")
}
func (enqueueFailOutbox) ListPendingUsageAppendWork(context.Context, int) ([]billing.UsageAppendWork, error) {
	return nil, nil
}
func (enqueueFailOutbox) MarkUsageAppendProcessed(context.Context, string) error { return nil }
func (enqueueFailOutbox) DeferUsageAppend(context.Context, string, string) error { return nil }
func (enqueueFailOutbox) FailUsageAppend(context.Context, string, string) error  { return nil }

type enqueueOKOutbox struct{}

func (enqueueOKOutbox) EnqueueCallUsageAppend(context.Context, billing.CallUsageRecord, string) error {
	return nil
}
func (enqueueOKOutbox) EnqueueCallLegUsageAppend(context.Context, billing.CallLegUsageRecord, string) error {
	return nil
}
func (enqueueOKOutbox) ListPendingUsageAppendWork(context.Context, int) ([]billing.UsageAppendWork, error) {
	return nil, nil
}
func (enqueueOKOutbox) MarkUsageAppendProcessed(context.Context, string) error { return nil }
func (enqueueOKOutbox) DeferUsageAppend(context.Context, string, string) error { return nil }
func (enqueueOKOutbox) FailUsageAppend(context.Context, string, string) error  { return nil }

func TestAppendIndependentCallLegLogsCriticalOnDualFailure(t *testing.T) {
	primary := failingLegAppender{err: errors.New("primary append boom")}
	retry, err := billing.NewRetryingCallLegUsageAppender(primary, enqueueFailOutbox{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	ex := TestExecutor()
	ex.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ex.CallLegUsageAppender = retry
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	ex.appendIndependentCallLeg(context.Background(), callID, billing.LegUsageRecord{
		ALegID: "a-1", BLegID: "b-1", Seq: 1, BackendID: "be", ProviderID: "be", ModelID: "m",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
		Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo,
		Evidence: billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
	})
	if !strings.Contains(buf.String(), "billing_call_leg_append_critical") {
		t.Fatalf("want critical log, got %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"level":"ERROR"`) && !strings.Contains(buf.String(), `"level":"error"`) {
		t.Fatalf("want ERROR level, got %s", buf.String())
	}
}

func TestAppendIndependentCallLegWarnsWhenOutboxArmed(t *testing.T) {
	primary := failingLegAppender{err: errors.New("primary append boom")}
	retry, err := billing.NewRetryingCallLegUsageAppender(primary, enqueueOKOutbox{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	ex := TestExecutor()
	ex.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ex.CallLegUsageAppender = retry
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	ex.appendIndependentCallLeg(context.Background(), callID, billing.LegUsageRecord{
		ALegID: "a-1", BLegID: "b-1", Seq: 1, BackendID: "be", ProviderID: "be", ModelID: "m",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
		Outcome: billing.LegOutcomeFailed, Surfaced: billing.SurfacedNo,
		Evidence: billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
	})
	got := buf.String()
	if strings.Contains(got, "billing_call_leg_append_critical") {
		t.Fatalf("outbox-armed failure must not be critical: %s", got)
	}
	if !strings.Contains(got, "billing call-leg append failed") {
		t.Fatalf("want warn message, got %s", got)
	}
	if !strings.Contains(got, `"level":"WARN"`) && !strings.Contains(got, `"level":"warn"`) {
		t.Fatalf("want WARN level, got %s", got)
	}
}
