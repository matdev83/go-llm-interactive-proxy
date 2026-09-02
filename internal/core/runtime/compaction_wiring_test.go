package runtime_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

// recordingCompactionObserver records dispatched compaction events in order.
type recordingCompactionObserver struct {
	mu     sync.Mutex
	events []compaction.Event
}

func (r *recordingCompactionObserver) OnCompaction(_ context.Context, ev compaction.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingCompactionObserver) snapshot() []compaction.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]compaction.Event(nil), r.events...)
}

// compactionTestExecutor wires a fresh memory store, an observer snapshot, and
// a detector into a test executor.
func compactionTestExecutor(t *testing.T, d runtime.CompactionDetector, rec *recordingCompactionObserver) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return compactionTestExecutorWithStore(t, d, rec, st)
}

// compactionTestExecutorWithStore wires a test executor around a caller-owned
// store so tests can prove cross-executor (generation-replacement) continuity:
// the shared store resolves the same continuity key to the same authoritative
// A-leg, mirroring the process-owned store shared across runtime generations.
func compactionTestExecutorWithStore(t *testing.T, d runtime.CompactionDetector, rec *recordingCompactionObserver, st b2bua.Store) *runtime.Executor {
	t.Helper()
	bus := hooks.New(hooks.Config{})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.Rand = routing.NewSeededRng(1)
	var observers []compaction.Observer
	if rec != nil {
		observers = []compaction.Observer{rec}
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers: observers,
		}),
	})
	if d != nil {
		ex.CompactionRuntime = runtime.CompactionRuntime{Detector: d}
	}
	return ex
}

// compactionSecureSessionForTest builds one process-owned secure-session
// manager over a shared store, mirroring the manager built by
// prepareExecutorSecureSessionForTests but shared across executor instances
// (generations) the way production shares it.
func compactionSecureSessionForTest(t *testing.T, st b2bua.Store) *app.Manager {
	t.Helper()
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := make([]byte, 32)
	for i := range fk {
		fk[i] = byte(i + 1)
	}
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: fk,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func openStubBackend(opens func() lipapi.ManagedEventStream) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return opens(), nil
		},
	}
}

func drain(t *testing.T, stream lipapi.EventStream) []lipapi.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out []lipapi.Event
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		out = append(out, ev)
	}
}

func compactCall(continuity string, items []lipapi.Item) *lipapi.Call {
	return &lipapi.Call{
		ID:         "call-" + continuity,
		Session:    lipapi.SessionRef{ContinuityKey: continuity},
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Items:      items,
	}
}

func bigItems(prefix string, tails ...string) []lipapi.Item {
	items := []lipapi.Item{{
		Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Status: lipapi.ItemStatusCompleted,
		Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: prefix}},
	}}
	for i, tt := range tails {
		role := lipapi.RoleAssistant
		if i%2 == 1 {
			role = lipapi.RoleUser
		}
		items = append(items, lipapi.Item{
			Kind: lipapi.ItemKindMessage, Role: role, Status: lipapi.ItemStatusCompleted,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: tt}},
		})
	}
	return items
}

// TestCompactionWiring_startEmittedOnlyAfterOpen proves a signature-looking
// request that opens upstream emits exactly one started event with
// signature-strict evidence (requirements 3.2, 4.6).
func TestCompactionWiring_startEmittedOnlyAfterOpen(t *testing.T) {
	t.Parallel()
	d := compactiondetect.New(compactiondetect.Config{})
	rec := &recordingCompactionObserver{}
	ex := compactionTestExecutor(t, d, rec)
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished},
			})
		}),
	}
	stream, err := ex.Execute(context.Background(), compactCall("ck-open", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now")))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, stream)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("events=%v want exactly one started", got)
	}
	ev := got[0]
	if ev.Phase != compaction.PhaseStarted || ev.RuleID != "codex.local_checkpoint.v1" ||
		ev.Evidence != compaction.EvidenceSignatureStrict {
		t.Fatalf("event wrong: %+v", ev)
	}
	if ev.ALegID == "" || ev.TraceID == "" || ev.TransactionID == "" {
		t.Fatalf("correlation metadata missing: %+v", ev)
	}
}

// TestCompactionWiring_noStartBeforeOpen proves a signature-looking request
// that never opens upstream (local rejection/open failure) emits nothing
// (requirement 8.5).
func TestCompactionWiring_noStartBeforeOpen(t *testing.T) {
	t.Parallel()
	d := compactiondetect.New(compactiondetect.Config{})
	rec := &recordingCompactionObserver{}
	ex := compactionTestExecutor(t, d, rec)
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("upstream refused")
			},
		},
	}
	_, err := ex.Execute(context.Background(), compactCall("ck-noopen", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now")))
	if err == nil {
		t.Fatal("expected open failure")
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("no-open request emitted events: %+v", got)
	}
}

// TestCompactionWiring_detectorRunsWithoutObservers proves process-owned
// detector state is committed even when the current generation has no metadata
// observers. A later snapshot with an observer must still receive the
// same-A-leg history completion from the earlier observer-free request; this
// prevents observer registration from becoming detector authority.
func TestCompactionWiring_detectorRunsWithoutObservers(t *testing.T) {
	t.Parallel()
	d := compactiondetect.New(compactiondetect.Config{})
	ex := compactionTestExecutor(t, d, nil)
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
			})
		}),
	}
	firstCall := compactCall("ck-no-observers", bigItems(bigText(40000), "tail-one", "tail-two"))
	first, err := ex.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, first)

	rec := &recordingCompactionObserver{}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers: []compaction.Observer{rec},
		}),
	})
	secondCall := compactCall("ck-no-observers", bigItems(bigText(6000), "tail-one", "tail-two"))
	secondCall.Session.AuthoritativeSessionID = firstCall.Session.AuthoritativeSessionID
	secondCall.Session.ResumeToken = firstCall.Session.ResumeToken
	second, err := ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, second)
	got := rec.snapshot()
	if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted || got[0].Evidence != compaction.EvidenceHistoryHeuristic {
		t.Fatalf("observer-free detector state was not retained: events=%v", got)
	}
}

// TestCompactionWiring_failoverSingleStart proves a start is created once per
// logical request even when the first B-leg fails and failover opens a
// replacement (requirements 3.5, 5.4).
func TestCompactionWiring_failoverSingleStart(t *testing.T) {
	t.Parallel()
	d := compactiondetect.New(compactiondetect.Config{})
	rec := &recordingCompactionObserver{}
	ex := compactionTestExecutor(t, d, rec)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				// A recoverable pre-output failure is the only open error class the
				// executor fails over to another candidate for.
				return nil, lipapi.RecoverablePreOutputError(errors.New("bad upstream"))
			},
		},
		"good": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished},
			})
		}),
	}
	call := compactCall("ck-failover", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	call.Route = lipapi.RouteIntent{Selector: "bad:m|good:m"}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, stream)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("events=%v want exactly one started across failover", got)
	}
	if got[0].Phase != compaction.PhaseStarted {
		t.Fatalf("event wrong: %+v", got[0])
	}
}

// TestCompactionWiring_releasedItemObservedOnce proves a released compaction
// item reaches the detector through the final release seam exactly once and
// the released canonical event is field-equivalent (requirements 3.3, 8.4).
func TestCompactionWiring_releasedItemObservedOnce(t *testing.T) {
	t.Parallel()
	d := compactiondetect.New(compactiondetect.Config{})
	rec := &recordingCompactionObserver{}
	ex := compactionTestExecutor(t, d, rec)
	itemEv := lipapi.Event{Kind: lipapi.EventItem, Item: &lipapi.Item{
		Kind: lipapi.ItemKindCompaction, ID: "cmp-1", Status: lipapi.ItemStatusCompleted,
		Compaction: &lipapi.CompactionItem{EncapsulatedID: "enc-1"},
	}}
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				itemEv,
				{Kind: lipapi.EventResponseFinished},
			})
		}),
	}
	stream, err := ex.Execute(context.Background(), compactCall("ck-item", bigItems("ordinary turn")))
	if err != nil {
		t.Fatal(err)
	}
	released := drain(t, stream)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("events=%v want exactly one protocol completion", got)
	}
	if got[0].Phase != compaction.PhaseCompleted || got[0].RuleID != "protocol.context_compaction.v1" ||
		got[0].Evidence != compaction.EvidenceProtocolStrict {
		t.Fatalf("event wrong: %+v", got[0])
	}
	var releasedItem *lipapi.Event
	for i := range released {
		if released[i].Kind == lipapi.EventItem && released[i].Item != nil && released[i].Item.Kind == lipapi.ItemKindCompaction {
			releasedItem = &released[i]
			break
		}
	}
	if releasedItem == nil {
		t.Fatal("compaction item was not released to the client")
	}
	want := &lipapi.Item{
		Kind: lipapi.ItemKindCompaction, ID: "cmp-1", Status: lipapi.ItemStatusCompleted,
		Compaction: &lipapi.CompactionItem{EncapsulatedID: "enc-1"},
	}
	if !reflect.DeepEqual(releasedItem.Item, want) {
		t.Fatalf("released compaction item mutated:\n got %+v\nwant %+v", releasedItem.Item, want)
	}
}

// TestCompactionWiring_detectorSharedAcrossGenerations proves the process-owned
// detector keeps per-A-leg fingerprint/transaction state across two separate
// executor instances wired with the same detector — the runtimebundle
// generation-replacement shape (requirements 7.1, 8.6).
func TestCompactionWiring_detectorSharedAcrossGenerations(t *testing.T) {
	t.Parallel()
	d := compactiondetect.New(compactiondetect.Config{})
	rec := &recordingCompactionObserver{}

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Both generations share one process-owned store AND one secure-session
	// manager so the same continuity key resolves to the same authoritative
	// A-leg across executor instances (production shares both across
	// generations).
	mgr := compactionSecureSessionForTest(t, st)
	wireGen := func() *runtime.Executor {
		ex := compactionTestExecutorWithStore(t, d, rec, st)
		ex.SecureSession = mgr
		ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
		ex.SyntheticLocalPrincipal = true
		return ex
	}
	genA := wireGen()
	genA.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished},
			})
		}),
	}
	genB := wireGen()
	genB.Backends = genA.Backends

	// Generation A serves the large pre-compaction request; the echoed session
	// identity becomes the resume basis for generation B's compacted request.
	callA := compactCall("ck-gen", bigItems(bigText(40000), "tail-one", "tail-two"))
	streamA, err := genA.Execute(context.Background(), callA)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, streamA)
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("large request emitted: %+v", got)
	}
	if callA.Session.AuthoritativeSessionID == "" || callA.Session.ResumeToken == "" {
		t.Fatalf("first turn did not echo session identity: %+v", callA.Session)
	}

	// Generation B (replacement runtime, same process-owned detector) serves
	// the resumed compacted request: the heuristic must still see the A-leg
	// history recorded by generation A.
	callB := compactCall("ck-gen", bigItems(bigText(6000), "tail-one", "tail-two"))
	callB.Session.AuthoritativeSessionID = callA.Session.AuthoritativeSessionID
	callB.Session.ResumeToken = callA.Session.ResumeToken
	streamB, err := genB.Execute(context.Background(), callB)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, streamB)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("events=%v want one heuristic completion across generations", got)
	}
	if got[0].Evidence != compaction.EvidenceHistoryHeuristic || got[0].RuleID != compactiondetect.HeuristicRuleID {
		t.Fatalf("event wrong: %+v", got[0])
	}
}

// TestCompactionWiring_streamedMarkerSplitAcrossDeltas proves a signature
// post marker streamed as multiple text deltas reaches the detector through
// the release seam, completes the transaction exactly once, and the released
// deltas are untouched (requirements 4.7, 8.4).
func TestCompactionWiring_streamedMarkerSplitAcrossDeltas(t *testing.T) {
	t.Parallel()
	d := compactiondetect.New(compactiondetect.Config{})
	rec := &recordingCompactionObserver{}
	ex := compactionTestExecutor(t, d, rec)
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "[CONTEXT SUMM"},
				{Kind: lipapi.EventTextDelta, Delta: "ARY]: compacted"},
				{Kind: lipapi.EventResponseFinished},
			})
		}),
	}
	stream, err := ex.Execute(context.Background(), compactCall("ck-stream", bigItems("ordinary turn")))
	if err != nil {
		t.Fatal(err)
	}
	released := drain(t, stream)

	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("events=%v want exactly one completed", got)
	}
	if got[0].Phase != compaction.PhaseCompleted || got[0].RuleID != "hermes.legacy_compaction_post.v1" ||
		got[0].Evidence != compaction.EvidenceSignatureStrict {
		t.Fatalf("event wrong: %+v", got[0])
	}
	var deltas []string
	for _, ev := range released {
		if ev.Kind == lipapi.EventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if !reflect.DeepEqual(deltas, []string{"[CONTEXT SUMM", "ARY]: compacted"}) {
		t.Fatalf("released deltas mutated: %v", deltas)
	}
}

// wiringClock is a deterministic, concurrency-safe clock used to pin detector
// TTL/sweep timing in the keepalive regression test.
type wiringClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *wiringClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *wiringClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestCompactionWiring_keepaliveNeverObserved proves keepalive warnings never
// reach the compaction detector: the release seam evaluates the keepalive
// guard before compaction observation (finding #6; requirement 8.4), so
// proxy-internal pings cannot refresh A-leg activity or keep a compaction
// transaction alive. A warning carries no text and can match no rule, so an
// events-count assertion cannot distinguish "not observed" from "observed but
// no-op"; this test therefore pins the observable side effect — keepalives
// must not extend the A-leg lifetime past the detector IdleTTL.
//
// Timeline: the response starts at t0 (leg lastSeen = t0), keepalives arrive
// after the IdleTTL but before and across the sweep boundary, and the
// terminal arrives after both. Had the keepalives refreshed
// lastSeen, the protocol transaction would survive the sweep and the "stop"
// terminal would complete it; with the fix the idle leg is evicted instead
// and the terminal closes nothing.
func TestCompactionWiring_keepaliveNeverObserved(t *testing.T) {
	t.Parallel()
	clock := &wiringClock{t: time.Unix(100, 0).UTC()}
	idleTTL := 30 * time.Second
	d := compactiondetect.New(compactiondetect.Config{IdleTTL: idleTTL, Now: clock.Now})
	rec := &recordingCompactionObserver{}
	ex := compactionTestExecutor(t, d, rec)
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventWarning, WarningCode: stream.KeepaliveEventCode},
				{Kind: lipapi.EventWarning, WarningCode: stream.KeepaliveEventCode},
				{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
			})
		}),
	}
	call := compactCall("ck-keepalive", bigItems("ordinary turn"))
	call.Invocation.Operation = lipapi.OperationContextCompaction

	out, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var got []lipapi.Event
	recv := func(advance time.Duration) lipapi.Event {
		clock.Advance(advance)
		ev, err := out.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got = append(got, ev)
		return ev
	}
	_ = recv(0)                                     // response starts at t0; leg lastSeen = t0
	_ = recv(idleTTL + time.Second)                 // past IdleTTL, before sweep
	_ = recv(compactiondetect.DefaultSweepInterval) // across sweep boundary
	fin := recv(0)                                  // terminal after eviction

	// The keepalives still flow to the client — only their observation is
	// skipped, so this failure mode is about the guard, not event delivery.
	if len(got) != 4 || fin.Kind != lipapi.EventResponseFinished ||
		got[1].WarningCode != stream.KeepaliveEventCode || got[2].WarningCode != stream.KeepaliveEventCode {
		t.Fatalf("keepalives were not released to the client: %+v", got)
	}
	// The started event fired at open; the idle leg was evicted by the sweep
	// before the terminal, so no completion may be fabricated.
	if events := rec.snapshot(); len(events) != 1 || events[0].Phase != compaction.PhaseStarted {
		t.Fatalf("events=%v want exactly one started and no completed (keepalives must not reach the detector)", events)
	}
}

// bigText renders n characters deterministically for token-heavy fixtures.
func bigText(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[i%len(alphabet)]
	}
	return string(out)
}
