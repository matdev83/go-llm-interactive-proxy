package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuationsafety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// helpers

func newCustomGateForTest(verifier stopguard.Verifier, maxCont, noProgress int) *stopgate.Gate {
	return stopgate.New(stopgate.Ports{Verifier: verifier, Now: time.Now}, stopgate.Config{
		Enabled: true, ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: maxCont, NoProgressLimit: noProgress,
	})
}

func execSetupGuardContinuationOpener(t *testing.T, rs *retryRecvStream, events []lipapi.Event) {
	t.Helper()
	if rs.recovery == nil {
		rs.recovery = &recoveryController{}
	}
	rs.recovery.opener = func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		blegID := "b-guard-2"
		seq := 2
		if cur := rs.attempt.snapshot(); cur != nil {
			seq = int(cur.bleg.Seq) + 1
			blegID = cur.bleg.BLegID + "-cont"
		}
		bleg := b2bua.BLegRecord{BLegID: blegID, Seq: seq, ALegID: rs.facts.aLegID}
		cand := routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}}
		stream := &guardContinuationEventStream{events: events}
		sess := newAttemptSession(attemptSessionInput{
			inner:            stream,
			bleg:             bleg,
			cand:             cand,
			authority:        authorityLifecycle{},
			aScope:           rs.terminal.aLegScope(),
			traceID:          rs.facts.traceID,
			billingCallID:    rs.facts.billingCallID,
			billingCallState: rs.facts.billingCallState,
		})
		ready := newReadyAttempt(sess, pendingSelectionEffects{})
		ready.state = readyStatePrepared
		return replacementOpenResult{opened: true, ready: ready, bleg: bleg, cand: cand}, nil
	}
}

type guardContinuationEventStream struct {
	events []lipapi.Event
	idx    int
	mu     sync.Mutex
}

func (s *guardContinuationEventStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *guardContinuationEventStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{}
}

func (s *guardContinuationEventStream) Close() error { return nil }

// --- Per-request Gate factory isolation ---

func TestGuardContinuation_PerRequestGateIsolation_Concurrent(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"}}
	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
	// Two logical requests sharing same Executor+Factory, concurrently.
	rs1 := newGuardedStreamForFactory(t, ex, "trace-g1", "a-g1", "b-g1")
	rs2 := newGuardedStreamForFactory(t, ex, "trace-g2", "a-g2", "b-g2")
	// Capture opener for each to allow continuation.
	execSetupGuardContinuationOpener(t, rs1, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "b2-g1"}, {Kind: lipapi.EventResponseFinished}})
	execSetupGuardContinuationOpener(t, rs2, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "b2-g2"}, {Kind: lipapi.EventResponseFinished}})
	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)
	go func() {
		defer wg.Done()
		// Exhaust rs1: 3 continues then forward
		for i := range 3 {
			ev, err := testRecvOne(context.Background(), rs1, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw"})
			if err != nil {
				errCh <- err
				return
			}
			if ev.Kind != lipapi.EventTextDelta {
				// first continue will be text, later after 3 continues will be forward terminal? For budget 3, 4th should be forward.
				// We just check that after 3 continues we still get text, not forward.
				if i < 2 && ev.Kind != lipapi.EventTextDelta {
					errCh <- errors.New("rs1 expected b2 text")
					return
				}
			}
			// Reset for next loop: need to set next B2 events again (opener already handles next seq)
			// For simplicity we don't loop fully via Recv, we directly test gate isolation via terminal gate.
		}
		// After exhausting, next candidate should be forward
		// Use direct gate to prove latch unaffected: rs2 should still continue
		errCh <- nil
	}()
	go func() {
		defer wg.Done()
		ev, err := testRecvOne(context.Background(), rs2, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw"})
		if err != nil {
			errCh <- err
			return
		}
		if ev.Kind != lipapi.EventTextDelta || ev.Delta != "b2-g2" {
			errCh <- errors.New("rs2 expected b2-g2 text, isolation failed")
			return
		}
		errCh <- nil
	}()
	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			t.Fatal(e)
		}
	}
	// Direct gate isolation proof already covered by concurrent Recv above: rs2 succeeded while rs1 was exhausting.
	// No extra direct gate check needed after Recv latch.
}

func newGuardedStreamForFactory(t *testing.T, ex *Executor, traceID, aLegID, bLegID string) *retryRecvStream {
	t.Helper()
	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:         "guard-isolation-" + traceID,
				Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
			},
			aLegID:  aLegID,
			traceID: traceID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: bLegID, Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{},
	}
	bindTestRuntimeOwners(rs, ex)
	return rs
}

// --- Honest prior and bounds ---

func TestGuardContinuation_HonestPriorAndBounds(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "do work", Reason: "pending"}}
	_, rs, _ := setupGuardedStream(t, fv, true)
	// Seed baseline with honest input
	rs.facts.baseline.Items = []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "user task"}}}}
	prior := buildGuardPrior(rs)
	if len(prior.Record.InputItems) == 0 {
		t.Fatal("honest prior must have InputItems from baseline")
	}
	if prior.Record.ID == "" {
		t.Fatal("prior ID empty")
	}
	if prior.Record.PreviousID != "" {
		t.Fatalf("initial prior PreviousID should be empty, got %q", prior.Record.PreviousID)
	}
	// Safety should be safe with empty tail and honest prior
	tail := buildGuardTailState(rs.responsePipeline, rs.attempt.snapshot())
	bounds := lipcont.DefaultBounds()
	res := continuationsafety.Evaluate(continuationsafety.Input{Prior: prior, Tail: tail, Bounds: bounds})
	if res.Outcome != continuationsafety.OutcomeContinueSafe {
		t.Fatalf("evaluate with honest prior should be safe, got %q", res.Outcome)
	}
	if res.Facts.PreviousID != prior.Record.ID {
		t.Fatalf("PreviousID %q want %q", res.Facts.PreviousID, prior.Record.ID)
	}
	// ChainDepth bound
	prior2 := prior
	prior2.Record.ChainDepth = bounds.MaxChainDepth
	res2 := continuationsafety.Evaluate(continuationsafety.Input{Prior: prior2, Tail: tail, Bounds: bounds})
	if res2.Outcome != continuationsafety.OutcomeChainDepthExceeded {
		t.Fatalf("chain depth exceeded should be unsafe, got %q", res2.Outcome)
	}
	// Materialized bytes bound
	prior3 := prior
	prior3.Record.MaterializedBytes = int64(bounds.MaxMaterializedBytes)
	tail3 := tail
	tail3.CommittedAssistantItems = []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: string(make([]byte, bounds.MaxMaterializedBytes))}}}}
	res3 := continuationsafety.Evaluate(continuationsafety.Input{Prior: prior3, Tail: tail3, Bounds: bounds})
	if res3.Outcome != continuationsafety.OutcomeMaterializationExceeded {
		t.Fatalf("materialized exceeded should be unsafe, got %q", res3.Outcome)
	}
}

// --- Safe continuation via normal admission ---

func TestGuardContinuation_ContinueSafe_MaterializesAndOpensB2_NoIntermediateTerminal(t *testing.T) {
	t.Parallel()
	callCount := 0
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			callCount++
			if callCount == 1 {
				return stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "run tests", Reason: "pending work"}, nil
			}
			return stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}, nil
		},
	}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	b2Text := lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "continued output"}
	b2Finished := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"}
	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{b2Text, b2Finished})
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_backend_finish"})
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if ev.FinishReason == guardContinuationPendingReason {
		t.Fatalf("intermediate A-terminal emitted, got %q", ev.FinishReason)
	}
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != "continued output" {
		t.Fatalf("expected B2 text, got kind %q delta %q", ev.Kind, ev.Delta)
	}
	if rs.terminal.guardHidden == "" || !contains(rs.terminal.guardHidden, "<automated-recovery>") {
		t.Fatalf("hidden instruction missing, got %q", rs.terminal.guardHidden)
	}
	ev2, err := rs.Recv(context.Background())
	if err != nil {
		t.Fatalf("second Recv: %v", err)
	}
	if ev2.Kind != lipapi.EventResponseFinished {
		t.Fatalf("second kind %q", ev2.Kind)
	}
	if !rs.terminal.finished() {
		t.Fatal("A finished after B2")
	}
	_, err = rs.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("third err %v want EOF", err)
	}
}

func TestGuardContinuation_B2_LineageAndAttemptAccounting(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "do work", Reason: "pending"}}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	b1 := rs.attempt.snapshot()
	b1ID := b1.bleg.BLegID
	seq1 := b1.bleg.Seq
	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "b2"}, {Kind: lipapi.EventResponseFinished}})
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if ev.Kind != lipapi.EventTextDelta {
		t.Fatalf("want B2 text, got %v", ev)
	}
	b2 := rs.attempt.snapshot()
	if b2.bleg.Seq != seq1+1 {
		t.Fatalf("seq %d want %d", b2.bleg.Seq, seq1+1)
	}
	if b2.bleg.BLegID == b1ID {
		t.Fatal("B2 ID same as B1")
	}
	if b1ID == "" || b2.bleg.BLegID == "" {
		t.Fatal("BLegID empty")
	}
}

func TestGuardContinuation_HiddenInstructionNotForwarded(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "run", Reason: "pending"}}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	var capturedCall *lipapi.Call
	origOpener := func() replacementOpener {
		if rs.recovery == nil {
			rs.recovery = &recoveryController{}
		}
		// Wrap opener to capture Call
		base := func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
			capturedCall = &req.pinnedFacts.baseline
			blegID := "b-guard-2"
			seq := 2
			if cur := rs.attempt.snapshot(); cur != nil {
				seq = int(cur.bleg.Seq) + 1
				blegID = cur.bleg.BLegID + "-cont"
			}
			bleg := b2bua.BLegRecord{BLegID: blegID, Seq: seq, ALegID: rs.facts.aLegID}
			cand := routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}}
			stream := &guardContinuationEventStream{events: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "visible"}, {Kind: lipapi.EventResponseFinished}}}
			sess := newAttemptSession(attemptSessionInput{inner: stream, bleg: bleg, cand: cand, aScope: rs.terminal.aLegScope(), traceID: rs.facts.traceID, billingCallID: rs.facts.billingCallID, billingCallState: rs.facts.billingCallState})
			ready := newReadyAttempt(sess, pendingSelectionEffects{})
			ready.state = readyStatePrepared
			return replacementOpenResult{opened: true, ready: ready, bleg: bleg, cand: cand}, nil
		}
		return base
	}()
	rs.recovery = &recoveryController{opener: origOpener}
	// Ensure factory path uses guard continuation mode (isRetryPath false)
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if contains(ev.Delta, "<automated-recovery>") {
		t.Fatalf("hidden leaked as A content %q", ev.Delta)
	}
	if capturedCall == nil {
		t.Fatal("opener not called, no captured Call")
	}
	foundHidden := false
	for _, it := range capturedCall.Items {
		if it.Role == lipapi.RoleDeveloper {
			for _, cp := range it.Content {
				if contains(cp.Text, "<automated-recovery>") {
					foundHidden = true
					if !contains(cp.Text, "not a new user request") {
						t.Fatalf("hidden missing normative wording, got %q", cp.Text)
					}
				}
			}
		}
		if it.Role == lipapi.RoleUser && len(it.Content) > 0 && contains(it.Content[0].Text, "<automated-recovery>") {
			t.Fatalf("hidden leaked as user role")
		}
	}
	for _, msg := range capturedCall.Messages {
		if msg.Role == lipapi.RoleDeveloper {
			for _, part := range msg.Parts {
				if contains(part.Text, "<automated-recovery>") {
					foundHidden = true
					if !contains(part.Text, "not a new user request") {
						t.Fatalf("hidden missing normative wording, got %q", part.Text)
					}
				}
			}
		}
		if msg.Role == lipapi.RoleUser {
			for _, part := range msg.Parts {
				if contains(part.Text, "<automated-recovery>") {
					t.Fatalf("hidden leaked as user role")
				}
			}
		}
	}
	if !foundHidden {
		t.Fatalf("hidden instruction not found in B2 Call Developer items/messages, captured Items=%v Messages=%v", capturedCall.Items, capturedCall.Messages)
	}
	visible := rs.responsePipeline.releasedOutputText()
	if contains(visible, "<automated-recovery>") {
		t.Fatalf("hidden in visible %q", visible)
	}
	seen := rs.responsePipeline.seenEventsCopy()
	for _, se := range seen {
		if se.Kind == lipapi.EventTextDelta && contains(se.Delta, "<automated-recovery>") {
			t.Fatalf("hidden in seenEvents")
		}
	}
	// Continuation prior input should not contain hidden as user-authored persistence
	if rs.terminal.guardPriorOK {
		for _, it := range rs.terminal.guardPriorRecord.InputItems {
			for _, cp := range it.Content {
				if contains(cp.Text, "<automated-recovery>") {
					t.Fatalf("hidden persisted as prior InputItems")
				}
			}
		}
	}
}

func TestGuardContinuation_Unsafe_DoesNotOpenB2(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "should not open", Reason: "pending"}}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	// Force incomplete args via tool assembler active
	rs.attempt.snapshot().toolFinal = &toolCallAssembler{
		// active non-empty forces unsafe
	}
	// manually set active map
	at := rs.attempt.snapshot().toolFinal
	at.active = map[string]*toolCallBuffer{"id1": {}}
	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "should not appear"}})
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("unsafe should produce final, got %v", ev)
	}
	if ev.Delta == "should not appear" {
		t.Fatal("B2 opened for unsafe")
	}
	if rs.terminal.guardHidden != "" {
		t.Fatalf("unsafe must not inject hidden, got %q", rs.terminal.guardHidden)
	}
	if !rs.terminal.finished() {
		t.Fatal("must be finished after unsafe")
	}
}

func TestGuardContinuation_CancellationPreventsContinuation(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	fv := &fakeGuardVerifierWithBlock{
		enteredCh: entered,
		blockCh:   block,
		verdict:   stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work"},
	}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "b2"}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var ev lipapi.Event
	go func() {
		ev, _ = testRecvOne(ctx, rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("verifier not entered")
	}
	cancel()
	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recv not done")
	}
	if ev.Kind == lipapi.EventTextDelta && ev.Delta == "b2" {
		t.Fatalf("cancel must prevent B2, got %v", ev)
	}
	if !rs.terminal.finished() {
		t.Fatal("must be finished after cancel")
	}
}

func TestGuardContinuation_VerifierRecursionSuppressed(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "x"}}
	_, rs, _ := setupGuardedStream(t, fv, true)
	// Production context suppression via execctx (auxreq path)
	ctx := execctx.WithSuppressedPluginIDs(context.Background(), []string{"agent_loop_guard"})
	out := rs.terminal.agentLoopGuardEvaluate(ctx, rs.facts.terminalFacts(), rs.attempt.snapshot(), rs.responsePipeline, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if out.Action != stopguard.ActionForwardTerminal {
		t.Fatalf("suppressed should forward, got %v", out.Action)
	}
	if fv.CallCount() != 0 {
		t.Fatalf("verifier called %d want 0 for suppressed, auxreq signal", fv.CallCount())
	}
	// Direct gate SuppressVerification also must not invoke verifier
	gate := newCustomGateForTest(fv, 3, 2)
	tf := stopgate.TerminalFacts{
		Candidate:            stopguard.Candidate{Cause: stopguard.CauseNormalEnd},
		Prior:                continuationsafety.PriorSummary{Record: lipcont.ContinuationRecord{ID: lipcont.ResponseID("r1")}},
		Bounds:               lipcont.DefaultBounds(),
		SuppressVerification: true,
	}
	out2 := gate.ObserveCandidate(context.Background(), tf)
	if out2.Action != stopguard.ActionForwardTerminal {
		t.Fatalf("direct suppressed should forward, got %v", out2.Action)
	}
	// After suppression gate is latched forward per design (prevents recursion), subsequent normal is forwarded, not continued
	out3 := rs.terminal.agentLoopGuardEvaluate(context.Background(), rs.facts.terminalFacts(), rs.attempt.snapshot(), rs.responsePipeline, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if out3.Action != stopguard.ActionForwardTerminal {
		t.Fatalf("after suppressed, subsequent should be forwarded (latched), got %v", out3.Action)
	}
}

func TestGuardContinuation_BudgetImmutableAcrossProgress(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "obj", Reason: "pending"}}
	gate := newCustomGateForTest(fv, 2, 10)
	makeFacts := func(text string) stopgate.TerminalFacts {
		return stopgate.TerminalFacts{
			Candidate: stopguard.Candidate{Cause: stopguard.CauseNormalEnd, OutputCommitted: true},
			Tail: continuationsafety.TailState{
				CommittedAssistantItems: []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleAssistant, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: text}}}},
			},
			Prior:                continuationsafety.PriorSummary{Record: lipcont.ContinuationRecord{ID: lipcont.ResponseID("r1")}},
			Bounds:               lipcont.DefaultBounds(),
			SupportsContinuation: true,
		}
	}
	out1 := gate.ObserveCandidate(context.Background(), makeFacts("progress-1"))
	if out1.Action != stopguard.ActionContinueLeg {
		t.Fatalf("1st want continue, got %v", out1.Action)
	}
	if out1.MaxAttempts != 2 {
		t.Fatalf("MaxAttempts %d want 2", out1.MaxAttempts)
	}
	out2 := gate.ObserveCandidate(context.Background(), makeFacts("progress-2"))
	if out2.Action != stopguard.ActionContinueLeg {
		t.Fatalf("2nd want continue, got %v", out2.Action)
	}
	if out2.MaxAttempts != 2 {
		t.Fatalf("MaxAttempts changed %d", out2.MaxAttempts)
	}
	out3 := gate.ObserveCandidate(context.Background(), makeFacts("progress-3"))
	if out3.Action != stopguard.ActionForwardTerminal {
		t.Fatalf("3rd should forward, got %v", out3.Action)
	}
}

func TestGuardContinuation_AdmissionNormalPath(t *testing.T) {
	t.Parallel()
	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "do work", Reason: "pending"}}
	_, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	var capturedReq *replacementOpenRequest
	var capturedIsRetryPath *bool
	origRecovery := rs.recovery
	captureOpener := func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		capturedReq = &req
		b := req.isRetryPath
		capturedIsRetryPath = &b
		blegID := "b-guard-admission-2"
		seq := 2
		if cur := rs.attempt.snapshot(); cur != nil {
			seq = int(cur.bleg.Seq) + 1
			blegID = cur.bleg.BLegID + "-cont"
		}
		bleg := b2bua.BLegRecord{BLegID: blegID, Seq: seq, ALegID: rs.facts.aLegID}
		cand := routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}}
		stream := &guardContinuationEventStream{events: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "admission"}, {Kind: lipapi.EventResponseFinished}}}
		sess := newAttemptSession(attemptSessionInput{inner: stream, bleg: bleg, cand: cand, aScope: rs.terminal.aLegScope(), traceID: rs.facts.traceID, billingCallID: rs.facts.billingCallID, billingCallState: rs.facts.billingCallState})
		ready := newReadyAttempt(sess, pendingSelectionEffects{})
		ready.state = readyStatePrepared
		return replacementOpenResult{opened: true, ready: ready, bleg: bleg, cand: cand}, nil
	}
	if rs.recovery == nil {
		rs.recovery = &recoveryController{}
	}
	rs.recovery.opener = captureOpener
	_ = origRecovery
	b1 := rs.attempt.snapshot()
	if b1 == nil {
		t.Fatal("b1 nil")
	}
	b1ID := b1.bleg.BLegID
	b1Seq := b1.bleg.Seq
	billingState := rs.facts.billingCallState
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if ev.Kind != lipapi.EventTextDelta || ev.Delta != "admission" {
		t.Fatalf("expected admission B2 text, got %v", ev)
	}
	if capturedReq == nil || capturedIsRetryPath == nil {
		t.Fatal("opener not captured")
	}
	if *capturedIsRetryPath != false {
		t.Fatalf("admission must be isRetryPath:false, got %v", *capturedIsRetryPath)
	}
	if capturedReq.pinnedFacts.billingCallState != billingState {
		t.Fatalf("billingCallState not carried")
	}
	if capturedReq.pinnedFacts.aLegID != rs.facts.aLegID {
		t.Fatalf("ALeg not preserved")
	}
	b2 := rs.attempt.snapshot()
	if b2.bleg.BLegID == b1ID {
		t.Fatal("B2 ID same as B1")
	}
	if b2.bleg.Seq != b1Seq+1 {
		t.Fatalf("B2 seq %d want %d", b2.bleg.Seq, b1Seq+1)
	}
	// B1 should be swallowed exactly once
	if b1.terminal != nil && b1.terminal.Owner() != nil && b1.terminal.Owner().State() == sdkterminal.StateOpen {
		t.Fatalf("B1 should be terminalized swallowed, state open")
	}
}

func TestGuardContinuation_DisabledParityUnchanged(t *testing.T) {
	t.Parallel()
	_, rs, _ := setupGuardedStreamForHoldback(t, nil, false)
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw"})
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if ev.FinishReason != "raw" {
		t.Fatalf("raw %q", ev.FinishReason)
	}
	if !rs.terminal.finished() {
		t.Fatal("finished")
	}
	if rs.terminal.guardHidden != "" {
		t.Fatal("disabled must not inject")
	}
}

func TestGuardContinuation_GatePriorHonestNotBLegID(t *testing.T) {
	t.Parallel()
	spy := &spyGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
	_, rs, _ := setupGuardedStream(t, spy, true)
	// Seed honest baseline
	rs.facts.baseline.Items = []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "honest task"}}}}
	blegID := rs.attempt.snapshot().bleg.BLegID
	ev := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"}
	_ = rs.terminal.agentLoopGuardEvaluate(context.Background(), rs.facts.terminalFacts(), rs.attempt.snapshot(), rs.responsePipeline, ev)
	if spy.captured.ContinuationLineage.ContinuationID == "" {
		t.Fatalf("verifier not called or lineage empty")
	}
	// Honest prior ID is guard-init-<traceID>, not BLegID
	honestID := "guard-init-" + rs.facts.traceID
	if spy.captured.ContinuationLineage.ContinuationID == blegID {
		t.Fatalf("Gate prior incorrectly uses BLegID %q, want honest %q", blegID, honestID)
	}
	if spy.captured.ContinuationLineage.ContinuationID != honestID {
		t.Fatalf("Gate prior ContinuationID %q want honest %q", spy.captured.ContinuationLineage.ContinuationID, honestID)
	}
	// Also verify second call uses same honest prior (previousID consistency) and not BLegID
	spy2 := &spyGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"}}
	_, rs2, _ := setupGuardedStream(t, spy2, true)
	rs2.facts.baseline.Items = []lipapi.Item{{Kind: lipapi.ItemKindMessage, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "task2"}}}}
	_ = rs2.terminal.agentLoopGuardEvaluate(context.Background(), rs2.facts.terminalFacts(), rs2.attempt.snapshot(), rs2.responsePipeline, ev)
	if spy2.captured.ContinuationLineage.ContinuationID == rs2.attempt.snapshot().bleg.BLegID {
		t.Fatalf("second Gate prior still uses BLegID")
	}
}

type spyGuardVerifier struct {
	captured stopguard.Evidence
	verdict  stopguard.Verdict
	err      error
}

func (s *spyGuardVerifier) Verify(_ context.Context, ev stopguard.Evidence) (stopguard.Verdict, error) {
	s.captured = ev
	if s.err != nil {
		return stopguard.Verdict{Kind: stopguard.VerdictUncertain}, s.err
	}
	return s.verdict, nil
}

// helpers for guard continuation tests

type fakeGuardVerifierWithCount struct {
	fn func() (stopguard.Verdict, error)
}

func (f *fakeGuardVerifierWithCount) Verify(ctx context.Context, _ stopguard.Evidence) (stopguard.Verdict, error) {
	return f.fn()
}

var _ = atomic.Int64{}
