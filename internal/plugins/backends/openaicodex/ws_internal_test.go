package openaicodex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestWSEndpoint_schemeAndPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in, want string
	}{
		{"https base path", "https://chatgpt.com/backend-api/codex", "wss://chatgpt.com/backend-api/codex/responses"},
		{"http localhost", "http://127.0.0.1:9/codex", "ws://127.0.0.1:9/codex/responses"},
		{"already responses path", "https://h/backend-api/codex/responses", "wss://h/backend-api/codex/responses"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := wsEndpoint(tc.in); got != tc.want {
				t.Fatalf("wsEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPayloadToWSResponseCreate_addsTypeRemovesStream(t *testing.T) {
	t.Parallel()
	p := Payload{
		Model:  "gpt-5.3-codex-spark",
		Stream: true,
		Store:  false,
		Input:  []inputItem{textMessageItem{Type: "message", Role: "user", Content: "hi"}},
	}
	raw, err := payloadToWSResponseCreate(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if typ := strings.TrimSpace(string(m["type"])); typ != `"response.create"` {
		t.Fatalf("type = %s, want response.create", typ)
	}
	if _, ok := m["stream"]; ok {
		t.Fatalf("stream must be omitted from WS frame: %s", m["stream"])
	}
	if _, ok := m["model"]; !ok {
		t.Fatalf("model must be preserved: %#v", m)
	}
}

func TestResponsesEndpoint_normalizesPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in, want string
	}{
		{"base path", "https://chatgpt.com/backend-api/codex", "https://chatgpt.com/backend-api/codex/responses"},
		{"already responses path", "https://h/backend-api/codex/responses", "https://h/backend-api/codex/responses"},
		{"trim trailing slash", "  http://127.0.0.1:9/codex/  ", "http://127.0.0.1:9/codex/responses"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := responsesEndpoint(tc.in); got != tc.want {
				t.Fatalf("responsesEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsWSFallbackError(t *testing.T) {
	t.Parallel()
	if isWSFallbackError(context.Background(), nil) {
		t.Fatal("nil error must not be fallback-eligible")
	}
	if isWSFallbackError(context.Background(), errors.New("dial failed")) {
		t.Fatal("unclassified error must not be fallback-eligible")
	}
	if !isWSFallbackError(context.Background(), newWSTransportError(errors.New("dial failed"))) {
		t.Fatal("classified transport error must be fallback-eligible")
	}
	if isWSFallbackError(context.Background(), fmt.Errorf("%s: marshal payload: %w", ID, errors.New("bad"))) {
		t.Fatal("payload marshal error must not be fallback-eligible")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if isWSFallbackError(ctx, newWSTransportError(errors.New("dial failed"))) {
		t.Fatal("cancelled context must not be treated as fallback-eligible")
	}
}

func TestWsPreFirstEventFailure(t *testing.T) {
	t.Parallel()
	if got := wsPreFirstEventFailure(nil); got != nil {
		t.Fatalf("nil = %v, want nil", got)
	}
	transport := newWSTransportError(errors.New("dial"))
	if got := wsPreFirstEventFailure(transport); got != transport {
		t.Fatal("existing transport error must pass through")
	}
	if !isWSFallbackError(context.Background(), wsPreFirstEventFailure(io.EOF)) {
		t.Fatal("pre-first-event EOF must be fallback-eligible")
	}
	readErr := newWSStreamReadError(errors.New("i/o timeout"))
	if !isWSFallbackError(context.Background(), wsPreFirstEventFailure(readErr)) {
		t.Fatal("pre-first-event ws read error must be fallback-eligible")
	}
	mapperErr := fmt.Errorf("%s: malformed stream event: %w", ID, errors.New("bad json"))
	if isWSFallbackError(context.Background(), wsPreFirstEventFailure(mapperErr)) {
		t.Fatal("mapper error must not be fallback-eligible")
	}
}

func TestTransportCooldown_markAndExpiry(t *testing.T) {
	t.Parallel()
	now := time.Time{}
	c := &transportCooldown{cooldown: 5 * time.Minute, now: func() time.Time { return now }}
	if c.active() {
		t.Fatal("cooldown must be inactive initially")
	}
	c.markFailed()
	if !c.active() {
		t.Fatal("cooldown must be active after failure")
	}
	if !c.until.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("until = %v, want %v", c.until, now.Add(5*time.Minute))
	}
	// After the window expires, auto mode may try WS again.
	c.now = func() time.Time { return now.Add(6 * time.Minute) }
	if c.active() {
		t.Fatal("cooldown must expire after the window")
	}
}

func TestTransportCooldown_zeroCooldownUsesDefault(t *testing.T) {
	t.Parallel()
	c := newTransportCooldown(0)
	if c.cooldown != DefaultWebSocketFallbackCooldown {
		t.Fatalf("cooldown = %v, want default %v", c.cooldown, DefaultWebSocketFallbackCooldown)
	}
}

func TestIsWSFreePlanRejection(t *testing.T) {
	t.Parallel()
	p := newDowngradePolicy(Config{})
	frame := []byte(`{"type":"error","error":{"message":"gpt-5.5 is not available on free plan"}}`)
	if !isWSFreePlanRejection(frame, p, "gpt-5.5") {
		t.Fatal("expected WS free-plan rejection")
	}
	if isWSFreePlanRejection(frame, p, "gpt-5.4") {
		t.Fatal("non-source model must not match")
	}
	if isWSFreePlanRejection([]byte(`{"type":"response.created"}`), p, "gpt-5.5") {
		t.Fatal("non-error frame must not match")
	}
}

func TestWSSessionStoreAcquireHonorsContextWhileCheckedOut(t *testing.T) {
	t.Parallel()
	store := newWSSessionStore()
	session := &wsSessionConn{
		key:   wsSessionKey{baseURL: "ws://example.test", accessToken: "tok", conversation: "sess"},
		store: store,
		sem:   make(chan struct{}, 1),
	}
	if err := session.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	store.sessions[session.key] = session

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	got, resp, reused, err := store.acquire(ctx, http.DefaultClient, session.key.baseURL, &Config{AccessToken: "tok"}, "sess")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire error = %v, want deadline exceeded", err)
	}
	if got != nil || resp != nil || reused {
		t.Fatalf("acquire returned session=%v resp=%v reused=%v on timeout", got, resp, reused)
	}
	session.release(true)
}

func TestWSSessionStoreIdleTimerForgetsReusableSession(t *testing.T) {
	t.Parallel()
	store := newWSSessionStore()
	store.idleTTL = 10 * time.Millisecond
	key := wsSessionKey{baseURL: "ws://example.test", accessToken: "tok", conversation: "sess"}
	session := &wsSessionConn{
		key:   key,
		store: store,
		sem:   make(chan struct{}, 1),
	}
	if err := session.acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	store.sessions[key] = session
	session.release(false)

	deadline := time.After(time.Second)
	for {
		store.mu.Lock()
		_, ok := store.sessions[key]
		store.mu.Unlock()
		if !ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("idle websocket session was not evicted")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestWSSessionStorePruneToCapEvictsOldestReusableSession(t *testing.T) {
	t.Parallel()
	store := newWSSessionStore()
	store.maxEntries = 2
	base := time.Unix(100, 0)
	oldKey := wsSessionKey{baseURL: "ws://example.test", accessToken: "tok", conversation: "old"}
	midKey := wsSessionKey{baseURL: "ws://example.test", accessToken: "tok", conversation: "mid"}
	newKey := wsSessionKey{baseURL: "ws://example.test", accessToken: "tok", conversation: "new"}
	oldSession := &wsSessionConn{key: oldKey, store: store, sem: make(chan struct{}, 1), lastUsed: base}
	midSession := &wsSessionConn{key: midKey, store: store, sem: make(chan struct{}, 1), lastUsed: base.Add(time.Second)}
	newSession := &wsSessionConn{key: newKey, store: store, sem: make(chan struct{}, 1), lastUsed: base.Add(2 * time.Second)}
	store.sessions[oldKey] = oldSession
	store.sessions[midKey] = midSession
	store.sessions[newKey] = newSession

	store.pruneToCapLocked(newSession)

	if _, ok := store.sessions[oldKey]; ok {
		t.Fatal("oldest reusable session was not evicted")
	}
	if _, ok := store.sessions[midKey]; !ok {
		t.Fatal("middle session was unexpectedly evicted")
	}
	if _, ok := store.sessions[newKey]; !ok {
		t.Fatal("protected session was unexpectedly evicted")
	}
}

func TestWSSessionStoreCloseIdleClosesOrphanedSession(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := newTestWebSocketPair(t)
	defer func() { _ = serverConn.Close() }()

	store := newWSSessionStore()
	key := wsSessionKey{baseURL: "ws://example.test", accessToken: "tok", conversation: "sess"}
	session := &wsSessionConn{
		key:   key,
		store: store,
		sem:   make(chan struct{}, 1),
		conn:  clientConn,
	}
	store.sessions[key] = &wsSessionConn{
		key:   key,
		store: store,
		sem:   make(chan struct{}, 1),
	}

	store.closeIdle(key, session)

	if session.conn != nil {
		t.Fatal("orphaned idle session conn was not closed")
	}
}

func TestWSStreamServerCloseIsNotReusable(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := newTestWebSocketPair(t)
	defer func() { _ = serverConn.Close() }()
	if err := serverConn.WriteMessage(gorillawebsocket.CloseMessage, gorillawebsocket.FormatCloseMessage(gorillawebsocket.CloseNormalClosure, "")); err != nil {
		t.Fatal(err)
	}
	var released bool
	var closeConn bool
	stream := newWSStream(clientConn, 0)
	stream.release = func(close bool) {
		released = true
		closeConn = close
	}

	_, err := stream.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Recv error = %v, want EOF", err)
	}
	if !released || !closeConn {
		t.Fatalf("release = (%v, %v), want released closeConn=true", released, closeConn)
	}
}

func TestWSStreamSkipsEmptyTextFrames(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := newTestWebSocketPair(t)
	defer func() { _ = serverConn.Close() }()
	if err := serverConn.WriteMessage(gorillawebsocket.TextMessage, []byte("   ")); err != nil {
		t.Fatal(err)
	}
	if err := serverConn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_1"}}`)); err != nil {
		t.Fatal(err)
	}
	stream := newWSStream(clientConn, 0)
	stream.release = func(bool) {}

	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != lipapi.EventResponseStarted {
		t.Fatalf("event kind = %q, want %q", ev.Kind, lipapi.EventResponseStarted)
	}
}

func TestWriteWSResponseCreateClosedConnIsFallbackEligible(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := newTestWebSocketPair(t)
	_ = serverConn.Close()
	_ = clientConn.Close()

	err := writeWSResponseCreate(context.Background(), clientConn, json.RawMessage(`{"type":"response.create"}`))
	if !isWSFallbackError(context.Background(), err) {
		t.Fatalf("write error = %v, want WS fallback-eligible", err)
	}
}

func newTestWebSocketPair(t *testing.T) (*gorillawebsocket.Conn, *gorillawebsocket.Conn) {
	t.Helper()
	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverConnCh := make(chan *gorillawebsocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- conn
	}))
	t.Cleanup(srv.Close)
	clientConn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })
	select {
	case serverConn := <-serverConnCh:
		return clientConn, serverConn
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for websocket server connection")
		return nil, nil
	}
}

func TestWSSessionStoreCloseIdleClosesTrackedArmedSession(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := newTestWebSocketPair(t)
	defer func() { _ = serverConn.Close() }()

	store := newWSSessionStore()
	key := wsSessionKey{baseURL: "ws://example.test", accessToken: "tok", conversation: "sess"}
	timer := time.AfterFunc(time.Hour, func() {})
	defer timer.Stop()
	session := &wsSessionConn{
		key:       key,
		store:     store,
		sem:       make(chan struct{}, 1),
		conn:      clientConn,
		idleTimer: timer,
	}
	store.sessions[key] = session

	store.closeIdle(key, session)

	if session.conn != nil {
		t.Fatal("tracked armed idle session conn was not closed")
	}
	if _, ok := store.sessions[key]; ok {
		t.Fatal("tracked armed idle session was not forgotten")
	}
	if session.idleTimer != nil {
		t.Fatal("tracked armed idle timer was not stopped")
	}
}

func TestWSSessionStoreCloseIdleSkipsTrackedInUseSession(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := newTestWebSocketPair(t)
	defer func() { _ = serverConn.Close() }()

	store := newWSSessionStore()
	key := wsSessionKey{baseURL: "ws://example.test", accessToken: "tok", conversation: "sess"}
	// idleTimer == nil models a concurrent acquire having stopped the timer to
	// reuse this tracked session while the stale closeIdle callback races. The
	// session is still the map entry, so the guard must skip closing it.
	session := &wsSessionConn{
		key:       key,
		store:     store,
		sem:       make(chan struct{}, 1),
		conn:      clientConn,
		idleTimer: nil,
	}
	store.sessions[key] = session

	store.closeIdle(key, session)

	if session.conn == nil {
		t.Fatal("tracked in-use session conn was closed by stale closeIdle callback")
	}
	if _, ok := store.sessions[key]; !ok {
		t.Fatal("tracked in-use session was forgotten by stale closeIdle callback")
	}
}

func TestWSStreamReleasesSessionOnMapperError(t *testing.T) {
	t.Parallel()
	clientConn, serverConn := newTestWebSocketPair(t)
	defer func() { _ = serverConn.Close() }()

	// A non-JSON text frame causes codexEventMapper.handleData to return a
	// malformed-stream-event error on the mapper-error path.
	if err := serverConn.WriteMessage(gorillawebsocket.TextMessage, []byte("not-json")); err != nil {
		t.Fatal(err)
	}

	var released bool
	var closeConn bool
	stream := newWSStream(clientConn, 0)
	stream.release = func(close bool) {
		released = true
		closeConn = close
	}

	if _, err := stream.Recv(context.Background()); err == nil {
		t.Fatal("expected Recv error for malformed frame")
	}
	if !released {
		t.Fatal("expected session released on mapper error")
	}
	if !closeConn {
		t.Fatal("expected release with closeConn=true on mapper error")
	}
}
