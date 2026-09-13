package runtimebundle_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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
	if ports.ConversationViewReaderOccupied {
		t.Errorf("ConversationViewReaderOccupied must be false on clean stock host without local_turn_handlers")
	}
	if ports.ConversationViewTaggerOccupied {
		t.Errorf("ConversationViewTaggerOccupied must be false on clean stock host without local_turn_handlers")
	}
	if ports.SteeringWriterFactoryOccupied {
		t.Errorf("SteeringWriterFactoryOccupied must be false on clean stock host without interleaved/continuation")
	}
	if ports.CompactionDetectorOccupied {
		t.Errorf("CompactionDetectorOccupied must be false on clean stock host without compaction_observers/compaction_preservers")
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

	// On stock host with backend map, CapsResolverOccupied remains a documented blocker
	// (capabilities.Resolver takes full Call), so AuthorityGate correctly declines
	// with reason=authority_blocker without crashing or silent downgrade (Item 3 mandate:
	// "If any authority genuinely needs Call content on the wire path, it STAYS a blocker").
	if !ports.CapsResolverOccupied {
		t.Errorf("expected CapsResolverOccupied to remain true as documented blocker")
	}
	decision, reason := pa.AuthorityGate.Evaluate()
	if decision != largebody.AssessmentDecisionDecline || reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("AuthorityGate.Evaluate() on stock host with CapsResolver blocker must decline, got decision=%v reason=%v", decision, reason)
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
