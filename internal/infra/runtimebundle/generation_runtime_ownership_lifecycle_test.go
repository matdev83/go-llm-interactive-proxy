package runtimebundle_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// TestOwnership_TransferDetachesCandidateFromLedger proves successful
// CompileGeneration transfers ledger ownership so an accidental candidate Close
// cannot release generation resources; the generation runtime closes them once.
func TestOwnership_TransferDetachesCandidateFromLedger(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	life := &overlapLife{}
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "own", "own-text", "own:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{FeatureLifecycles: []lipplugin.Lifecycle{life}},
		Compose:       stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}

	assertNoCandidateOwnerField(t, bundle.(*runtimebundle.GenerationBundle))

	// Direct transfer proof against a detached candidate handle.
	ledger := runtimebundle.NewResourceLedger()
	var closed atomic.Int32
	_ = ledger.AddClose("probe", runtimebundle.PhaseClose, func() error {
		closed.Add(1)
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	transferred := runtimebundle.TransferLedgerOwnershipForTest(cand)
	if transferred == nil {
		t.Fatal("TransferLedgerOwnershipForTest must return the ledger")
	}
	if err := cand.Close(); err != nil {
		t.Fatalf("post-transfer candidate Close: %v", err)
	}
	if err := cand.Quiesce(context.Background()); err != nil {
		t.Fatalf("post-transfer candidate Quiesce: %v", err)
	}
	if closed.Load() != 0 {
		t.Fatalf("candidate Close after transfer closed ledger entries=%d", closed.Load())
	}

	owned := runtimebundle.NewGenerationBundleWithLedgerForTest(transferred)
	if err := owned.Close(); err != nil {
		t.Fatalf("generation Close: %v", err)
	}
	if closed.Load() != 1 {
		t.Fatalf("generation Close closed=%d want 1", closed.Load())
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("idempotent generation Close: %v", err)
	}
	if closed.Load() != 1 {
		t.Fatalf("doubled generation Close closed=%d", closed.Load())
	}

	// Compiled bundle remains the sole owner for its lifecycle probe.
	if life.starts.Load() != 1 {
		t.Fatalf("compiled starts=%d", life.starts.Load())
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("compiled stops=%d want 1", life.stops.Load())
	}
	if ps.Closed() {
		t.Fatal("process must survive")
	}
}

// TestOwnership_SingularOwnerShape rejects dual lifecycle owners on the concrete runtime.
func TestOwnership_SingularOwnerShape(t *testing.T) {
	t.Parallel()
	elem := reflect.TypeOf((*runtimebundle.GenerationBundle)(nil)).Elem()
	forbiddenNames := map[string]bool{
		"owner": true, "Owner": true,
		"candidate": true, "Candidate": true, "cand": true,
		"quiesceOnce": true, "closeOnce": true,
		"process": true, "Process": true, "processCloser": true,
		"cfg": true, "config": true, "built": true, "Built": true,
		"app": true, "App": true, "requestPlane": true, "RequestPlane": true,
		"deps": true, "dependencies": true, "dependencyMap": true,
	}
	groupNames := map[string]bool{
		"execution": true, "publication": true, "models": true,
		"operations": true, "ownership": true,
	}
	foundGroups := map[string]bool{}
	for i := 0; i < elem.NumField(); i++ {
		f := elem.Field(i)
		if forbiddenNames[f.Name] {
			t.Fatalf("GenerationBundle retains forbidden dual-owner/mutable field %q (%s)", f.Name, f.Type.String())
		}
		if stringsHasCandidateRuntime(f.Type.String()) {
			t.Fatalf("GenerationBundle must not retain CandidateRuntime field %q (%s)", f.Name, f.Type.String())
		}
		if groupNames[f.Name] {
			foundGroups[f.Name] = true
			if f.Type.Kind() != reflect.Struct {
				t.Fatalf("group field %q must be a private struct, got %s", f.Name, f.Type.Kind())
			}
			if f.IsExported() {
				t.Fatalf("group field %q must be unexported", f.Name)
			}
		}
	}
	for name := range groupNames {
		if !foundGroups[name] {
			t.Fatalf("GenerationBundle missing cohesive group field %q", name)
		}
	}
}

func assertNoCandidateOwnerField(t *testing.T, bundle *runtimebundle.GenerationBundle) {
	t.Helper()
	elem := reflect.TypeOf(bundle).Elem()
	for i := 0; i < elem.NumField(); i++ {
		f := elem.Field(i)
		if f.Name == "owner" || stringsHasCandidateRuntime(f.Type.String()) {
			t.Fatalf("compiled runtime still exposes owner/candidate field %q (%s)", f.Name, f.Type.String())
		}
	}
}

func stringsHasCandidateRuntime(typeName string) bool {
	return containsToken(typeName, "CandidateRuntime")
}

func containsToken(s, tok string) bool {
	return len(s) >= len(tok) && (s == tok ||
		(len(s) > len(tok) && (indexOf(s, tok) >= 0)))
}

func indexOf(s, tok string) int {
	for i := 0; i+len(tok) <= len(s); i++ {
		if s[i:i+len(tok)] == tok {
			return i
		}
	}
	return -1
}

// TestLifecycle_CloseBeforeQuiesceRollsBackOnce verifies unpublished rollback
// ordering and singular ownership of the ledger phases.
func TestLifecycle_CloseBeforeQuiesceRollsBackOnce(t *testing.T) {
	t.Parallel()
	var order []string
	var mu sync.Mutex
	track := func(name string) func() error {
		return func() error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("a", runtimebundle.PhaseClose, track("close:a"))
	_ = ledger.AddClose("b", runtimebundle.PhaseQuiesce, track("quiesce:b"))
	_ = ledger.AddClose("c", runtimebundle.PhaseClose, track("close:c"))
	b := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"close:c", "quiesce:b", "close:a"}
	if len(got) != len(want) {
		t.Fatalf("order=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order[%d]=%q want %q (full=%v)", i, got[i], want[i], got)
		}
	}
	if err := b.Quiesce(context.Background()); err != nil {
		t.Fatalf("Quiesce after Close: %v", err)
	}
	mu.Lock()
	if len(order) != len(want) {
		t.Fatalf("Quiesce after Close restarted work: %v", order)
	}
	mu.Unlock()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestLifecycle_QuiesceThenClosePhaseOrdering verifies quiesce-only then close phases.
func TestLifecycle_QuiesceThenClosePhaseOrdering(t *testing.T) {
	t.Parallel()
	var order []string
	var mu sync.Mutex
	track := func(name string) func() error {
		return func() error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("worker", runtimebundle.PhaseQuiesce, track("quiesce"))
	_ = ledger.AddClose("backend", runtimebundle.PhaseClose, track("close"))
	b := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	if err := b.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(order) != 1 || order[0] != "quiesce" {
		t.Fatalf("after Quiesce order=%v", order)
	}
	mu.Unlock()

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[1] != "close" {
		t.Fatalf("after Close order=%v", order)
	}
}

// TestLifecycle_RepeatedCallsStableErrors proves cleanup errors stay joined/stable.
func TestLifecycle_RepeatedCallsStableErrors(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("boom", runtimebundle.PhaseClose, func() error {
		return errors.New("close-boom")
	})
	b := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)
	err1 := b.Close()
	if err1 == nil || !stringsContains(err1.Error(), "close-boom") {
		t.Fatalf("err1=%v", err1)
	}
	err2 := b.Close()
	if err2.Error() != err1.Error() {
		t.Fatalf("unstable close err: %v vs %v", err1, err2)
	}
	err3 := b.Quiesce(context.Background())
	if err3.Error() != err1.Error() {
		t.Fatalf("Quiesce after Close terminal=%v want %v", err3, err1)
	}
}

// TestLifecycle_ConcurrentQuiesceCloseUnderRace stresses singular ledger ownership.
func TestLifecycle_ConcurrentQuiesceCloseUnderRace(t *testing.T) {
	t.Parallel()
	var quiesced, closed atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesced.Add(1)
		return nil
	})
	_ = ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
		closed.Add(1)
		return nil
	})
	b := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = b.Quiesce(context.Background())
		}()
		go func() {
			defer wg.Done()
			_ = b.Close()
		}()
	}
	wg.Wait()

	if quiesced.Load() != 1 {
		t.Fatalf("quiesce ran %d times, want exactly 1", quiesced.Load())
	}
	if closed.Load() != 1 {
		t.Fatalf("close phase ran %d times, want exactly 1", closed.Load())
	}
}

// TestLifecycle_QuiesceBlocksCloseUntilReleased proves Close cannot run
// close/rollback work while Quiesce holds the in-progress critical section,
// then close runs exactly once after Quiesce releases.
func TestLifecycle_QuiesceBlocksCloseUntilReleased(t *testing.T) {
	t.Parallel()
	var quiesced, closed atomic.Int32
	quiesceEntered := make(chan struct{})
	releaseQuiesce := make(chan struct{})
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		close(quiesceEntered)
		<-releaseQuiesce
		quiesced.Add(1)
		return nil
	})
	_ = ledger.AddClose("be", runtimebundle.PhaseClose, func() error {
		closed.Add(1)
		return nil
	})
	b := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	quiesceDone := make(chan error, 1)
	go func() {
		quiesceDone <- b.Quiesce(context.Background())
	}()
	select {
	case <-quiesceEntered:
	case err := <-quiesceDone:
		t.Fatalf("Quiesce returned before entering cleanup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Quiesce to enter cleanup")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- b.Close()
	}()

	// Close must block until Quiesce releases; close/rollback work must not run yet.
	select {
	case err := <-closeDone:
		t.Fatalf("Close finished while Quiesce still held cleanup: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	if closed.Load() != 0 {
		t.Fatalf("close/rollback work ran while Quiesce in progress: closed=%d", closed.Load())
	}
	if quiesced.Load() != 0 {
		t.Fatalf("quiesce cleanup completed before release: quiesced=%d", quiesced.Load())
	}

	close(releaseQuiesce)
	select {
	case err := <-quiesceDone:
		if err != nil {
			t.Fatalf("Quiesce: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Quiesce to finish")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Close to finish")
	}
	if quiesced.Load() != 1 {
		t.Fatalf("quiesced=%d want 1", quiesced.Load())
	}
	if closed.Load() != 1 {
		t.Fatalf("closed=%d want 1", closed.Load())
	}
}

// TestLifecycle_ZeroValueConcurrentFirstUseRaceSafe proves concurrent first
// Quiesce/Close on a zero-value GenerationBundle is race-safe under -race.
func TestLifecycle_ZeroValueConcurrentFirstUseRaceSafe(t *testing.T) {
	t.Parallel()
	const goroutines = 64
	for n := 0; n < 8; n++ {
		b := &runtimebundle.GenerationBundle{}
		var wg sync.WaitGroup
		wg.Add(goroutines * 2)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				_ = b.Quiesce(context.Background())
			}()
			go func() {
				defer wg.Done()
				_ = b.Close()
			}()
		}
		wg.Wait()
	}
}

// TestLifecycle_PanicErrorCleanupAggregationJoinsErrors covers cleanup aggregation.
func TestLifecycle_PanicErrorCleanupAggregationJoinsErrors(t *testing.T) {
	t.Parallel()
	ledger := runtimebundle.NewResourceLedger()
	_ = ledger.AddClose("panic", runtimebundle.PhaseClose, func() error {
		panic("cleanup-panic")
	})
	_ = ledger.AddClose("err", runtimebundle.PhaseClose, func() error {
		return errors.New("cleanup-err")
	})
	b := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)
	err := b.Close()
	if err == nil {
		t.Fatal("expected joined cleanup errors")
	}
	msg := err.Error()
	if !stringsContains(msg, "cleanup-err") || !stringsContains(msg, "panic") {
		t.Fatalf("joined err missing pieces: %v", err)
	}
	if err2 := b.Close(); err2.Error() != err.Error() {
		t.Fatalf("unstable aggregate: %v vs %v", err, err2)
	}
}

// TestLifecycle_ProcessOwnerRemainsOpen ensures process outlives generation teardown.
func TestLifecycle_ProcessOwnerRemainsOpen(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "life", "life-text", "life:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if ps.Closed() {
		t.Fatal("ProcessServices must remain open")
	}
}

func stringsContains(s, sub string) bool {
	return indexOf(s, sub) >= 0
}
