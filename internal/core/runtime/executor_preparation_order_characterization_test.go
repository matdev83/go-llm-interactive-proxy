package runtime

import (
	"context"
	"crypto/rand"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type recordingStore struct {
	app.Store
	mu     *sync.Mutex
	events *[]string
}

func (s *recordingStore) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	s.mu.Lock()
	if !slices.Contains(*s.events, "SecureSession.BeginTurn") {
		*s.events = append(*s.events, "SecureSession.BeginTurn")
	}
	s.mu.Unlock()
	return s.Store.Create(ctx, rec)
}

func (s *recordingStore) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, src domain.ActivitySource) error {
	s.mu.Lock()
	if !slices.Contains(*s.events, "SecureSession.BeginTurn") {
		*s.events = append(*s.events, "SecureSession.BeginTurn")
	}
	s.mu.Unlock()
	return s.Store.TouchActivity(ctx, id, at, src)
}

type recordingB2BuaStore struct {
	b2bua.Store
	mu       *sync.Mutex
	events   *[]string
	executor *Executor
}

func (s *recordingB2BuaStore) FetchALeg(ctx context.Context, id string) (b2bua.ALegRecord, error) {
	s.mu.Lock()
	*s.events = append(*s.events, "FetchALeg")
	s.mu.Unlock()
	if s.executor != nil && s.executor.Keepwarm != nil {
		ctl := &fixedResultController{}
		now := time.Unix(2000, 0)
		exp := now.Add(time.Hour)
		obs := []promptcache.Observation{
			{
				ALegID:            id,
				BLegID:            "b-leg-1",
				BackendInstanceID: "backend-1",
				TargetID:          "target-1",
				GenerationID:      "gen-1",
				Lifecycle:         promptcache.LifecycleSlidingExpiry,
				Timing:            promptcache.Timing{ObservedAt: now, ExpiresAt: &exp},
				Renewable:         true,
				Handle:            promptcache.Handle("handle-1"),
			},
		}
		_ = s.executor.Keepwarm.ArmCommittedTurn(keepwarm.CommittedTurn(
			id,
			"b-leg-1",
			"backend-1",
			"model-1",
			[]lipapi.ToolEvent{{Kind: lipapi.ToolEventFinished, Category: lipapi.ToolCategoryOSCommand}},
			obs,
			ctl,
		))
	}
	return s.Store.FetchALeg(ctx, id)
}

type spyConcurrencyProvider struct {
	authority.ConcurrencyProvider
	mu     *sync.Mutex
	events *[]string
}

func (s *spyConcurrencyProvider) AdmitLease(ctx context.Context, in authority.LeaseAdmission) (authority.LeaseDecision, error) {
	s.mu.Lock()
	*s.events = append(*s.events, "RequestAdmission")
	s.mu.Unlock()
	return authority.LeaseDecision{
		Kind:       authority.LeaseAllow,
		LeaseID:    "lease-123",
		Generation: 1,
		ExpiresAt:  time.Unix(2000000000, 0),
	}, nil
}

type spySubmitHook struct {
	mu     *sync.Mutex
	events *[]string
}

func (s spySubmitHook) ID() string                        { return "spy_submit" }
func (s spySubmitHook) Order() int                        { return 0 }
func (s spySubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s spySubmitHook) Handle(ctx context.Context, call *lipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	s.mu.Lock()
	*s.events = append(*s.events, "SubmitHooks")
	s.mu.Unlock()
	return sdkhooks.SubmitDecision{}, nil
}

func isCaller(name string) bool {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, name) {
			return true
		}
		if !more {
			break
		}
	}
	return false
}

func TestExecutor_PreparationOrderCharacterization(t *testing.T) {
	ctx := context.Background()
	var events []string
	var mu sync.Mutex

	// 1. Setup B2BUA Store
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Setup SecureSession Store
	memSS := memory.New(memory.Options{SimulateDurable: true})
	recSSStore := &recordingStore{Store: memSS, mu: &mu, events: &events}

	// 4. Setup Executor
	ex := setSecureSessionDenialMapper(TestExecutor())
	recB2Store := &recordingB2BuaStore{Store: b2, mu: &mu, events: &events, executor: ex}
	ex.Store = recB2Store
	ex.SyntheticLocalPrincipal = true
	ex.SnapshotGeneration = nil

	// 3. Setup Secure Manager
	mgr, err := app.NewManager(recSSStore, app.NewRandGenerator(testFingerprintKey32(t)), b2bualineage.New(recB2Store), app.ManagerConfig{
		FingerprintKey: testFingerprintKey32(t),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex.SecureSession = mgr

	// Setup Keepwarm
	kwHooks := keepwarm.Hooks{
		Metric: func(name string) {
			if name == "cancel_foreground" {
				mu.Lock()
				events = append(events, "Keepwarm.BeginRealTurn")
				mu.Unlock()
			}
		},
	}
	cfg := keepwarm.DefaultConfig()
	kwMgr, err := keepwarm.NewManager(cfg, keepwarm.ClockFunc(func() time.Time {
		if ex.Now == nil {
			return time.Now().UTC()
		}
		return ex.Now()
	}), kwHooks)
	if err != nil {
		t.Fatal(err)
	}
	ex.Keepwarm = keepwarm.NewOrchestrator(kwMgr)

	// Hook submit hook
	ex.Bus = hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{spySubmitHook{mu: &mu, events: &events}},
	})

	// Setup Runtime Snapshot with a resolver
	snap := extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})
	ex.RuntimeSnapshot = snap

	// 5. Setup Route Authority Snapshot Barrier
	barrier := newRouteAuthoritySnapshotBarrier()
	ctx = withRouteAuthoritySnapshotBarrier(ctx, barrier)

	// Watch barrier arrival in background
	go func() {
		err := barrier.waitUntilArrived(ctx)
		if err == nil {
			mu.Lock()
			events = append(events, "RouteAuthoritySnapshotBarrier")
			mu.Unlock()
			barrier.releaseWaiters()
		}
	}()

	// 7. Override clock to intercept metering capture
	ex.Now = func() time.Time {
		select {
		case <-barrier.arrived:
			mu.Lock()
			if !slices.Contains(events, "MeteringCapture") {
				events = append(events, "MeteringCapture")
			}
			mu.Unlock()
		default:
		}
		return time.Unix(2000, 0).UTC()
	}

	// 6. Setup Request Coordinator
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Concurrency: &spyConcurrencyProvider{mu: &mu, events: &events},
		Now:         ex.Now,
	}

	call := &lipapi.Call{
		Session: lipapi.SessionRef{
			ClientSessionID: "c-order",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	// Intercept rand.Reader for billing ID generation
	oldRandReader := rand.Reader
	rand.Reader = &customRandReader{
		Reader: oldRandReader,
		onRead: func() {
			if isCaller("stampBillingCallID") {
				mu.Lock()
				events = append(events, "Billing.NewBillingCallID")
				mu.Unlock()
			}
		},
	}
	defer func() { rand.Reader = oldRandReader }()

	// Execute preparation
	pr, prepCtx, closeFn, err := ex.prepareRequest(ctx, call)
	if err != nil {
		t.Fatalf("prepareRequest failed: %v", err)
	}
	defer func() {
		closeFn()
		if ex.Keepwarm != nil {
			_ = ex.Keepwarm.Quiesce(ctx)
		}
	}()

	// Ensure the A-leg ID matches
	if v, ok := execctx.FromContext(prepCtx); ok {
		if v.Session.ALegID == "" {
			t.Fatal("expected aleg ID to be populated")
		}
	}

	// Verify order
	expectedOrder := []string{
		"SecureSession.BeginTurn",
		"FetchALeg",
		"RouteAuthoritySnapshotBarrier",
		"MeteringCapture",
		"RequestAdmission",
		"SubmitHooks",
		"Keepwarm.BeginRealTurn",
		"Billing.NewBillingCallID",
	}

	mu.Lock()
	actualEvents := append([]string(nil), events...)
	mu.Unlock()

	if len(actualEvents) != len(expectedOrder) {
		t.Fatalf("expected %d events, got %d. Expected: %v, Got: %v", len(expectedOrder), len(actualEvents), expectedOrder, actualEvents)
	}

	for i, expected := range expectedOrder {
		if actualEvents[i] != expected {
			t.Errorf("event %d: expected %s, got %s. Full trace: %v", i, expected, actualEvents[i], actualEvents)
		}
	}

	// Verify that A-leg scope is non-nil and was started after billing (which completed billing setup)
	if pr.aScope == nil {
		t.Error("expected A-leg scope to be initialized at the end of preparation")
	}
}

type fixedResultController struct{}

func (c *fixedResultController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	return promptcache.RenewResponse{}, nil
}

func (c *fixedResultController) Release(ctx context.Context, req promptcache.ReleaseRequest) error {
	return nil
}

type customRandReader struct {
	io.Reader
	onRead func()
}

func (r *customRandReader) Read(p []byte) (n int, err error) {
	if r.onRead != nil {
		r.onRead()
	}
	return r.Reader.Read(p)
}
