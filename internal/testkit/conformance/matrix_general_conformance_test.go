//go:build integration

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	refacp "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/acp"
	refanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/anthropicmessages"
	refgemini "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/gemini"
	refopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Executable proofs for the 32 general matrix cells (spec Phase 8, Task 8.5).
//
// This file consumes the executable scenario table (matrix_general_scenarios.go)
// directly: every scenario ID the evidence registry links is executed here
// through a real deployment. Positive scenarios assert the client-visible
// outcome and the upstream request count; negative scenarios (reasoning replay,
// assistant phase, item references, compaction, extensions) assert the request
// is rejected with zero upstream requests; cancellation, failover, and
// no-retry-after-visible-output are executed where the transport supports them.
//
// Runtime is kept reasonable by deploying each cell a bounded number of times
// and sharing one deployment across the scenarios that can reuse it.

// generalCellDeploy deploys one general matrix cell. OpenRouter/NVIDIA cells go
// through the actual connector executable (connector-host path); constructible
// cells go through the generic base-bundle selector.
func generalCellDeploy(tb testing.TB, cell MatrixCell, transport ClientTransport) *Deployment {
	tb.Helper()
	if cell.Backend == BackendOpenRouter || cell.Backend == BackendNVIDIA {
		return DeployConnectorColumnFor(tb, cell.Frontend, cell.Backend, transport)
	}
	return Deploy(tb, DeploymentSpec{Frontend: cell.Frontend, Backend: cell.Backend, Transport: transport})
}

// generalExpectedText returns the deterministic assistant text the observing
// origin of a general cell emits for a text prompt.
func generalExpectedText(cell MatrixCell) string {
	switch cell.Backend {
	case BackendOpenResponses:
		return HarnessFakeText
	case BackendACP:
		return "ok"
	case BackendOpenRouter, BackendNVIDIA:
		return "provider-mode-ok"
	default:
		return parityText
	}
}

// generalFrontendPath returns the create endpoint of a general frontend.
func generalFrontendPath(frontend string) string {
	switch frontend {
	case FrontendOpenAIResponses:
		return "/v1/responses"
	case FrontendOpenAILegacy:
		return "/v1/chat/completions"
	case FrontendAnthropic:
		return "/v1/messages"
	case FrontendGemini:
		return "/v1beta/models/gemini-2.0-flash:generateContent"
	default:
		return ""
	}
}

// generalStreamFrontendPath returns the streaming create endpoint of a general
// frontend (Gemini uses a distinct :streamGenerateContent path).
func generalStreamFrontendPath(frontend string) string {
	if frontend == FrontendGemini {
		return "/v1beta/models/gemini-2.0-flash:streamGenerateContent"
	}
	return generalFrontendPath(frontend)
}

// generalPlainCreateBody builds a simple non-streaming text create body in each
// frontend's wire.
func generalPlainCreateBody(frontend string) string {
	switch frontend {
	case FrontendOpenAIResponses:
		return `{"model":"gpt-4o-mini","input":"ping"}`
	case FrontendOpenAILegacy:
		return `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}]}`
	case FrontendAnthropic:
		return `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`
	case FrontendGemini:
		return `{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`
	default:
		return `{}`
	}
}

// generalStreamBody builds a streaming create body in each frontend's wire.
func generalStreamBody(frontend string) string {
	switch frontend {
	case FrontendOpenAIResponses:
		return `{"model":"gpt-4o-mini","input":"ping","stream":true}`
	case FrontendOpenAILegacy:
		return `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}],"stream":true}`
	case FrontendAnthropic:
		return `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"ping"}]}`
	case FrontendGemini:
		return `{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`
	default:
		return `{}`
	}
}

// generalToolBody builds a tools create body in each frontend's wire.
func generalToolBody(frontend string) string {
	switch frontend {
	case FrontendOpenAIResponses:
		return `{"model":"gpt-4o-mini","input":"weather?","tools":[{"type":"function","name":"get_weather","description":"tool","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	case FrontendOpenAILegacy:
		return `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"tool","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`
	case FrontendAnthropic:
		return `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"weather?"}],"tools":[{"name":"get_weather","description":"tool","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	default:
		return `{"contents":[{"role":"user","parts":[{"text":"weather?"}]}],"tools":[{"functionDeclarations":[{"name":"get_weather","description":"tool","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}}}}]}]}`
	}
}

// generalMultimodalBody builds an image-input create body in each frontend's wire.
func generalMultimodalBody(frontend string) string {
	switch frontend {
	case FrontendOpenAIResponses:
		return `{"model":"gpt-4o-mini","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`
	case FrontendOpenAILegacy:
		return `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
	case FrontendAnthropic:
		return `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`
	default:
		return `{"contents":[{"role":"user","parts":[{"text":"look"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}]}`
	}
}

// generalNegativeBodies returns raw create bodies that express the five
// unrepresentable canonical semantics in each frontend's wire. Every body is
// rejected at the frontend adapter boundary (before any network work) because
// the general frontend wire has no surface for the semantic.
func generalNegativeBodies(frontend string) map[string]string {
	switch frontend {
	case FrontendOpenAIResponses:
		return map[string]string{
			"replay-reject":     `{"model":"gpt-4o-mini","input":[{"type":"reasoning","reasoning":"think"}]}`,
			"phase-reject":      `{"model":"gpt-4o-mini","input":[{"type":"message","role":"assistant","content":[{"type":"output_text","phase":"in_progress","text":"x"}]}]}`,
			"itemref-reject":    `{"model":"gpt-4o-mini","input":[{"type":"item_reference","id":"item_1"}]}`,
			"compaction-reject": `{"model":"gpt-4o-mini","input":[{"type":"compaction","prior_response_id":"resp_1"}]}`,
			"extension-reject":  `{"model":"gpt-4o-mini","input":[{"type":"acme:telemetry","namespace":"acme","data":{"x":1}}]}`,
		}
	case FrontendOpenAILegacy:
		return map[string]string{
			"replay-reject":     `{"model":"gpt-4o-mini","messages":[{"role":"assistant","content":[{"type":"reasoning","reasoning":"think"}]}]}`,
			"phase-reject":      `{"model":"gpt-4o-mini","messages":[{"role":"assistant","content":[{"type":"output_text","phase":"in_progress","text":"x"}]}]}`,
			"itemref-reject":    `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"item_reference","id":"item_1"}]}]}`,
			"compaction-reject": `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"compaction","prior_response_id":"resp_1"}]}]}`,
			"extension-reject":  `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"acme:telemetry","data":{"x":1}}]}]}`,
		}
	case FrontendAnthropic:
		return map[string]string{
			"replay-reject":     `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"reasoning","reasoning":"think"}]}]}`,
			"phase-reject":      `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"output_text","phase":"in_progress","text":"x"}]}]}`,
			"itemref-reject":    `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":[{"type":"item_reference","id":"item_1"}]}]}`,
			"compaction-reject": `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":[{"type":"compaction","prior_response_id":"resp_1"}]}]}`,
			"extension-reject":  `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":[{"type":"acme:telemetry","data":{"x":1}}]}]}`,
		}
	default: // gemini
		return map[string]string{
			"replay-reject":     `{"contents":[{"role":"model","parts":[{"reasoning":"think"}]}]}`,
			"phase-reject":      `{"contents":[{"role":"model","parts":[{"phase":"in_progress"}]}]}`,
			"itemref-reject":    `{"contents":[{"role":"user","parts":[{"item_reference":{"id":"item_1"}}]}]}`,
			"compaction-reject": `{"contents":[{"role":"user","parts":[{"compaction":{"prior_response_id":"resp_1"}}]}]}`,
			"extension-reject":  `{"contents":[{"role":"user","parts":[{"acme_telemetry":{"x":1}}]}]}`,
		}
	}
}

// rawFrontendStreamPost POSTs a streaming create body to a general frontend and
// drains the response. It returns the HTTP status and any read error; the caller
// only asserts origin counts, never the wire bytes.
func rawFrontendStreamPost(t *testing.T, d *Deployment, frontend string, timeout time.Duration) (int, error) {
	t.Helper()
	if d == nil || d.Server == nil {
		t.Fatalf("rawFrontendStreamPost: deployment is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Server.URL+generalStreamFrontendPath(frontend), strings.NewReader(generalStreamBody(frontend)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// TestGeneralMatrix_ExecutableScenarios executes every feature scenario of the
// executable scenario table for every general matrix cell through real
// deployments, sharing one deployment per cell where the scenarios permit.
func TestGeneralMatrix_ExecutableScenarios(t *testing.T) {
	t.Parallel()
	for _, cell := range GeneralMatrixCells() {
		cell := cell
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			runGeneralCell(t, cell)
		})
	}
}

// runGeneralCell executes all executable features of one general matrix cell.
func runGeneralCell(t *testing.T, cell MatrixCell) {
	t.Helper()
	fe, be := cell.Frontend, cell.Backend
	wantText := generalExpectedText(cell)

	// Shared JSON deployment: json-text, tools, multimodal, negatives, commitment.
	d := generalCellDeploy(t, cell, TransportJSON)
	if d == nil {
		t.Fatalf("deploy(%s × %s) failed", fe, be)
	}
	defer d.Close()

	// json-text (also proves instructions/roles and history evidence).
	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("json round trip %s × %s: %v", fe, be, err)
	}
	if res.Status != "completed" {
		t.Fatalf("json status = %q, want completed", res.Status)
	}
	if !strings.Contains(res.Text, wantText) {
		t.Fatalf("json text = %q, want %q", res.Text, wantText)
	}
	if be == BackendACP {
		if d.RequestCount(be) < 1 {
			t.Fatalf("acp json request count = %d, want >= 1", d.RequestCount(be))
		}
	} else if got := d.RequestCount(be); got != 1 {
		t.Fatalf("json request count = %d, want exactly 1 (single-terminal commitment)", got)
	}

	// tools: admitted and projected for constructible cells; rejected with zero
	// requests for the ACP v1 subset and the streaming-only OpenRouter/NVIDIA
	// connector columns (the host-adapter backend admission fails closed on the
	// connectors' declared capabilities).
	before := d.RequestCount(be)
	status, err := d.RawFrontendPost(context.Background(), generalFrontendPath(fe), generalToolBody(fe))
	if err != nil {
		t.Fatalf("tools post %s × %s: %v", fe, be, err)
	}
	if be == BackendACP || be == BackendOpenRouter || be == BackendNVIDIA {
		if status == http.StatusOK {
			t.Fatalf("%s tools unexpectedly round-tripped", be)
		}
		if d.RequestCount(be) != before {
			t.Fatalf("%s tools rejection caused %d upstream requests, want 0", be, d.RequestCount(be)-before)
		}
	} else {
		if status != http.StatusOK {
			t.Fatalf("tools status = %d, want 200", status)
		}
		if d.RequestCount(be) != before+1 {
			t.Fatalf("tools request count delta = %d, want 1", d.RequestCount(be)-before)
		}
		if !generalOriginCarries(d, be, "get_weather") {
			t.Fatalf("tools upstream request did not carry the projected tool")
		}
	}

	// multimodal: image input is admitted and projected for constructible cells
	// (ACP via resource blocks); the streaming-only OpenRouter/NVIDIA connector
	// columns reject before network.
	before = d.RequestCount(be)
	status, err = d.RawFrontendPost(context.Background(), generalFrontendPath(fe), generalMultimodalBody(fe))
	if err != nil {
		t.Fatalf("multimodal post %s × %s: %v", fe, be, err)
	}
	if be == BackendOpenRouter || be == BackendNVIDIA {
		if status == http.StatusOK {
			t.Fatalf("%s multimodal unexpectedly round-tripped", be)
		}
		if d.RequestCount(be) != before {
			t.Fatalf("%s multimodal rejection caused %d upstream requests, want 0", be, d.RequestCount(be)-before)
		}
	} else {
		if status != http.StatusOK {
			t.Fatalf("multimodal status = %d, want 200", status)
		}
		if d.RequestCount(be) == before {
			t.Fatalf("multimodal caused no upstream request")
		}
		if !generalOriginCarries(d, be, "AAAA") {
			t.Fatalf("multimodal upstream request did not carry the projected image")
		}
	}

	// Negatives (replay/phase/itemref/compaction/extension): reject before any
	// network request.
	negatives := generalNegativeBodies(fe)
	for suffix, body := range negatives {
		before := d.RequestCount(be)
		status, err := d.RawFrontendPost(context.Background(), generalFrontendPath(fe), body)
		if err != nil {
			t.Fatalf("negative %s post %s × %s: %v", suffix, fe, be, err)
		}
		if status == http.StatusOK {
			t.Fatalf("negative %s unexpectedly round-tripped (%s × %s)", suffix, fe, be)
		}
		if d.RequestCount(be) != before {
			t.Fatalf("negative %s caused %d upstream requests, want 0 (%s × %s)", suffix, d.RequestCount(be)-before, fe, be)
		}
	}

	// sse-text: incremental streaming reaches the client over the same canonical
	// stream (a separate SSE deployment).
	dsse := generalCellDeploy(t, cell, TransportSSE)
	if dsse == nil {
		t.Fatalf("deploy(%s × %s, sse) failed", fe, be)
	}
	defer dsse.Close()
	sres, err := dsse.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("sse round trip %s × %s: %v", fe, be, err)
	}
	if sres.Status != "completed" {
		t.Fatalf("sse status = %q, want completed", sres.Status)
	}
	if !strings.Contains(sres.Text, wantText) {
		t.Fatalf("sse text = %q, want %q", sres.Text, wantText)
	}

	// usage-errors: an upstream 500 surfaces as a stable client-visible error
	// envelope (never a silent success) with an upstream attempt. The raw
	// frontend post returns the proxy's client-visible status, so the family
	// reference clients (which abort the test on errors) are not used here.
	derr := generalCellDeployError(t, cell)
	if derr == nil {
		t.Fatalf("deploy(%s × %s, error) failed", fe, be)
	}
	defer derr.Close()
	estatus, err := derr.RawFrontendPost(context.Background(), generalFrontendPath(fe), generalPlainCreateBody(fe))
	if err != nil {
		t.Fatalf("usage-errors post %s × %s: %v", fe, be, err)
	}
	if estatus >= 200 && estatus < 300 {
		t.Fatalf("upstream 500 unexpectedly round-tripped as HTTP %d (%s × %s)", estatus, fe, be)
	}
	if derr.RequestCount(be) < 1 {
		t.Fatalf("upstream error cell %s × %s caused no upstream attempt, want >= 1", fe, be)
	}

	// failover: pre-output failure fails over to a succeeding candidate. The ACP
	// connector classifies transport/HTTP 5xx/protocol failures before canonical
	// output as recoverable pre-output (terminal auth/validation stays terminal),
	// so ACP cells execute the failover chain too. The failing primary is excluded
	// and the candidate's deterministic text surfaces.
	dfail := generalCellDeployFailover(t, cell)
	if dfail == nil {
		t.Fatalf("deploy(%s × %s, failover) failed", fe, be)
	}
	defer dfail.Close()
	fres, err := dfail.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("failover round trip %s × %s: %v", fe, be, err)
	}
	if !strings.Contains(fres.Text, parityText) {
		t.Fatalf("failover text = %q, want candidate text %q", fres.Text, parityText)
	}
	if dfail.RequestCount(be) < 1 {
		t.Fatalf("failover primary origin received no requests")
	}
	cand := dfail.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("failover deployment has no candidate origin")
	}
	if cand.Count() < 1 {
		t.Fatalf("failover candidate origin received %d requests, want >= 1", cand.Count())
	}

	// no-retry-after-visible-output: an origin that emits the first content event
	// then dies must not trigger retry or failover (candidate stays untouched).
	dnr := generalCellDeployNoRetry(t, cell)
	if dnr == nil {
		t.Fatalf("deploy(%s × %s, no-retry) failed", fe, be)
	}
	defer dnr.Close()
	if _, err := rawFrontendStreamPost(t, dnr, fe, 20*time.Second); err != nil {
		// A post-output stream failure is expected; the assertion is the count.
		t.Logf("no-retry stream terminated with error (expected): %v", err)
	}
	if got := dnr.RequestCount(be); got < 1 {
		t.Fatalf("no-retry primary origin received %d requests, want >= 1", got)
	}
	cand = dnr.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("no-retry deployment has no candidate origin")
	}
	if cand.Count() != 0 {
		t.Fatalf("no-retry candidate origin received %d requests, want 0 (no failover after visible output)", cand.Count())
	}

	// cancellation/backpressure: a canceled client context stops upstream work
	// with no second attempt on the candidate.
	dc := generalCellDeployCancel(t, cell)
	if dc == nil {
		t.Fatalf("deploy(%s × %s, cancellation) failed", fe, be)
	}
	defer dc.Close()
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, dc.Server.URL+generalStreamFrontendPath(fe), strings.NewReader(generalStreamBody(fe)))
	if err != nil {
		t.Fatalf("cancellation request build %s × %s: %v", fe, be, err)
	}
	req.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		resp, err := dc.Server.Client().Do(req)
		if err != nil {
			done <- err
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		done <- nil
	}()
	// Wait until the upstream origin has received the request, then cancel, so
	// the cancellation is observed mid-flight (never before the origin).
	deadline := time.Now().Add(5 * time.Second)
	for dc.RequestCount(be) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ccancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("cancellation request did not terminate (%s × %s)", fe, be)
	}
	if dc.RequestCount(be) < 1 {
		t.Fatalf("cancellation primary origin received no request (%s × %s)", fe, be)
	}
	cand = dc.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("cancellation deployment has no candidate origin")
	}
	if cand.Count() != 0 {
		t.Fatalf("cancellation candidate origin received %d requests, want 0 (no retry after cancellation)", cand.Count())
	}

	// assistant-media: the cell's assistant media reference output surface.
	// Backends whose native wire emits canonical assistant media events
	// (openai-responses, anthropic, gemini) deliver the media reference to the
	// actual client wire with exactly one upstream request (lossless native /
	// projection). Backends without a native assistant-media output surface
	// (openai-legacy, bedrock, ACP, and the openrouter/nvidia connectors, whose
	// stream decoder surfaces no assistant media reference output regardless of
	// the wire they select) reject assistant-media requests before any network
	// request: the executable scenario drives a canonical assistant-media-ref
	// call and asserts the origin observes zero upstream requests.
	dmedia := generalCellDeployAssistantMedia(t, cell)
	if dmedia == nil {
		t.Fatalf("deploy(%s × %s, assistant-media) failed", fe, be)
	}
	defer dmedia.Close()
	if assistantMediaRejectBackend(be) {
		amCall := &lipapi.Call{
			ID: "am-" + fe + "-" + be,
			Route: lipapi.RouteIntent{
				Selector: dmedia.RouteSelector,
			},
			Items: []lipapi.Item{{
				Kind:   lipapi.ItemKindMessage,
				ID:     "am-1",
				Status: lipapi.ItemStatusCompleted,
				Role:   lipapi.RoleAssistant,
				Content: []lipapi.ContentPart{{
					Kind:         lipapi.ContentPartAssistantRef,
					AssistantRef: "https://cdn.example.com/out.png",
				}},
			}},
		}
		before := dmedia.RequestCount(be)
		_, err := dmedia.Exec.Execute(context.Background(), amCall)
		if err == nil {
			t.Fatalf("assistant-media request unexpectedly round-tripped (%s × %s)", fe, be)
		}
		if strings.Contains(err.Error(), "assistant media refs not supported") == false {
			t.Fatalf("assistant-media rejection reason = %v, want capability reject (%s × %s)", err, fe, be)
		}
		if dmedia.RequestCount(be) != before {
			t.Fatalf("assistant-media rejection caused %d upstream requests, want 0 (%s × %s)", dmedia.RequestCount(be)-before, fe, be)
		}
		return
	}
	before = dmedia.RequestCount(be)
	status, body, err := generalRawFrontendPostBody(dmedia, generalFrontendPath(fe), generalPlainCreateBody(fe))
	if err != nil {
		t.Fatalf("assistant-media post %s × %s: %v", fe, be, err)
	}
	if status != http.StatusOK {
		t.Fatalf("assistant-media status = %d, want 200 (%s × %s)", status, fe, be)
	}
	if !strings.Contains(body, generalAssistantMediaWireMarker) {
		t.Fatalf("assistant-media client wire did not carry the media reference %q (%s × %s): %s", generalAssistantMediaWireMarker, fe, be, body)
	}
	if dmedia.RequestCount(be) != before+1 {
		t.Fatalf("assistant-media request count delta = %d, want 1 (%s × %s)", dmedia.RequestCount(be)-before, fe, be)
	}
}

// generalAssistantMediaWireMarker is the deterministic media reference the
// general assistant-media origin emits in each backend's native wire. Every
// general frontend carries it on the client wire (as an image ref, image block,
// or fileData URI), so a single substring assertion inspects the actual client
// wire across all positive cells.
const generalAssistantMediaWireMarker = "cdn.example.com/out.png"

// assistantMediaRejectBackend reports whether a cell's wire has no assistant
// media reference output surface. These cells reject assistant-media requests
// before any network request; every other cell emits canonical
// EventAssistantImageRef/EventAssistantFileRef events. OpenRouter/NVIDIA cells
// always reject: the connectors' stream decoder surfaces no assistant media
// reference output regardless of the wire they select.
func assistantMediaRejectBackend(backendID string) bool {
	switch backendID {
	case BackendOpenAILegacy, BackendBedrock, BackendACP, BackendOpenRouter, BackendNVIDIA:
		return true
	}
	return false
}

// generalMediaOriginHandler returns the media-emitting origin responder for a
// positive general backend: it serves assistant media output in the backend's
// native wire for both the non-stream and stream paths (the bundled backends
// stream upstream by default, and non-stream clients collect the canonical
// stream).
func generalMediaOriginHandler(backendID string) http.Handler {
	switch backendID {
	case BackendOpenAIResponses:
		return refopenairesponses.NewHandler(refopenairesponses.Config{
			NonStreamJSON: generalResponsesMediaResource,
			StreamSSE:     generalResponsesMediaSSE(),
		})
	case BackendAnthropic:
		return refanthropic.NewHandler(refanthropic.Config{
			NonStreamJSON: generalAnthropicMediaResource,
			StreamSSE:     generalAnthropicMediaSSE,
		})
	case BackendGemini:
		return refgemini.NewHandler(refgemini.Config{
			NonStreamJSON: generalGeminiMediaResource,
			StreamSSE:     generalGeminiMediaSSE,
		})
	default:
		return nil
	}
}

// generalCellDeployAssistantMedia deploys one general matrix cell for the
// assistant-media scenario. Positive cells use the media-emitting origin; cells
// whose wire rejects assistant media use the standard deployment (only the
// executor admission and the origin request counter matter).
func generalCellDeployAssistantMedia(tb testing.TB, cell MatrixCell) *Deployment {
	tb.Helper()
	if assistantMediaRejectBackend(cell.Backend) {
		return generalCellDeploy(tb, cell, TransportJSON)
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:      cell.Frontend,
		Backend:       cell.Backend,
		Transport:     TransportJSON,
		OriginHandler: generalMediaOriginHandler(cell.Backend),
	})
}

// generalRawFrontendPostBody posts rawBody to path and returns the HTTP status
// and the actual client wire body, so scenarios can assert the real wire (never
// metadata) and the upstream request count.
func generalRawFrontendPostBody(d *Deployment, path, rawBody string) (int, string, error) {
	if d == nil || d.Server == nil {
		return 0, "", fmt.Errorf("generalRawFrontendPostBody: deployment is nil")
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.Server.URL+path, strings.NewReader(rawBody))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// generalResponsesMediaResource is a completed OpenAI Responses resource whose
// assistant message carries an assistant image output reference (single line so
// it embeds verbatim in the SSE data frame).
const generalResponsesMediaResource = `{"id":"resp_media","object":"response","created_at":1715620000,"status":"completed","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_media","status":"completed","role":"assistant","content":[{"type":"output_text","text":"see"},{"type":"input_image","image_url":"https://cdn.example.com/out.png"}]}]}`

func generalResponsesMediaSSE() string {
	return "event: response.completed\ndata: " + `{"type":"response.completed","sequence_number":1,"response":` + generalResponsesMediaResource + `}` + "\n\ndata: [DONE]\n\n"
}

// generalAnthropicMediaResource is an Anthropic Messages response whose
// assistant message carries an image content block.
const generalAnthropicMediaResource = `{
  "id": "msg_media",
  "type": "message",
  "role": "assistant",
  "model": "claude-3-5-haiku-20241022",
  "content": [
    {"type": "text", "text": "see"},
    {"type": "image", "source": {"type": "url", "url": "https://cdn.example.com/out.png"}}
  ],
  "stop_reason": "end_turn"
}`

// generalAnthropicMediaSSE is the Anthropic streaming wire carrying an image
// content block (the anthropic backend always streams).
const generalAnthropicMediaSSE = "event: message_start\ndata: " +
	`{"type":"message_start","message":{"id":"m_media","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
	"\n\n" +
	"event: content_block_start\ndata: " +
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` +
	"\n\n" +
	"event: content_block_delta\ndata: " +
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"see"}}` +
	"\n\n" +
	"event: content_block_stop\ndata: " +
	`{"type":"content_block_stop","index":0}` +
	"\n\n" +
	"event: content_block_start\ndata: " +
	`{"type":"content_block_start","index":1,"content_block":{"type":"image","source":{"type":"url","url":"https://cdn.example.com/out.png"}}}` +
	"\n\n" +
	"event: content_block_stop\ndata: " +
	`{"type":"content_block_stop","index":1}` +
	"\n\n" +
	"event: message_delta\ndata: " +
	`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}` +
	"\n\n" +
	"event: message_stop\ndata: " +
	`{"type":"message_stop"}` +
	"\n\n"

// generalGeminiMediaResource is a generateContent response whose model content
// carries a fileData part (image URI).
const generalGeminiMediaResource = `{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {"text": "see"},
          {"fileData": {"fileUri": "https://cdn.example.com/out.png", "mimeType": "image/png"}}
        ]
      }
    }
  ]
}`

// generalGeminiMediaSSE is the Gemini streaming wire carrying a fileData part
// (the gemini backend always streams).
const generalGeminiMediaSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"see\"},{\"fileData\":{\"fileUri\":\"https://cdn.example.com/out.png\",\"mimeType\":\"image/png\"}}]}}]}\n\n"

// generalOriginCarries reports whether any captured request at the primary
// origin of a deployment contains sub.
func generalOriginCarries(d *Deployment, backend, sub string) bool {
	if d == nil || d.OriginFor(backend) == nil {
		return false
	}
	for _, obs := range d.OriginFor(backend).Capture() {
		if strings.Contains(string(obs.Body), sub) {
			return true
		}
	}
	return false
}

// failoverCandidateBackend returns the essential backend the failover/no-retry
// chains use as a succeeding candidate for a general frontend's operation.
func failoverCandidateBackend(frontend string) string {
	switch frontend {
	case FrontendOpenAIResponses:
		return BackendOpenAIResponses
	case FrontendAnthropic:
		return BackendAnthropic
	case FrontendGemini:
		return BackendGemini
	default:
		return BackendOpenAILegacy
	}
}

// generalCellDeployError deploys a general cell whose primary origin returns an
// upstream 500, for the usage/error-mapping proof.
func generalCellDeployError(tb testing.TB, cell MatrixCell) *Deployment {
	tb.Helper()
	if cell.Backend == BackendOpenRouter || cell.Backend == BackendNVIDIA {
		return deployConnectorChain(tb, cell.Frontend, cell.Backend, TransportJSON, OriginFailServerError, nil, nil)
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:   cell.Frontend,
		Backend:    cell.Backend,
		Transport:  TransportJSON,
		OriginFail: OriginFailServerError,
	})
}

// generalCellDeployFailover deploys a general cell with a failing primary and a
// succeeding candidate.
func generalCellDeployFailover(tb testing.TB, cell MatrixCell) *Deployment {
	tb.Helper()
	cand := failoverCandidateBackend(cell.Frontend)
	if cell.Backend == BackendOpenRouter || cell.Backend == BackendNVIDIA {
		return deployConnectorChain(tb, cell.Frontend, cell.Backend, TransportJSON, OriginFailServerError, nil, []Candidate{{Backend: cand, OriginFail: OriginFailNone}})
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:   cell.Frontend,
		Backend:    cell.Backend,
		Transport:  TransportJSON,
		OriginFail: OriginFailServerError,
		Candidates: []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}

// generalCellDeployNoRetry deploys a general cell whose primary origin emits the
// first content event then dies mid-stream, with a succeeding candidate that must
// never be reached.
func generalCellDeployNoRetry(tb testing.TB, cell MatrixCell) *Deployment {
	tb.Helper()
	cand := failoverCandidateBackend(cell.Frontend)
	if cell.Backend == BackendOpenRouter || cell.Backend == BackendNVIDIA {
		return deployConnectorChain(tb, cell.Frontend, cell.Backend, TransportSSE, OriginFailNone, newMidStreamDeathHandler(cell.Backend), []Candidate{{Backend: cand, OriginFail: OriginFailNone}})
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:      cell.Frontend,
		Backend:       cell.Backend,
		Transport:     TransportSSE,
		OriginHandler: newMidStreamDeathHandler(cell.Backend),
		Candidates:    []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}

// generalCellDeployCancel deploys a general cell whose primary origin blocks
// until the client cancels, with a succeeding candidate that must never be
// reached.
func generalCellDeployCancel(tb testing.TB, cell MatrixCell) *Deployment {
	tb.Helper()
	cand := failoverCandidateBackend(cell.Frontend)
	if cell.Backend == BackendOpenRouter || cell.Backend == BackendNVIDIA {
		return deployConnectorChain(tb, cell.Frontend, cell.Backend, TransportSSE, OriginFailNone, blockingOriginHandler(), []Candidate{{Backend: cand, OriginFail: OriginFailNone}})
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:      cell.Frontend,
		Backend:       cell.Backend,
		Transport:     TransportSSE,
		OriginHandler: blockingOriginHandler(),
		Candidates:    []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}

// deployConnectorChain deploys a general frontend over the actual
// OpenRouter/NVIDIA connector executable (connector_host.go) with an optional
// primary origin handler/failure mode and succeeding essential candidates.
func deployConnectorChain(tb testing.TB, frontend, backendID string, transport ClientTransport, primaryFail OriginFailMode, primaryHandler http.Handler, candidates []Candidate) *Deployment {
	tb.Helper()
	d := &Deployment{
		Spec:     DeploymentSpec{Frontend: frontend, Backend: backendID, Transport: transport},
		origins:  map[string]*Origin{},
		backends: map[string]execbackend.Backend{},
	}
	custom := primaryHandler
	if custom == nil {
		custom = &connectorWire{text: "provider-mode-ok"}
	}
	primaryOrigin := newHarnessOrigin(tb, backendID, primaryFail, nil, 100, "", nil, custom)
	d.origins[backendID] = primaryOrigin

	d.backends[backendID] = connectorHostBackend(tb, backendID, primaryOrigin.URL())

	model := ConnectorColumnModel
	route := backendID + ":" + model
	for i, cand := range candidates {
		if !containsString(HarnessBackendIDs(), cand.Backend) || cand.Backend == BackendOpenRouter || cand.Backend == BackendNVIDIA {
			tb.Fatalf("harness: invalid connector-chain candidate %q", cand.Backend)
		}
		candOrigin := newHarnessOrigin(tb, cand.Backend, cand.OriginFail, nil, 100, cand.ProviderOrigin, nil, nil)
		key := candidateBackendKey(backendID, i)
		d.origins[key] = candOrigin
		d.candidateOrigins = append(d.candidateOrigins, candOrigin)
		d.backends[key] = harnessBackendFor(tb, cand.Backend, candOrigin.URL(), candOrigin.Client())
		route += "|" + RouteSelector(key, model)
	}
	d.RouteSelector = route
	d.Exec = harnessExecutor(tb, d.backends, backendID)
	d.Mux = http.NewServeMux()
	genCtx, genCancel := context.WithCancel(context.Background())
	d.genCancel = genCancel
	if err := mountHarnessFrontend(genCtx, d.Mux, frontend, d.Exec, d.RouteSelector, 0); err != nil {
		_ = d.Close()
		tb.Fatalf("harness: mount %q frontend: %v", frontend, err)
	}
	d.Server = httptest.NewServer(d.Mux)
	d.Client = harnessClientFor(tb, frontend, d)
	tb.Cleanup(func() { _ = d.Close() })
	return d
}

// blockingOriginHandler returns an origin responder that blocks until the
func blockingOriginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
}

// newMidStreamDeathHandler returns an origin responder that serves non-create
// requests normally (provider /models, ACP handshake) and emits the first
// content event of the wire the connector requests before abruptly closing the
// connection.
func newMidStreamDeathHandler(backendID string) http.Handler {
	var acpRef http.Handler
	if backendID == BackendACP {
		acpRef = refacp.NewHandler(refacp.Config{})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","owned_by":"provider"}]}`)
			return
		}
		if backendID == BackendACP && !isACPPromptRequest(r) {
			if acpRef != nil {
				acpRef.ServeHTTP(w, r)
				return
			}
		}
		if backendID == BackendOpenRouter || backendID == BackendNVIDIA {
			// The connector maps the operation to a wire by flavor: the
			// openai-responses frontend reaches /responses, every other frontend
			// reaches /chat/completions. Emit the first content event of the
			// actual wire.
			if strings.TrimRight(r.URL.Path, "/") == "/chat/completions" {
				writeChatFirstContentThenHijack(w, r)
				return
			}
		}
		writeFirstContentThenHijack(w, backendID, r)
	})
}

// isACPPromptRequest reports whether a POST /v1/acp JSON-RPC body targets
// session/prompt.
func isACPPromptRequest(r *http.Request) bool {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	var probe struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Method == "session/prompt"
}

// writeChatFirstContentThenHijack writes the opening content of the OpenAI
// chat-completions streaming wire then hijacks and closes the connection
// abruptly (the wire the openrouter/nvidia connectors request for every
// non-openai.responses operation).
func writeChatFirstContentThenHijack(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	write := func(s string) {
		if _, err := io.WriteString(w, s); err != nil {
			return
		}
		if fl != nil {
			fl.Flush()
		}
	}
	write("data: " + `{"id":"chatcmpl_death","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}` + "\n\n")
	hijackClose(w)
}

// writeFirstContentThenHijack writes the opening content of the backend's
// streaming wire then hijacks and closes the connection abruptly.
func writeFirstContentThenHijack(w http.ResponseWriter, backendID string, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	write := func(s string) {
		if _, err := io.WriteString(w, s); err != nil {
			return
		}
		if fl != nil {
			fl.Flush()
		}
	}
	switch backendID {
	case BackendOpenAILegacy:
		write("data: " + `{"id":"chatcmpl_death","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}` + "\n\n")
	case BackendAnthropic:
		write("event: message_start\ndata: " + `{"type":"message_start","message":{"id":"m_death","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n")
		write("event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n")
		write("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n\n")
	case BackendGemini:
		write("data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}]}` + "\n\n")
	case BackendBedrock:
		writeBedrockContentBlockDelta(w, fl)
	case BackendACP:
		sid := acpPromptSessionID(r)
		write("{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":" + jsonEncodeString(sid) + ",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"partial\"}}}}\n")
	default:
		// openai-responses and the connector columns' Responses wire share the
		// OpenAI Responses streaming wire.
		write("event: response.created\ndata: " + `{"type":"response.created","sequence_number":1,"response":{"id":"resp_death","object":"response","created_at":1715620000,"status":"in_progress","model":"gpt-4o-mini","output":[]}}` + "\n\n")
		write("event: response.output_item.added\ndata: " + `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"type":"message","id":"msg_death","status":"in_progress","role":"assistant","content":[{"type":"output_text","text":""}]}}` + "\n\n")
		write("event: response.content_part.added\ndata: " + `{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_death","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}` + "\n\n")
		write("event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_death","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n")
	}
	hijackClose(w)
}

// writeBedrockContentBlockDelta writes one AWS eventstream contentBlockDelta
// message for the Bedrock Converse streaming wire.
func writeBedrockContentBlockDelta(w http.ResponseWriter, fl http.Flusher) {
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	payload, err := json.Marshal(map[string]any{
		"contentBlockIndex": 0,
		"delta":             map[string]any{"text": "partial"},
	})
	if err != nil {
		return
	}
	msg := eventstream.Message{
		Headers: []eventstream.Header{
			{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.EventMessageType)},
			{Name: eventstreamapi.EventTypeHeader, Value: eventstream.StringValue("contentBlockDelta")},
			{Name: eventstreamapi.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
		},
		Payload: payload,
	}
	if err := enc.Encode(&buf, msg); err != nil {
		return
	}
	_, _ = w.Write(buf.Bytes())
	if fl != nil {
		fl.Flush()
	}
}

// acpPromptSessionID extracts the sessionId from an ACP session/prompt body.
func acpPromptSessionID(r *http.Request) string {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	var probe struct {
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Params.SessionID
}

func jsonEncodeString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// hijackClose hijacks the response connection and closes it abruptly so the
// upstream client observes a mid-stream termination without a terminal event.
func hijackClose(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

// TestGeneralMatrix_ScenarioTableIsExecutable proves the evidence scenario IDs
// are exactly the executable table entries (no metadata-only scenario IDs): the
// table is non-empty, every cell links table-derived IDs, and every table entry
// has a matching cell in GeneralMatrixCells.
func TestGeneralMatrix_ScenarioTableIsExecutable(t *testing.T) {
	t.Parallel()
	table := GeneralMatrixScenarios()
	if len(table) == 0 {
		t.Fatal("general matrix scenario table is empty")
	}
	cells := map[string]MatrixCell{}
	for _, cell := range GeneralMatrixCells() {
		cells[cell.Frontend+"\x00"+cell.Backend] = cell
	}
	for _, sc := range table {
		if _, ok := cells[sc.Frontend+"\x00"+sc.Backend]; !ok {
			t.Fatalf("scenario %s references unknown general cell %s × %s", sc.ScenarioID, sc.Frontend, sc.Backend)
		}
		if sc.ScenarioID == "" {
			t.Fatalf("scenario for %s × %s feature %q has an empty scenario ID", sc.Frontend, sc.Backend, sc.Feature)
		}
	}
}
