package runtime_test

// Spec: b-leg-path-virtualization Task 5.2. Requirements 5.2, 5.4, 8.5.
//
// Design sections consulted: "Existing Architecture and Placement" (the fourteen-step
// backend-bound order and its consequences, steps 2, 3, 4, 6, 8, 9, 10),
// "Canonical Outbound Rewriter", "Testing Strategy / Runtime-integration",
// "Boundary Commitments / Out of Boundary", and "Revalidation Triggers".
//
// WHAT THIS TEST IS
//
// The runtime integration regression Task 1.3 could not write: Task 1.3 deliberately
// installed test-local stand-ins on the two outbound planes so that its ordering
// assertions stay about the runtime's own sequence, and it explicitly claims nothing
// about the concrete feature. This file installs the REAL feature contributions -
// outbound.AttemptTransform on the candidate attempt plane and outbound.RequestPartHook
// on the request-part-hook bus - and proves the one property design.md's consequence
// list states as a runtime obligation:
//
//   "runtime tests must prove conversation-view reassertion/adaptation do not restore
//   real path-bearing history before PTB/backend open"
//
// (requirements.md 5.2 and 5.4).
//
// WHY THE FIXTURE INTRODUCES A REAL PATH AFTER THE EARLY PASS
//
// A regression that only virtualizes in the early pass would be satisfied by the early
// pass alone and could not tell the late pass apart from a no-op. So the run installs
// one LATE REQUEST SHAPING participant that sorts BEFORE the feature hook inside the
// same request-part chain and puts a second real-root tool call onto the history. That
// is exactly the hazard design.md step 4 exists for: any later shaping between the
// candidate attempt stage and the backend-bound request may restore a real path, and
// only a pass that runs after all of it can catch it. The feature hook sorts last (a
// fixed order above every shipped request-part participant), so it runs after the
// shaper. With the hook installed the backend bound carries two virtualized surfaces;
// with the hook contributing nothing the second one reaches Backend.Open as a real
// path, which is what makes this regression RED without the contribution and GREEN
// with it.
//
// WHAT IT REUSES FROM TASK 1.3
//
// The executor harness: the checkpoint recorder, the frozen conversation-view reader
// and snapshot, the eligibility observer, the view observer, the backend recorder, and
// the stage-ordinal renderer. Task 1.3's assertions are untouched; this file adds only
// new helpers and new assertions in its own test function. The two thin marker types
// below exist because the real passes have nowhere to record a stage ordinal: each
// delegates every decision to the real contribution and adds nothing but the ordinal
// the shared monotonic counter needs.
//
// BOUNDARY
//
// design.md "Out of Boundary" removes conversation-view message visibility semantics,
// so the fixture keeps the same non-trivial frozen view Task 1.3 used and asserts only
// that its semantics survive both real passes. A real path that appears after the early
// pass is introduced by an explicit request-part participant on the test side, never by
// the runtime, and no production file under internal/core is touched.
//
// EXTENSION POINTS USED
//
//	1. candidate attempt transform            request.AttemptTransform.HandleAttempt
//	2. candidate sizing / context eligibility  runtime.EligibilityResolver.Check
//	3. late request shaping (fixture)          sdkhooks.RequestPartHook.HandleRequestParts
//	4. the feature's late pass under test      sdkhooks.RequestPartHook.HandleRequestParts
//	5. final conversation-view reassertion     runtime.ConversationViewObserver, final stage
//	6. per-turn buffer materialization         traffic.Observer, proxy-to-backend leg
//	7. Backend.Open                            execbackend.Backend.Open
//
// No internal function name, private field, reflection probe, or call-graph shape is
// inspected; stage ordinals come from the harness's own observer callbacks. Every
// failure message is requirement-numbered and path-content-free: occurrence counts,
// byte totals, and stage ordinals only. The workspace identity tag is derived in-test
// for comparison and is never formatted into a message.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/outbound"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// The two real contributions satisfy the extension points the runtime chains them on.
// The assertions below then drive them through exactly these interfaces.
var (
	_ request.AttemptTransform = (*outbound.AttemptTransform)(nil)
	_ sdkhooks.RequestPartHook = (*outbound.RequestPartHook)(nil)
)

const (
	// hookRegEarlySuffix and hookRegLateSuffix are the two distinct path-bearing
	// suffixes on the fixture's historical tool calls. They differ so a real path
	// restored onto one surface can never be mistaken for the other, and each is
	// identical in real and virtualized form, so any difference at a later stage is
	// attributable to the root prefix alone.
	hookRegEarlySuffix = "src/main.go"
	hookRegLateSuffix  = "src/late_shaped.go"

	// hookRegEarlyLimit and hookRegLateLimit are the two non-path sibling values.
	// They differ so requirement 2.8's byte-for-byte preservation of a non-path
	// argument is a real check on each surface rather than a repeated one.
	hookRegEarlyLimit = 10
	hookRegLateLimit  = 20

	// hookRegPathField is the argument member the shipped built-in profile claims for
	// the fixture's exact tool name. Spelling it explicitly keeps the fixture
	// independent of the optional schema-inference step, so this regression measures
	// the runtime ordering and nothing else.
	hookRegPathField = "file_path"

	// hookRegPayloadField is the payload-concept sibling no shipped profile claims.
	// Its value deliberately spells the real root, so requirement 2.4's refusal is
	// observable at the backend bound: it must still be real-root text there.
	hookRegPayloadField = "content"

	// hookRegShapingOrder places the fixture's late request-shaping participant below
	// every shipped request-part order and far below the feature hook's fixed order,
	// so the chain really runs shaper-then-hook.
	hookRegShapingOrder = 1
)

// hookRegArgumentDocument is one path-bearing tool-call argument document in the form
// the fixture writes it: the selected path member, the payload-concept sibling, and
// one non-path sibling.
func hookRegArgumentDocument(root, suffix string, limit int) string {
	return `{"` + hookRegPathField + `":"` + root + "/" + suffix + `",` +
		`"` + hookRegPayloadField + `":"literal ` + root + ` text","limit":` +
		strconv.Itoa(limit) + `}`
}

// hookRegToolCallMessage is one historical path-bearing tool call the client replays.
func hookRegToolCallMessage(callID, root, suffix string, limit int) lipapi.Message {
	return lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
		Kind:       lipapi.PartJSON,
		ToolCallID: callID,
		ToolName:   twoPassToolName,
		Content:    json.RawMessage(hookRegArgumentDocument(root, suffix, limit)),
	}}}
}

// hookRegCall builds the ingress call. It is Task 1.3's fixture with one difference
// that matters here: the historical tool call names the argument member the shipped
// built-in profile claims, so the REAL feature rewriter selects it by exact-name
// profile rather than by inference. The never_backend note and the terminal
// forwardable message are kept so the final conversation-view reassertion is a real
// stage rather than a no-op.
func hookRegCall() *lipapi.Call {
	return &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "two-pass:m"},
		Tools: []lipapi.ToolDef{{
			Name: twoPassToolName,
			Parameters: []byte(`{"type":"object","properties":{"` + hookRegPathField +
				`":{"type":"string"},"limit":{"type":"integer"}}}`),
		}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto},
		Messages: []lipapi.Message{
			twoPassAnchorMessage(),
			hookRegToolCallMessage("hook-reg-early", twoPassRealRoot, hookRegEarlySuffix, hookRegEarlyLimit),
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(twoPassLocalText)}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(twoPassTailText)}},
		},
	}
}

// hookRegShaping is the fixture's late request-shaping participant: the only thing in
// this run that puts a real path onto the history AFTER the early pass has run.
//
// It is deliberately NOT a rewriter. Its whole job is to be the hazard the feature's
// late pass exists to catch: a participant that sorts before it in the same
// request-part chain and restores real-root path-bearing tool history. It mutates
// nothing else and returns no error, so the chain always continues.
type hookRegShaping struct{}

func (*hookRegShaping) ID() string                        { return "path-virtualization-late-shaping-fixture" }
func (*hookRegShaping) Order() int                        { return hookRegShapingOrder }
func (*hookRegShaping) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (*hookRegShaping) HandleRequestParts(_ context.Context, call *lipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	call.Messages = append(call.Messages,
		hookRegToolCallMessage("hook-reg-late", twoPassRealRoot, hookRegLateSuffix, hookRegLateLimit))
	return nil
}

// hookRegAttemptMarker records the candidate-attempt stage ordinal and then delegates
// every decision to the real outbound.AttemptTransform.
//
// The real pass owns the behavior under test; this marker owns nothing but the
// monotonic ordinal the shared harness counter needs. Identity, order, and failure mode
// are the real pass's, so the bus and the diagnostics see the real participant.
type hookRegAttemptMarker struct {
	rec  *twoPassRecorder
	real *outbound.AttemptTransform
}

func (m *hookRegAttemptMarker) ID() string                        { return m.real.ID() }
func (m *hookRegAttemptMarker) Order() int                        { return m.real.Order() }
func (m *hookRegAttemptMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *hookRegAttemptMarker) HandleAttempt(
	ctx context.Context,
	call *lipapi.Call,
	meta request.AttemptMeta,
	svc request.Services,
) (request.AttemptDecision, error) {
	m.rec.mark(twoPassCheckpointAttemptTransform)
	return m.real.HandleAttempt(ctx, call, meta, svc)
}

// hookRegPartMarker records the request-part stage ordinal and then delegates every
// decision to the real outbound.RequestPartHook.
//
// It is the same shape as hookRegAttemptMarker and for the same reason. Because it
// wraps the feature hook rather than a stand-in, the ordinal it records is the stage
// the REAL late pass ran at, which is what lets the ordering assertion below be a
// statement about the feature and not about the fixture.
type hookRegPartMarker struct {
	rec  *twoPassRecorder
	real *outbound.RequestPartHook
}

func (m *hookRegPartMarker) ID() string                        { return m.real.ID() }
func (m *hookRegPartMarker) Order() int                        { return m.real.Order() }
func (m *hookRegPartMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *hookRegPartMarker) HandleRequestParts(
	ctx context.Context,
	call *lipapi.Call,
	meta sdkhooks.PartMeta,
) error {
	m.rec.mark(twoPassCheckpointRequestPartHook)
	return m.real.HandleRequestParts(ctx, call, meta)
}

// hookRegTraffic records the proxy-to-backend leg, i.e. the per-turn buffer the runtime
// materializes immediately before Backend.Open.
//
// It records the raw body rather than a scan, because this regression must measure the
// REAL derived alias, which is a function of the authoritative project root and is
// therefore not a fixed literal the harness can spell out.
type hookRegTraffic struct {
	rec  *twoPassRecorder
	mu   sync.Mutex
	body []byte
}

func (o *hookRegTraffic) OnObservation(_ context.Context, ev traffic.Observation) error {
	if ev.Leg != traffic.LegPTB {
		return nil
	}
	o.rec.mark(twoPassCheckpointPTB)
	o.mu.Lock()
	o.body = append([]byte(nil), ev.Body...)
	o.mu.Unlock()
	return nil
}

func (o *hookRegTraffic) captured() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.body
}

// hookRegReports collects the bounded reports the two real passes emit.
//
// It is mutex-guarded because a reporter is documented as callable concurrently and one
// instance of each pass is shared by every attempt of the turn.
type hookRegReports struct {
	mu      sync.Mutex
	attempt []outbound.Report
	part    []outbound.Report
}

func (r *hookRegReports) onAttempt(report outbound.Report) {
	r.mu.Lock()
	r.attempt = append(r.attempt, report)
	r.mu.Unlock()
}

func (r *hookRegReports) onPart(report outbound.Report) {
	r.mu.Lock()
	r.part = append(r.part, report)
	r.mu.Unlock()
}

func (r *hookRegReports) oneAttempt(t *testing.T) outbound.Report {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempt) != 1 {
		t.Fatalf("early outbound pass reports = %d, want exactly 1: one candidate attempt runs one pass", len(r.attempt))
	}
	return r.attempt[0]
}

func (r *hookRegReports) onePart(t *testing.T) outbound.Report {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.part) != 1 {
		t.Fatalf("late outbound pass reports = %d, want exactly 1: one request-part chain runs one pass", len(r.part))
	}
	return r.part[0]
}

// hookRegScan is the path-content-free measurement of every selected path-bearing
// tool-call argument document on one backend-bound surface.
//
// It counts the two states the requirement 5.2 assertion is about - a selected path
// value still carrying the real root, and one carrying the derived alias - plus the two
// things requirements.md 2.4 and 2.8 require to survive every stage. No path value,
// suffix, identifier, digest, or workspace tag ever reaches a failure message through
// this struct.
type hookRegScan struct {
	documents   int  // argument documents observed
	realRoot    int  // selected path values still carrying the real root
	virtual     int  // selected path values carrying the derived alias
	limits      int  // sum of the non-path sibling values across documents
	payloadKept int  // documents whose payload-concept sibling still spells the real root
	validArgs   bool // every observed argument document is one complete JSON value
	shapeKept   bool // every document kept the selected, payload, and non-path members
	callBytes   int
}

// hookRegMeasure scans one canonical call's selected path-bearing argument documents.
//
// The scan walks both canonical authorities, so the measurement is a statement about the
// backend-bound request rather than about whichever representation candidate adaptation
// happened to choose. alias is the alias this feature derives from the authoritative
// project root; it is compared, never formatted.
func hookRegMeasure(call lipapi.Call, alias string) hookRegScan {
	out := hookRegScan{validArgs: true, shapeKept: true}
	raw, err := json.Marshal(call)
	if err != nil {
		return hookRegScan{validArgs: false}
	}
	out.callBytes = len(raw)

	documents := make([][]byte, 0, 4)
	for _, msg := range call.Messages {
		for _, part := range msg.Parts {
			if part.Kind != lipapi.PartJSON || part.ToolCallID == "" || len(part.Content) == 0 {
				continue
			}
			documents = append(documents, part.Content)
		}
	}
	for _, item := range call.Items {
		if item.ToolCall == nil || len(item.ToolCall.Arguments) == 0 {
			continue
		}
		documents = append(documents, item.ToolCall.Arguments)
	}
	out.documents = len(documents)

	for _, doc := range documents {
		if !json.Valid(doc) {
			out.validArgs = false
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(doc, &fields); err != nil || len(fields) != 3 {
			out.validArgs = false
			out.shapeKept = false
			continue
		}
		var selected string
		switch {
		case json.Unmarshal(fields[hookRegPathField], &selected) != nil:
			out.shapeKept = false
			continue
		case strings.Contains(selected, alias):
			out.virtual++
		case strings.Contains(selected, twoPassRealRoot):
			out.realRoot++
		default:
			out.shapeKept = false
		}
		var limit int
		if err := json.Unmarshal(fields["limit"], &limit); err != nil {
			out.shapeKept = false
			continue
		}
		out.limits += limit
		var payload string
		if err := json.Unmarshal(fields[hookRegPayloadField], &payload); err != nil {
			out.shapeKept = false
			continue
		}
		if strings.Contains(payload, twoPassRealRoot) {
			out.payloadKept++
		} else {
			out.shapeKept = false
		}
	}
	if out.documents == 0 {
		out.validArgs = false
		out.shapeKept = false
	}
	return out
}

// hookRegMeasureBody measures an already-serialized backend-bound body.
func hookRegMeasureBody(body []byte, alias string) hookRegScan {
	var call lipapi.Call
	if err := json.Unmarshal(body, &call); err != nil {
		return hookRegScan{validArgs: false, callBytes: len(body)}
	}
	return hookRegMeasure(call, alias)
}

// hookRegResolver builds the shipped conservative policy a deployment gets with no
// operator configuration: the built-in profile layer, which claims the fixture's
// argument member on the fixture's exact tool name.
func hookRegResolver(t *testing.T) *pathvirtualization.Resolver {
	t.Helper()
	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles(builtin) reject = %q, want none", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(nil, builtin, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver(builtin) reject = %q, want none", reject)
	}
	return resolver
}

// hookRegAliasOf derives the alias this feature publishes for the fixture root.
//
// The value is returned for comparison only. Nothing in this file formats it and no
// failure message contains it: requirement 7.7 forbids the workspace identity tag from
// reaching any observable dimension.
func hookRegAliasOf(t *testing.T) string {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(twoPassRealRoot)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatalf("fixture: the fixture project root must derive an active mapping; refusal reason is bounded and content-free")
	}
	return mapping.VirtualRoot
}

// hookRegEligibility observes the candidate sizing / context eligibility port and
// records the selected path-bearing arguments the port is shown.
//
// It cannot reuse Task 1.3's observer, and the reason is the point of this file: that
// observer counts a hardcoded literal alias, while this regression must measure the
// alias the feature actually derives from the authoritative project root. Everything
// else about the seam is identical - the same supported port, invoked on the selected
// candidate the same way, with the same always-eligible answer so the run reaches
// Backend.Open.
type hookRegEligibility struct {
	rec   *twoPassRecorder
	alias string

	mu    sync.Mutex
	scans []hookRegScan
}

func (o *hookRegEligibility) Check(
	_ context.Context,
	_ routing.AttemptCandidate,
	call lipapi.Call,
	facts modelcatalog.EffectiveFacts,
) modelcatalog.EligibilityDecision {
	// The shared counter records the stage ordinal this invocation ran at; the
	// per-regression scan records what this invocation was shown. Both appends happen
	// inside one Check call, so the Nth entry of each belongs to the same invocation.
	o.rec.noteEligibility(call)
	o.mu.Lock()
	o.scans = append(o.scans, hookRegMeasure(call, o.alias))
	o.mu.Unlock()
	return modelcatalog.EligibilityDecision{
		IsEligible: true,
		Reason:     modelcatalog.EligibilityEligible,
		Facts:      facts,
	}
}

func (o *hookRegEligibility) observed() []hookRegScan {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]hookRegScan(nil), o.scans...)
}

// hookRegObservation is the complete content-free record of one run of this regression.
type hookRegObservation struct {
	order    twoPassStages
	turnDone bool
	reports  *hookRegReports

	// eligStages and eligScans are parallel: the Nth entry of each belongs to the Nth
	// eligibility invocation. The stages come from Task 1.3's shared counter, the
	// scans from this file's own measurement.
	eligStages           []int
	eligScans            []hookRegScan
	ptbScan              hookRegScan
	openScan             hookRegScan
	steeringRestored     bool
	steeringAfterAnchor  bool
	localNoteFiltered    bool
	anchorMessagePresent bool
}

// hookRegOrderOf renders this run's ordinals through Task 1.3's own renderer, so a
// failure here names every stage exactly the way the ordering characterization does.
func hookRegOrderOf(got hookRegObservation) twoPassOrder {
	calls := make([]twoPassEligibilityCall, 0, len(got.eligStages))
	for _, stage := range got.eligStages {
		calls = append(calls, twoPassEligibilityCall{stage: stage})
	}
	return twoPassOrderOf(twoPassObservation{order: got.order, eligCalls: calls})
}

// hookRegRun executes the scenario: Task 1.3's fixture and frozen conversation view,
// the real early outbound pass, one late request-shaping participant that reintroduces a
// real-root tool call, and the real late outbound pass.
//
// withLatePass selects only whether the late pass contributes. Both runs wire an
// identical request-part chain and reach the same stages, so the two runs differ in one
// thing: whether the feature's late pass publishes anything.
//
// Execute errors are reduced to a boolean because a denial reason can carry anchor
// identities and overlay IDs, which must never reach a failure message.
func hookRegRun(t *testing.T, withLatePass bool) hookRegObservation {
	t.Helper()

	rec := &twoPassRecorder{}
	reports := &hookRegReports{}
	ptb := &hookRegTraffic{rec: rec}
	reader := &twoPassReader{snap: twoPassSnapshot(t)}
	alias := hookRegAliasOf(t)
	elig := &hookRegEligibility{rec: rec, alias: alias}

	// The early pass reads the workspace projection the runtime already pinned onto its
	// attempt metadata. The late pass has no such projection in sdkhooks.PartMeta and
	// resolves the same authoritative view itself. Both are handed the SAME resolver
	// instance here, which is what makes the run a proof that the two passes agree on
	// one workspace rather than on two independently spelled roots.
	authority := twoPassWorkspaceResolver{root: twoPassRealRoot}

	// The control keeps the marker wired so the request-part stage is reached and
	// recorded identically, and hands it a TYPED NIL of the real pass type. The real
	// pass documents the nil receiver as safe and inert, so the control is exactly
	// "the late pass contributed nothing" with no other variable changed: same chain,
	// same stage ordinal, same reassertion, same adaptation, same backend.
	late := (*outbound.RequestPartHook)(nil)
	if withLatePass {
		late = outbound.NewRequestPartHook(
			rewrite.ModeRewrite,
			hookRegResolver(t),
			authority,
			outbound.WithHookReporter(reports.onPart),
		)
	}
	partHooks := []sdkhooks.RequestPartHook{&hookRegShaping{}, &hookRegPartMarker{rec: rec, real: late}}

	bus := hooks.New(hooks.Config{RequestPartHooks: partHooks})
	ex := runtime.TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2bua store: %v", err)
	}
	ex.Store = store
	ex.Bus = bus
	early := outbound.NewAttemptTransform(
		rewrite.ModeRewrite,
		hookRegResolver(t),
		outbound.WithReporter(reports.onAttempt),
	)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:       authority,
		TrafficObserver: ptb,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms: []request.AttemptTransform{&hookRegAttemptMarker{rec: rec, real: early}},
		}),
	})
	ex.ConversationViewReader = reader
	ex.ConversationViewObserver = &twoPassViewObserver{rec: rec}
	ex.EligibilityResolver = elig
	ex.Backends = map[string]execbackend.Backend{"two-pass": twoPassBackend(rec)}
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }

	// The request runs on the ordinary ingress path rather than the detached
	// auxiliary path: the detached path deliberately projects an EMPTY workspace
	// view, so the early pass would have no authoritative project root to derive a
	// mapping from. On this path the runtime resolves the workspace through the same
	// snapshot resolver the late pass is handed, and pins the result onto the attempt
	// metadata the early pass reads - which is what makes one shared workspace
	// resolver the right thing to wire for both.
	stream, execErr := ex.Execute(context.Background(), hookRegCall())
	if execErr == nil {
		_, execErr = lipapi.Collect(context.Background(), stream)
		_ = stream.Close()
	}

	raw := rec.observation(execErr == nil, reader.readCount())
	stages := make([]int, 0, len(raw.eligCalls))
	for _, call := range raw.eligCalls {
		stages = append(stages, call.stage)
	}
	return hookRegObservation{
		order:      raw.order,
		turnDone:   raw.turnResolved,
		reports:    reports,
		eligStages: stages,
		eligScans:  elig.observed(),
		ptbScan:    hookRegMeasureBody(ptb.captured(), alias),
		openScan:   hookRegMeasure(raw.backendOpenCall, alias),

		steeringRestored:     raw.steeringRestored,
		steeringAfterAnchor:  raw.steeringAfterAnchor,
		localNoteFiltered:    raw.localNoteFiltered,
		anchorMessagePresent: raw.anchorMessagePresent,
	}
}

// TestOutboundAttempt_RequestPartHookPreservesVirtualizedHistoryThroughBackendOpen is
// the runtime integration regression for requirements.md 5.2, 5.4, and 8.5, and it
// drives the feature's REAL late pass rather than a stand-in.
//
// The run installs the real candidate attempt transform and the real request-part hook,
// plus one late request-shaping participant that reintroduces a real-root tool call
// between them. It then asserts, from the runtime's own semantic stages:
//
//   - the candidate attempt stage precedes candidate sizing/context eligibility and the
//     request-part stage, and both eligibility invocations observe the virtualized
//     representation;
//   - the request-part stage, recorded at the stage the REAL hook ran at, precedes the
//     final conversation-view reassertion, which precedes the per-turn buffer, which
//     precedes Backend.Open;
//   - the real hook rewrote exactly the one surface the early pass had left real and
//     published no bytes for the surface the early pass already virtualized;
//   - the reassertion and candidate adaptation preserve BOTH virtualized surfaces
//     through the per-turn buffer and Backend.Open, with no real-root occurrence on
//     either selected path value, every argument document still one complete JSON
//     value, and every non-path and payload-concept sibling intact;
//   - the frozen conversation view still restores its overlay once at its frozen
//     placement and still filters the never_backend note.
//
// The rewrite-loss oracle. The identical fixture, with the late pass reached but inert,
// reaches Backend.Open carrying the reintroduced surface as a real path, which is
// exactly what a missing, misordered, or non-contributing late pass would reproduce.
func TestOutboundAttempt_RequestPartHookPreservesVirtualizedHistoryThroughBackendOpen(t *testing.T) {
	t.Parallel()

	// Control: identical fixture, identical real early pass, and an identical
	// request-part chain in which the late pass is reached but contributes nothing.
	control := hookRegRun(t, false)
	got := hookRegRun(t, true)

	// Scaffolding guards. Every assertion below reads these, so they run first.
	order := hookRegOrderOf(got)
	if !control.turnDone {
		t.Fatalf("fixture: the run without the late pass must still complete one turn: %s", hookRegOrderOf(control))
	}
	if !got.turnDone {
		t.Fatalf("fixture: the run with the late pass must complete one turn with no pre-backend denial: %s", order)
	}
	for _, checkpoint := range []struct {
		name  string
		stage int
	}{
		{"candidate attempt stage", got.order.attempt},
		{"request-part stage", got.order.part},
		{"conversation-view reassertion", got.order.reassert},
		{"per-turn buffer", got.order.ptb},
		{"Backend.Open", got.order.open},
	} {
		if checkpoint.stage == 0 {
			t.Fatalf("fixture: the %s checkpoint must be reached: %s", checkpoint.name, order)
		}
	}
	if len(got.eligStages) == 0 || len(got.eligScans) != len(got.eligStages) {
		t.Fatalf("fixture: candidate sizing / context eligibility must be invoked and observed: %s eligibility_invocations=%d observed_scans=%d",
			order, len(got.eligStages), len(got.eligScans))
	}
	if got.openScan.documents != 2 {
		t.Fatalf("fixture: the backend-bound request must carry both path-bearing tool calls, one virtualized by the early pass and one restored by the fixture's late shaping: %s argument_documents=%d",
			order, got.openScan.documents)
	}

	// design.md "Existing Architecture and Placement" steps 2, 3, and 4.
	t.Run("real_attempt_transform_precedes_candidate_sizing_and_request_part_stage", func(t *testing.T) {
		first := got.eligStages[0]
		if got.order.attempt >= first {
			t.Fatalf("requirements.md 5.3 and design.md \"Existing Architecture and Placement\" step 2 before step 3 - the real early pass must precede preliminary candidate eligibility: %s",
				order)
		}
		if first >= got.order.part {
			t.Fatalf("requirements.md 5.2 and design.md \"Existing Architecture and Placement\" step 3 before step 4 - preliminary candidate eligibility must run before the request-part stage that carries the real late pass: %s",
				order)
		}
	})

	// design.md "Existing Architecture and Placement" step 4 before step 6, and step 6
	// before steps 9 and 10. These are the orderings the late pass depends on and the
	// ones the reassertion must not be able to invert.
	t.Run("real_late_pass_precedes_reassertion_ptb_and_backend_open", func(t *testing.T) {
		if got.order.part >= got.order.reassert {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" step 4 before step 6 - the real late pass must run before the final conversation-view reassertion: %s",
				order)
		}
		if got.order.reassert >= got.order.ptb {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" step 6 before step 9 - the final conversation-view reassertion must complete before the per-turn buffer is materialized: %s",
				order)
		}
		if got.order.reassert >= got.order.open {
			t.Fatalf("requirements.md 5.2/5.4 and design.md \"Existing Architecture and Placement\" step 6 before step 10 - the final conversation-view reassertion must complete before Backend.Open: %s",
				order)
		}
		if got.order.ptb >= got.order.open {
			t.Fatalf("requirements.md 5.2 and design.md \"Existing Architecture and Placement\" step 9 before step 10 - the per-turn buffer must be materialized before Backend.Open: %s",
				order)
		}
	})

	// requirements.md 5.3: candidate sizing and the post-hook rederivation observe the
	// virtualized representation, so admission is never charged for savings it cannot
	// see.
	t.Run("candidate_sizing_and_post_hook_rederivation_see_virtualized_representation", func(t *testing.T) {
		// The FIRST invocation is the preliminary admission check that runs right after
		// the whole candidate attempt stage, so it can only have seen the early pass's
		// work: exactly the one surface that surface started with, virtualized.
		// The SECOND is the post-hook rederivation, and it is the one that proves the
		// late pass ran inside the chain - it must see BOTH surfaces virtualized,
		// including the one the fixture's late shaping introduced.
		want := []int{1, 2}
		if len(got.eligScans) != len(want) {
			t.Fatalf("fixture: eligibility must be invoked once per attempt stage boundary, invocations=%d", len(got.eligScans))
		}
		for i, scan := range got.eligScans {
			if scan.virtual < want[i] || scan.realRoot != 0 {
				t.Fatalf("requirements.md 5.3/5.4 - eligibility invocation %d must be shown the virtualized representation, never the real-root one: %s invocation=%d stage=%d virtual=%d real_root=%d minimum_virtual=%d",
					i, order, i, got.eligStages[i], scan.virtual, scan.realRoot, want[i])
			}
		}
		if first := got.eligScans[0]; first.virtual != 1 {
			t.Fatalf("requirements.md 5.3 - the preliminary eligibility check runs before the request-part stage, so it must observe exactly the early pass's savings and nothing more: %s virtual=%d real_root=%d",
				order, first.virtual, first.realRoot)
		}
		if second := got.eligScans[1]; second.virtual != 2 {
			t.Fatalf("requirements.md 5.4 - the post-hook rederivation runs after the real late pass, so it must observe BOTH surfaces virtualized: %s virtual=%d real_root=%d",
				order, second.virtual, second.realRoot)
		}
	})

	// design.md "Canonical Outbound Rewriter" with requirements.md 2.9: the late pass
	// reapplies the SAME pure rewriter to the SAME mapping, so the surface the early
	// pass already virtualized contributes nothing and only the newly reintroduced
	// real-root occurrence is rewritten.
	t.Run("real_late_pass_rewrites_only_the_reintroduced_real_path", func(t *testing.T) {
		early := got.reports.oneAttempt(t)
		if early.Outcome != outbound.OutcomeRewriterRan {
			t.Fatalf("requirements.md 5.3 - the early outbound pass must run the shared rewriter, outcome = %v", early.Outcome)
		}
		if early.Stats.Rewritten != 1 || early.Stats.Eligible != 1 {
			t.Fatalf("fixture: the real early pass must virtualize exactly the one path-bearing tool call, eligible=%d rewritten=%d",
				early.Stats.Eligible, early.Stats.Rewritten)
		}

		late := got.reports.onePart(t)
		if late.Outcome != outbound.OutcomeRewriterRan {
			t.Fatalf("requirements.md 5.2/8.2 - the late outbound pass must run the shared rewriter and report its bounded outcome, outcome = %v", late.Outcome)
		}
		if late.Stats.Rewritten != 1 || late.Stats.Eligible != 1 {
			t.Fatalf("requirements.md 5.4 - the late outbound pass must catch exactly the real path the later shaping introduced, eligible=%d rewritten=%d",
				late.Stats.Eligible, late.Stats.Rewritten)
		}
		if late.Stats.BytesSaved() <= 0 {
			t.Fatalf("requirements.md 7.6 - the late outbound pass must report the saving it realized, saved=%d", late.Stats.BytesSaved())
		}
	})

	// requirements.md 5.2 and 5.4 with design.md "Testing Strategy"
	// ("PTB/backend ingress receives virtualized eligible tool history"): the
	// reassertion and candidate adaptation are the last writers of the backend-bound
	// request before the per-turn buffer and Backend.Open, and neither may restore a
	// real path-bearing history.
	t.Run("reassertion_and_candidate_adaptation_preserve_virtualized_history", func(t *testing.T) {
		for _, stage := range []struct {
			name string
			scan hookRegScan
		}{
			{"per_turn_buffer", got.ptbScan},
			{"Backend.Open", got.openScan},
		} {
			if stage.scan.documents != 2 {
				t.Fatalf("requirements.md 5.2 - %s must carry both path-bearing tool call argument documents, documents=%d valid_json=%t body_bytes=%d",
					stage.name, stage.scan.documents, stage.scan.validArgs, stage.scan.callBytes)
			}
			if stage.scan.virtual != 2 || stage.scan.realRoot != 0 {
				t.Fatalf("requirements.md 5.2 and 5.4 - %s must carry the virtualized path-bearing history on both surfaces; late request shaping, the final conversation-view reassertion, and candidate adaptation must not restore a real root: %s virtual=%d real_root=%d",
					stage.name, order, stage.scan.virtual, stage.scan.realRoot)
			}
			if !stage.scan.validArgs {
				t.Fatalf("requirements.md 8.5 - %s must receive one complete JSON value per tool-call argument document: %s valid_json=%t",
					stage.name, order, stage.scan.validArgs)
			}
			if !stage.scan.shapeKept {
				t.Fatalf("requirements.md 2.4, 2.8, and 8.5 - %s must keep the selected, payload-concept, and non-path argument members intact: %s shape_kept=%t",
					stage.name, order, stage.scan.shapeKept)
			}
		}
		wantLimits := hookRegEarlyLimit + hookRegLateLimit
		if got.ptbScan.limits != wantLimits || got.openScan.limits != wantLimits {
			t.Fatalf("requirements.md 2.8 - the non-path sibling of each argument must survive both real passes byte-for-byte: %s per_turn_buffer_non_path_sum=%d backend_open_non_path_sum=%d expected_sum=%d",
				order, got.ptbScan.limits, got.openScan.limits, wantLimits)
		}
		if got.ptbScan.payloadKept != 2 || got.openScan.payloadKept != 2 {
			t.Fatalf("requirements.md 2.4 - no shipped profile claims the payload-concept member, so it must still be real-root text at the backend bound: %s per_turn_buffer_payload_kept=%d backend_open_payload_kept=%d",
				order, got.ptbScan.payloadKept, got.openScan.payloadKept)
		}
	})

	// design.md "Out of Boundary" ("Conversation-view message visibility semantics")
	// and the boundary context's adjacent expectations: neither real pass may change
	// what the frozen conversation view does.
	t.Run("conversation_view_semantics_survive_both_real_outbound_passes", func(t *testing.T) {
		if !got.anchorMessagePresent {
			t.Fatalf("boundary context and design.md \"Out of Boundary\" - conversation-view projection must retain its current ownership: %s anchor_message_present=%t",
				order, got.anchorMessagePresent)
		}
		if !got.steeringRestored {
			t.Fatalf("boundary context and design.md \"Out of Boundary\" - the active steering overlay must be restored exactly once on the backend-bound call: %s steering_restored=%t",
				order, got.steeringRestored)
		}
		if !got.steeringAfterAnchor {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" - the final reassertion must rebuild steering at the frozen placement, not at the head of the request: %s steering_after_anchor=%t",
				order, got.steeringAfterAnchor)
		}
		if !got.localNoteFiltered {
			t.Fatalf("requirements.md 5.4 and design.md \"Existing Architecture and Placement\" - a never_backend-tagged message must stay filtered out of the backend-bound request: %s local_note_filtered=%t",
				order, got.localNoteFiltered)
		}
	})

	// The rewrite-loss oracle. The identical fixture with the late pass absent reaches
	// Backend.Open carrying the reintroduced surface as a real path, which is precisely
	// what a missing, misordered, or non-contributing late pass would reproduce. This
	// is what makes the assertion above a real guard rather than a tautology.
	t.Run("a_missing_late_pass_is_detectable_at_the_backend_bound", func(t *testing.T) {
		controlOrder := hookRegOrderOf(control)
		if control.openScan.documents != 2 {
			t.Fatalf("fixture: the run without the late pass must still carry both argument documents: %s argument_documents=%d",
				controlOrder, control.openScan.documents)
		}
		if control.openScan.realRoot < 1 {
			t.Fatalf("fixture: the run without the late pass must reach Backend.Open carrying the reintroduced surface as a real path, which is the shape a lost late rewrite reproduces: %s virtual=%d real_root=%d",
				controlOrder, control.openScan.virtual, control.openScan.realRoot)
		}
		if got.openScan.realRoot != 0 {
			t.Fatalf("requirements.md 5.4 - no real-root occurrence may reach Backend.Open once the late pass is wired: %s real_root=%d",
				order, got.openScan.realRoot)
		}
		if delta := control.openScan.callBytes - got.openScan.callBytes; delta <= 0 {
			t.Fatalf("fixture: the backend-bound request must be strictly smaller once the late pass publishes its alias: %s control_body_bytes=%d backend_open_body_bytes=%d",
				order, control.openScan.callBytes, got.openScan.callBytes)
		}
	})
}
