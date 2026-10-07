package featurehost

// Task 9.3 composed hot-path certification.
//
// Requirements 10.1, 10.8, 11.7 and 9.6, measured through the real standard
// featurehost: a real process over a real on-disk database, a real compiled
// generation, a real generic-core executor, and the production Prometheus
// observation surface.
//
// The warm-positive claim is certified here rather than only at the store seam,
// because "no database write" is only meaningful about the committed database.
// The census this file reads is the production counter 9.1 introduced, and the
// disabled claim reads the production counters too: a disabled generation must
// leave no classification observation of any kind (requirements 9.6, 10.8).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdkclassification "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
)

// hotPathClassificationObservations is the census of every classification-specific
// observation the production collector can emit. With a disabled generation every
// member must stay absent (requirements 9.6, 10.8), so the census is read as a
// whole rather than per family.
func hotPathClassificationObservations(tb testing.TB, registry *prometheus.Registry) map[string]float64 {
	tb.Helper()
	families, err := registry.Gather()
	if err != nil {
		tb.Fatalf("gather feature metrics: %v", err)
	}
	observed := map[string]float64{}
	for _, family := range families {
		switch family.GetName() {
		case "lip_session_classification_evaluations_total",
			"lip_session_classification_transitions_total",
			"lip_session_classification_remote_total",
			"lip_session_classification_remote_seconds",
			"lip_session_classification_store_total":
		default:
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make([]string, 0, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels = append(labels, label.GetName()+"="+label.GetValue())
			}
			key := family.GetName() + "{" + joinHotPathLabels(labels) + "}"
			switch {
			case metric.GetCounter() != nil:
				observed[key] += metric.GetCounter().GetValue()
			case metric.GetHistogram() != nil:
				observed[key] += float64(metric.GetHistogram().GetSampleCount())
			case metric.GetGauge() != nil:
				observed[key] += metric.GetGauge().GetValue()
			}
		}
	}
	return observed
}

func joinHotPathLabels(labels []string) string {
	out := ""
	for i, label := range labels {
		if i > 0 {
			out += ","
		}
		out += label
	}
	return out
}

// hotPathProcess composes a real featurehost process over a real on-disk SQLite
// database with a dedicated production metric registry.
func hotPathProcess(tb testing.TB, name string) (*Runtime, *bun.DB, *prometheus.Registry) {
	tb.Helper()
	dsn := certificationSQLiteDSN(filepath.Join(tb.TempDir(), name))
	database := certificationOpenSQLite(tb, dsn)
	tb.Cleanup(func() { _ = database.Close() })
	registry := prometheus.NewRegistry()
	rt, err := NewProcess(context.Background(), ProcessInput{
		Logger: slog.Default(), BunDB: database, MetricsRegistry: registry,
	})
	if err != nil {
		tb.Fatalf("NewProcess: %v", err)
	}
	tb.Cleanup(func() {
		if err := rt.Close(); err != nil {
			tb.Errorf("close process: %v", err)
		}
	})
	return rt, database, registry
}

// TestWarmPositiveComposedTurnsPerformNoDurableOrRemoteWork is the composed 10.1
// ratchet: after a session is promoted, further canonical turns on that session
// must not rewrite the durable row, must not register a durable operation, and
// must not attempt egress.
//
// The durable row is read back through an INDEPENDENT store instance after every
// turn, so "no database write" is observed as committed state rather than
// inferred from a counter. The control turn on an unrelated session supplies the
// discriminating opposite: it DOES register a durable read.
func TestWarmPositiveComposedTurnsPerformNoDurableOrRemoteWork(t *testing.T) {
	t.Parallel()

	rt, database, registry := hotPathProcess(t, "classification-hot-path-warm.db")
	generation := certificationCompile(t, rt, true, "")
	if generation.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	generation.start(t)

	executor, opens := certificationExecutor(t, database, certificationPlanesWithConsumer(t, generation.planes))
	sessionID, resumeToken := certificationResumableSession(t, executor, "client-hot-path-warm")

	// The SQL-level recorder counts every statement the feature's durable store
	// issues. It is the strongest available form of "no database write": it sees
	// the database boundary, not a counter that could disagree with it. It is
	// installed BEFORE the promoting turn so the recorder's own sensitivity is
	// established by a write it must see.
	statements := &hotPathStatementRecorder{}
	database.AddQueryHook(statements)

	certificationServe(t, executor, certificationCall("client-hot-path-warm", resumeToken, certificationPositiveUserAgent))
	if got := opens.Load(); got != 1 {
		t.Fatalf("promoting turn opened the backend %d times, want exactly 1", got)
	}
	statements.suspend()
	persisted, found := certificationLoadRow(t, database, sessionID)
	statements.resume()
	if !found || !persisted.Classification.IsCodingAgent() {
		t.Fatalf("durable row after the promoting turn = %+v (found=%t), want a persisted positive", persisted.Classification, found)
	}
	// The promoting turn's own write must be visible to the recorder, otherwise a
	// zero warm-turn count proves nothing about the recorder.
	if statements.snapshot().writes == 0 {
		t.Fatal("the recorder observed no durable write for the promoting turn; it cannot detect one either")
	}
	statements.reset()

	const warmTurns = 16
	for turn := range warmTurns {
		// The warm turns carry a WEAK identity, so only the restored durable
		// positive can classify them. A turn that could promote locally would not
		// prove the warm path.
		certificationServe(t, executor, certificationCall("client-hot-path-warm", resumeToken, certificationWeakUserAgent))
		if got := opens.Load(); got != int32(turn+2) {
			t.Fatalf("warm turn %d served %d total turns, want %d: the proxy must keep routing and streaming (requirement 11.7)", turn, got, turn+2)
		}
		// The test's own verification read is excluded from the feature census; its
		// statement is still recorded.
		statements.suspend()
		row, rowFound := certificationLoadRow(t, database, sessionID)
		statements.resume()
		if !rowFound || row != persisted {
			t.Fatalf("durable row after warm turn %d = %+v (found=%t), want it byte-identical at %+v: a warm positive must not write the database (requirement 10.1)",
				turn, row.Classification, rowFound, persisted.Classification)
		}
	}

	warm := statements.snapshot()
	if warm.writes != 0 {
		t.Fatalf("warm turns issued %d durable writes to the classification table (%+v), want zero (requirement 10.1)",
			warm.writes, warm.verbs)
	}
	if warm.reads != 0 {
		t.Fatalf("warm turns issued %d durable reads of the classification table (%+v), want zero: a warm positive cache entry needs no database access (requirement 10.1)",
			warm.reads, warm.verbs)
	}
	// Every statement the store issued must have matched a counted verb. A
	// dialect change to a CTE form would otherwise be recorded but uncounted, and
	// a write would read as zero work above.
	if len(warm.unclassified) != 0 {
		t.Fatalf("%d classification-table statements matched no counted verb, so their work is uncounted and the zero-write result above is not trustworthy: %d statements",
			len(warm.unclassified), len(warm.unclassified))
	}
	// The warm turns DID evaluate and DID restore, so the absence of durable work
	// is a decision rather than an inert stage.
	observed := hotPathClassificationObservations(t, registry)
	if evaluations := hotPathEvaluationCounts(observed); evaluations != warmTurns+1 {
		t.Fatalf("classification evaluations = %.0f over %d warm turns plus the promoting turn, want one per turn; the warm path was never exercised",
			evaluations, warmTurns)
	}
	if remote := hotPathRemoteAttemptCounts(observed); remote != 0 {
		t.Fatalf("remote attempts = %.0f across %d warm turns on a promoted session, want none (requirements 6.5, 10.1)",
			remote, warmTurns)
	}

	// Control: an unrelated unknown session on the same process DOES register a
	// durable read. Without it the warm-turn zero-count claim could not tell an
	// absent read from an unexercised store.
	controlExecutor, controlOpens := certificationExecutor(t, database,
		certificationPlanesWithConsumer(t, generation.planes))
	controlSession, controlToken := certificationResumableSession(t, controlExecutor, "client-hot-path-control")
	certificationServe(t, controlExecutor,
		certificationCall("client-hot-path-control", controlToken, certificationWeakUserAgent))
	if got := controlOpens.Load(); got != 1 {
		t.Fatalf("control turn opened the backend %d times, want exactly 1", got)
	}
	statements.suspend()
	controlRow, controlFound := certificationLoadRow(t, database, controlSession)
	statements.resume()
	if controlFound {
		t.Fatalf("an unknown control session created a durable row: %+v", controlRow.Classification)
	}
	control := statements.snapshot()
	if control.reads != 1 {
		t.Fatalf("the unknown control turn issued %d durable reads, want exactly the one indexed miss its unknown state requires (%+v)",
			control.reads, control.verbs)
	}
	if control.writes != 0 {
		t.Fatalf("the unknown control turn issued %d durable writes, want zero (requirement 10.6)", control.writes)
	}
	t.Logf("%d warm turns: %d durable reads, %d durable writes, 0 remote attempts; unknown control: %d read, %d writes",
		warmTurns, warm.reads, warm.writes, control.reads, control.writes)
}

// hotPathStatementRecorder counts the SQL statements the feature's durable store
// issues against its own table.
//
// It is a bun query hook, so it observes the database boundary rather than a
// feature-owned counter: a store that wrote without being observed, or an
// observation that disagreed with the database, is not possible by construction.
type hotPathStatementRecorder struct {
	mu    sync.Mutex
	reads int
	// writes counts every statement that can mutate a classification row. Both
	// the INSERT half of the first-positive upsert and the UPDATE half of a
	// rewrite count, because requirement 10.1 forbids either.
	writes int
	// suspended excludes the TEST's own independent verification reads from the
	// feature census. Their statements are still recorded, so a suspended read is
	// visible in failure output rather than hidden.
	suspended  bool
	statements []string
	// unclassified records classification-table statements whose verb the census
	// does not count. It must stay empty: an uncounted write would silently read
	// as zero work.
	unclassified []string
}

var _ bun.QueryHook = (*hotPathStatementRecorder)(nil)

func (r *hotPathStatementRecorder) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	// The statement only becomes visible in AfterQuery, so counting happens there.
	return ctx
}

func (r *hotPathStatementRecorder) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	query := event.Query
	if !strings.Contains(query, "session_classification") {
		// Schema DDL and other tables are not classification hot-path work.
		if !strings.Contains(query, "sqlite_") && !strings.Contains(query, "CREATE") &&
			!strings.Contains(query, "SELECT version()") && !strings.Contains(query, "PRAGMA") {
			return
		}
	}
	normalized := strings.Join(strings.Fields(query), " ")
	upper := strings.ToUpper(normalized)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, normalized)
	if r.suspended {
		return
	}
	switch {
	case strings.HasPrefix(upper, "SELECT"):
		r.reads++
	case strings.HasPrefix(upper, "INSERT"), strings.HasPrefix(upper, "UPDATE"),
		strings.HasPrefix(upper, "DELETE"):
		r.writes++
	default:
		// Every classification-table statement the store issues must land in one of
		// the counted verbs. A dialect change to a CTE form (WITH ... INSERT) would
		// otherwise be recorded but uncounted, and a write would read as zero.
		r.unclassified = append(r.unclassified, normalized)
	}
}

// suspend stops counting until resume. Statements keep being recorded.
func (r *hotPathStatementRecorder) suspend() {
	r.mu.Lock()
	r.suspended = true
	r.mu.Unlock()
}

func (r *hotPathStatementRecorder) resume() {
	r.mu.Lock()
	r.suspended = false
	r.mu.Unlock()
}

func (r *hotPathStatementRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads, r.writes, r.statements = 0, 0, nil
}

// hotPathSQLCounts is a snapshot of the recorder.
//
// The retained statements are reported as VERB COUNTS rather than full SQL: a
// durable classification upsert carries the session ID and the whole record, so
// echoing it into a failure message would put proxy-owned identity into test
// output. The counts are what a reviewer needs.
type hotPathSQLCounts struct {
	reads  int
	writes int
	// unclassified is the verbatim set of statements no counted verb matched.
	unclassified []string
	// verbs is a bounded "SELECT x N, INSERT y N" summary of everything recorded.
	verbs string
}

func (r *hotPathStatementRecorder) snapshot() hotPathSQLCounts {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := hotPathSQLCounts{unclassified: append([]string(nil), r.unclassified...)}
	verbs := map[string]int{}
	for _, statement := range r.statements {
		verbs[hotPathStatementVerb(statement)]++
	}
	names := make([]string, 0, len(verbs))
	for name := range verbs {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s x %d", name, verbs[name]))
	}
	out.reads, out.writes, out.verbs = r.reads, r.writes, strings.Join(parts, ", ")
	return out
}

// hotPathStatementVerb is the leading keyword of a normalized statement.
func hotPathStatementVerb(statement string) string {
	fields := strings.Fields(statement)
	if len(fields) == 0 {
		return "EMPTY"
	}
	return fields[0]
}

// hotPathRemoteAttemptCounts sums the production remote-attempt observations.
func hotPathRemoteAttemptCounts(observed map[string]float64) float64 {
	total := 0.0
	for key, value := range observed {
		if len(key) >= len("lip_session_classification_remote_total{") &&
			key[:len("lip_session_classification_remote_total{")] == "lip_session_classification_remote_total{" {
			total += value
		}
	}
	return total
}

// hotPathEvaluationCounts sums the production classification evaluations.
func hotPathEvaluationCounts(observed map[string]float64) float64 {
	total := 0.0
	for key, value := range observed {
		if len(key) >= len("lip_session_classification_evaluations_total{") &&
			key[:len("lip_session_classification_evaluations_total{")] == "lip_session_classification_evaluations_total{" {
			total += value
		}
	}
	return total
}

// TestDisabledGenerationPerformsNoClassificationWorkOfAnyKind is the composed
// 10.8 / 9.6 / 11.7 ratchet.
//
// A disabled generation publishes no classifier plane, so the stage must not run.
// The production census must therefore stay completely empty while the proxy still
// routes and streams every turn. The control compiles an enabled generation over
// the SAME process and requires the census to become non-empty, so an empty census
// cannot be attributed to an uninstrumented collector.
func TestDisabledGenerationPerformsNoClassificationWorkOfAnyKind(t *testing.T) {
	t.Parallel()

	rt, database, registry := hotPathProcess(t, "classification-hot-path-disabled.db")

	disabled := certificationCompile(t, rt, false, "")
	if disabled.classifier != nil {
		t.Fatalf("disabled generation published classifier %v", disabled.classifier)
	}
	// Production starts every published generation's lifecycles, so the disabled one
	// is started too. That is what makes "no state work" a claim about the disabled
	// lifecycle rather than about a lifecycle nobody ran.
	disabled.start(t)
	executor, opens := certificationExecutor(t, database, certificationPlanesWithConsumer(t, disabled.planes))

	const turns = 8
	_, resumeToken := certificationResumableSession(t, executor, "client-hot-path-disabled")
	for turn := range turns {
		certificationServe(t, executor,
			certificationCall("client-hot-path-disabled", resumeToken, certificationPositiveUserAgent))
		if got := opens.Load(); got != int32(turn+1) {
			t.Fatalf("disabled-generation turn %d served %d total turns, want %d: the proxy must route and stream without classification (requirement 11.7)",
				turn, got, turn+1)
		}
	}
	// Stronger than "no row": with no enabled generation the feature's own schema
	// was never created either, because only an enabled generation's lifecycle
	// initializes feature state (design "Process ownership"). So the assertion is
	// made against the schema itself rather than against a load that would fail.
	if hotPathClassificationTableExists(t, database) {
		t.Fatal("a disabled generation created the durable classification schema; no feature state work may happen")
	}
	if observed := hotPathClassificationObservations(t, registry); len(observed) != 0 {
		t.Fatalf("disabled generation emitted classification observations: %v; a disabled feature must emit none (requirements 9.6, 10.8)", observed)
	}
	// No remote egress was even attempted: the production remote observation is
	// absent, and the process held no initialized classification state.
	if rt.sessionClassification.Coordinator() != nil {
		t.Fatal("a disabled generation initialized the process classification coordinator; no state work may happen")
	}

	// Control: the SAME process with an enabled generation must populate the same
	// census, so the empty census above is an observation and not an uninstrumented
	// collector.
	enabled := certificationCompile(t, rt, true, "")
	if enabled.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	enabled.start(t)
	controlExecutor, controlOpens := certificationExecutor(t, database,
		certificationPlanesWithConsumer(t, enabled.planes))
	controlSession, controlToken := certificationResumableSession(t, controlExecutor, "client-hot-path-disabled-control")
	certificationServe(t, controlExecutor,
		certificationCall("client-hot-path-disabled-control", controlToken, certificationPositiveUserAgent))
	if got := controlOpens.Load(); got != 1 {
		t.Fatalf("control turn opened the backend %d times, want exactly 1", got)
	}
	if !certificationRowIsPositive(t, database, controlSession) {
		t.Fatal("the enabled control session did not reach a durable positive")
	}
	controlObserved := hotPathClassificationObservations(t, registry)
	if len(controlObserved) == 0 {
		t.Fatal("the enabled control emitted no classification observation; the disabled census proves nothing about an uninstrumented collector")
	}
	t.Logf("disabled generation: %v; enabled control: %d observation families", controlObserved, len(controlObserved))
}

// certificationRowIsPositive reports whether a durable positive exists for a
// session, reading committed state through an independent store instance.
func certificationRowIsPositive(tb testing.TB, database *bun.DB, sessionID string) bool {
	tb.Helper()
	row, found := certificationLoadRow(tb, database, sessionID)
	return found && row.Classification.IsCodingAgent()
}

// hotPathClassificationTableExists reports whether the feature's own durable
// schema exists. Only an enabled generation's lifecycle initializes feature
// state, so a disabled process must not have created it (design "Process
// ownership", requirements 10.8 and 11.7).
func hotPathClassificationTableExists(tb testing.TB, database *bun.DB) bool {
	tb.Helper()
	var names []string
	if err := database.NewRaw(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'session_classification'`,
	).Scan(context.Background(), &names); err != nil && !errors.Is(err, sql.ErrNoRows) {
		tb.Fatalf("inspect sqlite_master: %v", err)
	}
	return len(names) > 0
}

// hotPathClassificationStackMarkers are PRODUCTION frames that only a goroutine
// actually executing session classification can carry.
//
// The marker census is used instead of the process-wide live-goroutine count
// because the process-wide count is perturbed by unrelated infrastructure:
// database/sql starts and retires its own connection-pool goroutines during a
// composed turn, so a stable process count cannot distinguish a leaked
// classification worker from ordinary pool churn. A stack census attributes the
// goroutine to the feature, which is exactly what requirement 10.7 claims.
var hotPathClassificationStackMarkers = []string{
	// The feature's policy and classifier package.
	"internal/plugins/features/sessionclassification.",
	// The standard featurehost's concrete store/coordinator/decider package.
	"internal/standardplugins/featurehost/sessionclassification.",
	// The generic-core runner both lanes call.
	"extensions.RunSessionClassificationStage",
}

// hotPathClassificationGoroutines counts live goroutines whose stack carries a
// classification production frame and returns their traces, so a failure is
// actionable rather than a bare number.
func hotPathClassificationGoroutines() (int, []string) {
	buffer := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buffer, true)
		if n < len(buffer) {
			buffer = buffer[:n]
			break
		}
		buffer = make([]byte, 2*len(buffer))
	}
	var owned []string
	for trace := range strings.SplitSeq(string(buffer), "\n\n") {
		if trace == "" {
			continue
		}
		for _, marker := range hotPathClassificationStackMarkers {
			if strings.Contains(trace, marker) {
				owned = append(owned, trace)
				break
			}
		}
	}
	return len(owned), owned
}

// hotPathBlockingClassifier parks inside Classify so the stack census has a
// classification-owned goroutine it must find. It is the census's positive
// control: a marker set that cannot find this goroutine cannot find a leak either.
type hotPathBlockingClassifier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

var _ sdkclassification.Classifier = (*hotPathBlockingClassifier)(nil)

func (c *hotPathBlockingClassifier) ID() string { return "hot-path-blocking" }

func (c *hotPathBlockingClassifier) Classify(
	context.Context, sdkclassification.Input,
) (session.Classification, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return session.Classification{}, nil
}

// TestHotPathStackCensusFindsAndReleasesAClassificationGoroutine is the
// non-vacuity control for the stack census every 10.7 assertion in this file
// depends on.
//
// It parks a goroutine inside the generic classification runner, requires the
// census to find it, then releases it and requires the census to return to its
// baseline. Without this, a census whose markers never matched would satisfy
// every "zero classification goroutines" assertion forever.
func TestHotPathStackCensusFindsAndReleasesAClassificationGoroutine(t *testing.T) {
	baseline, baselineTraces := hotPathClassificationGoroutines()
	if baseline != 0 {
		t.Fatalf("classification goroutines before the control = %d, want 0: %v", baseline, baselineTraces)
	}

	blocking := &hotPathBlockingClassifier{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The generic runner is the production entry point; the classifier blocks
		// inside it, so this goroutine's stack carries a classification frame.
		extensions.RunSessionClassificationStage(context.Background(), nil, blocking, sdkclassification.Input{
			Session:  session.SessionView{AuthoritativeSessionID: "sess-stack-census"},
			Evidence: sdkclassification.Evidence{Operation: lipapi.OperationOpenAIResponses},
		})
	}()
	<-blocking.entered

	parked, parkedTraces := hotPathClassificationGoroutines()
	if parked != 1 {
		close(blocking.release)
		<-done
		t.Fatalf("the stack census found %d classification goroutines while one was parked, want exactly 1; it cannot observe a leak: %v",
			parked, parkedTraces)
	}
	close(blocking.release)
	<-done
	if after, traces := hotPathClassificationGoroutines(); after != baseline {
		t.Fatalf("the stack census found %d classification goroutines after releasing the control, want the baseline of %d: %v",
			after, baseline, traces)
	}
}

// TestComposedTurnsCreateNoGoroutinePerSession is the 10.7 ratchet at the composed
// layer, where a real executor, secure-session manager and durable store are in
// play.
//
// The census attributes goroutines to the feature by stack frame, so it is immune
// to the database/sql pool churn a composed turn causes, and a per-session worker
// created by this feature would be caught by its own frame.
func TestComposedTurnsCreateNoGoroutinePerSession(t *testing.T) {
	rt, database, _ := hotPathProcess(t, "classification-hot-path-goroutines.db")
	generation := certificationCompile(t, rt, true, "")
	if generation.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	generation.start(t)
	executor, _ := certificationExecutor(t, database, certificationPlanesWithConsumer(t, generation.planes))

	const sessions = 32
	for index := range sessions {
		_, resumeToken := certificationResumableSession(t, executor, "client-hot-path-goroutine")
		certificationServe(t, executor,
			certificationCall("client-hot-path-goroutine", resumeToken, certificationPositiveUserAgent))
		if owned, traces := hotPathClassificationGoroutines(); owned != 0 {
			t.Fatalf("after %d composed sessions %d goroutines were still executing classification: %v",
				index+1, owned, traces)
		}
	}
	t.Logf("%d composed sessions left zero classification-owned goroutines", sessions)
}

// TestComposedDisabledGenerationCreatesNoGoroutineAtAll is the 10.7 half for the
// disabled path: with no plane the stage must not run at all, so no goroutine may
// ever carry a classification frame.
func TestComposedDisabledGenerationCreatesNoGoroutineAtAll(t *testing.T) {
	rt, database, _ := hotPathProcess(t, "classification-hot-path-disabled-goroutines.db")
	disabled := certificationCompile(t, rt, false, "")
	if disabled.classifier != nil {
		t.Fatalf("disabled generation published classifier %v", disabled.classifier)
	}
	executor, _ := certificationExecutor(t, database, certificationPlanesWithConsumer(t, disabled.planes))

	_, resumeToken := certificationResumableSession(t, executor, "client-hot-path-disabled-goroutine")
	const turns = 24
	for index := range turns {
		certificationServe(t, executor,
			certificationCall("client-hot-path-disabled-goroutine", resumeToken, certificationPositiveUserAgent))
		if owned, traces := hotPathClassificationGoroutines(); owned != 0 {
			t.Fatalf("after %d disabled-generation turns %d goroutines carried a classification frame; the stage must not run at all: %v",
				index+1, owned, traces)
		}
	}
}

// TestComposedWarmPositiveTurnsSatisfyNoLatencyRegression is the latency-shaped
// half of 10.1 at the composed layer.
//
// It deliberately does NOT assert a wall-clock bound. A wall-clock assertion on a
// shared 8-CPU machine is a flaky ratchet, and requirement 10.1 is about work
// performed, not elapsed time. What this test does assert is that repeated warm
// turns neither accumulate durable state nor change the observation shape, which is
// what a latency regression from hidden per-turn I/O would actually look like.
func TestComposedWarmPositiveTurnsSatisfyNoLatencyRegression(t *testing.T) {
	t.Parallel()

	rt, database, registry := hotPathProcess(t, "classification-hot-path-latency.db")
	generation := certificationCompile(t, rt, true, "")
	if generation.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	generation.start(t)
	executor, opens := certificationExecutor(t, database, certificationPlanesWithConsumer(t, generation.planes))
	sessionID, resumeToken := certificationResumableSession(t, executor, "client-hot-path-latency")
	certificationServe(t, executor, certificationCall("client-hot-path-latency", resumeToken, certificationPositiveUserAgent))
	persisted, _ := certificationLoadRow(t, database, sessionID)

	const warmTurns = 64
	started := time.Now()
	for range warmTurns {
		certificationServe(t, executor, certificationCall("client-hot-path-latency", resumeToken, certificationWeakUserAgent))
	}
	elapsed := time.Since(started)
	if got := opens.Load(); got != warmTurns+1 {
		t.Fatalf("served %d turns, want %d", got, warmTurns+1)
	}
	if row, found := certificationLoadRow(t, database, sessionID); !found || row != persisted {
		t.Fatalf("durable row changed across %d warm turns; hidden per-turn I/O is exactly what this guards (requirement 10.1)", warmTurns)
	}
	observed := hotPathClassificationObservations(t, registry)
	if evaluations := hotPathEvaluationCounts(observed); evaluations != warmTurns+1 {
		t.Fatalf("classification evaluations = %.0f over %d warm turns plus promotion, want one per turn", evaluations, warmTurns+1)
	}
	// Reported for the reviewer; not asserted. The assertion above is the ratchet.
	t.Logf("%d warm composed turns in %s (%.2f ms/turn, 8 CPUs shared); durable state unchanged, one evaluation per turn",
		warmTurns, elapsed.Round(time.Microsecond), float64(elapsed.Microseconds())/float64(warmTurns)/1000)
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// BenchmarkComposedWarmPositiveTurn measures the composed 10.1 hot path and
// asserts the per-iteration durable-write census after the run.
func BenchmarkComposedWarmPositiveTurn(b *testing.B) {
	rt, database, registry := hotPathProcess(b, "classification-hot-path-bench-warm.db")
	generation := certificationCompile(b, rt, true, "")
	if generation.classifier == nil {
		b.Fatal("enabled generation published no classifier")
	}
	generation.start(b)
	executor, opens := certificationExecutor(b, database, certificationPlanesWithConsumer(b, generation.planes))
	sessionID, resumeToken := certificationResumableSession(b, executor, "client-bench-hot-path-warm")
	// SQL-level census over the benchmark's own turns: the strongest available
	// statement that a warm positive performs no durable I/O per turn. It is
	// installed BEFORE the promoting turn so its sensitivity to a write is
	// established by a write it must see.
	statements := &hotPathStatementRecorder{}
	database.AddQueryHook(statements)
	certificationServe(b, executor, certificationCall("client-bench-hot-path-warm", resumeToken, certificationPositiveUserAgent))
	statements.suspend()
	persisted, found := certificationLoadRow(b, database, sessionID)
	statements.resume()
	if !found {
		b.Fatal("the promoting turn persisted no positive")
	}
	if statements.snapshot().writes == 0 {
		b.Fatal("the recorder observed no durable write for the promoting turn; it cannot detect one either")
	}
	statements.reset()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		// A FRESH call per turn, exactly as a real client sends one. Reusing one
		// *lipapi.Call across Execute calls is not a client turn: the executor
		// mutates the call, so later reuses are resolved against a DIFFERENT
		// authoritative session. That is correct proxy behavior (requirement 2.9)
		// and it is why a reused call legitimately issues a durable read for a
		// brand-new session key; it would measure the wrong thing here.
		certificationServe(b, executor,
			certificationCall("client-bench-hot-path-warm", resumeToken, certificationWeakUserAgent))
	}
	b.StopTimer()

	if got := opens.Load(); got != int32(b.N+1) {
		b.Fatalf("served %d turns, want %d", got, b.N+1)
	}
	warm := statements.snapshot()
	if warm.reads != 0 || warm.writes != 0 {
		b.Fatalf("%d warm positive turns issued %d durable reads and %d durable writes (%+v), want zero of each (requirement 10.1)",
			b.N, warm.reads, warm.writes, warm.verbs)
	}
	statements.suspend()
	row, rowFound := certificationLoadRow(b, database, sessionID)
	statements.resume()
	if !rowFound || row != persisted {
		b.Fatal("a warm positive turn rewrote the durable row")
	}
	if remote := hotPathRemoteAttemptCounts(hotPathClassificationObservations(b, registry)); remote != 0 {
		b.Fatalf("remote attempts = %.0f over %d warm positive turns, want none (requirements 6.5, 10.1)", remote, b.N)
	}
}

// BenchmarkComposedDisabledTurn measures the composed 10.8 hot path: the same
// turn on a generation that publishes no classifier plane.
func BenchmarkComposedDisabledTurn(b *testing.B) {
	rt, database, registry := hotPathProcess(b, "classification-hot-path-bench-disabled.db")
	disabled := certificationCompile(b, rt, false, "")
	if disabled.classifier != nil {
		b.Fatalf("disabled generation published classifier %v", disabled.classifier)
	}
	executor, opens := certificationExecutor(b, database, certificationPlanesWithConsumer(b, disabled.planes))
	_, resumeToken := certificationResumableSession(b, executor, "client-bench-hot-path-disabled")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		// A fresh call per turn; see BenchmarkComposedWarmPositiveTurn for why a
		// reused call would measure a different authoritative session.
		certificationServe(b, executor,
			certificationCall("client-bench-hot-path-disabled", resumeToken, certificationPositiveUserAgent))
	}
	b.StopTimer()

	if got := opens.Load(); got != int32(b.N) {
		b.Fatalf("served %d turns, want %d", got, b.N)
	}
	if observed := hotPathClassificationObservations(b, registry); len(observed) != 0 {
		b.Fatalf("disabled generation emitted classification observations during the benchmark: %v", observed)
	}
}
