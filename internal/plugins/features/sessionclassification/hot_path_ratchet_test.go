package sessionclassification_test

// Task 9.3 feature-level hot-path allocation and store-call ratchets.
//
// Requirements 10.1, 10.2, 10.6, 10.7 and 5.1, at the classifier seam itself.
//
// This file deliberately measures the classifier over its own bounded evidence
// only, because that is the layer where 10.2 and 5.1 are claims about: local
// evaluation must inspect the current turn's metadata, tool-name category bits,
// workspace markers and cached control state, and nothing else.
//
// Every allocation ratchet is exact rather than a ceiling where it can be. The
// bounds are integers measured with testing.AllocsPerRun, and each is paired
// with an in-file control showing the measured path DOES allocate when the work
// is really done, so a zero bound cannot be satisfied by measuring nothing.

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdkclassification "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// featureRatchetMaxMarkerFanout is the widest workspace-marker list the policy
// is allowed to inspect (design "Memory bounds"/config bounds).
const featureRatchetMaxMarkerFanout = sessionclassification.MaxWorkspaceMarkers

// mustClassifierTB and mustRemoteClassifierTB are the testing.TB forms of the
// package's existing classifier fixtures. Benchmarks need the same construction
// path as tests, and the T-only helpers cannot accept a *testing.B.
func mustClassifierTB(
	tb testing.TB, cfg sessionclassification.Config, store *fakeStore, now func() time.Time,
) *sessionclassification.Classifier {
	tb.Helper()
	return mustRemoteClassifierTB(tb, cfg, store, now, nil)
}

func mustRemoteClassifierTB(
	tb testing.TB,
	cfg sessionclassification.Config,
	store *fakeStore,
	now func() time.Time,
	decider sessionclassification.RemoteDecider,
) *sessionclassification.Classifier {
	tb.Helper()
	classifier, err := sessionclassification.NewClassifier(cfg, sessionclassification.ClassifierDeps{
		State:  &fakeAuthority{store: store},
		Remote: decider,
		Now:    now,
	})
	if err != nil {
		tb.Fatalf("NewClassifier: %v", err)
	}
	return classifier
}

// featureRatchetUnknownInput is a still-unknown turn carrying the widest bounded
// metadata the V1 contract permits: a generic client identity, one read/search
// category bit, and a full window of unrecognized workspace markers. It is the
// worst case for requirement 10.2, so a ratchet on it bounds every narrower
// turn as well.
func featureRatchetUnknownInput(sessionID string) sdkclassification.Input {
	// Mixed case on purpose: the production evaluation normalizes each inspected
	// marker, so the fixtures must carry values whose normalization actually
	// allocates. Otherwise the allocation ratchets would measure an
	// already-normalized input and could not observe unbounded marker work.
	markers := make([]string, featureRatchetMaxMarkerFanout)
	for i := range markers {
		markers[i] = fmt.Sprintf("Unrecognized-Marker-%02d", i)
	}
	return sdkclassification.Input{
		TraceID: "trace-" + sessionID,
		Session: session.SessionView{
			AuthoritativeSessionID: sessionID,
			ClientSessionHint:      sessionID,
			ALegID:                 "aleg-" + sessionID,
			TurnID:                 "turn-" + sessionID,
			WorkspaceID:            "workspace-ratchet",
		},
		Workspace: workspace.WorkspaceView{ID: "workspace-ratchet", Markers: markers},
		Evidence: sdkclassification.Evidence{
			Operation:       lipapi.OperationOpenAIResponses,
			ClientUserAgent: "OpenAI/JS 4.0.0",
			// read/search plus ONE local mutation category: the one shape that
			// makes workspace-marker inspection load-bearing, so the worst-case
			// fixture really does read the bounded marker window. No recognized
			// marker is present, so the turn stays unknown.
			ToolCategories: sdkclassification.ToolCategoryFileRead | sdkclassification.ToolCategoryFileEdit,
		},
	}
}

// TestUnknownLocalEvaluationInspectsOnlyBoundedCurrentTurnMetadata pins 10.2
// and 5.1 by making the bounded window load-bearing from BOTH sides.
//
// In-bounds: a recognized project marker inside the policy's inspection window,
// combined with the read/search plus one local-mutation cluster, promotes.
// Out-of-bounds: the same marker placed one position past the window does not,
// so evaluation genuinely reads a bounded prefix rather than the whole list.
//
// Control: the same evaluation with a transcript-bearing input is byte-identical,
// which is what makes "no transcript scan" a measurement rather than a claim.
func TestUnknownLocalEvaluationInspectsOnlyBoundedCurrentTurnMetadata(t *testing.T) {
	cfg := sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}

	// The full bounded cluster: read/search plus edit plus OS command is decisive
	// on its own, so no marker is consulted at all.
	distinct := toolEvidenceInput("read_file", "write_file", "execute_command")
	if got := sessionclassification.EvaluateLocal(cfg, distinct); !got.Promotes {
		t.Fatalf("distinct same-turn cluster = %+v, want a promotion", got)
	}

	// read/search plus ONE local mutation category needs a project marker.
	weak := toolEvidenceInput("read_file", "write_file")

	inWindow := weak
	inWindow.Workspace.Markers = make([]string, featureRatchetMaxMarkerFanout)
	for i := range inWindow.Workspace.Markers {
		inWindow.Workspace.Markers[i] = fmt.Sprintf("unrecognized-marker-%02d", i)
	}
	inWindow.Workspace.Markers[featureRatchetMaxMarkerFanout-1] = "go.mod"
	if got := sessionclassification.EvaluateLocal(cfg, inWindow); !got.Promotes {
		t.Fatalf("marker at the last in-bounds position = %+v, want a promotion: the bounded window must be read", got)
	}

	outOfWindow := weak
	outOfWindow.Workspace.Markers = make([]string, featureRatchetMaxMarkerFanout+1)
	for i := range outOfWindow.Workspace.Markers {
		outOfWindow.Workspace.Markers[i] = fmt.Sprintf("unrecognized-marker-%02d", i)
	}
	outOfWindow.Workspace.Markers[featureRatchetMaxMarkerFanout] = "go.mod"
	if got := sessionclassification.EvaluateLocal(cfg, outOfWindow); got.Promotes {
		t.Fatalf("marker past the bounded window = %+v, want unknown: evaluation must not read unbounded marker data", got)
	}

	// The only free-form bounded string the classifier receives is the trace
	// identifier. Growing it cannot change the decision, which is the feature-level
	// half of "no transcript scan"; the runtime-stage half (content that a scan
	// WOULD read changing the evidence) lives in
	// internal/core/runtime's size-invariance ratchet.
	wideTrace := inWindow
	wideTrace.TraceID = strings.Repeat("t", 4096)
	if got := sessionclassification.EvaluateLocal(cfg, wideTrace); got != sessionclassification.EvaluateLocal(cfg, inWindow) {
		t.Fatalf("a %d-byte trace identifier changed the bounded decision: %+v vs %+v",
			len(wideTrace.TraceID), got, sessionclassification.EvaluateLocal(cfg, inWindow))
	}
}

// featureRatchetMaxEvaluationAllocs bounds the allocations one local evaluation
// may perform.
//
// The cost has two parts, both bounded. The identity branch normalizes the
// accepted client identity up to twice: once in the shared identity catalog and
// once in the configured exclusion list, each bounded by the canonical identity
// acceptance length. The workspace branch normalizes each marker it inspects,
// bounded to MaxWorkspaceMarkers by policy. So the ceiling is derived from those
// published bounds rather than chosen freely, which means removing the marker
// window must exceed it.
const featureRatchetMaxEvaluationAllocs = sessionclassification.MaxWorkspaceMarkers + 4

// TestUnknownLocalEvaluationAllocationsAreBoundedAndNonVacuous is the 10.2 / 5.1
// allocation ratchet.
//
// The bound is an integer measured with AllocsPerRun, and the control is inside
// the same test: a genuinely allocating measurement over the SAME input and
// policy must exceed the ceiling, so the ceiling cannot be satisfied by an
// instrument that observes nothing.
func TestUnknownLocalEvaluationAllocationsAreBoundedAndNonVacuous(t *testing.T) {
	cfg := sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}
	// The ceiling is measured against the UNBOUNDED fixture: a turn carrying far
	// more markers than the policy window admits. A correct bounded evaluation
	// reads only its window and stays at the ceiling; an unbounded scan reads
	// every marker and exceeds it. Measuring only the in-bounds fixture would let
	// a removed marker window pass unnoticed.
	input := featureRatchetUnknownInput("alloc-9-3")
	input.Workspace.Markers = unboundedRatchetMarkers(8 * featureRatchetMaxMarkerFanout)
	if got := sessionclassification.EvaluateLocal(cfg, input); got != (sessionclassification.LocalDecision{}) {
		t.Fatalf("fixture is not a still-unknown worst case: %+v", got)
	}

	allocs := testing.AllocsPerRun(256, func() {
		decisionSink = sessionclassification.EvaluateLocal(cfg, input)
	})
	if allocs > featureRatchetMaxEvaluationAllocs {
		t.Fatalf("bounded unknown evaluation allocated %.2f objects per evaluation, above the %d ceiling (requirements 5.1, 10.2)",
			allocs, featureRatchetMaxEvaluationAllocs)
	}
	t.Logf("bounded unknown evaluation: %.2f allocations per evaluation (ceiling %d) over %d workspace markers",
		allocs, featureRatchetMaxEvaluationAllocs, len(input.Workspace.Markers))

	// Control: work that scales with the turn's metadata must move the very same
	// instrument well past the ceiling. Normalizing every workspace marker is
	// precisely what an unbounded marker scan would do, so this control is the
	// shape of defect the ceiling exists to catch.
	allocatingControl := testing.AllocsPerRun(64, func() {
		for _, marker := range input.Workspace.Markers {
			markerSink = strings.ToLower(marker)
		}
	})
	if allocatingControl <= featureRatchetMaxEvaluationAllocs {
		t.Fatalf("the allocating control reported %.2f allocations over %d markers, at or below the %d ceiling; AllocsPerRun cannot observe work on this path",
			allocatingControl, len(input.Workspace.Markers), featureRatchetMaxEvaluationAllocs)
	}
	t.Logf("allocating control: %.2f allocations over the same %d markers",
		allocatingControl, len(input.Workspace.Markers))
}

// unboundedRatchetMarkers builds a marker list wider than the policy window,
// which is the shape an unbounded scan would read.
func unboundedRatchetMarkers(count int) []string {
	markers := make([]string, count)
	for i := range markers {
		markers[i] = fmt.Sprintf("Unrecognized-Marker-%08d", i)
	}
	return markers
}

// decisionSink and markerSink keep the measured closures from being optimized
// away and are package-level so they can never be stack-allocated away.
var (
	decisionSink sessionclassification.LocalDecision
	markerSink   string
)

// TestUnknownLocalEvaluationScalesWithNothing pins 10.2's boundedness from the
// width dimension: growing the turn's metadata beyond the policy's fixed window
// must not change the cost of the evaluation.
//
// This is the ratchet that a transcript scan would break. A scan over N messages
// or N markers would make the wide fixture measurably more expensive than the
// narrow one; a bounded prefix read makes them identical.
func TestUnknownLocalEvaluationScalesWithNothing(t *testing.T) {
	cfg := sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}
	narrow := featureRatchetUnknownInput("narrow-9-3")
	wide := narrow
	wide.Workspace.Markers = unboundedRatchetMarkers(8 * featureRatchetMaxMarkerFanout)

	narrowAllocs := testing.AllocsPerRun(256, func() {
		decisionSink = sessionclassification.EvaluateLocal(cfg, narrow)
	})
	wideAllocs := testing.AllocsPerRun(256, func() {
		decisionSink = sessionclassification.EvaluateLocal(cfg, wide)
	})
	if wideAllocs > narrowAllocs {
		t.Fatalf("evaluation cost grew with marker width: %.2f allocs over %d markers vs %.2f over %d; local evaluation must inspect a bounded prefix only (requirement 10.2)",
			wideAllocs, len(wide.Workspace.Markers), narrowAllocs, len(narrow.Workspace.Markers))
	}
	t.Logf("bounded evaluation: %.2f allocations over %d markers, %.2f over %d markers",
		narrowAllocs, len(narrow.Workspace.Markers), wideAllocs, len(wide.Workspace.Markers))

	// Control: a width-dependent measurement over the SAME two marker lists must
	// separate them, proving this comparison detects unbounded marker work. It
	// normalizes every marker, which is exactly what a scan past the policy's
	// window would cost.
	widthDependentNarrow := testing.AllocsPerRun(64, func() {
		for _, marker := range narrow.Workspace.Markers {
			markerSink = strings.ToLower(marker)
		}
	})
	widthDependentWide := testing.AllocsPerRun(64, func() {
		for _, marker := range wide.Workspace.Markers {
			markerSink = strings.ToLower(marker)
		}
	})
	if widthDependentWide <= widthDependentNarrow {
		t.Fatalf("the width-dependence control did not separate %d markers (%.2f) from %d markers (%.2f); this ratchet cannot detect unbounded marker work",
			len(narrow.Workspace.Markers), widthDependentNarrow, len(wide.Workspace.Markers), widthDependentWide)
	}
	t.Logf("width-dependence control: %.2f allocations over %d markers, %.2f over %d markers",
		widthDependentNarrow, len(narrow.Workspace.Markers), widthDependentWide, len(wide.Workspace.Markers))
}

// TestUnknownTurnMakesOneStoreReadAndNoWrites is the 10.6 store-call ratchet at
// the classifier seam, and it reuses the package's existing counting store rather
// than a new instrument, so the census it reads is the one the rest of the suite
// already trusts.
func TestUnknownTurnMakesOneStoreReadAndNoWrites(t *testing.T) {
	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())

	const turns = 16
	for turn := range turns {
		got, err := classifier.Classify(context.Background(), featureRatchetUnknownInput(fmt.Sprintf("store-ratchet-%02d", turn)))
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if got != (session.Classification{}) {
			t.Fatalf("turn %d classification = %+v, want unknown", turn, got)
		}
	}
	loads, promotes := store.counts()
	if loads != turns {
		t.Fatalf("unknown turns made %d store reads, want exactly one indexed miss each (%d)", loads, turns)
	}
	if promotes != 0 {
		t.Fatalf("unknown turns made %d durable writes, want zero (requirement 10.6)", promotes)
	}
	if claims, _, _ := store.leaseCounts(); claims != 0 {
		t.Fatalf("a heuristic generation claimed %d remote leases, want zero", claims)
	}

	// Control: the same census on a promoting turn reports a write, so a zero
	// write assertion built on it can fail.
	promoting := newFakeStore()
	promotingClassifier := mustClassifier(t,
		sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, promoting, fixedNow())
	decisive := featureRatchetUnknownInput("store-ratchet-promote")
	decisive.Evidence.ClientUserAgent = "codex_cli_rs/1.2.3"
	if _, err := promotingClassifier.Classify(context.Background(), decisive); err != nil {
		t.Fatalf("promoting turn: %v", err)
	}
	promotingLoads, promotingWrites := promoting.counts()
	if promotingLoads != 1 || promotingWrites != 1 {
		t.Fatalf("census control on a promoting turn = (%d reads, %d writes), want (1, 1): a zero-write assertion on this census cannot fail", promotingLoads, promotingWrites)
	}
}

// TestWarmPositiveSessionMakesNoStoreAndNoRemoteWork is the 10.1 ratchet when the
// positive arrives through the already-projected session view rather than
// through a process cache: the classifier must short-circuit before any state
// resolution, store call, or remote call.
//
// The control drives the identical classifier with a still-unknown view and
// requires the store read to register, so the zero read is discriminating.
func TestWarmPositiveSessionMakesNoStoreAndNoRemoteWork(t *testing.T) {
	store := newFakeStore()
	decider := &countingDecider{}
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode: sessionclassification.ModeHybrid, Remote: validTestRemoteConfig(),
	}, store, fixedNow(), decider)

	positive := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.codex",
		Revision:   1,
	}
	warm := featureRatchetUnknownInput("warm-9-3")
	warm.Session.Classification = positive

	const turns = 64
	for turn := range turns {
		got, err := classifier.Classify(context.Background(), warm)
		if err != nil {
			t.Fatalf("warm turn %d: %v", turn, err)
		}
		if got != positive {
			t.Fatalf("warm turn %d classification = %+v, want the established positive %+v", turn, got, positive)
		}
	}
	loads, promotes := store.counts()
	if loads != 0 || promotes != 0 {
		t.Fatalf("a warm positive session made %d store reads and %d writes over %d turns, want zero of each (requirement 10.1)", loads, promotes, turns)
	}
	if claims, completions, _ := store.leaseCounts(); claims != 0 || completions != 0 {
		t.Fatalf("a warm positive session claimed %d leases and completed %d, want zero", claims, completions)
	}
	if got := decider.callCount(); got != 0 {
		t.Fatalf("a warm positive session made %d remote calls, want zero (requirement 10.1)", got)
	}

	// Control: the same classifier over the same session ID with an unknown view
	// registers the store read and reaches the remote phase, so the zero census
	// above cannot be satisfied by an instrument that never fires.
	cold := featureRatchetUnknownInput("warm-9-3")
	if _, err := classifier.Classify(context.Background(), cold); err != nil {
		t.Fatalf("cold turn: %v", err)
	}
	coldLoads, coldPromotes := store.counts()
	if coldLoads != 1 {
		t.Fatalf("census control: the still-unknown turn made %d store reads, want 1", coldLoads)
	}
	if coldPromotes != 0 {
		t.Fatalf("census control: the still-unknown turn made %d writes, want 0", coldPromotes)
	}
	if got := decider.callCount(); got != 1 {
		t.Fatalf("census control: the still-unknown turn made %d remote calls, want exactly 1", got)
	}
}

// TestClassificationCreatesNoGoroutinePerSession is the behavioural 10.7 ratchet
// for the feature package, including the one bounded timer the retry backoff
// owns.
//
// The control leaks exactly one goroutine and requires the same gauge to move.
func TestClassificationCreatesNoGoroutinePerSession(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	remote := *validTestRemoteConfig()
	// Two attempts with a real backoff exercise waitRemoteBackoff, the only place
	// this feature creates a timer.
	remote.MaxAttemptsPerSession = 2
	remote.RetryBackoff = time.Millisecond
	decider := &countingDecider{fallback: scriptedAnswer{err: boundedFailure{
		outcome: sessionclassification.RemoteServerError, cause: context.Canceled,
	}}}
	classifier := mustRemoteClassifier(t, sessionclassification.Config{
		Mode: sessionclassification.ModeHybrid, Remote: &remote,
	}, store, realNow(), decider)

	// Warm the process up so one-off runtime allocations cannot be read as a
	// per-session worker.
	if _, err := classifier.Classify(ctx, featureRatchetUnknownInput("goroutine-warmup")); err != nil {
		t.Fatalf("warm-up turn: %v", err)
	}
	before := runtime.NumGoroutine()
	const sessions = 120
	for index := range sessions {
		got, err := classifier.Classify(ctx, featureRatchetUnknownInput(fmt.Sprintf("goroutine-9-3-%04d", index)))
		if err != nil {
			t.Fatalf("session %d: %v", index, err)
		}
		if got.IsCodingAgent() {
			t.Fatalf("session %d classification = %+v, want unknown after scripted failures", index, got)
		}
	}
	after := runtime.NumGoroutine()
	if after != before {
		t.Fatalf("%d sessions changed the live goroutine count from %d to %d (requirement 10.7)", sessions, before, after)
	}

	leak := make(chan struct{})
	go func() { <-leak }()
	leaked := runtime.NumGoroutine()
	close(leak)
	if leaked != before+1 {
		t.Fatalf("the goroutine gauge reported %d after leaking one goroutine from a baseline of %d; this ratchet cannot observe a leak", leaked, before)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// BenchmarkClassifyUnknownLocalBoundedEvaluation measures the unknown local hot
// path over the widest bounded metadata the contract allows, including the
// indexed state read the classifier must perform.
func BenchmarkClassifyUnknownLocalBoundedEvaluation(b *testing.B) {
	ctx := context.Background()
	store := newFakeStore()
	classifier := mustClassifierTB(b, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())

	const sessions = 256
	next := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessionID := fmt.Sprintf("bench-unknown-9-3-%04d", next%sessions)
		next++
		got, err := classifier.Classify(ctx, featureRatchetUnknownInput(sessionID))
		if err != nil {
			b.Fatalf("unknown turn: %v", err)
		}
		if got != (session.Classification{}) {
			b.Fatalf("unknown turn classification = %+v, want unknown", got)
		}
	}
	b.StopTimer()
	loads, promotes := store.counts()
	if promotes != 0 {
		b.Fatalf("unknown local turns made %d durable writes, want zero", promotes)
	}
	b.ReportMetric(float64(loads)/float64(b.N), "store-reads/op")
	if loads != b.N {
		b.Fatalf("unknown local turns made %d store reads over %d iterations, want exactly one indexed miss each", loads, b.N)
	}
}

// BenchmarkEvaluateLocalBoundedWidth isolates the pure evaluation from the state
// read, reporting the per-evaluation allocation cost of the widest bounded
// metadata the policy may inspect.
func BenchmarkEvaluateLocalBoundedWidth(b *testing.B) {
	cfg := sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}
	input := featureRatchetUnknownInput("bench-bounded-9-3")
	if got := sessionclassification.EvaluateLocal(cfg, input); got != (sessionclassification.LocalDecision{}) {
		b.Fatalf("fixture is not a still-unknown worst case: %+v", got)
	}
	b.ReportAllocs()
	for b.Loop() {
		decisionSink = sessionclassification.EvaluateLocal(cfg, input)
	}
}

// BenchmarkClassifyWarmPositiveSession measures the 10.1 hot path when the
// positive is already projected on the session view.
func BenchmarkClassifyWarmPositiveSession(b *testing.B) {
	ctx := context.Background()
	store := newFakeStore()
	decider := &countingDecider{}
	classifier := mustRemoteClassifierTB(b, sessionclassification.Config{
		Mode: sessionclassification.ModeHybrid, Remote: validTestRemoteConfig(),
	}, store, fixedNow(), decider)
	input := featureRatchetUnknownInput("bench-warm-9-3")
	input.Session.Classification = session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.codex",
		Revision:   1,
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := classifier.Classify(ctx, input); err != nil {
			b.Fatalf("warm turn: %v", err)
		}
	}
	b.StopTimer()
	if loads, promotes := store.counts(); loads != 0 || promotes != 0 {
		b.Fatalf("warm positive turns made %d store reads and %d writes, want zero of each", loads, promotes)
	}
	if got := decider.callCount(); got != 0 {
		b.Fatalf("warm positive turns made %d remote calls, want zero", got)
	}
}
