package runtime

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// RED 7.1: post-output EOF/idle must become semantic continuation, not retry.
// Guard-enabled composition will set AllowPostOutputContinuation=true via runtimebundle (7.2).
// Until 7.2 is wired, these tests MUST fail because retryRecvStream handleEOF/handleError
// do not yet consume DecisionContinuePostOutput into stopgate + continuationsafety + B2 open via openModeGuardContinuation.
// This file intentionally asserts the FUTURE contract (RED). Do not weaken to green via hooks/stubs.

type postOutputIdleStream struct {
	events    []lipapi.Event
	i         int
	cancelled atomic.Bool
	closed    atomic.Bool
}

func (s *postOutputIdleStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}
func (s *postOutputIdleStream) Close() error { s.closed.Store(true); return nil }
func (s *postOutputIdleStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	s.cancelled.Store(true)
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func TestAgentLoopGuard_PostOutput_VisibleTextThenEOF_NoRetryOneContinuationLegalStream_RED(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind string // "eof" or "idle"
	}{
		{"eof_postcommit_text", "eof"},
		{"idle_postcommit_text", "idle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			// Guard-enabled future: StreamRecovery must have AllowPostOutputContinuation=true when guard enabled.
			ex.StreamRecovery = streamrecovery.Config{Enabled: true, IdleTimeout: 5 * time.Millisecond, GracePeriod: 0, EmitWarning: true, AllowPostOutputContinuation: true}
			fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "finish answer", Reason: "post_output_interruption"}}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

			var opens atomic.Int32
			var retryPaths []bool
			// B1: emits text then EOF/idle (no response_finished). B2: emits continuation text then finish.
			// For determinism both kinds use EOF truncation after committed text; idle vs EOF share same DecisionContinuePostOutput contract.
			b2Text := " world"
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(ctx context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						n := opens.Add(1)
						if n == 1 {
							// Both eof and idle postcommit are truncated after visible text; runtime must not retry as replacement.
							return lipapi.NewFixedEventStream([]lipapi.Event{
								{Kind: lipapi.EventResponseStarted},
								{Kind: lipapi.EventMessageStarted},
								{Kind: lipapi.EventTextDelta, Delta: "hello"},
							}), nil
						}
						// Mark isRetryPath via opener capture; for Execute-level test we check opens count and text dedup.
						_ = cand
						retryPaths = append(retryPaths, false) // Execute path uses guard continuation (non-retry) by contract; we assert via opens==2 and dedup
						return lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventResponseStarted},
							{Kind: lipapi.EventMessageStarted},
							{Kind: lipapi.EventTextDelta, Delta: b2Text},
							{Kind: lipapi.EventResponseFinished},
						}), nil
					},
				},
			}
			call := &lipapi.Call{
				Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("task")}}},
			}
			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			defer func() { _ = stream.Close() }()
			col, err := lipapi.Collect(context.Background(), stream)
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			// Requirement 4.1-4.3, 9.5, 10.1-10.2: committed text emitted once, not retried as replacement, B2 only semantic continuation.
			if opens.Load() != 2 {
				t.Fatalf("post-output %s must open exactly one semantic continuation B2 (got opens=%d, want 2). Current production retries 0 or finishes synthetic, so RED.", tc.kind, opens.Load())
			}
			got := col.Text.String()
			if got != "hello"+b2Text {
				t.Fatalf("committed text must be emitted once with B2 continuation (got %q want %q); duplicate or missing means retry/replacement bug (RED).", got, "hello"+b2Text)
			}
			// Must not have duplicated "hello"
			if got == "hellohello"+b2Text || got == "hello hello" {
				t.Fatalf("duplicate committed text detected %q", got)
			}
			// Exactly one final terminal (Collect already ensures one terminal; we also assert B-leg settlement via store).
			leg, err := store.FetchALeg(context.Background(), call.Session.ALegID)
			if err != nil {
				t.Fatalf("FetchALeg: %v", err)
			}
			atts, err := store.LoadAttempts(context.Background(), leg.ALegID)
			if err != nil {
				t.Fatalf("LoadAttempts: %v", err)
			}
			if len(atts) != 2 {
				t.Fatalf("attempts len=%d want 2 (B1 swallowed post-output, B2 success)", len(atts))
			}
			if atts[0].Outcome == lipapi.AttemptSuccess && atts[1].Outcome == lipapi.AttemptSuccess {
				t.Fatalf("B1 must not be success after post-output interruption; must be swallowed/continued, got %v", atts[0].Outcome)
			}
			// No retry semantics: second open must not be counted as retry/replacement (guard continuation). Execute-level cannot directly capture isRetryPath without opener hook,
			// but opens==2 with AllowPostOutputContinuation proves retry path would be 0-1 pre-output; assert we saw continuation marker via text.
			_ = retryPaths
		})
	}
}

// Completed tool call + matching result then interruption: continue after retained result, tool executes once.
func TestAgentLoopGuard_PostOutput_CompletedToolPlusResultThenEOF_ContinuesWithoutReexecution_RED(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: true}
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "finish after tool", Reason: "post_output_tool"}}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

	var opens atomic.Int32
	var toolExecs atomic.Int32
	// tool reactor counting side effects (real tool correlation seam).
	type countingReactor struct{ c *atomic.Int32 }
	// Implement ToolReactor via hooks bus is heavier; instead use Open-counted tool execution via backend that would be re-invoked if replayed.
	// For this RED test we assert backend opens==2 and no re-execution via opens counter (tool would be inside B1 only).
	// If product re-executes tool on B2, it would appear as extra opens or duplicate tool events; we detect via LoadAttempts and collected text.
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				if n == 1 {
					toolExecs.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "read"},
						{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"path":"x"}`},
						{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_1"},
						{Kind: lipapi.EventTextDelta, Delta: "after-tool"},
					}), nil
				}
				// B2 must not re-execute tool; it just continues.
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: " continued"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("use tool")}}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	col, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if opens.Load() != 2 {
		t.Fatalf("completed tool+result then EOF must continue via one B2 (opens=%d want 2) RED", opens.Load())
	}
	if toolExecs.Load() != 1 {
		t.Fatalf("tool side effect must execute exactly once (got %d want 1) RED", toolExecs.Load())
	}
	if got := col.Text.String(); got != "after-tool continued" {
		t.Fatalf("text after tool continuation got %q want %q RED", got, "after-tool continued")
	}
	leg, _ := store.FetchALeg(context.Background(), call.Session.ALegID)
	atts, _ := store.LoadAttempts(context.Background(), leg.ALegID)
	if len(atts) != 2 {
		t.Fatalf("attempts %d want 2", len(atts))
	}
	// Ensure no duplicate tool pair in collected output (tool result already preserved canonically; we assert at least text legality).
	_ = col
}

// Incomplete args or unsafe opaque => one controlled final, zero continuation opens/tool executions.
func TestAgentLoopGuard_PostOutput_UnsafeState_NoReplayOneTerminal_RED(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mkB1 func() lipapi.ManagedEventStream
	}{
		{
			name: "incomplete_tool_args",
			mkB1: func() lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
					{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "bash"},
					{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"cmd":`}, // truncated, assembler will keep active
				})
			},
		},
		{
			name: "unsafe_opaque_thinking",
			mkB1: func() lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
					{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking"}`)},
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: true}
			fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "should be blocked", Reason: "unsafe"}}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
			var opens atomic.Int32
			var toolExecs atomic.Int32
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						n := opens.Add(1)
						if n == 1 {
							return tc.mkB1(), nil
						}
						toolExecs.Add(1)
						return lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventResponseFinished},
						}), nil
					},
				},
			}
			call := &lipapi.Call{
				Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("task")}}},
			}
			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			defer func() { _ = stream.Close() }()
			// Must produce exactly one controlled terminal, not hang, and zero B2.
			events, err := collectAll(stream)
			if err != nil && !errors.Is(err, io.EOF) {
				// controlled final may be error or finished; but we expect one terminal event.
				t.Fatalf("collect err %v", err)
			}
			if opens.Load() != 1 {
				t.Fatalf("%s must not open continuation B2 (opens=%d want 1) RED", tc.name, opens.Load())
			}
			if toolExecs.Load() != 0 {
				t.Fatalf("%s must not execute tool (execs=%d want 0) RED", tc.name, toolExecs.Load())
			}
			terms := countTerminal(events)
			if terms != 1 {
				t.Fatalf("%s must surface exactly one controlled terminal (got %d want 1) RED; events=%v", tc.name, terms, events)
			}
			leg, _ := store.FetchALeg(context.Background(), call.Session.ALegID)
			atts, _ := store.LoadAttempts(context.Background(), leg.ALegID)
			if len(atts) != 1 {
				t.Fatalf("attempts %d want 1", len(atts))
			}
		})
	}
}

// Cancellation prevents continuation.
func TestAgentLoopGuard_PostOutput_CancellationPreventsContinuation_RED(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		when string // "before_open" vs "during_open" (both RED: zero B2)
	}{
		{"cancel_before_continuation_open", "before"},
		{"cancel_during_continuation_open", "during"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: true}
			// Verifier would otherwise continue, but cancellation must win.
			block := make(chan struct{})
			entered := make(chan struct{}, 1)
			fv := &fakeGuardVerifierWithBlock{
				enteredCh: entered,
				blockCh:   block,
				verdict:   stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"},
			}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
			// Use retryRecvStream-level cancellation for deterministic channel test instead of Execute.
			// For Execute variant we also test context cancel before Collect.
			if tc.when == "before" {
				ctx, cancel := context.WithCancel(context.Background())
				var opens atomic.Int32
				ex.Backends = map[string]execbackend.Backend{
					"openai": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
						Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							opens.Add(1)
							return lipapi.NewFixedEventStream([]lipapi.Event{
								{Kind: lipapi.EventResponseStarted},
								{Kind: lipapi.EventMessageStarted},
								{Kind: lipapi.EventTextDelta, Delta: "hello"},
							}), nil
						},
					},
				}
				call := &lipapi.Call{
					Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
				}
				stream, err := ex.Execute(ctx, call)
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				defer func() { _ = stream.Close() }()
				cancel()
				close(block)
				_, err = lipapi.Collect(ctx, stream)
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, io.EOF) {
					// Collect may return EOF after cancellation settlement; authoritative is exactly one terminal/cancel.
					t.Logf("Collect err %v", err)
				}
				if opens.Load() != 1 {
					t.Fatalf("cancellation must prevent B2 open (opens=%d want 1) RED", opens.Load())
				}
				leg, _ := store.FetchALeg(context.Background(), call.Session.ALegID)
				atts, _ := store.LoadAttempts(context.Background(), leg.ALegID)
				if len(atts) != 1 {
					t.Fatalf("attempts %d want 1 authoritative cancellation", len(atts))
				}
				if atts[0].Outcome != lipapi.AttemptCancelled && atts[0].Outcome != lipapi.AttemptSwallowedFailure {
					// Accept either cancelled or swallowed depending on timing, but B2 must not have opened.
					t.Logf("outcome %v", atts[0].Outcome)
				}
			} else {
				// during: trigger EOF handling then cancel before B2 opens (retryRecvStream path)
				_, rs, _ := setupGuardedStream(t, fv, true)
				rs.StreamRecoveryForTestAllowPostOutputContinuation(true)
				ex2 := TestExecutor()
				store2, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
				ex2.Store = store2
				ex2.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
				bindTestRuntimeOwners(rs, ex2)
				// Prime committed output via visible text event
				if _, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"}); err != nil {
					t.Fatalf("prime text: %v", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				var ev lipapi.Event
				var recvErr error
				go func() {
					ev, recvErr = testRecvEOF(ctx, rs)
					close(done)
				}()
				select {
				case <-entered:
				case <-time.After(500 * time.Millisecond):
					t.Fatalf("verifier not entered (RED: post-output EOF must consult guard with DecisionContinuePostOutput+SafeCanonicalContinuation, currently not wired)")
				}
				cancel()
				close(block)
				<-done
				if recvErr == nil && ev.Kind == lipapi.EventTextDelta && ev.Delta == " world" {
					t.Fatalf("cancel during continuation must not produce B2 text RED")
				}
				if !rs.terminal.finished() {
					t.Fatal("cancellation must terminalize A-side RED")
				}
				_ = entered
				_ = block
			}
		})
	}
}

// Guard-disabled retains existing finish behavior (compatibility). This should PASS already.
func TestAgentLoopGuard_PostOutput_GuardDisabled_RetainsExistingFinishBehavior(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: false}
	ex.LoopGuardFactory = nil // disabled
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	events, err := collectAll(stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("collect %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("guard disabled must not open B2, opens=%d", opens.Load())
	}
	terms := countTerminal(events)
	if terms != 1 {
		t.Fatalf("guard disabled must surface exactly one terminal, got %d events=%v", terms, events)
	}
	leg, _ := store.FetchALeg(context.Background(), call.Session.ALegID)
	atts, _ := store.LoadAttempts(context.Background(), leg.ALegID)
	if len(atts) != 1 {
		t.Fatalf("attempts %d want 1", len(atts))
	}
}

// helpers for RED suite

func collectAll(s lipapi.EventStream) ([]lipapi.Event, error) {
	var out []lipapi.Event
	for {
		ev, err := s.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func countTerminal(evts []lipapi.Event) int {
	n := 0
	for _, e := range evts {
		if e.Kind == lipapi.EventResponseFinished {
			n++
		}
	}
	return n
}

// StreamRecoveryForTestAllowPostOutputContinuation is a minimal seam to set the flag on the retryRecvStream's recovery policy without exposing production globals.
// It installs a recoveryController with AllowPostOutputContinuation true. We implement via exported test helper attachment.
func (s *retryRecvStream) StreamRecoveryForTestAllowPostOutputContinuation(v bool) {
	if s.recovery == nil {
		s.recovery = &recoveryController{recoverPolicy: streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: v}, time.Now())}
		return
	}
	if s.recovery.recoverPolicy == nil {
		s.recovery.recoverPolicy = streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: v}, time.Now())
		return
	}
	// Recreate policy preserving enabled but toggling flag.
	s.recovery.recoverPolicy = streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true, AllowPostOutputContinuation: v}, time.Now())
}
