package openailegacy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/holdalive"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/jsonguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

const (
	// HeaderRouteSelector carries the core routing selector (e.g. stub:gpt-4o-mini).
	HeaderRouteSelector = "X-LIP-Route"
)

// Handler wires HTTP POST …/chat/completions to decode → executor → encode.
type Handler struct {
	Exec lipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	MaxRequestBodyBytes  int64
	Log                  *slog.Logger
	TrafficPorts         traffic.PortBundle
	DecodeLimiter        *decodeqos.Limiter
	PreRequestKeepalive  lipsdk.FrontendKeepaliveConfig
}

func (h *Handler) maxBodyLimit() int64 {
	if h != nil && h.MaxRequestBodyBytes > 0 {
		return h.MaxRequestBodyBytes
	}
	return reqbody.DefaultMaxBytes
}

func (h *Handler) logWriteJSONErr(ctx context.Context, msg string, werr error) {
	if h.Log == nil || werr == nil {
		return
	}
	diag.LogError(ctx, h.Log, msg, diag.AttrOpts{}, werr)
}

func (h *Handler) execute(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, stream bool) (lipapi.EventStream, error) {
	if !stream {
		return h.Exec.Execute(ctx, call)
	}
	return holdalive.Wait(ctx, w, holdalive.Config{
		Enabled:  h.PreRequestKeepalive.Enabled,
		Interval: h.PreRequestKeepalive.Interval,
	}, func(ctx context.Context) (lipapi.EventStream, error) {
		return h.Exec.Execute(ctx, call)
	})
}

// ServeHTTP implements Chat Completions create on POST …/chat/completions.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Path
	if !strings.HasSuffix(path, "/chat/completions") && path != "/chat/completions" {
		http.NotFound(w, r)
		return
	}

	limits := jsonguard.Limits{MaxBytes: h.maxBodyLimit()}
	body, err := reqbody.ReadAll(w, r, limits.MaxBytes)
	if err != nil {
		if reqbody.TooLarge(err) {
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", ""))
			return
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusBadRequest, "could not read request body", "invalid_request_error", ""))
		return
	}
	ct := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if h.Exec == nil {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured", "api_error", ""))
		return
	}

	sel := strings.TrimSpace(r.Header.Get(HeaderRouteSelector))
	releaseDecode, ok, err := h.DecodeLimiter.TryAcquire(ctx)
	if err != nil {
		return
	}
	if !ok {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusServiceUnavailable, execerr.InternalWireMessage, "api_error", ""))
		return
	}
	if _, err := jsonguard.Preflight(body, limits); err != nil {
		releaseDecode()
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusBadRequest, "invalid request JSON", "invalid_request_error", ""))
		return
	}
	if sel == "" {
		sel = routeselect.FromModelOrDefault(body, h.DefaultRouteSelector)
	}
	decoded, err := DecodeChatRequest(body, DecodeOptions{RouteSelector: sel, Headers: r.Header})
	releaseDecode()
	if err != nil {
		if h.Log != nil {
			diag.LogError(ctx, h.Log, "decode request failed", diag.AttrOpts{}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusBadRequest, "invalid request JSON", "invalid_request_error", ""))
		return
	}
	call := decoded.Call
	if err := call.Validate(); err != nil {
		if h.Log != nil {
			diag.LogError(ctx, h.Log, "validate call failed", diag.AttrOpts{CallID: call.ID}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusBadRequest, "invalid request", "invalid_request_error", ""))
		return
	}

	traceID := diag.StableCallID(call)
	ctx = diag.EnsureCallDiag(ctx, traceID, strings.TrimSpace(call.Session.ALegID))
	h.TrafficPorts.Emit(ctx, traffic.LegCTP, traffic.CaptureMeta{
		TraceID:   traceID,
		SessionID: call.Session.CorrelationID(),
	}, "http", ct, body)

	es, err := h.execute(ctx, w, call, decoded.Stream)
	if err != nil {
		out := execerr.ClassifyExecute(err)
		if out.Kind == execerr.KindInternalError && h.Log != nil && out.Err != nil {
			diag.LogError(ctx, h.Log, "execute failed", diag.AttrOpts{CallID: call.ID}, out.Err)
		}
		switch out.Kind {
		case execerr.KindSessionDenial:
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, out.Status, out.Message, execerr.OpenAIWireErrorType(out.Status), ""))
		case execerr.KindClientReject:
			code := "unsupported_parameter"
			if out.Status == http.StatusRequestEntityTooLarge {
				code = ""
			}
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, out.Status, out.Message, "invalid_request_error", code))
		default:
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, out.Status, execerr.InternalWireMessage, "api_error", ""))
		}
		return
	}

	ctx = diag.EnsureCallDiag(ctx, traceID, call.Session.ALegID)

	opts := EncodeOptions{
		CompletionID: "chatcmpl_" + diag.StableCallToken(call),
		CreatedAt:    diag.StableUnix(call),
	}
	if clk := h.Exec.WallClock(); clk != nil {
		opts.CreatedAt = clk().Unix()
	}
	if decoded.Stream {
		if err := WriteStreamSSE(ctx, w, call, es, opts); err != nil {
			diag.LogError(ctx, h.Log, "stream encode failed", diag.AttrOpts{CallID: call.ID}, err)
			return
		}
		return
	}
	if err := WriteNonStreamJSON(ctx, w, call, es, opts); err != nil {
		diag.LogError(ctx, h.Log, "non-stream encode failed", diag.AttrOpts{CallID: call.ID}, err)
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusInternalServerError, execerr.InternalWireMessage, "api_error", ""))
	}
}
