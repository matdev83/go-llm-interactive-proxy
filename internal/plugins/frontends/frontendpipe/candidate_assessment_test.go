package frontendpipe_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// Task 11.9: Call assessment while SAME decode permit remains held:
// - Assessment decline owns same-permit fallback: canonical Spec.Decode from replay under the
//   original permit still held, with no release/reacquire and no second TryAdmit/429/503 decision.
// - Accept => release once then commit.
// - Saturation/concurrency tests prove no fallback-induced second admission decision.
// - Requirements: 1, 6.

type testAssessorExecutor struct {
	candidateGatesExec
	mu                sync.Mutex
	assessFunc        func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error)
	executeLargeFunc  func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error)
	assessCalls       int64
	executeLargeCalls int64
}

func (e *testAssessorExecutor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	atomic.AddInt64(&e.assessCalls, 1)
	e.mu.Lock()
	fn := e.assessFunc
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, proof)
	}
	return e.candidateGatesExec.AssessLargeBody(ctx, proof)
}

func (e *testAssessorExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	atomic.AddInt64(&e.executeLargeCalls, 1)
	e.mu.Lock()
	fn := e.executeLargeFunc
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, accepted, src)
	}
	return e.candidateGatesExec.ExecuteLargeBody(ctx, accepted, src)
}

func (e *testAssessorExecutor) AssessCallCount() int64 {
	return atomic.LoadInt64(&e.assessCalls)
}

func (e *testAssessorExecutor) ExecuteLargeCallCount() int64 {
	return atomic.LoadInt64(&e.executeLargeCalls)
}

var _ lipsdk.ExecutorView = (*testAssessorExecutor)(nil)
var _ largebody.LargeBodyExecutor = (*testAssessorExecutor)(nil)

// minimalValidProofProfile produces a valid Proof and Seeds for assessment testing.
func minimalValidProofProfile() *certifiedTestProfile {
	return &certifiedTestProfile{
		profileID: "test_certified_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			modelSpan := largebody.Span{Offset: 10, Length: 8}
			rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
			if err != nil {
				return frontendpipe.ProofOutput{}, err
			}
			sess := largebody.SessionInput{
				AuthoritativeSessionID: "sess_test_1",
				ALegID:                 "aleg_test_1",
			}
			idWriter, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
				SessionInput:  sess,
				RouteSelector: "gpt-4o",
				ClientModel:   "gpt-4o",
			})
			if err != nil {
				return frontendpipe.ProofOutput{}, err
			}
			_ = idWriter.StartMessages()
			_ = idWriter.AddMessage(lipapi.Message{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hello")},
			})
			digest, err := idWriter.Digest()
			if err != nil {
				return frontendpipe.ProofOutput{}, err
			}
			sourceDigest := largebody.NewSourceDigest([32]byte{1, 2, 3})
			proof := largebody.Proof{
				ProfileID:       "test_certified_profile_v1",
				Operation:       lipapi.OperationOpenAIChatCompletions,
				Delivery:        lipapi.DeliveryModeNonStreaming,
				RouteSelector:   "gpt-4o",
				ClientModel:     "gpt-4o",
				MaxOutputTokens: 0,
				Facts: largebody.ProtocolFacts{
					RequirementsID: "openai_chat_v1",
				},
				Mode:      largebody.BodyModeIdentityJSON,
				Rewrite:   rewrite,
				ModelSpan: modelSpan,
				Identity:  digest,
				Turn: largebody.ClientTurnShape{
					Items: []largebody.ClientTurnItemShape{{
						Kind:    lipapi.ItemKindMessage,
						Role:    lipapi.RoleUser,
						Ordinal: 0,
						Parts: []largebody.ClientTurnPartShape{{
							Kind:         lipapi.ContentPartText,
							ContentBytes: 5,
						}},
					}},
					TotalContentBytes: 5,
				},
				Session:   sess,
				Source:    sourceDigest,
				BodyBytes: in.BodyBytes,
			}
			seeds := frontendpipe.NewResponseStateSeeds(
				digest,
				"",
				"gpt-4o",
				"gpt-4o",
				false,
				sess,
				"",
			)
			return frontendpipe.ProofOutput{
				State: frontendpipe.FrontendWireState{
					ProfileID: "test_certified_profile_v1",
					Proof:     proof,
					Seeds:     seeds,
				},
			}, nil
		},
	}
}

func makeAcceptedAssessment(proof largebody.Proof) (largebody.Assessment, error) {
	stamp, err := largebody.NewAssessmentStamp(
		"gen_test_1",
		proof.ProfileID,
		proof.Source,
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		proof.Identity,
	)
	if err != nil {
		return largebody.Assessment{}, err
	}
	wireReq := largebody.WireRequestFacts{
		ProfileID:       proof.ProfileID,
		Operation:       proof.Operation,
		Delivery:        proof.Delivery,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		ClientModel:     proof.ClientModel,
		CandidateModel:  proof.ClientModel,
		MaxOutputTokens: proof.MaxOutputTokens,
	}
	wireDomain := largebody.WireDomainFacts{
		ProfileID: proof.ProfileID,
		Operation: proof.Operation,
		Delivery:  proof.Delivery,
	}
	return largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
}

func newAssessmentTestSpec(
	exec lipsdk.ExecutorView,
	prof frontendpipe.FrontendProfile,
	cfg frontendpipe.LargePayloadConfig,
	admission decodeqos.TryAcquirer,
	assessmentRecord *[]frontendpipe.CandidateAssessmentResult,
	wireCommitRecord *[]frontendpipe.WireCommitResult,
	decodeObserver func(dctx frontendpipe.DecodeContext),
) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:            exec,
			FrontendID:      "candidate_assessment_test",
			LargePayload:    cfg,
			DecodeAdmission: admission,
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if path == "/v1/create" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			if decodeObserver != nil {
				decodeObserver(dctx)
			}
			return &frontendpipe.Decoded{
				RouteSelector: dctx.RouteSelector,
				Call: &lipapi.Call{
					ID: "call_assessment_test",
					Route: lipapi.RouteIntent{
						Selector: dctx.RouteSelector,
					},
					Messages: []lipapi.Message{{
						Role:  lipapi.RoleUser,
						Parts: []lipapi.Part{lipapi.TextPart(string(dctx.Body))},
					}},
				},
			}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			return struct{}{}
		},
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, es lipapi.EventStream, opts struct{}) error {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return nil
		},
		OnCandidateAssessment: func(r *http.Request, res frontendpipe.CandidateAssessmentResult) {
			if assessmentRecord != nil {
				*assessmentRecord = append(*assessmentRecord, res)
			}
		},
		OnWireCommit: func(r *http.Request, res frontendpipe.WireCommitResult) {
			if wireCommitRecord != nil {
				*wireCommitRecord = append(*wireCommitRecord, res)
			}
		},
	}
}

// TestCandidateAssessment_AssessCalledUnderPermit proves Requirement 6.1, 6.2:
// When proof succeeds, AssessLargeBody is invoked while the SAME decode permit is STILL HELD.
func TestCandidateAssessment_AssessCalledUnderPermit(t *testing.T) {
	t.Parallel()

	limiter := &trackingAdmissionLimiter{}
	exec := &testAssessorExecutor{}
	payload := buildJSONPayload(1200 * 1024)

	var permitHeldDuringAssess bool
	var inflightDuringAssess int64
	var releaseCallsDuringAssess int

	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		permitHeldDuringAssess = limiter.InflightBytes() > 0
		inflightDuringAssess = limiter.InflightBytes()
		releaseCallsDuringAssess = limiter.ReleaseCount()
		return largebody.NewDeclinedAssessment(largebody.DeclineReasonRouteIncompatible)
	}

	var assessmentRecords []frontendpipe.CandidateAssessmentResult
	spec := newAssessmentTestSpec(
		exec,
		minimalValidProofProfile(),
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		&assessmentRecords,
		nil,
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on fallback, got %d", rec.Code)
	}
	if exec.AssessCallCount() != 1 {
		t.Fatalf("expected AssessLargeBody called once, got %d", exec.AssessCallCount())
	}
	if !permitHeldDuringAssess {
		t.Fatal("expected decode permit to be held during AssessLargeBody, but was not")
	}
	if inflightDuringAssess != int64(len(payload)) {
		t.Fatalf("inflight during assess: got %d, want %d", inflightDuringAssess, len(payload))
	}
	if releaseCallsDuringAssess != 0 {
		t.Fatalf("release calls during assess: got %d, want 0", releaseCallsDuringAssess)
	}
	if len(assessmentRecords) != 1 {
		t.Fatalf("expected 1 assessment record, got %d", len(assessmentRecords))
	}
	if !assessmentRecords[0].PermitHeld {
		t.Fatal("expected PermitHeld=true in CandidateAssessmentResult")
	}
}

// TestCandidateAssessment_Decline_SamePermitFallbackDecode proves Requirement 6.3, 6.4:
// Assessment decline owns same-permit fallback: canonical Spec.Decode from replay under the
// original permit still held, with no release/reacquire and no second TryAdmit decision.
// Release occurs only at post-decode boundary.
func TestCandidateAssessment_Decline_SamePermitFallbackDecode(t *testing.T) {
	t.Parallel()

	limiter := &trackingAdmissionLimiter{}
	exec := &testAssessorExecutor{}
	payload := buildJSONPayload(1200 * 1024)

	var permitHeldDuringDecode bool
	var inflightDuringDecode int64
	var releaseCallsDuringDecode int
	var decodeCalls int

	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return largebody.NewDeclinedAssessment(largebody.DeclineReasonBackendIncompatible)
	}

	spec := newAssessmentTestSpec(
		exec,
		minimalValidProofProfile(),
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		nil,
		func(dctx frontendpipe.DecodeContext) {
			decodeCalls++
			permitHeldDuringDecode = limiter.InflightBytes() > 0
			inflightDuringDecode = limiter.InflightBytes()
			releaseCallsDuringDecode = limiter.ReleaseCount()
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}
	if decodeCalls != 1 {
		t.Fatalf("expected Spec.Decode called once, got %d", decodeCalls)
	}
	if !permitHeldDuringDecode {
		t.Fatal("expected permit to remain held during fallback Spec.Decode")
	}
	if inflightDuringDecode != int64(len(payload)) {
		t.Fatalf("inflight during decode: got %d, want %d", inflightDuringDecode, len(payload))
	}
	if releaseCallsDuringDecode != 0 {
		t.Fatalf("expected 0 releases before Spec.Decode completes, got %d", releaseCallsDuringDecode)
	}
	// After ServeHTTP completes, permit was released at post-decode boundary
	if limiter.ReleaseCount() != 1 {
		t.Fatalf("expected exactly 1 release after decode, got %d", limiter.ReleaseCount())
	}
	if limiter.InflightBytes() != 0 {
		t.Fatalf("expected 0 inflight bytes after release, got %d", limiter.InflightBytes())
	}
	if limiter.CallCount() != 1 {
		t.Fatalf("expected strictly 1 TryAcquire call (no second admission), got %d", limiter.CallCount())
	}
	if exec.ExecuteLargeCallCount() != 0 {
		t.Fatalf("expected ExecuteLargeBody NOT called on decline, got %d", exec.ExecuteLargeCallCount())
	}
}

// TestCandidateAssessment_Accept_ReleaseOnceThenCommit proves Requirement 6.5, 6.6:
// When assessment accepts:
// - The permit is released ONCE before wire commit.
// - ExecuteLargeBody runs with PermitHeld == false.
// - Spec.Decode is NEVER invoked (no canonical fallback after commit).
func TestCandidateAssessment_Accept_ReleaseOnceThenCommit(t *testing.T) {
	t.Parallel()

	limiter := &trackingAdmissionLimiter{}
	exec := &testAssessorExecutor{}
	payload := buildJSONPayload(1200 * 1024)

	var permitHeldDuringExecuteLarge bool
	var releaseCallsDuringExecuteLarge int
	var decodeCalls int

	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return makeAcceptedAssessment(proof)
	}

	exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		permitHeldDuringExecuteLarge = limiter.InflightBytes() > 0
		releaseCallsDuringExecuteLarge = limiter.ReleaseCount()
		return largebody.ExecutionResult{}, nil
	}

	var wireCommitRecords []frontendpipe.WireCommitResult
	spec := newAssessmentTestSpec(
		exec,
		minimalValidProofProfile(),
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		&wireCommitRecords,
		func(dctx frontendpipe.DecodeContext) {
			decodeCalls++
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}
	if exec.AssessCallCount() != 1 {
		t.Fatalf("expected AssessLargeBody called once, got %d", exec.AssessCallCount())
	}
	if exec.ExecuteLargeCallCount() != 1 {
		t.Fatalf("expected ExecuteLargeBody called once on accept, got %d", exec.ExecuteLargeCallCount())
	}
	if decodeCalls != 0 {
		t.Fatalf("expected Spec.Decode NEVER called on accept, got %d", decodeCalls)
	}
	if permitHeldDuringExecuteLarge {
		t.Fatal("expected permit to be RELEASED before ExecuteLargeBody runs")
	}
	if releaseCallsDuringExecuteLarge != 1 {
		t.Fatalf("expected permit released exactly once before ExecuteLargeBody, got %d", releaseCallsDuringExecuteLarge)
	}
	if limiter.ReleaseCount() != 1 {
		t.Fatalf("expected total release count 1, got %d", limiter.ReleaseCount())
	}
	if limiter.CallCount() != 1 {
		t.Fatalf("expected exactly 1 TryAcquire call, got %d", limiter.CallCount())
	}
	if len(wireCommitRecords) != 1 {
		t.Fatalf("expected 1 wire commit record, got %d", len(wireCommitRecords))
	}
	if wireCommitRecords[0].PermitHeld {
		t.Fatal("expected PermitHeld=false in WireCommitResult")
	}
}

// TestCandidateAssessment_AssessorError_DeclinesToCanonical proves Requirement 1.5, 6.3:
// If AssessLargeBody returns an error, it declines and falls back to canonical Spec.Decode
// under the same held permit without failing the client request.
func TestCandidateAssessment_AssessorError_DeclinesToCanonical(t *testing.T) {
	t.Parallel()

	limiter := &trackingAdmissionLimiter{}
	exec := &testAssessorExecutor{}
	payload := buildJSONPayload(1200 * 1024)

	var decodeCalls int
	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return largebody.Assessment{}, errors.New("simulated transient assessor error")
	}

	spec := newAssessmentTestSpec(
		exec,
		minimalValidProofProfile(),
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		nil,
		func(dctx frontendpipe.DecodeContext) {
			decodeCalls++
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on error fallback, got %d", rec.Code)
	}
	if decodeCalls != 1 {
		t.Fatalf("expected Spec.Decode called once on error fallback, got %d", decodeCalls)
	}
	if limiter.CallCount() != 1 {
		t.Fatalf("expected strictly 1 TryAcquire call, got %d", limiter.CallCount())
	}
	if limiter.ReleaseCount() != 1 {
		t.Fatalf("expected strictly 1 release call, got %d", limiter.ReleaseCount())
	}
}

// TestCandidateAssessment_AssessorPanic_SafelyReleasesPermit proves cleanup safety:
// If AssessLargeBody panics, the held admission permit is safely released via deferred cleanup.
func TestCandidateAssessment_AssessorPanic_SafelyReleasesPermit(t *testing.T) {
	t.Parallel()

	limiter := &trackingAdmissionLimiter{}
	exec := &testAssessorExecutor{}
	payload := buildJSONPayload(1200 * 1024)

	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		panic("simulated assessor panic")
	}

	spec := newAssessmentTestSpec(
		exec,
		minimalValidProofProfile(),
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		nil,
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from assessor")
		}
		// Inflight bytes must be 0 and permit must have been released by defer
		if limiter.InflightBytes() != 0 {
			t.Fatalf("permit leak on panic: inflight bytes = %d, want 0", limiter.InflightBytes())
		}
		if limiter.ReleaseCount() != 1 {
			t.Fatalf("expected release called on panic, got %d", limiter.ReleaseCount())
		}
	}()

	frontendpipe.ServeHTTP(&spec, rec, req)
}
