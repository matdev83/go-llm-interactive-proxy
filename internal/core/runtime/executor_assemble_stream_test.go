package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAssembleExecutorStream_WrapperSelection(t *testing.T) {
	t.Parallel()

	sel, err := routing.Parse("exec:m")
	if err != nil {
		t.Fatal(err)
	}
	plan := &routePlanState{
		sel:    sel,
		budget: &attemptBudget{max: 3},
	}
	newPrep := func() *preparedRequest {
		return &preparedRequest{
			traceID:  "assemble-test",
			baseline: lipapi.Call{Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
		}
	}
	stream := lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}})
	thinkerCand := routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "thinker", Model: "m"},
		InterleavedRole: interleavedstate.RoleThinker,
	}
	plainCand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "exec", Model: "m"},
	}
	out := attemptOpenResult{
		opened: true,
		stream: stream,
		cand:   plainCand,
	}

	t.Run("plain", func(t *testing.T) {
		t.Parallel()
		localPrep := newPrep()
		ex := TestExecutor()
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, plan, out)
		if err != nil {
			t.Fatal(err)
		}
		if !localPrep.streamReturned {
			t.Fatal("streamReturned must be set")
		}
		if _, ok := got.(*retryRecvStream); !ok {
			t.Fatalf("want *retryRecvStream, got %T", got)
		}
	})

	t.Run("hidden interleaved", func(t *testing.T) {
		t.Parallel()
		localPrep := newPrep()
		ex := TestExecutor()
		ex.MemoStore = interleavedthinking.NewMemoStore(1024)
		ex.InterleavedConfig = interleavedthinking.ShapeConfig{StreamToClient: "hidden"}
		hiddenOut := out
		hiddenOut.cand = thinkerCand
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, plan, hiddenOut)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(*hiddenInterleavedStream); !ok {
			t.Fatalf("want *hiddenInterleavedStream, got %T", got)
		}
	})

	t.Run("visible interleaved", func(t *testing.T) {
		t.Parallel()
		localPrep := newPrep()
		ex := TestExecutor()
		ex.MemoStore = interleavedthinking.NewMemoStore(1024)
		ex.InterleavedConfig = interleavedthinking.ShapeConfig{StreamToClient: "visible"}
		visibleOut := out
		visibleOut.cand = thinkerCand
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, plan, visibleOut)
		if err != nil {
			t.Fatal(err)
		}
		s, ok := got.(*interleavedContinuationStream)
		if !ok {
			t.Fatalf("want *interleavedContinuationStream, got %T", got)
		}
		if !s.surfaceVisible {
			t.Fatal("visible wrapper must set surfaceVisible")
		}
	})
}
