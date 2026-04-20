package gemini

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	maxBodyBytes = 8 << 20
	// HeaderRouteSelector carries the core routing selector (e.g. stub:gemini-2.0-flash).
	HeaderRouteSelector = "X-LIP-Route"
)

// Handler wires HTTP POST …/models/{model}:generateContent (and stream variant) to decode → executor → encode.
type Handler struct {
	Exec *runtime.Executor
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	Log                  *slog.Logger
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
	if h.Exec == nil {
		WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, "could not read request body")
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
		status, msg := mapExecuteError(err)
		WriteErrorJSON(w, status, msg)
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
		WriteErrorJSON(w, http.StatusInternalServerError, err.Error())
	}
}

func mapExecuteError(err error) (status int, message string) {
	if lipapi.IsReject(err) {
		return http.StatusBadRequest, err.Error()
	}
	return http.StatusInternalServerError, err.Error()
}
