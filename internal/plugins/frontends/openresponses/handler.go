package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	httpauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

// ExecutorView is the canonical event-stream executor used by create.
type ExecutorView interface {
	Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error)
}

// HandlerConfig configures the HTTP handler for OpenResponses.
type HandlerConfig struct {
	Authorizer            Authorizer
	RequireAuthentication bool
	AllowUnauthenticated  bool
	Executor              ExecutorView
	// ContinuationResolver is the narrow injected seam for resolving parent continuation state.
	ContinuationResolver ContinuationResolver
	// ContinuationStore is the injected protocol-neutral store port for continuation
	// state. When nil, continuation features degrade gracefully.
	ContinuationStore       lipcont.Store
	DefaultRouteSelector    string
	RoutePrefixes           []string
	MaxRequestBodyBytes     int64
	ProtocolLimits          proto.Limits
	DecodeAdmission         lipsdk.DecodeAdmission
	TrafficPorts            traffic.PortBundle
	PreRequestKeepalive     lipsdk.FrontendKeepaliveConfig
	Config                  Config
	HTTPHeaders             lipsdk.HTTPHeaders
	StreamKeepaliveInterval time.Duration
	ResponseIDSource        ResponseIDSource
	CompactResourceIDSource CompactResourceIDSource
	ResponseClock           ResponseClock
	// RecorderFactory is the narrow seam for incremental terminal recording.
	// A nil factory uses the standard core recorder.
	RecorderFactory ContinuationRecorderFactory
}

// Handler wires OpenResponses HTTP requests to auth → decode → executor. Direct
// handlers require an authenticated transport context by default; callers must
// explicitly opt into anonymous access with AllowUnauthenticated.
type Handler struct {
	cfg      HandlerConfig
	pipeOnce sync.Once
	pipe     frontendpipe.Spec[createEncodeState]
}

// NewHandler creates a new OpenResponses HTTP handler.
func NewHandler(cfg HandlerConfig) *Handler {
	if !cfg.AllowUnauthenticated {
		cfg.RequireAuthentication = true
	}
	return &Handler{cfg: cfg}
}

var _ http.Handler = (*Handler)(nil)

func (h *Handler) getStore() lipcont.Store {
	return h.cfg.ContinuationStore
}

// ServeHTTP handles HTTP requests for OpenResponses endpoints.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Method first so non-POST requests stay 405 even without application/json.
	// Auth and JSON Content-Type stay on the handler: frontendpipe does not
	// enforce application/json. Keepalive and body admission live in the pipe.
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dec, ok := h.authorizeCreate(w, r)
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeWireError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "unsupported_media_type", "Request Content-Type must be application/json")
		return
	}
	r = r.WithContext(contextWithAuthDecision(r.Context(), dec))
	frontendpipe.ServeHTTP(h.spec(), w, r)
}

func (h *Handler) authorizeCreate(w http.ResponseWriter, r *http.Request) (sdkauth.Decision, bool) {
	ctx := r.Context()
	if h.cfg.Authorizer != nil {
		opts := DecodeCreateOptions{
			Auth:        h.cfg.Authorizer,
			Headers:     r.Header,
			Method:      r.Method,
			Path:        r.URL.Path,
			RemoteAddr:  r.RemoteAddr,
			HTTPHeaders: h.cfg.HTTPHeaders,
		}
		dec, err := opts.Auth.Authenticate(ctx, opts.authMeta(r))
		if err != nil || dec.Outcome != sdkauth.OutcomeAllow {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(proto.WireErrorEnvelope{
				Error: proto.WireErrorDetails{
					Message: "Authentication required",
					Type:    "authentication_error",
					Code:    "unauthorized",
				},
			})
			return sdkauth.Decision{}, false
		}
		return dec, true
	}
	if h.cfg.RequireAuthentication {
		if _, ok := httpauth.PrincipalFromContext(ctx); !ok {
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return sdkauth.Decision{}, false
		}
	}
	return sdkauth.Decision{}, true
}

// cleanupContinuationReservation runs independently of a canceled request so
// failed execution cannot strand a reserved proxy ID until its TTL expires.
func cleanupContinuationReservation(store lipcont.Store, scope lipcont.Scope, responseID string) {
	if store == nil || responseID == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = store.Delete(cleanupCtx, scope, lipcont.ResponseID(responseID))
}

func continuationScope(decoded *DecodedCreate) lipcont.Scope {
	scope := lipcont.Scope{SessionID: strings.TrimSpace(decoded.Call.Session.AuthoritativeSessionID)}
	if decoded.AuthDecision.Scope != nil {
		scope.TenantID = decoded.AuthDecision.Scope.TenantID.String()
		scope.PrincipalID = decoded.AuthDecision.Scope.PrincipalID.String()
	}
	if scope.PrincipalID == "" {
		scope.PrincipalID = decoded.AuthDecision.Principal.ID
	}
	if scope.PrincipalID == "" {
		scope.PrincipalID = scope.SessionID
	}
	return scope
}

func (h *Handler) storagePolicy() lipcont.StoragePolicy {
	ttl, err := time.ParseDuration(h.cfg.Config.Continuation.TTL)
	if err != nil || ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return lipcont.StoragePolicy{Mode: lipcont.PersistencePersistent, TTL: ttl, Limits: lipcont.DefaultStorageLimits()}
}

func sanitizedExecutionMessage(err error) string {
	if errors.Is(err, context.Canceled) {
		return "Request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Request timed out"
	}
	return "Backend execution failed"
}

// safeCanonicalErrorMessage applies a strict allowlist to canonical messages
// crossing the wire. Error codes remain authoritative for programmatic
// handling; arbitrary provider text is never exposed by default.
func safeCanonicalErrorMessage(message string) string {
	switch strings.TrimSpace(message) {
	case "Backend execution failed",
		"Rate limit exceeded",
		"request processing timed out",
		"request was canceled by client",
		"upstream reported a compaction failure",
		"upstream reported an error",
		"stream error",
		"bad request",
		"failed":
		return strings.TrimSpace(message)
	default:
		return "Backend execution failed"
	}
}

// classifyExecutionError maps stable canonical errors to HTTP semantics without
// rendering provider or internal error text. Unknown execution failures remain
// gateway errors; only the known client/policy roots are downgraded to 4xx.
func classifyExecutionError(err error) (status int, typ, code, message string) {
	var streamErr *lipapi.StreamError
	if errors.As(err, &streamErr) {
		return classifyCanonicalEventError(lipapi.Event{
			Kind:         lipapi.EventError,
			ErrorCode:    streamErr.Code,
			ErrorMessage: streamErr.Message,
		})
	}
	if errors.Is(err, context.Canceled) {
		return http.StatusBadRequest, "invalid_request_error", "client_closed_request", "Request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "timeout_error", "timeout", "Request timed out"
	}
	if errors.Is(err, lipapi.ErrInvalidCall) {
		return http.StatusBadRequest, "invalid_request_error", "invalid_request", "Invalid request"
	}
	if errors.Is(err, lipapi.ErrCapabilityReject) || errors.Is(err, lipapi.ErrTransportReject) {
		return http.StatusBadGateway, "server_error", "backend_unavailable", "Request could not be served by the selected backend"
	}
	if errors.Is(err, lipapi.ErrPolicyDenied) {
		return http.StatusForbidden, "permission_error", "policy_denied", "Request denied by policy"
	}
	if errors.Is(err, lipapi.ErrSessionDenial) {
		return http.StatusBadRequest, "invalid_request_error", "session_denied", "Request session is not valid"
	}
	return http.StatusBadGateway, "server_error", "backend_error", sanitizedExecutionMessage(err)
}

func writeWireError(w http.ResponseWriter, status int, typ, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(proto.WireErrorEnvelope{
		Error: proto.WireErrorDetails{Message: message, Type: typ, Code: code},
	})
}

func (opts DecodeCreateOptions) authMeta(r *http.Request) sdkauth.InboundCallMeta {
	return sdkauth.InboundCallMeta{
		Frontend:            ID,
		Method:              r.Method,
		Path:                r.URL.Path,
		ClientAddr:          r.RemoteAddr,
		AuthorizationBearer: opts.HTTPHeaders.APIKeyFrom(r.Header),
	}
}

func (h *Handler) writeCompact(ctx context.Context, w http.ResponseWriter, es lipapi.EventStream, decoded *DecodedCompact) error {
	if decoded == nil || decoded.Call == nil {
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
		return nil
	}
	if es == nil {
		writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
		return nil
	}
	clock := h.cfg.ResponseClock
	if clock == nil {
		clock = systemResponseClock{}
	}
	ids := h.cfg.CompactResourceIDSource
	if ids == nil {
		ids = systemCompactResourceIDSource{}
	}
	resource, collectErr := collectCompact(ctx, es, compactEnvelope(decoded, ids.NewCompactResourceID(), clock.Now()), decoded.Call.Options, h.cfg.ProtocolLimits)
	if collectErr != nil {
		if ctx.Err() != nil {
			return nil
		}
		status, typ, code, message := classifyExecutionError(collectErr)
		writeWireError(w, status, typ, code, message)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resource)
	return nil
}
