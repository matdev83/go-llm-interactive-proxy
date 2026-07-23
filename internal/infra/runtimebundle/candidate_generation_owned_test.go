package runtimebundle_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

func TestCandidateRuntime_GenerationOwnedClose_DoesNotCloseProcess(t *testing.T) {
	t.Parallel()
	var candidateClosed, processClosed atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.Add("candidate-resource", runtimebundle.PhaseClose, func(context.Context) error {
		candidateClosed.Add(1)
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	processCloser := func() error {
		processClosed.Add(1)
		return nil
	}
	_ = processCloser // process services stay outside generation ownership

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("cand", cand)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	if err := m.Publish(m.Prepare("next")); err != nil {
		t.Fatal(err)
	}
	<-g.Drained()
	if err := g.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if candidateClosed.Load() != 1 {
		t.Fatalf("candidate closes=%d", candidateClosed.Load())
	}
	if processClosed.Load() != 0 {
		t.Fatalf("process closes=%d", processClosed.Load())
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	// CandidateRuntime.Close is idempotent; generation already closed it.
	if candidateClosed.Load() != 1 {
		t.Fatalf("idempotent candidate closes=%d", candidateClosed.Load())
	}
}
