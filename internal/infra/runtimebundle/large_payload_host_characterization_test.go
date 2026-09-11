package runtimebundle_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

type staticDispositionCheck interface {
	LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason)
}

func TestLargePayloadHost_StockHost_ExecutorRoutesCanonical(t *testing.T) {
	t.Parallel()

	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      basePath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	ex := hostActiveExecutor(t, host)
	if ex == nil {
		t.Fatal("expected non-nil executor from stock host")
	}

	// Characterization: The stock executor must satisfy LargeBodyExecutor (Phase 4 requirement).
	lbe, ok := largebody.AsLargeBodyExecutor(ex)
	if !ok || lbe == nil {
		t.Fatalf("stock executor must satisfy largebody.LargeBodyExecutor, got ok=%v", ok)
	}

	// Stock host has no wire backends / authority blockers -> static disposition is definitely canonical.
	if sdp, ok := any(ex).(staticDispositionCheck); ok {
		disp, reason := sdp.LargeBodyStaticDisposition("openai_chat_v1")
		if !disp.IsDefinitelyCanonical() {
			t.Fatalf("stock host without fast-path config must have definitely canonical disposition, got disp=%v reason=%v", disp, reason)
		}
	}

	// Proof evaluation on stock host must decline (zero spooling/allocation, canonical route).
	ctx := context.Background()
	proof := largebody.Proof{
		ProfileID: "openai_chat_v1",
		Operation: lipapi.OperationOpenAIChatCompletions,
		Delivery:  lipapi.DeliveryModeStreaming,
	}
	assessment, err := lbe.AssessLargeBody(ctx, proof)
	if err != nil {
		t.Fatalf("AssessLargeBody must return nil error (same-permit decline discipline), got: %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("stock host without eligible backend must decline assessment, got decision=%v", assessment.Decision)
	}

	// SpoolLedger and Diagnostics wiring verification (Task 19.1 unwired carry).
	if host.SpoolLedger() == nil {
		t.Fatal("expected non-nil SpoolLedger on host")
	}
	diag := host.LargePayloadDiagnostics()
	if diag == nil {
		t.Fatal("expected non-nil LargePayloadDiagnostics on host")
	}
}

// TestLargePayloadHost_StockHost_CandidatePrerequisites_TypeAssertionOnly characterizes that
// BuildHost's runtime.Executor satisfies largebody.LargeBodyExecutor for frontendpipe.CandidatePrerequisites.
//
// Deferral note (Phase 5): Full BuildHost-level wire eligibility and candidate-path execution
// cannot be verified at the host level in Phase 4 because runtimebundle's production composition
// occupies blocker-class full-Call ports (such as RoutingRT.CapsResolver in build_executor.go).
// As a result, the production census correctly reports static blockers, keeping stock hosts 100%
// canonical (zero spooling). Genuine enabled-config/eligible-generation candidate-path execution
// is characterized in TestLargePayload_EnabledConfig_PreCaptureGatesAndExecution.
// Wire eligibility enablement at the BuildHost level is deferred to Phase 5.
func TestLargePayloadHost_StockHost_CandidatePrerequisites_TypeAssertionOnly(t *testing.T) {
	t.Parallel()

	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      basePath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	ex := hostActiveExecutor(t, host)

	// In Phase 4, CandidatePrerequisites evaluates AsLargeBodyExecutor.
	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: ex,
		},
		Profile: stubFrontendProfile{id: "openai_chat_v1"},
	}
	prereqLbe, ok := frontendpipe.CandidatePrerequisites(spec)
	if !ok || prereqLbe == nil {
		t.Fatalf("CandidatePrerequisites must succeed with real runtime.Executor implementing LargeBodyExecutor, got ok=%v", ok)
	}
}

type stubFrontendProfile struct {
	id string
}

func (s stubFrontendProfile) ProfileID() string { return s.id }
func (s stubFrontendProfile) CompileProof(context.Context, frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	return frontendpipe.ProofOutput{}, nil
}

type charTestWireBackend struct {
	reqSupport    largebody.WireRequestSupport
	domainSupport largebody.WireDomainSupport
}

func (b charTestWireBackend) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	return b.reqSupport
}

func (b charTestWireBackend) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	return b.domainSupport
}

func newCharTestEligibleAssessor(t *testing.T, genID string) *coreruntime.ProductionLargeBodyAssessor {
	t.Helper()

	census := largebody.NewStandardDependencyCensus(genID)
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

	wb := charTestWireBackend{
		reqSupport: largebody.WireRequestSupport{
			Compatible: true,
			Reason:     largebody.WireSupportReasonNone,
		},
		domainSupport: largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: true,
			Reason:           largebody.WireSupportReasonNone,
		},
	}
	resolver := largebody.WireBackendMap{"test-backend": wb}

	execResolver := routing.BackendExecutionResolverFunc(func(string) (lipsdk.BackendExecutionClass, bool) {
		return lipsdk.BackendExecutionInference, true
	})
	validator := routing.NewGenerationSelectorValidator(
		nil,
		"test-backend",
		map[string]struct{}{"test-backend": {}},
		execResolver,
		config.ExecutionCompositionSafe,
	)

	initialGate := largebody.NewInitialRouteAssessmentGate(
		nil,
		"test-backend",
		execResolver,
		config.ExecutionCompositionSafe,
		nil,
		resolver,
	)
	overrideGate := largebody.NewRouteOverrideAssessmentGate(nil, validator, resolver)
	wireGate := largebody.NewBackendWireProofGate(initialGate, overrideGate, nil, resolver)

	return coreruntime.NewProductionLargeBodyAssessor(
		genID,
		genID,
		authGate,
		wireGate,
		coreruntime.StandardLaneDomainPolicies(),
	)
}

func validCharTestProof(profileID string, op lipapi.Operation, delivery lipapi.DeliveryMode) largebody.Proof {
	return largebody.Proof{
		ProfileID:       profileID,
		Operation:       op,
		Delivery:        delivery,
		RouteSelector:   "gpt-4o",
		ClientModel:     "gpt-4o",
		MaxOutputTokens: 4096,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "std-req-v1",
			ControlCount:   1,
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
		Identity:  largebody.NewIdentityDigest([32]byte{1, 2, 3}),
		Source:    largebody.NewSourceDigest([32]byte{4, 5, 6}),
		BodyBytes: 1024,
	}
}

// TestLargePayload_EnabledConfig_PreCaptureGatesAndExecution provides genuine
// enabled-config and eligible-generation candidate-path characterization.
//
// Deferral note (Phase 5): Full BuildHost-level wire eligibility is blocked in Phase 4 because
// runtimebundle unconditionally constructs and assigns blocker-class full-Call ports (such as
// RoutingRT.CapsResolver in build_executor.go). Enabling BuildHost-level fast-path config and
// wire eligibility is deferred to Phase 5.
func TestLargePayload_EnabledConfig_PreCaptureGatesAndExecution(t *testing.T) {
	t.Parallel()

	genID := "gen-eligible-cand"
	assessor := newCharTestEligibleAssessor(t, genID)

	ex := coreruntime.NewExecutor(coreruntime.ExecutorConfig{
		Core: coreruntime.CoreRuntime{
			LargeBodyAssessor:                  assessor,
			LargeBodyGenerationID:              genID,
			LargeBodyCandidateDomainGeneration: genID,
		},
	})

	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: ex,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1024,
			},
		},
		Profile: stubFrontendProfile{id: "openai_chat_v1"},
	}

	// 1. CandidatePrerequisites succeeds on real executor with LargeBodyExecutor capability.
	prereqLbe, ok := frontendpipe.CandidatePrerequisites(spec)
	if !ok || prereqLbe == nil {
		t.Fatalf("CandidatePrerequisites must succeed, got ok=%v lbe=%v", ok, prereqLbe)
	}

	// 2. EvaluatePreCaptureGates with uncompressed request >= threshold passes all gates to Eligible.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bytes.Repeat([]byte("a"), 2048)))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 2048

	gateRes := frontendpipe.EvaluatePreCaptureGates(spec, req)
	if !gateRes.Candidate {
		t.Fatalf("expected eligible candidate, got gate=%v reason=%v", gateRes.Gate, gateRes.Reason)
	}
	if gateRes.Gate != frontendpipe.PreCaptureGateNone {
		t.Fatalf("expected PreCaptureGateNone, got gate=%v", gateRes.Gate)
	}
	if gateRes.Reason != largebody.StaticWireReasonNone {
		t.Fatalf("expected StaticWireReasonNone, got reason=%v", gateRes.Reason)
	}

	// 3. Contrast: disabled config declines at Gate 1 (FeatureProfileExecutor).
	specDisabled := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: ex,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled: false,
			},
		},
		Profile: stubFrontendProfile{id: "openai_chat_v1"},
	}
	gateResDisabled := frontendpipe.EvaluatePreCaptureGates(specDisabled, req)
	if gateResDisabled.Candidate {
		t.Fatal("disabled config must not be eligible")
	}
	if gateResDisabled.Reason != largebody.StaticWireReasonFeatureDisabled {
		t.Fatalf("expected StaticWireReasonFeatureDisabled, got %v", gateResDisabled.Reason)
	}

	// 4. Contrast: below-threshold request declines at Gate 2 (KnownLengthThreshold).
	reqSmall := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte("small")))
	reqSmall.Header.Set("Content-Type", "application/json")
	reqSmall.ContentLength = 500
	gateResSmall := frontendpipe.EvaluatePreCaptureGates(spec, reqSmall)
	if gateResSmall.Candidate {
		t.Fatal("below-threshold request must not be eligible")
	}
	if gateResSmall.Reason != largebody.StaticWireReasonBelowThreshold {
		t.Fatalf("expected StaticWireReasonBelowThreshold, got %v", gateResSmall.Reason)
	}

	// 5. Contrast: gzip request declines at Gate 3 (GzipWave1).
	reqGzip := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte("gzip")))
	reqGzip.Header.Set("Content-Type", "application/json")
	reqGzip.Header.Set("Content-Encoding", "gzip")
	reqGzip.ContentLength = 2048
	gateResGzip := frontendpipe.EvaluatePreCaptureGates(spec, reqGzip)
	if gateResGzip.Candidate {
		t.Fatal("gzip request must not be eligible")
	}
	if gateResGzip.Reason != largebody.StaticWireReasonGzipCompressed {
		t.Fatalf("expected StaticWireReasonGzipCompressed, got %v", gateResGzip.Reason)
	}

	// 6. AssessLargeBody through real executor accepts valid streaming proof.
	proof := validCharTestProof("openai_chat_v1", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeStreaming)
	assessment, err := ex.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("AssessLargeBody: %v", err)
	}
	if assessment.Decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected Accept, got %v (reason: %v)", assessment.Decision, assessment.Reason)
	}
	if assessment.Stamp.IsZero() {
		t.Fatal("expected non-zero stamp on accepted assessment")
	}
	if assessment.Stamp.GenerationID() != genID {
		t.Fatalf("stamp generation ID mismatch: got %q want %q", assessment.Stamp.GenerationID(), genID)
	}

	// 7. Wire execution enforcement: declined assessment is rejected at the commit barrier.
	declined, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonRouteIncompatible)
	_, err = ex.ExecuteLargeBody(context.Background(), declined, nil)
	if err == nil {
		t.Fatal("ExecuteLargeBody must fail closed for non-accepted assessment")
	}
}
