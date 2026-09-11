package frontendpipe_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
)

// Task 19.3 / Remediation Phase 0 Characterization Tests:
// Target Invariant: Proof-time transient allocation (B/op) must be bounded by
// memory_spool_bytes (64 KiB) + max_semantic_fact_bytes (256 KiB) + fixed buffers (128 KiB).
//
// These tests are designed to be RED against the current io.ReadAll implementation
// across all three lanes (OpenAI Responses, OpenAI Chat Legacy, OpenResponses),
// and will turn GREEN once Phase 1 streaming proof and Phase 2 lane rework are in place.

const (
	targetInvariantMemorySpoolBytes   = int64(64 << 10)                                                                                      // 64 KiB
	targetInvariantMaxSemanticFact    = int64(256 << 10)                                                                                     // 256 KiB
	targetInvariantFixedBuffersBudget = int64(128 << 10)                                                                                     // 128 KiB
	maxAllowedProofBytesPerOp         = targetInvariantMemorySpoolBytes + targetInvariantMaxSemanticFact + targetInvariantFixedBuffersBudget // 448 KiB (458,752 B)
)

func baselineOpenResponsesBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"stub:bench","store":false,"input":"`
	const suffix = `"}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d smaller than envelope %d", target, len(prefix)+len(suffix))
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("openresponses body len=%d want %d", len(out), target)
	}
	return out
}

func bench20MiBChatBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const numParts = 4
	const prefix = `{"model":"stub:bench","messages":[`
	const msgPrefix = `{"role":"user","content":"`
	const msgSuffix = `"}`
	const suffix = `]}`
	fixedLen := len(prefix) + len(suffix) + numParts*(len(msgPrefix)+len(msgSuffix)) + (numParts - 1)
	totalPad := target - fixedLen
	if totalPad < 0 {
		tb.Fatalf("target %d too small", target)
	}
	padPerPart := totalPad / numParts
	remainder := totalPad % numParts

	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	for i := 0; i < numParts; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(msgPrefix)
		p := padPerPart
		if i == 0 {
			p += remainder
		}
		b.WriteString(strings.Repeat("a", p))
		b.WriteString(msgSuffix)
	}
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("chat chunked body len=%d want %d", len(out), target)
	}
	return out
}

func TestLargePayloadProof_TransientAllocBounded(t *testing.T) {
	type sizeCase struct {
		name   string
		target int
	}

	type laneCase struct {
		laneID      string
		profile     frontendpipe.FrontendProfile
		urlPath     string
		bodyBuilder func(tb testing.TB, target int) []byte
		sizes       []sizeCase
	}

	stdSizes := []sizeCase{
		{name: "1MiB", target: 1 << 20},
	}
	if !testing.Short() {
		stdSizes = append(stdSizes, sizeCase{name: "5MiB", target: 5 << 20})
	}

	fullSizes := []sizeCase{
		{name: "1MiB", target: 1 << 20},
	}
	if !testing.Short() {
		fullSizes = append(fullSizes,
			sizeCase{name: "5MiB", target: 5 << 20},
			sizeCase{name: "20MiB", target: 20 << 20},
		)
	}

	lanes := []laneCase{
		{
			laneID:  "openai_responses",
			profile: openairesponses.NewProfile(),
			urlPath: "/v1/responses",
			sizes:   fullSizes,
			bodyBuilder: func(tb testing.TB, target int) []byte {
				if target > (8 << 20) {
					return bench20MiBChunkedBody(tb, target)
				}
				return baselineResponsesBody(tb, target)
			},
		},
		{
			laneID:  "openai_chat",
			profile: openailegacy.NewProfile(),
			urlPath: "/v1/chat/completions",
			sizes:   fullSizes,
			bodyBuilder: func(tb testing.TB, target int) []byte {
				if target > (8 << 20) {
					return bench20MiBChatBody(tb, target)
				}
				return baselineChatBody(tb, target)
			},
		},
		{
			laneID:      "openresponses",
			profile:     openresponses.NewProfile(),
			urlPath:     "/openresponses/v1/responses",
			sizes:       stdSizes,
			bodyBuilder: baselineOpenResponsesBody,
		},
	}

	for _, lane := range lanes {
		t.Run(lane.laneID, func(t *testing.T) {
			for _, sz := range lane.sizes {
				t.Run(sz.name, func(t *testing.T) {
					body := lane.bodyBuilder(t, sz.target)

					spoolDir := t.TempDir()
					spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
						SpoolDir:         spoolDir,
						MemorySpoolBytes: targetInvariantMemorySpoolBytes,
						CopyBufferSize:   32 << 10,
					})
					require.NoError(t, err, "spill buffer initialization must succeed")

					_, err = spill.Write(body)
					require.NoError(t, err, "spill write must succeed")

					src, err := spill.Complete()
					require.NoError(t, err, "spill complete must succeed")
					t.Cleanup(func() { _ = src.Close() })

					require.True(t, src.HasSpilled(), "expected payload (%d B) to have spilled to disk with memory_spool_bytes=%d", len(body), targetInvariantMemorySpoolBytes)

					proofIn := frontendpipe.ProofInput{
						Ctx:                  context.Background(),
						Headers:              make(http.Header),
						URLPath:              lane.urlPath,
						RouteSelector:        "stub:bench",
						RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub", "openai"}),
						DefaultRouteSelector: "stub:bench",
						RouteFromBodyModel:   true,
						Source:               src,
						BodyBytes:            src.Size(),
					}

					// Measure proof compilation allocations under benchmark harness.
					benchRes := testing.Benchmark(func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for b.Loop() {
							out, perr := lane.profile.CompileProof(b.Context(), proofIn)
							if perr != nil {
								b.Fatalf("CompileProof failed: %v", perr)
							}
							baselineSink = out
						}
					})

					allocBytesPerOp := benchRes.AllocedBytesPerOp()
					allocsPerOp := benchRes.AllocsPerOp()

					t.Logf("[%s/%s] proof-time transient: %d B/op, %d allocs/op (target ceiling: %d B/op)",
						lane.laneID, sz.name, allocBytesPerOp, allocsPerOp, maxAllowedProofBytesPerOp)

					// Target Invariant: Proof-time transient allocations must NOT scale with body size,
					// and must remain strictly bounded by memory_spool_bytes + max_semantic_fact_bytes + fixed_buffers.
					assert.LessOrEqual(t, allocBytesPerOp, maxAllowedProofBytesPerOp,
						"proof-time B/op (%d B) must be bounded by memory_spool_bytes (%d) + max_semantic_fact_bytes (%d) + fixed_buffers (%d) = %d B, but exceeded target invariant by %d B",
						allocBytesPerOp, targetInvariantMemorySpoolBytes, targetInvariantMaxSemanticFact, targetInvariantFixedBuffersBudget, maxAllowedProofBytesPerOp, allocBytesPerOp-maxAllowedProofBytesPerOp)
				})
			}
		})
	}
}
