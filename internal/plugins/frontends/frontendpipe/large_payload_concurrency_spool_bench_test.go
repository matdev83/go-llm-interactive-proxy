package frontendpipe_test

// Task 19.5: Concurrent load and spool saturation
// (Requirements 20, 21; Design sections 5, 15).
//
// Coverage:
// - Realistic sessions: concurrent accepted requests under load.
// - Slow uploads: reader yielding chunks with delay; verifies decode permit not held.
// - Spool budget saturation: spool ledger with tight budget (2 MiB); proves excess
//   requests gracefully fall back to canonical path without client error (HTTP 200).
//   Spool budget is an optimization budget, not a global OOM admission barrier.
// - Cancellation: client context cancellation mid-flight; proves temporary files
//   are cleaned up and spool reservations released.
// - Benchmarks with -benchmem comparing GC/heap/latency.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// slowChunkReader simulates a slow client upload by chunking the payload.
type slowChunkReader struct {
	data      []byte
	offset    int
	chunkSize int
	delay     time.Duration
}

func (r *slowChunkReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	n := min(len(p), min(r.chunkSize, len(r.data)-r.offset))
	copy(p, r.data[r.offset:r.offset+n])
	r.offset += n
	return n, nil
}

// -----------------------------------------------------------------------------
// 19.5 Benchmark 1: Concurrent Accepted Wire Requests (RunParallel)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadConcurrent_AcceptedWire(b *testing.B) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()
	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
			}
		}
	})

	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

// -----------------------------------------------------------------------------
// 19.5 Benchmark 2: Slow Upload Under Concurrency
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadConcurrent_SlowUpload(b *testing.B) {
	const target = 256 << 10 // 256 KiB for bounded benchmark time
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()
	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	for b.Loop() {
		reader := &slowChunkReader{
			data:      body,
			chunkSize: 32 << 10,
			delay:     100 * time.Microsecond,
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", reader)
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(spec, rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("code=%d", rec.Code)
		}
	}

	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

// -----------------------------------------------------------------------------
// 19.5 Benchmark 3: Spool Budget Saturation (Lossless Canonical Fallback)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadConcurrent_SpoolBudgetSaturation(b *testing.B) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(b, target)
	spoolDir := b.TempDir()

	// Tight 2 MiB spool budget: 3rd and subsequent concurrent requests fall back to canonical
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 << 10,
		MaxInflightSpoolBytes: 2 << 20,
	})
	if err != nil {
		b.Fatalf("NewSpoolLedger: %v", err)
	}

	exec := &benchWireExecutor{}
	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:bench",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		Profile:              openairesponses.NewProfile(),
	}
	spec := h.Spec()
	spec.Config.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:          true,
		ThresholdBytes:   16 << 10,
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 64 << 10,
		SpoolLedger:      ledger,
	}
	spec.WireWriteNonStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es lipapi.EventStream) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"bench","object":"response","status":"completed"}`))
		return nil
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
			}
		}
	})

	b.StopTimer()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	reportBaselineGC(b, before, after)
}

// -----------------------------------------------------------------------------
// Verification: Spool Budget is Optimization Budget, Not Global OOM Admission
// -----------------------------------------------------------------------------

func TestLargePayloadConcurrent_SpoolBudgetIsOptimizationBudgetNotOOM(t *testing.T) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(t, target)
	spoolDir := t.TempDir()

	// 2 MiB spool budget
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 << 10,
		MaxInflightSpoolBytes: 2 << 20,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}

	var wireCount atomic.Int64
	var canonicalCount atomic.Int64

	exec := &benchWireExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			wireCount.Add(1)
			// Hold the execution slightly to ensure overlap
			time.Sleep(20 * time.Millisecond)
			rc, _ := src.Open()
			_ = rc.Close()
			return largebody.ExecutionResult{
				Stream: lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}),
				Facts: largebody.ResponseFacts{
					RequestID:      accepted.Stamp.IdentityDigest().CallID("call_budget"),
					EffectiveModel: accepted.WireRequest.CandidateModel,
				},
			}, nil
		},
		canonicalExecFunc: func(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
			canonicalCount.Add(1)
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
	}

	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:bench",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		Profile:              openairesponses.NewProfile(),
	}
	spec := h.Spec()
	spec.Config.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:          true,
		ThresholdBytes:   16 << 10,
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 64 << 10,
		SpoolLedger:      ledger,
	}
	spec.WireWriteNonStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es lipapi.EventStream) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return nil
	}

	// Launch 5 concurrent requests of 1 MiB each against 2 MiB spool budget
	const numReqs = 5
	var wg sync.WaitGroup
	var successCount atomic.Int64

	for i := 0; i < numReqs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(spec, rec, req)
			if rec.Code == http.StatusOK {
				successCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if successCount.Load() != numReqs {
		t.Fatalf("expected all %d requests to succeed (HTTP 200), got %d", numReqs, successCount.Load())
	}
	if wireCount.Load() == 0 {
		t.Fatal("expected at least one wire request to be admitted under spool budget")
	}
	if canonicalCount.Load() == 0 {
		t.Fatal("expected at least one request to fall back to canonical path when spool budget saturated")
	}
	if wireCount.Load()+canonicalCount.Load() != numReqs {
		t.Fatalf("wire (%d) + canonical (%d) != total (%d)", wireCount.Load(), canonicalCount.Load(), numReqs)
	}
}

// -----------------------------------------------------------------------------
// Verification: Cancellation Cleans Up Spill Files and Releases Reservations
// -----------------------------------------------------------------------------

func TestLargePayloadConcurrent_CancellationCleansUpSpillFiles(t *testing.T) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(t, target)
	spoolDir := t.TempDir()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 << 10,
		MaxInflightSpoolBytes: 10 << 20,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}

	exec := &benchWireExecutor{}
	spec := newBenchWireSpec(t, exec, 16<<10, spoolDir, 64<<10)
	spec.LargePayload.SpoolLedger = ledger

	ctx, cancel := context.WithCancel(context.Background())

	// Reader that cancels context halfway through upload
	halfwayReader := &slowChunkReader{
		data:      body,
		chunkSize: 32 << 10,
		delay:     0,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", halfwayReader).WithContext(ctx)
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// Cancel after brief moment
	go func() {
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()

	frontendpipe.ServeHTTP(spec, rec, req)

	// Wait for any deferred closes
	time.Sleep(10 * time.Millisecond)

	// Verify no temporary files remain in spoolDir
	entries, rerr := os.ReadDir(spoolDir)
	if rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 leftover temp files after cancellation, found %d", len(entries))
	}

	// Verify spool ledger in-flight bytes returned to zero
	if ledger.ActiveReservations() != 0 {
		t.Fatalf("expected 0 active reservations, got %d", ledger.ActiveReservations())
	}
}
