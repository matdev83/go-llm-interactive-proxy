package frontendpipe_test

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/frontendpipe"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type mockSource struct {
	data []byte
}

func (m *mockSource) Size() int64 { return int64(len(m.data)) }
func (m *mockSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m.data)), nil
}
func (m *mockSource) Close() error { return nil }

type mockStream struct {
	events []lipapi.Event
	idx    int
}

func (s *mockStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.idx >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *mockStream) Close() error { return nil }

// TestResponseContext_NewAndAccessors tests Requirement 18.1, 18.2, 18.8:
// ResponseContext is bounded, frontend-owned, couples FrontendWireState + ExecutionResult (Facts + Session + Stream),
// and provides accessors without fabricating a fake lipapi.Call.
func TestResponseContext_NewAndAccessors(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	es := &mockStream{}
	defer func() { _ = es.Close() }()

	facts := largebody.ResponseFacts{
		RequestID:       proofOut.Seeds().DeterministicCallID,
		TraceID:         "trace_001",
		ALegID:          "aleg_test_1",
		SessionID:       "sess_test_1",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeNonStreaming,
		EffectiveModel:  "gpt-4o-2024-08-06",
		Source:          proofOut.Proof().Source,
		BodyBytes:       1024,
		RewrittenLength: 1024,
	}

	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess_test_1",
		ALegID:                 "aleg_test_1",
		ResumeToken:            largebody.NewSensitiveString("resume_tok_secret"),
	}

	execRes := largebody.ExecutionResult{
		Stream:  es,
		Facts:   facts,
		Session: carrier,
	}

	// Verify ExecutionResult.ResponseFacts() method exists and matches
	if execRes.ResponseFacts().RequestID != proofOut.Seeds().DeterministicCallID {
		t.Fatalf("expected execRes.ResponseFacts().RequestID to match, got %q", execRes.ResponseFacts().RequestID)
	}

	// Construct ResponseContext
	rc := frontendpipe.NewResponseContext(proofOut.State, execRes)

	// Verify accessors
	if rc.ProfileID() != "test_certified_profile_v1" {
		t.Errorf("ProfileID: got %q, want %q", rc.ProfileID(), "test_certified_profile_v1")
	}
	if rc.CallID() != proofOut.Seeds().DeterministicCallID {
		t.Errorf("CallID: got %q, want %q", rc.CallID(), proofOut.Seeds().DeterministicCallID)
	}
	if rc.DeterministicCallID() != proofOut.Seeds().DeterministicCallID {
		t.Errorf("DeterministicCallID: got %q, want %q", rc.DeterministicCallID(), proofOut.Seeds().DeterministicCallID)
	}
	if rc.DeterministicTimestamp() != proofOut.Seeds().DeterministicTimestamp {
		t.Errorf("DeterministicTimestamp: got %d, want %d", rc.DeterministicTimestamp(), proofOut.Seeds().DeterministicTimestamp)
	}
	if rc.RouteSelector() != "gpt-4o" {
		t.Errorf("RouteSelector: got %q, want %q", rc.RouteSelector(), "gpt-4o")
	}
	if rc.ClientModel() != "gpt-4o" {
		t.Errorf("ClientModel: got %q, want %q", rc.ClientModel(), "gpt-4o")
	}
	if rc.EffectiveModel() != "gpt-4o-2024-08-06" {
		t.Errorf("EffectiveModel: got %q, want %q", rc.EffectiveModel(), "gpt-4o-2024-08-06")
	}
	if rc.IsStream() {
		t.Errorf("IsStream: got true, want false")
	}
	if rc.SessionID() != "sess_test_1" {
		t.Errorf("SessionID: got %q, want %q", rc.SessionID(), "sess_test_1")
	}
	if rc.ALegID() != "aleg_test_1" {
		t.Errorf("ALegID: got %q, want %q", rc.ALegID(), "aleg_test_1")
	}
	if rc.ResumeToken().Reveal() != "resume_tok_secret" {
		t.Errorf("ResumeToken: got %q, want %q", rc.ResumeToken().Reveal(), "resume_tok_secret")
	}
	if rc.TraceID() != "trace_001" {
		t.Errorf("TraceID: got %q, want %q", rc.TraceID(), "trace_001")
	}
	if rc.Stream != es {
		t.Errorf("Stream: got %v, want %v", rc.Stream, es)
	}
	if rc.Facts().RequestID != facts.RequestID {
		t.Errorf("Facts: got %+v, want %+v", rc.Facts(), facts)
	}
	if rc.SessionCarrier().AuthoritativeSessionID != carrier.AuthoritativeSessionID {
		t.Errorf("SessionCarrier: got %+v, want %+v", rc.SessionCarrier(), carrier)
	}
}

// TestResponseContext_Validate tests bounds and cross-consistency enforcement.
func TestResponseContext_Validate(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	facts := largebody.ResponseFacts{
		RequestID:       proofOut.Seeds().DeterministicCallID,
		TraceID:         "trace_1",
		ALegID:          "aleg_test_1",
		SessionID:       "sess_test_1",
		Operation:       lipapi.OperationOpenAIChatCompletions,
		Delivery:        lipapi.DeliveryModeNonStreaming,
		EffectiveModel:  "gpt-4o",
		Source:          proofOut.Proof().Source,
		BodyBytes:       1024,
		RewrittenLength: 1024,
	}

	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess_test_1",
		ALegID:                 "aleg_test_1",
		ResumeToken:            largebody.NewSensitiveString("tok_1"),
	}

	rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream:  &mockStream{},
		Facts:   facts,
		Session: carrier,
	})

	if err := rc.Validate(largebody.DefaultMaxSemanticFactBytes); err != nil {
		t.Fatalf("expected valid context, got error: %v", err)
	}

	// Budget <= 0 fails
	if err := rc.Validate(0); err == nil {
		t.Fatalf("expected error for budget <= 0, got nil")
	}

	// Inconsistent session ID
	inconsistentRC := rc
	inconsistentRC.ResponseFacts.SessionID = "different_sess"
	if err := inconsistentRC.Validate(largebody.DefaultMaxSemanticFactBytes); err == nil {
		t.Fatalf("expected error for inconsistent session ID, got nil")
	}

	// Inconsistent ALeg ID
	inconsistentRC = rc
	inconsistentRC.ResponseFacts.ALegID = "different_aleg"
	if err := inconsistentRC.Validate(largebody.DefaultMaxSemanticFactBytes); err == nil {
		t.Fatalf("expected error for inconsistent aleg ID, got nil")
	}

	// Inconsistent stream flag
	inconsistentRC = rc
	inconsistentRC.ResponseFacts.Delivery = lipapi.DeliveryModeStreaming
	if err := inconsistentRC.Validate(largebody.DefaultMaxSemanticFactBytes); err == nil {
		t.Fatalf("expected error for inconsistent delivery mode, got nil")
	}
}

// TestResponseContext_WriteSessionHeaders tests Requirement 18.2, 14.6:
// Writing session response carrier headers onto http.ResponseWriter and http.Header.
func TestResponseContext_WriteSessionHeaders(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess_auth_xyz",
		ALegID:                 "aleg_abc",
		ResumeToken:            largebody.NewSensitiveString("sensitive_resume_token_999"),
	}

	rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream: &mockStream{},
		Facts: largebody.ResponseFacts{
			RequestID:       proofOut.Seeds().DeterministicCallID,
			TraceID:         "trace_xyz",
			ALegID:          "aleg_abc",
			SessionID:       "sess_auth_xyz",
			Operation:       lipapi.OperationOpenAIChatCompletions,
			Delivery:        lipapi.DeliveryModeNonStreaming,
			EffectiveModel:  "gpt-4o",
			Source:          proofOut.Proof().Source,
			BodyBytes:       1024,
			RewrittenLength: 1024,
		},
		Session: carrier,
	})

	rec := httptest.NewRecorder()
	rc.WriteSessionHeaders(rec)

	if got := rec.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess_auth_xyz" {
		t.Errorf("Header %s: got %q, want %q", sessionwire.HeaderAuthoritativeSessionID, got, "sess_auth_xyz")
	}
	if got := rec.Header().Get(sessionwire.HeaderALegID); got != "aleg_abc" {
		t.Errorf("Header %s: got %q, want %q", sessionwire.HeaderALegID, got, "aleg_abc")
	}
	if got := rec.Header().Get(sessionwire.HeaderResumeToken); got != "sensitive_resume_token_999" {
		t.Errorf("Header %s: got %q, want %q", sessionwire.HeaderResumeToken, got, "sensitive_resume_token_999")
	}

	// Verify token is never in String() representation
	str := rc.Session.String()
	if strings.Contains(str, "sensitive_resume_token_999") {
		t.Fatalf("sensitive token leaked into String(): %s", str)
	}

	// Empty carrier does not set headers
	emptyRC := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream: &mockStream{},
	})
	emptyRec := httptest.NewRecorder()
	emptyRC.WriteSessionHeaders(emptyRec)
	if emptyRec.Header().Get(sessionwire.HeaderAuthoritativeSessionID) != "" {
		t.Errorf("expected no session header on empty carrier")
	}
}

// TestResponseContext_DrivesWireWrappingAndWriters tests Requirement 18:
// Wire execution creates bounded ResponseContext, invokes WireWrapStream, writes session carrier headers,
// invokes WireWriteStream, and supplies OnWireCommit with the ResponseContext.
func TestResponseContext_DrivesWireWrappingAndWriters(t *testing.T) {
	t.Parallel()

	exec := &testAssessorExecutor{}
	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return makeAcceptedAssessment(proof)
	}

	es := &mockStream{}
	defer func() { _ = es.Close() }()

	exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		return largebody.ExecutionResult{
			Stream: es,
			Facts: largebody.ResponseFacts{
				RequestID:       accepted.Stamp.GenerationID(),
				TraceID:         "trace_wire_001",
				ALegID:          "aleg_test_1",
				SessionID:       "sess_test_1",
				Operation:       accepted.WireRequest.Operation,
				Delivery:        accepted.WireRequest.Delivery,
				EffectiveModel:  "effective-provider-model",
				Source:          accepted.Stamp.SourceDigest(),
				BodyBytes:       src.Size(),
				RewrittenLength: 100,
			},
			Session: largebody.SessionResponseCarrier{
				AuthoritativeSessionID: "sess_wire_test",
				ALegID:                 "aleg_wire_test",
				ResumeToken:            largebody.NewSensitiveString("secret_tok_wire"),
			},
		}, nil
	}

	var wireWrapStreamCalled bool
	var wireWriteStreamCalled bool
	var wireWriteNonStreamCalled bool
	var observedCommitResult frontendpipe.WireCommitResult

	spec := frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                 exec,
			DefaultRouteSelector: "gpt-4o",
			MaxRequestBodyBytes:  10 * 1024 * 1024,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1024, // 1 KiB threshold
			},
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: minimalValidProofProfile(),
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			t.Fatal("Spec.Decode should NOT be called on wire execution fast path")
			return nil, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			t.Fatal("BuildEncodeOpts should NOT be called on wire execution fast path")
			return struct{}{}
		},
		WriteStream: func(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, stream lipapi.EventStream, opts struct{}) error {
			t.Fatal("canonical WriteStream should NOT be called on wire execution fast path")
			return nil
		},
		WireWrapStream: func(ctx context.Context, rc frontendpipe.ResponseContext, inner lipapi.EventStream) (lipapi.EventStream, error) {
			wireWrapStreamCalled = true
			if rc.CallID() == "" {
				t.Errorf("WireWrapStream rc.CallID is empty")
			}
			if rc.EffectiveModel() != "effective-provider-model" {
				t.Errorf("WireWrapStream rc.EffectiveModel: got %q, want %q", rc.EffectiveModel(), "effective-provider-model")
			}
			return inner, nil
		},
		WireWriteStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, stream lipapi.EventStream) error {
			wireWriteStreamCalled = true
			return nil
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, stream lipapi.EventStream) error {
			wireWriteNonStreamCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":"wire_non_stream"}`))
			return nil
		},
		OnWireCommit: func(r *http.Request, res frontendpipe.WireCommitResult) {
			observedCommitResult = res
		},
	}

	payload := buildJSONPayload(1200 * 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !wireWrapStreamCalled {
		t.Errorf("expected WireWrapStream to be called")
	}
	if !wireWriteNonStreamCalled {
		t.Errorf("expected WireWriteNonStream to be called for non-streaming request")
	}
	if wireWriteStreamCalled {
		t.Errorf("expected WireWriteStream NOT to be called for non-streaming request")
	}

	// Verify session carrier headers were written by the response context
	if got := rec.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess_wire_test" {
		t.Errorf("Session header: got %q, want %q", got, "sess_wire_test")
	}
	if got := rec.Header().Get(sessionwire.HeaderALegID); got != "aleg_wire_test" {
		t.Errorf("ALeg header: got %q, want %q", got, "aleg_wire_test")
	}
	if got := rec.Header().Get(sessionwire.HeaderResumeToken); got != "secret_tok_wire" {
		t.Errorf("Resume header: got %q, want %q", got, "secret_tok_wire")
	}

	// Verify WireCommitResult contains ResponseContext
	if observedCommitResult.ResponseContext.CallID() == "" {
		t.Errorf("observedCommitResult.ResponseContext.CallID should not be empty")
	}
}

// TestResponseContext_WireWrapStreamError proves that when WireWrapStream fails,
// it writes an execute error JSON and does NOT call writers.
func TestResponseContext_WireWrapStreamError(t *testing.T) {
	t.Parallel()

	exec := &testAssessorExecutor{}
	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return makeAcceptedAssessment(proof)
	}
	exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		return largebody.ExecutionResult{
			Stream: &mockStream{},
			Facts: largebody.ResponseFacts{
				RequestID:       accepted.Stamp.GenerationID(),
				TraceID:         "trace_001",
				ALegID:          "aleg_test_1",
				SessionID:       "sess_test_1",
				Operation:       accepted.WireRequest.Operation,
				Delivery:        accepted.WireRequest.Delivery,
				EffectiveModel:  "wire-model",
				Source:          accepted.Stamp.SourceDigest(),
				BodyBytes:       src.Size(),
				RewrittenLength: 100,
			},
		}, nil
	}

	var writerCalled bool
	spec := frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                 exec,
			DefaultRouteSelector: "gpt-4o",
			MaxRequestBodyBytes:  10 * 1024 * 1024,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1024,
			},
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: minimalValidProofProfile(),
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		WireWrapStream: func(ctx context.Context, rc frontendpipe.ResponseContext, inner lipapi.EventStream) (lipapi.EventStream, error) {
			return nil, errors.New("injected wire wrap stream failure")
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, stream lipapi.EventStream) error {
			writerCalled = true
			return nil
		},
	}

	payload := buildJSONPayload(1200 * 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected error status code, got %d", rec.Code)
	}
	if writerCalled {
		t.Errorf("writer should NOT be called after WireWrapStream error")
	}
}

// TestResponseContext_CoreDoesNotImportFrontendResponseState proves Requirement 18.8:
// Core packages (internal/core/...) MUST NOT import frontend response-state types or frontendpipe.
func TestResponseContext_CoreDoesNotImportFrontendResponseState(t *testing.T) {
	t.Parallel()

	// Walk internal/core directory and check all imports
	coreRoot := filepath.Join("..", "..", "..", "core")
	err := filepath.Walk(coreRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}
		fset := token.NewFileSet()
		node, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range node.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(importPath, "frontendpipe") {
				t.Errorf("core file %s illegally imports frontendpipe: %s", path, importPath)
			}
			if !strings.HasSuffix(info.Name(), "_test.go") && strings.Contains(importPath, "internal/plugins/frontends") {
				t.Errorf("core production file %s illegally imports frontends package: %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk core root: %v", err)
	}
}
