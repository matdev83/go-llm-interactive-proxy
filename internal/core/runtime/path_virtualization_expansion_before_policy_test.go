package runtime_test

// Spec: b-leg-path-virtualization Task 8.2, the expansion-before-policy half.
// Requirements 4.1, 4.2, 4.3, 4.8, 4.9, 5.1, and 8.3.
//
// Design sections read for this file:
//
//   - "Existing Architecture and Placement" steps 11 and 12 (response tool-call
//     assembly/finalization, then tool policies/reactors) plus the two
//     model-to-client consequences ("model->client expansion uses completed
//     tool-call finalization, not raw ToolCallArgsDelta mutation"; "expansion
//     completes before existing tool policy/reactors");
//   - "7. Path Expansion Finalizer" steps 10 and 11 ("assembler synthesizes the
//     canonical rewritten lifecycle"; "existing tool policies/reactors receive
//     real paths");
//   - "Error Handling" (stale/different workspace tag and mandatory assembly
//     overflow both fail closed);
//   - "Testing Strategy / Runtime-integration" ("Completed model call expands
//     before tool policy observer"; "PTB/backend ingress receives virtualized
//     eligible tool history").
//
// WHAT THIS FILE IS, AND WHY IT IS NOT A SECOND HARNESS
//
// Task 1.2's permanent guard (tool_call_path_expansion_ordering_test.go)
// deliberately uses a minimal test-local expansion pass so that its claims stay
// about the RUNTIME's ordering and metadata delivery. That is the right shape for
// that claim and the wrong shape for a composition claim: the expansion decision
// requirements.md 4.1/4.4/4.5/8.3 demand is made by the concrete feature, so it
// has to be asserted against the concrete feature.
//
// So this file drives the REAL contributions end to end on one executor:
//
//  1. the REAL shipped outbound attempt transform (Task 5.1) on the candidate
//     attempt plane;
//  2. the REAL shipped outbound request-part hook (Task 5.2) on the request-part
//     bus;
//  3. the REAL assembler the runtime builds from the frozen finalizer plane;
//  4. the REAL shipped path-expansion finalizer (Task 8.1) on that plane, with
//     the REAL shipped conservative built-in selector profile, in rewrite mode;
//  5. the REAL tool policy plane and the REAL tool reactor plane.
//
// The only things this file adds are (a) transparent markers that record a stage
// ordinal for the two outbound passes and for the expansion pass, and (b) two
// observers. Neither changes a decision, an identity, an order, or a refusal
// policy. That is the discipline path_virtualization_request_part_hook_regression_test.go
// and tool_call_repair_expansion_composition_test.go already established: a
// marker promotes the production ID/Order/capability and delegates everything
// else, so the chain that sorts, the declaration the assembler reads, and the
// verdict the client sees are all production values.
//
// WHY THE ALIAS THE BACKEND EMITS IS THE ONE THE PROXY MINTED
//
// The historical tool call on the ingress history carries the REAL project root,
// exactly as a coding-agent client replays it. Both real outbound passes rewrite
// it, so by the time the fixture's backend sees the request the history is already
// virtualized. The backend then emits a NEW tool call whose path-bearing argument
// is that very alias plus a distinct suffix: the model echoing back the namespace
// it was shown. The file asserts the backend really received the alias (and never
// the real root) BEFORE asserting anything about what policy, reactor, or client
// see, so a claim that "expansion ran before policy" can never be satisfied by a
// fixture whose alias was never derived here.
//
// CONTENT FREEDOM
//
// No assertion message in this file contains a path, an alias, a workspace tag, a
// tool-call ID, or argument bytes. Paths and the derived alias are compared, never
// formatted. Messages carry requirement numbers, stage ordinals, byte counts,
// occurrence counts, and closed bounded reason labels only.
//
// DETERMINISM
//
// Every ordinal comes from one mutex-guarded monotonic counter, every scan walks
// slices in order, the alias is derived from a fixed project root by a
// content-defined digest, and the executor RNG and clock are pinned by the shared
// harness. Repeated runs produce identical ordinals and counts.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/expansion"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/outbound"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

const (
	// expToolName is the fixture's exact tool name. It is one of the canonical
	// coding-agent names the shipped built-in profile layer claims, so the run
	// exercises the production selector policy rather than an operator stub.
	expToolName = twoPassToolName

	// expPathField is the argument member the shipped built-in profile claims for
	// expToolName. Spelling it explicitly keeps the fixture independent of the
	// optional schema-inference step, so the run measures the composition and
	// nothing else.
	expPathField = hookRegPathField

	// expPayloadField is a sibling argument no shipped profile claims. It holds the
	// model-visible payload, deliberately spelled with the reserved alias, so
	// requirements.md 4.9 is observable in the POSITIVE direction too: expansion must
	// leave a payload-concept field's own bytes alone even when they spell the
	// reserved namespace.
	expPayloadField = hookRegPayloadField

	// expSiblingField and expSiblingValue are the non-path argument sibling
	// requirements.md 4.8 requires to survive expansion byte-for-byte.
	expSiblingField = "limit"
	expSiblingValue = 10

	// expHistorySuffix and expModelSuffix are two DISTINCT path-bearing suffixes: one
	// on the historical client-replayed tool call, one on the backend's own
	// model-emitted tool call. They differ so a rewrite of one surface can never be
	// mistaken for a rewrite of the other.
	expHistorySuffix = hookRegEarlySuffix
	expModelSuffix   = hookRegLateSuffix

	// expToolCallID is the model-emitted call's identifier and expHistoryID the
	// historical one's. They are fixed fixture tokens, never formatted into a failure
	// message.
	expToolCallID = "expansion-order-model-call"

	expSelector   = "two-pass:m"
	expBackendID  = "two-pass"
	expAnchorText = "expansion-order-first-user"
	expTailText   = "expansion-order-continue"
	expHistoryID  = "expansion-order-history"

	// expControlLabel and expGuardedLabel are the two scenarios' bounded,
	// content-free failure-message tokens.
	expControlLabel  = "without_the_expansion_pass"
	expGuardedLabel  = "with_the_expansion_pass"
	expInertOrderOff = 5
)

// expFixture is the ordered pair of strings one argument document is built from:
// the root prefix a selected path value carries, and the path-bearing suffix. The
// root is a parameter rather than a constant so one builder serves the real-root
// form (ingress history, expanded client release) and the alias form (what the
// backend-bound request carries and what the model emits back).
type expFixture struct {
	root   string
	suffix string
}

// joined is the selected path value: the root prefix and the path-bearing suffix
// separated by exactly ONE segment separator. The alias root already ends with one
// (it is the frozen V1 spelling) and the real root does not, so joining them
// unconditionally would either drop the separator or double it, and both would
// produce a value that is legitimately not under its root.
func (f expFixture) joined() string {
	if strings.HasSuffix(f.root, "/") {
		return f.root + f.suffix
	}
	return f.root + "/" + f.suffix
}

// modelArgsDocument is the complete argument document the backend's model emits on
// its tool call: the ALIAS form of the selected path member, plus a non-path
// sibling and a payload-concept sibling expansion must not touch.
func (f expFixture) modelArgsDocument() string {
	return `{"` + expPathField + `":"` + f.joined() + `",` +
		`"` + expPayloadField + `":"/` + f.joined() + `",` +
		`"` + expSiblingField + `":` + strconv.Itoa(expSiblingValue) + `}`
}

// expExpandedModelArgsDocument is the document requirements.md 4.1 requires on
// every client-facing surface for the model's tool call: the selected path member
// spelled against the authoritative real root, and every other member byte identical
// to what the model emitted - including the payload-concept member, which keeps the
// alias bytes it arrived with because requirement 4.9 forbids expanding it.
//
// TwoPassRealRoot, not the authoritative view the runtime publishes, is spelled
// here on purpose: a finalizer that expanded against some OTHER root would produce
// a different document and fail the byte comparison, which is what makes this the
// assertion that the expansion used the authoritative root.
func (f expFixture) expExpandedModelArgsDocument() string {
	return `{"` + expPathField + `":"` + twoPassRealRoot + "/" + f.suffix + `",` +
		`"` + expPayloadField + `":"/` + f.joined() + `",` +
		`"` + expSiblingField + `":` + strconv.Itoa(expSiblingValue) + `}`
}

// expMeasure is the path-content-free measurement of one canonical surface. It
// counts occurrences only; no path value, alias, tag, or argument byte can reach a
// failure message through it.
type expMeasure struct {
	// aliasHits is how many selected path values carry the reserved alias.
	aliasHits int
	// realHits is how many selected path values carry the authoritative real root.
	realHits int
	// aliasAnywhere is how many times the reserved alias bytes occur anywhere in the
	// measured payload, in ANY field. It is deliberately wider than aliasHits: a
	// payload-concept field spelling the alias must NOT be counted as a leak, so this
	// counter is what makes that difference measurable rather than assumed.
	aliasAnywhere int
	// selectedFields counts selected path values actually read, so a scan that
	// matched nothing because nothing was parsed cannot read as a clean run.
	selectedFields int
	// siblingSum is the total of the non-path sibling across every document.
	siblingSum int
	// realRootInPayload counts documents whose payload-concept member still carries
	// the authoritative real root, which requirement 2.4's refusal makes the expected
	// outcome for a member no shipped profile claims.
	realRootInPayload int
	// documents counts argument documents observed.
	documents int
	// validDocs counts argument documents that are one complete JSON value.
	validDocs int
	// shapeKept reports that every readable document kept exactly the three fixture
	// members and read as the fixture's shape.
	shapeKept bool
}

// expMeasureDocuments measures every path-bearing tool-call argument document on
// one backend-bound surface, walking both canonical authorities so the result is a
// statement about the request rather than about whichever representation
// adaptation happened to choose.
func expMeasureDocuments(call lipapi.Call, alias string) expMeasure {
	out := expMeasure{shapeKept: true}
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
		out.aliasAnywhere += strings.Count(string(doc), alias)
		if !json.Valid(doc) {
			continue
		}
		out.validDocs++
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(doc, &fields); err != nil || len(fields) != 3 {
			out.shapeKept = false
			continue
		}
		var selected string
		if json.Unmarshal(fields[expPathField], &selected) != nil {
			out.shapeKept = false
			continue
		}
		out.selectedFields++
		switch {
		case strings.Contains(selected, alias):
			out.aliasHits++
		case strings.Contains(selected, twoPassRealRoot):
			out.realHits++
		default:
			out.shapeKept = false
		}
		var sibling int
		if json.Unmarshal(fields[expSiblingField], &sibling) != nil {
			out.shapeKept = false
			continue
		}
		out.siblingSum += sibling
		var payload string
		if json.Unmarshal(fields[expPayloadField], &payload) != nil {
			out.shapeKept = false
			continue
		}
		if strings.Contains(payload, twoPassRealRoot) {
			out.realRootInPayload++
		}
	}
	return out
}

// expStage is the single monotonic sequence every participant of one run records
// at, so the test can compare when each ran without inspecting any private
// call-graph trivia. Ordinals from different runs are never compared, and the FIRST
// ordinal is kept for a plane the runtime may invoke more than once, because
// requirements.md 5.1 is about what happened FIRST.
type expStage struct {
	mu        sync.Mutex
	seq       int
	attempt   int
	part      int
	expansion int
	policy    int
	reactor   int
}

func (s *expStage) first(field *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if *field == 0 {
		*field = s.seq
	}
}

func (s *expStage) markAttempt()   { s.first(&s.attempt) }
func (s *expStage) markPart()      { s.first(&s.part) }
func (s *expStage) markExpansion() { s.first(&s.expansion) }
func (s *expStage) markPolicy()    { s.first(&s.policy) }
func (s *expStage) markReactor()   { s.first(&s.reactor) }

func (s *expStage) seen() (attempt, part, expansion, policy, reactor int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempt, s.part, s.expansion, s.policy, s.reactor
}

// expOrd renders an ordinal, naming the unreached case rather than printing a zero
// that could be misread as "ran first".
func expOrd(v int) string {
	if v == 0 {
		return "unreached"
	}
	return strconv.Itoa(v)
}

// expExpansionMarker records the completed-tool-call finalization stage and then
// delegates every decision to the REAL shipped expansion finalizer.
//
// ID, Order, and ToolCallBufferingRequirement are promoted from the embedded
// production finalizer, so toolcall.MaterializeSorted sorts on the real production
// order value and the assembler reads the real production mandatory declaration -
// which is what makes requirements.md 4.5's overflow bound and 8.3's fail-closed
// refusal production behavior rather than fixture behavior. Only Finalize is
// intercepted, and only to record an ordinal, the observed document, and the
// decision.
type expExpansionMarker struct {
	*expansion.Finalizer

	stage *expStage

	mu     sync.Mutex
	calls  int
	seen   []byte
	result toolcall.Result
}

var (
	_ toolcall.Finalizer            = (*expExpansionMarker)(nil)
	_ toolcall.BufferingRequirement = (*expExpansionMarker)(nil)
)

func (p *expExpansionMarker) Finalize(
	ctx context.Context,
	call toolcall.CompletedCall,
	tool lipapi.ToolDef,
	catalog []lipapi.ToolDef,
	meta toolcall.Meta,
) (toolcall.Result, error) {
	res, err := p.Finalizer.Finalize(ctx, call, tool, catalog, meta)
	p.stage.markExpansion()
	p.mu.Lock()
	p.calls++
	p.seen = append(p.seen[:0], call.ArgsJSON...)
	p.result = res
	p.mu.Unlock()
	return res, err
}

func (p *expExpansionMarker) observation() (int, []byte, toolcall.Result) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, append([]byte(nil), p.seen...), p.result
}

// expPolicyObserver is the REAL tool policy plane's observer, standing in for the
// path/security policy evaluation requirements.md 5.1 orders after expansion. It
// records only the observation order, the joined argument bytes it was shown, and
// whether the authoritative project root reached it.
type expPolicyObserver struct {
	stage *expStage

	mu         sync.Mutex
	calls      int
	joined     string
	argEvents  int
	policyRoot bool
}

func (o *expPolicyObserver) ID() string                        { return "expansion-path-policy-observer" }
func (o *expPolicyObserver) Order() int                        { return 0 }
func (o *expPolicyObserver) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (o *expPolicyObserver) Handle(
	_ context.Context,
	ev lipapi.ToolEvent,
	meta toolpolicy.Meta,
	_ toolpolicy.Services,
) (toolpolicy.Decision, error) {
	o.mu.Lock()
	if ev.ArgsDelta != "" {
		o.stage.markPolicy()
		o.calls++
		o.argEvents++
		// Arguments are joined in observation order, so a reserved alias could only
		// be detected across fragment boundaries exactly as the backend streamed it.
		o.joined += ev.ArgsDelta
	} else {
		o.calls++
	}
	if strings.TrimSpace(meta.Workspace.ProjectRoot) != "" {
		o.policyRoot = true
	}
	o.mu.Unlock()
	return toolpolicy.DecisionAllow, nil
}

func (o *expPolicyObserver) observation() (int, int, string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls, o.argEvents, o.joined, o.policyRoot
}

// expReactorObserver is the REAL tool reactor plane's observer, the OTHER
// design.md step-12 consumer requirements.md 4.3 names ("existing tool
// policies/reactors"). It records only the joined argument bytes and the ordinal.
type expReactorObserver struct {
	stage *expStage

	mu        sync.Mutex
	calls     int
	joined    string
	argEvents int
}

func (o *expReactorObserver) ID() string                        { return "expansion-tool-reactor-observer" }
func (o *expReactorObserver) Order() int                        { return 0 }
func (o *expReactorObserver) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (o *expReactorObserver) HandleToolEvent(
	_ context.Context,
	ev lipapi.ToolEvent,
	_ sdkhooks.ToolMeta,
) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	o.mu.Lock()
	if ev.ArgsDelta != "" {
		o.stage.markReactor()
		o.calls++
		o.argEvents++
		o.joined += ev.ArgsDelta
	} else {
		o.calls++
	}
	o.mu.Unlock()
	return sdkhooks.ToolPass, ev, nil
}

func (o *expReactorObserver) observation() (int, int, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls, o.argEvents, o.joined
}

// expAttemptMarker and expPartMarker record the two real outbound stages. Each
// delegates every decision to the real shipped contribution, promoting its
// identity, order, and failure mode so the bus and the chain that sorts see the
// production participant.
type expAttemptMarker struct {
	stage *expStage
	real  *outbound.AttemptTransform
}

func (m *expAttemptMarker) ID() string                        { return m.real.ID() }
func (m *expAttemptMarker) Order() int                        { return m.real.Order() }
func (m *expAttemptMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *expAttemptMarker) HandleAttempt(
	ctx context.Context,
	call *lipapi.Call,
	meta request.AttemptMeta,
	svc request.Services,
) (request.AttemptDecision, error) {
	m.stage.markAttempt()
	return m.real.HandleAttempt(ctx, call, meta, svc)
}

type expPartMarker struct {
	stage *expStage
	real  *outbound.RequestPartHook
}

func (m *expPartMarker) ID() string                        { return m.real.ID() }
func (m *expPartMarker) Order() int                        { return m.real.Order() }
func (m *expPartMarker) FailureMode() sdkhooks.FailureMode { return m.real.FailureMode() }

func (m *expPartMarker) HandleRequestParts(
	ctx context.Context,
	call *lipapi.Call,
	meta sdkhooks.PartMeta,
) error {
	m.stage.markPart()
	return m.real.HandleRequestParts(ctx, call, meta)
}

// expInertFinalizer is the control run's single ordinary finalizer. It publishes no
// mandatory requirement, rewrites nothing, and refuses nothing, so it changes only
// whether the assembler exists - never a client-facing byte. Its order sits below
// the shipped expansion order so the control's chain is otherwise the same shape.
type expInertFinalizer struct{}

func (*expInertFinalizer) ID() string { return "expansion-control-inert-finalizer" }

func (*expInertFinalizer) Order() int { return expansion.FinalizerOrder - expInertOrderOff }

func (*expInertFinalizer) Finalize(
	context.Context,
	toolcall.CompletedCall,
	lipapi.ToolDef,
	[]lipapi.ToolDef,
	toolcall.Meta,
) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// expHookReports collects the bounded reports the real shipped contributions emit.
// Every field it stores is a closed code or a count, so a failure message built from
// it cannot carry payload bytes either.
type expHookReports struct {
	mu       sync.Mutex
	attempt  []outbound.Report
	part     []outbound.Report
	expansin []expansion.Report
}

func (r *expHookReports) onAttempt(report outbound.Report) {
	r.mu.Lock()
	r.attempt = append(r.attempt, report)
	r.mu.Unlock()
}

func (r *expHookReports) onPart(report outbound.Report) {
	r.mu.Lock()
	r.part = append(r.part, report)
	r.mu.Unlock()
}

func (r *expHookReports) onExpansion(report expansion.Report) {
	r.mu.Lock()
	r.expansin = append(r.expansin, report)
	r.mu.Unlock()
}

func (r *expHookReports) oneAttempt(t *testing.T) outbound.Report {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempt) != 1 {
		t.Fatalf("fixture: the real early outbound pass must report exactly one bounded outcome: reports=%d", len(r.attempt))
	}
	return r.attempt[0]
}

func (r *expHookReports) onePart(t *testing.T) outbound.Report {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.part) != 1 {
		t.Fatalf("fixture: the real late outbound pass must report exactly one bounded outcome: reports=%d", len(r.part))
	}
	return r.part[0]
}

func (r *expHookReports) expansion() []expansion.Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]expansion.Report(nil), r.expansin...)
}

// expBackend is the final provider-translation boundary. It records the call it was
// handed through the shared Task 1.3 recorder and returns the fixture's fixed
// canonical stream, which is the only source of the model-emitted tool call.
func expBackend(rec *twoPassRecorder, events []lipapi.Event) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
		Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			rec.mark(twoPassCheckpointBackendOpen)
			rec.noteOpen(call)
			return lipapi.NewFixedEventStream(events), nil
		},
	}
}

// expDrain collects every client-facing event the runtime released and reports the
// terminal stream error separately, so a fail-closed scenario can be asserted
// instead of being reported as a collection failure.
func expDrain(stream lipapi.EventStream) ([]lipapi.Event, error) {
	var out []lipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}

// expReleasedArgs joins the client-facing tool-call argument bytes, in release
// order, and counts how many tool-lifecycle events reached the client at all.
func expReleasedArgs(events []lipapi.Event) (string, int) {
	var joined strings.Builder
	lifecycle := 0
	for _, ev := range tcrToolLifecycle(events) {
		lifecycle++
		if ev.Kind == lipapi.EventToolCallArgsDelta {
			joined.WriteString(ev.Delta)
		}
	}
	return joined.String(), lifecycle
}

// expArgsFragments splits one model-emitted document into stream-shaped deltas whose
// cuts land inside the reserved alias and inside the path-bearing suffix, so no
// fragment is one complete JSON value and the SELECTED alias exists only across a
// fragment boundary. requirements.md 4.2 is then observable: the alias is only ever
// detectable in the COMPLETE document the assembler hands a finalizer.
//
// The fixture's payload-concept sibling deliberately spells the alias too, so the
// "no fragment contains a whole alias" property cannot be asserted here and is not
// claimed: what is asserted is that the alias's own byte span is cut by a boundary,
// which is the property 4.2 is about. The payload sibling's alias is what makes the
// positive requirement-4.9 assertion in the test body non-trivial.
func expArgsFragments(t *testing.T, doc, alias string) []string {
	t.Helper()
	aliasAt := strings.Index(doc, alias)
	if aliasAt < 0 {
		t.Fatal("fixture: the model-emitted document must carry the reserved alias")
	}
	const (
		intoTag    = len("/.__lip_v1__/w_") + 4
		intoSuffix = len("src/la")
	)
	if intoTag <= 0 || intoTag >= len(alias) {
		t.Fatalf("fixture: the cut offset must fall strictly inside the alias span: offset=%d alias_span=%d",
			intoTag, len(alias))
	}
	fragments := []string{
		doc[:aliasAt+intoTag],
		doc[aliasAt+intoTag : aliasAt+len(alias)+intoSuffix],
		doc[aliasAt+len(alias)+intoSuffix:],
	}
	if joined := strings.Join(fragments, ""); joined != doc {
		t.Fatalf("fixture: fragments must reconstruct the document: got %d bytes want %d", len(joined), len(doc))
	}
	for i, fragment := range fragments {
		if json.Valid([]byte(fragment)) {
			t.Fatalf("fixture: fragment %d must not be independently valid JSON", i)
		}
	}
	return fragments
}

// expBackendEvents is the fixture's canonical stream: one model-emitted tool call,
// delivered as deltas cut through the reserved alias.
func expBackendEvents(t *testing.T, fixture expFixture) []lipapi.Event {
	t.Helper()
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: expToolCallID, ToolName: expToolName},
	}
	for _, fragment := range expArgsFragments(t, fixture.modelArgsDocument(), fixture.root) {
		events = append(events, lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: expToolCallID,
			ToolName:   expToolName,
			Delta:      fragment,
		})
	}
	return append(events,
		lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: expToolCallID, ToolName: expToolName},
		lipapi.Event{Kind: lipapi.EventResponseFinished},
	)
}

// expOversizedDocument builds one VALID argument document of exactly total bytes
// whose selected path member carries the reserved alias, so a scenario can drive
// the assembler's mandatory bound with a body that would be an ordinary tool call
// at any smaller size.
//
// It is the fixture's ordinary three-member document plus one extra filler member,
// so the selected path member, the non-path sibling, and the payload-concept member
// are spelled exactly as everywhere else in this file.
func expOversizedDocument(t *testing.T, fixture expFixture, total int) string {
	t.Helper()
	prefix := `{"` + expPathField + `":"` + fixture.joined() + `",` +
		`"` + expPayloadField + `":"",` +
		`"` + expSiblingField + `":` + strconv.Itoa(expSiblingValue) + `,` +
		`"filler":"`
	tail := `"}`
	filler := total - len(prefix) - len(tail)
	if filler < 1 {
		t.Fatalf("fixture: the requested total is smaller than the fixed members: total=%d fixed=%d", total, len(prefix)+len(tail))
	}
	doc := prefix + strings.Repeat("x", filler) + tail
	if len(doc) != total {
		t.Fatalf("fixture: document sizing: got %d bytes want %d", len(doc), total)
	}
	if !json.Valid([]byte(doc)) {
		t.Fatal("fixture: the oversized document must still be one complete JSON value")
	}
	if !strings.Contains(doc, fixture.joined()) {
		t.Fatal("fixture: the oversized document must carry the reserved alias")
	}
	return doc
}

// expOversizedBackendEvents delivers one model-emitted tool call whose assembled
// arguments exceed total bytes, in chunks small enough that no single delta is at
// the canonical limit. The cuts are otherwise arbitrary, which is the point: the
// overflow decision belongs to the assembler, not to fragment shape.
func expOversizedBackendEvents(t *testing.T, fixture expFixture, total int) []lipapi.Event {
	t.Helper()
	doc := expOversizedDocument(t, fixture, total)
	const chunk = 128 * 1024
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: expToolCallID, ToolName: expToolName},
	}
	for start := 0; start < len(doc); start += chunk {
		end := min(start+chunk, len(doc))
		events = append(events, lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: expToolCallID,
			ToolName:   expToolName,
			Delta:      doc[start:end],
		})
	}
	return append(events,
		lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: expToolCallID, ToolName: expToolName},
		lipapi.Event{Kind: lipapi.EventResponseFinished},
	)
}

// expIngressCall is the canonical client request: one path-bearing historical tool
// call spelled against the REAL project root, exactly as a coding-agent client
// replays it, plus a first user message and a terminal forwardable one.
func expIngressCall(t *testing.T) *lipapi.Call {
	t.Helper()
	history := expFixture{root: twoPassRealRoot, suffix: expHistorySuffix}
	document := history.modelArgsDocument()
	if !json.Valid([]byte(document)) {
		t.Fatalf("fixture: the ingress history argument document must be one complete JSON value: bytes=%d", len(document))
	}
	return &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: expSelector},
		Tools: []lipapi.ToolDef{{
			Name: expToolName,
			Parameters: []byte(`{"type":"object","properties":{"` + expPathField +
				`":{"type":"string"},"limit":{"type":"integer"}}}`),
		}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(expAnchorText)}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
				Kind:       lipapi.PartJSON,
				ToolCallID: expHistoryID,
				ToolName:   expToolName,
				Content:    json.RawMessage(document),
			}}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(expTailText)}},
		},
	}
}

// expScenario selects the ONE variable a run changes. The resolvable case passes the
// alias the authoritative root derives; an unresolved-alias case passes the alias
// some OTHER supported root derives; an oversized case replaces the model's body.
type expScenario struct {
	// label is a bounded, content-free failure-message token.
	label string
	// aliasRoot is the root prefix the model-emitted alias is built on.
	aliasRoot string
	// expansionDecides installs the REAL shipped expansion finalizer on the frozen
	// finalizer plane. A control run with it off is exactly "the expansion finalizer
	// is absent", with no other variable changed.
	expansionDecides bool
	// assembleBody, when non-zero, replaces the model's argument document with one of
	// exactly this many bytes.
	assembleBody int
	// extraFinalizers are ordinary finalizers added to the chain for this scenario.
	// They exist so a scenario can place a participant at a chosen order relative to
	// the shipped expansion order.
	extraFinalizers []toolcall.Finalizer

	// observersExpected selects whether the two step-12 observer guards below apply.
	// A scenario whose call is REFUSED CLOSED releases no tool lifecycle at all, so
	// neither observer is invoked for it and asserting that they were would assert the
	// opposite of the property. The request-side guards stay unconditional for every
	// scenario, because a refused tool call does not change how the request was built.
	observersExpected bool
}

// expRun drives one scenario end to end.
func expRun(t *testing.T, scenario expScenario) expRunResult {
	t.Helper()

	alias := hookRegAliasOf(t)
	fixture := expFixture{root: scenario.aliasRoot, suffix: expModelSuffix}

	stage := &expStage{}
	rec := &twoPassRecorder{}
	reports := &expHookReports{}
	policy := &expPolicyObserver{stage: stage}
	reactor := &expReactorObserver{stage: stage}
	marker := &expExpansionMarker{
		Finalizer: newExpansionFin(t, reports.onExpansion),
		stage:     stage,
	}

	var backendEvents []lipapi.Event
	if scenario.assembleBody > 0 {
		backendEvents = expOversizedBackendEvents(t, fixture, scenario.assembleBody)
	} else {
		backendEvents = expBackendEvents(t, fixture)
	}

	// Both real outbound passes are handed the SAME compiled policy the shipped
	// expansion finalizer uses, and both read the SAME workspace view the runtime
	// pins for the turn - the early pass from its attempt metadata, the late pass from
	// the public SDK context projection. Neither is handed a workspace authority, which
	// is what makes one run a proof that the two request planes and the response plane
	// agree on one workspace rather than on two independently spelled roots.
	authority := twoPassWorkspaceResolver{root: twoPassRealRoot}
	resolver := hookRegResolver(t)
	early := outbound.NewAttemptTransform(
		rewrite.ModeRewrite,
		resolver,
		outbound.WithReporter(reports.onAttempt),
	)
	late := outbound.NewRequestPartHook(
		rewrite.ModeRewrite,
		resolver,
		outbound.WithHookReporter(reports.onPart),
	)

	bus := corehooks.New(corehooks.Config{
		RequestPartHooks: []sdkhooks.RequestPartHook{&expPartMarker{stage: stage, real: late}},
		ToolReactors:     []sdkhooks.ToolReactor{reactor},
	})
	ex, _ := interleavedSecureExecutor(t, map[string]execbackend.Backend{
		expBackendID: expBackend(rec, backendEvents),
	})
	ex.Processor = nil
	ex.Bus = bus

	finalizers := make([]toolcall.Finalizer, 0, 1+len(scenario.extraFinalizers))
	if scenario.expansionDecides {
		finalizers = append(finalizers, marker)
	} else {
		// The control still contributes one ordinary finalizer so the assembler is
		// constructed exactly as it is in the guarded run. It decides nothing and
		// publishes nothing, so the control is exactly "expansion contributed
		// nothing" rather than "a whole plane was missing".
		finalizers = append(finalizers, &expInertFinalizer{})
	}
	finalizers = append(finalizers, scenario.extraFinalizers...)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: authority,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms:  []request.AttemptTransform{&expAttemptMarker{stage: stage, real: early}},
			ToolCallPolicies:   []toolpolicy.Policy{policy},
			ToolCallFinalizers: finalizers,
		}),
	})

	stream, err := ex.Execute(principalCtx("path-virtualization-expansion-order"), expIngressCall(t))
	if err != nil {
		// The error TYPE only. A pre-backend denial reason can carry anchor identities
		// and overlay IDs, which requirements.md 7.7 keeps out of any observable
		// dimension; this file's harness never asserts on the text.
		t.Fatalf("%s: fixture: the turn must reach the backend: error_type=%T", scenario.label, err)
	}
	events, recvErr := expDrain(stream)

	result := expRunResult{
		label:   scenario.label,
		events:  events,
		recvErr: recvErr,
		reports: reports,
	}
	result.released, result.lifecycle = expReleasedArgs(events)
	raw := rec.observation(true, 0)
	result.openMeasure = expMeasureDocuments(raw.backendOpenCall, alias)
	result.expCalls, result.expSeen, result.expResult = marker.observation()
	result.expReports = reports.expansion()
	result.policyCalls, result.policyArg, result.policyJoined, result.policyRoot = policy.observation()
	result.reactorCalls, result.reactorArg, result.reactorJoined = reactor.observation()
	attemptAt, partAt, expansionAt, policyAt, reactorAt := stage.seen()
	result.stages = expStages{attemptAt, partAt, expansionAt, policyAt, reactorAt, raw.order.open}
	_ = stream.Close()

	// Scaffolding guards. Every assertion below reads one of them, so they run first
	// and any later failure is attributable to the property under test rather than to
	// a fixture that never reached the observers.
	if attemptAt == 0 || partAt == 0 {
		t.Fatalf("%s: fixture: both real outbound stages must be reached: %s", scenario.label, result.stages)
	}
	if result.openMeasure.documents != 1 || result.openMeasure.selectedFields != 1 {
		t.Fatalf("%s: fixture: the backend-bound request must carry exactly the one path-bearing history tool call: documents=%d selected_fields=%d",
			scenario.label, result.openMeasure.documents, result.openMeasure.selectedFields)
	}
	if scenario.observersExpected {
		if result.policyCalls < 1 || result.reactorCalls < 1 {
			t.Fatalf("%s: fixture: both step-12 observer planes must run: policy_calls=%d reactor_calls=%d",
				scenario.label, result.policyCalls, result.reactorCalls)
		}
		if !result.policyRoot {
			t.Fatalf("%s: fixture: the tool policy plane must receive the authoritative workspace view, otherwise the run cannot isolate expansion",
				scenario.label)
		}
	}
	return result
}

// newExpansionFin builds the REAL shipped expansion finalizer with the REAL shipped
// conservative built-in selector profile, in rewrite mode, and asserts the three
// production properties the composition claims rest on.
func newExpansionFin(t *testing.T, onReport func(expansion.Report)) *expansion.Finalizer {
	t.Helper()
	fin, err := expansion.NewFinalizer(
		hookRegResolver(t),
		rewrite.ModeRewrite,
		expansion.Policy{},
		expansion.WithReporter(onReport),
	)
	if err != nil {
		t.Fatalf("fixture: build the shipped expansion finalizer: %v", err)
	}
	if fin.Order() != expansion.FinalizerOrder {
		t.Fatalf("fixture: shipped expansion order changed: got %d want %d", fin.Order(), expansion.FinalizerOrder)
	}
	if got := fin.ToolCallBufferingRequirement(); got.Overflow != toolcall.OverflowReject {
		t.Fatalf("fixture: the shipped pass must declare OverflowReject, got %q", got.Overflow)
	}
	return fin
}

// expStages holds one run's six ordinals.
type expStages struct {
	attempt, part, expansion, policy, reactor, open int
}

// String renders one run's ordinals as a failure-message fragment. Only monotonic
// ordinals are emitted; nothing path-bearing is ever mixed in, and an unreached
// stage is named rather than printed as a zero that could read as "ran first".
func (s expStages) String() string {
	return "attempt_stage=" + expOrd(s.attempt) +
		" request_part_stage=" + expOrd(s.part) +
		" expansion_stage=" + expOrd(s.expansion) +
		" policy_stage=" + expOrd(s.policy) +
		" reactor_stage=" + expOrd(s.reactor) +
		" backend_open_stage=" + expOrd(s.open)
}

// expRunResult is the complete content-free record of one run.
type expRunResult struct {
	label   string
	stages  expStages
	events  []lipapi.Event
	recvErr error
	reports *expHookReports

	// released is the joined client-facing tool-call argument bytes and lifecycle
	// counts how many tool-lifecycle events reached the client.
	released  string
	lifecycle int

	openMeasure expMeasure

	expCalls   int
	expSeen    []byte
	expResult  toolcall.Result
	expReports []expansion.Report

	policyCalls  int
	policyArg    int
	policyJoined string
	policyRoot   bool

	reactorCalls  int
	reactorArg    int
	reactorJoined string
}

// TestStreamToolCall_RealExpansionReachesPolicyReactorAndClientAsTheRealPath is
// requirements.md 4.1, 4.2, 4.3, 4.8, 4.9, and 5.1 proved against the CONCRETE
// feature rather than against a stand-in: the shipped expansion finalizer, the
// shipped outbound attempt transform, the shipped request-part hook, the real
// assembler, and both real step-12 observer planes.
//
// It asserts, in order:
//
//   - the backend-bound request already carries the derived alias and no real root,
//     so the alias the model emits back is one the real outbound passes minted;
//   - the tool policy plane and the tool reactor plane each observe the FULL real
//     path - the complete expanded document, byte for byte, never the reserved alias
//     and never a truncated prefix;
//   - expansion completed before either of them observed an argument value;
//   - the client-facing release is that same expanded complete document, with the
//     non-path sibling preserved and the payload-concept sibling's own bytes intact
//     even though they spell the reserved namespace;
//   - the shipped pass reported a bounded, content-free reason for what it did.
//
// The control run is the fault-injection oracle: the identical fixture with the
// expansion finalizer absent reaches the policy plane, the reactor plane, and the
// client carrying the reserved alias, which is precisely what a missing, inert, or
// misordered expansion pass reproduces.
func TestStreamToolCall_RealExpansionReachesPolicyReactorAndClientAsTheRealPath(t *testing.T) {
	t.Parallel()

	resolvable := expFixture{root: hookRegAliasOf(t), suffix: expModelSuffix}
	control := expRun(t, expScenario{label: expControlLabel, aliasRoot: resolvable.root, expansionDecides: false, observersExpected: true})
	got := expRun(t, expScenario{label: expGuardedLabel, aliasRoot: resolvable.root, expansionDecides: true, observersExpected: true})

	// design.md "Testing Strategy / Runtime-integration": "PTB/backend ingress
	// receives virtualized eligible tool history". Without this, every assertion
	// below could be satisfied by a fixture that never virtualized anything.
	t.Run("the_real_outbound_passes_minted_the_alias_the_model_emitted_back", func(t *testing.T) {
		if got.openMeasure.aliasHits != 1 || got.openMeasure.realHits != 0 {
			t.Fatalf("requirements.md 5.2/5.3 - both real outbound passes must virtualize the client-replayed history before Backend.Open: %s backend_open_alias_hits=%d backend_open_real_hits=%d",
				got.stages, got.openMeasure.aliasHits, got.openMeasure.realHits)
		}
		if got.openMeasure.documents != got.openMeasure.validDocs {
			t.Fatalf("requirements.md 8.5 - the backend-bound argument document must remain one complete JSON value: documents=%d valid=%d",
				got.openMeasure.documents, got.openMeasure.validDocs)
		}
		early := got.reports.oneAttempt(t)
		if early.Outcome != outbound.OutcomeRewriterRan {
			t.Fatalf("requirements.md 5.3 - the real early pass must run the shared rewriter: outcome=%v", early.Outcome)
		}
		if early.Stats.Rewritten != 1 || early.Stats.Eligible != 1 {
			t.Fatalf("requirements.md 5.3 - the real early pass must virtualize exactly the one replayed surface: eligible=%d rewritten=%d",
				early.Stats.Eligible, early.Stats.Rewritten)
		}
		late := got.reports.onePart(t)
		if late.Outcome != outbound.OutcomeRewriterRan || late.Stats.Rewritten != 0 {
			t.Fatalf("requirements.md 5.2/5.4 - the real late pass must rerun the same pure rewrite and find nothing left to do: outcome=%v eligible=%d rewritten=%d",
				late.Outcome, late.Stats.Eligible, late.Stats.Rewritten)
		}
		if !got.openMeasure.shapeKept {
			t.Fatalf("requirements.md 2.8 - the backend-bound request must keep the selected, payload-concept, and non-path members intact: %s shape_kept=%t",
				got.stages, got.openMeasure.shapeKept)
		}
		if got.openMeasure.siblingSum != expSiblingValue {
			t.Fatalf("requirements.md 2.8 - the non-path sibling must survive both real passes byte-for-byte: sibling_sum=%d expected=%d",
				got.openMeasure.siblingSum, expSiblingValue)
		}
		// requirements.md 2.4/4.9, mirrored on the request side: the replayed
		// document's payload-concept member spells the real root and no shipped profile
		// claims it, so it must still carry that root at the backend bound while the
		// SELECTED member has become the alias. aliasAnywhere is the count of reserved
		// namespace occurrences anywhere in the measured payload, so pinning it to
		// exactly one is what proves the rewrite touched the selected member and only
		// the selected member.
		if got.openMeasure.realRootInPayload != 1 {
			t.Fatalf("requirements.md 2.4/4.9 - the payload-concept member must keep its real-root bytes at the backend bound: payload_documents_with_real_root=%d",
				got.openMeasure.realRootInPayload)
		}
		if got.openMeasure.aliasAnywhere != 1 {
			t.Fatalf("requirements.md 2.4/4.9 - exactly one reserved namespace occurrence may survive on the backend-bound surface, the payload-concept member's: occurrences=%d selected_alias_hits=%d",
				got.openMeasure.aliasAnywhere, got.openMeasure.aliasHits)
		}
	})

	// design.md "7. Path Expansion Finalizer" step 11 and requirements.md 4.3/5.1.
	t.Run("tool_policy_observes_the_full_real_path", func(t *testing.T) {
		assertObserverSeesExpandedDocument(t, "tool_policy", got.stages,
			got.policyCalls, got.policyArg, got.policyJoined,
			resolvable.expExpandedModelArgsDocument(), resolvable.root)
	})

	t.Run("tool_reactor_observes_the_full_real_path", func(t *testing.T) {
		assertObserverSeesExpandedDocument(t, "tool_reactor", got.stages,
			got.reactorCalls, got.reactorArg, got.reactorJoined,
			resolvable.expExpandedModelArgsDocument(), resolvable.root)
	})

	// requirements.md 5.1: policy evaluation runs ONLY AFTER expansion produced the
	// real filesystem path. The ordinal comparison is against both step-12 planes,
	// because 4.3 names policies AND reactors.
	t.Run("expansion_completes_before_both_step_twelve_observers", func(t *testing.T) {
		if got.stages.expansion == 0 || got.stages.policy == 0 || got.stages.reactor == 0 {
			t.Fatalf("every ordinal under comparison must be reached: %s", got.stages)
		}
		if got.stages.expansion >= got.stages.policy {
			t.Fatalf("requirements.md 5.1 - expansion must complete before tool policy evaluation observes the argument value: %s",
				got.stages)
		}
		if got.stages.expansion >= got.stages.reactor {
			t.Fatalf("requirements.md 4.3/5.1 - expansion must complete before a tool reactor observes the argument value: %s",
				got.stages)
		}
	})

	// requirements.md 4.1 plus 4.8/4.9: the released document is the expanded one,
	// byte for byte, with the non-path sibling preserved and the payload-concept
	// sibling untouched.
	t.Run("the_client_release_is_the_expanded_complete_document", func(t *testing.T) {
		if got.lifecycle != 3 {
			t.Fatalf("fixture: the assembler must synthesize the canonical rewritten lifecycle: lifecycle_events=%d want 3",
				got.lifecycle)
		}
		want := resolvable.expExpandedModelArgsDocument()
		if got.released != want {
			// The single assertion that matters: byte equality against the exact
			// expected document. It reports valid_json too, so a truncated, partially
			// rewritten, or alias-bearing release is distinguished from a merely
			// differently spelled one without the release bytes ever being printed.
			t.Fatalf("requirements.md 4.1/4.8 - the client-facing argument document must be the expanded complete document: released=%d bytes expected=%d bytes reserved_alias_released=%t valid_json=%t",
				len(got.released), len(want),
				strings.Contains(got.released, resolvable.root),
				json.Valid([]byte(got.released)))
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got.released), &fields); err != nil || len(fields) != 3 {
			t.Fatalf("requirements.md 4.8 - the released document must keep every argument member: released=%d bytes", len(got.released))
		}
		var sibling int
		if json.Unmarshal(fields[expSiblingField], &sibling) != nil || sibling != expSiblingValue {
			t.Fatalf("requirements.md 4.8 - the non-path argument sibling must survive expansion byte-for-byte: sibling_readable=%t",
				json.Unmarshal(fields[expSiblingField], &sibling) == nil)
		}
		// requirement 4.9: the payload-concept member spelled the reserved namespace
		// and must therefore still spell it after expansion. This is asserted by
		// comparison, never by formatting the value.
		var payload string
		if json.Unmarshal(fields[expPayloadField], &payload) != nil {
			t.Fatalf("requirements.md 4.9 - the payload-concept member must still be readable after expansion")
		}
		if payload != "/"+resolvable.joined() {
			t.Fatalf("requirements.md 4.9 - expansion must not touch a payload-concept member, even one spelling the reserved namespace: payload_bytes=%d reserved_alias_in_payload=%t",
				len(payload), strings.Contains(payload, resolvable.root))
		}
	})

	// The shipped pass must have DECIDED, and reported it through its own bounded
	// content-free channel, rather than the client release happening for some other
	// reason.
	t.Run("the_shipped_pass_decided_and_reported_a_bounded_reason", func(t *testing.T) {
		if got.expCalls != 1 {
			t.Fatalf("fixture: the shipped pass must run exactly once on the completed document: invocations=%d", got.expCalls)
		}
		if !json.Valid(got.expSeen) {
			t.Fatalf("requirements.md 4.2 - the shipped pass must receive the complete valid document: seen=%d bytes", len(got.expSeen))
		}
		if !strings.Contains(string(got.expSeen), resolvable.root) {
			t.Fatalf("requirements.md 4.2 - the shipped pass must receive the reassembled model-emitted document: seen=%d bytes reserved_alias_in_seen=%t",
				len(got.expSeen), strings.Contains(string(got.expSeen), resolvable.root))
		}
		if got.expResult.Action != toolcall.ActionRewrite {
			t.Fatalf("fixture: the shipped pass must rewrite: action=%d", int(got.expResult.Action))
		}
		reason, ok := expansion.ParseReason(got.expResult.ReasonCode)
		if !ok || reason != expansion.ReasonExpanded {
			t.Fatalf("requirements.md 4.1 - the shipped pass must report the bounded expanded reason: reason=%q parseable=%t",
				got.expResult.ReasonCode, ok)
		}
		if len(got.expReports) != 1 {
			t.Fatalf("fixture: the shipped pass must emit exactly one bounded report: reports=%d", len(got.expReports))
		}
		if got.expReports[0].Outcome != expansion.OutcomeExpanded || got.expReports[0].Reason != expansion.ReasonExpanded {
			t.Fatalf("requirements.md 7.6 - the shipped report must be the content-free expanded outcome: outcome=%v reason=%q",
				got.expReports[0].Outcome, got.expReports[0].Reason)
		}
	})

	// The fault-injection oracle. The identical fixture with the expansion finalizer
	// absent reaches the policy plane, the reactor plane, and the client carrying the
	// reserved alias. This is what makes every assertion above a real guard rather
	// than a statement about a fixture that could not have gone any other way.
	t.Run("removing_the_expansion_pass_is_detectable_at_both_observers_and_the_client", func(t *testing.T) {
		if control.expCalls != 0 {
			t.Fatalf("fixture: the control must not run the shipped pass: invocations=%d", control.expCalls)
		}
		if control.stages.expansion != 0 {
			t.Fatalf("fixture: the control must never reach the expansion stage: %s", control.stages)
		}
		for _, observer := range []struct {
			name   string
			calls  int
			arg    int
			joined string
		}{
			{"tool_policy", control.policyCalls, control.policyArg, control.policyJoined},
			{"tool_reactor", control.reactorCalls, control.reactorArg, control.reactorJoined},
		} {
			if observer.calls < 1 || observer.arg < 1 {
				t.Fatalf("fixture: the control's %s observer must still run: calls=%d argument_events=%d",
					observer.name, observer.calls, observer.arg)
			}
			if observer.joined != resolvable.modelArgsDocument() {
				t.Fatalf("fault-injection oracle - without the shipped expansion pass the %s observer must be handed the model's own bytes verbatim: observed=%d bytes expected=%d bytes",
					observer.name, len(observer.joined), len(resolvable.modelArgsDocument()))
			}
			if !strings.Contains(observer.joined, resolvable.root) {
				t.Fatalf("fault-injection oracle - without the shipped expansion pass the %s observer must see the reserved alias: observed=%d bytes reserved_alias=%t",
					observer.name, len(observer.joined), strings.Contains(observer.joined, resolvable.root))
			}
		}
		if control.released != resolvable.modelArgsDocument() {
			t.Fatalf("fault-injection oracle - without the shipped expansion pass the client must receive the model's own bytes verbatim: released=%d bytes expected=%d bytes",
				len(control.released), len(resolvable.modelArgsDocument()))
		}
		if got.released == control.released {
			t.Fatal("fixture: the guarded and control runs must release different bytes, otherwise neither proves anything")
		}
	})
}

// assertObserverSeesExpandedDocument is the shared body of the two step-12
// assertions. It requires the FULL real path: exact byte equality against the
// expected expanded document, which a truncated form, a partial rewrite, or the
// reserved alias itself all fail.
func assertObserverSeesExpandedDocument(
	t *testing.T,
	observer string,
	stages expStages,
	calls, argEvents int,
	joined string,
	want string,
	alias string,
) {
	t.Helper()
	if calls < 1 || argEvents < 1 {
		t.Fatalf("fixture: the %s plane must observe at least one argument event: calls=%d argument_events=%d",
			observer, calls, argEvents)
	}
	if joined != want {
		t.Fatalf("requirements.md 4.3/4.8/5.1 - the %s plane must observe the FULL expanded real path, byte for byte: observed=%d bytes expected=%d bytes reserved_alias_observed=%t valid_json=%t non_path_sibling_preserved=%t %s",
			observer, len(joined), len(want),
			strings.Contains(joined, alias),
			json.Valid([]byte(joined)),
			strings.Contains(joined, `"`+expSiblingField+`":`+strconv.Itoa(expSiblingValue)),
			stages)
	}
	if !json.Valid([]byte(joined)) {
		t.Fatalf("requirements.md 4.8 - the %s plane must observe one complete JSON value: observed=%d bytes %s",
			observer, len(joined), stages)
	}
	if !strings.Contains(joined, twoPassRealRoot) {
		t.Fatalf("requirements.md 4.1/5.1 - the %s plane must observe the authoritative real root, not a prefix of it: observed=%d bytes %s",
			observer, len(joined), stages)
	}
}
