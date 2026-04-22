package openailegacy

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Guardrail: streaming encode should not regress to per-chunk map+json.Marshal on the hot path.
//
//nolint:paralleltest // allocation budget is sensitive to cross-test scheduling vs httptest/pool state
func TestWriteStreamSSE_AllocBudget_textOnly(t *testing.T) {
	const n = 200
	ctx := context.Background()
	call := &lipapi.Call{}
	opts := EncodeOptions{CompletionID: "chatcmpl_alloc", CreatedAt: 1}
	allocs := testing.AllocsPerRun(5, func() {
		rec := httptest.NewRecorder()
		es := &benchTokenStream{n: n}
		if err := WriteStreamSSE(ctx, rec, call, es, opts); err != nil {
			t.Fatal(err)
		}
	})
	// Loose cap: CI/OS variance; fails on large accidental regressions.
	const maxAllocs = 80_000
	if int(allocs) > maxAllocs {
		t.Fatalf("allocs per run=%g (n=%d deltas), want <= %d", allocs, n, maxAllocs)
	}
}
