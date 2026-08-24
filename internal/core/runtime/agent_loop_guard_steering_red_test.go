package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuationsafety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Point 1: Actionable CONTINUE invokes canonical SteeringStore/Writer Put exactly once
// with fixed ID "alg-rec", RoleDeveloper, exact bounded instruction, AfterIngressTail
// resolved to a fixed anchor, FailClosed, reason "loop_guard_recovery".
// Requirements: 6.8, 6.9, 12.11.
func TestAgentLoopGuard_Steering_ActionableContinueRegistersOverlay(t *testing.T) {
	t.Parallel()

	fv := &fakeGuardVerifier{
		verdict: stopguard.Verdict{
			Kind:               stopguard.VerdictContinue,
			RemainingObjective: "run full test suite",
			Reason:             "uncompleted test execution",
		},
	}
	ex, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-act-key")
	require.NoError(t, err)
	rs.facts.aLegID = aLegRec.ALegID

	b2Text := lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "continued execution"}
	b2Finished := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"}
	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{b2Text, b2Finished})

	// Trigger clean finish from B1; guard should evaluate to CONTINUE and register steering overlay.
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw_backend_finish"})
	require.NoError(t, err)
	assert.Equal(t, lipapi.EventTextDelta, ev.Kind)
	assert.Equal(t, "continued execution", ev.Delta)

	// Check conversation-view store: overlay must be registered via canonical SteeringStore/Writer.
	cvStore, ok := conversationview.AsStore(ex.Store)
	require.True(t, ok, "store must implement conversationview.Store")
	snap, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
	require.NoError(t, err)

	// In current production, direct append was used and zero overlays were stored in SteeringStore -> RED.
	require.Len(t, snap.Steering, 1, "actionable CONTINUE must register exactly one steering overlay in SteeringStore")

	ov := snap.Steering[0]
	assert.Equal(t, "alg-rec", ov.OverlayID, "overlay ID must be fixed 'alg-rec'")
	assert.True(t, ov.Active, "overlay must be active")
	assert.Equal(t, lipapi.RoleDeveloper, ov.Message.Role, "overlay role must be RoleDeveloper")
	assert.Contains(t, ov.Message.Text, "<automated-recovery>", "instruction must contain <automated-recovery>")
	assert.Contains(t, ov.Message.Text, "run full test suite", "instruction must contain remaining objective")
	assert.Equal(t, conversationview.PlacementAfterMessage, ov.Placement.Kind, "placement must resolve to PlacementAfterMessage")
	require.NotNil(t, ov.Placement.Anchor, "anchor must be resolved to a fixed MessageAnchor")
	assert.Equal(t, conversationview.AnchorFailClosed, ov.AnchorMissingPolicy, "anchor missing policy must be FailClosed")
	assert.Equal(t, conversationview.ReasonCode("loop_guard_recovery"), ov.Reason, "reason code must be 'loop_guard_recovery'")
}

// Point 2: TrajectoryResolver uses preserved accepted user ingress call, not post-B1 assistant tail.
// Requirements: 6.9, 12.11.
func TestAgentLoopGuard_Steering_TrajectoryResolverUsesUserIngressNotAssistantTail(t *testing.T) {
	t.Parallel()

	// Direct contract check: ResolveAfterIngressTailAnchor fails with ErrTerminalNotUser
	// when given a call ending in assistant output.
	assistantCall := lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("user prompt")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("assistant output")}},
		},
	}
	_, err := conversationview.ResolveAfterIngressTailAnchor(assistantCall, conversationview.Snapshot{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, conversationview.ErrTerminalNotUser), "resolving against post-B1 assistant tail must return ErrTerminalNotUser")

	// Direct contract check: ResolveAfterIngressTailAnchor succeeds when given accepted user ingress call.
	userMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("user prompt")}}
	userCall := lipapi.Call{
		Messages: []lipapi.Message{userMsg},
	}
	anchor, err := conversationview.ResolveAfterIngressTailAnchor(userCall, conversationview.Snapshot{})
	require.NoError(t, err)
	expectedID, err := conversationview.MessageIdentityOf(userMsg)
	require.NoError(t, err)
	assert.Equal(t, expectedID, anchor.Identity)
	assert.Equal(t, uint32(1), anchor.Occurrence)

	// Runtime integration check: In a guarded continuation turn, verify that the stored
	// overlay anchor matches the ingress user message identity, proving TrajectoryResolver
	// used the accepted user ingress call and not post-B1 assistant tail.
	fv := &fakeGuardVerifier{
		verdict: stopguard.Verdict{
			Kind:               stopguard.VerdictContinue,
			RemainingObjective: "finish task",
			Reason:             "unfinished",
		},
	}
	ex, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-res-key")
	require.NoError(t, err)
	rs.facts.aLegID = aLegRec.ALegID
	ingressMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}
	expectedUserID, err := conversationview.MessageIdentityOf(ingressMsg)
	require.NoError(t, err)

	rs.facts.baseline = lipapi.Call{
		ID:         "resolver-call",
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages:   []lipapi.Message{ingressMsg},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
	}

	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "b2 output"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	})

	_, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
	require.NoError(t, err)

	cvStore, ok := conversationview.AsStore(ex.Store)
	require.True(t, ok)
	snap, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
	require.NoError(t, err)

	// On current production code: snap.Steering is empty -> RED.
	require.NotEmpty(t, snap.Steering, "steering overlay must be registered")
	require.NotNil(t, snap.Steering[0].Placement.Anchor, "anchor must be resolved")
	assert.Equal(t, expectedUserID, snap.Steering[0].Placement.Anchor.Identity, "anchor must match terminal user ingress message identity")
}

// Point 3: After Put, runtime freezes Snapshot N+1 and opens B2 through normal semantic admission;
// captured B2 call receives instruction once via projection/Reassert. It must not be manually appended.
// Requirements: 6.10, 6.11, 12.12.
func TestAgentLoopGuard_Steering_SnapshotIsolationAndProjectionEvidence(t *testing.T) {
	t.Parallel()

	fv := &fakeGuardVerifier{
		verdict: stopguard.Verdict{
			Kind:               stopguard.VerdictContinue,
			RemainingObjective: "compile and test",
			Reason:             "incomplete",
		},
	}
	ex, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-snap-key")
	require.NoError(t, err)
	rs.facts.aLegID = aLegRec.ALegID

	var capturedReq *replacementOpenRequest
	origOpener := func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		capturedReq = &req
		bleg := b2bua.BLegRecord{BLegID: "b-guard-snap-2", Seq: 2, ALegID: rs.facts.aLegID}
		cand := routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}}
		stream := &guardContinuationEventStream{events: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "b2"}, {Kind: lipapi.EventResponseFinished}}}
		sess := newAttemptSession(attemptSessionInput{
			inner:            stream,
			bleg:             bleg,
			cand:             cand,
			aScope:           rs.terminal.aLegScope(),
			traceID:          rs.facts.traceID,
			billingCallID:    rs.facts.billingCallID,
			billingCallState: rs.facts.billingCallState,
		})
		ready := newReadyAttempt(sess, pendingSelectionEffects{})
		ready.state = readyStatePrepared
		return replacementOpenResult{opened: true, ready: ready, bleg: bleg, cand: cand}, nil
	}
	if rs.recovery == nil {
		rs.recovery = &recoveryController{}
	}
	rs.recovery.opener = origOpener

	_, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
	require.NoError(t, err)

	cvStore, ok := conversationview.AsStore(ex.Store)
	require.True(t, ok)
	snap, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
	require.NoError(t, err)

	// Snapshot N+1 must be frozen and StateRevision bumped.
	// On current production: snap.StateRevision is 0 and snap.Steering is empty -> RED.
	require.Greater(t, snap.StateRevision, uint64(0), "StateRevision must be incremented to Snapshot N+1")
	require.Len(t, snap.Steering, 1, "Snapshot N+1 must contain registered steering overlay")
	assert.Equal(t, uint64(1), snap.Steering[0].Revision, "overlay revision must be 1 on initial Put")

	require.NotNil(t, capturedReq, "continuation open request must be captured")
	assert.False(t, capturedReq.isRetryPath, "continuation must use semantic admission (isRetryPath=false)")
}

// Point 4: Hidden instruction absent from A-side events and frontend continuation records/materialization.
// Requirements: 6.12, 10.1, 10.2, 12.12.
func TestAgentLoopGuard_Steering_TranscriptAndContinuationRecordIsolation(t *testing.T) {
	t.Parallel()

	callCount := 0
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			callCount++
			if callCount == 1 {
				return stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "resume task", Reason: "in-progress"}, nil
			}
			return stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "done"}, nil
		},
	}
	ex, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-trans-key")
	require.NoError(t, err)
	rs.facts.aLegID = aLegRec.ALegID

	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "final answer text"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
	})

	// B1 completes prematurely -> CONTINUE -> B2 produces text -> B2 finishes -> ALLOW_STOP
	ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
	require.NoError(t, err)
	assert.Equal(t, "final answer text", ev.Delta)
	assert.False(t, strings.Contains(ev.Delta, "<automated-recovery>"), "A-side text delta must not contain hidden recovery instruction")

	// 1. Verify A-side pipeline released text does not leak hidden instruction.
	releasedText := rs.responsePipeline.releasedOutputText()
	assert.False(t, strings.Contains(releasedText, "<automated-recovery>"), "released output text must not contain recovery instruction")
	assert.False(t, strings.Contains(releasedText, "alg-rec"), "released output text must not contain overlay ID")

	// 2. Verify all seen events on pipeline exclude hidden instruction.
	for _, se := range rs.responsePipeline.seenEventsCopy() {
		if se.Kind == lipapi.EventTextDelta {
			assert.False(t, strings.Contains(se.Delta, "<automated-recovery>"), "seen event delta must not contain recovery instruction")
		}
	}

	// 3. Verify turnTerminal does NOT store legacy guardHidden string.
	// On current production: t.guardHidden is populated directly -> RED.
	assert.Empty(t, rs.terminal.guardHiddenInstruction(), "turnTerminal must not retain legacy guardHidden string")

	// 4. Verify baseline call was not directly mutated with hidden recovery message.
	for _, m := range rs.facts.baseline.Messages {
		assert.NotEqual(t, lipapi.RoleDeveloper, m.Role, "baseline messages must not contain RoleDeveloper direct appends")
	}
}

// Point 5: Fixed overlay update across B2/B3 reuses same ID/slot; revision changes only
// when instruction changes, no duplicate copies.
// Requirements: 6.8, 6.13, 12.11.
func TestAgentLoopGuard_Steering_FixedOverlayUpdateReusesSlotAcrossAttempts(t *testing.T) {
	t.Parallel()

	step := 0
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			step++
			if step == 1 {
				return stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "step 1 work", Reason: "continue 1"}, nil
			}
			if step == 2 {
				return stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "step 2 work", Reason: "continue 2"}, nil
			}
			return stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "all done"}, nil
		},
	}
	ex, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
	aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-multi-slot-key")
	require.NoError(t, err)
	rs.facts.aLegID = aLegRec.ALegID

	// B2 opener that emits text and finish
	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "b2-output"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "raw"},
	})

	// B1 finish -> triggers B2
	_, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "raw"})
	require.NoError(t, err)

	cvStore, ok := conversationview.AsStore(ex.Store)
	require.True(t, ok)
	snap1, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
	require.NoError(t, err)

	// On current production: snap1.Steering is empty -> RED.
	require.Len(t, snap1.Steering, 1, "first continuation must register exactly one overlay")
	slot1 := snap1.Steering[0].SlotOrdinal
	rev1 := snap1.Steering[0].Revision
	assert.Equal(t, "alg-rec", snap1.Steering[0].OverlayID)

	// Reconfigure opener for B3
	execSetupGuardContinuationOpener(t, rs, []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "b3-output"},
		{Kind: lipapi.EventResponseFinished, FinishReason: "raw"},
	})

	// B2 finish -> triggers B3
	_, err = rs.Recv(context.Background())
	require.NoError(t, err)

	snap2, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
	require.NoError(t, err)

	require.Len(t, snap2.Steering, 1, "second continuation must NOT create duplicate overlay; must update existing 'alg-rec'")
	assert.Equal(t, "alg-rec", snap2.Steering[0].OverlayID)
	assert.Equal(t, slot1, snap2.Steering[0].SlotOrdinal, "slot ordinal must be preserved on update")
	assert.Greater(t, snap2.Steering[0].Revision, rev1, "overlay revision must bump when instruction changes")
}

// Point 6a: Deactivate on allow-stop, cancel, exhaustion, and open-failure.
// Requirements: 6.13, 12.13.
func TestAgentLoopGuard_Steering_DeactivationOnLifecycleTerminals(t *testing.T) {
	t.Parallel()

	t.Run("allow_stop_deactivates_overlay", func(t *testing.T) {
		t.Parallel()
		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"},
		}
		ex, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
		aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-deact-allow-key")
		require.NoError(t, err)
		rs.facts.aLegID = aLegRec.ALegID

		// Pre-seed an active "alg-rec" overlay in the store
		cvStore, ok := conversationview.AsStore(ex.Store)
		require.True(t, ok)
		_, err = cvStore.PutSteering(context.Background(), aLegRec.ALegID, conversationview.PutSteeringRequest{
			OverlayID:           "alg-rec",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: "recovery instruction"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "loop_guard_recovery",
		})
		require.NoError(t, err)

		// Finish with ALLOW_STOP
		_, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
		require.NoError(t, err)

		snap, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
		require.NoError(t, err)

		// Active snapshot must have zero steering overlays after deactivation.
		// On current production: deactivation was never called -> RED.
		assert.Empty(t, snap.Steering, "overlay must be deactivated on final ALLOW_STOP")
	})

	t.Run("cancel_deactivates_overlay", func(t *testing.T) {
		t.Parallel()
		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"},
		}
		ex, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
		aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-deact-cancel-key")
		require.NoError(t, err)
		rs.facts.aLegID = aLegRec.ALegID

		cvStore, ok := conversationview.AsStore(ex.Store)
		require.True(t, ok)
		_, err = cvStore.PutSteering(context.Background(), aLegRec.ALegID, conversationview.PutSteeringRequest{
			OverlayID:           "alg-rec",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: "recovery instruction"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "loop_guard_recovery",
		})
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, _ = testRecvOne(ctx, rs, lipapi.Event{Kind: lipapi.EventResponseFinished})

		snap, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
		require.NoError(t, err)
		assert.Empty(t, snap.Steering, "overlay must be deactivated on cancellation")
	})

	t.Run("budget_exhaustion_deactivates_overlay", func(t *testing.T) {
		t.Parallel()
		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "endless work", Reason: "loop"},
		}
		ex := TestExecutor()
		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		ex.Store = store
		aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-deact-exhaust-key")
		require.NoError(t, err)

		cvStore, ok := conversationview.AsStore(ex.Store)
		require.True(t, ok)
		_, err = cvStore.PutSteering(context.Background(), aLegRec.ALegID, conversationview.PutSteeringRequest{
			OverlayID:           "alg-rec",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: "recovery instruction"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "loop_guard_recovery",
		})
		require.NoError(t, err)

		ex.LoopGuardFactory = NewLoopGuardFactory(stopgate.Ports{Verifier: fv, Now: time.Now}, stopgate.Config{
			Enabled:                  true,
			ExplicitCompletionPolicy: stopguard.PolicyTrust,
			MaxSemanticContinuations: 1,
			NoProgressLimit:          2,
		})
		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{
					ID:         "exhaust-call",
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				},
				aLegID:  aLegRec.ALegID,
				traceID: "trace-exhaust",
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-exhaust-1", Seq: 1}, routing.AttemptCandidate{
				Key:     "openai:gpt-4",
				Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
			}, authorityLifecycle{}),
			responsePipeline: &responsePipeline{},
		}
		bindTestRuntimeOwners(rs, ex)

		execSetupGuardContinuationOpener(t, rs, []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "b2 text"},
			{Kind: lipapi.EventResponseFinished, FinishReason: "raw"},
		})

		// B1 finish -> B2 opened (1st continuation)
		_, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})
		require.NoError(t, err)

		// B2 finish -> budget exhausted -> should deactivate overlay
		_, err = rs.Recv(context.Background())
		require.NoError(t, err)

		snap, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
		require.NoError(t, err)
		assert.Empty(t, snap.Steering, "overlay must be deactivated on continuation budget exhaustion")
	})

	t.Run("open_failure_deactivates_overlay", func(t *testing.T) {
		t.Parallel()
		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"},
		}
		ex, rs, _ := setupGuardedStreamForHoldback(t, fv, true)
		aLegRec, err := ex.Store.CreateALeg(context.Background(), "steering-deact-open-fail-key")
		require.NoError(t, err)
		rs.facts.aLegID = aLegRec.ALegID

		cvStore, ok := conversationview.AsStore(ex.Store)
		require.True(t, ok)
		_, err = cvStore.PutSteering(context.Background(), aLegRec.ALegID, conversationview.PutSteeringRequest{
			OverlayID:           "alg-rec",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: "recovery instruction"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "loop_guard_recovery",
		})
		require.NoError(t, err)

		// Opener returns error
		if rs.recovery == nil {
			rs.recovery = &recoveryController{}
		}
		rs.recovery.opener = func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
			return replacementOpenResult{}, errors.New("backend connection refused")
		}

		_, _ = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished})

		snap, err := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
		require.NoError(t, err)
		assert.Empty(t, snap.Steering, "overlay must be deactivated on continuation leg open failure")
	})
}

// Point 6b: Stale external-ingress cleanup before snapshot freeze.
// Requirements: 6.14, 12.14.
func TestAgentLoopGuard_Steering_StaleExternalIngressCleanup(t *testing.T) {
	t.Parallel()

	t.Run("stale_overlay_cleaned_before_new_turn_snapshot", func(t *testing.T) {
		t.Parallel()

		memStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		cvStore := memStore.ConversationViewStore()
		ctx := context.Background()

		// Simulate pre-existing A-leg
		aLegRecord, err := memStore.CreateALeg(ctx, "stale-key-1")
		require.NoError(t, err)
		aLegID := aLegRecord.ALegID

		// Simulate crash/restart leaving an active "alg-rec" overlay in durable store
		_, err = cvStore.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID: "alg-rec",
			Message: conversationview.StoredMessageV1{
				Role: lipapi.RoleDeveloper,
				Text: "<automated-recovery>stale leftover from previous crashed turn</automated-recovery>",
			},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "loop_guard_recovery",
		})
		require.NoError(t, err)

		snapBefore, err := cvStore.Snapshot(ctx, aLegID)
		require.NoError(t, err)
		require.Len(t, snapBefore.Steering, 1, "store must contain stale active overlay before new turn")

		// Now a new external turn arrives on the same A-leg
		ex := TestExecutor()
		ex.Store = memStore
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
		ex.Rand = routing.NewSeededRng(1)
		ex.Now = func() time.Time { return time.Unix(6000, 0) }

		newTurnCall := &lipapi.Call{
			Session:  lipapi.SessionRef{ALegID: aLegID},
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("new user turn")}}},
		}

		pr, _, cleanup, err := ex.prepareRequest(execDetachedCtx(ctx), newTurnCall)
		require.NoError(t, err)
		defer cleanup()

		// The new turn's frozen snapshot MUST NOT contain the stale recovery overlay.
		// On current production: stale cleanup is not called on ingress -> RED.
		assert.Empty(t, pr.conversationSnapshot.Steering, "new turn initial snapshot must be free of stale recovery steering")

		// Store itself must also show overlay deactivated.
		snapAfter, err := cvStore.Snapshot(ctx, aLegID)
		require.NoError(t, err)
		assert.Empty(t, snapAfter.Steering, "stale recovery overlay must be deactivated in store on external turn ingress")
	})

	t.Run("stale_cleanup_noop_when_absent_or_already_inactive", func(t *testing.T) {
		t.Parallel()

		memStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		ctx := context.Background()

		ex := TestExecutor()
		ex.Store = memStore
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
		ex.Rand = routing.NewSeededRng(1)
		ex.Now = func() time.Time { return time.Unix(7000, 0) }

		call := &lipapi.Call{
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("fresh turn")}}},
		}

		pr, _, cleanup, err := ex.prepareRequest(execDetachedCtx(ctx), call)
		require.NoError(t, err, "cleanup on absent overlay must succeed cleanly as a no-op")
		defer cleanup()
		assert.Empty(t, pr.conversationSnapshot.Steering)
	})
}

// Point 8: Candidate unsupported role/placement rejection and exact final Reassert after attempted mutation.
// Requirements: 6.11, 6.16, 12.12.
func TestAgentLoopGuard_Steering_CandidateCapabilityRejectionAndReassert(t *testing.T) {
	t.Parallel()

	t.Run("reassert_restores_exact_once_steering_after_attempt_transforms", func(t *testing.T) {
		t.Parallel()

		userMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("user prompt")}}
		anchorID, err := conversationview.MessageIdentityOf(userMsg)
		require.NoError(t, err)
		anchor := conversationview.MessageAnchor{Identity: anchorID, Occurrence: 1}

		instrText := continuationsafety.BuildRecoveryInstruction(continuationsafety.RecoveryInput{
			Reason:             "uncompleted work",
			RemainingObjective: "finish calculations",
			Attempt:            1,
			MaxAttempts:        3,
		})
		ov := conversationview.SteeringOverlay{
			OverlayID: "alg-rec", Revision: 1, SlotOrdinal: 1, Active: true,
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: instrText},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              "loop_guard_recovery",
		}
		snap := conversationview.Snapshot{StateRevision: 1, Steering: []conversationview.SteeringOverlay{ov}}

		clientCall := lipapi.Call{
			Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("system prompt")}}},
			Messages:     []lipapi.Message{userMsg},
		}

		// Initial projection produces baseline with exact provenance
		baseline, ev, err := conversationview.Project(clientCall, snap)
		require.NoError(t, err)
		require.Len(t, ev.Provenance, 1)

		// Simulate a late attempt transform that deletes the steering message
		mutatedCall := lipapi.CloneCall(baseline)
		var cleanedMsgs []lipapi.Message
		for _, m := range mutatedCall.Messages {
			if m.Role == lipapi.RoleDeveloper && strings.Contains(m.Parts[0].Text, "<automated-recovery>") {
				continue // strip steering
			}
			cleanedMsgs = append(cleanedMsgs, m)
		}
		mutatedCall.Messages = cleanedMsgs

		// Reassert must restore the steering overlay exactly once at its resolved placement
		filteredBaseline, _ := conversationview.FilterNeverBackend(clientCall, snap)
		reasserted, _, err := conversationview.Reassert(mutatedCall, snap, ev.Provenance, filteredBaseline)
		require.NoError(t, err)

		count := 0
		for _, m := range reasserted.Messages {
			if m.Role == lipapi.RoleDeveloper && strings.Contains(m.Parts[0].Text, "<automated-recovery>") {
				count++
			}
		}
		for _, m := range reasserted.Instructions {
			if m.Role == lipapi.RoleDeveloper && strings.Contains(m.Parts[0].Text, "<automated-recovery>") {
				count++
			}
		}
		assert.Equal(t, 1, count, "Reassert must restore exact-once steering injection after attempt transform stripped it")
	})

	t.Run("adaptation_verification_rejects_relocated_steering", func(t *testing.T) {
		t.Parallel()

		userMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("user prompt")}}
		anchorID, err := conversationview.MessageIdentityOf(userMsg)
		require.NoError(t, err)
		anchor := conversationview.MessageAnchor{Identity: anchorID, Occurrence: 1}

		ov := conversationview.SteeringOverlay{
			OverlayID: "alg-rec", Revision: 1, SlotOrdinal: 1, Active: true,
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: "recovery steer"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              "loop_guard_recovery",
		}
		snap := conversationview.Snapshot{StateRevision: 1, Steering: []conversationview.SteeringOverlay{ov}}

		clientCall := lipapi.Call{
			Messages: []lipapi.Message{
				userMsg,
				{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("a1")}},
				{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("u2")}},
			},
		}

		baseline, ev, err := conversationview.Project(clientCall, snap)
		require.NoError(t, err)

		// Candidate adaptation illegally moves steering to tail of messages
		adaptedMoved := lipapi.CloneCall(baseline)
		var steeringMsg lipapi.Message
		var nonSteeringMsgs []lipapi.Message
		for _, m := range adaptedMoved.Messages {
			if m.Role == lipapi.RoleDeveloper && strings.Contains(m.Parts[0].Text, "recovery steer") {
				steeringMsg = m
			} else {
				nonSteeringMsgs = append(nonSteeringMsgs, m)
			}
		}
		adaptedMoved.Messages = append(nonSteeringMsgs, steeringMsg) // moved to tail

		err = conversationview.VerifyAdaptationPreservesProjection(baseline, adaptedMoved, snap, ev.Provenance)
		require.Error(t, err, "adaptation moving steering to tail must be rejected by VerifyAdaptationPreservesProjection")
	})

	t.Run("unsupported_candidate_backend_rejected", func(t *testing.T) {
		t.Parallel()

		// Backend with zero capabilities (no developer role support)
		unsupportedBackend := execbackend.Backend{
			Caps: lipapi.NewBackendCaps(), // Empty caps
		}
		ex := TestExecutor()
		memStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		ex.Store = memStore
		ex.Backends = map[string]execbackend.Backend{"unsupported": unsupportedBackend}

		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "cont"},
		}
		ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

		aLegRec, err := memStore.CreateALeg(context.Background(), "unsupported-cand-key")
		require.NoError(t, err)

		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{
					ID:         "unsupported-call",
					Route:      lipapi.RouteIntent{Selector: "unsupported:model"},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				},
				aLegID:  aLegRec.ALegID,
				traceID: "trace-unsupported",
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-unsup-1", Seq: 1}, routing.AttemptCandidate{
				Key:     "unsupported:model",
				Primary: routing.Primary{Backend: "unsupported", Model: "model"},
			}, authorityLifecycle{}),
			responsePipeline: &responsePipeline{},
		}
		bindTestRuntimeOwners(rs, ex)

		// Trigger B1 finish -> verifier returns CONTINUE -> tryGuardContinuation attempts to open unsupported candidate -> candidate admission fails -> deactivates overlay -> returns fallback
		_, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
		require.NoError(t, err)

		cvStore := memStore.ConversationViewStore()
		snap, _ := cvStore.Snapshot(context.Background(), aLegRec.ALegID)
		assert.Empty(t, snap.Steering, "overlay must be deactivated on candidate open failure")
	})
}

// Point 2b: Pre-existing active overlay before ALG Put is preserved and projected exact-once (no duplication).
// Requirements: 6.9, 6.10, 12.11.
func TestAgentLoopGuard_Steering_ExistingActiveOverlayBeforeAlgPut(t *testing.T) {
	t.Parallel()

	memStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ctx := context.Background()

	aLegRec, err := memStore.CreateALeg(ctx, "existing-overlay-key")
	require.NoError(t, err)
	aLegID := aLegRec.ALegID

	cvStore := memStore.ConversationViewStore()

	// Seed pre-existing active steering overlay before turn begins
	_, err = cvStore.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "existing-guide",
		Message: conversationview.StoredMessageV1{
			Role: lipapi.RoleDeveloper,
			Text: "Pre-existing system guidance",
		},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "system_guide",
	})
	require.NoError(t, err)

	userMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("user query")}}
	ingressCall := lipapi.Call{
		ID:         "ingress-call-with-guide",
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages:   []lipapi.Message{userMsg},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
	}

	fv := &fakeGuardVerifier{
		verdict: stopguard.Verdict{
			Kind:               stopguard.VerdictContinue,
			RemainingObjective: "finish computation",
			Reason:             "uncompleted",
		},
	}

	ex := TestExecutor()
	ex.Store = memStore
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline:    lipapi.CloneCall(ingressCall),
			ingressCall: lipapi.CloneCall(ingressCall),
			aLegID:      aLegID,
			traceID:     "trace-existing-overlay",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-exist-1", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{},
	}
	bindTestRuntimeOwners(rs, ex)

	var b2ReceivedCall lipapi.Call
	if rs.recovery == nil {
		rs.recovery = &recoveryController{}
	}
	rs.recovery.opener = func(octx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		b2ReceivedCall = req.facts.call
		blegID := "b-exist-2"
		seq := 2
		bleg := b2bua.BLegRecord{BLegID: blegID, Seq: seq, ALegID: rs.facts.aLegID}
		cand := routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}}
		stream := &guardContinuationEventStream{events: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "b2 response"},
			{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
		}}
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

	// Trigger B1 finish -> verifier triggers CONTINUE -> ALG Put adds "alg-rec"
	ev, err := testRecvOne(ctx, rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
	require.NoError(t, err)
	assert.Equal(t, lipapi.EventTextDelta, ev.Kind)
	assert.Equal(t, "b2 response", ev.Delta)

	// Verify Snapshot N+1 has both overlays
	snap, err := cvStore.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snap.Steering, 2, "Snapshot N+1 must contain both existing-guide and alg-rec")

	// Verify B2 received call has both overlays projected exactly once (no duplicates)
	guideCount := 0
	algRecCount := 0
	for _, m := range b2ReceivedCall.Messages {
		if m.Role == lipapi.RoleDeveloper && strings.Contains(m.Parts[0].Text, "Pre-existing system guidance") {
			guideCount++
		}
		if m.Role == lipapi.RoleDeveloper && strings.Contains(m.Parts[0].Text, "<automated-recovery>") {
			algRecCount++
		}
	}
	for _, m := range b2ReceivedCall.Instructions {
		if m.Role == lipapi.RoleDeveloper && strings.Contains(m.Parts[0].Text, "Pre-existing system guidance") {
			guideCount++
		}
		if m.Role == lipapi.RoleDeveloper && strings.Contains(m.Parts[0].Text, "<automated-recovery>") {
			algRecCount++
		}
	}
	assert.Equal(t, 1, guideCount, "pre-existing overlay must be injected exactly once in B2 call")
	assert.Equal(t, 1, algRecCount, "alg-rec overlay must be injected exactly once in B2 call")
}

// Point 3b: Fail-closed deactivation error handling when steering store returns real error.
// Requirements: 6.13, 12.13.
func TestAgentLoopGuard_Steering_DeactivationErrorHandling_FailClosed(t *testing.T) {
	t.Parallel()

	t.Run("allow_stop_deactivation_failure_fails_closed", func(t *testing.T) {
		t.Parallel()

		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		aLegRec, err := store.CreateALeg(context.Background(), "deact-fail-key")
		require.NoError(t, err)
		aLegID := aLegRec.ALegID

		failingStore := &failingDeactivateStore{
			Store:    store.ConversationViewStore(),
			deactErr: errors.New("durable store disk error during deactivation"),
		}

		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "done"},
		}
		ex := TestExecutor()
		ex.Store = store
		ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{
					ID:         "deact-fail-call",
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				},
				aLegID:  aLegID,
				traceID: "trace-deact-fail",
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-deact-1", Seq: 1}, routing.AttemptCandidate{
				Key:     "openai:gpt-4",
				Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
			}, authorityLifecycle{}),
			responsePipeline: &responsePipeline{},
		}
		bindTestRuntimeOwners(rs, ex)
		rs.terminal.steeringStore = failingStore

		// On ALLOW_STOP, terminalizeTurn attempts deactivateGuardOverlay; failingStore returns real store error.
		// Runtime must fail closed (return partial error).
		_, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
		require.Error(t, err, "deactivation failure on ALLOW_STOP must fail closed")
		assert.Contains(t, err.Error(), "durable store disk error")
	})

	t.Run("unhandled_hold_deactivation_failure_fails_closed", func(t *testing.T) {
		t.Parallel()

		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		aLegRec, err := store.CreateALeg(context.Background(), "deact-fail-unhandled-key")
		require.NoError(t, err)
		aLegID := aLegRec.ALegID

		failingStore := &failingDeactivateStore{
			Store:    store.ConversationViewStore(),
			deactErr: errors.New("store deactivation timeout"),
		}

		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "cont"},
		}
		ex := TestExecutor()
		ex.Store = store
		ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{
					ID:         "deact-fail-unhandled-call",
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				},
				aLegID:  aLegID,
				traceID: "trace-deact-fail-unhandled",
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-deact-unhandled-1", Seq: 1}, routing.AttemptCandidate{
				Key:     "openai:gpt-4",
				Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
			}, authorityLifecycle{}),
			responsePipeline: &responsePipeline{},
		}
		bindTestRuntimeOwners(rs, ex)
		rs.terminal.steeringStore = failingStore

		// Opener is nil -> unhandled hold -> deactivates overlay -> store returns real error -> fails closed
		_, err = testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
		require.Error(t, err, "deactivation failure on unhandled hold must fail closed")
		assert.Contains(t, err.Error(), "store deactivation timeout")
	})
}

// Point 8: Failure ordering when Put / Snapshot / Project / Open fails:
// B1 remains swallowed, overlay is not active, controlled failure to A-side, no B2.
func TestAgentLoopGuard_Steering_FailureOrdering(t *testing.T) {
	t.Parallel()

	t.Run("put_failure_swallows_b1_and_returns_controlled_fallback", func(t *testing.T) {
		t.Parallel()

		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		aLegRec, err := store.CreateALeg(context.Background(), "fail-put-key")
		require.NoError(t, err)
		aLegID := aLegRec.ALegID

		failingStore := &failingPutStore{
			Store:  store.ConversationViewStore(),
			putErr: errors.New("store put failed"),
		}

		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "cont"},
		}
		ex := TestExecutor()
		ex.Store = store
		ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{
					ID:         "fail-put-call",
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				},
				aLegID:  aLegID,
				traceID: "trace-fail-put",
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-fail-put-1", Seq: 1}, routing.AttemptCandidate{
				Key:     "openai:gpt-4",
				Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
			}, authorityLifecycle{}),
			responsePipeline: &responsePipeline{},
		}
		bindTestRuntimeOwners(rs, ex)
		rs.terminal.steeringStore = failingStore
		execSetupGuardContinuationOpener(t, rs, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "b2"}})

		// B1 finish -> Put fails -> B1 is swallowed -> controlled fallback emitted to A-side -> overlay not active
		ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
		require.NoError(t, err)
		assert.Equal(t, lipapi.EventResponseFinished, ev.Kind)
		assert.Equal(t, guardContinuationPendingReason, ev.FinishReason)

		snap, err := store.ConversationViewStore().Snapshot(context.Background(), aLegID)
		require.NoError(t, err)
		assert.Empty(t, snap.Steering, "overlay must not be active after Put failure")
	})

	t.Run("snapshot_failure_swallows_b1_and_deactivates_overlay", func(t *testing.T) {
		t.Parallel()

		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		aLegRec, err := store.CreateALeg(context.Background(), "fail-snap-key")
		require.NoError(t, err)
		aLegID := aLegRec.ALegID

		failingReader := &failingSnapshotReaderStore{
			Store:   store.ConversationViewStore(),
			snapErr: errors.New("snapshot query timeout"),
		}

		fv := &fakeGuardVerifier{
			verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "cont"},
		}
		ex := TestExecutor()
		ex.Store = store
		ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

		rs := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: lipapi.Call{
					ID:         "fail-snap-call",
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				},
				aLegID:  aLegID,
				traceID: "trace-fail-snap",
			}),
			attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-fail-snap-1", Seq: 1}, routing.AttemptCandidate{
				Key:     "openai:gpt-4",
				Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
			}, authorityLifecycle{}),
			responsePipeline: &responsePipeline{},
		}
		bindTestRuntimeOwners(rs, ex)
		rs.terminal.conversationReader = failingReader
		execSetupGuardContinuationOpener(t, rs, []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "b2"}})

		// B1 finish -> Snapshot fails -> B1 swallowed -> overlay deactivated -> controlled fallback to A-side
		ev, err := testRecvOne(context.Background(), rs, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"})
		require.NoError(t, err)
		assert.Equal(t, lipapi.EventResponseFinished, ev.Kind)
		assert.Equal(t, guardContinuationPendingReason, ev.FinishReason)

		snap, err := store.ConversationViewStore().Snapshot(context.Background(), aLegID)
		require.NoError(t, err)
		assert.Empty(t, snap.Steering, "overlay must be deactivated after snapshot failure")
	})
}

// Point 5: Exact Stale Ingress Cleanup call counts and isolation.
// Requirements: 6.14, 12.14.
func TestAgentLoopGuard_Steering_StaleCleanupCallCountsAndIsolation(t *testing.T) {
	t.Parallel()

	t.Run("active_alg_rec_triggers_exactly_one_deactivate_call", func(t *testing.T) {
		t.Parallel()

		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		ctx := context.Background()
		aLegRec, err := store.CreateALeg(ctx, "count-active-key")
		require.NoError(t, err)
		aLegID := aLegRec.ALegID

		// Put active alg-rec overlay
		_, err = store.ConversationViewStore().PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "alg-rec",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: "stale instruction"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "loop_guard_recovery",
		})
		require.NoError(t, err)

		countingStore := &countingSteeringStore{
			Store: store.ConversationViewStore(),
		}

		ex := TestExecutor()
		ex.Store = &delegatingStoreWithCV{MemoryStore: store, cvStore: countingStore}
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})

		call := &lipapi.Call{
			Session:  lipapi.SessionRef{ALegID: aLegID},
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("fresh turn")}}},
		}

		pr, _, cleanup, err := ex.prepareRequest(execDetachedCtx(ctx), call)
		require.NoError(t, err)
		defer cleanup()

		assert.Equal(t, 1, countingStore.deactivateCalls, "active alg-rec must trigger exactly 1 DeactivateSteering call")
		assert.Empty(t, pr.conversationSnapshot.Steering, "snapshot must be clean")
	})

	t.Run("absent_alg_rec_triggers_zero_deactivate_calls", func(t *testing.T) {
		t.Parallel()

		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		ctx := context.Background()
		aLegRec, err := store.CreateALeg(ctx, "count-absent-key")
		require.NoError(t, err)
		aLegID := aLegRec.ALegID

		countingStore := &countingSteeringStore{
			Store: store.ConversationViewStore(),
		}

		ex := TestExecutor()
		ex.Store = &delegatingStoreWithCV{MemoryStore: store, cvStore: countingStore}
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})

		call := &lipapi.Call{
			Session:  lipapi.SessionRef{ALegID: aLegID},
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("fresh turn")}}},
		}

		pr, _, cleanup, err := ex.prepareRequest(execDetachedCtx(ctx), call)
		require.NoError(t, err)
		defer cleanup()

		assert.Equal(t, 0, countingStore.deactivateCalls, "absent alg-rec must trigger 0 DeactivateSteering calls")
		assert.Empty(t, pr.conversationSnapshot.Steering)
	})

	t.Run("detached_verifier_session_does_not_cleanup_stale_overlay", func(t *testing.T) {
		t.Parallel()

		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		ctx := context.Background()
		aLegRec, err := store.CreateALeg(ctx, "count-verifier-key")
		require.NoError(t, err)
		aLegID := aLegRec.ALegID

		// Put active alg-rec overlay
		_, err = store.ConversationViewStore().PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "alg-rec",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: "recovery instruction"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "loop_guard_recovery",
		})
		require.NoError(t, err)

		countingStore := &countingSteeringStore{
			Store: store.ConversationViewStore(),
		}

		ex := TestExecutor()
		ex.Store = &delegatingStoreWithCV{MemoryStore: store, cvStore: countingStore}
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})

		// Suppressed plugin simulates detached verifier turn
		suppressedCtx := execctx.WithSuppressedPluginIDs(ctx, []string{"agent_loop_guard"})
		suppressedCtx = execctx.WithDetachedSession(suppressedCtx, execctx.DetachedSession{ParentALegID: aLegID})

		call := &lipapi.Call{
			Session:  lipapi.SessionRef{ALegID: aLegID},
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("verifier prompt")}}},
		}

		_, _, cleanup, err := ex.prepareRequest(suppressedCtx, call)
		require.NoError(t, err)
		defer cleanup()

		assert.Equal(t, 0, countingStore.deactivateCalls, "detached verifier session must not clean stale overlay")
	})

	t.Run("deactivate_error_on_stale_cleanup_fails_closed", func(t *testing.T) {
		t.Parallel()

		store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		require.NoError(t, err)
		ctx := context.Background()
		aLegRec, err := store.CreateALeg(ctx, "fail-stale-deact-key")
		require.NoError(t, err)
		aLegID := aLegRec.ALegID

		// Put active alg-rec overlay
		_, err = store.ConversationViewStore().PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "alg-rec",
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleDeveloper, Text: "stale instruction"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "loop_guard_recovery",
		})
		require.NoError(t, err)

		failingStore := &failingDeactivateStore{
			Store:    store.ConversationViewStore(),
			deactErr: errors.New("store deactivation failure on ingress"),
		}

		ex := TestExecutor()
		ex.Store = &delegatingStoreWithCV{MemoryStore: store, cvStore: failingStore}
		ex.Bus = hooks.New(hooks.Config{})
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})

		call := &lipapi.Call{
			Session:  lipapi.SessionRef{ALegID: aLegID},
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("fresh turn")}}},
		}

		_, _, cleanup, err := ex.prepareRequest(execDetachedCtx(ctx), call)
		if cleanup != nil {
			defer cleanup()
		}
		require.Error(t, err, "stale cleanup deactivation failure must fail closed")
		assert.Contains(t, err.Error(), "store deactivation failure on ingress")
	})
}

// Test helper stores for deterministic failure testing

type failingDeactivateStore struct {
	conversationview.Store
	deactErr error
}

func (f *failingDeactivateStore) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringState, error) {
	if f.deactErr != nil {
		return conversationview.SteeringState{}, f.deactErr
	}
	return f.Store.DeactivateSteering(ctx, aLegID, overlayID)
}

type failingPutStore struct {
	conversationview.Store
	putErr error
}

func (f *failingPutStore) PutSteering(ctx context.Context, aLegID string, req conversationview.PutSteeringRequest) (conversationview.SteeringState, error) {
	if f.putErr != nil {
		return conversationview.SteeringState{}, f.putErr
	}
	return f.Store.PutSteering(ctx, aLegID, req)
}

type failingSnapshotReaderStore struct {
	conversationview.Store
	snapErr error
}

func (f *failingSnapshotReaderStore) Snapshot(ctx context.Context, aLegID string) (conversationview.Snapshot, error) {
	if f.snapErr != nil {
		return conversationview.Snapshot{}, f.snapErr
	}
	return f.Store.Snapshot(ctx, aLegID)
}

type countingSteeringStore struct {
	conversationview.Store
	deactivateCalls int
}

func (c *countingSteeringStore) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringState, error) {
	c.deactivateCalls++
	return c.Store.DeactivateSteering(ctx, aLegID, overlayID)
}

type delegatingStoreWithCV struct {
	*b2bua.MemoryStore
	cvStore conversationview.Store
}

func (d *delegatingStoreWithCV) ConversationViewStore() conversationview.Store {
	return d.cvStore
}

func (d *delegatingStoreWithCV) Snapshot(ctx context.Context, aLegID string) (conversationview.Snapshot, error) {
	return d.cvStore.Snapshot(ctx, aLegID)
}

func (d *delegatingStoreWithCV) PutSteering(ctx context.Context, aLegID string, req conversationview.PutSteeringRequest) (conversationview.SteeringState, error) {
	return d.cvStore.PutSteering(ctx, aLegID, req)
}

func (d *delegatingStoreWithCV) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringState, error) {
	return d.cvStore.DeactivateSteering(ctx, aLegID, overlayID)
}

func (d *delegatingStoreWithCV) TagNeverBackend(ctx context.Context, aLegID string, tags []conversationview.TagRequest) (conversationview.TagResult, error) {
	return d.cvStore.TagNeverBackend(ctx, aLegID, tags)
}
