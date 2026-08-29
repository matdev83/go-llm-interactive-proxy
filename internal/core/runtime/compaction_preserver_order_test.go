package runtime_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
)

type runtimeCompactionPreserver struct {
	mu              sync.Mutex
	order           *[]string
	opened          int
	responses       int
	responseErr     error
	responsePan     bool
	responseInvalid bool
	failedOpen      bool
	failedCalls     int
	releaseCommit   bool
	state           state.Store
	background      auxiliary.BackgroundClient
}

type orderedRuntimeObserver struct {
	order  *[]string
	mu     sync.Mutex
	events []compaction.Event
}

func (o *orderedRuntimeObserver) OnCompaction(_ context.Context, ev compaction.Event) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, ev)
	if o.order != nil {
		switch ev.Phase {
		case compaction.PhaseStarted:
			*o.order = append(*o.order, "observer-started")
		case compaction.PhaseCompleted:
			*o.order = append(*o.order, "observer-completed")
		}
	}
	return nil
}

func (p *runtimeCompactionPreserver) ID() string { return "runtime-order" }

func (*runtimeCompactionPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p *runtimeCompactionPreserver) RequestOpened(_ context.Context, call lipapi.Call, events []compaction.Event, _ compaction.PreservationMeta, services compaction.Services) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opened++
	if p.order != nil && len(events) > 0 {
		*p.order = append(*p.order, "preserver-open")
	}
	if call.Session.AuthoritativeSessionID == "" {
		return errors.New("missing effective session correlation")
	}
	if p.state != nil && services.State != p.state {
		return errors.New("snapshot state was not passed to preserver")
	}
	if p.background != nil && services.BackgroundAux != p.background {
		return errors.New("process background auxiliary was not passed to preserver")
	}
	return nil
}

func (p *runtimeCompactionPreserver) BeforeResponseRelease(_ context.Context, ev *lipapi.Event, _ compaction.ResponsePreview, _ compaction.PreservationMeta, _ compaction.Services) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ev == nil || ev.Kind != lipapi.EventTextDelta {
		return nil
	}
	p.responses++
	if p.order != nil {
		*p.order = append(*p.order, "preserver-response")
	}
	if p.responseInvalid {
		ev.Kind = lipapi.EventReasoningPart
		ev.Reasoning = nil
		return nil
	}
	ev.Delta = "[CONTEXT SUMMARY]: preserved"
	if p.responsePan {
		panic("preserver panic")
	}
	return p.responseErr
}

func (p *runtimeCompactionPreserver) RequestOpenFailed(context.Context, compaction.PreservationMeta, compaction.Services) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failedOpen {
		p.failedCalls++
	}
	return nil
}

func (p *runtimeCompactionPreserver) AfterResponseRelease(_ context.Context, ev lipapi.Event, _ compaction.PreservationMeta, _ compaction.Services) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.releaseCommit && ev.Kind == lipapi.EventTextDelta {
		if p.order != nil {
			*p.order = append(*p.order, "release-commit")
		}
	}
	return nil
}

type runtimeBackgroundStub struct{}

func (runtimeBackgroundStub) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "", auxiliary.ErrNotConfigured
}

func (runtimeBackgroundStub) Await(context.Context, auxiliary.JobID) (lipapi.Collected, error) {
	return lipapi.Collected{}, auxiliary.ErrNotConfigured
}
func (runtimeBackgroundStub) Forget(auxiliary.JobID) {}

type runtimeStateStub struct{}

func (runtimeStateStub) Get(context.Context, state.Scope, string, string, any) (bool, error) {
	return false, state.ErrNotConfigured
}

func (runtimeStateStub) Put(context.Context, state.Scope, string, string, any, time.Duration) error {
	return state.ErrNotConfigured
}

func (runtimeStateStub) Delete(context.Context, state.Scope, string, string) error {
	return state.ErrNotConfigured
}

func (runtimeStateStub) InspectTTL(context.Context, state.Scope, string, string) (time.Duration, bool, error) {
	return 0, false, state.ErrNotConfigured
}

func configureRuntimeCompactionPreserver(t *testing.T, d *compactiondetect.Detector, observer *recordingCompactionObserver, p compaction.Preserver, st state.Store, aux auxiliary.BackgroundClient) *runtime.Executor {
	t.Helper()
	ex := compactionTestExecutor(t, d, observer)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		State: st,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers:  []compaction.Observer{observer},
			CompactionPreservers: []compaction.Preserver{p},
		}),
	})
	ex.CompactionRuntime = runtime.CompactionRuntime{Detector: d, BackgroundAux: aux}
	return ex
}

func TestCompactionPreserverRuntime_orderMutationVisibleToDetectorAndClient(t *testing.T) {
	d := compactiondetect.New(compactiondetect.Config{})
	observer := &recordingCompactionObserver{}
	order := make([]string, 0, 4)
	st := runtimeStateStub{}
	aux := runtimeBackgroundStub{}
	p := &runtimeCompactionPreserver{order: &order, state: st, background: aux, releaseCommit: true}
	ex := configureRuntimeCompactionPreserver(t, d, observer, p, st, aux)
	orderedObserver := &orderedRuntimeObserver{order: &order}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		State: st,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers:  []compaction.Observer{observer, orderedObserver},
			CompactionPreservers: []compaction.Preserver{p},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventTextDelta, Delta: "ordinary"},
				{Kind: lipapi.EventResponseFinished},
			})
		}),
	}
	stream, err := ex.Execute(context.Background(), compactCall("preserver-order", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now")))
	if err != nil {
		t.Fatal(err)
	}
	released := drain(t, stream)

	if got, want := order, []string{"preserver-open", "observer-started", "preserver-response", "observer-completed", "release-commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preserver order=%v want %v", got, want)
	}
	observed := observer.snapshot()
	if len(observed) != 2 || observed[0].Phase != compaction.PhaseStarted || observed[1].Phase != compaction.PhaseCompleted {
		t.Fatalf("detector events=%+v want started then completed", observed)
	}
	var found bool
	for _, ev := range released {
		if ev.Kind == lipapi.EventTextDelta && ev.Delta == "[CONTEXT SUMMARY]: preserved" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("client did not receive post-preservation event: %+v", released)
	}
	if p.opened != 1 || p.responses == 0 {
		t.Fatalf("callback counts opened=%d responses=%d", p.opened, p.responses)
	}
}

func TestCompactionPreserverRuntime_failedOpenAndFailoverOpenCallbackCardinality(t *testing.T) {
	t.Run("failed open", func(t *testing.T) {
		d := compactiondetect.New(compactiondetect.Config{})
		observer := &recordingCompactionObserver{}
		p := &runtimeCompactionPreserver{failedOpen: true}
		ex := configureRuntimeCompactionPreserver(t, d, observer, p, nil, nil)
		ex.Backends = map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return nil, errors.New("upstream refused")
				},
			},
		}
		_, err := ex.Execute(context.Background(), compactCall("preserver-failed-open", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now")))
		if err == nil {
			t.Fatal("expected open failure")
		}
		if p.failedCalls != 1 {
			t.Fatalf("RequestOpenFailed callbacks=%d want 1", p.failedCalls)
		}
	})

	t.Run("failover", func(t *testing.T) {
		d := compactiondetect.New(compactiondetect.Config{})
		observer := &recordingCompactionObserver{}
		p := &runtimeCompactionPreserver{failedOpen: true}
		ex := configureRuntimeCompactionPreserver(t, d, observer, p, nil, nil)
		ex.Backends = map[string]execbackend.Backend{
			"bad": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return nil, lipapi.RecoverablePreOutputError(errors.New("bad upstream"))
				},
			},
			"good": openStubBackend(func() lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}})
			}),
		}
		call := compactCall("preserver-failover", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
		call.Route.Selector = "bad:m|good:m"
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		drain(t, stream)
		if p.failedCalls != 0 || p.opened != 1 {
			t.Fatalf("failed-open callbacks=%d want 0, RequestOpened=%d want 1", p.failedCalls, p.opened)
		}
	})

	t.Run("aborted stream has no release commit", func(t *testing.T) {
		d := compactiondetect.New(compactiondetect.Config{})
		observer := &recordingCompactionObserver{}
		order := make([]string, 0, 2)
		p := &runtimeCompactionPreserver{order: &order, releaseCommit: true}
		ex := configureRuntimeCompactionPreserver(t, d, observer, p, nil, nil)
		ex.Backends = map[string]execbackend.Backend{
			"openai": openStubBackend(func() lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "ordinary"},
					{Kind: lipapi.EventResponseFinished},
				})
			}),
		}
		stream, err := ex.Execute(context.Background(), compactCall("preserver-abort", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now")))
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if len(order) != 1 || order[0] != "preserver-open" {
			t.Fatalf("aborted stream release callbacks=%v", order)
		}
	})
}

func TestCompactionPreserverRuntime_failedMutationRestoresDetectorAndClientEvent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		panic   bool
		invalid bool
	}{
		{name: "error", err: errors.New("preserver failed")},
		{name: "panic", panic: true},
		{name: "invalid", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := compactiondetect.New(compactiondetect.Config{})
			observer := &recordingCompactionObserver{}
			p := &runtimeCompactionPreserver{responseErr: tc.err, responsePan: tc.panic, responseInvalid: tc.invalid}
			ex := configureRuntimeCompactionPreserver(t, d, observer, p, nil, nil)
			ex.Backends = map[string]execbackend.Backend{
				"openai": openStubBackend(func() lipapi.ManagedEventStream {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventTextDelta, Delta: "ordinary"},
						{Kind: lipapi.EventResponseFinished},
					})
				}),
			}
			stream, err := ex.Execute(context.Background(), compactCall("preserver-rollback-"+tc.name, bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now")))
			if err != nil {
				t.Fatal(err)
			}
			released := drain(t, stream)
			for _, ev := range released {
				if ev.Kind == lipapi.EventTextDelta && ev.Delta != "ordinary" {
					t.Fatalf("failed callback mutation reached client: %+v", ev)
				}
			}
			for _, ev := range observer.snapshot() {
				if ev.Phase == compaction.PhaseCompleted {
					t.Fatalf("detector observed failed callback mutation: %+v", ev)
				}
			}
		})
	}
}
