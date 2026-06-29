package openairesponses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/execerr"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/holdalive"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/jsonguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

const (
	// HeaderRouteSelector carries the core routing selector (e.g. stub:gpt-4o-mini).
	HeaderRouteSelector  = "X-LIP-Route"
	responseIDALegPrefix = "resp_lip_"
)

// Handler wires HTTP POST /v1/responses to decode → executor → encode.
type Handler struct {
	Exec lipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	// MaxRequestBodyBytes caps the request body; zero uses reqbody.DefaultMaxBytes.
	MaxRequestBodyBytes int64
	Log                 *slog.Logger
	TrafficPorts        traffic.PortBundle
	DecodeLimiter       *decodeqos.Limiter
	PreRequestKeepalive lipsdk.FrontendKeepaliveConfig
	Config              Config
}

type aLegCanceler interface {
	CancelALeg(ctx context.Context, req lipapi.ALegCancelRequest) error
}

type responseIDCancelCarrier struct {
	ALegID    string `json:"a"`
	SessionID string `json:"s,omitempty"`
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

// ServeHTTP implements OpenAI Responses create on POST …/responses.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Path
	if isCancelPath(path) {
		h.serveCancel(ctx, w, r)
		return
	}
	if !strings.HasSuffix(path, "/responses") && path != "/responses" {
		http.NotFound(w, r)
		return
	}

	limits := jsonguard.Limits{MaxBytes: h.maxBodyLimit()}
	body, err := reqbody.ReadAll(w, r, limits.MaxBytes)
	if err != nil {
		if reqbody.TooLarge(err) {
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
				w,
				http.StatusRequestEntityTooLarge,
				"request body too large",
				"invalid_request_error",
				"",
			))
			return
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			http.StatusBadRequest,
			"could not read request body",
			"invalid_request_error",
			"",
		))
		return
	}
	ct := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if h.Exec == nil {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			http.StatusInternalServerError,
			"executor not configured",
			"api_error",
			"",
		))
		return
	}

	sel := strings.TrimSpace(r.Header.Get(HeaderRouteSelector))
	releaseDecode, ok, err := h.DecodeLimiter.TryAcquire(ctx)
	if err != nil {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusServiceUnavailable, execerr.InternalWireMessage, "api_error", ""))
		return
	}
	if !ok {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(w, http.StatusServiceUnavailable, execerr.InternalWireMessage, "api_error", ""))
		return
	}
	if _, err := jsonguard.Preflight(body, limits); err != nil {
		releaseDecode()
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			http.StatusBadRequest,
			"invalid request JSON",
			"invalid_request_error",
			"",
		))
		return
	}
	if sel == "" {
		sel = routeselect.FromModelOrDefault(body, h.DefaultRouteSelector)
	}
	decoded, err := DecodeCreateRequest(body, DecodeOptions{RouteSelector: sel, Headers: r.Header})
	releaseDecode()
	if err != nil {
		if h.Log != nil {
			diag.LogError(ctx, h.Log, "decode request failed", diag.AttrOpts{}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			http.StatusBadRequest,
			"invalid request JSON",
			"invalid_request_error",
			"",
		))
		return
	}
	call := decoded.Call
	if err := call.Validate(); err != nil {
		if h.Log != nil {
			diag.LogError(ctx, h.Log, "validate call failed", diag.AttrOpts{CallID: call.ID}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		}
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			http.StatusBadRequest,
			"invalid request",
			"invalid_request_error",
			"",
		))
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
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
				w,
				out.Status,
				out.Message,
				execerr.OpenAIWireErrorType(out.Status),
				"",
			))
		case execerr.KindClientReject:
			code := "unsupported_parameter"
			if out.Status == http.StatusRequestEntityTooLarge {
				code = ""
			}
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
				w,
				out.Status,
				out.Message,
				"invalid_request_error",
				code,
			))
		default:
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
				w,
				out.Status,
				execerr.InternalWireMessage,
				"api_error",
				"",
			))
		}
		return
	}

	ctx = diag.EnsureCallDiag(ctx, traceID, call.Session.ALegID)

	opts := EncodeOptions{
		ResponseID:               responseIDForCall(call),
		CreatedAt:                diag.StableUnix(call),
		ExposeLipUsageExtensions: h.Config.ExposeLipUsageExtensions,
	}
	opts.MessageID = "msg_" + opts.ResponseID
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
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			http.StatusInternalServerError,
			execerr.InternalWireMessage,
			"api_error",
			"",
		))
	}
}

func isCancelPath(path string) bool {
	path = strings.TrimSpace(path)
	return strings.HasSuffix(path, "/cancel") && responseIDFromCancelPath(path) != ""
}

func (h *Handler) serveCancel(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if h.Exec == nil {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			http.StatusInternalServerError,
			"executor not configured",
			"api_error",
			"",
		))
		return
	}
	canceler, ok := h.Exec.(aLegCanceler)
	if !ok {
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			http.StatusNotImplemented,
			"response cancellation is not configured",
			"api_error",
			"",
		))
		return
	}
	aLegID := strings.TrimSpace(r.Header.Get(sessionwire.HeaderALegID))
	responseID := responseIDFromCancelPath(r.URL.Path)
	sessionID := strings.TrimSpace(r.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	if aLegID == "" {
		decodedALegID, decodedSessionID, ok := cancelCarrierFromResponseID(responseID)
		if !ok {
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
				w,
				http.StatusBadRequest,
				"missing A-leg cancellation carrier",
				"invalid_request_error",
				"",
			))
			return
		}
		aLegID = decodedALegID
		if sessionID == "" {
			sessionID = decodedSessionID
		}
		if sessionID == "" {
			h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
				w,
				http.StatusBadRequest,
				"missing session cancellation carrier",
				"invalid_request_error",
				"",
			))
			return
		}
	}
	if err := canceler.CancelALeg(ctx, lipapi.ALegCancelRequest{
		ALegID:      aLegID,
		SessionID:   sessionID,
		ResumeToken: r.Header.Get(sessionwire.HeaderResumeToken),
		FrontendID:  ID,
		Reason:      "openai_responses_cancel",
	}); err != nil {
		if h.Log != nil {
			diag.LogError(ctx, h.Log, "cancel a-leg failed", diag.AttrOpts{}, err)
		}
		status, msg, typ := cancelErrorWire(err)
		h.logWriteJSONErr(ctx, "write error json failed", WriteErrorJSON(
			w,
			status,
			msg,
			typ,
			"",
		))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
	}{
		ID:     responseID,
		Object: "response",
		Status: "cancelled",
	})
}

func responseIDForCall(call *lipapi.Call) string {
	if call != nil {
		if aLegID := strings.TrimSpace(call.Session.ALegID); aLegID != "" {
			carrier := responseIDCancelCarrier{
				ALegID:    aLegID,
				SessionID: strings.TrimSpace(call.Session.AuthoritativeSessionID),
			}
			raw, err := json.Marshal(carrier)
			if err == nil {
				return responseIDALegPrefix + base64.RawURLEncoding.EncodeToString(raw)
			}
		}
	}
	return "resp_" + diag.StableCallToken(call)
}

func cancelCarrierFromResponseID(responseID string) (string, string, bool) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(responseID), responseIDALegPrefix)
	if !ok || encoded == "" {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}
	var carrier responseIDCancelCarrier
	if err := json.Unmarshal(raw, &carrier); err == nil {
		aLegID := strings.TrimSpace(carrier.ALegID)
		if aLegID == "" {
			return "", "", false
		}
		return aLegID, strings.TrimSpace(carrier.SessionID), true
	}
	aLegID := strings.TrimSpace(string(raw))
	if aLegID == "" {
		return "", "", false
	}
	return aLegID, "", true
}

func responseIDFromCancelPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	parts := strings.Split(path, "/")
	for i := len(parts) - 3; i >= 0; i-- {
		if parts[i] == "responses" && parts[i+2] == "cancel" {
			return parts[i+1]
		}
	}
	return ""
}

func cancelErrorWire(err error) (int, string, string) {
	switch {
	case errors.Is(err, domain.ErrMissingPrincipal):
		return http.StatusUnauthorized, "missing cancellation identity", "authentication_error"
	case errors.Is(err, domain.ErrOwnerMismatch):
		return http.StatusForbidden, "cancellation forbidden", "invalid_request_error"
	case errors.Is(err, domain.ErrSessionNotFound):
		return http.StatusNotFound, "cancellation target not found", "invalid_request_error"
	default:
		return http.StatusInternalServerError, execerr.InternalWireMessage, "api_error"
	}
}
