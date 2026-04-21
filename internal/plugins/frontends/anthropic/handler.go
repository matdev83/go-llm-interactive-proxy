package anthropic

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

const (
	// HeaderRouteSelector carries the core routing selector (e.g. stub:claude-3-5-haiku).
	HeaderRouteSelector = "X-LIP-Route"
	// HeaderAnthropicVersion is optional; when absent a default supported version is assumed for decode.
	HeaderAnthropicVersion = "anthropic-version"
)

// Handler wires HTTP POST /v1/messages to decode → executor → encode.
// Tool-call history: only the subset documented alongside decode/encode is preserved on the canonical model.
type Handler struct {
	Exec lipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	MaxRequestBodyBytes  int64
	Log                  *slog.Logger
}

func (h *Handler) maxBodyLimit() int64 {
	if h != nil && h.MaxRequestBodyBytes > 0 {
		return h.MaxRequestBodyBytes
	}
	return reqbody.DefaultMaxBytes
}

// ServeHTTP implements Messages create on POST …/messages.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Path
	if !strings.HasSuffix(path, "/messages") && path != "/messages" {
		http.NotFound(w, r)
		return
	}

	body, err := reqbody.ReadAll(w, r, h.maxBodyLimit())
	if err != nil {
		if reqbody.TooLarge(err) {
			WriteErrorJSON(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error")
			return
		}
		WriteErrorJSON(w, http.StatusBadRequest, "could not read request body", "invalid_request_error")
		return
	}
	if h.Exec == nil {
		WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured", "api_error")
		return
	}

	_ = r.Header.Get(HeaderAnthropicVersion)

	sel := strings.TrimSpace(r.Header.Get(HeaderRouteSelector))
	if sel == "" {
		sel = strings.TrimSpace(h.DefaultRouteSelector)
	}
	decoded, err := DecodeMessageRequest(body, DecodeOptions{RouteSelector: sel})
	if err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	call := decoded.Call
	if err := call.Validate(); err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	ctx := r.Context()
	es, err := h.Exec.Execute(ctx, call)
	if err != nil {
		out := execerr.ClassifyExecute(err)
		if out.Kind == execerr.InternalError && h.Log != nil && out.Err != nil {
			h.Log.Error("execute failed", "error", out.Err)
		}
		errType := "api_error"
		if out.Kind == execerr.ClientReject {
			errType = "invalid_request_error"
		}
		WriteErrorJSON(w, out.Status, out.Message, errType)
		return
	}

	opts := EncodeOptions{MessageID: "msg_" + diag.StableCallToken(call)}
	if decoded.Stream {
		if err := WriteStreamSSE(ctx, w, call, es, opts); err != nil {
			if h.Log != nil {
				h.Log.Error("stream encode failed", "error", err)
			}
			return
		}
		return
	}
	if err := WriteNonStreamJSON(ctx, w, call, es, opts); err != nil {
		if h.Log != nil {
			h.Log.Error("non-stream encode failed", "error", err)
		}
		WriteErrorJSON(w, http.StatusInternalServerError, execerr.InternalWireMessage, "api_error")
	}
}
