package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	coreauth "github.com/matdev83/go-llm-interactive-proxy/internal/core/auth"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkauth "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type sessionStartSink struct {
	evs []sdkauth.SessionStartEvent
}

func (s *sessionStartSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error {
	return nil
}

func (s *sessionStartSink) OnSessionStart(_ context.Context, ev sdkauth.SessionStartEvent) error {
	if s == nil {
		return nil
	}
	s.evs = append(s.evs, ev)
	return nil
}

func testSessionAuditPolicy() coreauth.SessionAuditPolicy {
	return coreauth.SessionAuditPolicy{
		AccessMode:    sdkauth.AccessSingleUser,
		HandlerKind:   sdkauth.HandlerLocalNoop,
		RequiredLevel: sdkauth.LevelNone,
	}
}

func TestExecutor_prepareSubmitAndALeg_sessionStart_newSecureSession_emitsOnce(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	sink := &sessionStartSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:              b2,
		Bus:                hooks.New(hooks.Config{}),
		RuntimeSnapshot:    snap,
		SecureSession:      mgr,
		Now:                func() time.Time { return time.Unix(3000, 0).UTC() },
		AuthEvents:         disp,
		SessionAuditPolicy: testSessionAuditPolicy(),
	})
	ctx := execview.WithFrontendID(
		execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-a", DisplayName: "Alice"}),
		"anthropic",
	)
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "client-hint-6-1"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	traceID, _, aLeg, _, err := ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.evs) != 1 {
		t.Fatalf("session-start events: want 1 got %d", len(sink.evs))
	}
	ev := sink.evs[0]
	if ev.TraceID != traceID {
		t.Fatalf("trace_id: want %q got %q", traceID, ev.TraceID)
	}
	if ev.PrincipalID != "user-a" || ev.PrincipalDisplayName != "Alice" {
		t.Fatalf("principal: %#v", ev)
	}
	if !ev.IsNew || ev.Certainty != sdkauth.SessionCertaintyKnown {
		t.Fatalf("is_new/certainty: %#v", ev)
	}
	if ev.SessionID == "" || ev.ALegID != aLeg.ALegID {
		t.Fatalf("session_id=%q a_leg=%q want non-empty session and matching a-leg", ev.SessionID, ev.ALegID)
	}
	if ev.ClientSessionRef != coreauth.OpaqueRefDigest("client-hint-6-1") {
		t.Fatalf("client ref digest: %q", ev.ClientSessionRef)
	}
	if ev.AccessMode != sdkauth.AccessSingleUser || ev.HandlerKind != sdkauth.HandlerLocalNoop {
		t.Fatalf("policy snapshot: %#v", ev)
	}
	if ev.Frontend != "anthropic" {
		t.Fatalf("frontend: want anthropic got %q", ev.Frontend)
	}
}

func TestExecutor_prepareSubmitAndALeg_sessionStart_proxySessionIDNotRawClientHint(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	sink := &sessionStartSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:              b2,
		Bus:                hooks.New(hooks.Config{}),
		RuntimeSnapshot:    snap,
		SecureSession:      mgr,
		Now:                func() time.Time { return time.Unix(3000, 0).UTC() },
		AuthEvents:         disp,
		SessionAuditPolicy: testSessionAuditPolicy(),
	})
	ctx := execview.WithFrontendID(
		execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-a", DisplayName: "Alice"}),
		"anthropic",
	)
	const clientHint = "client-controlled-hint-id"
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: clientHint},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.evs) != 1 {
		t.Fatalf("session-start events: want 1 got %d", len(sink.evs))
	}
	ev := sink.evs[0]
	if ev.SessionID == clientHint {
		t.Fatalf("authoritative session id must not equal client session hint %q", clientHint)
	}
	if ev.ClientSessionRef != coreauth.OpaqueRefDigest(clientHint) {
		t.Fatalf("client ref should be digest of hint, got %q", ev.ClientSessionRef)
	}
}

func TestExecutor_prepareSubmitAndALeg_sessionStart_resumeDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	sink := &sessionStartSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:              b2,
		Bus:                hooks.New(hooks.Config{}),
		RuntimeSnapshot:    snap,
		SecureSession:      mgr,
		Now:                func() time.Time { return time.Unix(3100, 0).UTC() },
		AuthEvents:         disp,
		SessionAuditPolicy: testSessionAuditPolicy(),
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-b"})
	call1 := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "resume-hint"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.evs) != 1 {
		t.Fatalf("after new: want 1 session-start got %d", len(sink.evs))
	}
	resumeTok := call1.Session.ResumeToken
	if resumeTok == "" {
		t.Fatal("expected resume token on new session for second turn")
	}
	call2 := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID:        "resume-hint",
			AuthoritativeSessionID: call1.Session.AuthoritativeSessionID,
			ResumeToken:            resumeTok,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("again")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call2)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.evs) != 1 {
		t.Fatalf("after resume: want still 1 session-start got %d", len(sink.evs))
	}
}

func TestExecutor_prepareSubmitAndALeg_sessionStart_resumeTokenNotLeakedIntoEvent(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	sink := &sessionStartSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:              b2,
		Bus:                hooks.New(hooks.Config{}),
		RuntimeSnapshot:    snap,
		SecureSession:      mgr,
		Now:                func() time.Time { return time.Unix(3200, 0).UTC() },
		AuthEvents:         disp,
		SessionAuditPolicy: testSessionAuditPolicy(),
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-c"})
	const secretMarker = "resume-proof-SECRET-MARKER-99331"
	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "ok-hint",
			ResumeToken:     secretMarker, // invalid resume; still test redaction path on new session path only if we used new - invalid resume fails prepare
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err == nil {
		t.Fatal("expected invalid resume to fail prepare")
	}
	if len(sink.evs) != 0 {
		t.Fatalf("no session-start on failed prepare, got %d", len(sink.evs))
	}

	// New session: client hint contains secret-like material; event must carry digest only.
	call2 := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "prefix-" + secretMarker + "-suffix"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call2)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.evs) != 1 {
		t.Fatalf("want 1 event got %d", len(sink.evs))
	}
	ev := sink.evs[0]
	if ev.ClientSessionRef == "" {
		t.Fatal("expected client session ref digest")
	}
	joined := strings.Join([]string{
		ev.TraceID, ev.SessionID, ev.ClientSessionRef, ev.ALegID,
		ev.PrincipalID, ev.PrincipalDisplayName, string(ev.AccessMode),
		string(ev.HandlerKind), string(ev.RequiredLevel), string(ev.Certainty),
		fmt.Sprint(ev.IsNew),
	}, "|")
	if strings.Contains(joined, secretMarker) {
		t.Fatalf("event record must not contain raw client session material: %q", joined)
	}
	if ev.ClientSessionRef != coreauth.OpaqueRefDigest("prefix-"+secretMarker+"-suffix") {
		t.Fatalf("client ref should be digest of full hint, got %q", ev.ClientSessionRef)
	}
}

func TestExecutor_prepareSubmitAndALeg_sessionStart_syntheticPrincipal_emitsPartialCertainty(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	sink := &sessionStartSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:                   b2,
		Bus:                     hooks.New(hooks.Config{}),
		RuntimeSnapshot:         snap,
		SecureSession:           mgr,
		Now:                     func() time.Time { return time.Unix(3300, 0).UTC() },
		AuthEvents:              disp,
		SessionAuditPolicy:      testSessionAuditPolicy(),
		SyntheticLocalPrincipal: true,
	})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "synth-hint"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(context.Background(), ex.Bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.evs) != 1 {
		t.Fatalf("want 1 session-start got %d", len(sink.evs))
	}
	if sink.evs[0].Certainty != sdkauth.SessionCertaintyPartial {
		t.Fatalf("want partial certainty, got %q", sink.evs[0].Certainty)
	}
}

type errSessionSink struct{}

func (errSessionSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error { return nil }

func (errSessionSink) OnSessionStart(context.Context, sdkauth.SessionStartEvent) error {
	return errors.New("sink error")
}

func TestExecutor_prepareSubmitAndALeg_sessionStart_failClosed_propagates(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	disp := coreauth.NewEventDispatcher(errSessionSink{}, coreauth.EventFailureFailClosed)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(&Executor{
		Store:              b2,
		Bus:                hooks.New(hooks.Config{}),
		RuntimeSnapshot:    snap,
		SecureSession:      mgr,
		Now:                func() time.Time { return time.Unix(3400, 0).UTC() },
		AuthEvents:         disp,
		SessionAuditPolicy: testSessionAuditPolicy(),
	})
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-fc"})
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ClientSessionID: "fc-hint"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err == nil {
		t.Fatal("expected fail-closed session-start delivery to fail prepare")
	}
}
