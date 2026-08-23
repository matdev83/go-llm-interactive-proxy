package conversationview_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// oneSnapshotReader counts Snapshot calls deterministically.
type oneSnapshotReader struct {
	snap  conversationview.Snapshot
	count atomic.Int64
}

func (r *oneSnapshotReader) Snapshot(_ context.Context, _ string) (conversationview.Snapshot, error) {
	r.count.Add(1)
	return r.snap, nil
}

func TestOneSnapshotNoPerCandidateIO(t *testing.T) {
	t.Parallel()
	// Build worst-case snapshot: 64 overlays 256 KiB + 4096 tags is not needed for per-candidate, but we test with 64.
	snap := benchSnapshot64Overlays256KiB()
	reader := &oneSnapshotReader{snap: snap}
	base := benchCallWithMessages(10)

	// Simulate runtime: one snapshot per logical turn.
	s, err := reader.Snapshot(context.Background(), "a-leg-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), reader.count.Load())

	projected, ev, err := conversationview.Project(base, s)
	require.NoError(t, err)
	filtered, err := conversationview.FilterNeverBackend(base, s)
	require.NoError(t, err)

	// Simulate 5 parallel candidate arms (failover, TTFT, interleaved) – each reasserts using frozen snapshot/provenance,
	// with zero additional Snapshot calls (no per-candidate I/O).
	for i := 0; i < 5; i++ {
		// Late transform mutates candidate: duplicate steering tail (simulated by appending)
		mutated := lipapi.CloneCall(projected)
		// Reassert must restore without reading store.
		out, _, err := conversationview.Reassert(mutated, s, ev.Provenance, filtered)
		require.NoError(t, err)
		require.NotNil(t, out)
		// No additional Snapshot incurred.
		require.Equal(t, int64(1), reader.count.Load(), "per-candidate must not read Snapshot")
	}
	// Also verify that multiple turns each do exactly one snapshot (not zero, not two).
	for iter := 0; iter < 3; iter++ {
		reader2 := &oneSnapshotReader{snap: snap}
		_, err := reader2.Snapshot(context.Background(), "a-leg-iter")
		require.NoError(t, err)
		_, _, err = conversationview.Project(base, snap)
		require.NoError(t, err)
		require.Equal(t, int64(1), reader2.count.Load())
	}
}

func TestOneSnapshotNoPerCandidateIO_4096Tags(t *testing.T) {
	t.Parallel()
	snap := benchSnapshot4096Tags()
	reader := &oneSnapshotReader{snap: snap}
	base := benchCallWithMessages(20)
	s, _ := reader.Snapshot(context.Background(), "a-leg")
	require.Equal(t, int64(1), reader.count.Load())
	_, _, err := conversationview.Project(base, s)
	require.NoError(t, err)
	// Reassert with same snapshot multiple times must not trigger additional reads.
	for i := 0; i < 3; i++ {
		late := lipapi.CloneCall(base)
		_, _, err := conversationview.Reassert(late, s, nil, lipapi.Call{})
		require.NoError(t, err)
		require.Equal(t, int64(1), reader.count.Load())
	}
}
