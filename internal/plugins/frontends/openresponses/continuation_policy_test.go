package openresponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type continuationAuth struct{}

func (continuationAuth) Authenticate(context.Context, sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return sdkauth.Decision{
		Outcome:   sdkauth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "principal-1"},
		Scope: &scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-1"),
			TenantID:    scope.Known("tenant-1"),
			Origin:      scope.OriginClient,
		},
	}, nil
}

type scriptedContinuationExecutor struct {
	calls  []lipapi.Call
	stream func() lipapi.EventStream
}

func (e *scriptedContinuationExecutor) Execute(_ context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	e.calls = append(e.calls, lipapi.CloneCall(*call))
	return e.stream(), nil
}

type responseStream struct {
	pos int
}

func (s *responseStream) Recv(context.Context) (lipapi.Event, error) {
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "answer"},
		{Kind: lipapi.EventResponseFinished},
	}
	if s.pos >= len(events) {
		return lipapi.Event{}, io.EOF
	}
	event := events[s.pos]
	s.pos++
	return event, nil
}

func (*responseStream) Close() error { return nil }

func TestHTTPContinuationStorePolicyAndMaterialization(t *testing.T) {
	store := continuation.NewMemoryStore()
	executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    store,
	})

	first := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"first","store":true}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first request failed: %d %s", first.Code, first.Body.String())
	}
	var resource struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &resource); err != nil {
		t.Fatal(err)
	}
	if resource.ID == "" {
		t.Fatal("stored response did not receive a proxy id")
	}
	if _, err := store.Get(context.Background(), lipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}, lipcont.ResponseID(resource.ID)); err != nil {
		t.Fatalf("first response was not stored: %v", err)
	}

	second := serveContinuationRequest(t, handler, `{"previous_response_id":"`+resource.ID+`","input":"second"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("continuation request failed: %d %s", second.Code, second.Body.String())
	}
	if len(executor.calls) != 2 {
		t.Fatalf("expected two executions, got %d", len(executor.calls))
	}
	items := executor.calls[1].Items
	if len(items) != 3 || items[0].Content[0].Text != "first" || items[1].Content[0].Text != "answer" || items[2].Content[0].Text != "second" {
		t.Fatalf("unexpected materialized order: %+v", items)
	}
	if executor.calls[1].Session.ClientSessionID != "" || executor.calls[1].Session.ResumeToken != "" {
		t.Fatal("client continuation/session material leaked into executor call")
	}
	var secondResource struct {
		Previous string `json:"previous_response_id"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResource); err != nil {
		t.Fatal(err)
	}
	if secondResource.Previous != resource.ID {
		t.Fatalf("parent echo mismatch: got %q want %q", secondResource.Previous, resource.ID)
	}
}

func TestHTTPContinuationStoreFalseDoesNotReserveOrPersist(t *testing.T) {
	store := continuation.NewMemoryStore()
	executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    store,
	})
	response := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"one","store":false}`)
	if response.Code != http.StatusOK {
		t.Fatalf("request failed: %d %s", response.Code, response.Body.String())
	}
	var resource struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &resource); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), lipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}, lipcont.ResponseID(resource.ID)); err == nil {
		t.Fatal("store:false response was persisted")
	}
}

type trackingTestStore struct {
	mu           sync.Mutex
	inner        lipcont.Store
	reserveCalls int
	putCalls     int
	deleteCalls  int
	reserveErr   error
	putErr       error
	deletedIDs   []lipcont.ResponseID
}

func newTrackingTestStore(inner lipcont.Store) *trackingTestStore {
	return &trackingTestStore{inner: inner}
}

func (s *trackingTestStore) Reserve(ctx context.Context, scope lipcont.Scope, policy lipcont.StoragePolicy) (lipcont.ResponseID, error) {
	s.mu.Lock()
	s.reserveCalls++
	err := s.reserveErr
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return s.inner.Reserve(ctx, scope, policy)
}

func (s *trackingTestStore) PutTerminal(ctx context.Context, record lipcont.ContinuationRecord) error {
	s.mu.Lock()
	s.putCalls++
	err := s.putErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.PutTerminal(ctx, record)
}

func (s *trackingTestStore) Get(ctx context.Context, scope lipcont.Scope, id lipcont.ResponseID) (lipcont.ContinuationRecord, error) {
	return s.inner.Get(ctx, scope, id)
}

func (s *trackingTestStore) Delete(ctx context.Context, scope lipcont.Scope, id lipcont.ResponseID) error {
	s.mu.Lock()
	s.deleteCalls++
	s.deletedIDs = append(s.deletedIDs, id)
	s.mu.Unlock()
	return s.inner.Delete(ctx, scope, id)
}

type failingContinuationExecutor struct {
	err error
}

func (e *failingContinuationExecutor) Execute(_ context.Context, _ *lipapi.Call) (lipapi.EventStream, error) {
	return nil, e.err
}

func TestContinuation_StoreTrue(t *testing.T) {
	memStore := continuation.NewMemoryStore()
	tStore := newTrackingTestStore(memStore)
	executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    tStore,
	})

	// Non-streaming with store: true
	rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello","store":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if storeVal, ok := res["store"].(bool); !ok || !storeVal {
		t.Errorf("expected store: true in response resource, got %v", res["store"])
	}
	respID, _ := res["id"].(string)
	if respID == "" {
		t.Fatalf("missing id in response")
	}
	if tStore.reserveCalls != 1 {
		t.Errorf("expected 1 reserve call, got %d", tStore.reserveCalls)
	}
	if _, err := memStore.Get(context.Background(), lipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}, lipcont.ResponseID(respID)); err != nil {
		t.Errorf("expected record to be stored in memStore, got error: %v", err)
	}
}

func TestContinuation_StoreFalse(t *testing.T) {
	memStore := continuation.NewMemoryStore()
	tStore := newTrackingTestStore(memStore)
	executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    tStore,
	})

	rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello","store":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if storeVal, ok := res["store"].(bool); !ok || storeVal {
		t.Errorf("expected store: false in response resource, got %v", res["store"])
	}
	if tStore.reserveCalls != 0 {
		t.Errorf("expected 0 reserve calls for store:false, got %d", tStore.reserveCalls)
	}
	respID, _ := res["id"].(string)
	if _, err := memStore.Get(context.Background(), lipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}, lipcont.ResponseID(respID)); err == nil {
		t.Errorf("expected record NOT to be stored for store:false")
	}
}

func TestContinuation_StoreDefault(t *testing.T) {
	memStore := continuation.NewMemoryStore()
	tStore := newTrackingTestStore(memStore)
	executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    tStore,
	})

	// Omitted store field
	rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if storeVal, ok := res["store"].(bool); !ok || !storeVal {
		t.Errorf("expected store: true for omitted store field, got %v", res["store"])
	}
	if tStore.reserveCalls != 1 {
		t.Errorf("expected 1 reserve call for omitted store field, got %d", tStore.reserveCalls)
	}
	respID, _ := res["id"].(string)
	if _, err := memStore.Get(context.Background(), lipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}, lipcont.ResponseID(respID)); err != nil {
		t.Errorf("expected record to be stored for omitted store field, got error: %v", err)
	}
}

func TestContinuation_ReserveFailure(t *testing.T) {
	memStore := continuation.NewMemoryStore()
	tStore := newTrackingTestStore(memStore)
	tStore.reserveErr = errors.New("reserve storage failure")
	executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    tStore,
	})

	rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello","store":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 Internal Server Error, got %d: %s", rec.Code, rec.Body.String())
	}
	var errEnv map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &errEnv)
	errDetails, _ := errEnv["error"].(map[string]any)
	if errDetails["code"] != "storage_error" {
		t.Errorf("expected code storage_error, got %v", errDetails["code"])
	}
	if len(executor.calls) != 0 {
		t.Errorf("expected 0 executor calls when reserve fails, got %d", len(executor.calls))
	}
}

func TestContinuation_ParentEcho(t *testing.T) {
	memStore := continuation.NewMemoryStore()
	sc := lipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}
	parentID := seedParentRecord(t, memStore, sc, lipcont.RecordStatusCompleted)

	executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    memStore,
	})

	// Non-streaming parent echo
	rec := serveContinuationRequest(t, handler, `{"previous_response_id":"`+parentID.String()+`","input":"turn 2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if prev, _ := res["previous_response_id"].(string); prev != parentID.String() {
		t.Errorf("expected previous_response_id %q, got %q", parentID.String(), prev)
	}

	// Streaming parent echo
	streamRec := serveContinuationRequest(t, handler, `{"previous_response_id":"`+parentID.String()+`","input":"turn 2 stream","stream":true}`)
	if streamRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for stream, got %d: %s", streamRec.Code, streamRec.Body.String())
	}
	bodyStr := streamRec.Body.String()
	if !strings.Contains(bodyStr, parentID.String()) {
		t.Errorf("expected streaming SSE events to echo parent ID %q, got body:\n%s", parentID.String(), bodyStr)
	}
}

func TestContinuation_Recorder(t *testing.T) {
	memStore := continuation.NewMemoryStore()
	tStore := newTrackingTestStore(memStore)
	executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           continuationAuth{},
		Executor:             executor,
		ContinuationStore:    tStore,
	})

	rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"record me","store":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if tStore.putCalls != 1 {
		t.Errorf("expected 1 PutTerminal call on recorder completion, got %d", tStore.putCalls)
	}

	// Streaming recorder test
	tStore.putCalls = 0
	streamRec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"record me stream","store":true,"stream":true}`)
	if streamRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for stream, got %d: %s", streamRec.Code, streamRec.Body.String())
	}
	if tStore.putCalls != 1 {
		t.Errorf("expected 1 PutTerminal call on streaming recorder completion, got %d", tStore.putCalls)
	}
}

func TestContinuation_StorageFailure(t *testing.T) {
	// Subtest 1: Post-output store failure ignored
	t.Run("PostOutputFailureIgnored", func(t *testing.T) {
		memStore := continuation.NewMemoryStore()
		tStore := newTrackingTestStore(memStore)
		tStore.putErr = errors.New("post-output put terminal failure")
		executor := &scriptedContinuationExecutor{stream: func() lipapi.EventStream { return &responseStream{} }}
		handler := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           continuationAuth{},
			Executor:             executor,
			ContinuationStore:    tStore,
		})

		rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello","store":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK despite post-output storage failure, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	// Subtest 2: Pre-output executor failure cleans up reservation exactly once
	t.Run("PreOutputExecutorFailureCleanupOnce", func(t *testing.T) {
		memStore := continuation.NewMemoryStore()
		tStore := newTrackingTestStore(memStore)
		failingExec := &failingContinuationExecutor{err: errors.New("backend failed")}
		handler := openresponses.NewHandler(openresponses.HandlerConfig{
			AllowUnauthenticated: true,
			Authorizer:           continuationAuth{},
			Executor:             failingExec,
			ContinuationStore:    tStore,
		})

		rec := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"hello","store":true}`)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502 Bad Gateway, got %d: %s", rec.Code, rec.Body.String())
		}
		if tStore.deleteCalls != 1 {
			t.Errorf("expected exactly 1 Delete call for reservation cleanup, got %d", tStore.deleteCalls)
		}
	})
}

func serveContinuationRequest(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
