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
// This file owns two cells of the 10.1 matrix:
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
// Remaining 10.1 cells (ordinary client tool then completion, malformed and
// multiple control calls, native completion collision, missing signal, the
// one-reprompt user-input case, unsupported backend tools, the ToolChoice
// matrix, item-authority twins, and the Anthropic and Gemini protocol
// representatives) are NOT covered here.
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

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	refopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
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
	return d, tr
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
	body := algCreateBody("gpt-4o-mini", stream)
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
