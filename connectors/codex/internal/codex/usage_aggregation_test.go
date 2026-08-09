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
)

func TestCompactionUsageEvent_isSeparateAuthoritativeProviderEvidence(t *testing.T) {
	event := nativeUsageEvent(&NativeUsageEvidence{
		InputTokens: 41, OutputTokens: 5, TotalTokens: 46,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Source:        lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
	})
	if event.Kind != lipapi.EventUsageDelta {
		t.Fatalf("kind = %q", event.Kind)
	}
	if len(event.UsageScopes) != 1 {
		t.Fatalf("usage scopes = %d, want one compaction scope", len(event.UsageScopes))
	}
	scope := event.UsageScopes[0]
	if scope.InputTokens != 41 || scope.OutputTokens != 5 || scope.TotalTokens != 46 {
		t.Fatalf("scope = %#v", scope)
	}
	if scope.Accounting.Plane != lipapi.UsagePlaneProviderBillable ||
		scope.Accounting.Source != lipapi.UsageSourceProviderReported ||
		scope.Accounting.Authority != lipapi.UsageAuthorityAuthoritative {
		t.Fatalf("accounting = %#v", scope.Accounting)
	}
	if event.Accounting != scope.Accounting {
		t.Fatalf("event accounting = %#v, scope accounting = %#v", event.Accounting, scope.Accounting)
	}
	if event.InputTokens != 0 || event.OutputTokens != 0 || event.TotalTokens != 0 {
		t.Fatalf("provider evidence leaked legacy client counters: %#v", event)
	}
}

func TestNativeUsageEvent_doesNotInventAuthorityOrPresence(t *testing.T) {
	event := nativeUsageEvent(&NativeUsageEvidence{})
	if event.Kind != "" {
		t.Fatalf("zero evidence produced event: %#v", event)
	}

	event = nativeUsageEvent(&NativeUsageEvidence{InputTokens: 13})
	if event.Kind != lipapi.EventUsageDelta {
		t.Fatalf("non-zero evidence was dropped: %#v", event)
	}
	if event.Accounting.Source != lipapi.UsageSourceUnknown || event.Accounting.Authority != lipapi.UsageAuthorityUnknown {
		t.Fatalf("unclassified evidence became billable: %#v", event.Accounting)
	}
	if event.UsagePresence.Any() {
		t.Fatalf("presence was invented: %#v", event.UsagePresence)
	}
}

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

func TestNativeUsageStream_emitsCompactionEvidenceOnceBeforeNormalUsage(t *testing.T) {
	base := &usageAggregateTestStream{events: []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{
			Kind: lipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 2, TotalTokens: 9,
			Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative},
		},
		{Kind: lipapi.EventResponseFinished},
	}}
	stream := newNativeUsageStream(base, &NativeUsageEvidence{InputTokens: 41, OutputTokens: 5, TotalTokens: 46, UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true}, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative})

	var events []lipapi.Event
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4: %#v", len(events), events)
	}
	if events[0].Kind != lipapi.EventResponseStarted || events[1].Kind != lipapi.EventUsageDelta {
		t.Fatalf("ordering = %#v", []lipapi.EventKind{events[0].Kind, events[1].Kind})
	}
	if events[1].UsageScopes[0].InputTokens != 41 || events[2].InputTokens != 7 {
		t.Fatalf("usage ordering/totals = %#v", events)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("second terminal recv = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeUsageStream_preservesEvidenceWhenNormalStreamErrors(t *testing.T) {
	base := &usageAggregateTestStream{events: []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
	}, err: errUsageAggregateTest}
	stream := newNativeUsageStream(base, &NativeUsageEvidence{InputTokens: 13, TotalTokens: 13, UsagePresence: lipapi.UsagePresence{InputTokens: true, TotalTokens: true}, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative})

	first, err := stream.Recv(context.Background())
	if err != nil || first.Kind != lipapi.EventResponseStarted {
		t.Fatalf("first = %#v, err = %v", first, err)
	}
	if usage, err := stream.Recv(context.Background()); err != nil || usage.Kind != lipapi.EventUsageDelta {
		t.Fatal(err)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, errUsageAggregateTest) {
		t.Fatalf("normal stream error = %v", err)
	}
	if got := base.recvCount; got != 2 {
		t.Fatalf("base recv count = %d, want 2", got)
	}
}

func TestNativeUsageStream_cancellationDoesNotDuplicateEvidenceOnClose(t *testing.T) {
	base := &usageAggregateTestStream{events: []lipapi.Event{{Kind: lipapi.EventResponseStarted}}}
	stream := newNativeUsageStream(base, &NativeUsageEvidence{InputTokens: 9, TotalTokens: 9, UsagePresence: lipapi.UsagePresence{InputTokens: true, TotalTokens: true}, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Recv(ctx); err != nil {
		t.Fatalf("evidence must not be lost to caller cancellation: %v", err)
	}
	if _, err := stream.Recv(ctx); err != nil {
		t.Fatalf("evidence must not be lost to caller cancellation: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("recv after close = %v", err)
	}
}

func TestNativeUsageStream_disabledPathIsTransparent(t *testing.T) {
	base := &usageAggregateTestStream{events: []lipapi.Event{{Kind: lipapi.EventResponseFinished}}}
	stream := newNativeUsageStream(base, nil)
	event, err := stream.Recv(context.Background())
	if err != nil || event.Kind != lipapi.EventResponseFinished {
		t.Fatalf("transparent event = %#v, err = %v", event, err)
	}
	if base.recvCount != 1 {
		t.Fatalf("base recv count = %d", base.recvCount)
	}
}

func TestNativeUsageStream_firstFailureUsesLegalStartBeforeEvidence(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "error", err: errUsageAggregateTest},
		{name: "eof", err: io.EOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &usageAggregateTestStream{err: tt.err}
			stream := newNativeUsageStream(base, &NativeUsageEvidence{InputTokens: 13, TotalTokens: 13, UsagePresence: lipapi.UsagePresence{InputTokens: true, TotalTokens: true}, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative})

			start, err := stream.Recv(context.Background())
			if err != nil || start.Kind != lipapi.EventResponseStarted {
				t.Fatalf("start = %#v, err = %v", start, err)
			}
			usage, err := stream.Recv(context.Background())
			if err != nil || usage.Kind != lipapi.EventUsageDelta {
				t.Fatalf("usage = %#v, err = %v", usage, err)
			}
			if _, err := stream.Recv(context.Background()); !errors.Is(err, tt.err) {
				t.Fatalf("terminal error = %v, want %v", err, tt.err)
			}
		})
	}
}

func TestNativeUsageStream_firstEventAndZeroEventDoNotLeakPhantomOrdering(t *testing.T) {
	base := &usageAggregateTestStream{events: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "text"}}}
	stream := newNativeUsageStream(base, &NativeUsageEvidence{InputTokens: 4, UsagePresence: lipapi.UsagePresence{InputTokens: true}, Source: lipapi.UsageSourceLocalEstimator, Authority: lipapi.UsageAuthorityEstimated})
	start, err := stream.Recv(context.Background())
	if err != nil || start.Kind != lipapi.EventResponseStarted {
		t.Fatalf("start = %#v, err = %v", start, err)
	}
	message, err := stream.Recv(context.Background())
	if err != nil || message.Kind != lipapi.EventMessageStarted {
		t.Fatalf("message = %#v, err = %v", message, err)
	}
	usage, err := stream.Recv(context.Background())
	if err != nil || usage.Kind != lipapi.EventUsageDelta {
		t.Fatalf("usage = %#v, err = %v", usage, err)
	}
	content, err := stream.Recv(context.Background())
	if err != nil || content.Kind != lipapi.EventTextDelta {
		t.Fatalf("content = %#v, err = %v", content, err)
	}

	zeroBase := &usageAggregateTestStream{events: []lipapi.Event{{Kind: lipapi.EventResponseFinished}}}
	zero := newNativeUsageStream(zeroBase, &NativeUsageEvidence{})
	if _, err := zero.Recv(context.Background()); err != nil {
		t.Fatalf("zero evidence changed transparent stream: %v", err)
	}
	if zeroBase.recvCount != 1 {
		t.Fatalf("zero evidence caused wrapper read behavior: %d", zeroBase.recvCount)
	}
}

func TestValidateCompactionUsageEvidence_acceptsLegalLifecycleOrders(t *testing.T) {
	usage := lipapi.Event{
		Kind: lipapi.EventUsageDelta,
		UsageScopes: []lipapi.ScopedUsageDelta{{
			InputTokens: 41, TotalTokens: 41,
			UsagePresence: lipapi.UsagePresence{InputTokens: true, TotalTokens: true},
			Accounting: lipapi.UsageAccountingMetadata{
				Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
			},
		}},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	for _, test := range []struct {
		name   string
		events []lipapi.Event
	}{
		{
			name: "usage before message repair",
			events: []lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				usage,
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "ignored by validator test"},
				{Kind: lipapi.EventResponseFinished},
			},
		},
		{
			name: "usage after message start",
			events: []lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				usage,
				{Kind: lipapi.EventTextDelta, Delta: "ignored by validator test"},
				{Kind: lipapi.EventResponseFinished},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCompactionUsageEvidence(test.events); err != nil {
				t.Fatalf("legal lifecycle rejected: %v", err)
			}
		})
	}
}

func TestValidateCompactionUsageEvidence_rejectsLateAndDuplicateEvidence_butAcceptsEstimated(t *testing.T) {
	providerUsage := lipapi.Event{
		Kind: lipapi.EventUsageDelta,
		UsageScopes: []lipapi.ScopedUsageDelta{{
			InputTokens: 41, UsagePresence: lipapi.UsagePresence{InputTokens: true},
			Accounting: lipapi.UsageAccountingMetadata{
				Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
			},
		}},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	tests := []struct {
		name   string
		events []lipapi.Event
		want   string
	}{
		{
			name:   "late",
			events: []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}, {Kind: lipapi.EventTextDelta}, providerUsage, {Kind: lipapi.EventResponseFinished}},
			want:   "after client-visible content",
		},
		{
			name:   "duplicate",
			events: []lipapi.Event{{Kind: lipapi.EventResponseStarted}, providerUsage, providerUsage, {Kind: lipapi.EventMessageStarted}, {Kind: lipapi.EventResponseFinished}},
			want:   "exactly one",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCompactionUsageEvidence(test.events)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
	estimated := lipapi.Event{
		Kind: lipapi.EventUsageDelta,
		UsageScopes: []lipapi.ScopedUsageDelta{{
			InputTokens: 41, UsagePresence: lipapi.UsagePresence{InputTokens: true},
			Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceLocalEstimator, Authority: lipapi.UsageAuthorityEstimated},
		}},
		Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceLocalEstimator, Authority: lipapi.UsageAuthorityEstimated},
	}
	if err := validateCompactionUsageEvidence([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, estimated, {Kind: lipapi.EventMessageStarted}, {Kind: lipapi.EventResponseFinished}}); err != nil {
		t.Fatalf("estimated compaction evidence rejected: %v", err)
	}
}

func TestNativeUsageStream_closeBeforeEmissionAndNilContext(t *testing.T) {
	base := &usageAggregateTestStream{events: []lipapi.Event{{Kind: lipapi.EventResponseStarted}}}
	stream := newNativeUsageStream(base, &NativeUsageEvidence{InputTokens: 4, UsagePresence: lipapi.UsagePresence{InputTokens: true}, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative})
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("recv after close = %v", err)
	}

	stream = newNativeUsageStream(base, &NativeUsageEvidence{InputTokens: 4, UsagePresence: lipapi.UsagePresence{InputTokens: true}, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative})
	if _, err := stream.Recv(nil); !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("nil context error = %v", err)
	}
}

func TestNativeUsageStream_ConcurrentRecvPreservesSingleCursor(t *testing.T) {
	base := &usageAggregateTestStream{events: []lipapi.Event{
		{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished},
	}}
	stream := newNativeUsageStream(base, &NativeUsageEvidence{InputTokens: 4, UsagePresence: lipapi.UsagePresence{InputTokens: true}, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var events []lipapi.Event
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				ev, err := stream.Recv(context.Background())
				if errors.Is(err, io.EOF) {
					return
				}
				if err != nil {
					t.Errorf("recv: %v", err)
					return
				}
				mu.Lock()
				events = append(events, ev)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	usage := 0
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			usage++
		}
	}
	if usage != 1 {
		t.Fatalf("usage events=%d events=%#v", usage, events)
	}
}

func TestNativeUsageStream_telemetryUsesInjectedClockAndSupportsConcurrentSnapshots(t *testing.T) {
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

var errUsageAggregateTest = errors.New("usage aggregate test stream error")

type usageAggregateTestStream struct {
	events    []lipapi.Event
	err       error
	idx       int
	recvCount int
}

func (s *usageAggregateTestStream) Recv(context.Context) (lipapi.Event, error) {
	s.recvCount++
	if s.idx < len(s.events) {
		event := s.events[s.idx]
		s.idx++
		return event, nil
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
