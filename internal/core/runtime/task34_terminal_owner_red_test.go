package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// Task 3.4 RED: terminal entry points must be owner operations over an
// explicitly captured evidence snapshot and attempt. The old retryRecvStream
// forwarding families do not provide this operation yet, so this test is
// intentionally uncached and fails to compile until the owner seam exists.
func TestTask34TerminalOwner_CommandsAndEffects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  sdkterminal.Command
	}{
		{name: "normal finish", cmd: sdkterminal.CommandNormalFinish},
		{name: "cancel", cmd: sdkterminal.CommandCancel},
		{name: "timeout", cmd: sdkterminal.CommandTimeout},
		{name: "eof", cmd: sdkterminal.CommandEOF},
		{name: "partial error", cmd: sdkterminal.CommandPartialError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			turn := newTurnTerminal()
			attempt := newAttemptSession(attemptSessionInput{})
			snapshot := coreterm.NewAccumulatorSnapshot([]byte(test.name), false)
			var attemptEffects, requestEffects atomic.Int32
			effects := func(context.Context, coreterm.Outcome) error {
				attemptEffects.Add(1)
				return nil
			}
			requestAfter := func(context.Context, coreterm.Outcome) error {
				requestEffects.Add(1)
				return nil
			}

			winner := turn.terminalizeSnapshot(context.Background(), test.cmd, attempt, snapshot, effects, requestAfter)
			if !winner.Won || winner.Err != nil || winner.Outcome.Command != test.cmd {
				t.Fatalf("winner=%+v want winning %s", winner, test.cmd)
			}
			observer := turn.terminalizeSnapshot(context.Background(), test.cmd, attempt, snapshot, effects, requestAfter)
			if observer.Won || observer.Err != nil || observer.Outcome.Command != test.cmd {
				t.Fatalf("same-command observer=%+v want published %s", observer, test.cmd)
			}
			conflict := sdkterminal.CommandEOF
			if conflict == test.cmd {
				conflict = sdkterminal.CommandCancel
			}
			loser := turn.terminalizeSnapshot(context.Background(), conflict, attempt, snapshot, effects, requestAfter)
			if loser.Won || !errors.Is(loser.Err, sdkterminal.ErrConflict) || loser.Outcome.Command != test.cmd {
				t.Fatalf("loser=%+v want conflict observing %s", loser, test.cmd)
			}
			if got := attemptEffects.Load(); got != 1 {
				t.Fatalf("attempt effects=%d want once", got)
			}
			if got := requestEffects.Load(); got != 1 {
				t.Fatalf("request effects=%d want once", got)
			}
		})
	}

	t.Run("swallowed attempt does not claim request", func(t *testing.T) {
		turn := newTurnTerminal()
		attempt := newAttemptSession(attemptSessionInput{})
		snapshot := coreterm.NewAccumulatorSnapshot(nil, false)
		var effects atomic.Int32
		result := turn.terminalizeSnapshot(context.Background(), sdkterminal.CommandSwallowedAttempt, attempt, snapshot, func(context.Context, coreterm.Outcome) error {
			effects.Add(1)
			return nil
		}, nil)
		if !result.Won || result.Err != nil || effects.Load() != 1 {
			t.Fatalf("swallowed result=%+v effects=%d", result, effects.Load())
		}
		if turn.requestTerminal().Owner().State() != sdkterminal.StateOpen {
			t.Fatalf("request state=%q want open", turn.requestTerminal().Owner().State())
		}
	})

	t.Run("committed gate replacement rejects without effects", func(t *testing.T) {
		callID, err := billing.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		var closures atomic.Int32
		executor := &Executor{BillingRuntime: BillingRuntime{
			TerminalUsageSink: testTerminalSink{appendCall: func(context.Context, billing.CallUsageRecord) error {
				closures.Add(1)
				return nil
			}},
			BillingIdentity: testBillingIdentity(),
		}}
		stream := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				aLegID:        "a-task34-gate",
				billingCallID: callID,
				baseline:      lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-task34-gate"}},
			}),
			attempt: testAttemptSlot(
				b2bua.BLegRecord{ALegID: "a-task34-gate", BLegID: "b-task34-gate", Seq: 1},
				routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
				authorityLifecycle{},
			),
		}
		stream = stampStreamIdentity(stream, executor)
		turn := stream.terminal
		attempt := stream.attempt.snapshot()
		turn.markCommitted(attempt)
		var attemptEffects atomic.Int32
		requestEffects := func(ctx context.Context, _ coreterm.Outcome) error {
			stream.terminal.recordBillingLegForAttempt(ctx, stream.facts, attempt, sdkterminal.CommandGateReplacement, lipapi.Event{}, false)
			stream.terminal.handoffBillingTurn(ctx, stream.facts, sdkterminal.CommandGateReplacement)
			return nil
		}
		result := turn.terminalizeSnapshot(context.Background(), sdkterminal.CommandGateReplacement, attempt, coreterm.NewAccumulatorSnapshot(nil, false), func(context.Context, coreterm.Outcome) error {
			attemptEffects.Add(1)
			return nil
		}, requestEffects)
		if result.Won || !errors.Is(result.Err, sdkterminal.ErrOutputCommitted) || attemptEffects.Load() != 0 || closures.Load() != 1 {
			t.Fatalf("gate replacement result=%+v attempt=%d closures=%d", result, attemptEffects.Load(), closures.Load())
		}
		// A competing rejected gate still invokes the request closure seam, but
		// the economic owner deduplicates the already sealed call.
		result = turn.terminalizeSnapshot(context.Background(), sdkterminal.CommandGateReplacement, attempt, coreterm.NewAccumulatorSnapshot(nil, false), nil, func(cctx context.Context, out coreterm.Outcome) error {
			return requestEffects(cctx, out)
		})
		if result.Won || !errors.Is(result.Err, sdkterminal.ErrOutputCommitted) || closures.Load() != 1 {
			t.Fatalf("repeated gate replacement result=%+v closures=%d", result, closures.Load())
		}
	})

	t.Run("captured attempt remains authoritative after replacement", func(t *testing.T) {
		turn := newTurnTerminal()
		old := newAttemptSession(attemptSessionInput{})
		next := newAttemptSession(attemptSessionInput{})
		result := turn.terminalizeSnapshot(context.Background(), sdkterminal.CommandSwallowedAttempt, old, coreterm.NewAccumulatorSnapshot(nil, false), nil, nil)
		if !result.Won || old.terminal.Owner().State() != sdkterminal.StateReleased {
			t.Fatalf("old result=%+v state=%q", result, old.terminal.Owner().State())
		}
		if next.terminal.Owner().State() != sdkterminal.StateOpen {
			t.Fatalf("replacement attempt state=%q want open", next.terminal.Owner().State())
		}
	})
}
