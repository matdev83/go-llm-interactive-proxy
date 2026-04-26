package runtime_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutor_capabilityNegotiatePanic_excludesCandidateThenOpensNext(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(3),
		Backends: map[string]execbackend.Backend{
			"panicaps": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				ResolveCaps: func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendCaps {
					panic("negotiate caps boom")
				},
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					t.Fatal("panicaps Open must not run after capability panic")
					return nil, errors.New("unexpected")
				},
			},
			"good": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					opened = cand.Primary.Backend
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "cap-panic-failover"},
		Route:   lipapi.RouteIntent{Selector: "panicaps:m|good:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "good" {
		t.Fatalf("opened backend: %q", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
}

func TestExecutor_openPanic_preOutput_swallowedFailoverToSecondBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens []string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(5),
		Backends: map[string]execbackend.Backend{
			"badopen": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					opens = append(opens, "badopen")
					panic("open boom")
				},
			},
			"good": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					opens = append(opens, "good")
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "open-panic-failover"},
		Route:   lipapi.RouteIntent{Selector: "badopen:m|good:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if len(opens) != 2 || opens[0] != "badopen" || opens[1] != "good" {
		t.Fatalf("open sequence: %v", opens)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts: want 2 got %d %#v", len(atts), atts)
	}
	if atts[0].Outcome != lipapi.AttemptSwallowedFailure {
		t.Fatalf("attempt1 outcome: %s", atts[0].Outcome)
	}
	if atts[0].Reason != "recoverable pre-output (open)" {
		t.Fatalf("attempt1 reason: %q", atts[0].Reason)
	}
	if atts[1].Outcome != lipapi.AttemptSuccess {
		t.Fatalf("attempt2 outcome: %s", atts[1].Outcome)
	}
}

type recvPanicStream struct{}

func (recvPanicStream) Recv(context.Context) (lipapi.Event, error) {
	panic("recv boom")
}

func (recvPanicStream) Close() error { return nil }

func TestExecutor_recvPanic_preOutput_failoverToSecondBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens []string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(9),
		Backends: map[string]execbackend.Backend{
			"badrecv": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					opens = append(opens, "badrecv")
					return recvPanicStream{}, nil
				},
			},
			"good": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					opens = append(opens, "good")
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "recv-panic-failover"},
		Route:   lipapi.RouteIntent{Selector: "badrecv:m|good:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), s); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(opens) != 2 || opens[0] != "badrecv" || opens[1] != "good" {
		t.Fatalf("open sequence: %v", opens)
	}
	_ = s.Close()
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts: want 2 got %d %#v", len(atts), atts)
	}
	if atts[0].Outcome != lipapi.AttemptSwallowedFailure {
		t.Fatalf("attempt1 outcome: %s", atts[0].Outcome)
	}
	if atts[0].Reason != "recoverable pre-output (recv)" {
		t.Fatalf("attempt1 reason: %q", atts[0].Reason)
	}
	if atts[1].Outcome != lipapi.AttemptSuccess {
		t.Fatalf("attempt2 outcome: %s", atts[1].Outcome)
	}
}

type deltaThenRecvPanicStream struct {
	sent bool
}

func (d *deltaThenRecvPanicStream) Recv(context.Context) (lipapi.Event, error) {
	if !d.sent {
		d.sent = true
		return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"}, nil
	}
	panic("recv after commit")
}

func (deltaThenRecvPanicStream) Close() error { return nil }

func TestExecutor_recvPanic_postOutput_notRecoverable(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(13),
		Backends: map[string]execbackend.Backend{
			"sole": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					opens++
					return &deltaThenRecvPanicStream{}, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "recv-panic-post"},
		Route:   lipapi.RouteIntent{Selector: "sole:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opens != 1 {
		t.Fatalf("opens=%d", opens)
	}
	_, err = lipapi.Collect(context.Background(), s)
	if err == nil {
		t.Fatal("expected collect error after post-output recv panic")
	}
	if lipapi.IsRecoverablePreOutput(err) {
		t.Fatal("post-output recv panic must not be recoverable pre-output")
	}
	_ = s.Close()
}

type finishThenClosePanicStream struct {
	i int
}

func (f *finishThenClosePanicStream) Recv(context.Context) (lipapi.Event, error) {
	switch f.i {
	case 0:
		f.i++
		return lipapi.Event{Kind: lipapi.EventResponseStarted}, nil
	case 1:
		f.i++
		return lipapi.Event{Kind: lipapi.EventResponseFinished}, nil
	default:
		return lipapi.Event{}, io.EOF
	}
}

func (*finishThenClosePanicStream) Close() error {
	panic("close boom")
}

func TestExecutor_streamClosePanic_returnsNil(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(17),
		Backends: map[string]execbackend.Backend{
			"sole": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return &finishThenClosePanicStream{}, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "close-panic"},
		Route:   lipapi.RouteIntent{Selector: "sole:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), s); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestExecutor_streamClosePanic_logsIsolatedCrashDiagnosticsAtDebug(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(17),
		Log:   log,
		Backends: map[string]execbackend.Backend{
			"sole": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return &finishThenClosePanicStream{}, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "close-panic-diag"},
		Route:   lipapi.RouteIntent{Selector: "sole:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), s); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var found bool
	scan := bufio.NewScanner(&logBuf)
	for scan.Scan() {
		line := scan.Bytes()
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line: %s", err)
		}
		if m["msg"] != "isolated_panic_backend_stream_close" {
			continue
		}
		found = true
		if m["panic_boundary"] != string(safety.BoundaryBackend) {
			t.Fatalf("panic_boundary=%v want %q", m["panic_boundary"], safety.BoundaryBackend)
		}
		if m["operation"] != "backend_stream_close" {
			t.Fatalf("operation=%v want backend_stream_close", m["operation"])
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("expected isolated_panic_backend_stream_close error log, got %q", logBuf.String())
	}
	// Bounded attrs must not embed the raw panic string as a dedicated field (only stack may mention it server-side).
	if strings.Contains(logBuf.String(), `"panic_message"`) {
		t.Fatalf("unexpected panic_message field in log output")
	}
}
