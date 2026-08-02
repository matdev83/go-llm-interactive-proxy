//go:build integration

package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Task 8.3 OpenResponses frontend compatibility row.
//
// Every named scenario below is linked from the row evidence registry
// (openresponses_frontend_row.go) and asserts behavior against the observing
// reference-provider origin: positive cells round-trip exactly once, and every
// rejected feature shows zero remote requests.

// rowDeploy deploys the OpenResponses frontend → backend cell through the
// generic harness (constructible cells) or the configured OpenAI-compatible
// provider mode (OpenRouter/NVIDIA connector columns).
func rowDeploy(tb testing.TB, backend string, transport ClientTransport) *Deployment {
	tb.Helper()
	if backend == BackendOpenRouter || backend == BackendNVIDIA {
		return DeployConfiguredProviderMode(tb, backend, transport)
	}
	return Deploy(tb, DeploymentSpec{
		Frontend:  FrontendOpenResponses,
		Backend:   backend,
		Transport: transport,
	})
}

// rowExpectedText returns the deterministic assistant text the observing origin
// for a row cell emits for a text prompt.
func rowExpectedText(backend string) string {
	switch backend {
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

// rowRawCreate posts an arbitrary body to the OpenResponses create endpoint with
// the harness session header and returns status and body.
func rowRawCreate(t *testing.T, d *Deployment, rawBody string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.Server.URL+"/openresponses/v1/responses", strings.NewReader(rawBody))
	if err != nil {
		t.Fatalf("build raw create: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LIP-Session-Id", "sess-openresponses-row")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("raw create: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// rowOriginHasSubstring reports whether any captured origin request body for
// backend contains sub.
func rowOriginHasSubstring(d *Deployment, backend, sub string) bool {
	for _, obs := range d.OriginFor(backend).Capture() {
		if strings.Contains(string(obs.Body), sub) {
			return true
		}
	}
	return false
}

// TestFrontendRow_OpenResponsesToJSONText runs the positive JSON text scenario
// for all nine row cells.
func TestFrontendRow_OpenResponsesToJSONText(t *testing.T) {
	t.Parallel()
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := rowDeploy(t, backend, TransportJSON)
			if d == nil {
				t.Fatalf("deploy(openresponses, %s) failed", backend)
			}
			defer d.Close()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("openresponses -> %s json round trip: %v", backend, err)
			}
			if res.Status != "completed" {
				t.Fatalf("openresponses -> %s status = %q, want completed", backend, res.Status)
			}
			if want := rowExpectedText(backend); !strings.Contains(res.Text, want) {
				t.Fatalf("openresponses -> %s text = %q, want %q", backend, res.Text, want)
			}
			if backend == BackendACP {
				// ACP performs a JSON-RPC handshake before the prompt turn.
				if got := d.RequestCount(backend); got < 1 {
					t.Fatalf("openresponses -> acp request count = %d, want >= 1 (handshake+prompt)", got)
				}
				return
			}
			if got := d.RequestCount(backend); got != 1 {
				t.Fatalf("openresponses -> %s request count = %d, want exactly 1", backend, got)
			}
		})
	}
}

// TestFrontendRow_OpenResponsesToStreaming runs the positive SSE text scenario
// for all nine row cells.
func TestFrontendRow_OpenResponsesToStreaming(t *testing.T) {
	t.Parallel()
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := rowDeploy(t, backend, TransportSSE)
			if d == nil {
				t.Fatalf("deploy(openresponses, %s, sse) failed", backend)
			}
			defer d.Close()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("openresponses -> %s sse round trip: %v", backend, err)
			}
			if res.Status != "completed" {
				t.Fatalf("openresponses -> %s sse status = %q, want completed", backend, res.Status)
			}
			if want := rowExpectedText(backend); !strings.Contains(res.Text, want) {
				t.Fatalf("openresponses -> %s sse text = %q, want %q", backend, res.Text, want)
			}
			terminals := 0
			for _, ev := range res.Events {
				if ev == "response.completed" {
					terminals++
				}
			}
			if terminals != 1 {
				t.Fatalf("openresponses -> %s sse terminals = %d, want exactly 1", backend, terminals)
			}
		})
	}
}

// TestFrontendRow_OpenResponsesToTools runs the tools scenario: the tool request
// is admitted and projected to the upstream wire (exactly one create request for
// non-ACP cells), or rejected before any network request for the ACP v1 subset.
func TestFrontendRow_OpenResponsesToTools(t *testing.T) {
	t.Parallel()
	toolsBody := `{"model":"gpt-4o-mini","store":false,"input":"hi","tools":[{"type":"function","name":"get_weather","description":"get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}]}`
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := rowDeploy(t, backend, TransportJSON)
			if d == nil {
				t.Fatalf("deploy failed")
			}
			defer d.Close()
			status, _ := rowRawCreate(t, d, toolsBody)
			if backend == BackendACP {
				if status == http.StatusOK {
					t.Fatal("ACP tools unexpectedly round-tripped; v1 prompt-turn subset rejects tools")
				}
				if got := d.RequestCount(backend); got != 0 {
					t.Fatalf("ACP tools rejection caused %d upstream requests, want 0", got)
				}
				return
			}
			if status != http.StatusOK {
				t.Fatalf("openresponses -> %s tools status = %d, want 200", backend, status)
			}
			if got := d.RequestCount(backend); got != 1 {
				t.Fatalf("openresponses -> %s tools request count = %d, want exactly 1", backend, got)
			}
			if !rowOriginHasSubstring(d, backend, "get_weather") {
				t.Fatalf("openresponses -> %s upstream request did not carry the projected tool", backend)
			}
		})
	}
}

// TestFrontendRow_OpenResponsesToMultimodal runs the multimodal image scenario:
// image input is admitted and projected to the upstream wire for every cell that
// can represent it (including ACP as URI resource prompt blocks).
func TestFrontendRow_OpenResponsesToMultimodal(t *testing.T) {
	t.Parallel()
	imageBody := `{"model":"gpt-4o-mini","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := rowDeploy(t, backend, TransportJSON)
			if d == nil {
				t.Fatalf("deploy failed")
			}
			defer d.Close()
			status, _ := rowRawCreate(t, d, imageBody)
			if status != http.StatusOK {
				t.Fatalf("openresponses -> %s multimodal status = %d, want 200", backend, status)
			}
			if got := d.RequestCount(backend); got < 1 {
				t.Fatalf("openresponses -> %s multimodal request count = %d, want >= 1", backend, got)
			}
			if backend == BackendACP {
				if !rowOriginHasSubstring(d, backend, "resource") || !rowOriginHasSubstring(d, backend, "AAAA") {
					t.Fatal("openresponses -> acp multimodal image was not projected to an ACP resource prompt block")
				}
				return
			}
			if got := d.RequestCount(backend); got != 1 {
				t.Fatalf("openresponses -> %s multimodal request count = %d, want exactly 1", backend, got)
			}
			if !rowOriginHasSubstring(d, backend, "AAAA") {
				t.Fatalf("openresponses -> %s upstream request did not carry the projected image", backend)
			}
		})
	}
}

// TestFrontendRow_OpenResponsesToACPSubset pins the ACP text/resource positive
// subset and the fail-closed negatives with zero upstream requests.
func TestFrontendRow_OpenResponsesToACPSubset(t *testing.T) {
	t.Parallel()
	positives := []struct {
		name string
		body string
		want string
	}{
		{name: "text", body: `{"model":"acp/agent","input":"ping","store":false}`, want: "ok"},
		{name: "resource", body: `{"model":"acp/agent","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`, want: "ok"},
	}
	for _, tc := range positives {
		tc := tc
		t.Run("positive_"+tc.name, func(t *testing.T) {
			t.Parallel()
			d := rowDeploy(t, BackendACP, TransportJSON)
			defer d.Close()
			status, body := rowRawCreate(t, d, tc.body)
			if status != http.StatusOK {
				t.Fatalf("ACP positive %s status = %d, body=%s", tc.name, status, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Fatalf("ACP positive %s body %q missing %q", tc.name, body, tc.want)
			}
			if d.RequestCount(BackendACP) < 1 {
				t.Fatalf("ACP positive %s caused no upstream work", tc.name)
			}
		})
	}

	negatives := []struct {
		name string
		body string
	}{
		{name: "tools", body: `{"model":"acp/agent","store":false,"input":"hi","tools":[{"type":"function","name":"f","description":"d","parameters":{"type":"object"}}]}`},
		{name: "video", body: `{"model":"acp/agent","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_video","video_url":"https://x/v.mp4"}]}]}`},
		{name: "phase", body: `{"model":"acp/agent","store":false,"input":[{"type":"message","role":"assistant","phase":"in_progress","content":[{"type":"output_text","text":"x"}]}]}`},
		{name: "replay", body: `{"model":"acp/agent","store":false,"input":[{"type":"reasoning","reasoning":"think"}]}`},
		{name: "compaction", body: `{"model":"acp/agent","store":false,"input":[{"type":"compaction","prior_response_id":"resp_1"}]}`},
		{name: "extension", body: `{"model":"acp/agent","store":false,"input":[{"type":"acme:telemetry","namespace":"acme","data":{"x":1}}]}`},
	}
	for _, tc := range negatives {
		tc := tc
		t.Run("negative_"+tc.name, func(t *testing.T) {
			t.Parallel()
			d := rowDeploy(t, BackendACP, TransportJSON)
			defer d.Close()
			status, _ := rowRawCreate(t, d, tc.body)
			if status == http.StatusOK {
				t.Fatalf("ACP negative %s unexpectedly round-tripped", tc.name)
			}
			if got := d.RequestCount(BackendACP); got != 0 {
				t.Fatalf("ACP negative %s caused %d upstream requests, want 0", tc.name, got)
			}
		})
	}
}

// TestFrontendRow_OpenResponsesToNoNetwork runs the fail-closed negative
// scenarios (phase, reasoning replay, compaction, extensions, item references)
// for every row cell and asserts zero remote requests. The OpenResponses cell
// is the documented exception for compaction (the generic backend declares the
// compaction capability, so compaction input round-trips): its executable
// scenario uses the positive "compaction" suffix while the other row cells keep
// "compaction-reject".
func TestFrontendRow_OpenResponsesToNoNetwork(t *testing.T) {
	t.Parallel()
	compactionBody := `{"model":"gpt-4o-mini","store":false,"input":[{"type":"compaction","prior_response_id":"resp_1"}]}`
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		backend := backend
		neg := map[string]string{
			"phase-reject":     `{"model":"gpt-4o-mini","store":false,"input":[{"type":"message","role":"assistant","phase":"in_progress","content":[{"type":"output_text","text":"x"}]}]}`,
			"replay-reject":    `{"model":"gpt-4o-mini","store":false,"input":[{"type":"reasoning","reasoning":"think"}]}`,
			"extension-reject": `{"model":"gpt-4o-mini","store":false,"input":[{"type":"acme:telemetry","namespace":"acme","data":{"x":1}}]}`,
			"itemref-reject":   `{"model":"gpt-4o-mini","store":false,"input":[{"type":"item_reference","id":"item_1"}]}`,
		}
		if backend == BackendOpenResponses {
			neg["compaction"] = compactionBody
		} else {
			neg["compaction-reject"] = compactionBody
		}
		for suffix, body := range neg {
			suffix, body := suffix, body
			t.Run(backend+"__"+suffix, func(t *testing.T) {
				t.Parallel()
				d := rowDeploy(t, backend, TransportJSON)
				if d == nil {
					t.Fatalf("deploy failed")
				}
				defer d.Close()
				status, _ := rowRawCreate(t, d, body)
				if suffix == "compaction" {
					if status != http.StatusOK {
						t.Fatalf("openresponses compaction status = %d, want 200 (compaction capability declared)", status)
					}
					if got := d.RequestCount(backend); got != 1 {
						t.Fatalf("openresponses compaction request count = %d, want 1", got)
					}
					return
				}
				if status == http.StatusOK {
					t.Fatalf("openresponses -> %s %s unexpectedly round-tripped", backend, suffix)
				}
				if got := d.RequestCount(backend); got != 0 {
					t.Fatalf("openresponses -> %s %s caused %d upstream requests, want 0", backend, suffix, got)
				}
			})
		}
	}
}

// TestFrontendRow_OpenResponsesToUsageErrorsCommitment proves usage surfacing,
// stable error mapping, and single-terminal commitment per row cell.
func TestFrontendRow_OpenResponsesToUsageErrorsCommitment(t *testing.T) {
	t.Parallel()
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := rowDeploy(t, backend, TransportSSE)
			if d == nil {
				t.Fatalf("deploy failed")
			}
			defer d.Close()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("openresponses -> %s commitment round trip: %v", backend, err)
			}
			if res.Status != "completed" {
				t.Fatalf("openresponses -> %s status = %q, want completed", backend, res.Status)
			}
			terminals := 0
			for _, ev := range res.Events {
				if ev == "response.completed" {
					terminals++
				}
			}
			if terminals != 1 {
				t.Fatalf("openresponses -> %s saw %d terminal events, want exactly 1 (single-terminal commitment)", backend, terminals)
			}
		})
	}

	// Usage surfacing: the OpenResponses contract-fake origin reports usage, so
	// the response resource must carry it.
	t.Run("usage", func(t *testing.T) {
		t.Parallel()
		d := rowDeploy(t, BackendOpenResponses, TransportJSON)
		defer d.Close()
		status, body := rowRawCreate(t, d, `{"model":"gpt-4o-mini","input":"ping","store":false}`)
		if status != http.StatusOK {
			t.Fatalf("usage create status = %d, body=%s", status, body)
		}
		var env struct {
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatalf("decode usage resource: %v", err)
		}
		if env.Usage == nil {
			t.Fatal("response resource has no usage field")
		}
		if env.Usage.InputTokens != 1 || env.Usage.OutputTokens != 1 || env.Usage.TotalTokens != 2 {
			t.Fatalf("usage = %+v, want input=1 output=1 total=2", env.Usage)
		}
	})

	// Error mapping: an upstream 500 on the primary origin surfaces as a stable
	// client-visible error (never a silent success), for representative cells.
	for _, backend := range []string{BackendOpenAIResponses, BackendAnthropic, BackendOpenResponses} {
		backend := backend
		t.Run("error_"+backend, func(t *testing.T) {
			t.Parallel()
			d := Deploy(t, DeploymentSpec{
				Frontend:   FrontendOpenResponses,
				Backend:    backend,
				Transport:  TransportJSON,
				OriginFail: OriginFailServerError,
			})
			if d == nil {
				t.Fatalf("deploy failed")
			}
			defer d.Close()
			if _, err := d.Client.RoundTrip(context.Background(), "ping"); err == nil {
				t.Fatal("upstream 500 unexpectedly round-tripped as success")
			}
			if got := d.RequestCount(backend); got < 1 {
				t.Fatalf("upstream error cell %s caused no upstream attempt, want >= 1", backend)
			}
		})
	}
}

// TestFrontendRow_OpenResponsesToOpenRouterAndNVIDIA_RouteProves the configured
// provider-mode route for the OpenRouter and NVIDIA connector columns: a real
// request reaches the configured OpenAI-compatible provider origin with the
// OpenAI Responses wire and round-trips exactly once, while the connectors stay
// optional (asserted separately by the evidence validator).
func TestFrontendRow_OpenResponsesToOpenRouterAndNVIDIA_RouteProves(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{BackendOpenRouter, BackendNVIDIA} {
		backend := backend
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := DeployConfiguredProviderMode(t, backend, TransportJSON)
			if d == nil {
				t.Fatalf("provider-mode deploy failed for %s", backend)
			}
			defer d.Close()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("%s configured provider-mode route: %v", backend, err)
			}
			if !strings.Contains(res.Text, "provider-mode-ok") {
				t.Fatalf("%s route text = %q, want provider-mode-ok", backend, res.Text)
			}
			if got := d.RequestCount(backend); got != 1 {
				t.Fatalf("%s route request count = %d, want exactly 1 (the configured provider origin)", backend, got)
			}
			captured := d.OriginFor(backend).Capture()
			if len(captured) == 0 {
				t.Fatal("no captured request at the configured provider origin")
			}
			last := captured[len(captured)-1]
			if !strings.Contains(string(last.Body), `"input"`) {
				t.Fatalf("%s route request is not the OpenAI-compatible Responses create wire: %s", backend, string(last.Body))
			}
			for _, forbidden := range []string{`"previous_response_id"`, `"store"`, `"stream"`, `"background"`} {
				if strings.Contains(string(last.Body), forbidden) {
					t.Fatalf("%s route request forwarded forbidden proxy field %q: %s", backend, forbidden, string(last.Body))
				}
			}
		})
	}
}
