package openaicodex

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const wsHandshakeTimeout = 30 * time.Second

const (
	wsSessionIdleTTL    = 2 * time.Minute
	wsSessionMaxEntries = 256
)

// wsFirstEventTimeout bounds the wait for the first canonical event after the
// WebSocket handshake. Without it, a server that upgrades but never sends would
// leave openWS blocked forever on conn.ReadMessage (which ignores ctx). It is a
// package var instead of a const so internal tests can shorten it; production
// callers always see the default.
var wsFirstEventTimeout = 30 * time.Second

var errWSPreviousResponseNotFound = errors.New("websocket previous response not found")

type wsSessionKey struct {
	baseURL      string
	accountID    string
	accessToken  string
	conversation string
}

type wsSessionStore struct {
	mu         sync.Mutex
	sessions   map[wsSessionKey]*wsSessionConn
	idleTTL    time.Duration
	maxEntries int
	now        func() time.Time
}

type wsSessionConn struct {
	key       wsSessionKey
	store     *wsSessionStore
	sem       chan struct{}
	conn      *websocket.Conn
	lastUsed  time.Time
	idleTimer *time.Timer
}

func newWSSessionStore() *wsSessionStore {
	return &wsSessionStore{
		sessions:   make(map[wsSessionKey]*wsSessionConn),
		idleTTL:    wsSessionIdleTTL,
		maxEntries: wsSessionMaxEntries,
		now:        time.Now,
	}
}

func (s *wsSessionStore) acquire(ctx context.Context, client *http.Client, url string, cfg *Config, convID string) (*wsSessionConn, *http.Response, bool, error) {
	key := wsSessionKey{
		baseURL:      strings.TrimSpace(url),
		accountID:    strings.TrimSpace(cfg.AccountID),
		accessToken:  strings.TrimSpace(cfg.AccessToken),
		conversation: strings.TrimSpace(convID),
	}
	s.mu.Lock()
	session := s.sessions[key]
	if session == nil {
		session = &wsSessionConn{
			key:      key,
			store:    s,
			sem:      make(chan struct{}, 1),
			lastUsed: s.now(),
		}
		s.sessions[key] = session
		s.pruneToCapLocked(session)
	}
	session.stopIdleTimerLocked()
	s.mu.Unlock()

	if err := session.acquire(ctx); err != nil {
		return nil, nil, false, err
	}
	if session.conn != nil {
		return session, nil, true, nil
	}
	conn, resp, err := dialCodexWebSocket(ctx, client, url, cfg, convID)
	if err != nil {
		session.release(true)
		return nil, resp, false, err
	}
	session.conn = conn
	return session, resp, false, nil
}

func (s *wsSessionStore) forgetLocked(key wsSessionKey, session *wsSessionConn) {
	if s.sessions[key] == session {
		delete(s.sessions, key)
	}
}

func (s *wsSessionStore) pruneToCapLocked(protected *wsSessionConn) {
	for len(s.sessions) > s.maxEntries {
		var oldestKey wsSessionKey
		var oldest *wsSessionConn
		for key, session := range s.sessions {
			if session == protected {
				continue
			}
			if !session.tryAcquire() {
				continue
			}
			if oldest == nil || session.lastUsed.Before(oldest.lastUsed) {
				if oldest != nil {
					oldest.unlock()
				}
				oldestKey = key
				oldest = session
				continue
			}
			session.unlock()
		}
		if oldest == nil {
			return
		}
		oldest.closeConnLocked()
		s.forgetLocked(oldestKey, oldest)
		oldest.unlock()
	}
}

func (s *wsSessionStore) closeIdle(key wsSessionKey, session *wsSessionConn) {
	if !session.tryAcquire() {
		return
	}
	defer session.unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	session.closeConnLocked()
	session.stopIdleTimerLocked()
	s.forgetLocked(key, session)
}

func (s *wsSessionConn) acquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *wsSessionConn) tryAcquire() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *wsSessionConn) unlock() {
	select {
	case <-s.sem:
	default:
	}
}

func (s *wsSessionConn) release(closeConn bool) {
	if s.store == nil {
		s.unlock()
		return
	}
	s.store.mu.Lock()
	if closeConn {
		s.closeConnLocked()
		s.stopIdleTimerLocked()
		s.store.forgetLocked(s.key, s)
	} else {
		s.lastUsed = s.store.now()
		s.scheduleIdleTimerLocked()
	}
	s.store.mu.Unlock()
	s.unlock()
}

func (s *wsSessionConn) closeConnLocked() {
	if s.conn == nil {
		return
	}
	_ = s.conn.Close()
	s.conn = nil
}

func (s *wsSessionConn) stopIdleTimerLocked() {
	if s.idleTimer == nil {
		return
	}
	s.idleTimer.Stop()
	s.idleTimer = nil
}

func (s *wsSessionConn) scheduleIdleTimerLocked() {
	s.stopIdleTimerLocked()
	if s.store == nil || s.store.idleTTL <= 0 {
		return
	}
	key := s.key
	store := s.store
	s.idleTimer = time.AfterFunc(s.store.idleTTL, func() {
		store.closeIdle(key, s)
	})
}

// wsEndpoint converts an HTTPS Codex base URL into the WebSocket scheme used by
// the Codex Responses WebSocket transport. Path handling mirrors
// responsesEndpoint so the same base_url value configures both transports.
func wsEndpoint(baseURL string) string {
	base := normalizedResponsesBase(baseURL)
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	default:
		return base
	}
}

func newWSDialer(client *http.Client) *websocket.Dialer {
	d := &websocket.Dialer{HandshakeTimeout: wsHandshakeTimeout}
	if client != nil {
		if t, ok := client.Transport.(*http.Transport); ok && t != nil {
			d.Proxy = t.Proxy
			d.NetDialContext = t.DialContext
			if t.TLSClientConfig != nil {
				d.TLSClientConfig = t.TLSClientConfig.Clone()
			} else {
				d.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			}
		}
	}
	return d
}

// openWS dials the Codex Responses WebSocket, sends a response.create frame, and
// returns a managed event stream after the first canonical event is received.
// A failure before the first canonical event is returned as an error so the
// auto transport can fall back to HTTPS.
func openWS(ctx context.Context, cfg *Config, policy downgradePolicy, usageEst *usageEstimator, sessions *wsSessionStore, continuation *wsContinuationStore, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	env, err := prepareCodexOpenEnv(ctx, cfg, call, cand, policy)
	if err != nil {
		return nil, err
	}
	es, _, err := openWSPrepared(ctx, env, cfg, policy.modelForPlan(env.originalModel, cfg.PlanTypeHint), call, usageEst, sessions, continuation)
	return es, err
}

func openWSPrepared(ctx context.Context, env *codexOpenEnv, cfg *Config, model string, call lipapi.Call, usageEst *usageEstimator, sessions *wsSessionStore, continuation *wsContinuationStore) (lipapi.ManagedEventStream, *http.Response, error) {
	es, resp, rawFirst, err := openWSPreparedAttempt(ctx, env, cfg, model, call, usageEst, sessions, continuation)
	if err == nil {
		return es, resp, nil
	}
	if !isWSFreePlanRejection(rawFirst, env.downgrade, env.originalModel) {
		return nil, resp, err
	}
	es, resp, _, err = openWSPreparedAttempt(ctx, env, cfg, env.downgrade.target, call, usageEst, sessions, continuation)
	return es, resp, err
}

type wsOpenRetryDecision int

const (
	wsOpenNoRetry wsOpenRetryDecision = iota
	wsOpenRetryFreshSession
	wsOpenRetryWithoutContinuation
)

type wsOpenAttemptState struct {
	allowContinuation bool
	allowStaleRetry   bool
}

func openWSPreparedAttempt(ctx context.Context, env *codexOpenEnv, cfg *Config, model string, call lipapi.Call, usageEst *usageEstimator, sessions *wsSessionStore, continuation *wsContinuationStore) (lipapi.ManagedEventStream, *http.Response, []byte, error) {
	state := wsOpenAttemptState{
		allowContinuation: true,
		allowStaleRetry:   true,
	}
	for {
		es, resp, rawFirst, retry, err := openWSPreparedAttemptOnce(ctx, env, cfg, model, call, usageEst, sessions, continuation, state)
		switch retry {
		case wsOpenNoRetry:
			return es, resp, rawFirst, err
		case wsOpenRetryFreshSession:
			state.allowStaleRetry = false
		case wsOpenRetryWithoutContinuation:
			state.allowContinuation = false
			state.allowStaleRetry = false
		}
	}
}

func openWSPreparedAttemptOnce(ctx context.Context, env *codexOpenEnv, cfg *Config, model string, call lipapi.Call, usageEst *usageEstimator, sessions *wsSessionStore, continuation *wsContinuationStore, state wsOpenAttemptState) (lipapi.ManagedEventStream, *http.Response, []byte, wsOpenRetryDecision, error) {
	if sessions == nil {
		sessions = newWSSessionStore()
	}
	if continuation == nil {
		continuation = newWSContinuationStore(codexContinuationTTL, codexContinuationMaxEntries)
	}
	env.payload.Model = model
	fullPayload := env.payload
	fullInputFingerprints := append([]string(nil), env.inputFingerprints...)
	continuationApplied := state.allowContinuation && continuation.prepareWithFingerprints(ctx, cfg, call, &env.payload, fullInputFingerprints)
	clearPreparedContinuation := func() {
		if continuationApplied {
			continuation.invalidateWithFingerprints(cfg, call, &fullPayload, fullInputFingerprints)
		}
	}
	restoreFullPayload := func() {
		env.payload = fullPayload
	}
	frame, err := payloadToWSResponseCreate(env.payload)
	if err != nil {
		clearPreparedContinuation()
		restoreFullPayload()
		return nil, nil, nil, wsOpenNoRetry, err
	}
	session, resp, reusedSession, err := sessions.acquire(ctx, env.client, wsEndpoint(cfg.BaseURL), cfg, env.convID)
	if err != nil {
		clearPreparedContinuation()
		// Restore the full payload snapshot before returning so a rotation retry on
		// another account does not inherit this attempt's continuation-trimmed Input
		// and PreviousResponseID. The other retry paths restore below for the same
		// reason; the handshake-error path must too because it hands resp back to the
		// managed loop, which rotates accounts on 401/403/429 reusing this env.
		restoreFullPayload()
		// Return the (body-closed) handshake response so the managed WS path can
		// classify 401/403/429 handshakes and rotate to the next account.
		return nil, resp, nil, wsOpenNoRetry, err
	}
	conn := session.conn
	if err := writeWSResponseCreate(ctx, conn, frame); err != nil {
		session.release(true)
		if reusedSession && state.allowStaleRetry {
			clearPreparedContinuation()
			restoreFullPayload()
			return nil, nil, nil, wsOpenRetryFreshSession, err
		}
		clearPreparedContinuation()
		restoreFullPayload()
		return nil, nil, nil, wsOpenNoRetry, err
	}
	effectiveModel := strings.TrimSpace(env.payload.Model)
	if effectiveModel == "" {
		effectiveModel = env.originalModel
	}
	// Read the first raw frame directly so a pre-content model rejection can be
	// detected before canonical mapping: the mapper synthesizes a ResponseStarted
	// event ahead of an EventError, which would hide the rejection from a
	// first-canonical-event check.
	rawFirst, rerr := readFirstNonEmptyWSMessage(ctx, conn, wsFirstEventTimeout)
	if rerr != nil {
		session.release(true)
		if reusedSession && state.allowStaleRetry && isWSFallbackError(ctx, rerr) {
			clearPreparedContinuation()
			restoreFullPayload()
			return nil, nil, nil, wsOpenRetryFreshSession, rerr
		}
		clearPreparedContinuation()
		restoreFullPayload()
		return nil, nil, nil, wsOpenNoRetry, rerr
	}
	if isWSFreePlanRejection(rawFirst, env.downgrade, env.originalModel) {
		session.release(true)
		clearPreparedContinuation()
		restoreFullPayload()
		return nil, resp, rawFirst, wsOpenNoRetry, fmt.Errorf("%s: websocket model rejected before first event", ID)
	}
	mapper := newCodexEventMapper(call.MaxPendingWireEvents)
	if err := mapper.handleData(string(rawFirst)); err != nil {
		session.release(true)
		clearPreparedContinuation()
		restoreFullPayload()
		return nil, nil, rawFirst, wsOpenNoRetry, err
	}
	wsStream := newWSStreamWithMapper(conn, mapper)
	wsStream.release = session.release
	var managed lipapi.ManagedEventStream
	if cfg.Transport == TransportWebSocket {
		// Strict WS mode returns as soon as the first canonical event is available.
		// This mirrors the HTTPS open contract and lets the frontend stream
		// response.started immediately. Waiting here for committed output is only
		// needed in auto mode, where the transport must still be able to downgrade
		// to HTTPS before any downstream-visible content commits.
		managed, rerr = openManagedFirstEvent(ctx, wsStream, usageEst, call, effectiveModel)
	} else {
		managed, rerr = openManagedUntilCommitted(ctx, wsStream, usageEst, call, effectiveModel, wsFirstEventTimeout)
	}
	if rerr != nil {
		if continuationApplied && errors.Is(rerr, errWSPreviousResponseNotFound) {
			continuation.invalidateWithFingerprints(cfg, call, &fullPayload, fullInputFingerprints)
			restoreFullPayload()
			wsStream.releaseOnce(true)
			return nil, nil, rawFirst, wsOpenRetryWithoutContinuation, rerr
		}
		clearPreparedContinuation()
		restoreFullPayload()
		return nil, nil, rawFirst, wsOpenNoRetry, wsPreFirstEventFailure(rerr)
	}
	managed = newCodexContinuationRecordingStream(managed, cfg, call, fullPayload, fullInputFingerprints, mapper, continuation)
	// The opening boundary has been reached: strict websocket mode returns after
	// the first canonical event, while auto mode waits until output is committed
	// or terminal. Clear the deadline so subsequent streaming reads are governed
	// by caller contexts rather than the open-time fallback window.
	_ = conn.SetReadDeadline(time.Time{})
	return managed, resp, rawFirst, wsOpenNoRetry, nil
}

func openManagedUntilCommitted(ctx context.Context, es lipapi.ManagedEventStream, usageEst *usageEstimator, call lipapi.Call, model string, timeout time.Duration) (lipapi.ManagedEventStream, error) {
	managed := newUsageEstimatingStream(es, usageEst, call, model)
	recvCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		recvCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	var first []lipapi.Event
	for {
		ev, err := managed.Recv(recvCtx)
		if err != nil {
			_ = managed.Close()
			if ctx != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, newWSTransportError(err)
		}
		first = append(first, ev)
		if ev.Kind == lipapi.EventError {
			_ = managed.Close()
			if ev.ErrorCode == "previous_response_not_found" {
				return nil, errWSPreviousResponseNotFound
			}
			return nil, fmt.Errorf("%s: upstream websocket error before output: %s", ID, ev.ErrorMessage)
		}
		if wsOpenCommitted(ev) {
			return prependManagedEvents(first, managed), nil
		}
	}
}

func wsOpenCommitted(ev lipapi.Event) bool {
	return lipapi.OutputCommitted(ev) || ev.Kind == lipapi.EventError || ev.Kind == lipapi.EventResponseFinished
}

func prependManagedEvents(events []lipapi.Event, rest lipapi.ManagedEventStream) lipapi.ManagedEventStream {
	out := rest
	for i := len(events) - 1; i >= 0; i-- {
		out = streampeek.NewManagedPrependFirst(events[i], out)
	}
	return out
}

var _ lipapi.ManagedEventStream = (*codexContinuationRecordingStream)(nil)

type codexContinuationRecordingStream struct {
	inner    lipapi.ManagedEventStream
	cfg      *Config
	call     lipapi.Call
	payload  Payload
	inputFP  []string
	mapper   *codexEventMapper
	store    *wsContinuationStore
	once     sync.Once
	mu       sync.Mutex
	recorded bool
}

func newCodexContinuationRecordingStream(inner lipapi.ManagedEventStream, cfg *Config, call lipapi.Call, payload Payload, inputFingerprints []string, mapper *codexEventMapper, store *wsContinuationStore) lipapi.ManagedEventStream {
	return &codexContinuationRecordingStream{
		inner:   inner,
		cfg:     cfg,
		call:    call,
		payload: payload,
		inputFP: append([]string(nil), inputFingerprints...),
		mapper:  mapper,
		store:   store,
	}
}

func (s *codexContinuationRecordingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	if err == nil && ev.Kind == lipapi.EventResponseFinished {
		s.record()
	}
	return ev, err
}

func (s *codexContinuationRecordingStream) Close() error {
	err := s.inner.Close()
	if !s.wasRecorded() {
		s.store.invalidateWithFingerprints(s.cfg, s.call, &s.payload, s.inputFP)
	}
	return err
}

func (s *codexContinuationRecordingStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	res := s.inner.Cancel(ctx, cause)
	if !s.wasRecorded() {
		s.store.invalidateWithFingerprints(s.cfg, s.call, &s.payload, s.inputFP)
	}
	return res
}

func (s *codexContinuationRecordingStream) record() {
	s.once.Do(func() {
		if s.mapper == nil {
			return
		}
		if strings.TrimSpace(s.mapper.responseID) == "" {
			return
		}
		s.mu.Lock()
		s.recorded = true
		s.mu.Unlock()
		s.store.recordWithFingerprints(s.cfg, s.call, s.payload, s.inputFP, s.mapper.responseID, s.mapper.outputItems...)
	})
}

func (s *codexContinuationRecordingStream) wasRecorded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recorded
}

// readFirstNonEmptyWSMessage reads WebSocket text frames, skipping empty ones, until
// the first non-empty frame arrives. Pre-first-event read/close failures are wrapped
// as wsTransportError so auto mode can fall back to HTTPS. The caller sets the read
// deadline.
func readFirstNonEmptyWSMessage(ctx context.Context, conn *websocket.Conn, timeout time.Duration) ([]byte, error) {
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
	}
	stopCancel := func() bool { return true }
	if ctx != nil {
		stopCancel = context.AfterFunc(ctx, func() {
			_ = conn.SetReadDeadline(time.Now())
		})
	}
	defer stopCancel()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil, newWSTransportError(fmt.Errorf("read websocket: %w", err))
			}
			return nil, newWSTransportError(fmt.Errorf("read websocket: %w", err))
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			return data, nil
		}
	}
}

func writeWSResponseCreate(ctx context.Context, conn *websocket.Conn, frame json.RawMessage) error {
	if wsFirstEventTimeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(wsFirstEventTimeout))
	}
	stopCancel := func() bool { return true }
	if ctx != nil {
		stopCancel = context.AfterFunc(ctx, func() {
			_ = conn.SetWriteDeadline(time.Now())
		})
	}
	err := conn.WriteJSON(frame)
	stopCancel()
	_ = conn.SetWriteDeadline(time.Time{})
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return newWSTransportError(fmt.Errorf("websocket send response.create: %w", err))
}

// isWSFreePlanRejection reports whether a raw WebSocket frame is a pre-content error
// event whose message matches a free-plan gpt-5.5 rejection. Mirrors the HTTP path's
// downgradePolicy.isFreePlanRejection but operates on an error event frame instead of
// an HTTP status+body pair, since the WebSocket transport has no status code.
func isWSFreePlanRejection(rawFrame []byte, policy downgradePolicy, originalModel string) bool {
	var probe struct {
		Type  string `json:"type"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawFrame, &probe); err != nil {
		return false
	}
	if probe.Type != "error" || probe.Error == nil {
		return false
	}
	return policy.shouldReactiveRetry(originalModel, false, probe.Error.Message)
}

func dialCodexWebSocket(ctx context.Context, client *http.Client, url string, cfg *Config, convID string) (*websocket.Conn, *http.Response, error) {
	d := newWSDialer(client)
	conn, resp, err := d.DialContext(ctx, url, codexWSHeaders(*cfg, convID))
	if err != nil {
		if resp != nil {
			// Body is closed but resp.StatusCode/Header remain readable so callers
			// (e.g. managed WS rotation) can classify 401/403/429 handshakes.
			_ = resp.Body.Close()
			return nil, resp, newWSTransportError(fmt.Errorf("websocket dial: %w (status=%s)", err, resp.Status))
		}
		return nil, nil, newWSTransportError(fmt.Errorf("websocket dial: %w", err))
	}
	return conn, resp, nil
}

const wsFrameTypeResponseCreate = "response.create"

type wsResponseCreateFrame struct {
	Type string `json:"type"`
	Payload
}

// payloadToWSResponseCreate builds a WebSocket response.create frame from a Codex
// HTTPS payload: same fields with stream omitted and type set explicitly.
func payloadToWSResponseCreate(p Payload) (json.RawMessage, error) {
	p.Stream = false
	frame := wsResponseCreateFrame{
		Type:    wsFrameTypeResponseCreate,
		Payload: p,
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal ws frame: %w", ID, err)
	}
	return out, nil
}

var _ lipapi.ManagedEventStream = (*wsStream)(nil)

type wsStream struct {
	mapper       *codexEventMapper
	mu           sync.Mutex
	conn         *websocket.Conn
	closed       bool
	release      func(closeConn bool)
	releaseOnceF sync.Once
}

func newWSStream(conn *websocket.Conn, maxPending int) *wsStream {
	return newWSStreamWithMapper(conn, newCodexEventMapper(maxPending))
}

// newWSStreamWithMapper builds a wsStream over a pre-existing event mapper. The caller
// may have already populated the mapper's pending queue (e.g. from a pre-read first
// frame); the stream's Recv drains pending before reading the next wire frame.
func newWSStreamWithMapper(conn *websocket.Conn, mapper *codexEventMapper) *wsStream {
	return &wsStream{
		mapper: mapper,
		conn:   conn,
	}
}

func (s *wsStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		if ev, ok := s.mapper.pending.PopFront(); ok {
			s.mu.Unlock()
			return ev, nil
		}
		if s.mapper.terminal {
			s.mu.Unlock()
			s.releaseOnce(false)
			return lipapi.Event{}, io.EOF
		}
		s.mu.Unlock()

		text, ok, err := s.readMessage(ctx)
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return lipapi.Event{}, io.EOF
			}
			s.releaseOnce(true)
			return lipapi.Event{}, err
		}
		if !ok {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return lipapi.Event{}, io.EOF
			}
			s.releaseOnce(false)
			return lipapi.Event{}, io.EOF
		}
		if text == "" {
			continue
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		if err := s.mapper.handleData(text); err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *wsStream) readMessage(ctx context.Context) (string, bool, error) {
	stopCancel := context.AfterFunc(ctx, func() {
		_ = s.conn.SetReadDeadline(time.Now())
	})
	defer stopCancel()
	_, data, err := s.conn.ReadMessage()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = s.conn.SetReadDeadline(time.Time{})
			return "", false, ctxErr
		}
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			return "", false, io.EOF
		}
		return "", false, newWSStreamReadError(err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", true, nil
	}
	return text, true, nil
}

func (s *wsStream) Close() error {
	closeConn := true
	s.mu.Lock()
	if s.mapper.terminal {
		closeConn = false
	}
	s.mu.Unlock()
	if closeConn && s.conn != nil {
		// Close first, without taking s.mu: Recv holds that lock while blocked in
		// ReadMessage, so taking it before closing would deadlock cancellation.
		_ = s.conn.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.releaseOnce(closeConn)
	return nil
}

func (s *wsStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	// Codex WebSocket does not have a request-cancel frame in this adapter. Close
	// the socket instead of pretending cancellation is protocol-level; this also
	// prevents reuse of an in-flight session whose upstream generation may still be
	// producing frames.
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly, Err: s.Close()}
}

func (s *wsStream) releaseOnce(closeConn bool) {
	s.releaseOnceF.Do(func() {
		if s.release != nil {
			s.release(closeConn)
		}
	})
}
