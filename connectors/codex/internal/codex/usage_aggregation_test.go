package codex

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestUsageEvidence_normalizesProviderAuthorityAndRejectsEmptyUsage(t *testing.T) {
	if got := usageEvidence(&completedUsage{}); got != nil {
		t.Fatalf("empty provider usage = %#v, want nil", got)
	}
	input := int64(7)
	got := usageEvidence(&completedUsage{InputTokens: &input})
	if got == nil || !got.UsagePresence.InputTokens || got.Source != lipapi.UsageSourceProviderReported || got.Authority != lipapi.UsageAuthorityAuthoritative {
		t.Fatalf("provider evidence = %#v", got)
	}
}

func TestAccountingEvidence_preservesPresenceAndProviderMetadata(t *testing.T) {
	usage := &NativeUsageEvidence{
		InputTokens: 41, OutputTokens: 5, TotalTokens: 46,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Source:        lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		DedupeKey: "codex-compaction:turn-1",
	}
	evidence := accountingEvidence(usage)
	if err := validateNativeUsageEvidenceForTest(evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.DedupeKey != usage.DedupeKey || evidence.InputTokens == nil || *evidence.InputTokens != 41 {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestNativeUsageSidebandStream_drainsEvidenceOnce(t *testing.T) {
	input := int64(13)
	stream := newNativeUsageSidebandStream(nil, &NativeUsageEvidence{
		InputTokens: input, UsagePresence: lipapi.UsagePresence{InputTokens: true},
		Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
	}, io.EOF)
	sideband, ok := stream.(*nativeUsageSidebandStream)
	if !ok {
		t.Fatal("expected native sideband stream")
	}
	first := sideband.DrainAccountingEvidence()
	if len(first) != 1 || first[0].InputTokens == nil || *first[0].InputTokens != input {
		t.Fatalf("first drain = %#v", first)
	}
	if second := sideband.DrainAccountingEvidence(); len(second) != 0 {
		t.Fatalf("second drain = %#v, want empty", second)
	}
	if _, err := sideband.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("open error = %v, want EOF", err)
	}
}

func TestNativeUsageSidebandStream_preservesOpenError(t *testing.T) {
	openErr := errors.New("normal request failed")
	stream := newNativeUsageSidebandStream(nil, &NativeUsageEvidence{
		InputTokens: 13, UsagePresence: lipapi.UsagePresence{InputTokens: true},
		Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
	}, openErr)
	sideband := stream.(*nativeUsageSidebandStream)
	if _, err := sideband.Recv(context.Background()); !errors.Is(err, openErr) {
		t.Fatalf("open error = %v, want %v", err, openErr)
	}
}

func TestNativeUsageSidebandStream_rejectsNilAndCanceledContext(t *testing.T) {
	stream := newNativeUsageSidebandStream(nil, &NativeUsageEvidence{
		InputTokens: 13, UsagePresence: lipapi.UsagePresence{InputTokens: true},
		Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
	}, io.EOF)
	sideband := stream.(*nativeUsageSidebandStream)
	if _, err := sideband.Recv(nil); !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("nil context error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sideband.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
}

func TestNativeContextTelemetry_snapshotIsFixedAndPrivate(t *testing.T) {
	telemetry := newNativeContextTelemetry()
	telemetry.recordContext(101, 41, 1001, 501)
	telemetry.recordReasoning(reasoningTelemetryRequested)
	telemetry.recordCheckpoint(checkpointTelemetryHit)
	telemetry.recordCheckpoint(checkpointTelemetryMismatch)
	telemetry.recordCompaction(compactionTelemetrySuccess, 17)
	telemetry.recordCompaction(compactionTelemetryCanceled, 3)

	snapshot := telemetry.snapshot()
	if snapshot.ContextBeforeTokens != 101 || snapshot.ContextAfterTokens != 41 || snapshot.ContextBeforeBytes != 1001 || snapshot.ContextAfterBytes != 501 {
		t.Fatalf("context snapshot = %#v", snapshot)
	}
	if snapshot.ReasoningRequested != 1 || snapshot.CheckpointHits != 1 || snapshot.CheckpointMismatches != 1 {
		t.Fatalf("reasoning/checkpoint snapshot = %#v", snapshot)
	}
	if snapshot.CompactionSuccesses != 1 || snapshot.CompactionCanceled != 1 || snapshot.CompactionUsageTokens != 20 {
		t.Fatalf("compaction snapshot = %#v", snapshot)
	}
	for _, secret := range []string{"opaque-ciphertext-secret", "prompt-marker-secret", "account-token-secret"} {
		if strings.Contains(snapshot.String(), secret) {
			t.Fatalf("snapshot leaked %q: %s", secret, snapshot.String())
		}
	}
}

func TestNativeContextTelemetry_concurrentSnapshots(t *testing.T) {
	start := time.Unix(100, 0)
	clock := start
	telemetry := newNativeContextTelemetryWithClock(func() time.Time { return clock })
	telemetry.recordLatency(start)
	clock = start.Add(17 * time.Millisecond)
	telemetry.recordLatency(start)
	if got := telemetry.snapshot().CompactionLatencyMillis; got != 17 {
		t.Fatalf("latency = %d, want 17", got)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			telemetry.recordReasoning(reasoningTelemetryRequested)
			_ = telemetry.snapshot().ReasoningRequested
		}()
	}
	wg.Wait()
	if got := telemetry.snapshot().ReasoningRequested; got != 8 {
		t.Fatalf("reasoning requests = %d, want 8", got)
	}
}

func validateNativeUsageEvidenceForTest(e backendplugin.AccountingEvidence) error {
	return backendplugin.ValidateAccountingEvidence(e)
}

type usageAggregateTestStream struct {
	events []lipapi.Event
	err    error
	idx    int
}

func (s *usageAggregateTestStream) Recv(context.Context) (lipapi.Event, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return lipapi.Event{}, err
	}
	return lipapi.Event{}, io.EOF
}

func (s *usageAggregateTestStream) Close() error { return nil }

func (s *usageAggregateTestStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func isNativeContentEvent(kind lipapi.EventKind) bool {
	switch kind {
	case lipapi.EventTextDelta, lipapi.EventReasoningDelta, lipapi.EventReasoningSignatureDelta,
		lipapi.EventReasoningOpaqueDelta, lipapi.EventReasoningPart,
		lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished,
		lipapi.EventAssistantImageRef, lipapi.EventAssistantFileRef:
		return true
	default:
		return false
	}
}
