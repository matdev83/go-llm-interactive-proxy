package openresponses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	httpauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/transport/httpauth"
)

const (
	// wsHandshakeTimeout bounds the HTTP-to-WebSocket upgrade handshake.
	wsHandshakeTimeout = 10 * time.Second
	// wsWriteWait bounds writes to the socket (close frames, pings, error envelopes).
	wsWriteWait = 10 * time.Second
	// wsDefaultMaxMessageBytes bounds a single inbound WebSocket message. It mirrors
	// the HTTP request body limit so turn envelopes stay equally bounded.
	wsDefaultMaxMessageBytes = proto.MaxRequestBytes
	// wsReadTerminationWait bounds the handoff window between peer-close
	// signaling and publication of the read-pump result.
	wsReadTerminationWait = 10 * time.Millisecond
	// wsTransportReadLimitPadding leaves room for Gorilla to receive an
	// oversized frame and let application decoding produce a graceful 413.
	wsTransportReadLimitPadding int64 = 4096
)

var (
	errWSAgeLimit  = errors.New("openresponses: websocket connection age limit reached")
	errWSIdleClose = errors.New("openresponses: websocket peer idle")
)

const (
	wsTerminationActive uint32 = iota
	wsTerminationAge
	wsTerminationPeer
	wsTerminationShutdown
)

// WSSessionRunner processes the message stream of an established session. The
// sequential-turn runner (SessionRunner, Task 6.2) executes one accepted turn at
// a time on the session pump goroutine; a nil runner keeps the bounded keepalive
// shell alive without turn execution. HandleMessage runs on the session pump
// goroutine (the request handler goroutine). The runner must not start
// goroutines it does not own and join; returning an error terminates the
// session.
type WSSessionRunner interface {
	HandleMessage(ctx context.Context, s *WSSession, data []byte) error
}

// WSCounterSnapshot is a point-in-time view of the WebSocket transport counters.
type WSCounterSnapshot struct {
	SessionsOpened    int64
	SessionsClosed    int64
	AuthRejected      int64
	OriginRejected    int64
	HandshakeRejected int64
	MethodRejected    int64
	AgeExpired        int64
	IdleClosed        int64
}

// WSCounters tracks authenticated-upgrade and session-lifecycle outcomes. A
// counter is incremented only after a decision is made; a rejected attempt never
// allocates session state, so SessionsOpened stays at zero for every failure.
type WSCounters struct {
	sessionsOpened    atomic.Int64
	sessionsClosed    atomic.Int64
	authRejected      atomic.Int64
	originRejected    atomic.Int64
	handshakeRejected atomic.Int64
	methodRejected    atomic.Int64
	ageExpired        atomic.Int64
	idleClosed        atomic.Int64
}

// Snapshot returns a consistent point-in-time view of all counters.
func (c *WSCounters) Snapshot() WSCounterSnapshot {
	return WSCounterSnapshot{
		SessionsOpened:    c.sessionsOpened.Load(),
		SessionsClosed:    c.sessionsClosed.Load(),
		AuthRejected:      c.authRejected.Load(),
		OriginRejected:    c.originRejected.Load(),
		HandshakeRejected: c.handshakeRejected.Load(),
		MethodRejected:    c.methodRejected.Load(),
		AgeExpired:        c.ageExpired.Load(),
		IdleClosed:        c.idleClosed.Load(),
	}
}

// WebSocketHandlerConfig configures the OpenResponses WebSocket upgrade/transport handler.
type WebSocketHandlerConfig struct {
	// Authorizer is evaluated before the upgrade and before any session state is
	// allocated. When nil, the handler requires an authenticated transport context
	// unless AllowUnauthenticated is explicitly set.
	Authorizer            Authorizer
	RequireAuthentication bool
	AllowUnauthenticated  bool
	// Config is the validated frontend configuration supplying the WebSocket policy.
	Config Config
	// Runner processes established sessions (Task 6.2). A nil runner keeps the
	// bounded keepalive shell alive without turn execution.
	Runner WSSessionRunner
	// ShutdownCtx, when non-nil, is a runtime-owned context that cancels when the
	// frontend begins shutdown. Every session observes it and closes exactly once.
	ShutdownCtx context.Context
	// MaxMessageBytes overrides the default per-message bound; zero uses the default.
	MaxMessageBytes int64
	// LocalContinuation configures connection-local store:false continuation.
	// A nil value enables the profile defaults derived from Config.
	LocalContinuation *WSLocalContinuationConfig
	// WriteTextWrapper, when non-nil, wraps the socket data-frame writer for every
	// session this handler establishes. next writes one text frame to the socket.
	// A wrapper may count frames and return an error on the Nth write to force a
	// deterministic writer failure; production leaves it nil.
	WriteTextWrapper func(next func(data []byte) error) func(data []byte) error
}

// WebSocketHandler serves the OpenResponses `GET <base_path>/responses` upgrade.
type WebSocketHandler struct {
	auth                  Authorizer
	requireAuthentication bool
	config                Config
	runner                WSSessionRunner
	shutdown              context.Context
	bounds                wsBounds
	counters              *WSCounters
	localCont             WSLocalContinuationConfig
	writeText             func(func([]byte) error) func([]byte) error
	upgrader              websocket.Upgrader
}

// NewWebSocketHandler creates a bounded, strict WebSocket upgrade handler.
func NewWebSocketHandler(cfg WebSocketHandlerConfig) *WebSocketHandler {
	bounds := wsBoundsFromConfig(cfg.Config.WebSocket)
	if cfg.MaxMessageBytes > 0 {
		bounds.maxMessageBytes = cfg.MaxMessageBytes
	}
	// Keep the application limit representable by the bounded queue. The
	// transport accepts an additional padding window so the decoder can emit a
	// graceful 413, but no accepted frame may exceed the queue ceiling.
	maxMessageBytes := wsMaxMessageBytes()
	if bounds.maxMessageBytes > maxMessageBytes {
		bounds.maxMessageBytes = maxMessageBytes
	}
	if bounds.maxQueuedBytes < wsMinimumQueuedBytes(bounds.maxMessageBytes) {
		bounds.maxQueuedBytes = wsMinimumQueuedBytes(bounds.maxMessageBytes)
	}
	if bounds.maxQueuedBytes > MaxAllowedQueuedBytes {
		bounds.maxQueuedBytes = MaxAllowedQueuedBytes
	}
	localCont := cfg.LocalContinuation
	if localCont == nil {
		derived := DefaultWSLocalContinuation(cfg.Config)
		localCont = &derived
	}
	if !cfg.AllowUnauthenticated {
		cfg.RequireAuthentication = true
	}
	return &WebSocketHandler{
		auth:                  cfg.Authorizer,
		requireAuthentication: cfg.RequireAuthentication,
		config:                cfg.Config,
		runner:                cfg.Runner,
		shutdown:              cfg.ShutdownCtx,
		bounds:                bounds,
		counters:              &WSCounters{},
		localCont:             *localCont,
		writeText:             cfg.WriteTextWrapper,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: wsHandshakeTimeout,
			Error: func(w http.ResponseWriter, _ *http.Request, status int, _ error) {
				// Gorilla's default hook writes plain text. Keep handshake failures
				// on the canonical OpenResponses error wire shape instead.
				if status < 400 {
					status = http.StatusBadRequest
				}
				writeWireError(w, status, "invalid_request_error", "invalid_upgrade", "Invalid WebSocket upgrade request")
			},
			// Origin is enforced as an explicit allowlist policy in ServeHTTP before
			// the upgrade; this mirrors the same policy as a second line of defense.
			CheckOrigin: func(r *http.Request) bool {
				allowed, _ := cfg.Config.WebSocket.originAllowed(strings.TrimSpace(r.Header.Get("Origin")))
				return allowed
			},
		},
	}
}

// Counters returns the transport counters for this handler.
func (h *WebSocketHandler) Counters() *WSCounters {
	return h.counters
}

// buildLocalStore constructs the connection-local continuation store for a scope,
// honoring an injected factory (tests) or the default bounded memory store.
func (h *WebSocketHandler) buildLocalStore(scope lipcont.Scope) lipcont.Store {
	if h.localCont.StoreFactory != nil {
		return h.localCont.StoreFactory(scope)
	}
	return newWSLocalStore(scope, h.localCont.Limits)
}

// ServeHTTP performs strict validation, authentication, and origin checks before
// any upgrade or session-state allocation. Session state is created only after
// auth, origin, and a valid handshake all succeed.
func (h *WebSocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodGet {
		h.counters.methodRejected.Add(1)
		writeWireError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "WebSocket upgrades require a GET request")
		return
	}

	var authDecision sdkauth.Decision
	if h.auth != nil {
		meta := sdkauth.InboundCallMeta{
			Frontend:            ID,
			Method:              r.Method,
			Path:                r.URL.Path,
			ClientAddr:          r.RemoteAddr,
			AuthorizationBearer: extractWebSocketBearerToken(r.Header),
		}
		dec, err := h.auth.Authenticate(ctx, meta)
		if err != nil || dec.Outcome != sdkauth.OutcomeAllow {
			h.counters.authRejected.Add(1)
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return
		}
		authDecision = dec
	} else if h.requireAuthentication {
		if _, ok := httpauth.PrincipalFromContext(ctx); !ok {
			h.counters.authRejected.Add(1)
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return
		}
	}

	originAllowed, normalizedOrigin := h.config.WebSocket.originAllowed(strings.TrimSpace(r.Header.Get("Origin")))
	if !originAllowed {
		h.counters.originRejected.Add(1)
		writeWireError(w, http.StatusForbidden, "permission_error", "origin_not_allowed", "Request origin is not allowed")
		return
	}

	if err := validateUpgradeRequest(r); err != nil {
		h.counters.handshakeRejected.Add(1)
		writeWireError(w, http.StatusBadRequest, "invalid_request_error", "invalid_upgrade", "Invalid WebSocket upgrade request")
		return
	}

	upgrader := h.upgrader
	if selected := websocketBearerSubprotocol(r.Header); selected != "" {
		// Use a per-request copy: Upgrader carries mutable negotiation fields and
		// the handler is safe for concurrent upgrades.
		upgrader.Subprotocols = []string{selected}
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.counters.handshakeRejected.Add(1)
		return
	}

	// Authentication, origin policy, and handshake validation all passed: only now
	// allocate the bounded session shell and its connection-local continuation state.
	h.counters.sessionsOpened.Add(1)
	session := newWSSession(conn, h.bounds, h.counters, authDecision, normalizedOrigin, h.writeText)
	if h.localCont.Enabled {
		connectionID := newWSConnectionID()
		scope := wsContinuationScope(authDecision, connectionID)
		session.SetLocalContinuation(h.buildLocalStore(scope), scope)
	}

	runCtx := ctx
	if h.shutdown != nil {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(h.shutdown, cancel)
		defer stop()
	}

	// Teardown must run even when a downstream runner/executor panics. Let the
	// enclosing HTTP server recover the panic after the socket and telemetry have
	// been handled exactly once.
	defer func() {
		_ = session.Close()
		h.counters.sessionsClosed.Add(1)
	}()
	_ = session.Run(runCtx, h.runner)
}

// validateUpgradeRequest checks the WebSocket handshake fields before any upgrade.
// Sec-WebSocket-Protocol is intentionally not rejected: browser clients cannot
// set Authorization headers and may carry bearer credentials in a protocol
// token. Unknown protocols are ignored rather than treated as an authorization
// or transport failure.
const wsBearerAuthorizationSubprotocol = "base64url.bearer.authorization.lip"

func websocketSubprotocols(h http.Header) []string {
	var protocols []string
	for _, value := range h.Values("Sec-WebSocket-Protocol") {
		for _, part := range strings.Split(value, ",") {
			if token := strings.TrimSpace(part); token != "" {
				protocols = append(protocols, token)
			}
		}
	}
	return protocols
}

// websocketBearerSubprotocol returns the safe marker to negotiate for the
// two-token browser form. The encoded credential is intentionally not selected
// or echoed in the 101 response.
func websocketBearerSubprotocol(h http.Header) string {
	protocols := websocketSubprotocols(h)
	for i, protocol := range protocols {
		if protocol == wsBearerAuthorizationSubprotocol && i+1 < len(protocols) {
			return protocol
		}
	}
	return ""
}

// extractWebSocketBearerToken recognizes the opt-in browser authorization
// subprotocol without treating arbitrary subprotocol names as credentials. The
// supported form is two tokens: marker followed by a base64url-encoded bearer
// token. Invalid encodings are ignored and fall back to the normal
// Authorization header path.
func extractWebSocketBearerToken(h http.Header) string {
	if token := extractBearerToken(h); token != "" {
		return token
	}
	if h == nil {
		return ""
	}
	protocols := websocketSubprotocols(h)
	for i, protocol := range protocols {
		if protocol != wsBearerAuthorizationSubprotocol || i+1 >= len(protocols) {
			continue
		}
		encoded := protocols[i+1]
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || strings.ContainsAny(string(decoded), "\r\n\x00") {
			continue
		}
		return string(decoded)
	}
	return ""
}

func validateUpgradeRequest(r *http.Request) error {
	if !headerContainsToken(r.Header, "Connection", "upgrade") {
		return errors.New("missing Connection: Upgrade")
	}
	if !headerContainsToken(r.Header, "Upgrade", "websocket") {
		return errors.New("missing Upgrade: websocket")
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")), "13") {
		return errors.New("unsupported Sec-WebSocket-Version")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key")) == "" {
		return errors.New("missing Sec-WebSocket-Key")
	}
	return nil
}

func headerContainsToken(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// originAllowed implements the strict Origin policy from configuration. Browser
// origins are accepted only when explicitly allowlisted; relaxing browser-origin
// behavior therefore requires explicit configuration. Requests without an Origin
// header (non-browser clients) are always accepted.
//
// Development-only relaxation: when both development_mode and allow_any_origin
// are set, any syntactically valid HTTP(S) origin is accepted (an empty
// allowlist then accepts browser origins). The policy never relaxes unless both
// flags are set, so a config that bypasses validation cannot open the policy.
func (c WebSocketConfig) originAllowed(headerOrigin string) (allowed bool, normalized string) {
	raw := strings.TrimSpace(headerOrigin)
	if raw == "" {
		return true, ""
	}
	normalized, err := normalizeOrigin(raw)
	if err != nil {
		return false, ""
	}
	if c.AllowAnyOrigin && c.DevelopmentMode {
		return true, normalized
	}
	for _, allowedOrigin := range c.AllowedOrigins {
		if normalized == allowedOrigin {
			return true, normalized
		}
	}
	return false, normalized
}

type wsBounds struct {
	maxAge          time.Duration
	idleTimeout     time.Duration
	maxMessageBytes int64
	maxQueuedTurns  int
	maxQueuedBytes  int64
}

func wsMaxMessageBytes() int64 {
	if MaxAllowedQueuedBytes <= wsTransportReadLimitPadding {
		return MaxAllowedQueuedBytes
	}
	return MaxAllowedQueuedBytes - wsTransportReadLimitPadding
}

func wsMinimumQueuedBytes(maxMessageBytes int64) int64 {
	if maxMessageBytes > (1<<63-1)-wsTransportReadLimitPadding {
		return maxMessageBytes
	}
	return maxMessageBytes + wsTransportReadLimitPadding
}

func wsBoundsFromConfig(c WebSocketConfig) wsBounds {
	maxAge, err := time.ParseDuration(c.MaxConnectionAge)
	if err != nil || maxAge <= 0 || maxAge > MaxAllowedWSConnectionAgeDur {
		maxAge = MaxAllowedWSConnectionAgeDur
	}
	idle, err := time.ParseDuration(c.IdleTimeout)
	if err != nil || idle <= 0 {
		idle = 5 * time.Minute
	}
	queued := c.MaxQueuedTurns
	if queued < 1 {
		queued = 1
	}
	queuedBytes := c.MaxQueuedBytes
	if queuedBytes < 1 {
		queuedBytes = DefaultMaxQueuedBytes
	}
	if queuedBytes > MaxAllowedQueuedBytes {
		queuedBytes = MaxAllowedQueuedBytes
	}
	if queuedBytes < wsMinimumQueuedBytes(wsDefaultMaxMessageBytes) {
		queuedBytes = wsMinimumQueuedBytes(wsDefaultMaxMessageBytes)
	}
	return wsBounds{
		maxAge:          maxAge,
		idleTimeout:     idle,
		maxMessageBytes: wsDefaultMaxMessageBytes,
		maxQueuedTurns:  queued,
		maxQueuedBytes:  queuedBytes,
	}
}

// wsWireErrorEnvelope is the pinned WebSocket error envelope shape.
type wsWireErrorEnvelope struct {
	Type   string          `json:"type"`
	Status int             `json:"status"`
	Error  wsWireErrorBody `json:"error"`
}

type wsWireErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
}

// WSSession is the bounded connection shell established by an authenticated
// handshake. It owns the socket, its limits, its deadlines, and closes it exactly
// once. Run drives one read pump and one pinger goroutine; both are owned and
// joined before Run returns, so no goroutine escapes the session.
type WSSession struct {
	conn      *websocket.Conn
	bounds    wsBounds
	counters  *WSCounters
	auth      sdkauth.Decision
	origin    string
	startedAt time.Time

	// byteBudget gates the total turn payload buffered in the session queue
	// (finding 4). It is created with the session, owned by Run, and closed
	// before Run joins the pumps.
	byteBudget *wsByteBudget

	// localStore and localScope are the connection-local store:false continuation
	// state allocated only after authorization. They are cleared when the session
	// closes, so reconnects never see a previous connection's records.
	localStore lipcont.Store
	localScope lipcont.Scope

	// peerClosedCh is closed exactly once when a pump observes a fatal transport
	// error (dead peer or failed control write). In-flight turn execution
	// observes it through PeerClosed so a disconnected client cancels a blocked
	// downstream stream promptly instead of hanging until the age limit.
	peerClosedCh   chan struct{}
	termination    atomic.Uint32
	peerClosedOnce sync.Once

	// writeText is the effective data-frame writer: the socket writer, optionally
	// wrapped by the handler's WriteTextWrapper seam (tests). A nil wrapper keeps
	// the plain socket writer.
	writeText func([]byte) error

	closeOnce sync.Once
	closeErr  error
}

func newWSSession(conn *websocket.Conn, b wsBounds, counters *WSCounters, auth sdkauth.Decision, origin string, wrap func(func([]byte) error) func([]byte) error) *WSSession {
	next := func(data []byte) error {
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		err := conn.WriteMessage(websocket.TextMessage, data)
		_ = conn.SetWriteDeadline(time.Time{})
		return err
	}
	if wrap != nil {
		next = wrap(next)
	}
	return &WSSession{
		conn:         conn,
		bounds:       b,
		counters:     counters,
		auth:         auth,
		origin:       origin,
		startedAt:    time.Now(),
		peerClosedCh: make(chan struct{}),
		byteBudget:   newWSByteBudget(b.maxQueuedBytes),
		writeText:    next,
	}
}

// PeerClosed returns a channel that is closed when the peer or transport fails.
// Turn runners select on it (directly or through a derived context) to propagate
// disconnects into in-flight executor reads.
func (s *WSSession) PeerClosed() <-chan struct{} {
	return s.peerClosedCh
}

func (s *WSSession) markPeerClosed() {
	s.peerClosedOnce.Do(func() {
		// The terminal arbiter gives peer closure a single linearization point.
		// If age already claimed termination, the already-selected age result
		// remains authoritative; otherwise peer closure wins classification.
		s.termination.CompareAndSwap(wsTerminationActive, wsTerminationPeer)
		close(s.peerClosedCh)
	})
}

func (s *WSSession) claimAgeTermination() bool {
	return s.termination.CompareAndSwap(wsTerminationActive, wsTerminationAge)
}

func (s *WSSession) claimShutdown() bool {
	return s.termination.CompareAndSwap(wsTerminationActive, wsTerminationShutdown)
}

// Auth returns the authenticated decision scoped to this session.
func (s *WSSession) Auth() sdkauth.Decision { return s.auth }

// Origin returns the normalized request Origin, or "" when the client sent none.
func (s *WSSession) Origin() string { return s.origin }

// StartedAt returns the session start time.
func (s *WSSession) StartedAt() time.Time { return s.startedAt }

// LocalStore returns the connection-local continuation store, or nil when local
// continuation is disabled for this session.
func (s *WSSession) LocalStore() lipcont.Store { return s.localStore }

// ContinuationScope returns the authoritative connection scope isolating this
// session's local continuation records.
func (s *WSSession) ContinuationScope() lipcont.Scope { return s.localScope }

// SetLocalContinuation attaches the connection-scoped continuation store. It is
// called once, after a successful upgrade and authorization.
func (s *WSSession) SetLocalContinuation(store lipcont.Store, scope lipcont.Scope) {
	s.localStore = store
	s.localScope = scope
}

// WriteText writes one bounded text frame to the client. Data writes are owned
// by the session pump goroutine (the request handler goroutine); control frames
// (pings, pongs, close) are safe from any goroutine.
func (s *WSSession) WriteText(data []byte) error {
	if s.writeText == nil {
		return errors.New("openresponses: session data writer is unavailable")
	}
	return s.writeText(data)
}

// WriteJSON writes one bounded JSON text frame to the client. See WriteText for
// the single-data-writer ownership invariant.
func (s *WSSession) WriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.WriteText(data)
}

type sessionPumpResult struct {
	err      error
	fromRead bool
}

// Run starts the bounded session pumps and blocks until the session terminates:
// on context cancellation, connection-age expiry, peer close/error, or a runner
// error. It owns both pump goroutines and joins them before returning. The read
// pump forwards text messages to the bounded queue; the pinger emits periodic
// pings so a silent-but-live peer is kept alive and a dead peer is detected
// within the idle window.
func (s *WSSession) Run(ctx context.Context, runner WSSessionRunner) error {
	queueCap := s.bounds.maxQueuedTurns
	if queueCap < 1 {
		queueCap = 1
	}
	messageCh := make(chan []byte, queueCap)
	doneCh := make(chan sessionPumpResult, 2)
	stop := make(chan struct{})
	budget := s.byteBudget

	wg := &sync.WaitGroup{}
	wg.Add(2)
	go s.readPump(ctx, messageCh, doneCh, stop, budget, wg)
	go s.pinger(ctx, doneCh, stop, wg)

	// The connection-age deadline must also cancel an in-flight turn: Run is
	// blocked inside HandleMessage while the turn executes, so the age case
	// alone cannot terminate a session whose downstream turn is blocked and
	// whose peer stays connected. The AfterFunc callback is the sole owner of
	// the age result; its completion channel lets cleanup join it without
	// concurrently draining a timer channel.
	ageDur := time.Until(s.startedAt.Add(s.bounds.maxAge))
	turnCtx, cancelTurn := context.WithCancel(ctx)
	ageExpired := make(chan struct{})
	ageTimerDone := make(chan struct{})
	ageTimer := time.AfterFunc(ageDur, func() {
		close(ageExpired)
		cancelTurn()
		close(ageTimerDone)
	})

	peerWatcherDone := make(chan struct{})
	go func() {
		defer close(peerWatcherDone)
		select {
		case <-s.PeerClosed():
			cancelTurn()
		case <-turnCtx.Done():
		}
	}()
	shutdownWatcherStop := make(chan struct{})
	shutdownWatcherDone := make(chan struct{})
	go func() {
		defer close(shutdownWatcherDone)
		select {
		case <-ctx.Done():
			s.claimShutdown()
		case <-shutdownWatcherStop:
		}
	}()
	defer func() {
		cancelTurn()
		if !ageTimer.Stop() {
			<-ageTimerDone
		}
		close(shutdownWatcherStop)
		<-peerWatcherDone
		<-shutdownWatcherDone
	}()
	// Run owns pump teardown, including panic paths from the turn runner. The
	// HTTP handler's Close defer is a second idempotent safety net, not the owner
	// of joining these goroutines.
	defer func() {
		budget.close()
		_ = s.close()
		close(stop)
		wg.Wait()
	}()

	var result error
loop:
	for {
		select {
		case <-ctx.Done():
			s.claimShutdown()
			result = ctx.Err()
			break loop
		case <-ageExpired:
			// Shutdown wins over age. Otherwise age must claim the terminal
			// classification atomically; a peer that claimed it first wins,
			// without a timing-based check-then-poll window.
			if ctx.Err() != nil {
				s.claimShutdown()
				result = ctx.Err()
				break loop
			}
			if !s.claimAgeTermination() {
				if s.termination.Load() == wsTerminationShutdown {
					result = ctx.Err()
				} else if term, ok := s.peerTermination(doneCh); ok {
					if term.fromRead && isReadTimeout(term.err) && s.counters != nil {
						s.counters.idleClosed.Add(1)
					}
					result = term.err
				} else {
					result = errWSIdleClose
				}
				break loop
			}
			if s.counters != nil {
				s.counters.ageExpired.Add(1)
			}
			_ = s.sendLimitReachedError()
			result = errWSAgeLimit
			break loop
		case r := <-doneCh:
			if r.fromRead && isReadTimeout(r.err) {
				if s.counters != nil {
					s.counters.idleClosed.Add(1)
				}
			}
			result = r.err
			break loop
		case msg := <-messageCh:
			if runner != nil {
				if err := runner.HandleMessage(turnCtx, s, msg); err != nil {
					// A peer-idle read timeout may have fired while the turn was
					// in flight; it takes precedence so the session classifies
					// the closure as an idle close (finding 5).
					if term, ok := pollReadTermination(doneCh, s.PeerClosed()); ok {
						if term.fromRead && isReadTimeout(term.err) {
							if s.counters != nil {
								s.counters.idleClosed.Add(1)
							}
						}
						result = term.err
						break loop
					}
					ageExpiredWhileTurn := false
					select {
					case <-ageExpired:
						ageExpiredWhileTurn = true
					default:
					}
					if ageExpiredWhileTurn && ctx.Err() == nil {
						if !s.claimAgeTermination() {
							if s.termination.Load() == wsTerminationShutdown {
								result = ctx.Err()
							} else if term, ok := s.peerTermination(doneCh); ok {
								if term.fromRead && isReadTimeout(term.err) && s.counters != nil {
									s.counters.idleClosed.Add(1)
								}
								result = term.err
							} else {
								result = errWSIdleClose
							}
						} else {
							// The connection age limit expired while a turn was in
							// flight; classify it as the bounded age termination and
							// emit the limit-reached envelope before the close.
							if s.counters != nil {
								s.counters.ageExpired.Add(1)
							}
							_ = s.sendLimitReachedError()
							result = errWSAgeLimit
						}
					} else {
						result = err
					}
					break loop
				}
			}
			budget.release(int64(len(msg)))
		}
	}

	return result
}

// peerTermination returns a peer-associated pump result without using a timing
// guess to arbitrate against age. markPeerClosed claims the peer terminal state
// before closing the notification channel, so the atomic termination state is
// the causal source of truth. The bounded poll remains only for collecting the
// read-pump error after that state has already been published.
func (s *WSSession) peerTermination(doneCh <-chan sessionPumpResult) (sessionPumpResult, bool) {
	if s.termination.Load() != wsTerminationPeer {
		return sessionPumpResult{}, false
	}
	if term, ok := pollReadTermination(doneCh, s.PeerClosed()); ok {
		return term, true
	}
	return sessionPumpResult{err: errWSIdleClose, fromRead: true}, true
}

// pollReadTermination drains any pump terminations that have already arrived
// while the runner was executing a turn, returning the first one and preferring
// a peer-idle read timeout so the session classifies such a close as idle.
func pollReadTermination(doneCh <-chan sessionPumpResult, peerClosed <-chan struct{}) (sessionPumpResult, bool) {
	var first sessionPumpResult
	found := false
	deadline := time.NewTimer(wsReadTerminationWait)
	defer deadline.Stop()

	for {
		select {
		case r := <-doneCh:
			if r.fromRead && isReadTimeout(r.err) {
				return r, true
			}
			if found {
				return first, true
			}
			first = r
			found = true
		case <-peerClosed:
			// readPump signals peerClosed before publishing its result. Disable
			// this case after observing it and continue waiting for the result.
			peerClosed = nil
		case <-deadline.C:
			if found {
				return first, true
			}
			return sessionPumpResult{}, false
		}
	}
}

// readPump is the bounded reader. It enforces the per-message read limit and the
// idle window via read deadlines refreshed by pong/ping activity, and forwards
// text messages to the bounded queue. Each forwarded message first reserves its
// byte size in the session's byte budget, so a burst of large turns cannot make
// the session buffer more than maxQueuedBytes of queued payload (finding 4).
func (s *WSSession) readPump(ctx context.Context, messageCh chan<- []byte, doneCh chan<- sessionPumpResult, stop <-chan struct{}, budget *wsByteBudget, wg *sync.WaitGroup) {
	defer wg.Done()
	conn := s.conn
	readLimit := s.bounds.maxMessageBytes
	if readLimit <= (1<<63-1)-wsTransportReadLimitPadding {
		readLimit += wsTransportReadLimitPadding
	}
	conn.SetReadLimit(readLimit)
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(s.bounds.idleTimeout))
		return nil
	})
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(s.bounds.idleTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(wsWriteWait))
	})

	for {
		if err := conn.SetReadDeadline(time.Now().Add(s.bounds.idleTimeout)); err != nil {
			s.markPeerClosed()
			doneCh <- sessionPumpResult{err: err, fromRead: true}
			return
		}
		mt, data, err := conn.ReadMessage()
		if err != nil {
			s.markPeerClosed()
			doneCh <- sessionPumpResult{err: err, fromRead: true}
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		size := int64(len(data))
		if !budget.reserve(size) {
			return
		}
		select {
		case messageCh <- data:
		case <-ctx.Done():
			budget.release(size)
			doneCh <- sessionPumpResult{err: ctx.Err(), fromRead: true}
			return
		case <-stop:
			budget.release(size)
			return
		}
	}
}

// pinger emits periodic pings so liveness is observable while the reader is idle.
func (s *WSSession) pinger(ctx context.Context, doneCh chan<- sessionPumpResult, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	period := s.bounds.idleTimeout * 9 / 10
	if period < time.Millisecond {
		period = time.Millisecond
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
				s.markPeerClosed()
				doneCh <- sessionPumpResult{err: err}
				return
			}
		case <-ctx.Done():
			doneCh <- sessionPumpResult{err: ctx.Err()}
			return
		case <-stop:
			return
		}
	}
}

// Close closes the socket exactly once, sending a normal close frame first.
func (s *WSSession) Close() error {
	return s.close()
}

func (s *WSSession) close() error {
	s.closeOnce.Do(func() {
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(wsWriteWait))
		s.closeErr = s.conn.Close()
		if s.localStore != nil {
			if closer, ok := s.localStore.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	})
	return s.closeErr
}

func (s *WSSession) sendLimitReachedError() error {
	env := wsWireErrorEnvelope{
		Type:   "error",
		Status: http.StatusBadRequest,
		Error: wsWireErrorBody{
			Code:    "websocket_connection_limit_reached",
			Message: "WebSocket connection age limit reached; open a new connection",
			Param:   "connection_age",
		},
	}
	return s.WriteJSON(env)
}

func isReadTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
