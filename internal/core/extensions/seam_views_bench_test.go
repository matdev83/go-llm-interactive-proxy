package extensions_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// Package-level benchmark sinks to prevent compiler dead-code elimination.
var (
	benchSinkCompletionGates      []completion.Gate
	benchSinkTrafficPortBundle    sdktraffic.PortBundle
	benchSinkTrafficObserver      sdktraffic.Observer
	benchSinkTrafficRedactors     []sdktraffic.Redactor
	benchSinkSecretGuardPlane     extensions.SecretGuardPlane
	benchSinkCompactionObservers  []compaction.Observer
	benchSinkCompactionPreservers []compaction.Preserver
	benchSinkTerminalProvider     terminaldecision.Provider
)

// Benchmark stub types for seam-view benchmark fixtures.

type benchGate struct {
	id string
}

func (g benchGate) ID() string                      { return g.id }
func (benchGate) Order() int                        { return 0 }
func (benchGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (benchGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type benchTrafficObs struct{}

func (benchTrafficObs) OnObservation(context.Context, sdktraffic.Observation) error { return nil }

type benchRawCapture struct{}

func (benchRawCapture) WriteRaw(context.Context, sdktraffic.Leg, sdktraffic.CaptureMeta, []byte) error {
	return nil
}

type benchTrafficRed struct {
	id string
}

func (r benchTrafficRed) ID() string { return r.id }
func (benchTrafficRed) Redact(context.Context, sdktraffic.Leg, sdktraffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type benchSecretGuard struct {
	id string
}

func (g benchSecretGuard) ID() string                         { return g.id }
func (benchSecretGuard) Order() int                           { return 0 }
func (benchSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailOpen }
func (benchSecretGuard) Evaluate(context.Context, *lipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type benchCompactionObs struct{}

func (benchCompactionObs) OnCompaction(context.Context, compaction.Event) error { return nil }

type benchCompactionPreserver struct {
	id string
}

func (p benchCompactionPreserver) ID() string { return p.id }
func (benchCompactionPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (benchCompactionPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (benchCompactionPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type benchTerminalProvider struct {
	id string
}

func (p benchTerminalProvider) ID() string { return p.id }
func (benchTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

// newBenchPopulatedSnapshot builds a populated snapshot containing non-empty fixtures across
// all five seam-view families.
func newBenchPopulatedSnapshot() *extensions.RequestRuntimeSnapshot {
	bus := hooks.New(hooks.Config{})
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{
			benchGate{id: "gate-1"},
			benchGate{id: "gate-2"},
		},
		TrafficObserver: benchTrafficObs{},
		RawCapture:      benchRawCapture{},
		TrafficRedactors: []sdktraffic.Redactor{
			benchTrafficRed{id: "red-1"},
			benchTrafficRed{id: "red-2"},
		},
		SecretGuardPlane: extensions.SecretGuardPlane{
			Guards: []secretguard.Guard{
				benchSecretGuard{id: "sg-1"},
				benchSecretGuard{id: "sg-2"},
			},
			AuditFailurePolicy: secretguard.AuditFailClosed,
			AccessMode:         "enforcing",
			ConfigVersion:      "v1",
		},
		CompactionObservers: []compaction.Observer{
			benchCompactionObs{},
		},
		CompactionPreservers: []compaction.Preserver{
			benchCompactionPreserver{id: "cp-1"},
		},
		TerminalDecisionProvider: benchTerminalProvider{id: "term-1"},
		Generation:               1,
	})
}

// newBenchEmptySnapshot builds an empty snapshot with default options.
func newBenchEmptySnapshot() *extensions.RequestRuntimeSnapshot {
	bus := hooks.New(hooks.Config{})
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})
}

// -----------------------------------------------------------------------------
// Family 1: Completion Gates
// Accessors:
// - RequestRuntimeSnapshot.CompletionGates(): DEFENSIVE CLONE (slices.Clone, 1 alloc/op when populated)
// - CompletionGatesFromContext(): DEFENSIVE CLONE via CompletionGates() when populated (1 alloc/op);
//   returns shared empty slice when empty/nil (0 alloc/op).
// -----------------------------------------------------------------------------

// BenchmarkCompletionGates_Populated measures CompletionGates on a populated snapshot.
// Defensive clone behavior: allocates 1 alloc/op via slices.Clone.
func BenchmarkCompletionGates_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = snap.CompletionGates()
	}
}

// BenchmarkCompletionGates_Empty measures CompletionGates on an empty snapshot.
// Defensive clone behavior: returns nil (0 alloc/op).
func BenchmarkCompletionGates_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = snap.CompletionGates()
	}
}

// BenchmarkCompletionGates_NilSnapshot measures CompletionGates on a nil snapshot.
// Defensive clone behavior: returns nil (0 alloc/op).
func BenchmarkCompletionGates_NilSnapshot(b *testing.B) {
	var snap *extensions.RequestRuntimeSnapshot
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = snap.CompletionGates()
	}
}

// BenchmarkCompletionGatesFromContext_Populated measures CompletionGatesFromContext with context snapshot.
// Defensive clone behavior: delegates to CompletionGates() (1 alloc/op).
func BenchmarkCompletionGatesFromContext_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	ctx := extensions.WithRequestRuntimeSnapshot(b.Context(), snap)
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = extensions.CompletionGatesFromContext(ctx, nil)
	}
}

// BenchmarkCompletionGatesFromContext_Empty measures CompletionGatesFromContext with empty context snapshot.
// Returns shared emptyCompletionGates (0 alloc/op).
func BenchmarkCompletionGatesFromContext_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	ctx := extensions.WithRequestRuntimeSnapshot(b.Context(), snap)
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = extensions.CompletionGatesFromContext(ctx, nil)
	}
}

// BenchmarkCompletionGatesFromContext_NilContextFallback measures fallback path when context has no snapshot.
// Defensive clone behavior: delegates to fallback.CompletionGates() (1 alloc/op).
func BenchmarkCompletionGatesFromContext_NilContextFallback(b *testing.B) {
	ctx := b.Context()
	fallback := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = extensions.CompletionGatesFromContext(ctx, fallback)
	}
}

// BenchmarkCompletionGatesFromContext_nilFallback_empty measures the hot path where no snapshot is
// on context and fallback is nil — result is the shared empty slice (zero allocations per call).
func BenchmarkCompletionGatesFromContext_nilFallback_empty(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = extensions.CompletionGatesFromContext(ctx, nil)
	}
}

// BenchmarkCompletionGatesFromContext_fallbackNilGates_empty uses a fallback view whose
// CompletionGates returns nil; hits the same shared empty slice as nil fallback (0 alloc/op).
func BenchmarkCompletionGatesFromContext_fallbackNilGates_empty(b *testing.B) {
	ctx := b.Context()
	fallback := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = extensions.CompletionGatesFromContext(ctx, fallback)
	}
}

// BenchmarkCompletionGatesFromContext_withGates measures fallback with gates configured.
func BenchmarkCompletionGatesFromContext_withGates(b *testing.B) {
	ctx := b.Context()
	fallback := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{benchGate{id: "g"}},
	})
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompletionGates = extensions.CompletionGatesFromContext(ctx, fallback)
	}
}

// -----------------------------------------------------------------------------
// Family 2: Traffic Seam
// Accessors:
// - RequestRuntimeSnapshot.TrafficPortBundle(): DEFENSIVE CLONE via TrafficRedactors() (1 alloc/op when redactors populated; 0 when empty/nil)
// - RequestRuntimeSnapshot.TrafficObserver(): DIRECT READ (0 alloc/op)
// - RequestRuntimeSnapshot.TrafficRedactors(): DEFENSIVE CLONE (slices.Clone, 1 alloc/op when populated)
// -----------------------------------------------------------------------------

// BenchmarkTrafficPortBundle_Populated measures TrafficPortBundle on a populated snapshot.
// Defensive clone behavior: calls TrafficRedactors() which clones the redactor slice (1 alloc/op).
func BenchmarkTrafficPortBundle_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTrafficPortBundle = snap.TrafficPortBundle()
	}
}

// BenchmarkTrafficPortBundle_Empty measures TrafficPortBundle on an empty snapshot.
// No redactors to clone (0 alloc/op).
func BenchmarkTrafficPortBundle_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTrafficPortBundle = snap.TrafficPortBundle()
	}
}

// BenchmarkTrafficPortBundle_NilSnapshot measures TrafficPortBundle on a nil snapshot.
// Returns empty struct (0 alloc/op).
func BenchmarkTrafficPortBundle_NilSnapshot(b *testing.B) {
	var snap *extensions.RequestRuntimeSnapshot
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTrafficPortBundle = snap.TrafficPortBundle()
	}
}

// BenchmarkTrafficObserver_Populated measures TrafficObserver on a populated snapshot.
// Direct read: returns internal interface value directly (0 alloc/op).
func BenchmarkTrafficObserver_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTrafficObserver = snap.TrafficObserver()
	}
}

// BenchmarkTrafficRedactors_Populated measures TrafficRedactors on a populated snapshot.
// Defensive clone behavior: allocates 1 alloc/op via slices.Clone.
func BenchmarkTrafficRedactors_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTrafficRedactors = snap.TrafficRedactors()
	}
}

// BenchmarkTrafficRedactors_Empty measures TrafficRedactors on an empty snapshot.
// Defensive clone behavior: returns nil (0 alloc/op).
func BenchmarkTrafficRedactors_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTrafficRedactors = snap.TrafficRedactors()
	}
}

// BenchmarkTrafficRedactors_NilSnapshot measures TrafficRedactors on a nil snapshot.
// Defensive clone behavior: returns nil (0 alloc/op).
func BenchmarkTrafficRedactors_NilSnapshot(b *testing.B) {
	var snap *extensions.RequestRuntimeSnapshot
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTrafficRedactors = snap.TrafficRedactors()
	}
}

// -----------------------------------------------------------------------------
// Family 3: Secret Guard Plane
// Accessors:
// - RequestRuntimeSnapshot.SecretGuardPlane(): DEFENSIVE CLONE (clones Guards slice, 1 alloc/op when populated)
// - RequestRuntimeSnapshot.SecretGuardExecutionPlane(): DIRECT READ (no cloning, 0 alloc/op)
// -----------------------------------------------------------------------------

// BenchmarkSecretGuardPlane_Populated measures SecretGuardPlane on a populated snapshot.
// Defensive clone behavior: allocates 1 alloc/op by cloning the Guards slice.
func BenchmarkSecretGuardPlane_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkSecretGuardPlane = snap.SecretGuardPlane()
	}
}

// BenchmarkSecretGuardPlane_Empty measures SecretGuardPlane on an empty snapshot.
// No guards to clone (0 alloc/op).
func BenchmarkSecretGuardPlane_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkSecretGuardPlane = snap.SecretGuardPlane()
	}
}

// BenchmarkSecretGuardPlane_NilSnapshot measures SecretGuardPlane on a nil snapshot.
// Returns empty SecretGuardPlane struct (0 alloc/op).
func BenchmarkSecretGuardPlane_NilSnapshot(b *testing.B) {
	var snap *extensions.RequestRuntimeSnapshot
	b.ReportAllocs()
	for b.Loop() {
		benchSinkSecretGuardPlane = snap.SecretGuardPlane()
	}
}

// BenchmarkSecretGuardExecutionPlane_Populated measures SecretGuardExecutionPlane on a populated snapshot.
// Execution hot path: returns the internal struct without cloning Guards (0 alloc/op).
func BenchmarkSecretGuardExecutionPlane_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkSecretGuardPlane = snap.SecretGuardExecutionPlane()
	}
}

// BenchmarkSecretGuardExecutionPlane_Empty measures SecretGuardExecutionPlane on an empty snapshot.
// Direct read (0 alloc/op).
func BenchmarkSecretGuardExecutionPlane_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkSecretGuardPlane = snap.SecretGuardExecutionPlane()
	}
}

// BenchmarkSecretGuardExecutionPlane_NilSnapshot measures SecretGuardExecutionPlane on a nil snapshot.
// Direct read returns empty SecretGuardPlane struct (0 alloc/op).
func BenchmarkSecretGuardExecutionPlane_NilSnapshot(b *testing.B) {
	var snap *extensions.RequestRuntimeSnapshot
	b.ReportAllocs()
	for b.Loop() {
		benchSinkSecretGuardPlane = snap.SecretGuardExecutionPlane()
	}
}

// -----------------------------------------------------------------------------
// Family 4: Compaction Observers & Preservers
// Accessors:
// - RequestRuntimeSnapshot.CompactionObservers(): DEFENSIVE CLONE (slices.Clone, 1 alloc/op when populated)
// - RequestRuntimeSnapshot.CompactionPreservers(): DEFENSIVE CLONE (slices.Clone, 1 alloc/op when populated)
// -----------------------------------------------------------------------------

// BenchmarkCompactionObservers_Populated measures CompactionObservers on a populated snapshot.
// Defensive clone behavior: allocates 1 alloc/op via slices.Clone.
func BenchmarkCompactionObservers_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompactionObservers = snap.CompactionObservers()
	}
}

// BenchmarkCompactionObservers_Empty measures CompactionObservers on an empty snapshot.
// Defensive clone behavior: returns nil (0 alloc/op).
func BenchmarkCompactionObservers_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompactionObservers = snap.CompactionObservers()
	}
}

// BenchmarkCompactionObservers_NilSnapshot measures CompactionObservers on a nil snapshot.
// Defensive clone behavior: returns nil (0 alloc/op).
func BenchmarkCompactionObservers_NilSnapshot(b *testing.B) {
	var snap *extensions.RequestRuntimeSnapshot
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompactionObservers = snap.CompactionObservers()
	}
}

// BenchmarkCompactionPreservers_Populated measures CompactionPreservers on a populated snapshot.
// Defensive clone behavior: allocates 1 alloc/op via slices.Clone.
func BenchmarkCompactionPreservers_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompactionPreservers = snap.CompactionPreservers()
	}
}

// BenchmarkCompactionPreservers_Empty measures CompactionPreservers on an empty snapshot.
// Defensive clone behavior: returns nil (0 alloc/op).
func BenchmarkCompactionPreservers_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompactionPreservers = snap.CompactionPreservers()
	}
}

// BenchmarkCompactionPreservers_NilSnapshot measures CompactionPreservers on a nil snapshot.
// Defensive clone behavior: returns nil (0 alloc/op).
func BenchmarkCompactionPreservers_NilSnapshot(b *testing.B) {
	var snap *extensions.RequestRuntimeSnapshot
	b.ReportAllocs()
	for b.Loop() {
		benchSinkCompactionPreservers = snap.CompactionPreservers()
	}
}

// -----------------------------------------------------------------------------
// Family 5: Terminal Decision Provider
// Accessors:
// - RequestRuntimeSnapshot.TerminalDecisionProvider(): DIRECT READ (0 alloc/op)
// -----------------------------------------------------------------------------

// BenchmarkTerminalDecisionProvider_Populated measures TerminalDecisionProvider on a populated snapshot.
// Direct read: returns the internal interface value directly (0 alloc/op).
func BenchmarkTerminalDecisionProvider_Populated(b *testing.B) {
	snap := newBenchPopulatedSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTerminalProvider = snap.TerminalDecisionProvider()
	}
}

// BenchmarkTerminalDecisionProvider_Empty measures TerminalDecisionProvider on an empty snapshot.
// Direct read: returns nil (0 alloc/op).
func BenchmarkTerminalDecisionProvider_Empty(b *testing.B) {
	snap := newBenchEmptySnapshot()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTerminalProvider = snap.TerminalDecisionProvider()
	}
}

// BenchmarkTerminalDecisionProvider_NilSnapshot measures TerminalDecisionProvider on a nil snapshot.
// Direct read: returns nil (0 alloc/op).
func BenchmarkTerminalDecisionProvider_NilSnapshot(b *testing.B) {
	var snap *extensions.RequestRuntimeSnapshot
	b.ReportAllocs()
	for b.Loop() {
		benchSinkTerminalProvider = snap.TerminalDecisionProvider()
	}
}
