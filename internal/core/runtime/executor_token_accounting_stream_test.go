package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	secureapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingledger "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestExecutorStreamAccountingRecordsProviderAndClientVisibleUsage(t *testing.T) {
	t.Parallel()

	ledger := accountingledger.NewMemoryLedger(accountingledger.Options{Now: fixedAccountingNow})
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Ledger: ledger})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-basic", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)

	usage := usageEventsOf(events)
	if len(usage) != 2 {
		t.Fatalf("usage events = %d, want provider + final scoped client usage", len(usage))
	}
	if usage[0].Accounting.Plane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("first usage plane = %q, want provider_billable", usage[0].Accounting.Plane)
	}
	if hasUsageScopePlane(usage[1], lipapi.UsagePlaneProviderBillable) {
		t.Fatalf("final synthesized usage replayed provider_billable scope already emitted to client: %+v", usage[1])
	}
	if got := usage[1].UsageScopes[len(usage[1].UsageScopes)-1]; got.Accounting.Plane != lipapi.UsagePlaneClientVisible || got.OutputTokens != len("hello") {
		t.Fatalf("final usage = %+v, want client_visible output count for visible text", got)
	}
	if usageEventIndex(events, lipapi.UsagePlaneClientVisible) > eventKindIndex(events, lipapi.EventResponseFinished) {
		t.Fatalf("client_visible usage emitted after response_finished: %+v", events)
	}

	records, err := ledger.ListByRequest(context.Background(), "acct-basic")
	if err != nil {
		t.Fatal(err)
	}
	assertLedgerPlane(t, records, lipapi.UsagePlaneProviderBillable, 3, 11)
	assertLedgerPlane(t, records, lipapi.UsagePlaneClientVisible, 2, 5)
}

func TestExecutorStreamAccountingRecordsSynthesizedUsageBeforeClientEmission(t *testing.T) {
	t.Parallel()

	recorder := &capturingSecureRecorder{}
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{
		Ledger:                accountingledger.NewMemoryLedger(accountingledger.Options{Now: fixedAccountingNow}),
		SecureSessionRecorder: recorder,
	})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-secure-usage", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)

	clientUsageDeltas := 0
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			clientUsageDeltas++
		}
	}
	recordedUsageDeltas := recorder.eventKindCount(string(lipapi.EventUsageDelta))
	if recordedUsageDeltas != clientUsageDeltas {
		t.Fatalf("recorded usage_delta events = %d, want %d client-emitted usage_delta events; events=%+v", recordedUsageDeltas, clientUsageDeltas, events)
	}
}

func TestExecutorStreamAccountingCountsTransformedClientVisibleOutputSeparately(t *testing.T) {
	t.Parallel()

	ledger := accountingledger.NewMemoryLedger(accountingledger.Options{Now: fixedAccountingNow})
	hook := responseHookFunc(func(_ context.Context, ev *lipapi.Event, _ sdkhooks.PartMeta) error {
		if ev.Kind == lipapi.EventTextDelta {
			ev.Delta = "redacted"
		}
		return nil
	})
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Ledger: ledger, ResponseHooks: []sdkhooks.ResponsePartHook{hook}})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-transform", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)

	if !containsTextDelta(events, "redacted") || containsTextDelta(events, "hello") {
		t.Fatalf("events did not expose transformed text only: %+v", events)
	}
	records, err := ledger.ListByRequest(context.Background(), "acct-transform")
	if err != nil {
		t.Fatal(err)
	}
	assertLedgerPlane(t, records, lipapi.UsagePlaneProviderBillable, 3, 11)
	assertLedgerPlane(t, records, lipapi.UsagePlaneClientVisible, 2, len("redacted"))
}

func TestExecutorStreamAccountingRequiredLedgerWriteFailClosesAtCompletion(t *testing.T) {
	t.Parallel()

	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Ledger: failingLedger{err: errors.New("ledger down")}, LedgerWriteRequired: true})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-ledger-required", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()

	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			if !strings.Contains(err.Error(), "ledger down") {
				t.Fatalf("Recv error = %v, want ledger failure", err)
			}
			return
		}
		if ev.Kind == lipapi.EventResponseFinished {
			t.Fatal("required ledger failure emitted response_finished")
		}
	}
}

func TestExecutorStreamAccountingBestEffortLedgerWriteEmitsFinishedAndObservesFailure(t *testing.T) {
	t.Parallel()

	obs := accountingobs.NewStats()
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Ledger: failingLedger{err: errors.New("ledger down")}, Observability: obs})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-ledger-best-effort", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, stream)
	if eventKindIndex(events, lipapi.EventResponseFinished) == len(events) {
		t.Fatalf("response_finished missing after best-effort ledger failure: %+v", events)
	}
	snap := obs.Snapshot()
	if snap.UnavailableReasons[accountingobs.ReasonError] == 0 {
		t.Fatalf("unavailable reasons = %+v, want ledger error observation", snap.UnavailableReasons)
	}
}

func TestExecutorStreamAccountingOutputCountTimeoutStillEmitsProviderUsageAndFinished(t *testing.T) {
	t.Parallel()

	counter := timeoutStreamCounter{inner: blockingStreamCounter{}, timeout: 10 * time.Millisecond}
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Ledger: accountingledger.NewMemoryLedger(accountingledger.Options{Now: fixedAccountingNow}), Counter: counter})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-output-timeout", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventResponseFinished {
			return
		}
	}
	t.Fatal("response_finished missing")
}

func TestExecutorStreamAccountingRecordsPreOutputFailedAttemptUsageSeparately(t *testing.T) {
	t.Parallel()

	ledger := accountingledger.NewMemoryLedger(accountingledger.Options{Now: fixedAccountingNow})
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Ledger: ledger, FailFirstPreOutputWithUsage: true})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-pre-output-fail", "openai:gpt-test|other:gpt-test-2"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainEvents(t, stream)
	records, err := ledger.ListByRequest(context.Background(), "acct-pre-output-fail")
	if err != nil {
		t.Fatal(err)
	}
	provider := recordsForPlane(records, lipapi.UsagePlaneProviderBillable)
	if len(provider) < 2 {
		t.Fatalf("provider records = %+v, want failed attempt and final attempt", provider)
	}
	if provider[0].AttemptID == provider[1].AttemptID {
		t.Fatalf("provider records share attempt id: %+v", provider)
	}
	if provider[0].FailureReason == "" {
		t.Fatalf("failed attempt record missing failure classification: %+v", provider[0])
	}
}

func TestExecutorStreamAccountingPostOutputFailureRecordsPartialUnavailableWithoutRetry(t *testing.T) {
	t.Parallel()

	ledger := accountingledger.NewMemoryLedger(accountingledger.Options{Now: fixedAccountingNow})
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Ledger: ledger, FailAfterOutputWithUsage: true})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-post-output-fail", "openai:gpt-test|other:gpt-test-2"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	for {
		_, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			break
		}
	}
	records, err := ledger.ListByRequest(context.Background(), "acct-post-output-fail")
	if err != nil {
		t.Fatal(err)
	}
	provider := recordsForPlane(records, lipapi.UsagePlaneProviderBillable)
	if len(provider) != 1 {
		t.Fatalf("provider records = %+v, want exactly one post-output partial attempt", provider)
	}
	if provider[0].UnavailableReason == "" || provider[0].FailureReason == "" {
		t.Fatalf("post-output record missing partial/unavailable classification: %+v", provider[0])
	}
}

func TestExecutorTokenAccountingObservationsIncludeStatusesLatencyFallbackAndNoContent(t *testing.T) {
	t.Parallel()

	sink := &capturingObservationSink{}
	obs := accountingobs.NewStats()
	obs.SetSink(sink)
	ex := newStreamAccountingExecutor(t, streamAccountingOptions{Ledger: accountingledger.NewMemoryLedger(accountingledger.Options{Now: fixedAccountingNow}), Observability: obs})
	stream, err := ex.Execute(context.Background(), accountingCall("acct-observe", "openai:gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	_ = drainEvents(t, stream)
	observations := sink.observations()
	if len(observations) == 0 {
		t.Fatal("no observations recorded")
	}
	foundSuccess := false
	for _, obs := range observations {
		attrs := obs.Attributes()
		for key, value := range attrs {
			if strings.Contains(value, "hello") || strings.Contains(key, "content") {
				t.Fatalf("observation leaked content: attrs=%+v", attrs)
			}
		}
		if obs.Status == accountingobs.StatusSuccess && obs.Duration > 0 {
			foundSuccess = true
		}
	}
	if !foundSuccess {
		t.Fatalf("observations = %+v, want success with non-zero latency", observations)
	}
}

func fixedAccountingNow() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

type streamAccountingOptions struct {
	Ledger                      accountingledger.Recorder
	LedgerWriteRequired         bool
	ResponseHooks               []sdkhooks.ResponsePartHook
	Counter                     accountingstream.Counter
	Observability               *accountingobs.Stats
	SecureSessionRecorder       secureapp.GateRecording
	FailFirstPreOutputWithUsage bool
	FailAfterOutputWithUsage    bool
}

func newStreamAccountingExecutor(t *testing.T, opts streamAccountingOptions) *runtime.Executor {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	counter := accountingstream.Counter(streamCountFunc(func(_ context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
		return accountingapp.CountResult{OutputTokens: len(input.Text), TotalTokens: len(input.Text), Accounting: localAccountingMeta()}, nil
	}))
	if opts.Counter != nil {
		counter = opts.Counter
	}
	obs := opts.Observability
	if obs == nil {
		obs = accountingobs.NewStats()
	}
	var opens int
	open := func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		opens++
		if opts.FailFirstPreOutputWithUsage && opens == 1 {
			return &errAfterEventsStream{events: []lipapi.Event{
				{Kind: lipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 0, TotalTokens: 7, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative}},
			}, err: &lipapi.UpstreamFailure{Phase: lipapi.PhasePreOutput, Recoverable: true, Reason: "pre-output accounting failure"}}, nil
		}
		if opts.FailAfterOutputWithUsage && opens == 1 {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventTextDelta, Delta: "hello"},
				{Kind: lipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 4, TotalTokens: 7, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative}},
			}), nil
		}
		return lipapi.NewFixedEventStream([]lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
			{Kind: lipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 11, TotalTokens: 14, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative}},
			{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
		}), nil
	}
	return &runtime.Executor{
		Store:                           store,
		Bus:                             hooks.New(hooks.Config{ResponsePartHooks: opts.ResponseHooks}),
		Rand:                            routing.NewSeededRng(1),
		StreamUsage:                     accountingstream.New(counter, accountingstream.Config{}),
		Ledger:                          opts.Ledger,
		TokenAccountingObservability:    obs,
		LedgerWriteRequired:             opts.LedgerWriteRequired,
		SecureSessionRecordingMandatory: false,
		SecureSessionRecorder:           opts.SecureSessionRecorder,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: open,
			},
			"other": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: open,
			},
		},
	}
}

type failingLedger struct{ err error }

func (l failingLedger) Record(context.Context, accountingledger.Record) error { return l.err }

type errAfterEventsStream struct {
	events []lipapi.Event
	err    error
}

func (s *errAfterEventsStream) Recv(context.Context) (lipapi.Event, error) {
	if len(s.events) == 0 {
		return lipapi.Event{}, s.err
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}

func (s *errAfterEventsStream) Close() error { return nil }

func (s *errAfterEventsStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

type blockingStreamCounter struct{}

func (blockingStreamCounter) CountCall(ctx context.Context, _ accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 2, TotalTokens: 2, Accounting: localAccountingMeta()}, nil
}

func (blockingStreamCounter) CountOutput(ctx context.Context, _ accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	<-ctx.Done()
	return accountingapp.CountResult{}, ctx.Err()
}

type timeoutStreamCounter struct {
	inner   accountingstream.Counter
	timeout time.Duration
}

func (c timeoutStreamCounter) CountCall(ctx context.Context, input accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	child, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.inner.CountCall(child, input)
}

func (c timeoutStreamCounter) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	child, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.inner.CountOutput(child, input)
}

type capturingObservationSink struct {
	mu  sync.Mutex
	obs []accountingobs.Observation
}

func (s *capturingObservationSink) Record(obs accountingobs.Observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obs = append(s.obs, obs)
}

func (s *capturingObservationSink) observations() []accountingobs.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]accountingobs.Observation(nil), s.obs...)
}

type capturingSecureRecorder struct {
	mu         sync.Mutex
	eventKinds []string
}

func (r *capturingSecureRecorder) RecordClientTurnAfterGate(context.Context, secureapp.ClientTurnRecordInput) error {
	return nil
}

func (r *capturingSecureRecorder) RecordPostHookStreamEvent(_ context.Context, in secureapp.StreamEventRecordInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventKinds = append(r.eventKinds, in.EventKind)
	return nil
}

func (r *capturingSecureRecorder) eventKindCount(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, got := range r.eventKinds {
		if got == kind {
			count++
		}
	}
	return count
}

func recordsForPlane(records []accountingledger.Record, plane lipapi.UsagePlane) []accountingledger.Record {
	out := []accountingledger.Record{}
	for _, record := range records {
		if record.Plane == plane {
			out = append(out, record)
		}
	}
	return out
}

type streamCountFunc func(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error)

func (f streamCountFunc) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 2, TotalTokens: 2, Accounting: localAccountingMeta()}, nil
}

func (f streamCountFunc) CountOutput(ctx context.Context, input accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return f(ctx, input)
}

func localAccountingMeta() lipapi.UsageAccountingMetadata {
	return lipapi.UsageAccountingMetadata{Source: lipapi.UsageSourceLocalEstimator, Authority: lipapi.UsageAuthorityEstimated}
}

func accountingCall(id, selector string) *lipapi.Call {
	return &lipapi.Call{ID: id, Route: lipapi.RouteIntent{Selector: selector}, Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
}

func drainEvents(t *testing.T, stream lipapi.EventStream) []lipapi.Event {
	t.Helper()
	defer func() {
		if err := stream.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()
	events := []lipapi.Event{}
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
}

func usageEventsOf(events []lipapi.Event) []lipapi.Event {
	out := []lipapi.Event{}
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			out = append(out, ev)
		}
	}
	return out
}

func usageEventIndex(events []lipapi.Event, plane lipapi.UsagePlane) int {
	for i, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		for _, scope := range ev.UsageScopes {
			if scope.Accounting.Plane == plane {
				return i
			}
		}
	}
	return len(events)
}

func hasUsageScopePlane(ev lipapi.Event, plane lipapi.UsagePlane) bool {
	if ev.Accounting.Plane == plane {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if scope.Accounting.Plane == plane {
			return true
		}
	}
	return false
}

func eventKindIndex(events []lipapi.Event, kind lipapi.EventKind) int {
	for i, ev := range events {
		if ev.Kind == kind {
			return i
		}
	}
	return len(events)
}

func assertLedgerPlane(t *testing.T, records []accountingledger.Record, plane lipapi.UsagePlane, input, output int) {
	t.Helper()
	for _, record := range records {
		if record.Plane == plane {
			if record.InputTokens != input || record.OutputTokens != output {
				t.Fatalf("ledger %s = input %d output %d, want input %d output %d", plane, record.InputTokens, record.OutputTokens, input, output)
			}
			return
		}
	}
	t.Fatalf("missing ledger record for plane %s: %+v", plane, records)
}

func containsTextDelta(events []lipapi.Event, text string) bool {
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, text) {
			return true
		}
	}
	return false
}

type responseHookFunc func(context.Context, *lipapi.Event, sdkhooks.PartMeta) error

func (f responseHookFunc) ID() string                        { return "test-transform" }
func (f responseHookFunc) Order() int                        { return 0 }
func (f responseHookFunc) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (f responseHookFunc) HandleEvent(ctx context.Context, ev *lipapi.Event, meta sdkhooks.PartMeta) error {
	return f(ctx, ev, meta)
}
