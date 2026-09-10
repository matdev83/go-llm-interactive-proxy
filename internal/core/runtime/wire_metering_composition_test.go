package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// TestWireComposition_NoAccounting_ReachesWireModeAndCompletesLifecycle proves
// that with token accounting and preflight disabled, a standard production composition
// (SecureSession + SessionRecorder + MeteringRecorder + B2BUA Store) reaches wire mode
// and completes the full wire lifecycle across FE ingress, BE attempt, failover attempt,
// and turn recording with zero Call retention (Requirements 15.1–15.4, 21.6; Task 10.3).
func TestWireComposition_NoAccounting_ReachesWireModeAndCompletesLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715624000, 0).UTC()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})

	factRecorder := &memoryFactRecorder{}
	turnRecorder := &capturingTurnRecorder{}

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SecureSessionRecorder = turnRecorder
	ex.MeteringRecorder = factRecorder
	ex.Now = func() time.Time { return now }

	// Ensure token accounting / preflight is disabled (no accounting ports occupied)
	ex.Preflight = nil
	ex.AdminCountService = nil
	ex.StreamUsage = nil

	// Step 1: Wire Eligibility Compilation Proof (Req 15.4, 21.6)
	// Build generation-pinned eligibility with disabled accounting ports.
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := 0; i < largebody.WireEligibilityPlaneCount; i++ {
		id, ok := largebody.WireEligibilityPlaneID(i)
		if !ok {
			t.Fatalf("missing plane ID for index %d", i)
		}
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID: "gen-no-accounting-v1",
		Planes:       planes,
		Hooks:        largebody.HookEligibilityInput{},
		Ports: largebody.NarrowPortEligibilityInput{
			BackendsEmpty:             false,
			PreflightEnabled:          false,
			PreflightHasExactCounter:  false,
			AdminCountServiceOccupied: false,
			StreamUsageOccupied:       false,
			TokenCountingRequired:     false,
		},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary failed: %v", err)
	}

	if summary.HasStaticBlocker() {
		t.Fatalf("expected no static blocker with disabled accounting, got blockers: plane=%#x hook=%#x port=%#x",
			summary.PlaneBlockers(), uint8(summary.HookBlockers()), uint32(summary.PortBlockers()))
	}

	// Static disposition must reach wire assessment mode
	disposition, reason := largebody.StaticDisposition(summary, largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-no-accounting-v1",
		ThresholdBytes: 64 * 1024,
		HasKnownLength: true,
		ContentLength:  128 * 1024,
	})
	if disposition != largebody.StaticWireNeedsRequestAssessment {
		t.Fatalf("expected NeedsRequestAssessment, got %v (reason: %v)", disposition, reason)
	}
	if reason != largebody.StaticWireReasonNone {
		t.Fatalf("expected ReasonNone, got %v", reason)
	}

	// Step 2: Prepare Secure Session & Turn Execution (9.x seam)
	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("user-comp-001"),
		TenantID:    scope.Known("tenant-comp-001"),
	}
	baseCtx := execview.WithFrontendID(context.Background(), "openai_responses")
	baseCtx = execview.WithPrincipal(baseCtx, execview.PrincipalView{ID: "user-comp-001"})
	baseCtx = scope.WithScope(baseCtx, sc)

	traceID := "trace-comp-001"
	requestID := "req-comp-001"
	maxOutput := 4096

	prep, err := ex.PrepareSecureSession(baseCtx, SecureSessionPrepInput{
		TraceID: traceID,
		Session: largebody.SessionInput{
			ClientSessionID:     "client-sess-comp-001",
			NewSessionRequested: true,
		},
		ContinuityKey: "ck-comp-001",
	})
	if err != nil {
		t.Fatalf("PrepareSecureSession failed: %v", err)
	}

	br, err := prep.ExecuteBeginTurn(prep.Context())
	if err != nil {
		t.Fatalf("ExecuteBeginTurn failed: %v", err)
	}
	aLeg, _, err := prep.ResolveALeg(prep.Context(), br.Record.ALegID)
	if err != nil {
		t.Fatalf("ResolveALeg failed: %v", err)
	}

	// Record client turn using bounded ClientTurnShape without prompt text (Req 14.3, 14.5)
	shape := largebody.ClientTurnShape{
		Items: []largebody.ClientTurnItemShape{
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    "system",
				Ordinal: 0,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: lipapi.ContentPartText, ContentBytes: 50},
				},
			},
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    "user",
				Ordinal: 1,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: lipapi.ContentPartText, ContentBytes: 200},
					{Kind: lipapi.ContentPartImageRef, ContentBytes: 150},
				},
			},
		},
	}
	if err := prep.RecordClientTurnWithShape(prep.Context(), br, shape, 4096); err != nil {
		t.Fatalf("RecordClientTurnWithShape failed: %v", err)
	}
	if len(turnRecorder.recorded) != 1 {
		t.Fatalf("recorded turns len=%d want 1", len(turnRecorder.recorded))
	}
	recTurn := turnRecorder.recorded[0]
	if recTurn.TraceID != traceID || len(recTurn.Lines) != 2 {
		t.Fatalf("recorded turn lines mismatch: %+v", recTurn)
	}

	// Step 3: Wire Frontend Ingress Checkpoint (10.1 seam)
	outCtx, holder, err := prep.CaptureFrontendIngressCheckpoint(
		prep.Context(),
		requestID,
		br,
		aLeg,
		&maxOutput,
	)
	if err != nil {
		t.Fatalf("CaptureFrontendIngressCheckpoint failed: %v", err)
	}
	if holder == nil || holder.FrontendIngress == nil {
		t.Fatal("expected non-nil RequestHolder and FrontendIngress")
	}
	if !holder.FrontendIngress.IsWire() {
		t.Fatal("holder.FrontendIngress must be wire mode")
	}
	if !reflect.DeepEqual(holder.FrontendIngress.Call, lipapi.Call{}) {
		t.Fatalf("wire FrontendIngress Call must be empty, got %+v", holder.FrontendIngress.Call)
	}

	// Verify quantities contain Request=1, MaxOutput=4096, and NO input tokens
	feQuantities := holder.FrontendIngress.Public.Quantities
	reqVal, ok := checkpoint.QuantityComponentValue(feQuantities, metering.ComponentRequest)
	if !ok || reqVal != 1 {
		t.Fatalf("ComponentRequest=%d (ok=%v) want 1", reqVal, ok)
	}
	outVal, ok := checkpoint.QuantityComponentValue(feQuantities, metering.ComponentOutputToken)
	if !ok || outVal != int64(maxOutput) {
		t.Fatalf("ComponentOutputToken=%d (ok=%v) want %d", outVal, ok, maxOutput)
	}
	if _, ok := checkpoint.QuantityComponentValue(feQuantities, metering.ComponentInputToken); ok {
		t.Fatal("wire FE ingress must not contain input tokens before tokenization/counting")
	}

	// Persist FE ingress fact using exported helper
	feFactID, err := ex.PersistFrontendIngressFact(outCtx, holder)
	if err != nil {
		t.Fatalf("PersistFrontendIngressFact failed: %v", err)
	}
	if feFactID != "fe-ingress:"+requestID {
		t.Fatalf("feFactID=%q want %q", feFactID, "fe-ingress:"+requestID)
	}
	// Verify idempotency (no duplicate facts appended on second call)
	feFactID2, err := ex.PersistFrontendIngressFact(outCtx, holder)
	if err != nil || feFactID2 != feFactID {
		t.Fatalf("second PersistFrontendIngressFact failed or diverged: id=%q err=%v", feFactID2, err)
	}
	if len(factRecorder.facts) != 1 {
		t.Fatalf("factRecorder len=%d want 1", len(factRecorder.facts))
	}

	// Enrichment on wire snapshot must be a clean no-op
	if err := ex.enrichFrontendIngressQuantities(outCtx); err != nil {
		t.Fatalf("enrichFrontendIngressQuantities must not error on wire snapshot: %v", err)
	}

	// Step 4: Wire Backend Attempt Checkpoint (10.2 seam)
	attempt1ID := "att-comp-001"
	bLeg1ID := "bleg-comp-001"
	sourceDigest := sha256.Sum256([]byte("wire-payload-bytes"))
	rewriteDigest := checkpoint.ComputeRewriteDigest(128, 10, `"gpt-4o"`)
	attempt1Digest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "gpt-4o")

	beSnap1, err := ex.CaptureWireBackendIngress(outCtx, holder, WireBackendIngressArgs{
		AttemptID:       attempt1ID,
		BLegID:          bLeg1ID,
		BackendID:       "openai_primary",
		Model:           "gpt-4o",
		MaxOutputTokens: &maxOutput,
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attempt1Digest,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("CaptureWireBackendIngress attempt 1 failed: %v", err)
	}
	if !beSnap1.IsWire() {
		t.Fatal("beSnap1 must be wire mode")
	}
	if !reflect.DeepEqual(beSnap1.Call, lipapi.Call{}) {
		t.Fatalf("wire BackendIngress Call must be empty, got %+v", beSnap1.Call)
	}
	if beSnap1.Public.Correlation.RequestID != requestID || beSnap1.Public.Correlation.ALegID != aLeg.ALegID {
		t.Fatalf("beSnap1 correlation mismatch: %+v", beSnap1.Public.Correlation)
	}

	// Persist BE ingress fact using exported helper
	beFact1ID, err := ex.PersistBackendIngressFact(outCtx, holder, attempt1ID)
	if err != nil {
		t.Fatalf("PersistBackendIngressFact attempt 1 failed: %v", err)
	}
	if beFact1ID != "be-ingress:"+attempt1ID {
		t.Fatalf("beFact1ID=%q want %q", beFact1ID, "be-ingress:"+attempt1ID)
	}
	if len(factRecorder.facts) != 2 {
		t.Fatalf("factRecorder len=%d want 2 (FE + BE1)", len(factRecorder.facts))
	}

	// Verify BE enrichment safely ignores wire snapshots
	ex.enrichBackendIngressQuantities(holder, attempt1ID, accountingapp.CountResult{InputTokens: 999})
	ex.enrichBackendIngressQuantitiesWithDecision(holder, attempt1ID, accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 888},
	})
	beSnapCheck := holder.BackendIngressFor(attempt1ID)
	if _, ok := checkpoint.QuantityComponentValue(beSnapCheck.Public.Quantities, metering.ComponentInputToken); ok {
		t.Fatal("enrichBackendIngressQuantities must not inject unmeasured input tokens into wire snapshot")
	}

	// Verify widening check on attempt 1
	evidence1 := checkpoint.WireAttemptEvidence{
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attempt1Digest,
		Model:           "gpt-4o",
		MaxOutputTokens: &maxOutput,
	}
	if err := ex.AssertWireAttemptNotWidened(holder, attempt1ID, evidence1); err != nil {
		t.Fatalf("AssertWireAttemptNotWidened failed: %v", err)
	}

	// Step 5: Failover Second Attempt (10.2 failover correlation)
	attempt2ID := "att-comp-002"
	bLeg2ID := "bleg-comp-002"
	attempt2Digest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "gpt-4o-failover")

	beSnap2, err := ex.CaptureWireBackendIngress(outCtx, holder, WireBackendIngressArgs{
		AttemptID:       attempt2ID,
		BLegID:          bLeg2ID,
		BackendID:       "openai_secondary",
		Model:           "gpt-4o-failover",
		MaxOutputTokens: &maxOutput,
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attempt2Digest,
		Now:             now.Add(200 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("CaptureWireBackendIngress attempt 2 failed: %v", err)
	}
	if beSnap2.Public.Correlation.ALegID != aLeg.ALegID || beSnap2.Public.Correlation.RequestID != requestID {
		t.Fatalf("beSnap2 correlation mismatch: %+v", beSnap2.Public.Correlation)
	}

	beFact2ID, err := ex.PersistBackendIngressFact(outCtx, holder, attempt2ID)
	if err != nil {
		t.Fatalf("PersistBackendIngressFact attempt 2 failed: %v", err)
	}
	if beFact2ID != "be-ingress:"+attempt2ID {
		t.Fatalf("beFact2ID=%q want %q", beFact2ID, "be-ingress:"+attempt2ID)
	}
	if len(factRecorder.facts) != 3 {
		t.Fatalf("factRecorder len=%d want 3 (FE + BE1 + BE2)", len(factRecorder.facts))
	}

	// Step 6: Response Carrier
	carrier := prep.ResponseCarrier(br)
	if carrier.AuthoritativeSessionID != string(br.Record.SessionID) {
		t.Fatalf("carrier AuthoritativeSessionID=%q want %q", carrier.AuthoritativeSessionID, br.Record.SessionID)
	}
	if carrier.ALegID != aLeg.ALegID {
		t.Fatalf("carrier ALegID=%q want %q", carrier.ALegID, aLeg.ALegID)
	}
	if carrier.ResumeToken.Reveal() != string(br.Response.ResumeToken) {
		t.Fatalf("carrier ResumeToken mismatch")
	}

	// Step 7: Zero Call retention check across all snapshots
	for _, snap := range []*checkpoint.Snapshot{holder.FrontendIngress, holder.BackendIngressFor(attempt1ID), holder.BackendIngressFor(attempt2ID)} {
		if snap == nil {
			t.Fatal("unexpected nil snapshot in holder")
		}
		if !snap.IsWire() {
			t.Fatal("all snapshots in holder must be wire mode")
		}
		if !reflect.DeepEqual(snap.Call, lipapi.Call{}) {
			t.Fatalf("snapshot Call must be empty, got %+v", snap.Call)
		}
	}
}

// TestWireComposition_AccountingEnabledWithoutWireCounter_BlocksWireMode proves
// that when accounting preflight or token counting is enabled without an exact wire
// counter, the composition correctly flags static blockers and declines to canonical
// mode, satisfying Requirements 15.5, 15.8, and 21.6.
func TestWireComposition_AccountingEnabledWithoutWireCounter_BlocksWireMode(t *testing.T) {
	t.Parallel()

	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := 0; i < largebody.WireEligibilityPlaneCount; i++ {
		id, ok := largebody.WireEligibilityPlaneID(i)
		if !ok {
			t.Fatalf("missing plane ID for index %d", i)
		}
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}

	testCases := []struct {
		name          string
		ports         largebody.NarrowPortEligibilityInput
		expectBlocker largebody.WirePortBlocker
	}{
		{
			name: "preflight enabled count-only without exact counter blocks",
			ports: largebody.NarrowPortEligibilityInput{
				PreflightEnabled:         true,
				PreflightHasExactCounter: false,
			},
			expectBlocker: largebody.WirePortPreflightCountOnly,
		},
		{
			name: "admin count service occupied blocks",
			ports: largebody.NarrowPortEligibilityInput{
				AdminCountServiceOccupied: true,
			},
			expectBlocker: largebody.WirePortAdminCountService,
		},
		{
			name: "stream usage occupied blocks",
			ports: largebody.NarrowPortEligibilityInput{
				StreamUsageOccupied: true,
			},
			expectBlocker: largebody.WirePortStreamUsage,
		},
		{
			name: "token counting required without exact counter blocks",
			ports: largebody.NarrowPortEligibilityInput{
				TokenCountingRequired:        true,
				TokenCountingHasExactCounter: false,
			},
			expectBlocker: largebody.WirePortTokenCounting,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
				GenerationID:              "gen-accounting-blocked",
				Planes:                    planes,
				Hooks:                     largebody.HookEligibilityInput{},
				Ports:                     tc.ports,
				TwoPhaseExecutorAvailable: true,
			}, 1024)
			if err != nil {
				t.Fatalf("CompileWireEligibilitySummary failed: %v", err)
			}

			if !summary.HasStaticBlocker() {
				t.Fatal("expected summary to have static blocker")
			}
			if summary.PortBlockers()&tc.expectBlocker == 0 {
				t.Fatalf("expected port blocker %#x, got %#x", tc.expectBlocker, summary.PortBlockers())
			}

			disposition, reason := largebody.StaticDisposition(summary, largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   "gen-accounting-blocked",
				ThresholdBytes: 64 * 1024,
				HasKnownLength: true,
				ContentLength:  128 * 1024,
			})
			if disposition != largebody.StaticWireDefinitelyCanonical {
				t.Fatalf("expected DefinitelyCanonical, got %v", disposition)
			}
			if reason != largebody.StaticWireReasonStaticBlocker {
				t.Fatalf("expected ReasonStaticBlocker, got %v", reason)
			}
		})
	}
}

// TestWireComposition_ContextFallbackAndWideningEdges verifies fallback of holder
// from context and strict rejection of unmeasured attempt widening (Req 10, 15.3).
func TestWireComposition_ContextFallbackAndWideningEdges(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715625000, 0).UTC()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS{}}),
	})

	factRecorder := &memoryFactRecorder{}
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.MeteringRecorder = factRecorder
	ex.Now = func() time.Time { return now }

	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("user-edge-001"),
		TenantID:    scope.Known("tenant-edge-001"),
	}
	baseCtx := execview.WithFrontendID(context.Background(), "openai_responses")
	baseCtx = execview.WithPrincipal(baseCtx, execview.PrincipalView{ID: "user-edge-001"})
	baseCtx = scope.WithScope(baseCtx, sc)

	prep, err := ex.PrepareSecureSession(baseCtx, SecureSessionPrepInput{
		TraceID: "trace-edge-001",
		Session: largebody.SessionInput{ClientSessionID: "sess-edge-001", NewSessionRequested: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	br, err := prep.ExecuteBeginTurn(prep.Context())
	if err != nil {
		t.Fatal(err)
	}
	aLeg, _, err := prep.ResolveALeg(prep.Context(), br.Record.ALegID)
	if err != nil {
		t.Fatal(err)
	}

	maxOutput := 1024
	outCtx, holder, err := prep.CaptureFrontendIngressCheckpoint(prep.Context(), "req-edge-001", br, aLeg, &maxOutput)
	if err != nil {
		t.Fatal(err)
	}

	// Passing holder=nil resolves from context in PersistFrontendIngressFact
	factID, err := ex.PersistFrontendIngressFact(outCtx, nil)
	if err != nil {
		t.Fatalf("PersistFrontendIngressFact with context fallback failed: %v", err)
	}
	if factID != "fe-ingress:req-edge-001" {
		t.Fatalf("factID=%q want fe-ingress:req-edge-001", factID)
	}

	// Passing holder=nil resolves from context in CaptureWireBackendIngress
	sourceDigest := sha256.Sum256([]byte("edge-payload"))
	rewriteDigest := checkpoint.ComputeRewriteDigest(100, 5, `"model-a"`)
	attemptDigest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "model-a")

	beSnap, err := ex.CaptureWireBackendIngress(outCtx, nil, WireBackendIngressArgs{
		AttemptID:       "att-edge-001",
		BackendID:       "backend-a",
		Model:           "model-a",
		MaxOutputTokens: &maxOutput,
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attemptDigest,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("CaptureWireBackendIngress with context fallback failed: %v", err)
	}
	if beSnap.Public.Correlation.RequestID != "req-edge-001" {
		t.Fatalf("inherited RequestID=%q want req-edge-001", beSnap.Public.Correlation.RequestID)
	}

	// Passing holder=nil resolves from context in PersistBackendIngressFact
	beFactID, err := ex.PersistBackendIngressFact(outCtx, nil, "att-edge-001")
	if err != nil {
		t.Fatalf("PersistBackendIngressFact with context fallback failed: %v", err)
	}
	if beFactID != "be-ingress:att-edge-001" {
		t.Fatalf("beFactID=%q want be-ingress:att-edge-001", beFactID)
	}

	// Widening check: altered source digest must fail with ErrUnmeasuredWidening
	tamperedSource := sha256.Sum256([]byte("tampered-payload"))
	err = ex.AssertWireAttemptNotWidened(holder, "att-edge-001", checkpoint.WireAttemptEvidence{
		SourceDigest:    tamperedSource,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attemptDigest,
		Model:           "model-a",
		MaxOutputTokens: &maxOutput,
	})
	if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("expected ErrUnmeasuredWidening for tampered digest, got %v", err)
	}
}
