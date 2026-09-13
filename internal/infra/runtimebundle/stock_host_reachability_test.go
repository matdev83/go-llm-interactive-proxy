package runtimebundle_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestStockHost_StockComposedCensus_Accept verifies that a stock-composed census
// (where conversation store, conversation reader, compaction detector, and session recorder
// are present from standard featurehost construction, with clean baseline / no mutating planes)
// is accepted by the authority gate rather than unconditionally declining.
func TestStockHost_StockComposedCensus_Accept(t *testing.T) {
	t.Parallel()

	genID := "gen-stock-test"
	census := largebody.NewStandardDependencyCensus(genID)

	// In stock composition, featurehost always instantiates conversation store and compaction detector.
	// Under PR-2 Item 3, when no mutating planes (local_turn_handlers, compaction_observers,
	// compaction_preservers, terminal_decision_provider, interleaved) are active, these stock
	// ports operate under a safe wire contract (identity no-op / idle request side).
	// We also register security.session_recorder (Item 5).
	census.AddPort("security.session_recorder", true)

	// Simulate stock-composed ports:
	// - ConversationViewReader: present but clean baseline (no local turn handlers)
	// - ConversationViewTagger: present but idle (no local turn handlers)
	// - SteeringWriterFactory: present but idle (no continuation / interleaved)
	// - CompactionDetector: present but request-idle (no compaction observers/preservers)
	census.Ports.BackendsEmpty = false
	census.Ports.ConversationViewReaderOccupied = false // after Item 3 refinement
	census.Ports.ConversationViewTaggerOccupied = false // after Item 3 refinement
	census.Ports.SteeringWriterFactoryOccupied = false  // after Item 3 refinement
	census.Ports.CompactionDetectorOccupied = false     // after Item 3 refinement

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary: %v", err)
	}

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := authGate.Evaluate()
	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("stock-composed census with clean baseline must be accepted, got decision=%v reason=%v", decision, reason)
	}
}

// TestStockHost_BuildHost_WithFastPath_StockComposed_Reachability verifies that
// BuildHost with server.large_payload_fast_path enabled on a clean stock configuration
// compiles an authority gate that evaluates to Accept (wire reachable).
func TestStockHost_BuildHost_WithFastPath_StockComposed_Reachability(t *testing.T) {
	t.Parallel()

	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	spoolDir := t.TempDir()
	// Replace server configuration to enable large_payload_fast_path
	customYAML := strings.Replace(
		string(raw),
		"server:\n  address: \"127.0.0.1:18080\"",
		fmt.Sprintf("server:\n  address: \"127.0.0.1:18080\"\n  large_payload_fast_path:\n    enabled: true\n    threshold_bytes: 4096\n    memory_spool_bytes: 32768\n    max_inflight_spool_bytes: 1048576\n    max_semantic_fact_bytes: 16384\n    spool_dir: %q", spoolDir),
		1,
	)

	// Disable test-only hook noop features so the baseline has no hook blockers
	customYAML = strings.ReplaceAll(customYAML, "id: submit-noop\n      enabled: true", "id: submit-noop\n      enabled: false")
	customYAML = strings.ReplaceAll(customYAML, "id: parts-noop\n      enabled: true", "id: parts-noop\n      enabled: false")
	customYAML = strings.ReplaceAll(customYAML, "id: tool-reactor-noop\n      enabled: true", "id: tool-reactor-noop\n      enabled: false")
	customYAML = strings.ReplaceAll(customYAML, "id: tool-call-repair\n      enabled: true", "id: tool-call-repair\n      enabled: false")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-reachability.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	ex := hostActiveExecutor(t, host)
	pa, ok := ex.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	if !ok || pa == nil {
		t.Fatalf("expected *coreruntime.ProductionLargeBodyAssessor, got %T", ex.LargeBodyAssessor)
	}

	ports := pa.AuthorityGate.Census.Ports
	if !ports.ConversationViewReaderOccupied {
		t.Errorf("expected ConversationViewReaderOccupied to be true when reader is present")
	}
	if !ports.ConversationReaderFreshALegSupported {
		t.Errorf("expected ConversationReaderFreshALegSupported to be true for certified fresh A-leg reader")
	}
	if ports.ConversationViewTaggerOccupied {
		t.Errorf("ConversationViewTaggerOccupied must be false on clean stock host without local_turn_handlers")
	}
	if ports.SteeringWriterFactoryOccupied {
		t.Errorf("SteeringWriterFactoryOccupied must be false on clean stock host without interleaved/continuation")
	}
	if !ports.CompactionDetectorOccupied {
		t.Errorf("expected CompactionDetectorOccupied to be true when compaction detector is present")
	}
	if !ports.CompactionDetectorWireSupported {
		t.Errorf("expected CompactionDetectorWireSupported to be true for wire-capable detector")
	}
	if ports.ExposureAdmissionOccupied {
		t.Errorf("ExposureAdmissionOccupied must be false on stock host")
	}
	if ports.PreflightEnabled {
		t.Errorf("PreflightEnabled must be false on stock host")
	}
	if ports.StreamUsageOccupied {
		t.Errorf("StreamUsageOccupied must be false on stock host")
	}
	if ports.AdminCountServiceOccupied {
		t.Errorf("AdminCountServiceOccupied must be false on stock host")
	}
	if !ports.CapsResolverOccupied {
		t.Errorf("expected CapsResolverOccupied to remain true for stock capabilities map resolver")
	}
	if !ports.CapsResolverWireProofSubsumed {
		t.Errorf("expected CapsResolverWireProofSubsumed to be true when subsumed by BackendWireProofGate")
	}

	if pa.AuthorityGate.Summary.HasStaticBlocker() {
		t.Errorf("expected AuthorityGate.Summary.HasStaticBlocker() to be false on certified baseline, got true")
	}
	if pb := pa.AuthorityGate.Summary.PlaneBlockers(); pb != 0 {
		t.Errorf("expected 0 plane blockers, got %d", pb)
	}
	if hb := pa.AuthorityGate.Summary.HookBlockers(); hb != 0 {
		t.Errorf("expected 0 hook blockers, got %d", hb)
	}
	if portB := pa.AuthorityGate.Summary.PortBlockers(); portB != 0 {
		t.Errorf("expected 0 port blockers, got %d", portB)
	}

	decision, reason := pa.AuthorityGate.Evaluate()
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone {
		t.Fatalf("AuthorityGate.Evaluate() on stock host with subsumed/certified baseline must accept, got decision=%v reason=%v", decision, reason)
	}
}

// TestStockHost_BuildHost_WithFastPath_RealE2EReachability verifies that
// a production BuildHost with server.large_payload_fast_path enabled, certified frontend
// profile (openai_chat_v1), and wire-capable backend routes streaming requests >= threshold
// through OpenWire (OpenWire == 1, canonical Open == 0) for a fresh session, and declines
// to canonical (canonical Open == 1, OpenWire == 0) for a resumed session.
func TestStockHost_BuildHost_WithFastPath_RealE2EReachability(t *testing.T) {
	t.Parallel()

	var (
		upstreamCalls      atomic.Int32
		openWireCalls      atomic.Int32
		openCanonicalCalls atomic.Int32
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-e2e\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"reachability-wire-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-wire-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	padding := strings.Repeat("x", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hello %s"}]}`, padding)

	// Case 1: Fresh session streaming request -> wire accepted, OpenWire == 1, canonical Open == 0
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("fresh wire request failed with HTTP %d: %s", rec.Code, rec.Body.String())
		}
		if got := openWireCalls.Load(); got != 1 {
			t.Fatalf("expected OpenWire == 1, got %d", got)
		}
		if got := openCanonicalCalls.Load(); got != 0 {
			t.Fatalf("expected canonical Open == 0, got %d", got)
		}
	}

	// Case 2: Resumed session request -> pre-commit decline to canonical, OpenWire remains 1, canonical Open increments
	{
		// Seed a real session via a small below-threshold canonical request
		initReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"init"}]}`))
		initReq.Header.Set("Content-Type", "application/json")
		initRec := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(initRec, initReq)
		if initRec.Code != http.StatusOK {
			t.Fatalf("session init failed with HTTP %d: %s", initRec.Code, initRec.Body.String())
		}
		resumeTok := initRec.Header().Get("X-LIP-Resume-Token")
		sessionID := initRec.Header().Get("X-LIP-Session-ID")
		if resumeTok == "" {
			t.Fatalf("expected non-empty X-LIP-Resume-Token from canonical init, got headers: %v", initRec.Header())
		}

		canonBefore := openCanonicalCalls.Load()

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LIP-Resume-Token", resumeTok)
		if sessionID != "" {
			req.Header.Set("X-LIP-Session-ID", sessionID)
		}
		rec := httptest.NewRecorder()

		host.HTTPHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("resumed fallback request failed with HTTP %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "reachability-wire-ok") {
			t.Fatalf("expected streamed response containing reachability-wire-ok, got: %s", rec.Body.String())
		}
		if got := openWireCalls.Load(); got != 1 {
			t.Fatalf("expected OpenWire to remain 1, got %d", got)
		}
		if got := openCanonicalCalls.Load(); got != canonBefore+1 {
			t.Fatalf("expected canonical Open to increment by 1 (got %d, want %d)", got, canonBefore+1)
		}
	}
}

// TestStockHost_Audit_PortsDeclineWhenActive verifies the audited ports
// (ExposureAdmission, Preflight, StreamUsage, AdminCountService, etc.)
// decline wire execution when they are actively occupied/configured.
func TestStockHost_Audit_PortsDeclineWhenActive(t *testing.T) {
	t.Parallel()

	genID := "gen-audit-test"

	cases := []struct {
		name     string
		mutate   func(*largebody.DependencyCensus)
		wantDecl largebody.AssessmentDecision
		wantReas largebody.DeclineReason
	}{
		{
			name: "ExposureAdmissionOccupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.ExposureAdmissionOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "PreflightEnabled without exact counter blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.PreflightEnabled = true
				c.Ports.PreflightHasExactCounter = false
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "StreamUsageOccupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.StreamUsageOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "AdminCountServiceOccupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.AdminCountServiceOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "CompactionObservers plane occupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				for i := range c.Planes {
					if c.Planes[i].ID == "compaction_observers" {
						c.Planes[i].Occupied = true
					}
				}
				c.Ports.CompactionDetectorOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "LocalTurnHandlers plane occupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				for i := range c.Planes {
					if c.Planes[i].ID == "local_turn_handlers" {
						c.Planes[i].Occupied = true
					}
				}
				c.Ports.ConversationViewTaggerOccupied = true
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "Arbitrary ConversationReader (not stock) blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.ConversationViewReaderOccupied = true
				c.Ports.ConversationReaderFreshALegSupported = false
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "Arbitrary CapsResolver (not stock) blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				c.Ports.CapsResolverOccupied = true
				c.Ports.CapsResolverWireProofSubsumed = false
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
		{
			name: "CompactionPreservers plane occupied blocks wire",
			mutate: func(c *largebody.DependencyCensus) {
				idx, ok := largebody.WireEligibilityPlaneIndex("compaction_preservers")
				if ok {
					c.Planes[idx].Occupied = true
				}
			},
			wantDecl: largebody.AssessmentDecisionDecline,
			wantReas: largebody.DeclineReasonAuthorityBlocker,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			census := largebody.NewStandardDependencyCensus(genID)
			tc.mutate(&census)

			summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
				GenerationID:              genID,
				Planes:                    census.Planes,
				Hooks:                     census.Hooks,
				Ports:                     census.Ports,
				TwoPhaseExecutorAvailable: true,
			}, 4096)
			if err != nil {
				t.Fatalf("CompileWireEligibilitySummary: %v", err)
			}

			authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
			decision, reason := authGate.Evaluate()
			if decision != tc.wantDecl {
				t.Fatalf("decision mismatch: got %v want %v", decision, tc.wantDecl)
			}
			if reason != tc.wantReas {
				t.Fatalf("reason mismatch: got %v want %v", reason, tc.wantReas)
			}
		})
	}
}

type stubModelInventoryProvider struct {
	models []modelinventory.Model
}

func (s *stubModelInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	return modelinventory.Snapshot{Models: s.models}, nil
}

func wireSupportStub(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	return largebody.WireRequestSupport{Compatible: true}
}

func TestStockHost_IsBackendCapsSubsumed_AgreementAndDisagreement(t *testing.T) {
	t.Parallel()

	// 1. Empty/nil backends -> false
	if runtimebundle.IsBackendCapsSubsumedForTest(nil) {
		t.Error("expected nil backends to return false")
	}
	if runtimebundle.IsBackendCapsSubsumedForTest(map[string]execbackend.Backend{}) {
		t.Error("expected empty backends to return false")
	}

	// 2. Wire backend with Caps supporting CapabilityStreaming -> true
	backendsAgree := map[string]execbackend.Backend{
		"b1": {
			ResolveWireRequest: wireSupportStub,
			Caps:               lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		},
	}
	if !runtimebundle.IsBackendCapsSubsumedForTest(backendsAgree) {
		t.Error("expected agreeing caps to return true")
	}

	// 3. Wire backend with Caps lacking CapabilityStreaming -> false (disagreement rejection!)
	backendsDisagree := map[string]execbackend.Backend{
		"b1": {
			ResolveWireRequest: wireSupportStub,
			Caps:               lipapi.NewBackendCaps(lipapi.CapabilityTools),
		},
	}
	if runtimebundle.IsBackendCapsSubsumedForTest(backendsDisagree) {
		t.Error("expected disagreeing caps (missing streaming) to return false")
	}

	// 4. Wire backend with ResolveCaps: model inventory with models, one model lacks streaming -> false
	inventory := &stubModelInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "model-streaming"},
		{CanonicalID: "model-batch-only"},
	}}
	backendsModelDepDisagree := map[string]execbackend.Backend{
		"b1": {
			ResolveWireRequest: wireSupportStub,
			ModelInventory:     inventory,
			ResolveCaps: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
				if cand.Primary.Model == "model-streaming" {
					return lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
				}
				return lipapi.NewBackendCaps()
			},
		},
	}
	if runtimebundle.IsBackendCapsSubsumedForTest(backendsModelDepDisagree) {
		t.Error("expected model-dependent disagreement to return false")
	}

	// 5. Wire backend with ResolveCaps: all inventory models support streaming -> true
	backendsModelDepAgree := map[string]execbackend.Backend{
		"b1": {
			ResolveWireRequest: wireSupportStub,
			ModelInventory:     inventory,
			ResolveCaps: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
				return lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
			},
		},
	}
	if !runtimebundle.IsBackendCapsSubsumedForTest(backendsModelDepAgree) {
		t.Error("expected all models supporting streaming to return true")
	}

	// 6. Backend without wire support -> skipped from wire check
	backendsNoWire := map[string]execbackend.Backend{
		"b-legacy": {
			Caps: lipapi.NewBackendCaps(), // no streaming, but also no wire support
		},
		"b-wire": {
			ResolveWireRequest: wireSupportStub,
			Caps:               lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		},
	}
	if !runtimebundle.IsBackendCapsSubsumedForTest(backendsNoWire) {
		t.Error("expected non-wire backend to be skipped and return true")
	}
}

// TestStockHost_HistoricalFallback_PersistedNeverBackendAndSteering proves that on a
// resumed session with an actual persisted NeverBackend tag and steering overlay:
// 1. ProductionLargeBodyAssessor precommit evaluation declines wire fast path (!ProvesFreshALeg()).
// 2. OpenWire calls == 0 (wire is never invoked).
// 3. Request proceeds in canonical execution under held decode permit.
// 4. Canonical snapshotAndProject reads fresh snapshot from conversation store once.
// 5. Tagged message is removed from the projected call forwarded to backend Open.
// 6. Steering overlay is applied to the projected call forwarded to backend Open.
// 7. Canonical Open is called exactly once with the modified call.
// 8. Client receives 200 OK streamed response.
func TestStockHost_HistoricalFallback_PersistedNeverBackendAndSteering(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-hist\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hist-fallback-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-hist-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var openWireCalls atomic.Int32
	var openCanonicalCalls atomic.Int32
	var capturedCall atomic.Pointer[lipapi.Call]

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							clone := call
							capturedCall.Store(&clone)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)

	// Obtain authoritative conversation store via active executor tagger port
	ex := hostActiveExecutor(t, host)
	convStore, ok := conversationview.AsStore(ex.ConversationViewTagger)
	require.True(t, ok, "expected ConversationViewTagger to provide conversationview.Store")
	require.NotNil(t, convStore)

	// Step 1: Initialize session via fresh large payload turn (> 1024 bytes threshold)
	// Because this is a fresh turn with clean baseline and certified stock origin, wire reachability succeeds.
	initPadding := strings.Repeat("x", 1500)
	initBody := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"init %s"}]}`, initPadding)
	initReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(initBody))
	initReq.Header.Set("Content-Type", "application/json")
	initRec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(initRec, initReq)
	require.Equal(t, http.StatusOK, initRec.Code)
	require.Equal(t, int32(1), openWireCalls.Load(), "fresh turn must reach backend via wire streaming")
	require.Equal(t, int32(0), openCanonicalCalls.Load(), "canonical open must not be called for fresh wire stream")

	resumeTok := initRec.Header().Get("X-LIP-Resume-Token")
	sessionID := initRec.Header().Get("X-LIP-Session-ID")
	aLegID := initRec.Header().Get("X-LIP-A-Leg-ID")
	require.NotEmpty(t, resumeTok, "expected non-empty resume token")
	require.NotEmpty(t, aLegID, "expected non-empty a-leg id")

	// Step 2: Persist NeverBackend tag for a specific user message using authoritative aLegID
	msgToDrop := lipapi.Message{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart("never-backend-tagged-part")},
	}
	tagID, err := conversationview.MessageIdentityOf(msgToDrop)
	require.NoError(t, err)

	_, err = convStore.TagNeverBackend(context.Background(), aLegID, []conversationview.TagRequest{
		{Identity: tagID, Reason: "test-historical-tag"},
	})
	require.NoError(t, err)

	// Step 3: Persist Steering overlay
	steeringText := "historical-steering-overlay-content"
	_, err = convStore.PutSteering(context.Background(), aLegID, conversationview.PutSteeringRequest{
		OverlayID: "hist-steering-1",
		Message: conversationview.StoredMessageV1{
			Role: lipapi.RoleSystem,
			Text: steeringText,
		},
		Placement: conversationview.StoredPlacement{
			Kind: conversationview.PlacementStablePrefix,
		},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test-historical-steering",
	})
	require.NoError(t, err)

	// Step 4: Send large payload request with resume token and session ID
	// Request contains both the tagged message to be dropped and a large user message (> 1024 bytes threshold)
	padding := strings.Repeat("z", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"never-backend-tagged-part"},{"role":"user","content":"main-content %s"}]}`, padding)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LIP-Resume-Token", resumeTok)
	req.Header.Set("X-LIP-Session-ID", sessionID)
	rec := httptest.NewRecorder()

	wireBefore := openWireCalls.Load()
	canonBefore := openCanonicalCalls.Load()

	host.HTTPHandler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, aLegID, rec.Header().Get("X-Lip-A-Leg-ID"), "resumed turn must retain the same A-leg identity")
	// Wire calls must NOT increment (openWireCalls remains 1)
	require.Equal(t, wireBefore, openWireCalls.Load(), "openWireCalls must not increment for resumed historical request")
	// Canonical Open must increment by exactly 1 (0 -> 1)
	require.Equal(t, canonBefore+1, openCanonicalCalls.Load(), "canonical open must increment by 1")

	// Step 5: Verify captured canonical call to backend:
	// - Tagged message MUST be removed by canonical projection
	// - Steering overlay MUST be applied as leading system message
	captured := capturedCall.Load()
	require.NotNil(t, captured)

	for _, msg := range captured.Messages {
		for _, part := range msg.Parts {
			if strings.Contains(part.Text, "never-backend-tagged-part") {
				t.Fatalf("projected backend call still contains never-backend tagged turn: %q", part.Text)
			}
		}
	}

	hasSteering := false
	for _, msgList := range [][]lipapi.Message{captured.Instructions, captured.Messages} {
		for _, msg := range msgList {
			for _, part := range msg.Parts {
				if strings.Contains(part.Text, steeringText) {
					hasSteering = true
					break
				}
			}
		}
	}
	require.True(t, hasSteering, "projected backend call must contain applied steering overlay in instructions or messages")
}

// TestStockHost_CustomOverriddenConversationReader_Decline verifies that if a non-stock
// ConversationReader is configured without stock origin certification, the authority gate
// evaluates ConversationReaderFreshALegSupported as false, triggering a static blocker
// that declines wire execution.
func TestStockHost_CustomOverriddenConversationReader_Decline(t *testing.T) {
	t.Parallel()

	genID := "gen-custom-reader-test"
	census := largebody.NewStandardDependencyCensus(genID)
	census.AddPort("security.session_recorder", true)
	census.Ports.BackendsEmpty = false
	census.Ports.ConversationViewReaderOccupied = true
	// Custom overridden reader does NOT certify stock origin
	census.Ports.ConversationReaderFreshALegSupported = false

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID:              genID,
		Planes:                    census.Planes,
		Hooks:                     census.Hooks,
		Ports:                     census.Ports,
		TwoPhaseExecutorAvailable: true,
	}, 4096)
	require.NoError(t, err)

	authGate := largebody.NewAuthorityAssessmentGate(summary, census, genID)
	decision, reason := authGate.Evaluate()
	require.Equal(t, largebody.AssessmentDecisionDecline, decision)
	require.Equal(t, largebody.DeclineReasonAuthorityBlocker, reason)
}

// TestStockHost_FastPath_ReloadContinuity verifies that when a stock host with fast-path enabled
// undergoes a configuration reload, stock origin certification is preserved across generations,
// authority gate continues to evaluate Accept on the new generation, and fresh requests reach wire streaming.
func TestStockHost_FastPath_ReloadContinuity(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-reload\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"reload-ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	customYAML := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "stock-reload-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(customYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var openWireCalls atomic.Int32
	var openCanonicalCalls atomic.Int32

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
							openWireCalls.Add(1)
							return origOpenWire(ctx, req)
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							openCanonicalCalls.Add(1)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)

	// Pre-reload verification: executor has ConversationReaderFreshALegSupported == true
	ex1 := hostActiveExecutor(t, host)
	pa1, ok := ex1.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.True(t, pa1.AuthorityGate.Census.Ports.ConversationReaderFreshALegSupported)

	// Send fresh large payload before reload
	padding := strings.Repeat("x", 1500)
	bodyJSON := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"turn-1 %s"}]}`, padding)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code)
	require.Equal(t, int32(1), openWireCalls.Load(), "pre-reload request must reach wire streaming")
	require.Equal(t, int32(0), openCanonicalCalls.Load())

	// Mutate config to trigger a genuine published reload via atomic replacement
	tmpPath := filepath.Join(t.TempDir(), "reloaded.yaml")
	reloadedYAML := strings.Replace(customYAML, "max_attempts: 3", "max_attempts: 4", 1)
	require.NoError(t, os.WriteFile(tmpPath, []byte(reloadedYAML), 0o600))
	require.NoError(t, replaceTestFile(tmpPath, cfgPath))

	// Trigger Host.Reload
	res := host.Reload(t.Context(), sdkreload.Trigger{
		Kind:       sdkreload.TriggerAPI,
		AcceptedAt: time.Now().UTC(),
		SafeActor:  "test-reload-continuity",
	})
	require.Equal(t, sdkreload.ResultPublished, res.Category, "reload must publish new generation: %s", res.ReasonCategory)

	// Post-reload verification: new generation executor still retains ConversationReaderFreshALegSupported
	ex2 := hostActiveExecutor(t, host)
	pa2, ok := ex2.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.True(t, pa2.AuthorityGate.Census.Ports.ConversationReaderFreshALegSupported)

	// Send fresh large payload after reload on new active generation
	bodyJSON2 := fmt.Sprintf(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"turn-2 %s"}]}`, padding)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(bodyJSON2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Equal(t, int32(2), openWireCalls.Load(), "post-reload request must also reach wire streaming")
	require.Equal(t, int32(0), openCanonicalCalls.Load())
}
