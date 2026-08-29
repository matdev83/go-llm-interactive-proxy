package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func TestExecutor_secretGuardBlock_quarantinesSession(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS2{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []secretguard.Guard{&blockingSecretGuard{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1900, 0) }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				t.Fatal("backend must not open after block")
				return nil, errors.New("unreachable")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-q"})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	stream, execErr := ex.Execute(ctx, call)
	if stream != nil {
		_, _ = lipapi.Collect(t.Context(), stream)
	}
	if !lipapi.IsPolicyDenied(execErr) {
		t.Fatalf("want policy denied, got %v", execErr)
	}

	sums, err := memSS.Summary(t.Context(), domain.SummaryQuery{OwnerID: "user-q", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 {
		t.Fatalf("summaries: %d", len(sums))
	}
	rec, err := memSS.LoadByID(t.Context(), sums[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Status.IsQuarantined() || rec.ResumeEligible {
		t.Fatalf("quarantine state: status=%q resume=%v", rec.Status, rec.ResumeEligible)
	}
	if err := mgr.AssertActive(t.Context(), rec.SessionID); !errors.Is(err, domain.ErrSessionQuarantined) {
		t.Fatalf("AssertActive: %v", err)
	}
}

func TestExecutor_quarantinePersistenceFailure_failClosed(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fake := &testkit.FakeSecureSessionStore{
		Delegate:      memSS,
		QuarantineErr: errors.New("disk full"),
	}
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(fake, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS2{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []secretguard.Guard{&blockingSecretGuard{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1901, 0) }
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(2)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-qf"})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	stream, execErr := ex.Execute(ctx, call)
	if stream != nil {
		_, _ = lipapi.Collect(t.Context(), stream)
	}
	if opens.Load() != 0 {
		t.Fatal("backend must not open when quarantine persistence fails")
	}
	if execErr == nil {
		t.Fatal("expected denial")
	}
	if lipapi.IsPolicyDenied(execErr) {
		t.Fatal("persistence failure must not surface as allowable policy-only path; want storage denial")
	}
	code := lipapi.SessionDenialPublicCode(execErr)
	if code != string(lipapi.SessionDeniedStorageUnavailable) {
		t.Fatalf("denial code: %q want %q (err=%v)", code, lipapi.SessionDeniedStorageUnavailable, execErr)
	}
	if strings.Contains(execErr.Error(), "disk full") {
		t.Fatal("client error must not include internal quarantine failure detail")
	}
	for _, needle := range testkit.AllSyntheticSecretGuardNeedles() {
		if strings.Contains(execErr.Error(), needle) {
			t.Fatalf("client error must not include synthetic secret substring %q", needle)
		}
	}
	if fake.QuarantineCalls == 0 {
		t.Fatal("Quarantine must have been attempted")
	}
}

func TestExecutor_secretGuardBlock_emptySessionID_failClosed(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS2{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []secretguard.Guard{&blockingSecretGuard{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1902, 0) }

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	execErr := ex.RunSecretGuardStageForTest(t.Context(), call, runtime.SecretGuardStageInputForTest{
		TraceID:   "trace-empty-sid",
		Principal: execview.PrincipalView{ID: "user-empty-sid"},
		SessionID: "", // invariant: session plane present, no ID to quarantine
		TurnID:    "turn-empty",
	})
	if execErr == nil {
		t.Fatal("expected fail-closed denial")
	}
	if lipapi.IsPolicyDenied(execErr) {
		t.Fatal("empty SessionID must not surface as soft policy-only denial")
	}
	code := lipapi.SessionDenialPublicCode(execErr)
	if code != string(lipapi.SessionDeniedStorageUnavailable) {
		t.Fatalf("denial code: %q want %q (err=%v)", code, lipapi.SessionDeniedStorageUnavailable, execErr)
	}
	if !ex.QuarantinePersistenceFaulted() {
		t.Fatal("quarantine persistence fault must be latched")
	}
	for _, needle := range testkit.AllSyntheticSecretGuardNeedles() {
		if strings.Contains(execErr.Error(), needle) {
			t.Fatalf("client error must not include synthetic secret substring %q", needle)
		}
	}
}

func TestPreDispatchAssertActive_raceAfterQuarantine(t *testing.T) {
	t.Parallel()
	memSS := memory.New(memory.Options{SimulateDurable: true})
	ctx := t.Context()
	fp := domain.TokenFingerprint{9, 8, 7}
	rec, err := memSS.Create(ctx, domain.CreateRecord{
		SessionID:         "sess-race",
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: "u"},
		ALegID:            "aleg-race",
		ResumeEligible:    true,
		CreatedAt:         time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	var start, released sync.WaitGroup
	start.Add(1)
	released.Add(1)
	errCh := make(chan error, 1)
	go func() {
		start.Wait()
		// Simulate paused turn between guard and backend open: re-check session.
		released.Wait()
		errCh <- memSSAssertActive(ctx, memSS, rec.SessionID)
	}()

	start.Done()
	if err := memSS.Quarantine(ctx, domain.QuarantineInput{
		SessionID:  rec.SessionID,
		TurnID:     "turn-block",
		ReasonCode: "secret_guard_block",
		EventID:    "evt-race",
		At:         time.Unix(2, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	released.Done()
	if err := <-errCh; !errors.Is(err, domain.ErrSessionQuarantined) {
		t.Fatalf("paused turn after quarantine: got %v want ErrSessionQuarantined", err)
	}
}

func memSSAssertActive(ctx context.Context, s *memory.Store, id domain.SessionID) error {
	rec, err := s.LoadByID(ctx, id)
	if err != nil {
		return err
	}
	if rec.Status.IsQuarantined() {
		return domain.ErrSessionQuarantined
	}
	return nil
}
