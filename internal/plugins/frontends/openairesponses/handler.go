package openairesponses

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

const (
	// HeaderRouteSelector carries the core routing selector (e.g. stub:gpt-4o-mini).
	HeaderRouteSelector = "X-LIP-Route"
)

// Handler wires HTTP POST /v1/responses to decode → executor → encode.
type Handler struct {
	Exec lipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	// MaxRequestBodyBytes caps the request body; zero uses reqbody.DefaultMaxBytes.
	MaxRequestBodyBytes int64
	Log                 *slog.Logger
}

func (h *Handler) maxBodyLimit() int64 {
	if h != nil && h.MaxRequestBodyBytes > 0 {
		return h.MaxRequestBodyBytes
	}
	return reqbody.DefaultMaxBytes
}

// ServeHTTP implements OpenAI Responses create on POST …/responses.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Path
	if !strings.HasSuffix(path, "/responses") && path != "/responses" {
		http.NotFound(w, r)
		return
	}

	body, err := reqbody.ReadAll(w, r, h.maxBodyLimit())
	if err != nil {
		if reqbody.TooLarge(err) {
			WriteErrorJSON(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "")
			return
		}
		WriteErrorJSON(w, http.StatusBadRequest, "could not read request body", "invalid_request_error", "")
		return
	}
	if h.Exec == nil {
		WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured", "api_error", "")
		return
	}

	sel := strings.TrimSpace(r.Header.Get(HeaderRouteSelector))
	if sel == "" {
		sel = strings.TrimSpace(h.DefaultRouteSelector)
	}
	decoded, err := DecodeCreateRequest(body, DecodeOptions{RouteSelector: sel})
	if err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	call := decoded.Call
	if err := call.Validate(); err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}

	ctx := r.Context()
	es, err := h.Exec.Execute(ctx, call)
	if err != nil {
		status, msg, code := mapExecuteError(err)
		WriteErrorJSON(w, status, msg, "invalid_request_error", code)
		return
	}

	opts := EncodeOptions{
		ResponseID: "resp_" + diag.StableCallToken(call),
		CreatedAt:  diag.StableUnix(call),
	}
	opts.MessageID = "msg_" + opts.ResponseID
	if clk := h.Exec.WallClock(); clk != nil {
		opts.CreatedAt = clk().Unix()
	}
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
		WriteErrorJSON(w, http.StatusInternalServerError, err.Error(), "api_error", "")
	}
}

func mapExecuteError(err error) (status int, message string, code string) {
	if lipapi.IsReject(err) {
		return http.StatusBadRequest, err.Error(), "unsupported_parameter"
	}
	return http.StatusInternalServerError, err.Error(), ""
}
