//go:build integration

package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Executable proofs for the five OpenResponses backend column cells (spec Phase
// 8, Task 8.4).
//
// This file consumes the executable scenario table
// (openresponses_backend_column_scenarios.go) directly: every scenario ID the
// evidence registry links is executed here through a real deployment with the
// independent OpenResponses refbackend injected as the reference-provider
// origin. Positive scenarios assert the client-visible outcome and the exact
// refbackend request count; negative scenarios assert rejection with zero
// reference-backend requests. Continuation is positive only for the
// OpenResponses frontend cell (the proxy-owned continuation surface) and is
// honestly out_of_scope for the legacy column frontends. Cancellation,
// pre-output failover, and post-visible no-retry are executed for every column
// cell with exact request-count assertions.

// TestOpenResponsesBackendColumn_ExecutableScenarios executes every feature
// scenario of the column executable table for every column cell.
func TestOpenResponsesBackendColumn_ExecutableScenarios(t *testing.T) {
	t.Parallel()
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		frontend := frontend
		t.Run(frontend, func(t *testing.T) {
			t.Parallel()
			runOpenResponsesBackendColumnCell(t, frontend)
		})
	}
}

// columnFrontendPath returns the create endpoint of a column frontend.
func columnFrontendPath(frontend string) string {
	if frontend == FrontendOpenResponses {
		return "/openresponses/v1/responses"
	}
	return generalFrontendPath(frontend)
}

// columnPlainCreateBody returns a simple non-streaming text create body in each
// column frontend's wire.
func columnPlainCreateBody(frontend string) string {
	if frontend == FrontendOpenResponses {
		return `{"model":"gpt-4o-mini","input":"ping","store":false}`
	}
	return generalPlainCreateBody(frontend)
}

// columnToolBody returns a tools create body in each column frontend's wire.
func columnToolBody(frontend string) string {
	if frontend == FrontendOpenResponses {
		return `{"model":"gpt-4o-mini","store":false,"input":"weather?","tools":[{"type":"function","name":"get_weather","description":"get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	}
	return generalToolBody(frontend)
}

// columnMultimodalBody returns an image-input create body in each column
// frontend's wire.
func columnMultimodalBody(frontend string) string {
	if frontend == FrontendOpenResponses {
		return `{"model":"gpt-4o-mini","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`
	}
	return generalMultimodalBody(frontend)
}

// columnNegativeBodies returns raw create bodies that express the five
// unrepresentable canonical semantics in each column frontend's wire. Every
// body is rejected at the frontend adapter boundary (before any
// reference-backend request). The OpenResponses frontend cell is the positive
// compaction exception (the generic backend declares the capability), so its
// compaction body uses the positive "compaction" suffix.
func columnNegativeBodies(frontend string) map[string]string {
	if frontend == FrontendOpenResponses {
		return map[string]string{
			"replay-reject":    `{"model":"gpt-4o-mini","store":false,"input":[{"type":"reasoning","reasoning":"think"}]}`,
			"phase-reject":     `{"model":"gpt-4o-mini","store":false,"input":[{"type":"message","role":"assistant","phase":"in_progress","content":[{"type":"output_text","text":"x"}]}]}`,
			"itemref-reject":   `{"model":"gpt-4o-mini","store":false,"input":[{"type":"item_reference","id":"item_1"}]}`,
			"compaction":       `{"model":"gpt-4o-mini","store":false,"input":[{"type":"compaction","prior_response_id":"resp_1"}]}`,
			"extension-reject": `{"model":"gpt-4o-mini","store":false,"input":[{"type":"acme:telemetry","namespace":"acme","data":{"x":1}}]}`,
		}
	}
	return generalNegativeBodies(frontend)
}

// columnStreamBody returns a streaming create body in each column frontend's
// wire.
func columnStreamBody(frontend string) string {
	if frontend == FrontendOpenResponses {
		return `{"model":"gpt-4o-mini","input":"ping","stream":true,"store":false}`
	}
	return generalStreamBody(frontend)
}

// columnStreamPath returns the streaming create endpoint of a column frontend.
func columnStreamPath(frontend string) string {
	if frontend == FrontendOpenResponses {
		return "/openresponses/v1/responses"
	}
	return generalStreamFrontendPath(frontend)
}

// columnFailoverCandidate returns the succeeding candidate backend for a column
// frontend's operation: the OpenResponses backend for the OpenResponses
// frontend, and the same-family essential backend for the legacy frontends.
func columnFailoverCandidate(frontend string) string {
	if frontend == FrontendOpenResponses {
		return BackendOpenResponses
	}
	return failoverCandidateBackend(frontend)
}

// columnFailoverCandidateText returns the deterministic candidate-origin text a
// column failover round trip must surface.
func columnFailoverCandidateText(frontend string) string {
	if frontend == FrontendOpenResponses {
		return HarnessFakeText
	}
	return parityText
}

// runOpenResponsesBackendColumnCell executes all executable features of one
// column cell. The shared refbackend deployment covers json-text, tools,
// multimodal, negatives, and continuation (OpenResponses frontend cell only);
// dedicated deployments cover SSE text, usage/errors, failover, no-retry, and
// cancellation.
func runOpenResponsesBackendColumnCell(t *testing.T, frontend string) {
	t.Helper()
	fe := frontend

	// json-text: legacy frontends' message-authority calls (or the OpenResponses
	// frontend's item authority) project through the explicit legacy→ordered
	// items projector and the independent refbackend captures exactly one
	// ordered create request.
	d, ref := backendColumnDeploy(t, fe, TransportJSON)
	if d == nil {
		t.Fatalf("deploy(%s, json) failed", fe)
	}
	defer d.Close()
	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("%s -> openresponses json round trip: %v", fe, err)
	}
	if res.Status != "completed" {
		t.Fatalf("%s -> openresponses status = %q, want completed", fe, res.Status)
	}
	if !strings.Contains(res.Text, "column-ok") {
		t.Fatalf("%s -> openresponses text = %q, want column-ok", fe, res.Text)
	}
	if ref.Capture().Total() != 1 {
		t.Fatalf("%s -> openresponses json refbackend request total = %d, want exactly 1", fe, ref.Capture().Total())
	}
	backendColumnCapturedInput(t, ref)

	// tools: projected to ordered function items on the OpenResponses wire.
	before := ref.Capture().Total()
	status, err := d.RawFrontendPost(context.Background(), columnFrontendPath(fe), columnToolBody(fe))
	if err != nil {
		t.Fatalf("tools post through %s: %v", fe, err)
	}
	if status != http.StatusOK {
		t.Fatalf("%s tools status = %d, want 200", fe, status)
	}
	if ref.Capture().Total() != before+1 {
		t.Fatalf("%s tools refbackend request delta = %d, want exactly 1", fe, ref.Capture().Total()-before)
	}
	if !rowOriginHasSubstring(d, BackendOpenResponses, "get_weather") {
		t.Fatalf("%s tools upstream request did not carry the projected tool", fe)
	}

	// multimodal: image input projects to the ordered image item on the wire.
	before = ref.Capture().Total()
	status, err = d.RawFrontendPost(context.Background(), columnFrontendPath(fe), columnMultimodalBody(fe))
	if err != nil {
		t.Fatalf("multimodal post through %s: %v", fe, err)
	}
	if status != http.StatusOK {
		t.Fatalf("%s multimodal status = %d, want 200", fe, status)
	}
	if ref.Capture().Total() != before+1 {
		t.Fatalf("%s multimodal refbackend request delta = %d, want exactly 1", fe, ref.Capture().Total()-before)
	}
	if !rowOriginHasSubstring(d, BackendOpenResponses, "AAAA") {
		t.Fatalf("%s multimodal upstream request did not carry the projected image", fe)
	}

	// Negatives (replay/phase/itemref/compaction/extension): rejected at the
	// frontend adapter boundary with zero reference-backend requests. The
	// OpenResponses frontend cell is the documented positive exception for
	// compaction (the generic backend declares the capability); its executable
	// scenario uses the positive "compaction" suffix.
	negatives := columnNegativeBodies(fe)
	for suffix, body := range negatives {
		before := ref.Capture().Total()
		status, err := d.RawFrontendPost(context.Background(), columnFrontendPath(fe), body)
		if err != nil {
			t.Fatalf("negative %s post through %s: %v", suffix, fe, err)
		}
		if suffix == "compaction" {
			if status != http.StatusOK {
				t.Fatalf("%s compaction status = %d, want 200 (compaction capability declared)", fe, status)
			}
			if ref.Capture().Total() != before+1 {
				t.Fatalf("%s compaction refbackend request delta = %d, want exactly 1", fe, ref.Capture().Total()-before)
			}
			continue
		}
		if status == http.StatusOK {
			t.Fatalf("negative %s unexpectedly round-tripped through %s", suffix, fe)
		}
		if ref.Capture().Total() != before {
			t.Fatalf("negative %s caused %d reference-backend requests, want 0 (through %s)", suffix, ref.Capture().Total()-before, fe)
		}
	}

	// continuation: positive only for the OpenResponses frontend cell (proxy
	// continuation surface). The second store:true create with
	// previous_response_id re-executes through the refbackend exactly once.
	if fe == FrontendOpenResponses {
		firstID, err := columnStoreCreate(t, d)
		if err != nil {
			t.Fatalf("openresponses column continuation first create: %v", err)
		}
		before := ref.Capture().Total()
		secondID, err := columnStoreCreateWithParent(t, d, firstID)
		if err != nil {
			t.Fatalf("openresponses column continuation second create: %v", err)
		}
		if secondID == "" || secondID == firstID {
			t.Fatalf("openresponses column continuation second id = %q, want a new proxy id distinct from %q", secondID, firstID)
		}
		if ref.Capture().Total() != before+1 {
			t.Fatalf("openresponses column continuation re-execution refbackend request delta = %d, want exactly 1", ref.Capture().Total()-before)
		}
	}

	// sse-text: incremental streaming reaches the client over the same canonical
	// stream (a separate SSE refbackend deployment).
	dsse, refsse := backendColumnDeploy(t, fe, TransportSSE)
	if dsse == nil {
		t.Fatalf("deploy(%s, sse) failed", fe)
	}
	defer dsse.Close()
	sres, err := dsse.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("%s -> openresponses sse round trip: %v", fe, err)
	}
	if sres.Status != "completed" {
		t.Fatalf("%s -> openresponses sse status = %q, want completed", fe, sres.Status)
	}
	if !strings.Contains(sres.Text, "column-ok") {
		t.Fatalf("%s -> openresponses sse text = %q, want column-ok", fe, sres.Text)
	}
	if refsse.Capture().Total() != 1 {
		t.Fatalf("%s -> openresponses sse refbackend request total = %d, want exactly 1", fe, refsse.Capture().Total())
	}
	if fe == FrontendOpenResponses {
		terminals := 0
		for _, ev := range sres.Events {
			if ev == "response.completed" {
				terminals++
			}
		}
		if terminals != 1 {
			t.Fatalf("%s -> openresponses sse saw %d terminal events, want exactly 1 (single-terminal commitment)", fe, terminals)
		}
	}

	// usage-errors: an upstream 500 surfaces as a stable client-visible error
	// (never a silent success) with an upstream attempt.
	derr := columnCellDeployError(t, fe)
	if derr == nil {
		t.Fatalf("deploy(%s, openresponses, error) failed", fe)
	}
	defer derr.Close()
	estatus, err := derr.RawFrontendPost(context.Background(), columnFrontendPath(fe), columnPlainCreateBody(fe))
	if err != nil {
		t.Fatalf("usage-errors post through %s: %v", fe, err)
	}
	if estatus >= 200 && estatus < 300 {
		t.Fatalf("upstream 500 unexpectedly round-tripped as HTTP %d (through %s)", estatus, fe)
	}
	if derr.RequestCount(BackendOpenResponses) < 1 {
		t.Fatalf("upstream error cell %s caused no upstream attempt, want >= 1", fe)
	}

	// failover: pre-output failure fails over to the succeeding candidate; both
	// origin counts are asserted.
	dfail := columnCellDeployFailover(t, fe)
	if dfail == nil {
		t.Fatalf("deploy(%s, openresponses, failover) failed", fe)
	}
	defer dfail.Close()
	fres, err := dfail.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("failover round trip through %s: %v", fe, err)
	}
	if want := columnFailoverCandidateText(fe); !strings.Contains(fres.Text, want) {
		t.Fatalf("failover text = %q, want candidate text %q (through %s)", fres.Text, want, fe)
	}
	if dfail.RequestCount(BackendOpenResponses) < 1 {
		t.Fatalf("failover primary origin received no requests (through %s)", fe)
	}
	cand := dfail.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("failover deployment has no candidate origin (through %s)", fe)
	}
	if cand.Count() < 1 {
		t.Fatalf("failover candidate origin received %d requests, want >= 1 (through %s)", cand.Count(), fe)
	}

	// no-retry-after-visible-output: an origin that emits the first content
	// event then dies must not trigger retry or failover (candidate stays
	// untouched).
	dnr := columnCellDeployNoRetry(t, fe)
	if dnr == nil {
		t.Fatalf("deploy(%s, openresponses, no-retry) failed", fe)
	}
	defer dnr.Close()
	if _, err := rawColumnStreamPost(t, dnr, fe, 20*time.Second); err != nil {
		// A post-output stream failure is expected; the assertion is the count.
		t.Logf("no-retry stream terminated with error (expected): %v", err)
	}
	if dnr.RequestCount(BackendOpenResponses) < 1 {
		t.Fatalf("no-retry primary origin received %d requests, want >= 1 (through %s)", dnr.RequestCount(BackendOpenResponses), fe)
	}
	cand = dnr.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("no-retry deployment has no candidate origin (through %s)", fe)
	}
	if cand.Count() != 0 {
		t.Fatalf("no-retry candidate origin received %d requests, want 0 (no failover after visible output, through %s)", cand.Count(), fe)
	}

	// cancellation/backpressure: a canceled client context stops upstream work
	// with no second attempt on the candidate.
	dc := columnCellDeployCancel(t, fe)
	if dc == nil {
		t.Fatalf("deploy(%s, openresponses, cancellation) failed", fe)
	}
	defer dc.Close()
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, dc.Server.URL+columnStreamPath(fe), strings.NewReader(columnStreamBody(fe)))
	if err != nil {
		t.Fatalf("cancellation request build through %s: %v", fe, err)
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
	deadline := time.Now().Add(5 * time.Second)
	for dc.RequestCount(BackendOpenResponses) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ccancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("cancellation request did not terminate (through %s)", fe)
	}
	if dc.RequestCount(BackendOpenResponses) < 1 {
		t.Fatalf("cancellation primary origin received no request (through %s)", fe)
	}
	cand = dc.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("cancellation deployment has no candidate origin (through %s)", fe)
	}
	if cand.Count() != 0 {
		t.Fatalf("cancellation candidate origin received %d requests, want 0 (no retry after cancellation, through %s)", cand.Count(), fe)
	}
}

// columnStoreCreate performs one store:true non-streaming create through the
// OpenResponses frontend and returns the proxy-issued response id.
func columnStoreCreate(t *testing.T, d *Deployment) (string, error) {
	t.Helper()
	return columnStoreCreateWithParent(t, d, "")
}

// columnStoreCreateWithParent performs one store:true create with an optional
// previous_response_id and returns the proxy-issued response id.
func columnStoreCreateWithParent(t *testing.T, d *Deployment, parentID string) (string, error) {
	t.Helper()
	payload := map[string]any{"model": "gpt-4o-mini", "input": "next", "store": true}
	if parentID != "" {
		payload["previous_response_id"] = parentID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.Server.URL+"/openresponses/v1/responses", strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LIP-Session-Id", "sess-openresponses-column")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", &columnStatusError{status: resp.StatusCode, body: string(body)}
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", &columnStatusError{status: resp.StatusCode, body: "create resource has no id"}
	}
	return out.ID, nil
}

type columnStatusError struct {
	status int
	body   string
}

func (e *columnStatusError) Error() string {
	return "create status " + itoa(e.status) + ": " + e.body
}

// rawColumnStreamPost POSTs a streaming create body to a column frontend and
// drains the response. It returns the HTTP status and any read error; the
// caller only asserts origin counts, never the wire bytes.
func rawColumnStreamPost(t *testing.T, d *Deployment, frontend string, timeout time.Duration) (int, error) {
	t.Helper()
	if d == nil || d.Server == nil {
		t.Fatalf("rawColumnStreamPost: deployment is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Server.URL+columnStreamPath(frontend), strings.NewReader(columnStreamBody(frontend)))
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

// columnCellDeployError deploys a column cell whose primary origin returns an
// upstream 500, for the usage/error-mapping proof.
func columnCellDeployError(tb testing.TB, frontend string) *Deployment {
	tb.Helper()
	return Deploy(tb, DeploymentSpec{
		Frontend:   frontend,
		Backend:    BackendOpenResponses,
		Transport:  TransportJSON,
		OriginFail: OriginFailServerError,
	})
}

// columnCellDeployFailover deploys a column cell with a failing primary and a
// succeeding candidate.
func columnCellDeployFailover(tb testing.TB, frontend string) *Deployment {
	tb.Helper()
	cand := columnFailoverCandidate(frontend)
	return Deploy(tb, DeploymentSpec{
		Frontend:   frontend,
		Backend:    BackendOpenResponses,
		Transport:  TransportJSON,
		OriginFail: OriginFailServerError,
		Candidates: []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}

// columnCellDeployNoRetry deploys a column cell whose primary origin emits the
// first content event then dies mid-stream, with a succeeding candidate that
// must never be reached.
func columnCellDeployNoRetry(tb testing.TB, frontend string) *Deployment {
	tb.Helper()
	cand := columnFailoverCandidate(frontend)
	return Deploy(tb, DeploymentSpec{
		Frontend:      frontend,
		Backend:       BackendOpenResponses,
		Transport:     TransportSSE,
		OriginHandler: newMidStreamDeathHandler(BackendOpenResponses),
		Candidates:    []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}

// columnCellDeployCancel deploys a column cell whose primary origin blocks until
// the client cancels, with a succeeding candidate that must never be reached.
func columnCellDeployCancel(tb testing.TB, frontend string) *Deployment {
	tb.Helper()
	cand := columnFailoverCandidate(frontend)
	return Deploy(tb, DeploymentSpec{
		Frontend:      frontend,
		Backend:       BackendOpenResponses,
		Transport:     TransportSSE,
		OriginHandler: blockingOriginHandler(),
		Candidates:    []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}
