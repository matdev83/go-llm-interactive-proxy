package runtime

// Phase 19.3 disabled/enabled overhead certification (Requirements 4.6, 18.3,
// 18.5, 18.6).
//
// The accounting-disabled executor configuration must not build or deliver a
// terminal billing leg record: no observer callback, no sink append, no
// discarded record allocation. The guarded terminal path is the only
// accounting work that would otherwise run for every call with every billing
// seam unbound.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// TestPhase193DisabledAccountingSkipsDiscardedBillingLegRecord proves that an
// executor with every accounting seam unbound (billingEnabled() == false) does
// not construct or hand off a terminal billing leg record. Before the guard,
// the attempt terminalizer built the full V2 record and passed it to the
// no-op observer/append callbacks, which allocated and discarded it.
func TestPhase193DisabledAccountingSkipsDiscardedBillingLegRecord(t *testing.T) {
	t.Parallel()

	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	var observed atomic.Int32
	var appended atomic.Int32
	session := &attemptSession{
		terminal:      newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:          b2bua.BLegRecord{ALegID: "a-193", BLegID: "b-193", Seq: 1},
		cand:          routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
		billingCallID: callID,
		now:           func() time.Time { return time.Unix(193, 0).UTC() },
		billingEnabled: func() bool {
			return false
		},
		observeBillingLeg: func(context.Context, billing.CallLegUsageRecord) {
			observed.Add(1)
		},
		appendBillingLeg: func(context.Context, billing.BillingCallID, billing.CallLegUsageRecord) {
			appended.Add(1)
		},
	}
	result := session.TerminalizeAttempt(context.Background(), IntentSuccess, attemptEvidence{
		Command: sdkterminal.CommandNormalFinish, LegOutcome: billing.LegOutcomeWinner,
	})
	if result.Result.Err != nil {
		t.Fatalf("terminalize disabled accounting: %v", result.Result.Err)
	}
	if got := observed.Load(); got != 0 {
		t.Fatalf("disabled accounting observed %d terminal billing legs, want 0", got)
	}
	if got := appended.Load(); got != 0 {
		t.Fatalf("disabled accounting appended %d terminal billing legs, want 0", got)
	}
}

// TestPhase193DisabledLocalBoundaryDrainAllocatesNothing proves the attempt
// terminal cleanup does not allocate scope/identity state when no local
// boundary accumulator exists. The accumulator is never created for ordinary
// no-accounting execution, so the drain must return before building identity.
//
//nolint:paralleltest // AllocsPerRun forbids parallel tests.
func TestPhase193DisabledLocalBoundaryDrainAllocatesNothing(t *testing.T) {
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	session := &attemptSession{
		bleg:           b2bua.BLegRecord{ALegID: "a-193", BLegID: "b-193", Seq: 1},
		billingCallID:  callID,
		billingStoreID: "store-193",
		boundaryScope: scope.PrincipalScopeView{
			PrincipalID:  scope.Known("p1"),
			SafeClaims:   map[string]string{"tier": "gold"},
			PolicyLabels: map[string]string{"region": "eu"},
		},
	}
	now := time.Unix(193, 0).UTC()
	allocs := testing.AllocsPerRun(1000, func() {
		if got := session.localBoundaryObservations(now, false); got != nil {
			t.Fatalf("no-accumulator observations = %d, want nil", len(got))
		}
	})
	if allocs != 0 {
		t.Fatalf("no-accumulator boundary drain allocs = %v, want 0", allocs)
	}
}

// phase193LargeResponseEvents builds a canonical stream whose text deltas total
// at least totalBytes with 64 KiB chunks.
func phase193LargeResponseEvents(totalBytes int) []lipapi.Event {
	const chunk = 64 << 10
	pad := strings.Repeat("x", chunk)
	events := make([]lipapi.Event, 0, 3+totalBytes/chunk)
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseStarted}, lipapi.Event{Kind: lipapi.EventMessageStarted})
	for written := 0; written < totalBytes; written += chunk {
		events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: pad})
	}
	return append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})
}

func phase193LargeExecutor(tb testing.TB, enabled bool, capture *accountingBaselineCapture, events []lipapi.Event) *Executor {
	tb.Helper()
	ex := accountingBaselineExecutor(tb, enabled, capture)
	ex.Backends["accounting-backend"] = execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			capture.backendOpens.Add(1)
			return lipapi.NewFixedEventStream(events), nil
		},
	}
	return ex
}

// BenchmarkPhase193MultiMiBCanonicalAccounting drains a multi-MiB canonical
// response with accounting disabled and enabled. The enabled capture cost is
// the constant terminal record plus one observer/append callback per call; it
// must not scale with response size, and the disabled path must perform zero
// monetary callbacks.
func BenchmarkPhase193MultiMiBCanonicalAccounting(b *testing.B) {
	for _, total := range []int{1 << 20, 5 << 20} {
		name := "1MiB"
		if total == 5<<20 {
			name = "5MiB"
		}
		for _, tc := range []struct {
			name    string
			enabled bool
		}{
			{name: "Disabled", enabled: false},
			{name: "Enabled", enabled: true},
		} {
			b.Run(name+"/"+tc.name, func(b *testing.B) {
				capture := newAccountingBaselineCapture(false)
				ex := phase193LargeExecutor(b, tc.enabled, capture, phase193LargeResponseEvents(total))
				ctx := b.Context()
				b.SetBytes(int64(total))
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					stream, err := ex.Execute(ctx, accountingBaselineCall())
					if err != nil {
						b.Fatalf("execute: %v", err)
					}
					for {
						_, rerr := stream.Recv(ctx)
						if rerr != nil {
							break
						}
					}
					_ = stream.Close()
				}
				b.StopTimer()

				iterations := int64(b.N)
				if got := capture.backendOpens.Load(); got != iterations {
					b.Fatalf("backend opens = %d, want %d", got, iterations)
				}
				wantTerminal := int64(0)
				if tc.enabled {
					wantTerminal = iterations
				}
				if got := capture.observedLegs.Load(); got != wantTerminal {
					b.Fatalf("observed terminal legs = %d, want %d", got, wantTerminal)
				}
				if got := capture.appendedLegs.Load(); got != wantTerminal {
					b.Fatalf("appended terminal B-legs = %d, want %d", got, wantTerminal)
				}
				if got := capture.appendedCalls.Load(); got != wantTerminal {
					b.Fatalf("appended terminal calls = %d, want %d", got, wantTerminal)
				}
				if iterations > 0 {
					b.ReportMetric(float64(capture.observedLegs.Load())/float64(iterations), "terminal-observed-legs/op")
					b.ReportMetric(float64(capture.appendedLegs.Load())/float64(iterations), "terminal-leg-appends/op")
					b.ReportMetric(float64(capture.appendedCalls.Load())/float64(iterations), "terminal-call-appends/op")
				}
			})
		}
	}
}
