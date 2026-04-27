//go:build integration

package conformance

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

// Credential-pool conformance: repeat hosted-provider matrix checks with two ordered API keys
// on each in-scope backend (Bedrock/ACP use the same single-credential harness as before).

func TestConformance_CredentialPool_TextOnly_roundTrip(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.TextViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			beSrv := NewSuccessRefBackend(t, cell.Backend, nil)
			exec := NewTestExecutorDualCredential(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			got := nonStreamAssistantText(t, cell.Frontend, feSrv.URL, feSrv.Client())
			if cell.Backend == "acp" {
				if !strings.Contains(got, "ok") {
					t.Fatalf("expected ACP stock emulator text containing ok, got %q", got)
				}
				return
			}
			if !strings.Contains(got, parityText) {
				t.Fatalf("expected parity text in response, got %q", got)
			}
		})
	}
}

func TestConformance_CredentialPool_TextOnly_streamAndNonStreamParity(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.TextViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			beSrv := NewSuccessRefBackend(t, cell.Backend, nil)
			exec := NewTestExecutorDualCredential(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			ns := nonStreamAssistantText(t, cell.Frontend, feSrv.URL, feSrv.Client())
			st := streamAssistantText(t, cell.Frontend, feSrv.URL, feSrv.Client())
			if cell.Backend == "acp" {
				if !strings.Contains(ns, "ok") || !strings.Contains(st, "ok") {
					t.Fatalf("expected ok in both paths non-stream=%q stream=%q", ns, st)
				}
				return
			}
			if !strings.Contains(ns, parityText) || !strings.Contains(st, parityText) {
				t.Fatalf("expected parity in both paths non-stream=%q stream=%q", ns, st)
			}
		})
	}
}

func TestConformance_CredentialPool_TextOnly_upstreamErrorShape(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.TextViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			up := NewUpstream400Server(t, cell.Backend)
			exec := NewTestExecutorDualCredential(t, cell.Backend, up.URL, up.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			err := nonStreamExpectError(t, cell.Frontend, feSrv.URL, feSrv.Client())
			if err == nil {
				t.Fatal("expected upstream error")
			}
			switch cell.Frontend {
			case "openai-responses", "openai-legacy":
				var apiErr *openai.Error
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected *openai.Error, got %T: %v", err, err)
				}
				if apiErr.StatusCode != http.StatusBadRequest && apiErr.StatusCode != http.StatusInternalServerError {
					t.Fatalf("status %d", apiErr.StatusCode)
				}
				if !clientVisibleErrorIndicatesFailure(apiErr.Error()) {
					t.Fatalf("expected sanitized or diagnostic client error text: %v", apiErr)
				}
			case "anthropic":
				var apiErr *anthropic.Error
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected *anthropic.Error, got %T: %v", err, err)
				}
				if apiErr.StatusCode != http.StatusBadRequest && apiErr.StatusCode != http.StatusInternalServerError {
					t.Fatalf("status %d", apiErr.StatusCode)
				}
				if !clientVisibleErrorIndicatesFailure(apiErr.Error()) {
					t.Fatalf("expected sanitized or diagnostic client error text: %v", apiErr)
				}
			case "gemini":
				lower := strings.ToLower(err.Error())
				if !strings.Contains(lower, "400") && !strings.Contains(lower, "invalid") && !strings.Contains(lower, "internal error") {
					t.Fatalf("expected client-visible error mentioning status, invalid, or generic internal failure, got %v", err)
				}
			default:
				t.Fatalf("unexpected frontend %q", cell.Frontend)
			}
		})
	}
}

func TestConformance_CredentialPool_Tools_roundTripAndUsage(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.ToolsViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			var captured string
			beSrv := NewToolRefBackend(t, cell.Backend, func(b []byte) { captured = string(b) })
			exec := NewTestExecutorDualCredential(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			raw := toolStreamRawJoined(t, cell.Frontend, feSrv.URL, feSrv.Client(), cell.Backend)
			name := toolNameForBackend(cell.Backend)
			if !strings.Contains(strings.ToLower(captured), strings.ToLower(name)) {
				t.Fatalf("upstream request should include tool name %q, body prefix: %s", name, trim(captured, 800))
			}
			if !strings.Contains(strings.ToLower(raw), strings.ToLower(name)) {
				t.Fatalf("client-visible stream should include tool name %q, joined: %s", name, trim(raw, 1200))
			}
			lower := strings.ToLower(raw)
			capLower := strings.ToLower(captured)
			if !stringsContainsAny(lower, []string{"input_tokens", "prompt_tokens", "prompttokencount", "total_tokens", "totaltokencount", "usagemetadata"}) &&
				!stringsContainsAny(capLower, []string{"input_tokens", "usage", "prompt_tokens", "total_tokens", "usagemetadata"}) {
				t.Fatalf("expected usage markers in client stream or captured upstream body, raw=%s cap=%s", trim(raw, 600), trim(captured, 600))
			}
		})
	}
}

func TestConformance_CredentialPool_Multimodal_imageInUpstream(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.MultimodalViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			var captured string
			beSrv := NewSuccessRefBackend(t, cell.Backend, func(b []byte) { captured = string(b) })
			exec := NewTestExecutorDualCredential(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			png := refclienttest.ReadRefclientFixture(t, "tiny.png")
			multimodalImageOnly(t, cell.Frontend, feSrv.URL, feSrv.Client(), png)
			assertUpstreamImageMarker(t, cell.Backend, captured)
		})
	}
}

func TestConformance_CredentialPool_Multimodal_pdfInUpstream(t *testing.T) {
	t.Parallel()
	for _, cell := range AllCells() {
		if !cell.Meta.MultimodalViable {
			continue
		}
		t.Run(cell.Frontend+"__"+cell.Backend, func(t *testing.T) {
			t.Parallel()
			var captured string
			beSrv := NewSuccessRefBackend(t, cell.Backend, func(b []byte) { captured = string(b) })
			exec := NewTestExecutorDualCredential(t, cell.Backend, beSrv.URL, beSrv.Client())
			route := RouteSelector(cell.Backend, DefaultModel(cell.Backend))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
			multimodalPDFOnly(t, cell.Frontend, feSrv.URL, feSrv.Client(), pdf)
			assertUpstreamPDFMarker(t, cell.Backend, captured)
		})
	}
}
