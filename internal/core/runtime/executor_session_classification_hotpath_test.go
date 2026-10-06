package runtime

// Task 9.3 runtime hot-path ratchets for session classification.
//
// Requirements 10.8, 11.7 and 5.1, measured at the generic-core stage boundary
// where an absent plane must cost nothing and a present plane must not change
// what the request does.
//
// The absent-plane ratchet is exact: with no classifier plane the stage returns
// before it touches the caller, so it must allocate nothing at all. That is the
// strongest statement requirement 10.8 can carry, and it is only credible
// because the same test drives the identical stage with a plane and requires it
// to do observable work.

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// hotPathWorkspaceID is the workspace the hot-path stage fixtures resolve. The
// stage itself receives the resolved view directly, so this only keeps the
// snapshot well-formed.
const hotPathWorkspaceID = "workspace-hot-path"

// hotPathSessionID is the authoritative session the hot-path stage fixtures bind.
const hotPathSessionID = "sess-hot-path-9-3"

type hotPathWorkspaceResolver struct{}

func (hotPathWorkspaceResolver) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{ID: hotPathWorkspaceID}, nil
}

func contextForHotPath() context.Context { return context.Background() }

// hotPathStageSink keeps the measured stage call from being optimized away.
var hotPathStageSink session.Classification

// hotPathStageExecutor builds the minimal executor the classification stage
// needs. The stage reads only the runtime snapshot, the bound turn and the call,
// so no session store, backend, or authority layer participates.
func hotPathStageExecutor(tb testing.TB, bundle testFeatureBundle) *Executor {
	tb.Helper()
	ex := TestExecutor()
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]lipworkspace.Resolver{hotPathWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(bundle),
	})
	return ex
}

// hotPathBoundTurn is a fully bound identity turn: authoritative session, A-leg,
// turn ID, and resolved workspace. Requirement 4.1 requires exactly these to be
// available before classification runs, so a fixture missing any of them would
// exercise the wrong path.
func hotPathBoundTurn() *identityBoundTurn {
	return &identityBoundTurn{
		traceID: "trace-hot-path-9-3",
		preSession: session.SessionView{
			AuthoritativeSessionID: hotPathSessionID,
			ALegID:                 "aleg-hot-path-9-3",
			TurnID:                 "turn-hot-path-9-3",
			WorkspaceID:            "workspace-hot-path",
		},
		workspace: lipworkspace.WorkspaceView{ID: "workspace-hot-path"},
	}
}

// hotPathClassificationCall is a canonical client turn carrying the widest bounded
// metadata the stage may derive evidence from: an accepted coding-harness
// identity and a decisive tool catalog.
func hotPathClassificationCall() *lipapi.Call {
	call := classificationCall()
	call.Tools = append(call.Tools,
		lipapi.ToolDef{Name: "write_file"},
		lipapi.ToolDef{Name: "search_files"},
	)
	return call
}

// TestSessionClassificationAbsentPlaneCostsNothing is the exact 10.8 / 11.7
// ratchet: with no classifier plane the stage performs no evaluation and
// allocates nothing at all.
//
// Zero allocations is the strongest statement 10.8 can carry, and it is only
// credible because the SAME stage with a plane is measured in the same test and
// its classifier invocation count is required to be exactly one per turn. That
// count is the discriminating instrument: a stage that had been short-circuited
// for some other reason would report zero invocations on BOTH paths.
func TestSessionClassificationAbsentPlaneCostsNothing(t *testing.T) {
	const turns = 256
	call := hotPathClassificationCall()

	exPlaneFree := hotPathStageExecutor(t, testFeatureBundle{})
	if exPlaneFree.RuntimeSnapshot.SessionClassifier() != nil {
		t.Fatal("the disabled-generation fixture still published a classifier plane")
	}
	if got := testing.AllocsPerRun(turns, func() {
		turn := hotPathBoundTurn()
		exPlaneFree.runSessionClassificationStage(contextForHotPath(), call, turn)
		hotPathStageSink = turn.preSession.Classification
	}); got != 0 {
		t.Fatalf("the session-classification stage allocated %.2f objects per turn with no classifier plane, want exactly 0 (requirements 10.8, 11.7)", got)
	}

	// Behavioral half: the classification stays unknown and the bound request
	// survives the stage untouched (requirement 11.7).
	turn := hotPathBoundTurn()
	exPlaneFree.runSessionClassificationStage(contextForHotPath(), call, turn)
	if got := turn.preSession.Classification; got != (session.Classification{}) {
		t.Fatalf("classification with no plane = %+v, want unknown", got)
	}
	if turn.preSession.AuthoritativeSessionID != hotPathSessionID {
		t.Fatalf("the stage disturbed the bound session view: %+v", turn.preSession)
	}

	// Control: with a plane the very same stage evaluates exactly once per turn.
	presentCalls := 0
	exPresent := hotPathStageExecutor(t, testFeatureBundle{
		SessionClassifier: newSpySessionClassifier("hot-path-present",
			func(context.Context, sessionclassification.Input) (session.Classification, error) {
				presentCalls++
				return codingAgentSessionClassification("hot_path.present", 1), nil
			}),
	})
	presentAllocs := testing.AllocsPerRun(turns, func() {
		presentTurn := hotPathBoundTurn()
		exPresent.runSessionClassificationStage(contextForHotPath(), call, presentTurn)
		hotPathStageSink = presentTurn.preSession.Classification
	})
	if presentCalls != turns+1 {
		t.Fatalf("the present-plane stage evaluated %d times over %d turns, want exactly one evaluation per turn; the absent-plane measurement cannot be attributed to an absent plane",
			presentCalls-1, turns)
	}
	if presentAllocs != 0 {
		t.Fatalf("the present-plane stage allocated %.2f objects per turn; the evaluation is expected to stay allocation-free, and the absent-plane zero would be uninformative against a noisy baseline",
			presentAllocs)
	}
	t.Logf("absent plane: 0 evaluations and exactly 0 allocations/turn; present plane: one evaluation per turn, %.2f allocations/turn",
		presentAllocs)
}

// hotPathMaxStageAllocs bounds what one classification stage evaluation may
// allocate, whatever the call looks like.
//
// Evidence construction reads a fixed number of bounded values: the canonical
// operation, the accepted client identity, and one category bit per declared tool.
// The ceiling is therefore a small fixed number rather than a function of body
// size. A scan that walked the message tree, the tool descriptions or the tool
// schemas would allocate once per part it touched.
const hotPathMaxStageAllocs = 16

// TestSessionClassificationPlaneKeepsTheWireLaneWireEligible is the 5.1 ratchet:
// the classifier's presence alone must not turn an otherwise wire-eligible large
// request into a canonical one.
//
// The discriminator is the execution itself: the wire lane commits to exactly one
// OpenWire and must never reach a canonical Open or a second execution. A plane
// that forced canonical materialization would have to construct the request to
// classify it, and it would show up here as a canonical backend call.
//
// The control is the SAME wire request with no classifier plane, so the difference
// between the two runs is the plane and nothing else.
func TestSessionClassificationPlaneKeepsTheWireLaneWireEligible(t *testing.T) {
	run := func(tb *testing.T, bundle testFeatureBundle) (wireOpens, canonicalOpens, commits int32) {
		tb.Helper()
		ex, metrics, _ := setupTestExecutor(tb)
		var wire, canonical atomic.Int32
		ex.Backends = map[string]execbackend.Backend{
			"default": {
				OpenWire: func(context.Context, largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					wire.Add(1)
					return lipapi.CloseOnlyManagedStream{Stream: lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseFinished},
					})}, nil
				},
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					canonical.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		}
		ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
			Workspace:     workspace.NewResolverChain([]lipworkspace.Resolver{hotPathWorkspaceResolver{}}),
			FeaturePlanes: freezeBundle(bundle),
		})

		src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
		assessment := makeTestAcceptedAssessment(tb, "gen-1", "openai-chat", src, true)
		principalCtx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-hot-path-wire"})
		ctx := largebody.ContextWithWireProof(principalCtx, wireClassificationProof(hotPathWireEvidence), "req-hot-path-wire")
		if _, err := ex.ExecuteLargeBody(ctx, assessment, src); err != nil {
			tb.Fatalf("ExecuteLargeBody: %v", err)
		}
		return wire.Load(), canonical.Load(), metrics.newCalls.Load()
	}

	// The classifier plane is present and evaluating: a spy that counts every call,
	// so a zero-cost claim cannot come from an unexercised plane.
	evaluations := 0
	recorder := &wireClassificationRecorder{}
	spy := newWireClassificationClassifier("hot-path-wire", recorder,
		func(in sessionclassification.Input) (session.Classification, error) {
			evaluations++
			if in.Evidence != hotPathWireEvidence {
				return session.Classification{}, errors.New("wire evidence was not derived from the proof")
			}
			return codingAgentSessionClassification("hot_path.wire", 1), nil
		})

	wireWithPlane, canonicalWithPlane, commitsWithPlane := run(t, testFeatureBundle{SessionClassifier: spy})
	if wireWithPlane != 1 {
		t.Fatalf("wire opens with the classifier plane = %d, want exactly 1", wireWithPlane)
	}
	if canonicalWithPlane != 0 {
		t.Fatalf("canonical opens with the classifier plane = %d, want 0: the plane must not make a wire-eligible request canonical-required (requirement 5.1)",
			canonicalWithPlane)
	}
	if commitsWithPlane != 1 {
		t.Fatalf("BeginTurn commits with the classifier plane = %d, want exactly 1 (no second execution)", commitsWithPlane)
	}
	if evaluations != 1 {
		t.Fatalf("classifier evaluations = %d on the committed wire turn, want exactly 1", evaluations)
	}
	observed := recorder.seen()
	if len(observed) != 1 {
		t.Fatalf("the wire stage handed the classifier %d inputs, want exactly 1", len(observed))
	}
	// The lane must classify from the proof's bounded evidence. A stage that
	// dropped it, or that reconstructed a request to obtain it, would leave the
	// classifier with nothing - the failure shape requirement 5.2 forbids.
	if observed[0].Evidence != hotPathWireEvidence {
		t.Fatalf("wire classification evidence = %+v, want the proof's bounded evidence %+v", observed[0].Evidence, hotPathWireEvidence)
	}
	if observed[0].Session.AuthoritativeSessionID == "" || observed[0].Session.ALegID == "" {
		t.Fatalf("wire classification ran before authority binding: %+v", observed[0].Session)
	}

	// Control: the SAME wire request with no classifier plane, so the difference
	// between the two runs is the plane and nothing else.
	wireWithoutPlane, canonicalWithoutPlane, _ := run(t, testFeatureBundle{})
	if wireWithoutPlane != 1 || canonicalWithoutPlane != 0 {
		t.Fatalf("control lane without the plane = (%d wire, %d canonical), want (1, 0)",
			wireWithoutPlane, canonicalWithoutPlane)
	}
	t.Logf("wire lane with the plane: %d wire opens, %d canonical opens; without the plane: %d wire opens, %d canonical opens",
		wireWithPlane, canonicalWithPlane, wireWithoutPlane, canonicalWithoutPlane)
}

// hotPathWireEvidence is the bounded evidence a certified frontend proof carries
// for a coding-agent turn: the canonical operation, the accepted client identity,
// and the decisive tool-category bits.
var hotPathWireEvidence = sessionclassification.Evidence{
	Operation:       lipapi.OperationOpenAIChatCompletions,
	ClientUserAgent: "codex-cli/0.42.0 (accepted)",
	ToolCategories: sessionclassification.ToolCategoryFileRead |
		sessionclassification.ToolCategoryFileEdit |
		sessionclassification.ToolCategoryOSCommand,
}

// TestSessionClassificationStageAllocationsAreIndependentOfCallSize is the 5.1 /
// 10.1 no-scan ratchet at the stage boundary. It makes two independent claims.
//
// The cost claim: a call padded with a large message tree, tool descriptions and
// tool schemas must cost the same bounded number of allocations as a small call,
// because requirements 3.8 and 10.1 forbid a transcript scan.
//
// The behaviour claim, which is what actually pins "no scan": the padded call's
// content is seeded with DECISIVE coding-agent evidence - recognized project
// marker names, the decisive canonical tool names, and an accepted
// coding-harness identity. If any part of the call's content reached the evidence,
// the derived evidence would differ. It does not, so the content provably never
// entered the decision.
func TestSessionClassificationStageAllocationsAreIndependentOfCallSize(t *testing.T) {
	small := hotPathClassificationCall()
	padded := hotPathPaddedClassificationCall()

	smallEvidence := sessionClassificationEvidence(small)
	paddedEvidence := sessionClassificationEvidence(padded)
	if paddedEvidence != smallEvidence {
		t.Fatalf("evidence from a padded call differs from the small call: %+v vs %+v; call content reached the bounded evidence (requirements 3.8, 5.1, 10.1)",
			paddedEvidence, smallEvidence)
	}

	smallAllocs := testing.AllocsPerRun(256, func() {
		evidenceSink = sessionClassificationEvidence(small)
	})
	paddedAllocs := testing.AllocsPerRun(256, func() {
		evidenceSink = sessionClassificationEvidence(padded)
	})
	if paddedAllocs > smallAllocs {
		t.Fatalf("evidence construction cost grew with call size: %.2f allocations for a padded call vs %.2f for a small one; bounded metadata only (requirements 3.8, 5.1, 10.1)",
			paddedAllocs, smallAllocs)
	}
	if paddedAllocs > hotPathMaxStageAllocs {
		t.Fatalf("evidence construction allocated %.2f objects for a padded call, above the %d ceiling (requirements 5.1, 10.1)",
			paddedAllocs, hotPathMaxStageAllocs)
	}

	// The whole stage, not just evidence construction: a stage that traversed the
	// message tree would pay for it here even if evidence construction stayed
	// bounded. A live classifier plane is present so the stage really runs, and it
	// must receive the identical bounded evidence every time.
	var seen []sessionclassification.Evidence
	stageCalls := 0
	stageExecutor := hotPathStageExecutor(t, testFeatureBundle{
		SessionClassifier: newSpySessionClassifier("hot-path-size",
			func(_ context.Context, in sessionclassification.Input) (session.Classification, error) {
				stageCalls++
				seen = append(seen, in.Evidence)
				return session.Classification{}, nil
			}),
	})
	smallStageAllocs := testing.AllocsPerRun(256, func() {
		turn := hotPathBoundTurn()
		stageExecutor.runSessionClassificationStage(contextForHotPath(), small, turn)
		hotPathStageSink = turn.preSession.Classification
	})
	paddedStageAllocs := testing.AllocsPerRun(256, func() {
		turn := hotPathBoundTurn()
		stageExecutor.runSessionClassificationStage(contextForHotPath(), padded, turn)
		hotPathStageSink = turn.preSession.Classification
	})
	if stageCalls == 0 {
		t.Fatal("the size-invariance stage never invoked the classifier")
	}
	if paddedStageAllocs > smallStageAllocs {
		t.Fatalf("stage cost grew with call size: %.2f allocations for a padded call vs %.2f for a small one; the stage must not traverse the transcript (requirements 3.8, 10.1)",
			paddedStageAllocs, smallStageAllocs)
	}
	if paddedStageAllocs > hotPathMaxStageAllocs {
		t.Fatalf("the stage allocated %.2f objects for a padded call, above the %d ceiling (requirements 5.1, 10.1)",
			paddedStageAllocs, hotPathMaxStageAllocs)
	}
	for _, evidence := range seen {
		if evidence != smallEvidence {
			t.Fatalf("the stage handed the classifier evidence derived from call content: %+v vs the bounded %+v",
				evidence, smallEvidence)
		}
	}

	// Control: genuinely size-dependent work over the SAME two calls must separate
	// them, proving these comparisons detect a content scan. Reading the text out of
	// every part is what a transcript traversal costs.
	smallScan := testing.AllocsPerRun(64, func() {
		transcriptSink = transcriptBytes(small)
	})
	paddedScan := testing.AllocsPerRun(64, func() {
		transcriptSink = transcriptBytes(padded)
	})
	if paddedScan <= smallScan {
		t.Fatalf("the size-dependence control did not separate a small call (%.2f allocations) from a padded one (%.2f); this ratchet cannot detect a content scan",
			smallScan, paddedScan)
	}
	t.Logf("evidence: %.2f allocations small, %.2f padded (ceiling %d); stage: %.2f small, %.2f padded; size-dependent control: %.2f small, %.2f padded",
		smallAllocs, paddedAllocs, hotPathMaxStageAllocs, smallStageAllocs, paddedStageAllocs, smallScan, paddedScan)
}

// hotPathPaddedClassificationCall is a canonical call padded with content a
// transcript scan would mistake for coding-agent evidence: recognized project
// marker names, the decisive canonical tool names, and an accepted coding-harness
// identity. None of it may reach the bounded evidence.
func hotPathPaddedClassificationCall() *lipapi.Call {
	call := hotPathClassificationCall()
	decisive := lipapi.TextPart(
		"this workspace has go.mod and pom.xml, so run read_file, then write_file, then execute_command, as codex_cli_rs/1.2.3")
	call.Messages = append(call.Messages,
		lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{decisive, lipapi.TextPart(largeTranscriptSentinel())}},
		lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart(largeTranscriptSentinel())}},
	)
	call.Instructions = []lipapi.Message{{
		Role:  lipapi.RoleSystem,
		Parts: []lipapi.Part{lipapi.TextPart(largeTranscriptSentinel())},
	}}
	// Tool NAMES are bounded metadata, so the decisive tool cluster legitimately
	// counts. DESCRIPTIONS and SCHEMAS are not, so padding both makes a scan over
	// them expensive while leaving the derived category bits unchanged.
	for i := range call.Tools {
		call.Tools[i].Description = decisive.Text + largeTranscriptSentinel()
		call.Tools[i].Parameters = []byte(decisive.Text + largeTranscriptSentinel())
	}
	return call
}

// evidenceSink and transcriptSink keep the measured closures observable.
var (
	evidenceSink   sessionclassification.Evidence
	transcriptSink string
)

// largeTranscriptSentinel is a payload big enough that scanning it would dominate
// the bounded evidence cost, and carrying no classification signal so it can only
// be detected by its cost or by its absence from the evidence.
//
// Mixed case on purpose. strings.ToLower has an allocation-free fast path for
// already-lowercase ASCII, so an all-lowercase sentinel would let a
// size-proportional transcript traversal allocate LESS than the small fixture and
// the size-invariance ratchet would pass on a scanning implementation. Uppercase
// bytes force the normalization to allocate, which is what makes the ratchet
// discriminating. This mirrors the note in
// internal/plugins/features/sessionclassification/hot_path_ratchet_test.go.
func largeTranscriptSentinel() string {
	buf := make([]byte, 64*1024)
	for i := range buf {
		if i%2 == 0 {
			buf[i] = byte('A' + i%26)
		} else {
			buf[i] = byte('a' + i%26)
		}
	}
	return string(buf)
}

// transcriptBytes models an implementation that walked the call's content: it
// materializes every message part's text into one retained string. It exists only
// as the control that makes the size-invariance ratchet discriminating.
func transcriptBytes(call *lipapi.Call) string {
	var out strings.Builder
	for _, message := range call.Messages {
		for _, part := range message.Parts {
			out.WriteString(part.Text)
		}
	}
	return out.String()
}
