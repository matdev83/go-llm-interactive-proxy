package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

type scriptedALegStore struct {
	resolveSeq []b2bua.ALegRecord
	byID       map[string]b2bua.ALegRecord
	resolveIdx int
}

func newScriptedALegStore(records ...b2bua.ALegRecord) *scriptedALegStore {
	byID := make(map[string]b2bua.ALegRecord, len(records))
	for _, rec := range records {
		byID[rec.ALegID] = rec
	}
	return &scriptedALegStore{
		resolveSeq: records,
		byID:       byID,
	}
}

func (s *scriptedALegStore) ResolveALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return b2bua.ALegRecord{}, err
	}
	if s.resolveIdx >= len(s.resolveSeq) {
		return b2bua.ALegRecord{}, errors.New("unexpected ResolveALeg call")
	}
	rec := s.resolveSeq[s.resolveIdx]
	s.resolveIdx++
	return rec, nil
}

func (s *scriptedALegStore) CreateALeg(context.Context, string) (b2bua.ALegRecord, error) {
	return b2bua.ALegRecord{}, errors.New("unexpected CreateALeg call")
}

func (s *scriptedALegStore) FetchALeg(ctx context.Context, aLegID string) (b2bua.ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return b2bua.ALegRecord{}, err
	}
	rec, ok := s.byID[aLegID]
	if !ok {
		return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
	}
	return rec, nil
}

func (s *scriptedALegStore) SetWeightedFirstConsumed(context.Context, string, bool) error {
	return errors.New("unexpected SetWeightedFirstConsumed call")
}

func (s *scriptedALegStore) NextBLeg(context.Context, string) (b2bua.BLegRecord, error) {
	return b2bua.BLegRecord{}, errors.New("unexpected NextBLeg call")
}

func (s *scriptedALegStore) RecordAttempt(context.Context, lipapi.AttemptRecord) error {
	return errors.New("unexpected RecordAttempt call")
}

func (s *scriptedALegStore) LoadAttempts(context.Context, string) ([]lipapi.AttemptRecord, error) {
	return nil, errors.New("unexpected LoadAttempts call")
}

type failingSubmitHook struct{}

func (failingSubmitHook) ID() string                        { return "submit-fail" }
func (failingSubmitHook) Order() int                        { return 0 }
func (failingSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (failingSubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, errors.New("submit boom")
}

type captureSessionOpener struct {
	seen   session.OpenInput
	labels map[string]string
}

func (o *captureSessionOpener) ID() string { return "capture-session" }

func (o *captureSessionOpener) Open(_ context.Context, in session.OpenInput) (session.OpenResult, error) {
	o.seen = in
	return session.OpenResult{SessionLabelUpserts: o.labels}, nil
}

func TestExecutor_prepareSubmitAndALeg_preservesTraceOnSubmitError(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	store := newScriptedALegStore(
		b2bua.ALegRecord{
			ALegID:        "aleg-pre",
			ContinuityKey: "ck-1",
			CreatedAt:     now,
			LastSeenAt:    now,
		},
	)
	bus := hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{failingSubmitHook{}},
	})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "client-1",
			ContinuityKey:   "ck-1",
		},
	}
	ex := &Executor{Store: store}

	traceID, _, _, outCtx, err := ex.prepareSubmitAndALeg(context.Background(), bus, call)
	if err == nil {
		t.Fatal("expected submit error")
	}
	if traceID != "" {
		t.Fatalf("trace id return on error: want empty got %q", traceID)
	}
	if call.ID == "" {
		t.Fatal("expected helper to assign call id")
	}
	if got := diag.TraceID(outCtx); got != call.ID {
		t.Fatalf("returned context trace id: want %q got %q", call.ID, got)
	}
	if got := diag.ALegID(outCtx); got != "" {
		t.Fatalf("returned context aleg id: want empty got %q", got)
	}
	if store.resolveIdx != 1 {
		t.Fatalf("ResolveALeg calls: want 1 got %d", store.resolveIdx)
	}
}

func TestExecutor_prepareSubmitAndALeg_usesPreResolveSessionForOpenersAndPostResolveAlegForViews(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	preALeg := b2bua.ALegRecord{
		ALegID:        "aleg-pre",
		ContinuityKey: "ck-2",
		CreatedAt:     now,
		LastSeenAt:    now,
	}
	postALeg := b2bua.ALegRecord{
		ALegID:        "aleg-post",
		ContinuityKey: "ck-2",
		CreatedAt:     now,
		LastSeenAt:    now.Add(2 * time.Minute),
	}
	store := newScriptedALegStore(preALeg, postALeg)
	opener := &captureSessionOpener{
		labels: map[string]string{"opened": "yes"},
	}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		SessionOpeners: []session.Opener{opener},
	})
	ex := &Executor{
		Store:           store,
		RuntimeSnapshot: snap,
	}
	bus := hooks.New(hooks.Config{})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "client-2",
			ContinuityKey:   "ck-2",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}

	traceID, _, aLeg, outCtx, err := ex.prepareSubmitAndALeg(context.Background(), bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if aLeg.ALegID != postALeg.ALegID {
		t.Fatalf("a-leg id: want %q got %q", postALeg.ALegID, aLeg.ALegID)
	}
	if call.Session.ALegID != postALeg.ALegID {
		t.Fatalf("call session aleg id: want %q got %q", postALeg.ALegID, call.Session.ALegID)
	}
	if opener.seen.Session.SessionID != "client-2" {
		t.Fatalf("opener saw session id: want client-2 got %q", opener.seen.Session.SessionID)
	}
	if opener.seen.Session.ALegID != preALeg.ALegID {
		t.Fatalf("opener saw aleg id: want %q got %q", preALeg.ALegID, opener.seen.Session.ALegID)
	}
	if !opener.seen.Session.IsNew {
		t.Fatal("opener should see the pre-resolve session as new")
	}

	views, ok := execctx.FromContext(outCtx)
	if !ok {
		t.Fatal("expected execctx views on returned context")
	}
	if views.Session.ALegID != postALeg.ALegID {
		t.Fatalf("views aleg id: want %q got %q", postALeg.ALegID, views.Session.ALegID)
	}
	if views.Session.IsNew {
		t.Fatal("views should reflect the post-resolve session as not new")
	}
	if views.Session.Labels["opened"] != "yes" {
		t.Fatalf("views session labels: %v", views.Session.Labels)
	}
	if got := diag.TraceID(outCtx); got != traceID {
		t.Fatalf("returned context trace id: want %q got %q", traceID, got)
	}
	if store.resolveIdx != 2 {
		t.Fatalf("ResolveALeg calls: want 2 got %d", store.resolveIdx)
	}
}
