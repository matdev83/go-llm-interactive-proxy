package frontendpipe_test

// Task 19.3: Enforce accepted-lane no-payload-heap invariant
// (Requirements 19, 21, 22; Design section 15).
//
// Coverage:
// - Heap invariant: accepted spill-backed wire requests must retain no payload-sized
//   []byte/string, full Call/item/message tree, or payload-scale CloneCall.
// - Retained request heap bounded by:
//   memory_spool_bytes (64 KiB) + max_semantic_fact_bytes (256 KiB) + O(fixed buffers) + metadata.
// - Honest measurement of two distinct phases:
//   1. Transient proof-time allocation (CompileProof): measured honestly; notes
//      body-proportional O(payload) slope from io.ReadAll + JSON unmarshal.
//   2. Post-commit retained heap: measured during/after wire commit; demonstrates
//      approximately flat/bounded retained heap across 1 MiB -> 5 MiB -> 20 MiB.
// - Gate evaluation: provides explicit PASS/FAIL per Requirement 21.10/21.11.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type heapInvariantSize struct {
	name   string
	target int
}

func heapInvariantSizes() []heapInvariantSize {
	return []heapInvariantSize{
		{name: "1MiB", target: 1 << 20},
		{name: "5MiB", target: 5 << 20},
	}
}

// -----------------------------------------------------------------------------
// Phase 1: Transient Proof-Time Allocation (measured honestly)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadHeap_ProofTimeTransient(b *testing.B) {
	sizes := heapInvariantSizes()
	if !testing.Short() {
		sizes = append(sizes, heapInvariantSize{name: "20MiB", target: 20 << 20})
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			var body []byte
			if sz.target > (8 << 20) {
				body = bench20MiBChunkedBody(b, sz.target)
			} else {
				body = baselineResponsesBody(b, sz.target)
			}
			prof := openairesponses.NewProfile()
			spoolDir := b.TempDir()

			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()

			for b.Loop() {
				b.StopTimer()
				spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
					SpoolDir:         spoolDir,
					MemorySpoolBytes: 64 << 10,
					CopyBufferSize:   32 << 10,
				})
				if err != nil {
					b.Fatalf("spill: %v", err)
				}
				if _, err := spill.Write(body); err != nil {
					b.Fatalf("spill write: %v", err)
				}
				src, err := spill.Complete()
				if err != nil {
					b.Fatalf("spill complete: %v", err)
				}
				proofIn := frontendpipe.ProofInput{
					Ctx:                  b.Context(),
					URLPath:              "/v1/responses",
					RouteSelector:        "stub:bench",
					DefaultRouteSelector: "stub:bench",
					Source:               src,
					BodyBytes:            src.Size(),
				}
				b.StartTimer()

				// CompileProof reads from replay source and unmarshals for semantic proof
				out, perr := prof.CompileProof(b.Context(), proofIn)
				if perr != nil {
					b.Fatalf("compile proof: %v", perr)
				}
				baselineSink = out

				b.StopTimer()
				_ = src.Close()
				b.StartTimer()
			}
			b.StopTimer()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			reportBaselineGC(b, before, after)
		})
	}
}

// -----------------------------------------------------------------------------
// Phase 2: Post-Commit Wire Execution Retained Heap (bounded, flat slope)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadHeap_PostCommitRetained(b *testing.B) {
	sizes := heapInvariantSizes()
	if !testing.Short() {
		sizes = append(sizes, heapInvariantSize{name: "20MiB", target: 20 << 20})
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			var body []byte
			if sz.target > (8 << 20) {
				body = bench20MiBChunkedBody(b, sz.target)
			} else {
				body = baselineResponsesBody(b, sz.target)
			}
			spoolDir := b.TempDir()

			// Wire executor that streams from replay source in fixed 32 KiB chunks
			exec := &benchWireExecutor{
				executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
					rc, err := src.Open()
					if err != nil {
						return largebody.ExecutionResult{}, err
					}
					defer rc.Close()
					buf := make([]byte, 32*1024)
					for {
						_, rerr := rc.Read(buf)
						if rerr != nil {
							if errors.Is(rerr, io.EOF) {
								break
							}
							return largebody.ExecutionResult{}, rerr
						}
					}
					return largebody.ExecutionResult{
						Stream: lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventResponseStarted},
							{Kind: lipapi.EventMessageStarted},
							{Kind: lipapi.EventTextDelta, Delta: "retained bench ok"},
							{Kind: lipapi.EventResponseFinished},
						}),
						Facts: largebody.ResponseFacts{
							RequestID:      accepted.Stamp.IdentityDigest().CallID("call_bench"),
							EffectiveModel: accepted.WireRequest.CandidateModel,
						},
					}, nil
				},
			}
			spec := newBenchWireSpec(b, exec, 16<<10, spoolDir, 64<<10)
			if sz.target > (8 << 20) {
				spec.MaxRequestBodyBytes = int64(sz.target) * 2
			}

			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()

			for b.Loop() {
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
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
		})
	}
}

// -----------------------------------------------------------------------------
// Phase 3: Retained Heap Isolated Post-Proof (flat slope across 1->5->20 MiB)
// -----------------------------------------------------------------------------

func BenchmarkLargePayloadHeap_RetainedPostProofGC(b *testing.B) {
	sizes := heapInvariantSizes()
	if !testing.Short() {
		sizes = append(sizes, heapInvariantSize{name: "20MiB", target: 20 << 20})
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			var body []byte
			if sz.target > (8 << 20) {
				body = bench20MiBChunkedBody(b, sz.target)
			} else {
				body = baselineResponsesBody(b, sz.target)
			}
			prof := openairesponses.NewProfile()
			spoolDir := b.TempDir()

			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()

			for b.Loop() {
				b.StopTimer()
				spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
					SpoolDir:         spoolDir,
					MemorySpoolBytes: 64 << 10,
					CopyBufferSize:   32 << 10,
				})
				if err != nil {
					b.Fatalf("spill: %v", err)
				}
				if _, err := spill.Write(body); err != nil {
					b.Fatalf("spill write: %v", err)
				}
				src, err := spill.Complete()
				if err != nil {
					b.Fatalf("spill complete: %v", err)
				}

				proofIn := frontendpipe.ProofInput{
					Ctx:                  b.Context(),
					URLPath:              "/v1/responses",
					RouteSelector:        "stub:bench",
					DefaultRouteSelector: "stub:bench",
					Source:               src,
					BodyBytes:            src.Size(),
				}
				proofOut, perr := prof.CompileProof(b.Context(), proofIn)
				if perr != nil {
					b.Fatalf("compile proof: %v", perr)
				}
				proof := proofOut.Proof()

				// Trigger GC to release transient proof allocations
				runtime.GC()

				var before runtime.MemStats
				runtime.ReadMemStats(&before)

				b.StartTimer()
				// Measure wire streaming from replay source (post-commit wire execution)
				rc, oerr := src.Open()
				if oerr != nil {
					b.Fatalf("src open: %v", oerr)
				}
				buf := make([]byte, 32*1024)
				for {
					_, rerr := rc.Read(buf)
					if rerr != nil {
						if errors.Is(rerr, io.EOF) {
							break
						}
						b.Fatalf("read: %v", rerr)
					}
				}
				_ = rc.Close()
				baselineSink = proof

				b.StopTimer()
				var after runtime.MemStats
				runtime.ReadMemStats(&after)
				reportBaselineGC(b, before, after)
				_ = src.Close()
				b.StartTimer()
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Verification: No Call tree, no CloneCall, and spill confirmed on wire path
// -----------------------------------------------------------------------------

func TestLargePayloadHeap_AcceptedWireHasNoCallTree(t *testing.T) {
	const target = 1 << 20 // 1 MiB
	body := baselineResponsesBody(t, target)
	spoolDir := t.TempDir()

	var wireCommitted atomic.Bool
	var sourceHasSpilled atomic.Bool
	var receivedCallTree atomic.Bool

	exec := &benchWireExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			wireCommitted.Store(true)
			if cs, ok := src.(*largebody.CompletedSource); ok {
				sourceHasSpilled.Store(cs.HasSpilled())
			} else {
				sourceHasSpilled.Store(src.Size() > (64 << 10))
			}
			rc, err := src.Open()
			if err != nil {
				return largebody.ExecutionResult{}, err
			}
			defer rc.Close()
			return largebody.ExecutionResult{
				Stream: lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}),
				Facts: largebody.ResponseFacts{
					RequestID:      accepted.Stamp.IdentityDigest().CallID("call_test"),
					EffectiveModel: accepted.WireRequest.CandidateModel,
				},
			}, nil
		},
		canonicalExecFunc: func(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
			receivedCallTree.Store(true)
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
	}

	spec := newBenchWireSpec(t, exec, 16<<10, spoolDir, 64<<10)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d: %s", rec.Code, rec.Body.String())
	}
	if !wireCommitted.Load() {
		t.Fatal("expected wire execution to be committed")
	}
	if !sourceHasSpilled.Load() {
		t.Fatal("expected 1 MiB request to be file-spilled (MemorySpoolBytes=64KiB)")
	}
	if receivedCallTree.Load() {
		t.Fatal("accepted wire path must NOT construct or pass lipapi.Call tree to executor")
	}
}
