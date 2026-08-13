package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
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
	cfg HandlerConfig
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
	ctx := r.Context()
	if h.cfg.StreamKeepaliveInterval > 0 {
		ctx = stream.ContextWithKeepaliveInterval(ctx, h.cfg.StreamKeepaliveInterval)
		r = r.WithContext(ctx)
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/responses/compact") {
		h.handleCompact(w, r)
		return
	}

	// 1. Auth check BEFORE reading body or continuation work (Requirement 2.10, 10.8)
	var authDecision sdkauth.Decision
	if h.cfg.Authorizer != nil {
		opts := DecodeCreateOptions{
			Auth:       h.cfg.Authorizer,
			Headers:    r.Header,
			Method:     r.Method,
			Path:       r.URL.Path,
			RemoteAddr: r.RemoteAddr,
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
			return
		}
		authDecision = dec
	} else if h.cfg.RequireAuthentication {
		if _, ok := httpauth.PrincipalFromContext(ctx); !ok {
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return
		}
	}

	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeWireError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "unsupported_media_type", "Request Content-Type must be application/json")
		return
	}

	// 2. Reserve decode capacity before reading the body. Content-Length is
	// only an upper bound; unknown-length requests reserve the hard cap.
	maxBytes := h.cfg.MaxRequestBodyBytes
	if maxBytes <= 0 {
		maxBytes = proto.MaxRequestBytes
	}
	// Reserve the hard cap rather than trusting Content-Length: clients can
	// under-report it while the bounded reader still accepts maxBytes bytes.
	releaseDecode, admitted, admissionErr := decodeqos.TryAdmit(ctx, h.cfg.DecodeAdmission, maxBytes)
	if decision := decodeqos.Decide(admitted, admissionErr); decision.Status != 0 {
		if decision.RetryAfter {
			w.Header().Set("Retry-After", decodeqos.RetryAfterSeconds)
		}
		writeWireError(w, decision.Status, "server_error", "decode_admission_rejected", decision.Message)
		return
	}
	releaseDecodeOnce := sync.OnceFunc(func() {
		if releaseDecode != nil {
			releaseDecode()
		}
	})
	defer releaseDecodeOnce()

	bodyReader := io.LimitReader(r.Body, maxBytes+1)
	body, err := io.ReadAll(bodyReader)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(proto.WireErrorEnvelope{
			Error: proto.WireErrorDetails{
				Message: "Failed to read request body",
				Type:    "invalid_request_error",
				Code:    "bad_request",
			},
		})
		return
	}

	if int64(len(body)) > maxBytes {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(proto.WireErrorEnvelope{
			Error: proto.WireErrorDetails{
				Message: "Request body exceeds max limit",
				Type:    "invalid_request_error",
				Code:    "body_too_large",
			},
		})
		return
	}

	// 3. Decode request payload
	opts := DecodeCreateOptions{
		DefaultRouteSelector: h.cfg.DefaultRouteSelector,
		RoutePrefixes:        h.cfg.RoutePrefixes,
		RouteSelector:        h.cfg.HTTPHeaders.RouteSelector(r.Header),
		Headers:              r.Header,
		Method:               r.Method,
		Path:                 r.URL.Path,
		RemoteAddr:           r.RemoteAddr,
		MaxBodyBytes:         maxBytes,
		Limits:               h.cfg.ProtocolLimits,
		HTTPHeaders:          h.cfg.HTTPHeaders,
		// Authentication was completed above, before reading the body. Avoid a
		// second policy evaluation while retaining the direct decode seam's auth.
	}

	var decoded *DecodedCreate
	err = decodeqos.Guard(releaseDecodeOnce, func() error {
		var decodeErr error
		decoded, decodeErr = AuthenticateAndDecodeCreate(ctx, body, opts)
		if decodeErr == nil && decoded != nil && decoded.Call != nil {
			hdrs := h.cfg.HTTPHeaders.OrDefault()
			sessionwire.ApplyAuthoritativeHeadersNamed(&decoded.Call.Session, r.Header, hdrs.SessionID, hdrs.ResumeToken)
		}
		return decodeErr
	})
	releaseDecode = nil
	if err != nil {
		status := http.StatusBadRequest
		errType := "invalid_request_error"
		errCode := "bad_request"
		if errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "unauthorized") {
			status = http.StatusUnauthorized
			errType = "authentication_error"
			errCode = "unauthorized"
		}
		message := "Invalid request"
		if status == http.StatusUnauthorized {
			message = "Authentication required"
		}
		writeWireError(w, status, errType, errCode, message)
		return
	}
	decoded.AuthDecision = authDecision

	var (
		store       = h.getStore()
		parent      lipcont.ContinuationRecord
		responseID  string
		isReserved  bool
		recordInput = decoded.Call.Items
		scope       = continuationScope(decoded)
	)
	if decoded.PreviousResponseID != "" {
		resolver := h.cfg.ContinuationResolver
		if resolver == nil && store != nil {
			resolver = NewStoreContinuationResolver(store, lipcont.Bounds{
				MaxChainDepth:        h.cfg.Config.Continuation.MaxChainDepth,
				MaxMaterializedBytes: h.cfg.Config.Continuation.MaxMaterializedBytes,
			})
		}
		if resolver == nil {
			writeWireError(w, http.StatusBadRequest, "invalid_request_error", "previous_response_not_found", "Previous response was not found")
			return
		}
		materialized, parentRecord, err := resolver.ResolveParent(ctx, scope, decoded.PreviousResponseID, *decoded.Call)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, lipcont.ErrStorageFailure) || errors.Is(err, context.DeadlineExceeded) {
				writeWireError(w, http.StatusInternalServerError, "server_error", "storage_error", "Continuation storage is unavailable")
				return
			}
			writeWireError(w, http.StatusBadRequest, "invalid_request_error", "previous_response_not_found", "Previous response was not found")
			return
		}
		parent = parentRecord
		// Echo only the resolved proxy ID. The resolver is authoritative and a
		// backend-native identifier must never become client continuation state.
		if parent.ID != "" {
			decoded.PreviousResponseID = parent.ID.String()
		}
		decoded.Call = &materialized
		if decoded.Model == "" {
			decoded.Model = parent.Lineage.Model
		}
		if decoded.Call.Route.Selector == "" {
			decoded.Call.Route.Selector = parent.Lineage.RouteSelector
			if decoded.Call.Route.Selector == "" {
				decoded.Call.Route.Selector = parent.Lineage.Model
			}
		}
	}
	if h.cfg.Executor == nil {
		writeWireError(w, http.StatusNotImplemented, "invalid_request_error", "operation_not_implemented", "OpenResponses responses is not enabled")
		return
	}

	if ctx.Err() != nil {
		return
	}
	if decoded.Store && store != nil {
		policy := h.storagePolicy()
		reserved, err := store.Reserve(ctx, scope, policy)
		if err != nil {
			writeWireError(w, http.StatusInternalServerError, "server_error", "storage_error", "Failed to prepare response storage")
			return
		}
		responseID = reserved.String()
		isReserved = true
	}
	if responseID == "" {
		ids := h.cfg.ResponseIDSource
		if ids == nil {
			ids = systemResponseIDSource{}
		}
		responseID = ids.NewResponseID()
	}

	var observer lipcont.StreamObserver
	if decoded.Store && store != nil {
		lineage := lipcont.Lineage{ProfileID: DefaultProfile, Model: decoded.Model, RouteSelector: decoded.Call.Route.Selector}
		previous := lipcont.ResponseID(decoded.PreviousResponseID)
		depth := parent.ChainDepth + 1
		recorderFactory := h.cfg.RecorderFactory
		if recorderFactory == nil {
			recorderFactory = defaultContinuationRecorderFactory()
		}
		observer = recorderFactory.NewRecorder(store, lipcont.ContinuationRecord{
			ID:           lipcont.ResponseID(responseID),
			Scope:        scope,
			PreviousID:   previous,
			ProfileID:    DefaultProfile,
			Lineage:      lineage,
			InputItems:   lipcont.CloneItems(recordInput),
			Requirements: lipapi.DeriveProtocolRequirements(*decoded.Call),
			Policy:       h.storagePolicy(),
			ChainDepth:   depth,
		})
	}

	// 4. Submit call to the canonical executor.
	{
		if ctx.Err() != nil {
			if isReserved && store != nil {
				cleanupContinuationReservation(store, scope, responseID)
			}
			return
		}
		stream, err := h.cfg.Executor.Execute(ctx, decoded.Call)
		if err != nil {
			if isReserved && store != nil {
				cleanupContinuationReservation(store, scope, responseID)
			}
			status, typ, code, message := classifyExecutionError(err)
			writeWireError(w, status, typ, code, message)
			return
		}
		if stream == nil {
			// A nil executor stream has no transport lifecycle on which an
			// observer could close itself. Transfer ownership before closing
			// recorder-backed observers so the frontend fallback cannot release
			// the same reservation a second time.
			if owner, ok := observer.(continuationReservationOwner); ok && owner.OwnsContinuationReservation() {
				// There is no stream lifecycle to finalize. Consume the
				// recorder-owned reservation before closing the observer.
				safeReleaseContinuationReservation(owner)
			}
			safeCloseObserverFrontend(observer)
			if isReserved && store != nil {
				cleanupContinuationReservation(store, scope, responseID)
			}
			writeWireError(w, http.StatusBadGateway, "server_error", "backend_error", "Backend execution failed")
			return
		}
		// Enforce the allowed_tools hard constraint before any client output
		// path (streaming and non-streaming both consume this stream).
		stream = newAllowedToolsStream(decoded.Call, stream)
		var owner continuationReservationOwner
		if observer != nil {
			stream = &observedEventStream{EventStream: stream, observer: observer}
			if candidate, ok := observer.(continuationReservationOwner); ok && candidate.OwnsContinuationReservation() {
				// The built-in recorder has transferred reservation ownership to
				// its Close/FinalizeIncomplete lifecycle.
				owner = candidate
				isReserved = false
			}
		}
		if decoded.Stream {
			h.serveStreaming(ctx, w, stream, decoded, responseID, store, scope, isReserved, owner)
			return
		}
		if !decoded.Stream {
			clock := h.cfg.ResponseClock
			if clock == nil {
				clock = systemResponseClock{}
			}
			resource, collectErr := collectNonStreaming(ctx, stream, nonStreamingEnvelope(decoded, responseID, clock.Now()), decoded.Call.Options, h.cfg.ProtocolLimits)
			if collectErr != nil {
				if isReserved && store != nil {
					cleanupContinuationReservation(store, scope, responseID)
				}
				if ctx.Err() != nil {
					return
				}
				status, typ, code, message := classifyExecutionError(collectErr)
				writeWireError(w, status, typ, code, message)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(resource)
			return
		}
	}
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

func (opts DecodeCompactOptions) authMeta(r *http.Request) sdkauth.InboundCallMeta {
	return sdkauth.InboundCallMeta{
		Frontend:            ID,
		Method:              r.Method,
		Path:                r.URL.Path,
		ClientAddr:          r.RemoteAddr,
		AuthorizationBearer: opts.HTTPHeaders.APIKeyFrom(r.Header),
	}
}

func (h *Handler) handleCompact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Auth check BEFORE reading body or work (Requirement 2.10, 10.8)
	var authDecision sdkauth.Decision
	if h.cfg.Authorizer != nil {
		opts := DecodeCompactOptions{
			Auth:       h.cfg.Authorizer,
			Headers:    r.Header,
			Method:     r.Method,
			Path:       r.URL.Path,
			RemoteAddr: r.RemoteAddr,
		}
		dec, err := opts.Auth.Authenticate(ctx, opts.authMeta(r))
		if err != nil || dec.Outcome != sdkauth.OutcomeAllow {
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return
		}
		authDecision = dec
	} else if h.cfg.RequireAuthentication {
		if _, ok := httpauth.PrincipalFromContext(ctx); !ok {
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return
		}
	}

	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeWireError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "unsupported_media_type", "Request Content-Type must be application/json")
		return
	}

	// 2. Reserve decode capacity before reading the body. Content-Length is
	// only an upper bound; unknown-length requests reserve the hard cap.
	maxBytes := h.cfg.MaxRequestBodyBytes
	if maxBytes <= 0 {
		maxBytes = proto.MaxRequestBytes
	}
	// Reserve the hard cap rather than trusting Content-Length: clients can
	// under-report it while the bounded reader still accepts maxBytes bytes.
	releaseDecode, admitted, admissionErr := decodeqos.TryAdmit(ctx, h.cfg.DecodeAdmission, maxBytes)
	if decision := decodeqos.Decide(admitted, admissionErr); decision.Status != 0 {
		if decision.RetryAfter {
			w.Header().Set("Retry-After", decodeqos.RetryAfterSeconds)
		}
		writeWireError(w, decision.Status, "server_error", "decode_admission_rejected", decision.Message)
		return
	}
	releaseDecodeOnce := sync.OnceFunc(func() {
		if releaseDecode != nil {
			releaseDecode()
		}
	})
	defer releaseDecodeOnce()

	bodyReader := io.LimitReader(r.Body, maxBytes+1)
	body, err := io.ReadAll(bodyReader)
	if err != nil {
		writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Failed to read request body")
		return
	}

	if int64(len(body)) > maxBytes {
		writeWireError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "body_too_large", "Request body exceeds max limit")
		return
	}

	// 3. Decode compact request payload
	opts := DecodeCompactOptions{
		DefaultRouteSelector: h.cfg.DefaultRouteSelector,
		RoutePrefixes:        h.cfg.RoutePrefixes,
		RouteSelector:        h.cfg.HTTPHeaders.RouteSelector(r.Header),
		Headers:              r.Header,
		Method:               r.Method,
		Path:                 r.URL.Path,
		RemoteAddr:           r.RemoteAddr,
		MaxBodyBytes:         maxBytes,
		Limits:               h.cfg.ProtocolLimits,
		HTTPHeaders:          h.cfg.HTTPHeaders,
	}

	var decoded *DecodedCompact
	err = decodeqos.Guard(releaseDecodeOnce, func() error {
		var decodeErr error
		decoded, decodeErr = DecodeCompactRequest(ctx, body, opts)
		if decodeErr == nil && decoded != nil && decoded.Call != nil {
			hdrs := h.cfg.HTTPHeaders.OrDefault()
			sessionwire.ApplyAuthoritativeHeadersNamed(&decoded.Call.Session, r.Header, hdrs.SessionID, hdrs.ResumeToken)
		}
		return decodeErr
	})
	releaseDecode = nil
	if err != nil {
		status := http.StatusBadRequest
		errType := "invalid_request_error"
		errCode := "bad_request"
		if errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "unauthorized") {
			status = http.StatusUnauthorized
			errType = "authentication_error"
			errCode = "unauthorized"
		}
		message := "Invalid request"
		if status == http.StatusUnauthorized {
			message = "Authentication required"
		} else if strings.Contains(err.Error(), "model is required") {
			message = "model is required for compact request"
		}
		writeWireError(w, status, errType, errCode, message)
		return
	}
	decoded.AuthDecision = authDecision

	// 4. Execute compaction through the same canonical stream port as create.
	// The executor owns candidate admission and failover; this frontend only
	// collects the resulting stream into the compact resource.
	operation, err := CompactOperationFromDecoded(decoded)
	if err != nil {
		writeWireError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "Invalid compact operation")
		return
	}
	if h.cfg.Executor == nil {
		writeWireError(w, http.StatusNotImplemented, "invalid_request_error", "operation_not_implemented", "OpenResponses compact is not enabled")
		return
	}
	stream, err := h.cfg.Executor.Execute(ctx, operation.Call)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		status, typ, code, message := classifyExecutionError(err)
		writeWireError(w, status, typ, code, message)
		return
	}
	// Enforce the allowed_tools hard constraint before compact output too.
	stream = newAllowedToolsStream(operation.Call, stream)

	clock := h.cfg.ResponseClock
	if clock == nil {
		clock = systemResponseClock{}
	}
	ids := h.cfg.CompactResourceIDSource
	if ids == nil {
		ids = systemCompactResourceIDSource{}
	}
	resource, collectErr := collectCompact(ctx, stream, compactEnvelope(decoded, ids.NewCompactResourceID(), clock.Now()), operation.Call.Options, h.cfg.ProtocolLimits)
	if collectErr != nil {
		if ctx.Err() != nil {
			return
		}
		status, typ, code, message := classifyExecutionError(collectErr)
		writeWireError(w, status, typ, code, message)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resource)
}
