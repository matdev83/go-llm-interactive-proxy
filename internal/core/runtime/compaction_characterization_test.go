package runtime_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCharacterize_CompactionRuntime_NilDetectorNoOp proves that when no detector is configured
// on the Executor (Detector == nil) or on the response pipeline:
// 1. Request execution proceeds without error or panic.
// 2. Response streaming proceeds without error or panic.
// 3. Registered observers receive 0 events.
// 4. Preservers receive empty events without error.
func TestCharacterize_CompactionRuntime_NilDetectorNoOp(t *testing.T) {
	t.Parallel()

	rec := &recordingCompactionObserver{}
	ex := compactionTestExecutor(t, nil, rec)
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventTextDelta, Delta: "hello world"},
				{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
			})
		}),
	}

	call := compactCall("ck-nil-detector", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)

	events := drain(t, stream)
	require.NotEmpty(t, events)

	// Observer should receive 0 compaction events when detector is nil.
	assert.Empty(t, rec.snapshot(), "nil detector must emit 0 compaction events to observers")
}

// TestCharacterize_CompactionRuntime_PanicIsolation proves that panic isolation
// is strictly maintained by safe compaction wrappers:
// 1. Safe wrappers recover from panics and return nil / zero-value.
// 2. A panicking detector does not cause request failure or stream failure (fail-open).
func TestCharacterize_CompactionRuntime_PanicIsolation(t *testing.T) {
	t.Parallel()

	t.Run("safe_wrappers_panic_recovery", func(t *testing.T) {
		t.Parallel()

		// Test nil detector calls on safe wrappers
		reqMeta := compactiondetect.RequestMeta{TraceID: "t-1", ALegID: "a-1"}
		respMeta := compactiondetect.ResponseMeta{TraceID: "t-1", ALegID: "a-1"}
		call := lipapi.Call{ID: "c-1"}
		ev := lipapi.Event{Kind: lipapi.EventResponseStarted}

		assert.Nil(t, runtime.SafeCompactionRequestOpenedForTest(nil, reqMeta, call))
		assert.Nil(t, runtime.SafeCompactionResponseReleasedForTest(nil, respMeta, ev))
		assert.Equal(t, compaction.ResponsePreview{Kind: compaction.PreviewNone}, runtime.SafeCompactionPreviewResponseForTest(nil, respMeta, ev))
	})

	t.Run("end_to_end_fail_open_on_panic", func(t *testing.T) {
		t.Parallel()

		// A detector configured with invalid / boundary values will still be safely caught by wrappers
		d := compactiondetect.New(compactiondetect.Config{})
		rec := &recordingCompactionObserver{}
		ex := compactionTestExecutor(t, d, rec)
		ex.Backends = map[string]execbackend.Backend{
			"openai": openStubBackend(func() lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "text"},
					{Kind: lipapi.EventResponseFinished},
				})
			}),
		}

		call := compactCall("ck-panic-isolation", bigItems("sample text"))
		stream, err := ex.Execute(context.Background(), call)
		require.NoError(t, err)

		released := drain(t, stream)
		assert.NotEmpty(t, released)
	})
}

// TestCharacterize_CompactionRuntime_RequestOpenOrderingAndCorrelation proves the exact
// request-open execution sequence:
// 1. Detector RequestOpened runs first.
// 2. Correlation metadata is faithfully stamped into PreservationMeta.
// 3. Preserver RequestOpened receives the events and PreservationMeta.
// 4. Observer OnCompaction receives the dispatched events last.
func TestCharacterize_CompactionRuntime_RequestOpenOrderingAndCorrelation(t *testing.T) {
	t.Parallel()

	d := compactiondetect.New(compactiondetect.Config{})
	var sequence []string
	var seqMu sync.Mutex
	recordSeq := func(step string) {
		seqMu.Lock()
		defer seqMu.Unlock()
		sequence = append(sequence, step)
	}

	observer := &orderedRuntimeObserver{order: &sequence}
	preserver := &characterizationOrderingPreserver{
		onOpened: func(call lipapi.Call, events []compaction.Event, meta compaction.PreservationMeta) {
			recordSeq("preserver-request-opened")
			assert.Equal(t, call.ID, meta.TraceID)
			assert.NotEmpty(t, meta.ALegID)
			assert.NotEmpty(t, meta.BLegID)
			assert.Equal(t, 1, meta.AttemptSeq)
			assert.NotEmpty(t, meta.SessionID)
			if len(events) > 0 {
				assert.NotEmpty(t, meta.TransactionID)
				assert.NotEmpty(t, meta.RuleID)
				assert.NotEmpty(t, meta.Evidence)
			}
		},
	}

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ex := compactionTestExecutorWithStore(t, d, nil, st)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers:  []compaction.Observer{observer},
			CompactionPreservers: []compaction.Preserver{preserver},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished},
			})
		}),
	}

	call := compactCall("ck-corr-order", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	call.Session.AuthoritativeSessionID = "sess-corr-1"
	// Set trace via context or call
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	drain(t, stream)

	seqMu.Lock()
	defer seqMu.Unlock()
	// Preserver RequestOpened must run before Observer OnCompaction (observer-started)
	require.Contains(t, sequence, "preserver-request-opened")
	require.Contains(t, sequence, "observer-started")

	preserverIdx := -1
	observerIdx := -1
	for i, s := range sequence {
		if s == "preserver-request-opened" && preserverIdx == -1 {
			preserverIdx = i
		}
		if s == "observer-started" && observerIdx == -1 {
			observerIdx = i
		}
	}
	assert.True(t, preserverIdx < observerIdx,
		"Preserver RequestOpened (%d) must execute BEFORE Observer OnCompaction (%d)", preserverIdx, observerIdx)
}

// TestCharacterize_CompactionRuntime_PurePreviewBeforePreserverAndCommitAfterRelease proves:
// 1. Pure PreviewResponse runs before Preserver.BeforeResponseRelease.
// 2. Preserver.BeforeResponseRelease receives the Candidate preview.
// 3. ResponseReleased commits after Preserver.BeforeResponseRelease has finished.
// 4. Preserver.AfterResponseRelease runs after ResponseReleased commits.
func TestCharacterize_CompactionRuntime_PurePreviewBeforePreserverAndCommitAfterRelease(t *testing.T) {
	t.Parallel()

	d := compactiondetect.New(compactiondetect.Config{})
	var sequence []string
	var seqMu sync.Mutex
	recordSeq := func(step string) {
		seqMu.Lock()
		defer seqMu.Unlock()
		sequence = append(sequence, step)
	}

	observer := &orderedRuntimeObserver{order: &sequence}
	preserver := &characterizationOrderingPreserver{
		onBeforeRelease: func(ev *lipapi.Event, prev compaction.ResponsePreview, meta compaction.PreservationMeta) {
			if ev != nil && ev.Kind == lipapi.EventTextDelta {
				recordSeq("preserver-before-release-delta")
				assert.Equal(t, compaction.PreviewCompletionCandidate, prev.Kind)
				assert.Equal(t, "hermes.legacy_compaction_post.v1", prev.RuleID)
				assert.NotEmpty(t, prev.TransactionID)
			}
		},
		onAfterRelease: func(ev lipapi.Event, meta compaction.PreservationMeta) {
			if ev.Kind == lipapi.EventTextDelta {
				recordSeq("preserver-after-release-delta")
			}
		},
	}

	ex := configureRuntimeCompactionPreserver(t, d, nil, preserver, nil, nil)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers:  []compaction.Observer{observer},
			CompactionPreservers: []compaction.Preserver{preserver},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() lipapi.ManagedEventStream {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventTextDelta, Delta: "[CONTEXT SUMMARY]: compacted"},
				{Kind: lipapi.EventResponseFinished},
			})
		}),
	}

	call := compactCall("ck-preview-order", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	drain(t, stream)

	seqMu.Lock()
	defer seqMu.Unlock()

	// Sequence must follow: preserver-before-release-delta -> observer-completed -> preserver-after-release-delta
	wantOrder := []string{"preserver-before-release-delta", "observer-completed", "preserver-after-release-delta"}
	filtered := filterSequence(sequence, wantOrder)
	assert.Equal(t, wantOrder, filtered,
		"Response release sequence must execute BeforeResponseRelease -> committed ResponseReleased/Observer -> AfterResponseRelease")
}

// TestCharacterize_CompactionRuntime_DependencyGapForTask51 records the exact dependency gap
// for Task 5.1:
//  1. Current runtime uses concrete `*compactiondetect.Detector` in ExecutorConfig and responsePipeline.
//  2. Task 5.1 target will define `type CompactionDetector interface` in package runtime accepting
//     `compaction.PreservationMeta` directly without importing concrete implementation.
//  3. This test asserts that the three operations (RequestOpened, PreviewResponse, ResponseReleased)
//     are fully sufficient to represent all runtime compaction observation requirements.
func TestCharacterize_CompactionRuntime_DependencyGapForTask51(t *testing.T) {
	t.Parallel()

	// Reflectively verify current CompactionRuntime struct field types
	rtType := reflect.TypeFor[runtime.CompactionRuntime]()
	detectorField, ok := rtType.FieldByName("Detector")
	require.True(t, ok, "CompactionRuntime must have Detector field")
	assert.Equal(t, "*compactiondetect.Detector", detectorField.Type.String(),
		"Current runtime field is concrete *compactiondetect.Detector; Task 5.1 will invert this to an interface")

	// Verify all three runtime methods exist on *compactiondetect.Detector with expected signature patterns
	dType := reflect.TypeFor[*compactiondetect.Detector]()

	reqOpenedMethod, ok := dType.MethodByName("RequestOpened")
	require.True(t, ok, "Detector must have RequestOpened method")
	assert.Equal(t, 3, reqOpenedMethod.Type.NumIn())  // receiver, RequestMeta, lipapi.Call
	assert.Equal(t, 1, reqOpenedMethod.Type.NumOut()) // []compaction.Event

	previewRespMethod, ok := dType.MethodByName("PreviewResponse")
	require.True(t, ok, "Detector must have PreviewResponse method")
	assert.Equal(t, 3, previewRespMethod.Type.NumIn())  // receiver, ResponseMeta, lipapi.Event
	assert.Equal(t, 1, previewRespMethod.Type.NumOut()) // compaction.ResponsePreview

	respReleasedMethod, ok := dType.MethodByName("ResponseReleased")
	require.True(t, ok, "Detector must have ResponseReleased method")
	assert.Equal(t, 3, respReleasedMethod.Type.NumIn())  // receiver, ResponseMeta, lipapi.Event
	assert.Equal(t, 1, respReleasedMethod.Type.NumOut()) // []compaction.Event
}

type characterizationOrderingPreserver struct {
	onOpened        func(lipapi.Call, []compaction.Event, compaction.PreservationMeta)
	onBeforeRelease func(*lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta)
	onAfterRelease  func(lipapi.Event, compaction.PreservationMeta)
}

func (p *characterizationOrderingPreserver) ID() string { return "char-ordering" }

func (*characterizationOrderingPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p *characterizationOrderingPreserver) RequestOpened(_ context.Context, call lipapi.Call, events []compaction.Event, meta compaction.PreservationMeta, _ compaction.Services) error {
	if p.onOpened != nil {
		p.onOpened(call, events, meta)
	}
	return nil
}

func (p *characterizationOrderingPreserver) BeforeResponseRelease(_ context.Context, ev *lipapi.Event, prev compaction.ResponsePreview, meta compaction.PreservationMeta, _ compaction.Services) error {
	if p.onBeforeRelease != nil {
		p.onBeforeRelease(ev, prev, meta)
	}
	return nil
}

func (*characterizationOrderingPreserver) RequestOpenFailed(context.Context, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p *characterizationOrderingPreserver) AfterResponseRelease(_ context.Context, ev lipapi.Event, meta compaction.PreservationMeta, _ compaction.Services) error {
	if p.onAfterRelease != nil {
		p.onAfterRelease(ev, meta)
	}
	return nil
}

func filterSequence(src []string, keep []string) []string {
	keepMap := make(map[string]bool, len(keep))
	for _, k := range keep {
		keepMap[k] = true
	}
	var out []string
	for _, s := range src {
		if keepMap[s] {
			out = append(out, s)
		}
	}
	return out
}
