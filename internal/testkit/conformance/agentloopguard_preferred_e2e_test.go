// End-to-end preferred-protocol acceptance fixtures for slice 10.1A of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Testing /
// Runtime / Acceptance Matrix rows 1, 2, and 20; requirements 2.1-2.7,
// 3.1-3.6, 4.1-4.7, 5.1-5.7, 6.1-6.7, 12.5).
//
// Every fixture in this file drives the complete production path for one
// deployment: a real frontend handler mounted on a real httptest origin, the
// real runtime executor, a real backend adapter, and a real
// reference-provider origin emulator. No live or billable call is made.
//
// The Agent Loop Guard generation is compiled through the production feature
// registry, the enabled-surface merge, and the production request-snapshot
// builder, so the control provider and terminal provider under test are the
// ones the factory actually composes. Nothing here assigns a provider fake.
//
// Slice 10.1A owns two cells of the 10.1 matrix:
//
//  1. completion-only: the model emits only the private control call, and the
//     client receives the bounded result as ordinary assistant text exactly
//     once;
//  2. streamed text then completion: the already-committed assistant text is
//     delivered exactly once and the bounded result is not appended.
//
// Plus the explicit early-stream observation assertion the task validation
// line requires: the final-stream observation of every canonical event happens
// after the real response-part hook ran for it and before the frontend encoder
// emitted it, and the pending result is released through the accepted terminal
// owner ahead of the response completion the client observes.
//
// Slice 10.1B appends five more cells for the message-authority,
// OpenAI-Responses protocol column, in the section at the end of this file: the
// ordinary client tool then completion, the malformed control call, the multiple
// control calls, the native completion collision, and the missing signal.
//
// Remaining 10.1 cases (the one-reprompt user-input case, unsupported backend
// tools, the ToolChoice matrix, item-authority twins, and the Anthropic and
// Gemini protocol representatives) are NOT covered here.
//
// Known soundness defect in the slice 10.1A streamed cell, NOT fixed here
// because that cell is independently approved and immutable: the comparison
// "clientSawText < finishObserved" orders an index recorded by the client
// reader goroutine against an index recorded by the response-pipeline goroutine.
// Nothing in the production path creates a happens-before edge between them,
// because the pipeline never blocks on the client reader, so a run in which the
// reader goroutine is not scheduled until after the pipeline finished violates
// the assertion. It reproduces on committed HEAD 04ffc3a7 with this file
// reverted (see the slice 10.1B status report), so it is a pre-existing harness
// race rather than a defect of the cells below. The 10.1B cells are
// deliberately serial so they do not amplify that scheduling window.
package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationprojection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview/sdkadapter"
	refopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
)

// algControlToolName, algControlCallID, and algControlItemID are the private
// protocol facts these fixtures deliberately plant upstream. They exist only so
// the fixtures can prove they never reach the client.
const (
	algControlToolName = "attempt_completion"
	algControlCallID   = "call_alg_private_control"
	algControlItemID   = "fc_alg_private_control"
)

// algCompletionResult is the bounded result the model returns from the private
// control call.
const algCompletionResult = "Migration finished: schema applied and backfill verified."

// algStreamedText is ordinary assistant text the model streams BEFORE the
// private control call.
const algStreamedText = "Schema applied; backfill verification is running."

// The three real seams whose relative order this file certifies.
const (
	algTraceStageHook    = "response_part_hook"
	algTraceStageObserve = "final_stream_observer"
	algTraceStageClient  = "client_wire"
)

// --- observation trace --------------------------------------------------------

// algTraceItem is one observation of one canonical event or one client-visible
// wire frame. text is the assistant delta for a text frame, tool carries the
// effective tool name for a tool lifecycle frame, and seq is the observation
// ordinal in a single ordering shared by all three stages.
type algTraceItem struct {
	seq   int
	stage string
	kind  string
	text  string
	tool  string
}

// algTrace is the single ordered observation log shared by the real
// response-part hook, the real final-stream observer, and the client-side wire
// reader. One mutex orders all three because they genuinely run on different
// goroutines: the hook and the observer run inside the response pipeline, the
// wire reader runs in the client.
type algTrace struct {
	mu    sync.Mutex
	items []algTraceItem
}

func (tr *algTrace) add(stage, kind, text, tool string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.items = append(tr.items, algTraceItem{seq: len(tr.items), stage: stage, kind: kind, text: text, tool: tool})
}

func (tr *algTrace) snapshot() []algTraceItem {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]algTraceItem(nil), tr.items...)
}

// firstIndex returns the sequence number of the first matching observation, or -1.
func (tr *algTrace) firstIndex(stage, kind, text string) int {
	for _, it := range tr.snapshot() {
		if it.stage == stage && it.kind == kind && it.text == text {
			return it.seq
		}
	}
	return -1
}

// algFirstKind returns the sequence number of the first observation of one kind
// in one stage, or -1.
func algFirstKind(items []algTraceItem, stage, kind string) int {
	for _, it := range items {
		if it.stage == stage && it.kind == kind {
			return it.seq
		}
	}
	return -1
}

// algTraceHook is a real sdkhooks.ResponsePartHook. It records and mutates
// nothing, so every assertion below observes the production hook chain rather
// than a stubbed recorder.
type algTraceHook struct{ tr *algTrace }

func (algTraceHook) ID() string                        { return "alg-e2e-trace-hook" }
func (algTraceHook) Order() int                        { return 0 }
func (algTraceHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (h algTraceHook) HandleEvent(_ context.Context, ev *lipapi.Event, _ sdkhooks.PartMeta) error {
	if ev == nil {
		return nil
	}
	h.tr.add(algTraceStageHook, string(ev.Kind), ev.Delta, ev.ToolName)
	return nil
}

// algTraceObserverFactory is a real response.StreamObserverFactory contributed
// through the real feature plane, so its Observe and Finish calls are driven by
// extensions.RunFinalStreamObservationStage at the production observation point.
type algTraceObserverFactory struct{ tr *algTrace }

func (algTraceObserverFactory) ID() string                        { return "alg-e2e-trace-observer" }
func (algTraceObserverFactory) Order() int                        { return 0 }
func (algTraceObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (f algTraceObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return algTraceObserver(f), nil
}

type algTraceObserver struct{ tr *algTrace }

func (o algTraceObserver) Observe(_ context.Context, ev lipapi.Event) error {
	o.tr.add(algTraceStageObserve, string(ev.Kind), ev.Delta, ev.ToolName)
	return nil
}

func (algTraceObserver) Finish(context.Context, response.StreamOutcome) error { return nil }

// --- deployment seam ----------------------------------------------------------

// algDeployPreferred composes one deployment whose executor carries the real
// preferred-strategy Agent Loop Guard generation plus the observation factory,
// installed through the production registry, merge, and snapshot path, and
// returns the shared observation trace.
func algDeployPreferred(t *testing.T, transport ClientTransport, origin http.Handler) (*Deployment, *algTrace) {
	t.Helper()

	d := Deploy(t, DeploymentSpec{
		Frontend:               FrontendOpenAIResponses,
		Backend:                BackendOpenAIResponses,
		Transport:              transport,
		OriginHandler:          origin,
		AgentLoopGuardStrategy: AgentLoopGuardStrategyAttemptCompletion,
	})
	if d == nil {
		t.Fatal("Deploy returned nil for the preferred-strategy cell")
	}

	tr := &algTrace{}
	bus := hooks.New(hooks.Config{ResponsePartHooks: []sdkhooks.ResponsePartHook{algTraceHook{tr: tr}}})
	d.Exec.Bus = bus

	// Extend the generation Deploy installed with the observation factory through
	// the same real plane merge, so the executor snapshot still comes from the
	// production snapshot builder rather than a hand-assembled provider list.
	planes, err := AgentLoopGuardFeaturePlanes(t, AgentLoopGuardStrategyAttemptCompletion)
	if err != nil {
		t.Fatalf("AgentLoopGuardFeaturePlanes: %v", err)
	}
	cs := lipfeature.NewContributionSet()
	if err := planes.ReplayTo(cs, "alg-e2e-observer"); err != nil {
		t.Fatalf("ReplayTo: %v", err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "alg-e2e-observer", []response.StreamObserverFactory{algTraceObserverFactory{tr: tr}}); err != nil {
		t.Fatalf("Contribute stream observer factories: %v", err)
	}
	d.Exec.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{FeaturePlanes: cs.Freeze()})
	algWireContinuationPorts(t, d)
	return d, tr
}

// algWireContinuationPorts composes the two real conversation-view ports the
// production host wires in build_executor.go: the trajectory reader and the
// steering-writer factory, both over the real in-memory conversation-view store
// and the real production steering-writer adapter.
//
// The generic continuation transaction that the missing-signal policy drives needs
// them, so without them a bounded protocol-repair leg cannot be admitted and the
// deployment degrades to the unfactored host shape. This composes no terminal
// provider and no control provider: those still come only from the production
// feature registry, merge, and snapshot builder. It is applied by every cell that
// uses algDeployPreferred so all of them observe one identical environment.
func algWireContinuationPorts(t *testing.T, d *Deployment) {
	t.Helper()

	store := conversationview.NewReferenceStore()
	d.Exec.ConversationViewReader = algAutoRegisteringReader{store: store}
	d.Exec.SteeringWriterFactory = func(_ context.Context, aLegID string, resolver runtime.SteeringWriterResolver) (steering.Writer, error) {
		return sdkadapter.NewWriter(store, aLegID, sdkadapter.TrajectoryResolver(resolver))
	}
}

// algAutoRegisteringReader is the same on-demand A-leg registration and unknown-leg
// tolerance the production feature-host store wrapper applies, so a request whose
// A-leg never registered reads as an empty snapshot instead of failing the turn.
type algAutoRegisteringReader struct {
	store *conversationview.ReferenceStore
}

func (r algAutoRegisteringReader) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	if err := r.store.CreateALeg(ctx, aLegID); err != nil {
		return conversationprojection.Snapshot{}, err
	}
	return r.store.Snapshot(ctx, aLegID)
}

// --- upstream wire fixtures ---------------------------------------------------

// algControlArgs renders the private control-call arguments exactly as a model
// would emit them: one JSON object with exactly one string `result` member.
func algControlArgs(t *testing.T, result string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"result": result})
	if err != nil {
		t.Fatalf("marshal control args: %v", err)
	}
	return string(raw)
}

// algCompletionOnlyOrigin is the upstream reference-provider wire for matrix
// row 1: the model's only output is the private control call.
func algCompletionOnlyOrigin(t *testing.T) http.Handler {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":         "resp_alg_control_only",
		"object":     "response",
		"created_at": 1715620000,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output": []any{map[string]any{
			"type":      "function_call",
			"id":        algControlItemID,
			"call_id":   algControlCallID,
			"name":      algControlToolName,
			"arguments": algControlArgs(t, algCompletionResult),
		}},
		"usage": map[string]any{"input_tokens": 21, "output_tokens": 9, "total_tokens": 30},
	})
	if err != nil {
		t.Fatalf("marshal completion-only resource: %v", err)
	}
	return refopenairesponses.NewHandler(refopenairesponses.Config{NonStreamJSON: string(body)})
}

// algStreamedTextThenCompletionOrigin is the upstream reference-provider wire
// for matrix row 2: ordinary assistant text first, then the private control
// call, then the terminal event.
func algStreamedTextThenCompletionOrigin(t *testing.T) http.Handler {
	t.Helper()
	frame := func(name string, payload map[string]any) string {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal sse frame %s: %v", name, err)
		}
		return "event: " + name + "\ndata: " + string(raw) + "\n\n"
	}
	var sse strings.Builder
	seq := 0
	next := func() int { seq++; return seq }
	sse.WriteString(frame("response.created", map[string]any{
		"type": "response.created", "sequence_number": next(),
		"response": map[string]any{"id": "resp_alg_streamed", "object": "response", "created_at": 1715620000, "status": "in_progress", "model": "gpt-4o-mini"},
	}))
	sse.WriteString(frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "message", "id": "msg_alg_streamed", "status": "in_progress", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": ""}},
		},
	}))
	sse.WriteString(frame("response.content_part.added", map[string]any{
		"type": "response.content_part.added", "sequence_number": next(), "item_id": "msg_alg_streamed",
		"output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""},
	}))
	sse.WriteString(frame("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "sequence_number": next(), "item_id": "msg_alg_streamed",
		"output_index": 0, "content_index": 0, "delta": algStreamedText,
	}))
	sse.WriteString(frame("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "sequence_number": next(), "item_id": "msg_alg_streamed",
		"output_index": 0, "content_index": 0, "text": algStreamedText,
	}))
	sse.WriteString(frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "message", "id": "msg_alg_streamed", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": algStreamedText}},
		},
	}))
	sse.WriteString(frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "sequence_number": next(), "output_index": 1,
		"item": map[string]any{"type": "function_call", "id": algControlItemID, "call_id": algControlCallID, "name": algControlToolName, "arguments": ""},
	}))
	sse.WriteString(frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": next(), "output_index": 1,
		"item": map[string]any{"type": "function_call", "id": algControlItemID, "call_id": algControlCallID, "name": algControlToolName, "arguments": algControlArgs(t, algCompletionResult)},
	}))
	sse.WriteString(frame("response.completed", map[string]any{
		"type": "response.completed", "sequence_number": next(),
		"response": map[string]any{
			"id": "resp_alg_streamed", "object": "response", "created_at": 1715620000, "status": "completed", "model": "gpt-4o-mini",
			"usage": map[string]any{"input_tokens": 31, "output_tokens": 14, "total_tokens": 45},
		},
	}))
	sse.WriteString("data: [DONE]\n\n")
	return refopenairesponses.NewHandler(refopenairesponses.Config{StreamSSE: sse.String()})
}

// --- client drivers -----------------------------------------------------------

// algCreateBody is the exact A-leg request the fixtures post to the real
// OpenAI Responses frontend.
func algCreateBody(model string, stream bool) string {
	body, err := json.Marshal(map[string]any{
		"model":  model,
		"input":  "apply the schema, verify the backfill, then report the result",
		"stream": stream,
	})
	if err != nil {
		panic("marshal alg create body: " + err.Error())
	}
	return string(body)
}

// algWireFrame is one client-visible frame: either the non-streaming response
// resource or one SSE frame.
type algWireFrame struct {
	Type  string
	Delta string
	Text  string
	Raw   string
}

// algPostCreate drives one client request over the real HTTP boundary and, when
// tr is non-nil, records every delivered frame into the shared trace as it is
// read off the wire.
func algPostCreate(ctx context.Context, d *Deployment, stream bool, tr *algTrace) (int, []algWireFrame, error) {
	return algPostCreateBody(ctx, d, algCreateBody("gpt-4o-mini", stream), stream, tr)
}

// algPostCreateBody is the raw-body form of algPostCreate. It is the same driver
// over the same real HTTP boundary and the same trace; only the A-leg document is
// supplied by the caller, so a multi-turn cell can post its own client tool
// catalog, tool results, and echoed model items.
func algPostCreateBody(ctx context.Context, d *Deployment, body string, stream bool, tr *algTrace) (int, []algWireFrame, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL()+"/v1/responses", strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LIP-Route", d.RouteSelector)
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if !stream {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, nil, err
		}
		var decoded struct {
			Output []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return resp.StatusCode, nil, fmt.Errorf("decode create resource: %w", err)
		}
		var b strings.Builder
		for _, item := range decoded.Output {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					b.WriteString(part.Text)
				}
			}
		}
		if tr != nil {
			tr.add(algTraceStageClient, "json_resource", "", "")
		}
		return resp.StatusCode, []algWireFrame{{Type: "json_resource", Text: b.String(), Raw: string(raw)}}, nil
	}

	var frames []algWireFrame
	br := bufio.NewReader(resp.Body)
	var data strings.Builder
	flush := func() error {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		var decoded struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return fmt.Errorf("decode sse frame: %w", err)
		}
		frames = append(frames, algWireFrame{Type: decoded.Type, Delta: decoded.Delta, Text: decoded.Text, Raw: payload})
		if tr != nil {
			tr.add(algTraceStageClient, decoded.Type, decoded.Delta, "")
		}
		return nil
	}
	for {
		line, readErr := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "data:"):
			payload := strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(payload)
		case trimmed == "":
			if err := flush(); err != nil {
				return resp.StatusCode, frames, err
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return resp.StatusCode, frames, readErr
			}
			break
		}
	}
	if err := flush(); err != nil {
		return resp.StatusCode, frames, err
	}
	return resp.StatusCode, frames, nil
}

// --- assertions ---------------------------------------------------------------

// algWireText concatenates every assistant text fragment the client observed.
func algWireText(frames []algWireFrame) string {
	var b strings.Builder
	for _, f := range frames {
		switch f.Type {
		case "json_resource":
			b.WriteString(f.Text)
		case "response.output_text.delta":
			b.WriteString(f.Delta)
		}
	}
	return b.String()
}

// algCountWireType counts client-visible frames of one wire type.
func algCountWireType(frames []algWireFrame, typ string) int {
	n := 0
	for _, f := range frames {
		if f.Type == typ {
			n++
		}
	}
	return n
}

// algAssertNoPrivateControlLeak fails when any client-visible frame or the whole
// wire body carries the private control name, call id, item id, argument member,
// or any tool-call lifecycle frame at all.
func algAssertNoPrivateControlLeak(t *testing.T, frames []algWireFrame) {
	t.Helper()
	var body strings.Builder
	for _, f := range frames {
		body.WriteString(f.Raw)
		body.WriteByte('\n')
	}
	raw := body.String()
	for _, secret := range []string{algControlToolName, algControlCallID, algControlItemID, `"result"`, `"function_call"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("client wire leaked private control fact %q:\n%s", secret, raw)
		}
	}
	for _, f := range frames {
		if strings.Contains(f.Type, "function_call") || strings.Contains(f.Type, "tool") {
			t.Fatalf("client wire exposed a tool lifecycle frame %q:\n%s", f.Type, raw)
		}
	}
}

// algAssertObservationOrder proves the explicit ordering the task validation
// line requires: the real response-part hook ran first for a canonical event,
// the real final-stream observation ran second, the frontend encoder emitted
// the frame third, and the accepted terminal owner released the private pending
// result before the response completion the client observes.
func algAssertObservationOrder(t *testing.T, tr *algTrace, frames []algWireFrame, publishedText string) {
	t.Helper()

	items := tr.snapshot()
	hookAt := tr.firstIndex(algTraceStageHook, string(lipapi.EventTextDelta), publishedText)
	obsAt := tr.firstIndex(algTraceStageObserve, string(lipapi.EventTextDelta), publishedText)
	if hookAt < 0 {
		t.Fatalf("response-part hook never observed the published text %q; trace=%+v", publishedText, items)
	}
	if obsAt < 0 {
		t.Fatalf("final-stream observation never observed the published text %q; trace=%+v", publishedText, items)
	}
	if hookAt >= obsAt {
		t.Fatalf("final-stream observation must run after the response-part hook; hook=%d observe=%d trace=%+v", hookAt, obsAt, items)
	}

	finishObserved := algFirstKind(items, algTraceStageObserve, string(lipapi.EventResponseFinished))
	if finishObserved < 0 {
		t.Fatalf("final-stream observation never saw the accepted response finish; trace=%+v", items)
	}
	if obsAt >= finishObserved {
		t.Fatalf("the private result must be observed before the accepted response finish; result=%d finish=%d trace=%+v", obsAt, finishObserved, items)
	}

	// The response-part hook runs in two real phases here and both are covered by
	// the checks above. The raw upstream finish passes the hook when it enters the
	// pipeline; the synthesized result frames pass it later, during the accepted
	// terminal's publication preparation, and it is that later hook observation
	// that must precede the result's observation stage (hookAt < obsAt above). The
	// raw finish's hook position is therefore deliberately not compared against
	// the result's.

	// The client-observed terminal frame must arrive after the private result was
	// already observed, which is only possible when the accepted terminal owner
	// released it before completing the response.
	clientTerminal := algFirstKind(items, algTraceStageClient, "response.completed")
	if clientTerminal < 0 {
		clientTerminal = algFirstKind(items, algTraceStageClient, "json_resource")
	}
	if clientTerminal < 0 {
		t.Fatalf("client never observed a terminal frame; frames=%+v", frames)
	}
	if obsAt >= clientTerminal {
		t.Fatalf("the private result must be released before the client observes the terminal; result=%d terminal=%d trace=%+v", obsAt, clientTerminal, items)
	}

	// A streaming cell additionally proves the encoder emitted the published
	// frame only after the observation stage ran for it.
	if streamFrames := algClientTextFrames(frames); streamFrames != nil {
		clientAt := algFirstKind(items, algTraceStageClient, "response.output_text.delta")
		if clientAt < 0 || obsAt >= clientAt {
			t.Fatalf("the client frame must be emitted after the final-stream observation; observe=%d client=%d trace=%+v", obsAt, clientAt, items)
		}
	}

	for _, it := range items {
		if it.tool == algControlToolName {
			t.Fatalf("%s exposed the private control tool name at seq %d: %+v", it.stage, it.seq, it)
		}
	}
}

// algClientTextFrames returns the client-visible text frames of a streaming
// cell, or nil for a non-streaming resource cell.
func algClientTextFrames(frames []algWireFrame) []algWireFrame {
	var out []algWireFrame
	for _, f := range frames {
		if f.Type == "response.output_text.delta" {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- matrix cells -------------------------------------------------------------

// TestPreferredProtocolE2E_completionOnlyPublishesResultAsAssistantText is
// acceptance-matrix row 1: proxy completion as the only model output. The client
// receives the bounded result as ordinary assistant text exactly once and no
// private control fact appears anywhere on the wire.
func TestPreferredProtocolE2E_completionOnlyPublishesResultAsAssistantText(t *testing.T) {
	t.Parallel()

	d, tr := algDeployPreferred(t, TransportJSON, algCompletionOnlyOrigin(t))

	status, frames, err := algPostCreate(t.Context(), d, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, frames[0].Raw)
	}
	if got := algWireText(frames); got != algCompletionResult {
		t.Fatalf("client assistant text = %q, want exactly %q", got, algCompletionResult)
	}
	algAssertNoPrivateControlLeak(t, frames)
	algAssertObservationOrder(t, tr, frames, algCompletionResult)
}

// TestPreferredProtocolE2E_streamedTextThenCompletionDoesNotDuplicate is
// acceptance-matrix row 2 plus row 20: ordinary assistant text is committed and
// delivered exactly once before the private control call, and the bounded result
// is not appended or duplicated. The early-stream observation assertion proves
// the ordinary text the client already saw was observed before the backend
// terminal rather than buffered until completion.
func TestPreferredProtocolE2E_streamedTextThenCompletionDoesNotDuplicate(t *testing.T) {
	t.Parallel()

	d, tr := algDeployPreferred(t, TransportSSE, algStreamedTextThenCompletionOrigin(t))

	status, frames, err := algPostCreate(t.Context(), d, true, tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if n := algCountWireType(frames, "response.completed"); n != 1 {
		t.Fatalf("response.completed frames = %d, want exactly 1; frames=%+v", n, frames)
	}
	if got := algWireText(frames); got != algStreamedText {
		t.Fatalf("client assistant text = %q, want exactly the committed streamed text %q", got, algStreamedText)
	}
	if strings.Contains(algWireText(frames), algCompletionResult) {
		t.Fatal("the committed assistant text was duplicated with the completion result")
	}
	algAssertNoPrivateControlLeak(t, frames)

	// Row 20: the ordinary text is observed through the real seams before the
	// backend terminal, not buffered until completion because the strategy is on.
	items := tr.snapshot()
	textObserved := tr.firstIndex(algTraceStageObserve, string(lipapi.EventTextDelta), algStreamedText)
	if textObserved < 0 {
		t.Fatalf("final-stream observation never observed the streamed text; trace=%+v", items)
	}
	finishObserved := algFirstKind(items, algTraceStageObserve, string(lipapi.EventResponseFinished))
	if finishObserved < 0 || textObserved >= finishObserved {
		t.Fatalf("ordinary text must be observed before the backend terminal; text=%d finish=%d trace=%+v", textObserved, finishObserved, items)
	}
	clientSawText := tr.firstIndex(algTraceStageClient, "response.output_text.delta", algStreamedText)
	if clientSawText < 0 {
		t.Fatalf("client never observed the ordinary text delta; trace=%+v", items)
	}
	textHooked := tr.firstIndex(algTraceStageHook, string(lipapi.EventTextDelta), algStreamedText)
	if textHooked < 0 || textHooked >= textObserved {
		t.Fatalf("the response-part hook must run before the final-stream observation; hook=%d observe=%d trace=%+v", textHooked, textObserved, items)
	}
	if clientSawText >= finishObserved {
		t.Fatalf("the client must observe the ordinary text delta before the response completion; client=%d finish=%d trace=%+v", clientSawText, finishObserved, items)
	}
	for _, it := range items {
		if it.tool == algControlToolName {
			t.Fatalf("%s exposed the private control tool name at seq %d: %+v", it.stage, it.seq, it)
		}
	}
}

// =============================================================================
// Slice 10.1B — message authority, OpenAI-Responses protocol column
// =============================================================================
//
// The five cells below extend the same real seam slice 10.1A deployed. Each drives
// a complete deployment: real frontend handler on a real httptest origin, real
// runtime executor, real backend adapter, and the real reference-provider origin
// emulator. No provider fake is assigned anywhere; the control provider and the
// terminal provider under test are still the ones the production feature factory
// composes, and the observation trace is still driven by the real response-part
// hook and the real final-stream observation stage.
//
// Cases owned here (design Runtime / Acceptance Matrix rows 3, 4, 12, 15; the
// remaining rows and columns are still unclaimed):
//
//  1. ordinary client tool then completion — the ordinary tool lifecycle stays
//     ordinary and the client's answer is honored;
//  2. malformed control call — swallowed privately, never published and never
//     executed as an ordinary client tool;
//  3. multiple control calls — one response admits one control call, so the
//     duplicate revokes the completion and the turn fails closed;
//  4. native completion collision — a client-owned completion tool is never
//     relabelled or intercepted by name, and its native evidence still allows the
//     terminal;
//  5. missing signal — one bounded hidden protocol repair, then the terminal.

// --- slice 10.1B planted facts ------------------------------------------------

// The ordinary client-owned tool facts these cells plant upstream. Unlike the
// private control facts they are REQUIRED to reach the client untouched.
const (
	algOrdinaryToolName  = "run_backfill_check"
	algOrdinaryCallID    = "call_alg_ordinary_backfill"
	algOrdinaryItemID    = "fc_alg_ordinary_backfill"
	algOrdinaryToolArgs  = `{"scope":"full"}`
	algOrdinaryToolInput = "run the backfill check the deployment plan asks for"
	algToolResultOutput  = "backfill verified: 0 rows out of sync"
)

// The client-owned explicit-completion tool facts of the native-collision cell.
// The CLIENT declares this tool, so the proxy owns nothing here and must not
// intercept the call by name alone.
const (
	algNativeCallID = "call_alg_client_owned_completion"
	algNativeItemID = "fc_alg_client_owned_completion"
	algNativeArgs   = `{"result":"All requested work is complete."}`
	algNativeResult = "All requested work is complete."
)

// algUnmarkedText is ordinary assistant text the model commits in the malformed,
// multiple-control-call, and missing-signal turns. It exists so "no result was
// invented" is observable as exact text equality rather than as an absence.
const algUnmarkedText = "Deployment summary draft is ready for review."

// algRepairText is the ordinary assistant text the model commits on the bounded
// protocol-repair leg that follows an unmarked first turn. It is deliberately
// different from algUnmarkedText so the client-visible answer of a two-leg turn
// is exact text equality over both commits, which is what distinguishes
// "two model turns were preserved" from "the proxy duplicated one".
const algRepairText = "Continuing the outstanding verification before reporting."

// algProtocolInstructionMarker and algRepairInstructionMarker are the two private
// instruction markers the proxy may add to the B-leg. They are backend-visible
// control text (design Accounting/Traffic) and must never reach the client.
const (
	algProtocolInstructionMarker = "<task-completion-protocol>"
	algRepairInstructionMarker   = "<automated-completion-protocol-repair>"
)

// --- slice 10.1B upstream wire fixtures ---------------------------------------

// algUpstreamTurn is one planted upstream response for a scripted origin.
type algUpstreamTurn struct {
	JSON string
	SSE  string
}

// algUpstreamLog records every upstream request body the scripted origin served,
// in arrival order. The origin emulator serves requests on its own goroutine, so
// the log owns one mutex.
type algUpstreamLog struct {
	mu     sync.Mutex
	bodies []string
}

func (l *algUpstreamLog) record(body []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bodies = append(l.bodies, string(body))
}

func (l *algUpstreamLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

func (l *algUpstreamLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.bodies...)
}

// at returns the body of upstream request i (0-based), failing when it was never
// served.
func (l *algUpstreamLog) at(t *testing.T, i int) string {
	t.Helper()
	bodies := l.all()
	if i < 0 || i >= len(bodies) {
		t.Fatalf("upstream request %d was never served; served=%d bodies=%q", i, len(bodies), bodies)
	}
	return bodies[i]
}

// algScriptedOrigin returns one stateful multi-request reference-provider origin
// plus the log of every upstream body it served. Turn i answers with turns[i], and
// any request past the end of the script repeats the final turn, so an unexpected
// extra upstream leg stays observable in the count instead of becoming an error
// the fixture cannot assert on.
func algScriptedOrigin(t *testing.T, turns ...algUpstreamTurn) (http.Handler, *algUpstreamLog) {
	t.Helper()
	if len(turns) == 0 {
		t.Fatal("algScriptedOrigin requires at least one scripted turn")
	}
	log := &algUpstreamLog{}
	handler := refopenairesponses.NewHandler(refopenairesponses.Config{
		Responder: func(req refopenairesponses.Request) refopenairesponses.Response {
			log.record(req.Body)
			turn := turns[min(max(int(req.Sequence)-1, 0), len(turns)-1)]
			return refopenairesponses.Response{JSON: turn.JSON, SSE: turn.SSE}
		},
	})
	return handler, log
}

// algResourceJSON renders one completed upstream non-streaming response carrying
// exactly the planted output items.
func algResourceJSON(t *testing.T, id string, output []any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": 1715620000,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output":     output,
		"usage":      map[string]any{"input_tokens": 24, "output_tokens": 11, "total_tokens": 35},
	})
	if err != nil {
		t.Fatalf("marshal upstream resource %q: %v", id, err)
	}
	return string(raw)
}

// algMessageOutput returns the upstream output item for ordinary assistant text.
func algMessageOutput(id, text string) map[string]any {
	return map[string]any{
		"type": "message", "id": id, "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

// algControlOutput returns the upstream output item for one private control call.
// Distinct item and call identities let a cell plant two of them in one response.
func algControlOutput(itemID, callID, args string) map[string]any {
	return map[string]any{
		"type": "function_call", "id": itemID, "call_id": callID,
		"name": algControlToolName, "arguments": args,
	}
}

// algOrdinaryToolOutput returns the upstream output item for the ordinary
// client-owned tool call.
func algOrdinaryToolOutput() map[string]any {
	return map[string]any{
		"type": "function_call", "id": algOrdinaryItemID, "call_id": algOrdinaryCallID,
		"name": algOrdinaryToolName, "arguments": algOrdinaryToolArgs,
	}
}

// algCompletionTurn is the upstream wire of a valid private completion with no
// other model output.
func algCompletionTurn(t *testing.T, id string) algUpstreamTurn {
	t.Helper()
	return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{
		algControlOutput(algControlItemID, algControlCallID, algControlArgs(t, algCompletionResult)),
	})}
}

// algOrdinaryToolTurn is the upstream wire of one ordinary client tool call and
// nothing else.
func algOrdinaryToolTurn(t *testing.T, id string) algUpstreamTurn {
	t.Helper()
	return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{algOrdinaryToolOutput()})}
}

// algUnmarkedStopTurn is the upstream wire of ordinary assistant text followed by
// a clean stop with no completion signal.
func algUnmarkedStopTurn(t *testing.T, id string) algUpstreamTurn {
	t.Helper()
	return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{
		algMessageOutput("msg_"+id, algUnmarkedText),
	})}
}

// algRepairStopTurn is the upstream wire of the bounded protocol-repair leg that
// requirement 7.1 requests after an unmarked first turn: the model commits
// ordinary text and again ends cleanly WITHOUT the completion signal, which is
// the situation requirement 7.4 says must allow the new terminal instead of
// forcing an unbounded sequence of repair turns.
func algRepairStopTurn(t *testing.T, id string) algUpstreamTurn {
	t.Helper()
	return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{
		algMessageOutput("msg_"+id, algRepairText),
	})}
}

// algScriptedMissingSignalOrigin is the approved two-leg missing-signal script
// used by every cell whose first turn carries no trusted completion signal: an
// unmarked first turn, then the unmarked repair leg. Both legs are scripted
// explicitly, so a cell can assert the exact upstream leg count instead of
// accepting whatever the origin happens to repeat.
func algScriptedMissingSignalOrigin(t *testing.T, first algUpstreamTurn, firstID string) (http.Handler, *algUpstreamLog) {
	t.Helper()
	return algScriptedOrigin(t, first, algRepairStopTurn(t, firstID+"_repair"))
}

// --- slice 10.1B client request bodies ----------------------------------------

// algClientTool is one client-declared tool definition on the A-leg. The
// parameterless schema keeps the A-leg wire small; the proxy owns no part of it.
func algClientTool(name string) map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        name,
		"description": "client-owned tool",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"scope": map[string]any{"type": "string"}},
		},
	}
}

// algCreateBodyWith renders an A-leg create body with an arbitrary input document
// and an explicit client tool catalog.
func algCreateBodyWith(t *testing.T, input any, stream bool, tools ...string) string {
	t.Helper()
	doc := map[string]any{"model": "gpt-4o-mini", "input": input, "stream": stream}
	if len(tools) > 0 {
		names := make([]any, 0, len(tools))
		for _, name := range tools {
			names = append(names, algClientTool(name))
		}
		doc["tools"] = names
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal alg create body: %v", err)
	}
	return string(raw)
}

// algToolResultInput is the client answer to one tool call: the official
// function_call_output item the real frontend decodes into a canonical tool result.
func algToolResultInput(callID, output string) map[string]any {
	return map[string]any{"type": "function_call_output", "call_id": callID, "output": output}
}

// algEchoedToolCallInput is the client echo of one model tool call, which is what
// makes a correlated client-owned explicit-completion fact reachable at all.
func algEchoedToolCallInput(callID, name, args string) map[string]any {
	return map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": args}
}

// --- slice 10.1B assertions ---------------------------------------------------

// algWireBody is every client-visible frame payload joined, so a hygiene check
// reads exactly what the client received.
func algWireBody(frames []algWireFrame) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString(f.Raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// algAssertNoPrivateControlFacts fails when any client-visible frame carries a
// private control name, call id, item id, or argument member. It tolerates
// ordinary client tool frames, which several slice 10.1B cells require.
func algAssertNoPrivateControlFacts(t *testing.T, frames []algWireFrame) {
	t.Helper()
	raw := algWireBody(frames)
	for _, secret := range []string{algControlToolName, algControlCallID, algControlItemID, `"result"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("client wire leaked private control fact %q:\n%s", secret, raw)
		}
	}
}

// algAssertNoPrivateControlIdentity is the narrower form used by the native
// collision cell, where the CLIENT legitimately owns a tool called
// attempt_completion: the private call identity and the private argument member
// must still be absent, because the proxy injected and called nothing there.
func algAssertNoPrivateControlIdentity(t *testing.T, frames []algWireFrame) {
	t.Helper()
	raw := algWireBody(frames)
	for _, secret := range []string{algControlCallID, algControlItemID, `"result"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("client wire leaked private control fact %q:\n%s", secret, raw)
		}
	}
}

// algAssertNoPrivateControlStage fails when any internal observation stage — the
// real response-part hook or the real final-stream observer — exposed the private
// control tool name. The client stage is covered by the wire checks above.
func algAssertNoPrivateControlStage(t *testing.T, tr *algTrace) {
	t.Helper()
	for _, it := range tr.snapshot() {
		if it.tool == algControlToolName {
			t.Fatalf("%s exposed the private control tool name at seq %d: %+v", it.stage, it.seq, it)
		}
	}
}

// algAssertOrdinaryToolLifecycle proves the ordinary client tool lifecycle was
// neither suppressed nor duplicated: the real response-part hook and the real
// final-stream observation each saw exactly one start and one finish.
//
// The two stages are counted separately on purpose. They are two genuinely
// distinct observation points on two distinct goroutines, so one canonical start
// is expected to be seen once by each; counting them together would make the
// assertion about their sum rather than about either stage.
//
// Starts are matched by the client-owned tool name, because the canonical
// tool_call_started event carries it. Finishes are counted per stage without a name
// filter, because the canonical tool_call_finished event carries no tool name at
// all; each fixture below plants exactly one ordinary client tool call, so that
// per-stage total is that call's lifecycle and suppression or duplication of the
// finish event stays detectable.
func algAssertOrdinaryToolLifecycle(t *testing.T, tr *algTrace, wantName string, wantStarts, wantFinishes int) {
	t.Helper()
	starts := map[string]int{algTraceStageHook: 0, algTraceStageObserve: 0}
	finishes := map[string]int{algTraceStageHook: 0, algTraceStageObserve: 0}
	for _, it := range tr.snapshot() {
		switch it.kind {
		case string(lipapi.EventToolCallStarted):
			if it.tool == wantName {
				starts[it.stage]++
			}
		case string(lipapi.EventToolCallFinished):
			finishes[it.stage]++
		}
	}
	for _, stage := range []string{algTraceStageHook, algTraceStageObserve} {
		if starts[stage] != wantStarts {
			t.Fatalf("%s %q starts = %d, want %d; trace=%+v", stage, wantName, starts[stage], wantStarts, tr.snapshot())
		}
		if finishes[stage] != wantFinishes {
			t.Fatalf("%s tool-call finishes = %d, want %d; trace=%+v", stage, finishes[stage], wantFinishes, tr.snapshot())
		}
	}
}

// algAssertOrdinaryToolEvent proves the ordinary tool lifecycle really crossed the
// client boundary as an ordinary tool event, in the hook, in the observation
// stage, and on the wire.
func algAssertOrdinaryToolEvent(t *testing.T, tr *algTrace, frames []algWireFrame, name string) {
	t.Helper()
	items := tr.snapshot()
	hookStart, obsStart := -1, -1
	for _, it := range items {
		if it.tool != name {
			continue
		}
		if it.kind != string(lipapi.EventToolCallStarted) {
			continue
		}
		switch it.stage {
		case algTraceStageHook:
			if hookStart < 0 {
				hookStart = it.seq
			}
		case algTraceStageObserve:
			if obsStart < 0 {
				obsStart = it.seq
			}
		}
	}
	if hookStart < 0 || obsStart < 0 {
		t.Fatalf("the ordinary tool start never crossed both internal stages; trace=%+v", items)
	}
	if hookStart >= obsStart {
		t.Fatalf("the ordinary tool start must pass the response-part hook before the observation stage; hook=%d observe=%d trace=%+v", hookStart, obsStart, items)
	}
	algAssertOrdinaryToolLifecycle(t, tr, name, 1, 1)
	if !strings.Contains(algWireBody(frames), `"name":"`+name+`"`) {
		t.Fatalf("the ordinary tool call never reached the client as an ordinary tool event; wire=%s", algWireBody(frames))
	}
}

// algAssertBoundedUpstreamCount fails when the turn used more upstream legs than
// the approved bound, which is how an unbounded repair sequence would show up.
func algAssertBoundedUpstreamCount(t *testing.T, log *algUpstreamLog, want int) {
	t.Helper()
	if got := log.count(); got != want {
		t.Fatalf("upstream request count = %d, want %d; bodies=%q", got, want, log.all())
	}
}

// algAssertUpstreamCarries fails when an upstream request body does NOT carry text
// it must. The B-leg body is the only place the private protocol instruction and
// the private control tool legitimately appear, so this is the positive direction
// of the B-leg-only visibility contract.
func algAssertUpstreamCarries(t *testing.T, log *algUpstreamLog, i int, marker string) {
	t.Helper()
	if body := log.at(t, i); !strings.Contains(body, marker) {
		t.Fatalf("upstream request %d did not carry %q although the protocol was active:\n%s", i, marker, body)
	}
}

// algAssertUpstreamLacks fails when an upstream request body carries text it must
// not. The B-leg body is where a private instruction legitimately appears, so this
// is used only for the negative direction.
func algAssertUpstreamLacks(t *testing.T, log *algUpstreamLog, marker string) {
	t.Helper()
	for i, body := range log.all() {
		if strings.Contains(body, marker) {
			t.Fatalf("upstream request %d carried %q although the protocol is inactive:\n%s", i, marker, body)
		}
	}
}

// --- slice 10.1B matrix cells -------------------------------------------------

// TestPreferredProtocolE2E_ordinaryClientToolThenCompletionPreservesToolLifecycle
// is acceptance-matrix row 3: an ordinary client-visible tool call first, the
// client's answer honored, and the private completion on the following turn.
//
// The first turn carries no completion signal, so requirement 7.1 legitimately
// suppresses its terminal for ONE bounded repair leg, and requirement 7.4 allows
// the new terminal when that leg also stops unmarked. The ordinary tool call is
// NOT held back by that: requirement 5.1 forbids buffering ordinary client tool
// events until terminal, and requirement 5.3 keeps existing client-release
// behavior authoritative. So the cell pins that the ordinary lifecycle crossed the
// client boundary exactly once on the first turn, that the client's answer was
// forwarded upstream exactly once and never re-executed (requirement 8.3), and
// that the bounded result is published exactly once at the accepted terminal of the
// completing turn. The private control call must still never appear anywhere.
func TestPreferredProtocolE2E_ordinaryClientToolThenCompletionPreservesToolLifecycle(t *testing.T) {
	origin, upstream := algScriptedOrigin(t,
		algOrdinaryToolTurn(t, "resp_alg_tool_turn"),
		algRepairStopTurn(t, "resp_alg_tool_repair"),
		algCompletionTurn(t, "resp_alg_tool_completion"),
	)
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	firstBody := algCreateBodyWith(t, algOrdinaryToolInput, false, algOrdinaryToolName)
	status, firstFrames, err := algPostCreateBody(t.Context(), d, firstBody, false, tr)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("first status = %d, want 200; wire=%s", status, algWireBody(firstFrames))
	}

	// Turn one: the client gets exactly one ordinary tool call and no invented
	// assistant answer beyond the repair leg's own committed text.
	algAssertOrdinaryToolEvent(t, tr, firstFrames, algOrdinaryToolName)
	if got := algWireText(firstFrames); got != algRepairText {
		t.Fatalf("client assistant text on the tool turn = %q, want only the repair leg's committed text %q", got, algRepairText)
	}
	if strings.Contains(algWireBody(firstFrames), algCompletionResult) {
		t.Fatal("a completion result was published on the turn that did not complete")
	}
	algAssertNoPrivateControlFacts(t, firstFrames)
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// The client answers its own tool on the next turn. The answer is forwarded
	// upstream exactly once, so the proxy neither dropped nor duplicated it.
	secondBody := algCreateBodyWith(t, []any{algToolResultInput(algOrdinaryCallID, algToolResultOutput)}, false, algOrdinaryToolName)
	status, secondFrames, err := algPostCreateBody(t.Context(), d, secondBody, false, tr)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("second status = %d, want 200; wire=%s", status, algWireBody(secondFrames))
	}
	if got := strings.Count(upstream.at(t, 2), algOrdinaryCallID); got != 1 {
		t.Fatalf("client tool result call id appears %d times upstream, want exactly 1; body=%s", got, upstream.at(t, 2))
	}
	if strings.Contains(upstream.at(t, 2), algToolResultOutput) && strings.Count(upstream.at(t, 2), algToolResultOutput) != 1 {
		t.Fatalf("the client tool result was forwarded more than once upstream; body=%s", upstream.at(t, 2))
	}

	// The bounded result is published exactly once on the completing turn.
	if got := algWireText(secondFrames); got != algCompletionResult {
		t.Fatalf("client assistant text on the completing turn = %q, want exactly %q", got, algCompletionResult)
	}
	if strings.Contains(algWireBody(secondFrames), algOrdinaryCallID) {
		t.Fatalf("the ordinary tool call was replayed to the client on the completing turn:\n%s", algWireBody(secondFrames))
	}
	algAssertNoPrivateControlFacts(t, secondFrames)

	// The ordinary lifecycle ran once in total across both client requests, was
	// never duplicated, and the private control name never entered any stage.
	algAssertOrdinaryToolLifecycle(t, tr, algOrdinaryToolName, 1, 1)
	algAssertNoPrivateControlStage(t, tr)
	algAssertBoundedUpstreamCount(t, upstream, 3)
}

// algMalformedControlCallCase is one planted control-call argument shape the
// frozen control contract must reject. Requirement 5.5 and the design handler
// semantics pin every one of them to the same bounded outcome: the call is
// swallowed privately and never becomes client tool execution.
type algMalformedControlCallCase struct {
	name string
	args string
}

// algMalformedControlCallCases covers the missing, extra, wrong-typed, empty, and
// trailing-value argument shapes plus one oversized result. Every case must produce
// the same observable outcome, so the table is also the cell's discrimination
// matrix: no case may publish a result, and no case may reach the client as a tool.
func algMalformedControlCallCases() []algMalformedControlCallCase {
	oversized := strings.Repeat("a", controltool.MaxResultTextBytes+1)
	return []algMalformedControlCallCase{
		{name: "missing_result", args: `{"summary":"the work is done"}`},
		{name: "extra_member", args: `{"result":"` + algUnmarkedText + `","command":"reboot the proxy"}`},
		{name: "wrong_typed_result", args: `{"result":42}`},
		{name: "empty_result", args: `{"result":"   "}`},
		{name: "trailing_value", args: `{"result":"` + algUnmarkedText + `"}{"result":"` + algUnmarkedText + `"}`},
		{name: "oversized_result", args: `{"result":"` + oversized + `"}`},
	}
}

// TestPreferredProtocolE2E_malformedControlCallIsNeverPublishedOrExecuted is the
// malformed/oversized half of acceptance-matrix row 12.
//
// Requirement 5.5 and the design handler semantics pin the outcome of an invalid
// control-call argument shape: the call is swallowed privately and never becomes
// client tool execution, and no trusted completion fact is recorded (requirement
// 6.1 requires strict valid JSON with one non-empty bounded `result`). A turn that
// therefore carries no trusted completion signal is exactly requirement 7.1's
// missing-signal case: the clean terminal is suppressed and ONE bounded hidden
// protocol-repair continuation is requested. The repair leg itself again ends
// without a completion signal, so requirement 7.4 requires the new terminal to be
// allowed rather than an unbounded repair sequence. The client therefore sees both
// of the model's own committed texts, exactly once each, and nothing else.
func TestPreferredProtocolE2E_malformedControlCallIsNeverPublishedOrExecuted(t *testing.T) {
	for _, tc := range algMalformedControlCallCases() {
		t.Run(tc.name, func(t *testing.T) {
			first := algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_malformed_"+tc.name, []any{
				algMessageOutput("msg_alg_malformed", algUnmarkedText),
				algControlOutput(algControlItemID, algControlCallID, tc.args),
			})}
			origin, upstream := algScriptedMissingSignalOrigin(t, first, "resp_alg_malformed_repair_"+tc.name)
			d, tr := algDeployPreferred(t, TransportJSON, origin)

			body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
			status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			// Requirement 7.4 with requirement 5.6: the bounded repair leg is spent
			// and the turn then terminates normally with its committed output
			// preserved. A provider error surfacing here would be a leak.
			if status != http.StatusOK {
				t.Fatalf("status = %d, want the approved bounded-repair terminal; wire=%s", status, algWireBody(frames))
			}

			// Exactly the two approved legs: the unmarked turn and one repair leg.
			// A third leg would be the unbounded repair sequence requirement 7.4
			// forbids.
			algAssertBoundedUpstreamCount(t, upstream, 2)

			// Requirement 7.2: the repair leg carries the fixed recovery control
			// text, which requirement 3.1 keeps on the B-leg only.
			algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)
			algAssertNoPrivateControlStage(t, tr)

			// No result may be published and no ordinary text may be duplicated:
			// exact equality over both committed texts proves neither an invented
			// result nor a replayed commit.
			if got, want := algWireText(frames), algUnmarkedText+algRepairText; got != want {
				t.Fatalf("client assistant text = %q, want exactly the two committed model texts %q", got, want)
			}
			// The malformed call must not become client tool execution, and no
			// private control fact may reach the client.
			algAssertNoPrivateControlLeak(t, frames)
			algAssertNoPrivateControlFacts(t, frames)
		})
	}
}

// TestPreferredProtocolE2E_multipleControlCallsRevokeTheCompletion is the multiple
// half of acceptance-matrix row 12.
//
// The design capture rules pin V1 to "only one control call may be active per
// response" and "multiple control calls mark protocol invalid and are swallowed
// rather than leaked to client execution". The first call therefore never becomes a
// trusted completion fact (requirement 6.1), so the turn carries no completion
// signal and requirement 7.1 suppresses its terminal for ONE bounded repair leg;
// that leg again ends unmarked, so requirement 7.4 allows the new terminal. The
// client sees the two model texts exactly once each, no completion result from the
// revoked call, and neither control call as client tool execution.
func TestPreferredProtocolE2E_multipleControlCallsRevokeTheCompletion(t *testing.T) {
	const secondCallID = "call_alg_private_control_second"
	const secondItemID = "fc_alg_private_control_second"
	first := algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_multiple_calls", []any{
		algMessageOutput("msg_alg_multiple", algUnmarkedText),
		algControlOutput(algControlItemID, algControlCallID, algControlArgs(t, algCompletionResult)),
		algControlOutput(secondItemID, secondCallID, algControlArgs(t, algCompletionResult)),
	})}
	origin, upstream := algScriptedMissingSignalOrigin(t, first, "resp_alg_multiple_repair")
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
	status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want the approved bounded-repair terminal; wire=%s", status, algWireBody(frames))
	}

	// Exactly one repair leg, then the allowed terminal.
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// The revoked completion publishes nothing: the answer is exactly the two
	// ordinary model texts and neither completion result.
	if got, want := algWireText(frames), algUnmarkedText+algRepairText; got != want {
		t.Fatalf("client assistant text = %q, want exactly the two committed model texts %q with no result", got, want)
	}
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("the revoked completion was still published:\n%s", algWireBody(frames))
	}
	// Neither control call may become client tool execution, and neither private
	// identity may leak or appear in any internal stage.
	algAssertNoPrivateControlLeak(t, frames)
	algAssertNoPrivateControlFacts(t, frames)
	algAssertNoPrivateControlStage(t, tr)
	if strings.Contains(algWireBody(frames), secondCallID) {
		t.Fatalf("the second control call reached the client:\n%s", algWireBody(frames))
	}
}

// TestPreferredProtocolE2E_nativeCompletionCollisionStaysClientOwned is
// acceptance-matrix row 15. The CLIENT declares its own attempt_completion tool, so
// proxy injection fails closed on the name collision and the client's tool stays
// client-owned. The model calls it natively, the client answers it, and the
// correlated native explicit-completion fact must allow the terminal without a
// hidden repair leg and without any second, proxy-owned answer.
func TestPreferredProtocolE2E_nativeCompletionCollisionStaysClientOwned(t *testing.T) {
	origin, upstream := algScriptedOrigin(t,
		algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_native_call", []any{
			algControlOutput(algNativeItemID, algNativeCallID, algNativeArgs),
		})},
		algUnmarkedStopTurn(t, "alg_native_answer"),
	)
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	firstBody := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false, algControlToolName)
	status, firstFrames, err := algPostCreateBody(t.Context(), d, firstBody, false, tr)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("first status = %d, want 200; wire=%s", status, algWireBody(firstFrames))
	}

	// The client's own tool is not relabelled or intercepted by name: its call
	// reaches the client as an ordinary tool event.
	algAssertOrdinaryToolEvent(t, tr, firstFrames, algControlToolName)
	algAssertNoPrivateControlIdentity(t, firstFrames)

	// Proxy injection failed closed on the collision: no private instruction and no
	// second copy of the tool were added to the B-leg.
	algAssertUpstreamLacks(t, upstream, algProtocolInstructionMarker)

	// No missing-signal repair leg was created for the inactive protocol.
	algAssertBoundedUpstreamCount(t, upstream, 1)

	// The client answers its own tool; the correlated native fact allows the
	// terminal, and nothing is invented on top of it.
	secondBody := algCreateBodyWith(t, []any{
		algEchoedToolCallInput(algNativeCallID, algControlToolName, algNativeArgs),
		algToolResultInput(algNativeCallID, algNativeResult),
	}, false, algControlToolName)
	status, secondFrames, err := algPostCreateBody(t.Context(), d, secondBody, false, tr)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("second status = %d, want 200; wire=%s", status, algWireBody(secondFrames))
	}
	if got := algWireText(secondFrames); got != algUnmarkedText {
		t.Fatalf("client assistant text = %q, want only the model's own answer %q with no duplicate answer", got, algUnmarkedText)
	}
	algAssertNoPrivateControlIdentity(t, secondFrames)
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamLacks(t, upstream, algRepairInstructionMarker)
}

// TestPreferredProtocolE2E_missingSignalIsBoundedAndInventsNothing is the
// clean-stop-without-a-control-call case of acceptance-matrix row 4 and the
// terminal half of requirement 7.4.
//
// The model stops cleanly with no control call at all. Requirement 7.1 requires the
// clean terminal to be suppressed and ONE bounded hidden protocol-repair
// continuation to be requested; the repair leg also stops without a completion
// signal, so requirement 7.4 requires the new terminal to be ALLOWED. The cell
// pins all four approved properties at once: no invented result, exactly one
// bounded repair leg carrying the fixed requirement 7.2 control text, both model
// texts delivered exactly once, and no private fact anywhere.
func TestPreferredProtocolE2E_missingSignalIsBoundedAndInventsNothing(t *testing.T) {
	origin, upstream := algScriptedMissingSignalOrigin(t, algUnmarkedStopTurn(t, "resp_alg_missing_signal"), "resp_alg_missing_signal_repair")
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
	status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want the approved bounded-repair terminal; wire=%s", status, algWireBody(frames))
	}

	// Bounded: requirement 10.1 fixes the default reprompt budget at one, so the
	// repaired turn is the last upstream leg and the client gets its terminal.
	algAssertBoundedUpstreamCount(t, upstream, 2)

	// Requirement 7.2: the bounded repair leg carries the fixed recovery control
	// text on the B-leg. Requirement 7.4: no third leg follows it.
	algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// Nothing was invented and nothing was duplicated: the client answer is exactly
	// the two ordinary texts the model itself committed, once each.
	if got, want := algWireText(frames), algUnmarkedText+algRepairText; got != want {
		t.Fatalf("client assistant text = %q, want exactly the two committed model texts %q", got, want)
	}
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("a completion result was invented on a turn with no control call:\n%s", algWireBody(frames))
	}
	// Requirement 3.1/3.5: the private control name stays off every stage and off
	// the wire even though the protocol was active and advertised on the B-leg.
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}
