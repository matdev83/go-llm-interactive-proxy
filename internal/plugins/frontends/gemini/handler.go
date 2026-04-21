package gemini

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

const (
	// HeaderRouteSelector carries the core routing selector (e.g. stub:gemini-2.0-flash).
	HeaderRouteSelector = "X-LIP-Route"
)

// Handler wires HTTP POST …/models/{model}:generateContent (and stream variant) to decode → executor → encode.
// Tool/function-call history follows the subset documented with the Gemini adapter (see requirements 8.x).
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

// ServeHTTP implements generateContent / streamGenerateContent for the Google AI (ML dev) layout.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	model, stream, ok := ParseGenerateContentPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	body, err := reqbody.ReadAll(w, r, h.maxBodyLimit())
	if err != nil {
		if reqbody.TooLarge(err) {
			WriteErrorJSON(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		WriteErrorJSON(w, http.StatusBadRequest, "could not read request body")
		return
	}
	if h.Exec == nil {
		WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured")
		return
	}

	sel := strings.TrimSpace(r.Header.Get(HeaderRouteSelector))
	if sel == "" {
		sel = strings.TrimSpace(h.DefaultRouteSelector)
	}
	decoded, err := DecodeGenerateContentRequest(body, DecodeOptions{
		RouteSelector: sel,
		Model:         model,
		Stream:        stream,
	})
	if err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	call := decoded.Call
	if err := call.Validate(); err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	es, err := h.Exec.Execute(ctx, call)
	if err != nil {
		out := execerr.ClassifyExecute(err)
		if out.Kind == execerr.InternalError && h.Log != nil && out.Err != nil {
			h.Log.Error("execute failed", "error", out.Err)
		}
		WriteErrorJSON(w, out.Status, out.Message)
		return
	}

	opts := EncodeOptions{}
	if stream {
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
		WriteErrorJSON(w, http.StatusInternalServerError, execerr.InternalWireMessage)
	}
}
