package runtime_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutor_AlegCancellationCancelsManagedBLegBeforeClose(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inner := &managedBlockingStream{ready: make(chan struct{})}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"managed": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return inner, nil
				},
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "managed-cancel"},
		Route:   lipapi.RouteIntent{Selector: "managed:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stream.Recv(ctx)
		done <- err
	}()
	<-inner.ready
	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv err = %v want context.Canceled", err)
	}
	if got, want := inner.calls(), []string{"cancel:context_done", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed B-leg calls = %v want %v", got, want)
	}
}

func TestExecutor_AlegCancellationRecordsEstimatedBillingMarker(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &billingMarkerRecorder{}
	ex := &runtime.Executor{
		Store:                 st,
		Bus:                   hooks.New(hooks.Config{}),
		Rand:                  routing.NewSeededRng(1),
		SecureSessionRecorder: rec,
		Backends: map[string]execbackend.Backend{
			"managed": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return &managedBlockingStream{ready: make(chan struct{})}, nil
				},
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "managed-billing-cancel"},
		Route:   lipapi.RouteIntent{Selector: "managed:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv err = %v want context.Canceled", err)
	}

	got, ok := rec.usage()
	if !ok {
		t.Fatal("expected cancellation billing marker")
	}
	if got.EventKind != string(lipapi.EventUsageDelta) || !got.IsUsageEvent {
		t.Fatalf("unexpected marker shape: %+v", got)
	}
	if got.CostSource != "estimated" || !got.BillingUnavailable {
		t.Fatalf("expected estimated unavailable billing marker, got %+v", got)
	}
	if got.BLegID == "" || got.RawUsageJSON == "" {
		t.Fatalf("expected b-leg id and raw usage basis, got %+v", got)
	}
	if rec.usageCount() != 1 {
		t.Fatalf("usage events = %d want 1", rec.usageCount())
	}
}

func TestExecutor_AlegCancellationPersistsAuthoritativeFinalBilling(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &billingMarkerRecorder{}
	var finalIn execbackend.BillingFinalizationInput
	ex := &runtime.Executor{
		Store:                 st,
		Bus:                   hooks.New(hooks.Config{}),
		Rand:                  routing.NewSeededRng(1),
		SecureSessionRecorder: rec,
		Backends: map[string]execbackend.Backend{
			"managed": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return &managedBlockingStream{ready: make(chan struct{})}, nil
				},
				FinalizeBilling: func(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
					if err := ctx.Err(); err != nil {
						return lipapi.Event{}, err
					}
					finalIn = in
					return lipapi.Event{
						Kind:         lipapi.EventUsageDelta,
						InputTokens:  2,
						OutputTokens: 3,
						TotalTokens:  5,
						CostSource:   "provider_reported",
						RawUsageJSON: `{"provider":"final"}`,
					}, nil
				},
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "managed-final-billing-cancel"},
		Route:   lipapi.RouteIntent{Selector: "managed:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv err = %v want context.Canceled", err)
	}

	if finalIn.ALegID != call.Session.ALegID || finalIn.BLegID == "" || finalIn.Reason != "context canceled" {
		t.Fatalf("finalizer input = %+v, call A-leg=%q", finalIn, call.Session.ALegID)
	}
	got, ok := rec.usage()
	if !ok {
		t.Fatal("expected final billing usage to be persisted")
	}
	if got.CostSource != "provider_reported" || got.InputTokens != 2 || got.OutputTokens != 3 || got.TotalTokens != 5 {
		t.Fatalf("expected authoritative final usage, got %+v", got)
	}
	if got.RawUsageJSON != `{"provider":"final"}` {
		t.Fatalf("raw usage = %q", got.RawUsageJSON)
	}
	if rec.usageCount() != 1 {
		t.Fatalf("usage events = %d want 1; cancellation must not emit estimate plus final billing", rec.usageCount())
	}
}

func TestExecutor_AlegCancellationBlocksRecvPhaseReplacementBeforeOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lc := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	var opens atomic.Int32
	ex := &runtime.Executor{
		Store:         st,
		Bus:           hooks.New(hooks.Config{}),
		Rand:          routing.NewSeededRng(1),
		ALegLifecycle: lc,
		Backends: map[string]execbackend.Backend{
			"bad": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return &recoverableBeforeOutputStream{}, nil
				},
			},
			"waste": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "cancel-before-replacement"},
		Route:   lipapi.RouteIntent{Selector: "bad:m|waste:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if err := lc.CancelALeg(context.Background(), call.Session.ALegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv(context.Background())
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("Recv err = %v want ErrALegCanceled", err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("opens = %d want 1; replacement opened after A-leg cancellation", got)
	}
}

func TestExecutor_RegisterBLegFailureDoesNotDoubleCancelStream(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lc := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	inner := &managedBlockingStream{ready: make(chan struct{})}
	ex := &runtime.Executor{
		Store:         st,
		Bus:           hooks.New(hooks.Config{}),
		Rand:          routing.NewSeededRng(1),
		ALegLifecycle: lc,
		Backends: map[string]execbackend.Backend{
			"managed": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					if call.Session.ALegID == "" {
						return nil, errors.New("missing a-leg id")
					}
					if err := lc.CancelALeg(ctx, call.Session.ALegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
						return nil, err
					}
					return inner, nil
				},
			},
		},
	}
	_, err = ex.Execute(context.Background(), &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "register-cancel-once"},
		Route:   lipapi.RouteIntent{Selector: "managed:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	})
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("Execute err = %v want ErrALegCanceled", err)
	}
	if got, want := inner.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("managed B-leg calls = %v want %v", got, want)
	}
}

type managedBlockingStream struct {
	ready chan struct{}
	once  sync.Once
	mu    sync.Mutex
	log   []string
}

type recoverableBeforeOutputStream struct{}

func (s *recoverableBeforeOutputStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	return lipapi.Event{}, lipapi.RecoverablePreOutputError(errors.New("temporary"))
}

func (s *recoverableBeforeOutputStream) Close() error { return nil }

func (s *recoverableBeforeOutputStream) Cancel(context.Context, leglifecycle.CancelCause) leglifecycle.CancelResult {
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (m *managedBlockingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	m.once.Do(func() { close(m.ready) })
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}

func (m *managedBlockingStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, "close")
	return nil
}

func (m *managedBlockingStream) Cancel(_ context.Context, cause leglifecycle.CancelCause) leglifecycle.CancelResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, "cancel:"+string(cause.Kind))
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (m *managedBlockingStream) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.log...)
}

func (m *managedBlockingStream) drain() {
	for {
		if _, err := m.Recv(context.Background()); errors.Is(err, io.EOF) {
			return
		}
	}
}

type billingMarkerRecorder struct {
	mu   sync.Mutex
	last app.StreamEventRecordInput
	ok   bool
	n    int
}

func (r *billingMarkerRecorder) RecordClientTurnAfterGate(context.Context, app.ClientTurnRecordInput) error {
	return nil
}

func (r *billingMarkerRecorder) RecordPostHookStreamEvent(_ context.Context, in app.StreamEventRecordInput) error {
	if in.IsUsageEvent {
		r.mu.Lock()
		r.last = in
		r.ok = true
		r.n++
		r.mu.Unlock()
	}
	return nil
}

func (r *billingMarkerRecorder) AppendTranscript(context.Context, domain.TranscriptItem) error {
	return nil
}
func (r *billingMarkerRecorder) AppendAudit(context.Context, domain.AuditItem) error { return nil }
func (r *billingMarkerRecorder) AddUsage(context.Context, domain.UsageDelta) error   { return nil }
func (r *billingMarkerRecorder) TouchActivity(context.Context, domain.SessionID, time.Time, domain.ActivitySource) error {
	return nil
}

func (r *billingMarkerRecorder) usage() (app.StreamEventRecordInput, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last, r.ok
}

func (r *billingMarkerRecorder) usageCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}
