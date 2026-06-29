package gemini

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
	TrafficPorts         traffic.PortBundle
	DecodeLimiter        *decodeqos.Limiter
	PreRequestKeepalive  lipsdk.FrontendKeepaliveConfig
	Config               Config
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

// ServeHTTP implements generateContent / streamGenerateContent for the Google AI (ML dev) layout.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	model, stream, ok := ParseGenerateContentPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	limits := jsonguard.Limits{MaxBytes: h.maxBodyLimit()}
	body, err := reqbody.ReadAll(w, r, limits.MaxBytes)
	if err != nil {
		if reqbody.TooLarge(err) {
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusRequestEntityTooLarge, "request body too large"))
			return
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusBadRequest, "could not read request body"))
		return
	}
	ct := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if h.Exec == nil {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusInternalServerError, "executor not configured"))
		return
	}

	sel := strings.TrimSpace(r.Header.Get(HeaderRouteSelector))
	releaseDecode, ok, err := h.DecodeLimiter.TryAcquire(ctx)
	if err != nil {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusServiceUnavailable, execerr.InternalWireMessage))
		return
	}
	if !ok {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusServiceUnavailable, execerr.InternalWireMessage))
		return
	}
	if _, err := jsonguard.Preflight(body, limits); err != nil {
		releaseDecode()
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusBadRequest, "invalid request JSON"))
		return
	}
	if sel == "" {
		sel = routeselect.InlineOrDefault(model, h.DefaultRouteSelector)
	}
	decoded, err := DecodeGenerateContentRequest(body, DecodeOptions{
		RouteSelector: sel,
		Model:         model,
		Stream:        stream,
		Headers:       r.Header,
	})
	releaseDecode()
	if err != nil {
		if h.Log != nil {
			diag.LogError(ctx, h.Log, "decode request failed", diag.AttrOpts{}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusBadRequest, "invalid request JSON"))
		return
	}
	call := decoded.Call
	if err := call.Validate(); err != nil {
		if h.Log != nil {
			diag.LogError(ctx, h.Log, "validate call failed", diag.AttrOpts{CallID: call.ID}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusBadRequest, "invalid request"))
		return
	}

	traceID := diag.StableCallID(call)
	ctx = diag.EnsureCallDiag(ctx, traceID, strings.TrimSpace(call.Session.ALegID))
	h.TrafficPorts.Emit(ctx, traffic.LegCTP, traffic.CaptureMeta{
		TraceID:   traceID,
		SessionID: call.Session.CorrelationID(),
	}, "http", ct, body)

	es, err := h.execute(ctx, w, call, stream)
	if err != nil {
		out := execerr.ClassifyExecute(err)
		if out.Kind == execerr.KindInternalError && h.Log != nil && out.Err != nil {
			diag.LogError(ctx, h.Log, "execute failed", diag.AttrOpts{CallID: call.ID}, out.Err)
		}
		msg := out.Message
		if out.Kind == execerr.KindInternalError {
			msg = execerr.InternalWireMessage
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, out.Status, msg))
		return
	}

	ctx = diag.EnsureCallDiag(ctx, traceID, call.Session.ALegID)

	opts := EncodeOptions{ExposeLipUsageExtensions: h.Config.ExposeLipUsageExtensions}
	if stream {
		if err := WriteStreamSSE(ctx, w, call, es, opts); err != nil {
			diag.LogError(ctx, h.Log, "stream encode failed", diag.AttrOpts{CallID: call.ID}, err)
			return
		}
		return
	}
	if err := WriteNonStreamJSON(ctx, w, call, es, opts); err != nil {
		diag.LogError(ctx, h.Log, "non-stream encode failed", diag.AttrOpts{CallID: call.ID}, err)
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusInternalServerError, execerr.InternalWireMessage))
	}
}
