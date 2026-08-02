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

// Executable proofs for the nine OpenResponses frontend row cells (spec Phase 8,
// Task 8.3).
//
// This file consumes the executable scenario table
// (openresponses_frontend_row_scenarios.go) directly: every scenario ID the
// evidence registry links is executed here through a real deployment. Positive
// scenarios assert the client-visible outcome and the upstream request count;
// negative scenarios (reasoning replay, assistant phase, item references,
// compaction, extensions) assert the request is rejected with zero upstream
// requests. Continuation (positive, proxy-owned), cancellation/backpressure,
// pre-output failover, and post-visible no-retry are each executed for every row
// cell with their own scenarios and exact request-count assertions.

// TestOpenResponsesFrontendRow_ExecutableScenarios executes every feature
// scenario of the row executable table for every row cell.
func TestOpenResponsesFrontendRow_ExecutableScenarios(t *testing.T) {
	t.Parallel()
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			runOpenResponsesFrontendRowCell(t, backend)
		})
	}
}

// rowStreamingCreateBody is the OpenResponses create wire for the row streaming
// scenarios (cancellation / no-retry).
func rowStreamingCreateBody() string {
	return `{"model":"gpt-4o-mini","input":"ping","stream":true,"store":false}`
}

// rowStoreCreate performs one store:true create under the row session and
// returns the proxy-issued response id (the continuation handle).
func rowStoreCreate(t *testing.T, d *Deployment, prevID string) (string, error) {
	t.Helper()
	payload := map[string]any{"model": "gpt-4o-mini", "input": "next", "store": true}
	if prevID != "" {
		payload["previous_response_id"] = prevID
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
	req.Header.Set("X-LIP-Session-Id", "sess-openresponses-row")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", &rowStatusError{status: resp.StatusCode, body: string(body)}
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", &rowStatusError{status: resp.StatusCode, body: "create resource has no id"}
	}
	return out.ID, nil
}

type rowStatusError struct {
	status int
	body   string
}

func (e *rowStatusError) Error() string {
	return "create status " + itoa(e.status) + ": " + e.body
}

func itoa(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// runOpenResponsesFrontendRowCell executes all executable features of one row
// cell. The shared JSON deployment covers json-text, tools, multimodal,
// negatives, and continuation; dedicated deployments cover SSE text,
// usage/errors, failover, no-retry, and cancellation.
func runOpenResponsesFrontendRowCell(t *testing.T, backend string) {
	t.Helper()
	wantText := rowExpectedText(backend)
	be := backend

	d := rowDeploy(t, be, TransportJSON)
	if d == nil {
		t.Fatalf("deploy(openresponses, %s) failed", be)
	}
	defer d.Close()

	// json-text (also proves instructions/roles and history evidence).
	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("openresponses -> %s json round trip: %v", be, err)
	}
	if res.Status != "completed" {
		t.Fatalf("openresponses -> %s status = %q, want completed", be, res.Status)
	}
	if !strings.Contains(res.Text, wantText) {
		t.Fatalf("openresponses -> %s text = %q, want %q", be, res.Text, wantText)
	}
	if be == BackendACP {
		if d.RequestCount(be) < 1 {
			t.Fatalf("openresponses -> acp json request count = %d, want >= 1 (handshake+prompt)", d.RequestCount(be))
		}
	} else if got := d.RequestCount(be); got != 1 {
		t.Fatalf("openresponses -> %s json request count = %d, want exactly 1 (single-terminal commitment)", be, got)
	}

	// continuation: a second store:true create with previous_response_id
	// re-executes through the same projector. The delta proves the materialized
	// history caused real upstream work (exactly one create for non-ACP cells).
	// The ACP v1 prompt-turn subset cannot replay a materialized trajectory that
	// carries the prior ACP reasoning output, so the ACP continuation call is
	// honestly rejected before any network request (zero additional upstream
	// requests).
	firstID, err := rowStoreCreate(t, d, "")
	if err != nil {
		t.Fatalf("openresponses -> %s continuation first create: %v", be, err)
	}
	afterFirst := d.RequestCount(be)
	secondID, err := rowStoreCreate(t, d, firstID)
	if be == BackendACP {
		if err == nil {
			t.Fatalf("openresponses -> acp continuation unexpectedly round-tripped; the materialized trajectory with prior ACP reasoning output must be rejected before network")
		}
		if d.RequestCount(be) != afterFirst {
			t.Fatalf("openresponses -> acp continuation rejection caused %d additional upstream requests, want 0 (rejected before network)", d.RequestCount(be)-afterFirst)
		}
	} else {
		if err != nil {
			t.Fatalf("openresponses -> %s continuation second create: %v", be, err)
		}
		if secondID == "" || secondID == firstID {
			t.Fatalf("openresponses -> %s continuation second id = %q, want a new proxy id distinct from %q", be, secondID, firstID)
		}
		delta := d.RequestCount(be) - afterFirst
		if delta != 1 {
			t.Fatalf("openresponses -> %s continuation re-execution caused %d upstream requests, want exactly 1", be, delta)
		}
	}

	// tools: admitted and projected for non-ACP cells; rejected with zero
	// requests for the ACP v1 subset.
	before := d.RequestCount(be)
	status, err := d.RawFrontendPost(context.Background(), "/openresponses/v1/responses", `{"model":"gpt-4o-mini","store":false,"input":"hi","tools":[{"type":"function","name":"get_weather","description":"get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}]}`)
	if err != nil {
		t.Fatalf("tools post openresponses -> %s: %v", be, err)
	}
	if be == BackendACP {
		if status == http.StatusOK {
			t.Fatalf("ACP tools unexpectedly round-tripped; v1 prompt-turn subset rejects tools")
		}
		if d.RequestCount(be) != before {
			t.Fatalf("ACP tools rejection caused %d upstream requests, want 0", d.RequestCount(be)-before)
		}
	} else {
		if status != http.StatusOK {
			t.Fatalf("tools status = %d, want 200 (openresponses -> %s)", status, be)
		}
		if d.RequestCount(be) != before+1 {
			t.Fatalf("tools request count delta = %d, want 1 (openresponses -> %s)", d.RequestCount(be)-before, be)
		}
		if !rowOriginHasSubstring(d, be, "get_weather") {
			t.Fatalf("tools upstream request did not carry the projected tool (openresponses -> %s)", be)
		}
	}

	// multimodal: image input is admitted and projected to the upstream wire
	// (ACP via resource prompt blocks).
	before = d.RequestCount(be)
	status, err = d.RawFrontendPost(context.Background(), "/openresponses/v1/responses", `{"model":"gpt-4o-mini","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`)
	if err != nil {
		t.Fatalf("multimodal post openresponses -> %s: %v", be, err)
	}
	if status != http.StatusOK {
		t.Fatalf("multimodal status = %d, want 200 (openresponses -> %s)", status, be)
	}
	if d.RequestCount(be) == before {
		t.Fatalf("multimodal caused no upstream request (openresponses -> %s)", be)
	}
	if !rowOriginHasSubstring(d, be, "AAAA") {
		t.Fatalf("multimodal upstream request did not carry the projected image (openresponses -> %s)", be)
	}

	// Negatives (replay/phase/itemref/compaction/extension): reject before any
	// network request. The OpenResponses backend cell is the documented positive
	// exception for compaction (the generic backend declares the capability), so
	// its executable scenario uses the positive "compaction" suffix; every other
	// row cell keeps the "compaction-reject" suffix.
	negatives := map[string]string{
		"phase-reject":     `{"model":"gpt-4o-mini","store":false,"input":[{"type":"message","role":"assistant","phase":"in_progress","content":[{"type":"output_text","text":"x"}]}]}`,
		"replay-reject":    `{"model":"gpt-4o-mini","store":false,"input":[{"type":"reasoning","reasoning":"think"}]}`,
		"extension-reject": `{"model":"gpt-4o-mini","store":false,"input":[{"type":"acme:telemetry","namespace":"acme","data":{"x":1}}]}`,
		"itemref-reject":   `{"model":"gpt-4o-mini","store":false,"input":[{"type":"item_reference","id":"item_1"}]}`,
	}
	compactionBody := `{"model":"gpt-4o-mini","store":false,"input":[{"type":"compaction","prior_response_id":"resp_1"}]}`
	if be == BackendOpenResponses {
		negatives["compaction"] = compactionBody
	} else {
		negatives["compaction-reject"] = compactionBody
	}
	for suffix, body := range negatives {
		before := d.RequestCount(be)
		status, err := d.RawFrontendPost(context.Background(), "/openresponses/v1/responses", body)
		if err != nil {
			t.Fatalf("negative %s post openresponses -> %s: %v", suffix, be, err)
		}
		if suffix == "compaction" {
			if status != http.StatusOK {
				t.Fatalf("openresponses compaction status = %d, want 200 (compaction capability declared)", status)
			}
			if d.RequestCount(be) != before+1 {
				t.Fatalf("openresponses compaction request count delta = %d, want 1", d.RequestCount(be)-before)
			}
			continue
		}
		if status == http.StatusOK {
			t.Fatalf("negative %s unexpectedly round-tripped (openresponses -> %s)", suffix, be)
		}
		if d.RequestCount(be) != before {
			t.Fatalf("negative %s caused %d upstream requests, want 0 (openresponses -> %s)", suffix, d.RequestCount(be)-before, be)
		}
	}

	// sse-text: incremental streaming reaches the client over the same canonical
	// stream (a separate SSE deployment).
	dsse := rowDeploy(t, be, TransportSSE)
	if dsse == nil {
		t.Fatalf("deploy(openresponses, %s, sse) failed", be)
	}
	defer dsse.Close()
	sres, err := dsse.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("openresponses -> %s sse round trip: %v", be, err)
	}
	if sres.Status != "completed" {
		t.Fatalf("openresponses -> %s sse status = %q, want completed", be, sres.Status)
	}
	if !strings.Contains(sres.Text, wantText) {
		t.Fatalf("openresponses -> %s sse text = %q, want %q", be, sres.Text, wantText)
	}

	// usage-errors: an upstream 500 surfaces as a stable client-visible error
	// (never a silent success) with an upstream attempt.
	derr := rowCellDeployError(t, be)
	if derr == nil {
		t.Fatalf("deploy(openresponses, %s, error) failed", be)
	}
	defer derr.Close()
	estatus, err := derr.RawFrontendPost(context.Background(), "/openresponses/v1/responses", `{"model":"gpt-4o-mini","input":"ping","store":false}`)
	if err != nil {
		t.Fatalf("usage-errors post openresponses -> %s: %v", be, err)
	}
	if estatus >= 200 && estatus < 300 {
		t.Fatalf("upstream 500 unexpectedly round-tripped as HTTP %d (openresponses -> %s)", estatus, be)
	}
	if derr.RequestCount(be) < 1 {
		t.Fatalf("upstream error cell openresponses -> %s caused no upstream attempt, want >= 1", be)
	}

	// failover: pre-output failure fails over to the succeeding OpenResponses
	// candidate; both origin counts are asserted.
	dfail := rowCellDeployFailover(t, be)
	if dfail == nil {
		t.Fatalf("deploy(openresponses, %s, failover) failed", be)
	}
	defer dfail.Close()
	fres, err := dfail.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("failover round trip openresponses -> %s: %v", be, err)
	}
	if !strings.Contains(fres.Text, HarnessFakeText) {
		t.Fatalf("failover text = %q, want candidate text %q (openresponses -> %s)", fres.Text, HarnessFakeText, be)
	}
	if dfail.RequestCount(be) < 1 {
		t.Fatalf("failover primary origin received no requests (openresponses -> %s)", be)
	}
	cand := dfail.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("failover deployment has no candidate origin (openresponses -> %s)", be)
	}
	if cand.Count() < 1 {
		t.Fatalf("failover candidate origin received %d requests, want >= 1 (openresponses -> %s)", cand.Count(), be)
	}

	// no-retry-after-visible-output: an origin that emits the first content
	// event then dies must not trigger retry or failover (candidate stays
	// untouched).
	dnr := rowCellDeployNoRetry(t, be)
	if dnr == nil {
		t.Fatalf("deploy(openresponses, %s, no-retry) failed", be)
	}
	defer dnr.Close()
	if _, err := rawRowStreamPost(t, dnr, 20*time.Second); err != nil {
		// A post-output stream failure is expected; the assertion is the count.
		t.Logf("no-retry stream terminated with error (expected): %v", err)
	}
	if dnr.RequestCount(be) < 1 {
		t.Fatalf("no-retry primary origin received %d requests, want >= 1 (openresponses -> %s)", dnr.RequestCount(be), be)
	}
	cand = dnr.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("no-retry deployment has no candidate origin (openresponses -> %s)", be)
	}
	if cand.Count() != 0 {
		t.Fatalf("no-retry candidate origin received %d requests, want 0 (no failover after visible output, openresponses -> %s)", cand.Count(), be)
	}

	// cancellation/backpressure: a canceled client context stops upstream work
	// with no second attempt on the candidate.
	dc := rowCellDeployCancel(t, be)
	if dc == nil {
		t.Fatalf("deploy(openresponses, %s, cancellation) failed", be)
	}
	defer dc.Close()
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, dc.Server.URL+"/openresponses/v1/responses", strings.NewReader(rowStreamingCreateBody()))
	if err != nil {
		t.Fatalf("cancellation request build openresponses -> %s: %v", be, err)
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
	for dc.RequestCount(be) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ccancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("cancellation request did not terminate (openresponses -> %s)", be)
	}
	if dc.RequestCount(be) < 1 {
		t.Fatalf("cancellation primary origin received no request (openresponses -> %s)", be)
	}
	cand = dc.CandidateOrigin(0)
	if cand == nil {
		t.Fatalf("cancellation deployment has no candidate origin (openresponses -> %s)", be)
	}
	if cand.Count() != 0 {
		t.Fatalf("cancellation candidate origin received %d requests, want 0 (no retry after cancellation, openresponses -> %s)", cand.Count(), be)
	}
}

// rawRowStreamPost POSTs a streaming create body to the OpenResponses frontend
// and drains the response. It returns the HTTP status and any read error; the
// caller only asserts origin counts, never the wire bytes.
func rawRowStreamPost(t *testing.T, d *Deployment, timeout time.Duration) (int, error) {
	t.Helper()
	if d == nil || d.Server == nil {
		t.Fatalf("rawRowStreamPost: deployment is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Server.URL+"/openresponses/v1/responses", strings.NewReader(rowStreamingCreateBody()))
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

// rowCellDeployError deploys a row cell whose primary origin returns an upstream
// 500, for the usage/error-mapping proof.
func rowCellDeployError(tb testing.TB, backend string) *Deployment {
	tb.Helper()
	if backend == BackendOpenRouter || backend == BackendNVIDIA {
		return deployProviderModeChain(tb, FrontendOpenResponses, backend, TransportJSON, OriginFailServerError, nil, nil)
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:   FrontendOpenResponses,
		Backend:    backend,
		Transport:  TransportJSON,
		OriginFail: OriginFailServerError,
	})
}

// rowCellDeployFailover deploys a row cell with a failing primary and a
// succeeding OpenResponses candidate.
func rowCellDeployFailover(tb testing.TB, backend string) *Deployment {
	tb.Helper()
	cand := BackendOpenResponses
	if backend == BackendOpenRouter || backend == BackendNVIDIA {
		return deployProviderModeChain(tb, FrontendOpenResponses, backend, TransportJSON, OriginFailServerError, nil, []Candidate{{Backend: cand, OriginFail: OriginFailNone}})
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:   FrontendOpenResponses,
		Backend:    backend,
		Transport:  TransportJSON,
		OriginFail: OriginFailServerError,
		Candidates: []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}

// rowCellDeployNoRetry deploys a row cell whose primary origin emits the first
// content event then dies mid-stream, with a succeeding candidate that must
// never be reached.
func rowCellDeployNoRetry(tb testing.TB, backend string) *Deployment {
	tb.Helper()
	cand := BackendOpenResponses
	if backend == BackendOpenRouter || backend == BackendNVIDIA {
		return deployProviderModeChain(tb, FrontendOpenResponses, backend, TransportSSE, OriginFailNone, newMidStreamDeathHandler(backend), []Candidate{{Backend: cand, OriginFail: OriginFailNone}})
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:      FrontendOpenResponses,
		Backend:       backend,
		Transport:     TransportSSE,
		OriginHandler: newMidStreamDeathHandler(backend),
		Candidates:    []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}

// rowCellDeployCancel deploys a row cell whose primary origin blocks until the
// client cancels, with a succeeding candidate that must never be reached.
func rowCellDeployCancel(tb testing.TB, backend string) *Deployment {
	tb.Helper()
	cand := BackendOpenResponses
	if backend == BackendOpenRouter || backend == BackendNVIDIA {
		return deployProviderModeChain(tb, FrontendOpenResponses, backend, TransportSSE, OriginFailNone, blockingOriginHandler(), []Candidate{{Backend: cand, OriginFail: OriginFailNone}})
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:      FrontendOpenResponses,
		Backend:       backend,
		Transport:     TransportSSE,
		OriginHandler: blockingOriginHandler(),
		Candidates:    []Candidate{{Backend: cand, OriginFail: OriginFailNone}},
	})
}
