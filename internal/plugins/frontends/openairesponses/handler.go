package openairesponses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openaiwire"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

const (
	HeaderRouteSelector  = routeselect.HeaderRouteSelector
	responseIDALegPrefix = "resp_lip_"
)

// Handler wires HTTP POST /v1/responses to decode → executor → encode.
type Handler struct {
	Exec lipsdk.ExecutorView
	// DefaultRouteSelector is used when HeaderRouteSelector is absent.
	DefaultRouteSelector string
	// RoutePrefixes are backend route-selector prefixes accepted from body model.
	RoutePrefixes routeselect.PrefixSet
	// MaxRequestBodyBytes caps the request body; zero uses reqbody.DefaultMaxBytes.
	MaxRequestBodyBytes     int64
	Log                     *slog.Logger
	TrafficPorts            traffic.PortBundle
	DecodeAdmission         lipsdk.DecodeAdmission
	PreRequestKeepalive     lipsdk.FrontendKeepaliveConfig
	Config                  Config
	HTTPHeaders             lipsdk.HTTPHeaders
	StreamKeepaliveInterval time.Duration

	// pipeOnce serializes the first spec() build; handlers serve concurrent requests.
	pipeOnce sync.Once
	pipe     frontendpipe.Spec[EncodeOptions]
}

type aLegCanceler interface {
	CancelALeg(ctx context.Context, req lipapi.ALegCancelRequest) error
}

type responseIDCancelCarrier struct {
	ALegID    string `json:"a"`
	SessionID string `json:"s,omitempty"`
}

func (h *Handler) spec() *frontendpipe.Spec[EncodeOptions] {
	h.pipeOnce.Do(func() {
		h.buildPipe()
	})
	return &h.pipe
}

func (h *Handler) buildPipe() {
	h.pipe = frontendpipe.Spec[EncodeOptions]{
		Config: frontendpipe.Config{
			Exec:                    h.Exec,
			DefaultRouteSelector:    h.DefaultRouteSelector,
			RoutePrefixes:           h.RoutePrefixes,
			MaxRequestBodyBytes:     h.MaxRequestBodyBytes,
			Log:                     h.Log,
			TrafficPorts:            h.TrafficPorts,
			DecodeAdmission:         h.DecodeAdmission,
			PreRequestKeepalive:     h.PreRequestKeepalive,
			FrontendID:              ID,
			HTTPHeaders:             h.HTTPHeaders,
			StreamKeepaliveInterval: h.StreamKeepaliveInterval,
		},
		Wire:               frontendpipe.OpenAIWire{},
		RouteFromBodyModel: true,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if strings.HasSuffix(path, "/responses") || path == "/responses" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		AltServe: func(ctx context.Context, w http.ResponseWriter, r *http.Request) bool {
			if isCancelPath(r.URL.Path) {
				h.serveCancel(ctx, w, r)
				return true
			}
			return false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			decoded, err := DecodeCreateRequest(dctx.Body, DecodeOptions{RouteSelector: dctx.RouteSelector, Headers: dctx.Headers})
			if err != nil {
				return nil, err
			}
			return &frontendpipe.Decoded{Call: decoded.Call, Stream: decoded.Stream, RouteSelector: dctx.RouteSelector}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) EncodeOptions {
			call := decoded.Call
			opts := EncodeOptions{
				ResponseID:               responseIDForCall(call),
				CreatedAt:                diag.StableUnix(call),
				ExposeLipUsageExtensions: h.Config.ExposeLipUsageExtensions,
			}
			opts.MessageID = "msg_" + opts.ResponseID
			if clk := h.Exec.WallClock(); clk != nil {
				opts.CreatedAt = clk().Unix()
			}
			return opts
		},
		WriteStream:    WriteStreamSSE,
		WriteNonStream: WriteNonStreamJSON,
	}
}

// ServeHTTP implements OpenAI Responses create on POST …/responses.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	frontendpipe.ServeHTTP(h.spec(), w, r)
}

func isCancelPath(path string) bool {
	path = strings.TrimSpace(path)
	return strings.HasSuffix(path, "/cancel") && responseIDFromCancelPath(path) != ""
}

func (h *Handler) logWriteJSONErr(ctx context.Context, msg string, werr error) {
	if h.Log == nil || werr == nil {
		return
	}
	diag.LogError(ctx, h.Log, msg, diag.AttrOpts{}, werr)
}

func (h *Handler) serveCancel(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if h.Exec == nil {
		h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(
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
		h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(
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
			h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(
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
			h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(
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
		h.logWriteJSONErr(ctx, "write error json failed", openaiwire.WriteErrorJSON(w, status, msg, typ, ""))
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
		return http.StatusInternalServerError, "internal error", "api_error"
	}
}
