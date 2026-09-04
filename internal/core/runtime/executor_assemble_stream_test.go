package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func TestAssembleExecutorStream_WrapperSelection(t *testing.T) {
	t.Parallel()

	sel, err := routing.Parse("exec:m")
	if err != nil {
		t.Fatal(err)
	}
	newPlan := func() *routePlanState {
		return &routePlanState{
			routeFacts: routeFacts{sel: sel},
			progress: newRecoveryController(recoveryControllerInput{
				budget: &attemptBudget{max: 3},
				sel:    sel,
			}),
		}
	}
	newPrep := func() *preparedRequest {
		call := &lipapi.Call{
			Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions},
			Session: lipapi.SessionRef{
				ALegID: "a-1",
			},
		}
		preSession := session.SessionView{
			ALegID: "a-1",
		}
		ibt, err := newIdentityBoundTurn(
			"assemble-test",
			call,
			execview.PrincipalView{},
			scope.PrincipalScopeView{},
			false,
			lipworkspace.WorkspaceView{},
			b2bua.ALegRecord{ALegID: "a-1"},
			routeAuthoritySnapshot{},
			execctx.SecureSessionTurn{},
			false,
			preSession,
		)
		if err != nil {
			panic(err)
		}
		return &preparedRequest{
			identity: ibt,
			call:     ibt.call,
			guard:    &preStreamGuard{},
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
	out := openedAttempt{
		ready: newReadyAttempt(&attemptSession{
			inner: stream,
			cand:  plainCand,
		}, pendingSelectionEffects{}),
	}

	t.Run("plain", func(t *testing.T) {
		t.Parallel()
		localPrep := newPrep()
		ex := TestExecutor()
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, newPlan(), out)
		if err != nil {
			t.Fatal(err)
		}
		if !localPrep.guard.handedOver {
			t.Fatal("guard must be handed over")
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
		hiddenOut.ready = newReadyAttempt(&attemptSession{
			inner: stream,
			cand:  thinkerCand,
		}, pendingSelectionEffects{})
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, newPlan(), hiddenOut)
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
		visibleOut.ready = newReadyAttempt(&attemptSession{
			inner: stream,
			cand:  thinkerCand,
		}, pendingSelectionEffects{})
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, newPlan(), visibleOut)
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
