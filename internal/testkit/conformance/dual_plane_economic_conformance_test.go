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
)

// Phase 7.1 dual-plane economic conformance rows: every bundled frontend ×
// stream/nonstream/error/cancel/encoding mode is an explicit matrix cell.
// Stream/nonstream/error exercise wire adapters; cancel and encoding failure
// customer economics are proven in runtime phase71 tests against the same mode
// vocabulary (requirements 1.2, 1.3, 13.2, 13.9).

func TestDualPlaneEconomic_MatrixCells(t *testing.T) {
	t.Parallel()
	cells := DualPlaneEconomicCells()
	want := len(BundledFrontendIDs()) * len(DualPlaneEconomicModes())
	if len(cells) != want {
		t.Fatalf("cells=%d want %d", len(cells), want)
	}
	for _, cell := range cells {
		t.Run(cell.Frontend+"__"+string(cell.Mode), func(t *testing.T) {
			t.Parallel()
			if !cell.Mode.IsKnown() {
				t.Fatalf("unknown mode %q", cell.Mode)
			}
			found := false
			for _, fe := range BundledFrontendIDs() {
				if fe == cell.Frontend {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("frontend %q not in BundledFrontendIDs", cell.Frontend)
			}
		})
	}
}

func TestDualPlaneEconomic_StreamAndNonStreamParity(t *testing.T) {
	t.Parallel()
	for _, cell := range DualPlaneEconomicCells() {
		if cell.Mode != DualPlaneEconomicModeStream && cell.Mode != DualPlaneEconomicModeNonStream {
			continue
		}
		t.Run(cell.Frontend+"__"+string(cell.Mode), func(t *testing.T) {
			t.Parallel()
			beSrv := NewSuccessRefBackend(t, "openai-responses", nil)
			exec := NewTestExecutor(t, "openai-responses", beSrv.URL, beSrv.Client())
			route := RouteSelector("openai-responses", DefaultModel("openai-responses"))
			mux := http.NewServeMux()
			if err := MountFrontend(mux, cell.Frontend, exec, route); err != nil {
				t.Fatal(err)
			}
			feSrv := httptest.NewServer(mux)
			t.Cleanup(feSrv.Close)

			ns := nonStreamAssistantText(t, cell.Frontend, feSrv.URL, feSrv.Client())
			st := streamAssistantText(t, cell.Frontend, feSrv.URL, feSrv.Client())
			if !strings.Contains(ns, parityText) || !strings.Contains(st, parityText) {
				t.Fatalf("expected parity in both paths non-stream=%q stream=%q", ns, st)
			}
		})
	}
}

func TestDualPlaneEconomic_ProtocolErrorShape(t *testing.T) {
	t.Parallel()
	for _, cell := range DualPlaneEconomicCells() {
		if cell.Mode != DualPlaneEconomicModeProtocolError {
			continue
		}
		t.Run(cell.Frontend+"__"+string(cell.Mode), func(t *testing.T) {
			t.Parallel()
			up := NewUpstream400Server(t, "openai-responses")
			exec := NewTestExecutor(t, "openai-responses", up.URL, up.Client())
			route := RouteSelector("openai-responses", DefaultModel("openai-responses"))
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
			if strings.Contains(err.Error(), parityText) {
				t.Fatalf("error path must not invent customer parity text: %v", err)
			}
			switch cell.Frontend {
			case "openai-responses", "openai-legacy":
				var apiErr *openai.Error
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected *openai.Error, got %T: %v", err, err)
				}
			case "anthropic":
				var apiErr *anthropic.Error
				if !errors.As(err, &apiErr) {
					t.Fatalf("expected *anthropic.Error, got %T: %v", err, err)
				}
			case "gemini":
				// Gemini client errors are stringly typed.
			default:
				t.Fatalf("unexpected frontend %q", cell.Frontend)
			}
		})
	}
}

func TestDualPlaneEconomic_CancelAndEncodingModesPresent(t *testing.T) {
	t.Parallel()
	var cancel, encoding int
	for _, cell := range DualPlaneEconomicCells() {
		switch cell.Mode {
		case DualPlaneEconomicModeCancel:
			cancel++
		case DualPlaneEconomicModeEncodingFailure:
			encoding++
		}
	}
	want := len(BundledFrontendIDs())
	if cancel != want || encoding != want {
		t.Fatalf("cancel=%d encoding=%d want %d each", cancel, encoding, want)
	}
}
