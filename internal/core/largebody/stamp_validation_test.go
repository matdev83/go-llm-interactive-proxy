package largebody_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func sha256Digest(b byte) [32]byte {
	var d [32]byte
	for i := range d {
		d[i] = b
	}
	return d
}

func makeValidProof(gen string) (largebody.Proof, [32]byte) {
	srcDigest := sha256.Sum256([]byte(`{"model":"gpt-4o","prompt":"hello"}`))
	identDigest := sha256.Sum256([]byte(`canonical-identity`))

	proof := largebody.Proof{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		RouteSelector:   "default",
		ClientModel:     "gpt-4o",
		MaxOutputTokens: 4096,
		Facts:           largebody.ProtocolFacts{RequirementsID: "chat-v1", ControlCount: 0},
		Mode:            largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		ModelSpan:       largebody.Span{},
		Identity:        largebody.NewIdentityDigest(identDigest),
		Turn: largebody.ClientTurnShape{
			Items: []largebody.ClientTurnItemShape{
				{
					Kind:    lipapi.ItemKindMessage,
					Role:    lipapi.RoleUser,
					Ordinal: 0,
					Parts: []largebody.ClientTurnPartShape{
						{Kind: lipapi.ContentPartText, ContentBytes: 32},
					},
				},
			},
			TotalContentBytes: 32,
		},
		Session: largebody.SessionInput{
			AuthoritativeSessionID: "sess-1",
			ClientSessionID:        "client-1",
			ALegID:                 "a-leg-1",
		},
		Source:    largebody.NewSourceDigest(srcDigest),
		BodyBytes: int64(len(`{"model":"gpt-4o","prompt":"hello"}`)),
	}
	return proof, srcDigest
}

// TestTask11_8_StampBindsCandidateDomainGeneration verifies that AssessmentStamp
// binds generation identity, profile/proof identity, source digest/size, body mode/rewrite contract,
// and candidate/domain proof generation (Requirements 6.7, 8.5; Task 11.8).
func TestTask11_8_StampBindsCandidateDomainGeneration(t *testing.T) {
	proof, srcDigest := makeValidProof("gen-1")
	identDigest := sha256Digest(0xaa)

	// 1. NewAssessmentStamp with explicit candidateDomainGen
	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-chat",
		largebody.NewSourceDigest(srcDigest),
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		largebody.NewIdentityDigest(identDigest),
		"domain-gen-1",
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}

	if stamp.GenerationID() != "gen-1" {
		t.Errorf("GenerationID = %q, want %q", stamp.GenerationID(), "gen-1")
	}
	if stamp.ProfileID() != "openai-chat" {
		t.Errorf("ProfileID = %q, want %q", stamp.ProfileID(), "openai-chat")
	}
	if stamp.SourceDigest() != largebody.NewSourceDigest(srcDigest) {
		t.Errorf("SourceDigest mismatch")
	}
	if stamp.BodyBytes() != proof.BodyBytes {
		t.Errorf("BodyBytes = %d, want %d", stamp.BodyBytes(), proof.BodyBytes)
	}
	if stamp.BodyMode() != proof.Mode {
		t.Errorf("BodyMode mismatch")
	}
	if stamp.Rewrite() != proof.Rewrite {
		t.Errorf("Rewrite mismatch")
	}
	if stamp.IdentityDigest() != largebody.NewIdentityDigest(identDigest) {
		t.Errorf("IdentityDigest mismatch")
	}
	if stamp.CandidateDomainGeneration() != "domain-gen-1" {
		t.Errorf("CandidateDomainGeneration = %q, want %q", stamp.CandidateDomainGeneration(), "domain-gen-1")
	}

	// 2. Default candidateDomainGen falls back to generationID
	defaultStamp, err := largebody.NewAssessmentStamp(
		"gen-2",
		"openai-chat",
		largebody.NewSourceDigest(srcDigest),
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		largebody.NewIdentityDigest(identDigest),
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp default error: %v", err)
	}
	if defaultStamp.CandidateDomainGeneration() != "gen-2" {
		t.Errorf("default CandidateDomainGeneration = %q, want %q", defaultStamp.CandidateDomainGeneration(), "gen-2")
	}

	// 3. BindAssessmentStamp helper
	boundStamp, err := largebody.BindAssessmentStamp("gen-1", proof, "domain-gen-1")
	if err != nil {
		t.Fatalf("BindAssessmentStamp error: %v", err)
	}
	if boundStamp.GenerationID() != "gen-1" {
		t.Errorf("bound GenerationID = %q, want %q", boundStamp.GenerationID(), "gen-1")
	}
	if boundStamp.ProfileID() != proof.ProfileID {
		t.Errorf("bound ProfileID = %q, want %q", boundStamp.ProfileID(), proof.ProfileID)
	}
	if boundStamp.CandidateDomainGeneration() != "domain-gen-1" {
		t.Errorf("bound CandidateDomainGeneration = %q, want %q", boundStamp.CandidateDomainGeneration(), "domain-gen-1")
	}

	// 4. Verify opacity: all fields must remain unexported
	stampTyp := reflect.TypeOf(largebody.AssessmentStamp{})
	for i := 0; i < stampTyp.NumField(); i++ {
		field := stampTyp.Field(i)
		if field.PkgPath == "" {
			t.Fatalf("AssessmentStamp field %q is exported; stamp must stay opaque (Requirement 6.7)", field.Name)
		}
	}
}

// TestTask11_8_ExecuteRevalidation_DisagreementIsInvariantFailure verifies that
// ExecuteLargeBody revalidates generation/profile/source/size/mode/rewrite/candidate-domain generation
// against live facts, and that ANY disagreement produces an invariant failure error, NEVER fallback to canonical
// (Requirements 6.6, 6.7, 8.5; Task 11.8).
func TestTask11_8_ExecuteRevalidation_DisagreementIsInvariantFailure(t *testing.T) {
	proof, srcDigest := makeValidProof("gen-1")
	identDigest := sha256Digest(0xbb)

	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-chat",
		largebody.NewSourceDigest(srcDigest),
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		largebody.NewIdentityDigest(identDigest),
		"domain-gen-1",
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}

	wireReq := largebody.WireRequestFacts{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		ClientModel:     "gpt-4o",
		CandidateModel:  "gpt-4o",
		MaxOutputTokens: 4096,
	}

	wireDomain := largebody.WireDomainFacts{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		CandidateModels: []string{"gpt-4o"},
	}

	accepted, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		t.Fatalf("NewAcceptedAssessment error: %v", err)
	}

	baseLive := largebody.LiveExecutionFacts{
		GenerationID:              "gen-1",
		ProfileID:                 "openai-chat",
		Source:                    largebody.NewSourceDigest(srcDigest),
		BodyBytes:                 proof.BodyBytes,
		Mode:                      proof.Mode,
		Rewrite:                   proof.Rewrite,
		CandidateDomainGeneration: "domain-gen-1",
	}

	// Baseline matching facts must validate successfully
	if err := largebody.ValidateAssessmentStamp(stamp, baseLive); err != nil {
		t.Fatalf("matching live facts rejected: %v", err)
	}

	// Disagreement test cases: each must fail with ErrStampDisagreement (invariant failure)
	tests := []struct {
		name      string
		modify    func(f *largebody.LiveExecutionFacts)
		wantField string
	}{
		{
			name: "generation identity mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.GenerationID = "gen-2-drifted"
			},
			wantField: "generation",
		},
		{
			name: "profile identity mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.ProfileID = "openai-responses-v1"
			},
			wantField: "profile",
		},
		{
			name: "source digest mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.Source = largebody.NewSourceDigest(sha256Digest(0x99))
			},
			wantField: "source",
		},
		{
			name: "body size mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.BodyBytes = proof.BodyBytes + 1
			},
			wantField: "body size",
		},
		{
			name: "body mode mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.Mode = largebody.BodyMode("compressed_json")
			},
			wantField: "body mode",
		},
		{
			name: "rewrite contract mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.Rewrite, _ = largebody.NewModelTokenRewrite(largebody.Span{Offset: 10, Length: 5})
			},
			wantField: "rewrite",
		},
		{
			name: "candidate domain generation mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.CandidateDomainGeneration = "domain-gen-2-drifted"
			},
			wantField: "candidate/domain proof generation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			live := baseLive
			tc.modify(&live)

			err := largebody.ValidateAssessmentStamp(stamp, live)
			if err == nil {
				t.Fatalf("%s: expected invariant failure, got nil", tc.name)
			}

			// Invariant: must wrap ErrStampDisagreement
			if !errors.Is(err, largebody.ErrStampDisagreement) {
				t.Fatalf("%s: error must be ErrStampDisagreement, got %v", tc.name, err)
			}

			// Invariant: error message must detail the mismatched field
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantField) {
				t.Errorf("%s: error %q does not mention %q", tc.name, err.Error(), tc.wantField)
			}

			// Invariant: must never be a decline or request canonical fallback
			var invErr *largebody.StampInvariantError
			if !errors.As(err, &invErr) {
				t.Fatalf("%s: error must be *largebody.StampInvariantError, got %T", tc.name, err)
			}
			if !invErr.IsInvariantFailure() {
				t.Fatalf("%s: IsInvariantFailure() must report true", tc.name)
			}
		})
	}

	// Also test ExecuteLargeBody via StampValidatingExecutor:
	// Disagreement must abort execution immediately, never invoke delegate, never fallback.
	for _, tc := range tests {
		t.Run("executor/"+tc.name, func(t *testing.T) {
			live := baseLive
			tc.modify(&live)

			delegateCalled := false
			delegate := &mockWireExecutor{
				executeFunc: func(_ context.Context, _ largebody.Assessment, _ largebody.Source) (largebody.ExecutionResult, error) {
					delegateCalled = true
					return largebody.ExecutionResult{}, nil
				},
			}

			exec := largebody.NewStampValidatingExecutor(live, delegate)
			_, execErr := exec.ExecuteLargeBody(context.Background(), accepted, nil)
			if execErr == nil {
				t.Fatalf("executor %s: expected invariant failure, got nil", tc.name)
			}
			if !errors.Is(execErr, largebody.ErrStampDisagreement) {
				t.Fatalf("executor %s: expected ErrStampDisagreement, got %v", tc.name, execErr)
			}
			if delegateCalled {
				t.Fatalf("executor %s: delegate was invoked; execution must abort immediately on invariant failure", tc.name)
			}
		})
	}
}

// TestTask11_8_ExecuteRevalidation_ValidFactsExecuteSuccessfully verifies that
// matching live facts pass revalidation and proceed to execution.
func TestTask11_8_ExecuteRevalidation_ValidFactsExecuteSuccessfully(t *testing.T) {
	proof, srcDigest := makeValidProof("gen-1")
	identDigest := sha256Digest(0xcc)

	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-chat",
		largebody.NewSourceDigest(srcDigest),
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		largebody.NewIdentityDigest(identDigest),
		"domain-gen-1",
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}

	wireReq := largebody.WireRequestFacts{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		ClientModel:     "gpt-4o",
		CandidateModel:  "gpt-4o",
		MaxOutputTokens: 4096,
	}

	wireDomain := largebody.WireDomainFacts{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		CandidateModels: []string{"gpt-4o"},
	}

	accepted, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		t.Fatalf("NewAcceptedAssessment error: %v", err)
	}

	live := largebody.LiveExecutionFacts{
		GenerationID:              "gen-1",
		ProfileID:                 "openai-chat",
		Source:                    largebody.NewSourceDigest(srcDigest),
		BodyBytes:                 proof.BodyBytes,
		Mode:                      proof.Mode,
		Rewrite:                   proof.Rewrite,
		CandidateDomainGeneration: "domain-gen-1",
	}

	delegateCalled := false
	expectedResult := largebody.ExecutionResult{}
	delegate := &mockWireExecutor{
		executeFunc: func(_ context.Context, acc largebody.Assessment, _ largebody.Source) (largebody.ExecutionResult, error) {
			delegateCalled = true
			if acc.Stamp.GenerationID() != "gen-1" {
				t.Errorf("delegate received stamp with GenerationID %q", acc.Stamp.GenerationID())
			}
			return expectedResult, nil
		},
	}

	exec := largebody.NewStampValidatingExecutor(live, delegate)
	result, err := exec.ExecuteLargeBody(context.Background(), accepted, nil)
	if err != nil {
		t.Fatalf("ExecuteLargeBody error: %v", err)
	}
	if !delegateCalled {
		t.Fatal("delegate was not called for valid facts")
	}
	_ = result
}

// TestTask11_8_ExecuteRevalidation_InvalidAcceptedAssessment verifies that
// executing with an unaccepted, declined, or zero-stamp assessment fails fail-closed
// with an invariant error.
func TestTask11_8_ExecuteRevalidation_InvalidAcceptedAssessment(t *testing.T) {
	live := largebody.LiveExecutionFacts{
		GenerationID:              "gen-1",
		ProfileID:                 "openai-chat",
		Source:                    largebody.NewSourceDigest(sha256Digest(0x01)),
		BodyBytes:                 100,
		Mode:                      largebody.BodyModeIdentityJSON,
		Rewrite:                   largebody.NewNoRewrite(),
		CandidateDomainGeneration: "domain-gen-1",
	}

	exec := largebody.NewStampValidatingExecutor(live, nil)

	// 1. Zero assessment
	if _, err := exec.ExecuteLargeBody(context.Background(), largebody.Assessment{}, nil); err == nil {
		t.Fatal("zero assessment must fail")
	}

	// 2. Declined assessment
	declined, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
	if _, err := exec.ExecuteLargeBody(context.Background(), declined, nil); err == nil {
		t.Fatal("declined assessment must fail invariant at execution")
	}

	// 3. Zero stamp in accepted assessment
	emptyStampAcc := largebody.Assessment{
		Decision: largebody.AssessmentDecisionAccept,
	}
	if _, err := exec.ExecuteLargeBody(context.Background(), emptyStampAcc, nil); err == nil {
		t.Fatal("accepted assessment with zero stamp must fail")
	}
}

// TestTask11_8_BackendWireProofAssessor_ExecuteLargeBody verifies that
// BackendWireProofAssessor implements LargeBodyWireExecutor (and therefore LargeBodyExecutor),
// binding the stamp at assessment and revalidating it at execute time.
func TestTask11_8_BackendWireProofAssessor_ExecuteLargeBody(t *testing.T) {
	proof, srcDigest := makeValidProof("gen-1")
	identDigest := sha256Digest(0xdd)

	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-chat",
		largebody.NewSourceDigest(srcDigest),
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		largebody.NewIdentityDigest(identDigest),
		"domain-gen-1",
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}

	wireReq := largebody.WireRequestFacts{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		ClientModel:     "gpt-4o",
		CandidateModel:  "gpt-4o",
		MaxOutputTokens: 4096,
	}

	wireDomain := largebody.WireDomainFacts{
		ProfileID:       "openai-chat",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		CandidateModels: []string{"gpt-4o"},
	}

	assessor := largebody.NewBackendWireProofAssessor(largebody.BackendWireProofGate{})
	assessor.GenerationID = "gen-1"
	assessor.CandidateDomainGeneration = "domain-gen-1"
	assessor.AcceptStamp = stamp
	assessor.AcceptWireReq = wireReq
	assessor.AcceptDomain = wireDomain

	// Verify interface compliance
	var _ largebody.LargeBodyExecutor = assessor
	var _ largebody.LargeBodyWireExecutor = assessor

	accepted, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("AssessLargeBody error: %v", err)
	}
	if !accepted.Accepted() {
		t.Fatalf("expected accepted assessment, got %v", accepted)
	}

	// 1. ExecuteLargeBody succeeds with matching generation and facts
	_, err = assessor.ExecuteLargeBody(context.Background(), accepted, nil)
	if err != nil {
		t.Fatalf("ExecuteLargeBody error: %v", err)
	}

	// 2. Generation drift before execute => invariant failure
	assessor.GenerationID = "gen-2-drifted"
	_, err = assessor.ExecuteLargeBody(context.Background(), accepted, nil)
	if err == nil {
		t.Fatal("ExecuteLargeBody with drifted GenerationID must fail")
	}
	if !errors.Is(err, largebody.ErrStampDisagreement) {
		t.Fatalf("expected ErrStampDisagreement, got %v", err)
	}

	// 3. Domain generation drift before execute => invariant failure
	assessor.GenerationID = "gen-1"
	assessor.CandidateDomainGeneration = "domain-gen-2-drifted"
	_, err = assessor.ExecuteLargeBody(context.Background(), accepted, nil)
	if err == nil {
		t.Fatal("ExecuteLargeBody with drifted CandidateDomainGeneration must fail")
	}
	if !errors.Is(err, largebody.ErrStampDisagreement) {
		t.Fatalf("expected ErrStampDisagreement, got %v", err)
	}
}

type mockWireExecutor struct {
	executeFunc func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error)
}

func (m *mockWireExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, accepted, src)
	}
	return largebody.ExecutionResult{}, nil
}
