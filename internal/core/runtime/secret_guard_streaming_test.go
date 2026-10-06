package runtime_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	featuresecretguard "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

// boundedManagedEventStream models a provider that can release one bounded event
// at a time. It has no producer goroutine: the test owns every release boundary.
type boundedManagedEventStream struct {
	events chan lipapi.Event
	closed chan struct{}

	closeOnce   sync.Once
	eventsOnce  sync.Once
	readOnce    sync.Once
	readStarted chan struct{}
	closeCount  atomic.Int32
	finished    atomic.Bool
}

func newBoundedManagedEventStream() *boundedManagedEventStream {
	return &boundedManagedEventStream{
		events:      make(chan lipapi.Event, 1),
		closed:      make(chan struct{}),
		readStarted: make(chan struct{}),
	}
}

func (s *boundedManagedEventStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	s.readOnce.Do(func() { close(s.readStarted) })
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-s.closed:
		return lipapi.Event{}, io.EOF
	case ev, ok := <-s.events:
		if !ok {
			return lipapi.Event{}, io.EOF
		}
		return ev, nil
	}
}

func (s *boundedManagedEventStream) release(ev lipapi.Event) error {
	select {
	case <-s.closed:
		return io.EOF
	case s.events <- ev:
		return nil
	}
}

func (s *boundedManagedEventStream) releaseFinished(ev lipapi.Event) error {
	s.finished.Store(true)
	if err := s.release(ev); err != nil {
		return err
	}
	s.eventsOnce.Do(func() { close(s.events) })
	return nil
}

func (s *boundedManagedEventStream) Close() error {
	s.closeCount.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (s *boundedManagedEventStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	_ = s.Close()
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

// completionBufferedManagedEventStream is the intentional negative control: it
// hides the first event behind completion, which violates the streaming contract.
type completionBufferedManagedEventStream struct {
	event        lipapi.Event
	completion   chan struct{}
	closed       chan struct{}
	closeOnce    sync.Once
	closeCount   atomic.Int32
	completionOK atomic.Bool
}

func newCompletionBufferedManagedEventStream(ev lipapi.Event) *completionBufferedManagedEventStream {
	return &completionBufferedManagedEventStream{
		event:      ev,
		completion: make(chan struct{}),
		closed:     make(chan struct{}),
	}
}

func (s *completionBufferedManagedEventStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-s.closed:
		return lipapi.Event{}, io.EOF
	case <-s.completion:
		return s.event, nil
	}
}

func (s *completionBufferedManagedEventStream) Close() error {
	s.closeCount.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (s *completionBufferedManagedEventStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	_ = s.Close()
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (s *completionBufferedManagedEventStream) releaseCompletion() {
	if s.completionOK.CompareAndSwap(false, true) {
		close(s.completion)
	}
}

func installBetterLeaksProviderStream(t *testing.T, h *betterLeaksRuntimeHarness, provider lipapi.ManagedEventStream) *lipapi.Call {
	t.Helper()
	backend, ok := h.exec.Backends["openai"]
	if !ok || backend.Open == nil {
		t.Fatal("openai backend opener is not configured")
	}
	originalOpen := backend.Open
	var providerCall lipapi.Call
	backend.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		observed, err := originalOpen(ctx, call, cand)
		if err != nil {
			return nil, err
		}
		_ = observed.Close()
		providerCall = lipapi.CloneCall(call)
		return provider, nil
	}
	h.exec.Backends["openai"] = backend
	return &providerCall
}

func validateRuntimeSecretGuardCall(t *testing.T, call *lipapi.Call) {
	t.Helper()
	if err := call.Validate(); err != nil {
		assertRuntimeNoSyntheticSecrets(t, err.Error())
		t.Fatalf("canonical call validation failed: error type %T", err)
	}
}

func assertRuntimeNoSyntheticSecrets(t *testing.T, value string) {
	t.Helper()
	for _, secret := range testkit.AllSyntheticSecretGuardValues() {
		if secret != "" && strings.Contains(value, secret) {
			t.Fatalf("diagnostic contained synthetic secret (len=%d)", len(value))
		}
	}
}

func assertProviderReceivedRedactedRequest(t *testing.T, providerCall, before lipapi.Call) {
	t.Helper()
	validateRuntimeSecretGuardCall(t, &providerCall)
	if reflect.DeepEqual(providerCall, before) {
		t.Fatal("provider received the original canonical request")
	}
	if len(providerCall.Messages) == 0 || len(providerCall.Messages[0].Parts) == 0 {
		t.Fatal("provider request lost its user message")
	}
	if strings.Contains(providerCall.Messages[0].Parts[0].Text, syntheticBetterLeaksRuntimeGitHubToken) {
		t.Fatal("provider received the unredacted request secret")
	}
}

func recvWithDeadline(t *testing.T, stream lipapi.EventStream) (lipapi.Event, error) {
	t.Helper()
	return recvWithTimeout(t, stream, time.Second)
}

func recvWithTimeout(t *testing.T, stream lipapi.EventStream, timeout time.Duration) (lipapi.Event, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	return stream.Recv(ctx)
}

func TestExecutor_BetterLeaksRedactStreamsResponseIncrementally(t *testing.T) {
	h := newBetterLeaksRuntimeHarness(t, featuresecretguard.ActionRedact)
	validateRuntimeSecretGuardCall(t, h.call)
	before := lipapi.CloneCall(*h.call)

	responseSecret := "response " + syntheticBetterLeaksRuntimeGitHubToken
	first := lipapi.Event{Kind: lipapi.EventTextDelta, Delta: responseSecret + " first"}
	second := lipapi.Event{Kind: lipapi.EventTextDelta, Delta: responseSecret + " second"}
	finish := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "stop"}
	provider := newBoundedManagedEventStream()
	defer func() { _ = provider.Close() }()
	providerCall := installBetterLeaksProviderStream(t, h, provider)

	execCtx, cancelExec := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelExec()
	ctx := execview.WithPrincipal(execCtx, execview.PrincipalView{ID: h.ownerID})
	stream, execErr := h.exec.Execute(ctx, h.call)
	if execErr != nil {
		t.Fatalf("redacted request execution failed: error type %T", execErr)
	}
	if stream == nil {
		t.Fatal("redacted request did not reach the backend")
	}
	defer func() { _ = stream.Close() }()
	validateRuntimeSecretGuardCall(t, h.call)
	assertProviderReceivedRedactedRequest(t, *providerCall, before)
	if h.backendOpens.Load() != 1 {
		t.Fatalf("backend opens = %d", h.backendOpens.Load())
	}

	observed := make([]lipapi.Event, 0, 5)
	responseStarted := lipapi.Event{Kind: lipapi.EventResponseStarted}
	messageStarted := lipapi.Event{Kind: lipapi.EventMessageStarted}
	if err := provider.release(responseStarted); err != nil {
		t.Fatalf("release response start: error type %T", err)
	}
	got, err := recvWithDeadline(t, stream)
	if err != nil {
		t.Fatalf("response start was not observable: error type %T", err)
	}
	if !reflect.DeepEqual(got, responseStarted) {
		t.Fatal("response start event was changed before downstream delivery")
	}
	observed = append(observed, got)
	if err := provider.release(messageStarted); err != nil {
		t.Fatalf("release message start: error type %T", err)
	}
	got, err = recvWithDeadline(t, stream)
	if err != nil {
		t.Fatalf("message start was not observable: error type %T", err)
	}
	if !reflect.DeepEqual(got, messageStarted) {
		t.Fatal("message start event was changed before downstream delivery")
	}
	observed = append(observed, got)
	if provider.finished.Load() {
		t.Fatal("provider completion was released before the first content event")
	}
	if err := provider.release(first); err != nil {
		t.Fatalf("release first provider event: error type %T", err)
	}
	got, err = recvWithDeadline(t, stream)
	if err != nil {
		t.Fatalf("first provider event was not observable before completion: error type %T", err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatal("first provider event was changed before downstream delivery")
	}
	observed = append(observed, got)
	if provider.finished.Load() {
		t.Fatal("provider completion was released before the second event")
	}
	if err := provider.release(second); err != nil {
		t.Fatalf("release second provider event: error type %T", err)
	}
	got, err = recvWithDeadline(t, stream)
	if err != nil {
		t.Fatalf("second provider event was not observable incrementally: error type %T", err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatal("second provider event was changed before downstream delivery")
	}
	observed = append(observed, got)
	if provider.finished.Load() {
		t.Fatal("provider completion was released before the finish event")
	}
	if err := provider.releaseFinished(finish); err != nil {
		t.Fatalf("release provider finish: error type %T", err)
	}
	got, err = recvWithDeadline(t, stream)
	if err != nil {
		t.Fatalf("provider finish was not observable: error type %T", err)
	}
	if !reflect.DeepEqual(got, finish) {
		t.Fatal("provider finish event was changed before downstream delivery")
	}
	observed = append(observed, got)
	if err := lipapi.ValidateEventSequence(observed); err != nil {
		t.Fatalf("observed provider event sequence: error type %T", err)
	}
	_, err = recvWithDeadline(t, stream)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("normal stream termination: got %T want EOF", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close runtime stream: error type %T", err)
	}
	if provider.closeCount.Load() == 0 {
		t.Fatal("runtime did not close the provider stream")
	}
}

func TestExecutor_BetterLeaksRedactBufferingAdapterIsRejected(t *testing.T) {
	first := lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "buffered response " + syntheticBetterLeaksRuntimeGitHubToken}
	provider := newCompletionBufferedManagedEventStream(first)
	defer func() { _ = provider.Close() }()
	got, err := recvWithTimeout(t, provider, 50*time.Millisecond)
	if err == nil {
		assertRuntimeNoSyntheticSecrets(t, got.Delta)
		t.Fatal("completion-buffered adapter exposed an event before completion")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("buffering control error: got %T want deadline", err)
	}
	if provider.completionOK.Load() {
		t.Fatal("buffering control completion gate opened")
	}
	provider.releaseCompletion()
	got, err = recvWithDeadline(t, provider)
	if err != nil {
		t.Fatalf("released completion did not expose buffered event: error type %T", err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatal("completion-buffered adapter changed the delayed event")
	}
	if !provider.completionOK.Load() {
		t.Fatal("buffering control completion gate did not open")
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("close buffering control: error type %T", err)
	}
	if provider.closeCount.Load() == 0 {
		t.Fatal("buffering control was not closed")
	}
}

func TestExecutor_BetterLeaksRedactBufferingIntegrationDoesNotExposeBeforeCompletion(t *testing.T) {
	h := newBetterLeaksRuntimeHarness(t, featuresecretguard.ActionRedact)
	validateRuntimeSecretGuardCall(t, h.call)
	before := lipapi.CloneCall(*h.call)
	provider := newCompletionBufferedManagedEventStream(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "buffered response " + syntheticBetterLeaksRuntimeGitHubToken})
	defer func() { _ = provider.Close() }()
	providerCall := installBetterLeaksProviderStream(t, h, provider)
	execCtx, cancelExec := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelExec()
	ctx := execview.WithPrincipal(execCtx, execview.PrincipalView{ID: h.ownerID})
	stream, execErr := h.exec.Execute(ctx, h.call)
	if execErr != nil || stream == nil {
		t.Fatalf("buffering integration setup failed: error type %T stream_nil=%t", execErr, stream == nil)
	}
	defer func() { _ = stream.Close() }()
	validateRuntimeSecretGuardCall(t, h.call)
	assertProviderReceivedRedactedRequest(t, *providerCall, before)
	got, err := recvWithTimeout(t, stream, 50*time.Millisecond)
	if err == nil {
		assertRuntimeNoSyntheticSecrets(t, got.Delta)
		t.Fatal("completion-buffered provider exposed an event before completion")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("buffering integration error: got %T want deadline", err)
	}
}

func TestExecutor_BetterLeaksRedactCancellationCleansProviderStream(t *testing.T) {
	h := newBetterLeaksRuntimeHarness(t, featuresecretguard.ActionRedact)
	validateRuntimeSecretGuardCall(t, h.call)
	before := lipapi.CloneCall(*h.call)
	provider := newBoundedManagedEventStream()
	defer func() { _ = provider.Close() }()
	providerCall := installBetterLeaksProviderStream(t, h, provider)
	execCtx, cancelExec := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelExec()
	ctx := execview.WithPrincipal(execCtx, execview.PrincipalView{ID: h.ownerID})
	stream, execErr := h.exec.Execute(ctx, h.call)
	if execErr != nil || stream == nil {
		t.Fatalf("cancellation setup failed: error type %T stream_nil=%t", execErr, stream == nil)
	}
	validateRuntimeSecretGuardCall(t, h.call)
	assertProviderReceivedRedactedRequest(t, *providerCall, before)

	recvCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := stream.Recv(recvCtx)
		done <- err
	}()
	joined := false
	defer func() {
		cancel()
		_ = stream.Close()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("canceled runtime Recv goroutine did not join")
			}
		}
	}()
	select {
	case <-provider.readStarted:
	case <-time.After(time.Second):
		t.Fatal("provider Recv did not become blocked")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled runtime Recv: got %T want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled runtime Recv did not return")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close canceled runtime stream: error type %T", err)
	}
	if provider.closeCount.Load() == 0 {
		t.Fatal("cancellation cleanup did not close the provider stream")
	}
}
