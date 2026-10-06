package sessionclassification

// Task 9.3 hot-path allocation, latency and store-call ratchets.
//
// Requirements 10.1-10.8, 5.1 and 11.7. Design "Performance and Concurrency"
// and "Testing Strategy".
//
// Every ratchet here is deterministic. Counters, mutex TryLock probes,
// in-use connection counts and goroutine gauges are compared against exact
// expectations, so no assertion here depends on wall-clock latency and none of
// them can pass because the machine was fast.
//
// Every probe that can report "held" is paired with a control that holds the
// thing it probes for, so a probe that can never fail is impossible by
// construction. See TestRatchetProbesDetectHeldLocksAndTransactions.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	featurestate "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdkclassification "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	_ "modernc.org/sqlite"
)

// ratchetProgress bounds every "this must finish" wait. It is a failure bound,
// never a synchronization mechanism: each wait also has a positive completion
// signal, so a passing test never needed the timeout to expire.
const ratchetProgress = 20 * time.Second

// ratchetClockTime is the fixed logical instant used where lease and cache
// arithmetic must be deterministic. Remote decision latency is measured from
// the process clock, never from this value.
var ratchetClockTime = func() time.Time {
	return time.Date(2026, time.October, 4, 9, 0, 0, 0, time.UTC)
}

// ratchetRemoteConfig mirrors the documented operational remote values. The
// credential stays a referenced environment name and is never read here, so no
// egress and no credential is possible (requirements 6.1, 12.9).
func ratchetRemoteConfig() *featurestate.RemoteConfig {
	return &featurestate.RemoteConfig{
		Provider:              "jev",
		APIKeyEnv:             "TYPESAFE_API_KEY",
		Timeout:               750 * time.Millisecond,
		MaxAttemptsPerSession: 1,
		LeaseTTL:              2 * time.Second,
		RetryBackoff:          0,
		PositiveThreshold:     0.90,
	}
}

// ---------------------------------------------------------------------------
// Instruments
// ---------------------------------------------------------------------------

// ratchetStoreCalls is the explicit durable-operation census. It is the counter
// that turns "no database write" and "no durable read" from prose into numbers,
// and every ratchet that asserts zero also asserts a non-zero control value so
// the census provably fires.
type ratchetStoreCalls struct {
	loads     int64
	promotes  int64
	claims    int64
	completes int64
}

func (c ratchetStoreCalls) total() int64 { return c.loads + c.promotes + c.claims + c.completes }

// ratchetDurableStore is the authoritative store the coordinator delegates to. It
// counts every durable operation and tracks how many are executing at once.
//
// The in-flight gauge is what makes requirement 10.4 measurable: the remote flow
// issues claim, decision and completion as three separate calls, so at the
// instant the decision runs no durable call may still be executing.
type ratchetDurableStore struct {
	inner featurestate.Store

	inFlight  atomic.Int64
	maxFlight atomic.Int64

	loads     atomic.Int64
	promotes  atomic.Int64
	claims    atomic.Int64
	completes atomic.Int64
}

var _ featurestate.Store = (*ratchetDurableStore)(nil)

func newRatchetDurableStore(inner featurestate.Store) *ratchetDurableStore {
	return &ratchetDurableStore{inner: inner}
}

// ratchetCoordinator is the benchmark-side twin of newTestCoordinatorWithConfig,
// which is test-only. Both configure identical finite bounds, so a benchmark
// measures the same coordinator the ratchets certify.
func ratchetCoordinator(tb testing.TB, store featurestate.Store, config CoordinatorConfig) *Coordinator {
	tb.Helper()
	coordinator, err := NewCoordinator(store, config)
	if err != nil {
		tb.Fatalf("NewCoordinator: %v", err)
	}
	return coordinator
}

func (s *ratchetDurableStore) enter() func() {
	flight := s.inFlight.Add(1)
	for {
		peak := s.maxFlight.Load()
		if flight <= peak || s.maxFlight.CompareAndSwap(peak, flight) {
			break
		}
	}
	return func() { s.inFlight.Add(-1) }
}

func (s *ratchetDurableStore) Load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	s.loads.Add(1)
	defer s.enter()()
	return s.inner.Load(ctx, key)
}

func (s *ratchetDurableStore) Promote(
	ctx context.Context, key featurestate.Key, proposal session.Classification, now time.Time,
) (featurestate.Record, bool, error) {
	s.promotes.Add(1)
	defer s.enter()()
	return s.inner.Promote(ctx, key, proposal, now)
}

func (s *ratchetDurableStore) ClaimRemote(
	ctx context.Context, key featurestate.Key, now time.Time,
	maxAttempts uint32, leaseTTL time.Duration, retryBackoff time.Duration,
) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	s.claims.Add(1)
	defer s.enter()()
	return s.inner.ClaimRemote(ctx, key, now, maxAttempts, leaseTTL, retryBackoff)
}

func (s *ratchetDurableStore) CompleteRemote(
	ctx context.Context, claim featurestate.RemoteClaim, result featurestate.RemoteCompletion, now time.Time,
) (featurestate.Record, error) {
	s.completes.Add(1)
	defer s.enter()()
	return s.inner.CompleteRemote(ctx, claim, result, now)
}

func (s *ratchetDurableStore) calls() ratchetStoreCalls {
	return ratchetStoreCalls{
		loads:     s.loads.Load(),
		promotes:  s.promotes.Load(),
		claims:    s.claims.Load(),
		completes: s.completes.Load(),
	}
}

func (s *ratchetDurableStore) resetCalls() {
	s.loads.Store(0)
	s.promotes.Store(0)
	s.claims.Store(0)
	s.completes.Store(0)
}

// ratchetStateAuthority hands the shared process coordinator to one generation's
// classifier, exactly as the production StateHolder does.
type ratchetStateAuthority struct{ store featurestate.Store }

func (a ratchetStateAuthority) ClassificationState() (featurestate.Store, error) { return a.store, nil }

// ratchetRemoteWindow is the bounded observation taken at the exact instant the
// remote decision is in flight.
type ratchetRemoteWindow struct {
	coordinatorLockHeld bool
	durableLockHeld     bool
	storeCallsInFlight  int64
	sqlConnsInUse       int
}

// ratchetDecider is the hermetic remote decider. It counts every call, so a
// "zero remote call" claim is always paired with a control that registers one,
// and it parks inside Decide so the in-flight window can be observed without
// sleeping or racing (requirements 12.8, 12.9).
type ratchetDecider struct {
	decision featurestate.RemoteDecision
	err      error

	calls atomic.Int64

	probe    func() ratchetRemoteWindow
	windows  chan ratchetRemoteWindow
	parked   chan struct{}
	release  chan struct{}
	spinWork int
}

var _ featurestate.RemoteDecider = (*ratchetDecider)(nil)

func (d *ratchetDecider) Decide(_ context.Context, in featurestate.RemoteInput) (featurestate.RemoteDecision, error) {
	d.calls.Add(1)
	if d.probe != nil {
		d.windows <- d.probe()
	}
	if d.parked != nil {
		select {
		case d.parked <- struct{}{}:
		default:
		}
		<-d.release
	}
	// spinWork burns deterministic CPU rather than sleeping, so a benchmark can
	// carry simulated decision cost without a timing primitive anywhere.
	for i := 0; i < d.spinWork; i++ {
		_ = in.Operation
	}
	return d.decision, d.err
}

func (d *ratchetDecider) callCount() int64 { return d.calls.Load() }

// ratchetProbe reports what is still held right now. A nil component is simply
// not observed, which is how the memory-only and the SQLite variants share it.
func ratchetProbe(
	coordinator *Coordinator,
	durable *MemoryStore,
	store *ratchetDurableStore,
	sqlDB *sql.DB,
) func() ratchetRemoteWindow {
	return func() ratchetRemoteWindow {
		window := ratchetRemoteWindow{}
		if coordinator != nil {
			window.coordinatorLockHeld = !ratchetMutexFree(&coordinator.mu)
		}
		if durable != nil {
			window.durableLockHeld = !ratchetMutexFree(&durable.mu)
		}
		window.storeCallsInFlight = store.inFlight.Load()
		if sqlDB != nil {
			window.sqlConnsInUse = sqlDB.Stats().InUse
		}
		return window
	}
}

// ratchetMutexFree reports whether the mutex can be acquired right now. TryLock
// is what makes "not held" an observation instead of an inference.
func ratchetMutexFree(mu *sync.Mutex) bool {
	if mu.TryLock() {
		mu.Unlock()
		return true
	}
	return false
}

// remoteWindowVerdict is the single judgment both remote-window ratchets use, so
// the in-memory and SQLite variants cannot drift apart in what they accept.
func remoteWindowVerdict(window ratchetRemoteWindow) error {
	var violations []string
	if window.coordinatorLockHeld {
		violations = append(violations, "the process-wide coordinator mutex was held while the remote decision was in flight (requirement 10.4)")
	}
	if window.durableLockHeld {
		violations = append(violations, "the authoritative store mutex was held while the remote decision was in flight (requirement 10.4)")
	}
	if window.storeCallsInFlight != 0 {
		violations = append(violations, fmt.Sprintf(
			"%d durable store calls were still executing during the remote decision (%+v); claim and completion must both return before egress",
			window.storeCallsInFlight, window))
	}
	if window.sqlConnsInUse != 0 {
		violations = append(violations, fmt.Sprintf(
			"%d SQLite connections were in use while the remote decision was in flight (%+v); no database connection or transaction may be held across egress",
			window.sqlConnsInUse, window))
	}
	if len(violations) == 0 {
		return nil
	}
	return errors.New("remote decision window violated: " + strings.Join(violations, "; "))
}

// ratchetUnknownInput is a still-unknown ordinary client turn: a generic SDK
// identity, one file-read tool, and no recognized workspace marker. Local
// evaluation cannot promote it, so in hybrid mode it is the shape that reaches
// the remote phase.
func ratchetUnknownInput(sessionID string) sdkclassification.Input {
	return sdkclassification.Input{
		TraceID: "trace-" + sessionID,
		Session: session.SessionView{
			AuthoritativeSessionID: sessionID,
			ClientSessionHint:      sessionID,
			ALegID:                 "aleg-" + sessionID,
			TurnID:                 "turn-" + sessionID,
			WorkspaceID:            "workspace-ratchet",
		},
		Workspace: workspace.WorkspaceView{ID: "workspace-ratchet"},
		Evidence: sdkclassification.Evidence{
			Operation:       lipapi.OperationOpenAIResponses,
			ClientUserAgent: "OpenAI/JS 4.0.0",
			ToolCategories:  sdkclassification.ToolCategoryFileRead,
		},
	}
}

// ratchetDecisiveInput is a turn whose accepted high-confidence coding-harness
// identity alone promotes, with no tools carried.
func ratchetDecisiveInput(sessionID string) sdkclassification.Input {
	in := ratchetUnknownInput(sessionID)
	in.Evidence.ClientUserAgent = "codex_cli_rs/1.2.3"
	return in
}

func ratchetClassifier(
	tb testing.TB, cfg featurestate.Config, coordinator *Coordinator,
	decider featurestate.RemoteDecider, now func() time.Time,
) *featurestate.Classifier {
	tb.Helper()
	classifier, err := featurestate.NewClassifier(cfg, featurestate.ClassifierDeps{
		State:  ratchetStateAuthority{store: coordinator},
		Remote: decider,
		Now:    now,
	})
	if err != nil {
		tb.Fatalf("NewClassifier: %v", err)
	}
	return classifier
}

func ratchetMemoryStore(tb testing.TB, maxEntries int, idleTTL time.Duration, now func() time.Time) *MemoryStore {
	tb.Helper()
	store, err := NewMemoryStore(MemoryStoreConfig{MaxEntries: maxEntries, IdleTTL: idleTTL}, WithClock(now))
	if err != nil {
		tb.Fatalf("NewMemoryStore: %v", err)
	}
	return store
}

// ratchetSQLite opens a real durable store. maxOpenConns bounds the pool so a
// held transaction or connection is observable instead of absorbed by spare
// capacity.
func ratchetSQLite(tb testing.TB, maxOpenConns int) (*sql.DB, featurestate.Store) {
	tb.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(tb.TempDir(), "classification-hot-path.db")) +
		"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(200)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		tb.Fatalf("open sqlite: %v", err)
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	tb.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		tb.Fatalf("new bun db: %v", err)
	}
	tb.Cleanup(func() { _ = bunDB.Close() })
	durable, err := NewBunStore(bunDB)
	if err != nil {
		tb.Fatalf("NewBunStore: %v", err)
	}
	if err := durable.EnsureSchema(context.Background()); err != nil {
		tb.Fatalf("EnsureSchema: %v", err)
	}
	return sqlDB, durable
}

// ---------------------------------------------------------------------------
// 10.1 / 10.6: a warm positive cache entry performs no durable and no remote work
// ---------------------------------------------------------------------------

// TestWarmPositiveCacheEntryPerformsNoDurableOrRemoteWork is the composed 10.1 /
// 10.6 ratchet. It runs real turns through the real classifier and the real
// coordinator over a real authoritative store.
//
// The load-bearing comparison is the pair of turns: a WARM key and a COLD key
// carrying byte-identical evidence. The cold key registers the remote call and
// the durable miss, so the warm key's zero census is a discriminating
// observation rather than a counter that never fires.
func TestWarmPositiveCacheEntryPerformsNoDurableOrRemoteWork(t *testing.T) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	durable := newRatchetDurableStore(ratchetMemoryStore(t, 64, time.Hour, clock))
	coordinator := newTestCoordinatorWithConfig(t, durable, CoordinatorConfig{
		CacheCapacity: 8, MaxInflight: 8, IdleTTL: time.Minute, Now: clock,
	})
	decider := &ratchetDecider{decision: featurestate.RemoteDecision{CodingProbability: 0.99}}
	// Hybrid mode is the discriminating posture: local decisive evidence
	// promotes without egress, while a still-unknown turn is remote-eligible, so
	// the remote-call counter is reachable from this same classifier.
	classifier := ratchetClassifier(t, featurestate.Config{
		Mode: featurestate.ModeHybrid, Remote: ratchetRemoteConfig(),
	}, coordinator, decider, clock)

	warmKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "warm-9-3"}
	establishing, err := classifier.Classify(ctx, ratchetDecisiveInput(warmKey.ID))
	if err != nil {
		t.Fatalf("establishing turn: %v", err)
	}
	if !establishing.IsCodingAgent() {
		t.Fatalf("establishing turn classification = %+v, want coding_agent", establishing)
	}
	establishingCalls := durable.calls()
	if establishingCalls.promotes != 1 || establishingCalls.loads != 1 || establishingCalls.total() != 2 {
		t.Fatalf("establishing durable calls = %+v, want exactly one load and one promote", establishingCalls)
	}
	if got := decider.callCount(); got != 0 {
		t.Fatalf("establishing turn made %d remote calls, want 0: decisive local evidence must not egress", got)
	}

	// The census is armed. The stored positive is read back through an
	// independent store instance so later "unchanged" claims observe committed
	// state rather than the process cache.
	stored := ratchetStoredPositive(t, durable.inner, warmKey)

	durable.resetCalls()
	const warmTurns = 64
	for turn := 0; turn < warmTurns; turn++ {
		got, err := classifier.Classify(ctx, ratchetUnknownInput(warmKey.ID))
		if err != nil {
			t.Fatalf("warm turn %d: %v", turn, err)
		}
		if got != establishing {
			t.Fatalf("warm turn %d classification = %+v, want the cached positive %+v", turn, got, establishing)
		}
	}
	warmCalls := durable.calls()
	if warmCalls.total() != 0 {
		t.Fatalf("warm positive cache entry made %d durable calls over %d turns (%+v), want zero",
			warmCalls.total(), warmTurns, warmCalls)
	}
	if got := decider.callCount(); got != 0 {
		t.Fatalf("warm positive cache entry made %d remote calls, want zero", got)
	}
	if after := ratchetStoredPositive(t, durable.inner, warmKey); after != stored {
		t.Fatalf("durable row after %d warm turns = %+v, want it byte-identical at %+v", warmTurns, after, stored)
	}

	// Control: byte-identical weak evidence for a key with no warm cache entry
	// registers exactly the work the warm key skipped. Without this control the
	// zero census above would be indistinguishable from a counter that never fires.
	coldKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "cold-9-3"}
	got, err := classifier.Classify(ctx, ratchetUnknownInput(coldKey.ID))
	if err != nil {
		t.Fatalf("cold turn: %v", err)
	}
	if !got.IsCodingAgent() {
		t.Fatalf("cold turn classification = %+v, want the remote-sourced promotion", got)
	}
	coldCalls := durable.calls()
	if coldCalls.loads != 1 || coldCalls.claims != 1 || coldCalls.completes != 1 {
		t.Fatalf("cold durable calls = %+v, want exactly one load, one claim and one completion", coldCalls)
	}
	if remote := decider.callCount(); remote != 1 {
		t.Fatalf("cold turn made %d remote calls, want exactly 1", remote)
	}
	if coldCalls.promotes != 0 {
		t.Fatalf("cold remote promotion made %d Store.Promote calls, want zero", coldCalls.promotes)
	}
}

// ratchetStoredPositive reads one committed positive through the authoritative
// store itself, bypassing every process cache.
func ratchetStoredPositive(tb testing.TB, store featurestate.Store, key featurestate.Key) featurestate.Record {
	tb.Helper()
	record, found, err := store.Load(context.Background(), key)
	if err != nil || !found || !record.Classification.IsCodingAgent() {
		tb.Fatalf("durable positive for %q = (%+v, found=%t, err=%v)", key.ID, record.Classification, found, err)
	}
	return record
}

// TestUnknownTurnDurableIOIsLimitedToTheMissItself pins 10.6 from the other
// side: an unknown session pays for exactly one indexed miss per turn and never
// a write, a lease claim, or a completion merely to rediscover that it is still
// unknown in heuristic mode.
func TestUnknownTurnDurableIOIsLimitedToTheMissItself(t *testing.T) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	durable := newRatchetDurableStore(ratchetMemoryStore(t, 64, time.Hour, clock))
	coordinator := newTestCoordinatorWithConfig(t, durable, CoordinatorConfig{
		CacheCapacity: 8, MaxInflight: 8, IdleTTL: time.Minute, Now: clock,
	})
	// A heuristic generation is structurally refused a decider, so egress is not
	// merely uncounted here: no remote dependency can exist at all (requirement
	// 6.2). A claim or completion could therefore only come from the local path.
	if _, err := featurestate.NewClassifier(
		featurestate.Config{Mode: featurestate.ModeHeuristic},
		featurestate.ClassifierDeps{
			State:  ratchetStateAuthority{store: coordinator},
			Remote: &ratchetDecider{decision: featurestate.RemoteDecision{CodingProbability: 0.99}},
			Now:    clock,
		},
	); !errors.Is(err, featurestate.ErrRemoteNotConfigured) {
		t.Fatalf("a heuristic generation accepted a remote decider (err=%v); egress could not be ruled out structurally", err)
	}
	classifier := ratchetClassifier(t, featurestate.Config{Mode: featurestate.ModeHeuristic},
		coordinator, nil, clock)

	const unknownTurns = 32
	for turn := 0; turn < unknownTurns; turn++ {
		got, err := classifier.Classify(ctx, ratchetUnknownInput("still-unknown-9-3"))
		if err != nil {
			t.Fatalf("unknown turn %d: %v", turn, err)
		}
		if got != (session.Classification{}) {
			t.Fatalf("unknown turn %d classification = %+v, want unknown", turn, got)
		}
	}
	calls := durable.calls()
	if calls.loads != unknownTurns {
		t.Fatalf("unknown turns made %d durable loads, want exactly one indexed miss each (%d)", calls.loads, unknownTurns)
	}
	if calls.promotes != 0 || calls.claims != 0 || calls.completes != 0 {
		t.Fatalf("unknown turns wrote durable state (%+v): writes=%d claims=%d completions=%d, want all zero",
			calls, calls.promotes, calls.claims, calls.completes)
	}
	// No decider counter is read here on purpose: this classifier was built with
	// a nil decider, so a remote call is not "uncounted", it is unrepresentable.
	// The discriminating counter that proves the same census fires for a
	// remote-capable generation is the cold-key control in
	// TestWarmPositiveCacheEntryPerformsNoDurableOrRemoteWork.
}

// ---------------------------------------------------------------------------
// 10.3 / 10.4: no lock or transaction is held across the remote decision
// ---------------------------------------------------------------------------

// TestRemoteDecisionHoldsNoCoordinatorLockNoStoreLockAndNoTransaction is the
// load-bearing 10.3 / 10.4 ratchet.
//
// The remote call is parked inside the decider, so the assertions are taken at
// the exact instant of the decision rather than around it. Three facts must hold
// at that instant: the process-wide coordinator mutex is free, the authoritative
// store mutex is free, and no durable store call is executing. While parked,
// unrelated sessions must also reach committed positives through the same
// coordinator and the same store, which is the only way "not held" becomes
// progress rather than a probe artefact.
func TestRemoteDecisionHoldsNoCoordinatorLockNoStoreLockAndNoTransaction(t *testing.T) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	memory := ratchetMemoryStore(t, 256, time.Hour, clock)
	durable := newRatchetDurableStore(memory)
	coordinator := newTestCoordinatorWithConfig(t, durable, CoordinatorConfig{
		CacheCapacity: 32, MaxInflight: 32, IdleTTL: time.Minute, Now: clock,
	})
	decider := &ratchetDecider{
		decision: featurestate.RemoteDecision{CodingProbability: 0.99},
		windows:  make(chan ratchetRemoteWindow, 1),
		parked:   make(chan struct{}, 1),
		release:  make(chan struct{}),
		probe:    ratchetProbe(coordinator, memory, durable, nil),
	}
	classifier := ratchetClassifier(t, featurestate.Config{
		Mode: featurestate.ModeHybrid, Remote: ratchetRemoteConfig(),
	}, coordinator, decider, clock)

	turnDone := make(chan error, 1)
	go func() {
		_, err := classifier.Classify(ctx, ratchetUnknownInput("remote-parked-9-3"))
		turnDone <- err
	}()

	select {
	case <-decider.parked:
	case err := <-turnDone:
		t.Fatalf("the turn finished before the remote decision was observed (err=%v); nothing was parked", err)
	case <-time.After(ratchetProgress):
		t.Fatal("the remote decision never reached the decider")
	}

	window := <-decider.windows
	if err := remoteWindowVerdict(window); err != nil {
		t.Error(err)
	}

	// Unrelated sessions must make real progress against the same coordinator and
	// the same authoritative store while one session waits on the network.
	const unrelatedSessions = 24
	progressDone := make(chan ratchetStoreCalls, 1)
	go func() {
		var mu sync.Mutex
		for index := range unrelatedSessions {
			key := featurestate.Key{
				Kind: featurestate.ScopeSecureSession,
				ID:   fmt.Sprintf("unrelated-9-3-%02d", index),
			}
			if _, _, err := coordinator.Promote(ctx, key,
				positiveProposal(session.SourceLocalIdentity, "client_family.codex"), now); err != nil {
				mu.Lock()
				progressDone <- durable.calls()
				mu.Unlock()
				return
			}
		}
		progressDone <- durable.calls()
	}()

	var unrelatedCalls ratchetStoreCalls
	select {
	case unrelatedCalls = <-progressDone:
	case <-time.After(ratchetProgress):
		t.Fatalf("%d unrelated sessions could not reach a positive while one session's remote call was in flight; a long-held process-wide mutex is serializing them (requirement 10.3)",
			unrelatedSessions)
	}
	if unrelatedCalls.promotes != unrelatedSessions {
		t.Fatalf("unrelated sessions made %d durable promotes, want %d", unrelatedCalls.promotes, unrelatedSessions)
	}
	for index := range unrelatedSessions {
		key := featurestate.Key{
			Kind: featurestate.ScopeSecureSession,
			ID:   fmt.Sprintf("unrelated-9-3-%02d", index),
		}
		if record := ratchetStoredPositive(t, durable.inner, key); !record.Classification.IsCodingAgent() {
			t.Fatalf("unrelated session %q did not reach a committed positive: %+v", key.ID, record.Classification)
		}
	}
	// The probe above was taken while the durable store was idle; unrelated work
	// proves the very same store and coordinator were reachable, so a held lock
	// would have been a real block rather than an unused one.
	if durable.inFlight.Load() != 0 {
		t.Fatalf("durable store still reports %d in-flight calls after all unrelated work completed", durable.inFlight.Load())
	}

	close(decider.release)
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatalf("parked remote turn: %v", err)
		}
	case <-time.After(ratchetProgress):
		t.Fatal("the parked remote turn never completed after the decider was released")
	}
	if got := decider.callCount(); got != 1 {
		t.Fatalf("remote calls for the parked turn = %d, want exactly 1", got)
	}
	finalCalls := durable.calls()
	if finalCalls.claims != 1 || finalCalls.completes != 1 {
		t.Fatalf("parked remote turn durable calls = %+v, want exactly one claim and one completion", finalCalls)
	}
	// The peak-concurrency gauge is the sequencing half of the same claim: claim,
	// decision and completion are three SEPARATE store calls, so no two durable
	// operations may ever overlap. A wrap-the-pair-in-one-call implementation would
	// hold the store for the whole remote phase and show a peak of one covering
	// both, while the unrelated-session progress assertion above is what catches
	// the blocked window.
	if peak := durable.maxFlight.Load(); peak != 1 {
		t.Fatalf("peak concurrent durable operations = %d, want 1: the remote pair must be separate store calls", peak)
	}
}

// TestRemoteDecisionHoldsNoSQLiteConnectionOrTransaction proves 10.4 against a
// real durable adapter instead of only against the in-process store.
//
// The SQLite pool is deliberately limited to ONE connection. A transaction or
// connection held across the remote decision would therefore make the pool
// unreachable, and an unrelated session's durable read would block. Completing
// that read while the decision is parked is the production-shaped proof.
func TestRemoteDecisionHoldsNoSQLiteConnectionOrTransaction(t *testing.T) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	sqlDB, bunDurable := ratchetSQLite(t, 1)
	durable := newRatchetDurableStore(bunDurable)
	coordinator := newTestCoordinatorWithConfig(t, durable, CoordinatorConfig{
		CacheCapacity: 16, MaxInflight: 16, IdleTTL: time.Minute, Now: clock,
	})
	decider := &ratchetDecider{
		decision: featurestate.RemoteDecision{CodingProbability: 0.99},
		windows:  make(chan ratchetRemoteWindow, 1),
		parked:   make(chan struct{}, 1),
		release:  make(chan struct{}),
		probe:    ratchetProbe(coordinator, nil, durable, sqlDB),
	}
	classifier := ratchetClassifier(t, featurestate.Config{
		Mode: featurestate.ModeJev, Remote: ratchetRemoteConfig(),
	}, coordinator, decider, clock)

	turnDone := make(chan error, 1)
	go func() {
		_, err := classifier.Classify(ctx, ratchetUnknownInput("sqlite-remote-9-3"))
		turnDone <- err
	}()

	select {
	case <-decider.parked:
	case err := <-turnDone:
		t.Fatalf("the turn finished before the remote decision was observed (err=%v)", err)
	case <-time.After(ratchetProgress):
		t.Fatal("the remote decision never reached the decider")
	}

	window := <-decider.windows
	if err := remoteWindowVerdict(window); err != nil {
		t.Error(err)
	}

	// The single-connection pool is free, so an unrelated durable read completes
	// immediately. This is the assertion a held transaction would break.
	unrelatedDone := make(chan error, 1)
	go func() {
		_, _, err := durable.inner.Load(ctx, featurestate.Key{
			Kind: featurestate.ScopeSecureSession, ID: "sqlite-unrelated-9-3",
		})
		unrelatedDone <- err
	}()
	select {
	case err := <-unrelatedDone:
		if err != nil {
			t.Fatalf("unrelated durable read while a remote decision was in flight: %v", err)
		}
	case <-time.After(ratchetProgress):
		t.Fatal("an unrelated durable read could not complete while one session's remote decision was in flight; a connection or transaction is held across egress (requirement 10.4)")
	}

	close(decider.release)
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatalf("parked remote turn: %v", err)
		}
	case <-time.After(ratchetProgress):
		t.Fatal("the parked remote turn never completed")
	}
	if got := decider.callCount(); got != 1 {
		t.Fatalf("remote calls for the parked turn = %d, want exactly 1", got)
	}
}

// TestRatchetProbesDetectHeldLocksAndTransactions is the non-vacuity control for
// every probe the remote-window ratchets rely on.
//
// Each probe reports "held" for a genuinely held lock or transaction. If any of
// these controls failed, the zero-observations in the two ratchets above would
// be indistinguishable from a probe that cannot fail.
func TestRatchetProbesDetectHeldLocksAndTransactions(t *testing.T) {
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	memory := ratchetMemoryStore(t, 16, time.Hour, clock)
	durable := newRatchetDurableStore(memory)
	coordinator := newTestCoordinatorWithConfig(t, durable, CoordinatorConfig{
		CacheCapacity: 4, MaxInflight: 4, IdleTTL: time.Minute, Now: clock,
	})

	free := ratchetProbe(coordinator, memory, durable, nil)
	if window := free(); window.coordinatorLockHeld || window.durableLockHeld || window.storeCallsInFlight != 0 {
		t.Fatalf("an idle store and coordinator reported held locks: %+v", window)
	}

	coordinator.mu.Lock()
	held := free()
	coordinator.mu.Unlock()
	if !held.coordinatorLockHeld {
		t.Error("the coordinator mutex probe did not detect a deliberately held coordinator mutex")
	}
	if held.durableLockHeld {
		t.Error("the durable mutex probe reported the store mutex while only the coordinator mutex was held")
	}

	memory.mu.Lock()
	held = free()
	memory.mu.Unlock()
	if !held.durableLockHeld {
		t.Error("the durable mutex probe did not detect a deliberately held store mutex")
	}
	if held.coordinatorLockHeld {
		t.Error("the coordinator probe reported the coordinator mutex while only the store mutex was held")
	}

	release := durable.enter()
	inFlight := free()
	release()
	if inFlight.storeCallsInFlight != 1 {
		t.Errorf("the in-flight gauge reported %d during one simulated durable call, want 1", inFlight.storeCallsInFlight)
	}
	if settled := free(); settled.storeCallsInFlight != 0 {
		t.Errorf("the in-flight gauge reported %d after the simulated durable call returned, want 0", settled.storeCallsInFlight)
	}

	// The verdict both remote-window ratchets use must reject each violation the
	// probes above can produce. Without this, a verdict that returned nil for
	// every window would satisfy both ratchets forever.
	for name, window := range map[string]ratchetRemoteWindow{
		"held coordinator mutex": {coordinatorLockHeld: true},
		"held store mutex":       {durableLockHeld: true},
		"durable call in flight": {storeCallsInFlight: 1},
		"held sql connection":    {sqlConnsInUse: 1},
	} {
		if err := remoteWindowVerdict(window); err == nil {
			t.Errorf("the remote-window verdict accepted %s (%+v); the ratchets it serves cannot fail", name, window)
		}
	}
	if err := remoteWindowVerdict(ratchetRemoteWindow{}); err != nil {
		t.Errorf("the remote-window verdict rejected a clean window: %v", err)
	}

	// The same in-use connection probe, against a real single-connection pool
	// holding a real open transaction.
	sqlDB, _ := ratchetSQLite(t, 1)
	sqlProbe := ratchetProbe(nil, nil, durable, sqlDB)
	if got := sqlProbe(); got.sqlConnsInUse != 0 {
		t.Fatalf("an idle single-connection pool reported %d in-use connections", got.sqlConnsInUse)
	}
	tx, err := sqlDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if got := sqlProbe(); got.sqlConnsInUse < 1 {
		t.Errorf("the in-use connection probe reported %d while a real transaction held the only connection, want at least 1", got.sqlConnsInUse)
	}
	// A pool whose only connection is held cannot serve an unrelated read, which
	// is exactly the condition the remote-decision ratchet relies on.
	blocked := make(chan error, 1)
	go func() {
		_, err := sqlDB.ExecContext(context.Background(), "SELECT 1")
		blocked <- err
	}()
	select {
	case err := <-blocked:
		t.Fatalf("an unrelated read completed while the only connection was held by a transaction (err=%v); the SQLite probe cannot discriminate a held connection", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback control transaction: %v", err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("unrelated read after rollback: %v", err)
		}
	case <-time.After(ratchetProgress):
		t.Fatal("the unrelated read never completed after the control transaction was rolled back")
	}
}

// ---------------------------------------------------------------------------
// 10.5: the process cache is explicitly bounded under unique session hints
// ---------------------------------------------------------------------------

// TestCoordinatorCacheStaysBoundedUnderManyUniqueSessionHints is the 10.5
// ratchet. The cache is driven with far more unique authoritative keys than its
// configured capacity and must never exceed it, while every evicted positive
// stays recoverable from the authoritative store.
//
// The control below repeats the drive with capacity enforcement disabled by
// construction: a coordinator whose cache capacity exceeds every key written can
// hold them all. That is what makes the bounded observation a measurement of the
// eviction policy rather than a property of the key count.
func TestCoordinatorCacheStaysBoundedUnderManyUniqueSessionHints(t *testing.T) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	const cacheCapacity = 64
	const uniqueKeys = 5000

	durable := newRatchetDurableStore(ratchetMemoryStore(t, uniqueKeys+16, time.Hour, clock))
	coordinator := newTestCoordinatorWithConfig(t, durable, CoordinatorConfig{
		CacheCapacity: cacheCapacity, MaxInflight: 64, IdleTTL: time.Minute, Now: clock,
	})

	last := ""
	for index := range uniqueKeys {
		last = fmt.Sprintf("unique-hint-9-3-%05d", index)
		key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: last}
		if _, promoted, err := coordinator.Promote(ctx, key,
			positiveProposal(session.SourceLocalIdentity, "client_family.codex"), now); err != nil || !promoted {
			t.Fatalf("Promote(%q) = promoted %v, err %v", key.ID, promoted, err)
		}
		if got := len(coordinator.cache); got > cacheCapacity {
			t.Fatalf("cache retained %d entries after %d unique hints, above the configured capacity %d (requirement 10.5)",
				got, index+1, cacheCapacity)
		}
	}
	if got := len(coordinator.cache); got != cacheCapacity {
		t.Fatalf("cache retained %d entries after %d unique hints, want exactly the configured capacity %d",
			got, uniqueKeys, cacheCapacity)
	}
	if got := coordinator.cacheLRU.Len(); got != cacheCapacity {
		t.Fatalf("cache list retained %d entries, want %d", got, cacheCapacity)
	}

	// Control: a capacity above the key count holds every key, so the bounded
	// observation above is caused by the eviction policy and not by the workload.
	controlDurable := newRatchetDurableStore(ratchetMemoryStore(t, uniqueKeys+16, time.Hour, clock))
	controlCoordinator := newTestCoordinatorWithConfig(t, controlDurable, CoordinatorConfig{
		CacheCapacity: uniqueKeys + 1, MaxInflight: 64, IdleTTL: time.Minute, Now: clock,
	})
	for index := range 512 {
		key := featurestate.Key{
			Kind: featurestate.ScopeSecureSession,
			ID:   fmt.Sprintf("unique-hint-9-3-%05d", index),
		}
		if _, promoted, err := controlCoordinator.Promote(ctx, key,
			positiveProposal(session.SourceLocalIdentity, "client_family.codex"), now); err != nil || !promoted {
			t.Fatalf("control Promote(%q) = promoted %v, err %v", key.ID, promoted, err)
		}
	}
	if got := len(controlCoordinator.cache); got != 512 {
		t.Fatalf("control cache retained %d entries, want 512: the bounded observation above cannot discriminate eviction", got)
	}

	// An evicted positive is still served, and it costs exactly one indexed
	// durable read to rediscover: capacity eviction must not lose the
	// classification or make it permanently unknown (design "Memory bounds").
	evicted := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "unique-hint-9-3-00000"}
	before := durable.calls()
	record, found, err := coordinator.Load(ctx, evicted)
	if err != nil || !found || !record.Classification.IsCodingAgent() {
		t.Fatalf("Load(evicted) = (%+v, found=%t, err=%v), want the durable positive", record.Classification, found, err)
	}
	if spent := durable.calls().loads - before.loads; spent != 1 {
		t.Fatalf("reloading one evicted positive cost %d durable reads, want exactly 1", spent)
	}

	// The most recently written key is still resident, so eviction is
	// least-recently-used rather than arbitrary.
	recent := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: last}
	if _, cached := coordinator.cache[recent]; !cached {
		t.Fatalf("the most recently promoted key %q was evicted; capacity eviction must be least-recently-used", recent.ID)
	}
}

// ---------------------------------------------------------------------------
// 10.7: no permanent goroutine, timer or background worker per session/request
// ---------------------------------------------------------------------------

// TestClassificationCreatesNoGoroutinePerSessionOrRequest is the behavioural half
// of 10.7. It drives many distinct sessions, including turns that exercise the
// bounded retry backoff and therefore the only timer the feature creates, and
// requires the live goroutine count to be exactly unchanged.
//
// The control at the end leaks one goroutine and requires the very same gauge to
// move, so this ratchet cannot pass on a gauge that cannot observe a leak.
func TestClassificationCreatesNoGoroutinePerSessionOrRequest(t *testing.T) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	durable := newRatchetDurableStore(ratchetMemoryStore(t, 512, time.Hour, clock))
	coordinator := newTestCoordinatorWithConfig(t, durable, CoordinatorConfig{
		CacheCapacity: 64, MaxInflight: 64, IdleTTL: time.Minute, Now: clock,
	})
	// RetryBackoff is non-zero and the scripted failure is retryable, so every
	// remote turn runs the one bounded time.NewTimer this feature owns.
	remote := ratchetRemoteConfig()
	remote.RetryBackoff = time.Millisecond
	remote.MaxAttemptsPerSession = 2
	decider := &ratchetDecider{
		err:     ratchetRetryableFailure{},
		windows: make(chan ratchetRemoteWindow, 1),
	}
	classifier := ratchetClassifier(t, featurestate.Config{
		Mode: featurestate.ModeJev, Remote: remote,
	}, coordinator, decider, clock)

	const sessions = 120
	// Warm the process up first so one-off runtime and store allocations cannot
	// be mistaken for a per-session worker.
	if _, err := classifier.Classify(ctx, ratchetUnknownInput("goroutine-warmup-9-3")); err != nil {
		t.Fatalf("warm-up turn: %v", err)
	}
	before := runtime.NumGoroutine()
	for index := range sessions {
		got, err := classifier.Classify(ctx, ratchetUnknownInput(fmt.Sprintf("goroutine-9-3-%04d", index)))
		if err != nil {
			t.Fatalf("session %d: %v", index, err)
		}
		if got.IsCodingAgent() {
			t.Fatalf("session %d classification = %+v, want unknown after a scripted retryable failure", index, got)
		}
	}
	after := runtime.NumGoroutine()
	if after != before {
		t.Fatalf("%d sessions left the live goroutine count at %d from a baseline of %d (requirement 10.7)",
			sessions, after, before)
	}

	// Control: one deliberately leaked goroutine must move the same gauge.
	leak := make(chan struct{})
	go func() { <-leak }()
	leaked := runtime.NumGoroutine()
	close(leak)
	if leaked != before+1 {
		t.Fatalf("the goroutine gauge reported %d after leaking exactly one goroutine from a baseline of %d; this ratchet cannot observe a leak",
			leaked, before)
	}
}

// ratchetRetryableFailure is a bounded, retryable remote failure. It carries a
// closed-vocabulary outcome through the optional featurestate.RemoteFailure
// contract, so the classifier retries inside its finite attempt budget.
type ratchetRetryableFailure struct{}

func (ratchetRetryableFailure) Error() string { return "scripted retryable remote failure" }

func (ratchetRetryableFailure) Outcome() featurestate.RemoteOutcome {
	return featurestate.RemoteServerError
}

var _ featurestate.RemoteFailure = ratchetRetryableFailure{}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// BenchmarkWarmPositiveCacheEntryTurn measures the 10.1 hot path: a session whose
// positive is already cached. Every iteration asserts the per-iteration census,
// so the reported cost can never belong to a path that quietly re-read or
// re-wrote durable state.
func BenchmarkWarmPositiveCacheEntryTurn(b *testing.B) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	durable := newRatchetDurableStore(ratchetMemoryStore(b, 64, time.Hour, clock))
	coordinator := ratchetCoordinator(b, durable, CoordinatorConfig{
		CacheCapacity: 8, MaxInflight: 8, IdleTTL: time.Minute, Now: clock,
	})
	decider := &ratchetDecider{decision: featurestate.RemoteDecision{CodingProbability: 0.99}}
	classifier := ratchetClassifier(b, featurestate.Config{
		Mode: featurestate.ModeHybrid, Remote: ratchetRemoteConfig(),
	}, coordinator, decider, clock)

	const sessionID = "bench-warm-9-3"
	if _, err := classifier.Classify(ctx, ratchetDecisiveInput(sessionID)); err != nil {
		b.Fatalf("establishing turn: %v", err)
	}
	durable.resetCalls()
	warmInput := ratchetUnknownInput(sessionID)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, err := classifier.Classify(ctx, warmInput)
		if err != nil {
			b.Fatalf("warm turn: %v", err)
		}
		if !got.IsCodingAgent() {
			b.Fatalf("warm turn classification = %+v, want coding_agent", got)
		}
	}
	b.StopTimer()
	if calls := durable.calls(); calls.total() != 0 {
		b.Fatalf("warm positive turns made %d durable calls (%+v), want zero", calls.total(), calls)
	}
	if remote := decider.callCount(); remote != 0 {
		b.Fatalf("warm positive turns made %d remote calls, want zero", remote)
	}
}

// BenchmarkUnknownLocalTurn measures the 10.2 / 5.1 unknown hot path over a real
// coordinator and a real authoritative store, including the indexed miss each
// unknown turn necessarily pays.
func BenchmarkUnknownLocalTurn(b *testing.B) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	durable := newRatchetDurableStore(ratchetMemoryStore(b, 4096, time.Hour, clock))
	coordinator := ratchetCoordinator(b, durable, CoordinatorConfig{
		CacheCapacity: 64, MaxInflight: 64, IdleTTL: time.Minute, Now: clock,
	})
	// A heuristic generation is structurally refused a decider, so egress cannot
	// exist at all on this path (requirement 6.2).
	if _, err := featurestate.NewClassifier(featurestate.Config{Mode: featurestate.ModeHeuristic},
		featurestate.ClassifierDeps{
			State:  ratchetStateAuthority{store: coordinator},
			Remote: &ratchetDecider{decision: featurestate.RemoteDecision{CodingProbability: 0.99}},
			Now:    clock,
		},
	); !errors.Is(err, featurestate.ErrRemoteNotConfigured) {
		b.Fatalf("a heuristic generation accepted a remote decider (err=%v)", err)
	}
	classifier := ratchetClassifier(b, featurestate.Config{Mode: featurestate.ModeHeuristic},
		coordinator, nil, clock)

	const sessions = 512
	next := 0

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessionID := fmt.Sprintf("bench-unknown-9-3-%04d", next%sessions)
		next++
		got, err := classifier.Classify(ctx, ratchetUnknownInput(sessionID))
		if err != nil {
			b.Fatalf("unknown turn: %v", err)
		}
		if got != (session.Classification{}) {
			b.Fatalf("unknown turn classification = %+v, want unknown", got)
		}
	}
	b.StopTimer()
	calls := durable.calls()
	if calls.promotes != 0 || calls.claims != 0 || calls.completes != 0 {
		b.Fatalf("unknown local turns wrote durable state (%+v), want loads only", calls)
	}
	// This classifier was built with a nil decider, so egress is unrepresentable
	// rather than merely uncounted; BenchmarkRemoteDecisionWithSimulatedLatency
	// is the paired control proving the same census does register egress calls.
}

// BenchmarkRemoteDecisionWithSimulatedLatency measures the remote hot path with a
// decider that carries deterministic CPU cost, and reports the durable-call
// census as a custom metric so the claim and the cost are read together.
func BenchmarkRemoteDecisionWithSimulatedLatency(b *testing.B) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	durable := newRatchetDurableStore(ratchetMemoryStore(b, 4096, time.Hour, clock))
	coordinator := ratchetCoordinator(b, durable, CoordinatorConfig{
		CacheCapacity: 64, MaxInflight: 64, IdleTTL: time.Minute, Now: clock,
	})
	decider := &ratchetDecider{
		decision: featurestate.RemoteDecision{CodingProbability: 0.99},
		spinWork: 512,
	}
	classifier := ratchetClassifier(b, featurestate.Config{
		Mode: featurestate.ModeJev, Remote: ratchetRemoteConfig(),
	}, coordinator, decider, clock)

	const sessions = 512
	next := 0
	remoteCalls := 0

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessionID := fmt.Sprintf("bench-remote-9-3-%04d", next%sessions)
		next++
		got, err := classifier.Classify(ctx, ratchetUnknownInput(sessionID))
		if err != nil {
			b.Fatalf("remote turn: %v", err)
		}
		if !got.IsCodingAgent() {
			b.Fatalf("remote turn classification = %+v, want the remote-sourced promotion", got)
		}
	}
	b.StopTimer()
	remoteCalls = int(decider.callCount())
	calls := durable.calls()
	b.ReportMetric(float64(calls.loads)/float64(b.N), "durable-loads/op")
	b.ReportMetric(float64(calls.claims)/float64(b.N), "durable-claims/op")
	b.ReportMetric(float64(calls.completes)/float64(b.N), "durable-completions/op")
	b.ReportMetric(float64(remoteCalls)/float64(b.N), "remote-calls/op")
	if remoteCalls != b.N {
		b.Fatalf("remote calls = %d over %d iterations, want exactly one egress per still-unknown turn", remoteCalls, b.N)
	}
	if calls.claims != calls.completes || calls.claims != int64(b.N) {
		b.Fatalf("remote durable calls = %+v over %d iterations, want one claim and one completion each", calls, b.N)
	}
	if calls.promotes != 0 {
		b.Fatalf("remote promotions made %d Store.Promote calls, want zero", calls.promotes)
	}
}

// BenchmarkCoordinatorCacheUnderUniqueHints measures the 10.5 admission path:
// every iteration writes a fresh authoritative key into a capacity-bounded cache.
func BenchmarkCoordinatorCacheUnderUniqueHints(b *testing.B) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	const cacheCapacity = 256
	durable := newRatchetDurableStore(ratchetMemoryStore(b, cacheCapacity*64, time.Hour, clock))
	coordinator := ratchetCoordinator(b, durable, CoordinatorConfig{
		CacheCapacity: cacheCapacity, MaxInflight: 64, IdleTTL: time.Minute, Now: clock,
	})

	index := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		key := featurestate.Key{
			Kind: featurestate.ScopeSecureSession,
			ID:   fmt.Sprintf("bench-hint-9-3-%08d", index),
		}
		index++
		if _, promoted, err := coordinator.Promote(ctx, key,
			positiveProposal(session.SourceLocalIdentity, "client_family.codex"), now); err != nil || !promoted {
			b.Fatalf("Promote(%q) = promoted %v, err %v", key.ID, promoted, err)
		}
		if got := len(coordinator.cache); got > cacheCapacity {
			b.Fatalf("cache retained %d entries, above the configured capacity %d", got, cacheCapacity)
		}
	}
	b.StopTimer()
}

// TestRatchetStoreCensusFires is the smallest possible non-vacuity control for the
// durable census itself: one load and one promotion must register exactly one
// each. Every zero-assertion in this file is only meaningful because of it.
func TestRatchetStoreCensusFires(t *testing.T) {
	ctx := context.Background()
	now := ratchetClockTime()
	clock := func() time.Time { return now }
	durable := newRatchetDurableStore(ratchetMemoryStore(t, 8, time.Hour, clock))
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "census-9-3"}

	if _, found, err := durable.Load(ctx, key); err != nil || found {
		t.Fatalf("durable Load of a missing key = (found %v, err %v), want false, nil", found, err)
	}
	if _, _, err := durable.Promote(ctx, key,
		positiveProposal(session.SourceLocalIdentity, "client_family.codex"), now); err != nil {
		t.Fatalf("durable Promote: %v", err)
	}
	want := ratchetStoreCalls{loads: 1, promotes: 1}
	if got := durable.calls(); got != want {
		t.Fatalf("durable census = %+v, want %+v; a zero assertion built on it could never fail", got, want)
	}
}
