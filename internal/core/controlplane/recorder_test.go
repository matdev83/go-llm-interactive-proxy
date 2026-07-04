package controlplane_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// recordingStore is a test-only adapter recording calls to the core-owned Store
// port so recorder behavior can be asserted without SQL/Bun/HTTP types.
type recordingStore struct {
	appends     atomic.Int64
	appendErr   error
	lastEvent   cp.Event
	retention   atomic.Int64
	retentionFn func(controlplane.RetentionCommand) (controlplane.RetentionResult, error)
	ready       error
}

func (s *recordingStore) Append(_ context.Context, ev cp.Event) (cp.RecordResult, error) {
	s.appends.Add(1)
	s.lastEvent = ev
	if s.appendErr != nil {
		return cp.RecordResult{}, s.appendErr
	}
	seq := s.appends.Load()
	return cp.RecordResult{
		ID:         cp.EventID{StoreID: "rec", Sequence: seq},
		Dedupe:     cp.DedupeInserted,
		RecordedAt: ev.OccurredAt,
	}, nil
}

func (s *recordingStore) Sessions(context.Context, cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	return cp.Page[cp.SessionSummary]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) Attempts(context.Context, cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	return cp.Page[cp.AttemptRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) Usage(context.Context, cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	return cp.Page[cp.UsageRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) UsageAggregate(context.Context, cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	return cp.Page[cp.UsageAggregate]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) PolicyAudit(context.Context, cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	return cp.Page[cp.PolicyAuditRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) Events(context.Context, cp.EventQuery) (cp.Page[cp.Event], error) {
	return cp.Page[cp.Event]{Visibility: cp.VisibilityDefault}, nil
}

func (s *recordingStore) ApplyRetention(_ context.Context, cmd controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
	s.retention.Add(1)
	if s.retentionFn != nil {
		return s.retentionFn(cmd)
	}
	return controlplane.RetentionResult{Marked: 1}, nil
}
func (s *recordingStore) CheckReadiness(context.Context) error { return s.ready }

var _ controlplane.Store = (*recordingStore)(nil)

func recorderEvent() cp.Event {
	return cp.Event{
		Category:       cp.CategoryAuth,
		OccurredAt:     time.Now(),
		RecordedAt:     time.Now(),
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Auth:           &cp.AuthDetail{Outcome: "allow"},
		Source:         cp.SourceRef{Name: "test"},
	}
}

func TestRecorderDisabledReturnsErrDisabledAndDoesNotTouchStore(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityDisabled, RecordingPolicy: cp.RecordingDisabled})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingDisabled,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err := rec.Record(context.Background(), recorderEvent())
	if !errors.Is(err, controlplane.ErrDisabled) {
		t.Fatalf("disabled recorder must return ErrDisabled, got %v", err)
	}
	if store.appends.Load() != 0 {
		t.Fatalf("disabled recorder must not call store.Append, got %d", store.appends.Load())
	}
}

func TestRecorderBestEffortRecordsAndReturnsResult(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	res, err := rec.Record(context.Background(), recorderEvent())
	if err != nil {
		t.Fatalf("best-effort record returned error: %v", err)
	}
	if res.Dedupe != cp.DedupeInserted || res.ID.IsZero() {
		t.Fatalf("best-effort record result lost: %#v", res)
	}
	if store.appends.Load() != 1 {
		t.Fatalf("expected one append, got %d", store.appends.Load())
	}
}

func TestRecorderBestEffortFailureDegradesStatusButPreservesRequestOutcome(t *testing.T) {
	t.Parallel()
	store := &recordingStore{appendErr: errors.New("transient store down")}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err := rec.Record(context.Background(), recorderEvent())
	if err != nil {
		t.Fatalf("best-effort failure must not surface error to caller (preserve request outcome), got %v", err)
	}
	got := status.Snapshot()
	if got.State != cp.CapabilityDegraded || got.Reason != cp.ReasonRecordingFailure {
		t.Fatalf("status must become degraded with recording_failure, got %#v", got)
	}
}

func TestRecorderRequiredPreWorkFailureFailsClosedBeforeUpstream(t *testing.T) {
	t.Parallel()
	store := &recordingStore{appendErr: errors.New("store down")}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingRequiredPreWork})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAuth},
		Clock:    fixedClock{t: time.Now()},
	})
	_, err := rec.Record(context.Background(), recorderEvent())
	if err == nil {
		t.Fatalf("required pre-work failure must return error to fail closed before upstream work")
	}
	if !errors.Is(err, controlplane.ErrUnavailable) && !errors.Is(err, controlplane.ErrDegraded) {
		t.Fatalf("required pre-work failure must classify as unavailable or degraded, got %v", err)
	}
	got := status.Snapshot()
	if got.State != cp.CapabilityDegraded && got.State != cp.CapabilityUnavailable {
		t.Fatalf("status must reflect failure after required pre-work failure, got %q", got.State)
	}
}

func TestRecorderRequiredPreWorkSucceedsForBestEffortCategory(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingRequiredPreWork})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAuth},
		Clock:    fixedClock{t: time.Now()},
	})
	// usage category is NOT in required set, so required_pre_work policy treats it as best-effort.
	ev := recorderEvent()
	ev.Category = cp.CategoryUsage
	ev.Auth = nil
	ev.Usage = &cp.UsageDetail{Plane: cp.UsagePlaneObserved, Availability: cp.UsageAvailabilityObserved}
	_, err := rec.Record(context.Background(), ev)
	if err != nil {
		t.Fatalf("non-required category under required_pre_work must be best-effort, got %v", err)
	}
}

func TestRecorderBestEffortStoreSideUnsafeEvidenceSurfacesWithoutDegrading(t *testing.T) {
	t.Parallel()
	store := &recordingStore{appendErr: fmt.Errorf("%w: store rejected evidence", controlplane.ErrUnsafeEvidence)}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	_, err := rec.RecordBestEffort(context.Background(), recorderEvent())
	if !errors.Is(err, controlplane.ErrUnsafeEvidence) {
		t.Fatalf("RecordBestEffort must surface store-side ErrUnsafeEvidence, got %v", err)
	}
	got := status.Snapshot()
	if got.State != cp.CapabilityReady {
		t.Fatalf("store-side unsafe evidence must not degrade status, got %q", got.State)
	}
}

func TestRecorderBestEffortPathNeverFailsClosedForPostOutput(t *testing.T) {
	t.Parallel()
	store := &recordingStore{appendErr: errors.New("post-output failure")}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingRequiredPreWork})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy:   cp.RecordingRequiredPreWork,
		Required: []cp.Category{cp.CategoryAuth},
		Clock:    fixedClock{t: time.Now()},
	})
	// RecordBestEffort is the post-output / non-protected path: it must never
	// fail closed, even for a category that is otherwise required pre-work.
	_, err := rec.RecordBestEffort(context.Background(), recorderEvent())
	if err != nil {
		t.Fatalf("RecordBestEffort must never fail closed (no retry/failover/replacement), got %v", err)
	}
	if got := status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("post-output failure must degrade status, got %q", got)
	}
}

func TestRecorderRejectsUnsafeEvidenceRegardlessOfPolicy(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	ev := recorderEvent()
	ev.Summary = "Bearer secrettoken"
	if _, err := rec.Record(context.Background(), ev); err == nil {
		t.Fatalf("recorder must reject unsafe evidence regardless of policy")
	}
	if store.appends.Load() != 0 {
		t.Fatalf("recorder must not call store.Append for unsafe evidence")
	}
}

func TestRecorderStatusReflectsCurrentState(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	rec := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: time.Now()},
	})
	got, err := rec.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.State != cp.CapabilityReady {
		t.Fatalf("Status must report ready, got %q", got.State)
	}
}
