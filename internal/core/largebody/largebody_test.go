package largebody_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// testFactBudget is deliberately tiny so bound enforcement stays cheap:
// any count or string length above it must be rejected.
const testFactBudget = 64

type stubSource struct{ size int64 }

func (s stubSource) Size() int64 { return s.size }
func (s stubSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("body")), nil
}
func (s stubSource) Close() error { return nil }

type stubStream struct{}

func (stubStream) Recv(context.Context) (lipapi.Event, error) { return lipapi.Event{}, io.EOF }
func (stubStream) Close() error                               { return nil }

func validProof() largebody.Proof {
	span, err := largebody.NewModelTokenRewrite(mustSpan(100, 12))
	if err != nil {
		panic(err)
	}
	return largebody.Proof{
		ProfileID:       "openai-responses-v1",
		Operation:       lipapi.OperationOpenAIResponses,
		Delivery:        lipapi.DeliveryModeStreaming,
		RouteSelector:   "openai/responses",
		ClientModel:     "gpt-5",
		MaxOutputTokens: 1024,
		Facts:           largebody.ProtocolFacts{RequirementsID: "responses-create-v1", ControlCount: 2},
		Mode:            largebody.BodyModeIdentityJSON,
		Rewrite:         span,
		ModelSpan:       mustSpan(100, 12),
		Identity:        largebody.NewIdentityDigest(digestOf(1)),
		Turn: largebody.ClientTurnShape{
			Items: []largebody.ClientTurnItemShape{
				{
					Kind:    lipapi.ItemKindMessage,
					Role:    lipapi.RoleUser,
					Ordinal: 0,
					Parts: []largebody.ClientTurnPartShape{
						{Kind: lipapi.ContentPartText, ContentBytes: 1 << 20},
					},
				},
			},
			TotalContentBytes: 1 << 20,
		},
		Session: largebody.SessionInput{
			AuthoritativeSessionID: "sess-1",
			ClientSessionID:        "client-1",
			ALegID:                 "a-leg-1",
		},
		Source:    largebody.NewSourceDigest(digestOf(2)),
		BodyBytes: 1 << 20,
	}
}

func mustSpan(offset, length int64) largebody.Span {
	span := largebody.Span{Offset: offset, Length: length}
	if err := span.Validate(); err != nil {
		panic(err)
	}
	return span
}

func digestOf(fill byte) [32]byte {
	var sum [32]byte
	for i := range sum {
		sum[i] = fill + byte(i)
	}
	return sum
}

func validStamp() largebody.AssessmentStamp {
	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-responses-v1",
		largebody.NewSourceDigest(digestOf(2)),
		1<<20,
		largebody.BodyModeIdentityJSON,
		largebody.NewNoRewrite(),
		largebody.NewIdentityDigest(digestOf(1)),
	)
	if err != nil {
		panic(err)
	}
	return stamp
}

func TestSource_ContractShape(t *testing.T) {
	var src largebody.Source = stubSource{size: 8}
	if got := src.Size(); got != 8 {
		t.Fatalf("Size() = %d, want 8", got)
	}
	rc, err := src.Open()
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read replay reader: %v", err)
	}
	if closeErr := rc.Close(); closeErr != nil {
		t.Fatalf("reader Close() error = %v", closeErr)
	}
	if closeErr := src.Close(); closeErr != nil {
		t.Fatalf("source Close() error = %v", closeErr)
	}
	if string(body) != "body" {
		t.Fatalf("replay bytes = %q, want %q", body, "body")
	}
}

func TestSpan_Validate(t *testing.T) {
	if err := (largebody.Span{Offset: 0, Length: 8}).Validate(); err != nil {
		t.Fatalf("valid span rejected: %v", err)
	}
	for _, span := range []largebody.Span{{Offset: -1, Length: 8}, {Offset: 0, Length: -1}} {
		if err := span.Validate(); err == nil {
			t.Fatalf("span %+v must be rejected", span)
		}
	}
	overflow := largebody.Span{Offset: math.MaxInt64 - 4, Length: 8}
	if err := overflow.Validate(); err == nil {
		t.Fatal("overflowing span must be rejected")
	}
	if _, err := overflow.End(); err == nil {
		t.Fatal("overflowing span End() must fail")
	}
	end, err := (largebody.Span{Offset: 100, Length: 12}).End()
	if err != nil || end != 112 {
		t.Fatalf("End() = %d, %v; want 112, nil", end, err)
	}
}

func TestCheckedSpliceLength(t *testing.T) {
	got, err := largebody.CheckedSpliceLength(100, 5, 200)
	if err != nil || got != 305 {
		t.Fatalf("CheckedSpliceLength = %d, %v; want 305, nil", got, err)
	}
	for _, lens := range [][3]int64{{-1, 5, 200}, {100, -1, 200}, {100, 5, -1}, {math.MaxInt64, 1, 0}, {math.MaxInt64 - 4, 4, 1}} {
		if _, err := largebody.CheckedSpliceLength(lens[0], lens[1], lens[2]); err == nil {
			t.Fatalf("CheckedSpliceLength(%v) must fail", lens)
		}
	}
}

func TestBodyMode_ValidateAndString(t *testing.T) {
	if err := largebody.BodyModeIdentityJSON.Validate(); err != nil {
		t.Fatalf("identity body mode rejected: %v", err)
	}
	if err := largebody.BodyMode("").Validate(); err == nil {
		t.Fatal("unknown body mode must be rejected")
	}
	if err := largebody.BodyMode("provider-native").Validate(); err == nil {
		t.Fatal("unregistered body mode must be rejected")
	}
	if got := largebody.BodyModeIdentityJSON.String(); got == "" || strings.Contains(got, " ") {
		t.Fatalf("body mode label = %q, want bounded static label", got)
	}
}

func TestRewriteSemantics_Immutable(t *testing.T) {
	none := largebody.NewNoRewrite()
	if none.NeedsModelRewrite() {
		t.Fatal("no-rewrite must not need a model rewrite")
	}
	if err := none.Validate(); err != nil {
		t.Fatalf("no-rewrite rejected: %v", err)
	}
	rewrite, err := largebody.NewModelTokenRewrite(mustSpan(100, 12))
	if err != nil {
		t.Fatalf("model token rewrite rejected: %v", err)
	}
	if !rewrite.NeedsModelRewrite() {
		t.Fatal("model token rewrite must need a model rewrite")
	}
	if got := rewrite.Span(); got != mustSpan(100, 12) {
		t.Fatalf("Span() = %+v, want offset 100 length 12", got)
	}
	if _, err := largebody.NewModelTokenRewrite(largebody.Span{Offset: -1, Length: 4}); err == nil {
		t.Fatal("rewrite with invalid span must be rejected")
	}
	if _, err := largebody.NewModelTokenRewrite(largebody.Span{Offset: 4, Length: 0}); err == nil {
		t.Fatal("rewrite with empty span must be rejected")
	}
	typ := reflect.TypeOf(largebody.RewriteSemantics{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).PkgPath == "" {
			t.Fatalf("RewriteSemantics field %q is exported; semantics must be immutable", typ.Field(i).Name)
		}
	}
}

func TestSensitiveString_Redaction(t *testing.T) {
	const secret = "lip-resume-token-abc123"
	s := largebody.NewSensitiveString(secret)
	if got := s.Reveal(); got != secret {
		t.Fatalf("Reveal() = %q, want secret", got)
	}
	renderings := map[string]string{
		"String":   s.String(),
		"GoString": s.GoString(),
		"SprintfV": fmt.Sprintf("%v", s),
		"SprintfS": fmt.Sprintf("%s", s),
		"SprintfQ": fmt.Sprintf("%q", s),
	}
	for name, rendered := range renderings {
		if strings.Contains(rendered, secret) {
			t.Fatalf("%s leaks the secret: %q", name, rendered)
		}
		if !strings.Contains(rendered, "redacted") {
			t.Fatalf("%s = %q, want explicit redaction marker", name, rendered)
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("MarshalJSON error = %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("MarshalJSON leaks the secret: %s", raw)
	}
	text, err := s.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error = %v", err)
	}
	if strings.Contains(string(text), secret) {
		t.Fatalf("MarshalText leaks the secret: %s", text)
	}
	if largebody.NewSensitiveString("").IsZero() != true {
		t.Fatal("empty sensitive string must report IsZero")
	}
	if s.IsZero() {
		t.Fatal("non-empty sensitive string must not report IsZero")
	}
}

func TestProof_Validate_Bounded(t *testing.T) {
	if err := validProof().Validate(testFactBudget); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	if err := validProof().Validate(0); err == nil {
		t.Fatal("non-positive fact budget must be rejected")
	}
	oversize := strings.Repeat("m", testFactBudget+1)
	cases := map[string]func(*largebody.Proof){
		"profile":  func(p *largebody.Proof) { p.ProfileID = oversize },
		"selector": func(p *largebody.Proof) { p.RouteSelector = oversize },
		"model":    func(p *largebody.Proof) { p.ClientModel = oversize },
	}
	for name, mutate := range cases {
		proof := validProof()
		mutate(&proof)
		if err := proof.Validate(testFactBudget); err == nil {
			t.Fatalf("oversize %s must be rejected", name)
		}
	}
	empty := validProof()
	empty.ProfileID = ""
	if err := empty.Validate(testFactBudget); err == nil {
		t.Fatal("empty profile ID must be rejected")
	}
	badDelivery := validProof()
	badDelivery.Delivery = "eventually"
	if err := badDelivery.Validate(testFactBudget); err == nil {
		t.Fatal("unknown delivery mode must be rejected")
	}
	zeroBody := validProof()
	zeroBody.BodyBytes = 0
	if err := zeroBody.Validate(testFactBudget); err == nil {
		t.Fatal("non-positive body size must be rejected")
	}
	rewriteMismatch := validProof()
	rewriteMismatch.Rewrite = largebody.NewNoRewrite()
	if err := rewriteMismatch.Validate(testFactBudget); err == nil {
		t.Fatal("proof needing a rewrite span without rewrite semantics must be rejected")
	}
}

func TestClientTurnShape_BoundedNoPromptText(t *testing.T) {
	if err := validProof().Turn.Validate(testFactBudget); err != nil {
		t.Fatalf("valid turn shape rejected: %v", err)
	}
	badRole := validProof().Turn
	badRole.Items[0].Role = "narrator"
	if err := badRole.Validate(testFactBudget); err == nil {
		t.Fatal("unknown role must be rejected")
	}
	badKind := validProof().Turn
	badKind.Items[0].Parts[0].Kind = "prompt_text"
	if err := badKind.Validate(testFactBudget); err == nil {
		t.Fatal("unknown part kind must be rejected")
	}
	negative := validProof().Turn
	negative.Items[0].Parts[0].ContentBytes = -1
	if err := negative.Validate(testFactBudget); err == nil {
		t.Fatal("negative content bytes must be rejected")
	}
	many := validProof().Turn
	many.Items = make([]largebody.ClientTurnItemShape, testFactBudget+1)
	if err := many.Validate(testFactBudget); err == nil {
		t.Fatal("item count above budget must be rejected")
	}
}

func TestDigests_DistinctNamespaces(t *testing.T) {
	identity := largebody.NewIdentityDigest(digestOf(1))
	source := largebody.NewSourceDigest(digestOf(1))
	if identity.IsZero() || source.IsZero() {
		t.Fatal("non-zero digests must not report IsZero")
	}
	if reflect.TypeOf(identity) == reflect.TypeOf(source) {
		t.Fatal("identity and source digests must be distinct types")
	}
	if identity.String() == "" || source.String() == "" {
		t.Fatal("digest labels must render without retaining content")
	}
	if identity.String() != source.String() {
		t.Fatal("same bytes should render the same hex in either namespace; namespaces differ by type, not rendering")
	}
	if largebody.NewIdentityDigest([32]byte{}).IsZero() != true {
		t.Fatal("zero identity digest must report IsZero")
	}
}

func TestAssessment_Lifecycle(t *testing.T) {
	req := largebody.AssessmentRequest{Proof: validProof(), GenerationID: "gen-1"}
	if err := req.Validate(testFactBudget); err != nil {
		t.Fatalf("valid assessment request rejected: %v", err)
	}
	emptyGen := req
	emptyGen.GenerationID = ""
	if err := emptyGen.Validate(testFactBudget); err == nil {
		t.Fatal("empty generation binding must be rejected")
	}
	declined := largebody.AssessmentResult{Decision: largebody.AssessmentDecisionDecline, Reason: largebody.DeclineReasonAuthorityBlocker}
	if err := declined.Validate(testFactBudget); err != nil {
		t.Fatalf("valid decline rejected: %v", err)
	}
	declineNoReason := largebody.AssessmentResult{Decision: largebody.AssessmentDecisionDecline}
	if err := declineNoReason.Validate(testFactBudget); err == nil {
		t.Fatal("decline without a bounded reason must be rejected")
	}
	accepted := largebody.AssessmentResult{Decision: largebody.AssessmentDecisionAccept, Reason: largebody.DeclineReasonNone, Stamp: validStamp()}
	if err := accepted.Validate(testFactBudget); err != nil {
		t.Fatalf("valid accept rejected: %v", err)
	}
	acceptNoStamp := largebody.AssessmentResult{Decision: largebody.AssessmentDecisionAccept, Reason: largebody.DeclineReasonNone}
	if err := acceptNoStamp.Validate(testFactBudget); err == nil {
		t.Fatal("accept without a stamp must be rejected")
	}
	unknown := largebody.AssessmentResult{}
	if err := unknown.Validate(testFactBudget); err == nil {
		t.Fatal("unknown decision must be rejected")
	}
	if got := largebody.AssessmentDecisionAccept.String(); got != "accept" {
		t.Fatalf("accept label = %q", got)
	}
	if got := largebody.DeclineReasonAuthorityBlocker.String(); got != "authority_blocker" {
		t.Fatalf("decline label = %q", got)
	}
}

func TestAssessmentStamp_BindsFacts(t *testing.T) {
	stamp := validStamp()
	if stamp.GenerationID() != "gen-1" || stamp.ProfileID() != "openai-responses-v1" {
		t.Fatalf("stamp binds %+v, want gen-1/openai-responses-v1", stamp)
	}
	if stamp.BodyBytes() != 1<<20 || stamp.BodyMode() != largebody.BodyModeIdentityJSON {
		t.Fatalf("stamp binds wrong body contract: %+v", stamp)
	}
	if _, err := largebody.NewAssessmentStamp("", "p", largebody.NewSourceDigest(digestOf(2)), 8, largebody.BodyModeIdentityJSON, largebody.NewNoRewrite(), largebody.NewIdentityDigest(digestOf(1))); err == nil {
		t.Fatal("stamp without generation binding must be rejected")
	}
	if _, err := largebody.NewAssessmentStamp("g", "p", largebody.NewSourceDigest(digestOf(2)), 0, largebody.BodyModeIdentityJSON, largebody.NewNoRewrite(), largebody.NewIdentityDigest(digestOf(1))); err == nil {
		t.Fatal("stamp with non-positive body size must be rejected")
	}
	if _, err := largebody.NewAssessmentStamp("g", "p", largebody.NewSourceDigest(digestOf(2)), 8, largebody.BodyMode(""), largebody.NewNoRewrite(), largebody.NewIdentityDigest(digestOf(1))); err == nil {
		t.Fatal("stamp with unknown body mode must be rejected")
	}
	typ := reflect.TypeOf(largebody.AssessmentStamp{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).PkgPath == "" {
			t.Fatalf("AssessmentStamp field %q is exported; the stamp must stay opaque", typ.Field(i).Name)
		}
	}
}

func TestWireFacts_Bounded(t *testing.T) {
	req := largebody.WireRequestFacts{
		ProfileID:      "openai-responses-v1",
		Operation:      lipapi.OperationOpenAIResponses,
		Delivery:       lipapi.DeliveryModeStreaming,
		BodyMode:       largebody.BodyModeIdentityJSON,
		Rewrite:        largebody.NewNoRewrite(),
		ClientModel:    "gpt-5",
		CandidateModel: "gpt-5",
	}
	if err := req.Validate(testFactBudget); err != nil {
		t.Fatalf("valid wire request facts rejected: %v", err)
	}
	badOp := req
	badOp.Operation = ""
	if err := badOp.Validate(testFactBudget); err == nil {
		t.Fatal("empty operation must be rejected")
	}
	oversize := req
	oversize.CandidateModel = strings.Repeat("m", testFactBudget+1)
	if err := oversize.Validate(testFactBudget); err == nil {
		t.Fatal("oversize candidate model must be rejected")
	}
	domain := largebody.WireDomainFacts{
		ProfileID:       "openai-responses-v1",
		Operation:       lipapi.OperationOpenAIResponses,
		Delivery:        lipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		CandidateModels: []string{"gpt-5"},
	}
	if err := domain.Validate(testFactBudget); err != nil {
		t.Fatalf("valid finite domain rejected: %v", err)
	}
	universal := largebody.WireDomainFacts{
		ProfileID:      "openai-responses-v1",
		Operation:      lipapi.OperationOpenAIResponses,
		Delivery:       lipapi.DeliveryModeStreaming,
		BodyMode:       largebody.BodyModeIdentityJSON,
		UniversalModel: true,
	}
	if err := universal.Validate(testFactBudget); err != nil {
		t.Fatalf("valid universal domain rejected: %v", err)
	}
	ambiguous := universal
	ambiguous.CandidateModels = []string{"gpt-5"}
	if err := ambiguous.Validate(testFactBudget); err == nil {
		t.Fatal("universal domain with enumerated models must be rejected")
	}
	empty := universal
	empty.UniversalModel = false
	if err := empty.Validate(testFactBudget); err == nil {
		t.Fatal("empty finite domain must be rejected")
	}
}

func TestRewritePlan_CheckedMath(t *testing.T) {
	plan := largebody.RewritePlan{
		Rewrite:          mustRewrite(),
		ReplacementModel: "gpt-5-mini",
		RewrittenLength:  1<<20 + 4,
	}
	if err := plan.Validate(testFactBudget); err != nil {
		t.Fatalf("valid rewrite plan rejected: %v", err)
	}
	noRewrite := plan
	noRewrite.Rewrite = largebody.NewNoRewrite()
	if err := noRewrite.Validate(testFactBudget); err == nil {
		t.Fatal("rewrite plan without certified rewrite semantics must be rejected")
	}
	emptyModel := plan
	emptyModel.ReplacementModel = ""
	if err := emptyModel.Validate(testFactBudget); err == nil {
		t.Fatal("rewrite plan without replacement model must be rejected")
	}
	badLength := plan
	badLength.RewrittenLength = 0
	if err := badLength.Validate(testFactBudget); err == nil {
		t.Fatal("rewrite plan without rewritten length must be rejected")
	}
}

func mustRewrite() largebody.RewriteSemantics {
	rewrite, err := largebody.NewModelTokenRewrite(mustSpan(100, 12))
	if err != nil {
		panic(err)
	}
	return rewrite
}

func TestExecutionResult_Bounded(t *testing.T) {
	result := largebody.ExecutionResult{
		Stream: stubStream{},
		Facts: largebody.ResponseFacts{
			RequestID:       "req-1",
			TraceID:         "trace-1",
			ALegID:          "a-leg-1",
			SessionID:       "sess-1",
			Operation:       lipapi.OperationOpenAIResponses,
			Delivery:        lipapi.DeliveryModeStreaming,
			EffectiveModel:  "gpt-5",
			Source:          largebody.NewSourceDigest(digestOf(2)),
			BodyBytes:       1 << 20,
			RewrittenLength: 0,
		},
		Session: largebody.SessionResponseCarrier{
			AuthoritativeSessionID: "sess-1",
			ALegID:                 "a-leg-1",
			ResumeToken:            largebody.NewSensitiveString("lip-resume-1"),
		},
	}
	if err := result.Validate(testFactBudget); err != nil {
		t.Fatalf("valid execution result rejected: %v", err)
	}
	nilStream := result
	nilStream.Stream = nil
	if err := nilStream.Validate(testFactBudget); err == nil {
		t.Fatal("execution result without a stream must be rejected")
	}
	oversize := result
	facts := oversize.Facts
	facts.EffectiveModel = strings.Repeat("m", testFactBudget+1)
	oversize.Facts = facts
	if err := oversize.Validate(testFactBudget); err == nil {
		t.Fatal("oversize effective model must be rejected")
	}
	missingRequest := result
	facts = missingRequest.Facts
	facts.RequestID = ""
	missingRequest.Facts = facts
	if err := missingRequest.Validate(testFactBudget); err == nil {
		t.Fatal("response facts without request identity must be rejected")
	}
}

func TestSessionCarrier_NeverLogsSecrets(t *testing.T) {
	const token = "lip-resume-token-secret"
	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess-9",
		ALegID:                 "a-leg-9",
		ResumeToken:            largebody.NewSensitiveString(token),
	}
	for name, rendered := range map[string]string{
		"SprintfV": fmt.Sprintf("%v", carrier),
		"SprintfS": fmt.Sprintf("%s", carrier),
		"SprintfQ": fmt.Sprintf("%q", carrier),
		"GoString": carrier.GoString(),
		"String":   carrier.String(),
	} {
		for _, secret := range []string{token, "sess-9", "a-leg-9"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("%s leaks carrier value %q: %q", name, secret, rendered)
			}
		}
	}
	raw, err := json.Marshal(carrier)
	if err != nil {
		t.Fatalf("carrier MarshalJSON error = %v", err)
	}
	for _, secret := range []string{token, "sess-9", "a-leg-9"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("carrier JSON leaks %q: %s", secret, raw)
		}
	}
	if err := carrier.Validate(testFactBudget); err != nil {
		t.Fatalf("valid carrier rejected: %v", err)
	}
}

func TestSessionInput_MarshalRedactsResumeToken(t *testing.T) {
	const token = "lip-resume-token-secret"
	input := largebody.SessionInput{
		AuthoritativeSessionID: "sess-1",
		ResumeToken:            largebody.NewSensitiveString(token),
		NewSessionRequested:    true,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("session input MarshalJSON error = %v", err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("session input JSON leaks resume token: %s", raw)
	}
	if err := input.Validate(testFactBudget); err != nil {
		t.Fatalf("valid session input rejected: %v", err)
	}
	oversize := input
	oversize.AuthoritativeSessionID = strings.Repeat("s", testFactBudget+1)
	if err := oversize.Validate(testFactBudget); err == nil {
		t.Fatal("oversize session ID must be rejected")
	}
}

func TestPackage_HasNoSmugglingVectors(t *testing.T) {
	structs := []reflect.Type{
		reflect.TypeOf(largebody.Span{}),
		reflect.TypeOf(largebody.RewriteSemantics{}),
		reflect.TypeOf(largebody.SensitiveString{}),
		reflect.TypeOf(largebody.ProtocolFacts{}),
		reflect.TypeOf(largebody.SessionInput{}),
		reflect.TypeOf(largebody.ClientTurnPartShape{}),
		reflect.TypeOf(largebody.ClientTurnItemShape{}),
		reflect.TypeOf(largebody.ClientTurnShape{}),
		reflect.TypeOf(largebody.IdentityDigest{}),
		reflect.TypeOf(largebody.SourceDigest{}),
		reflect.TypeOf(largebody.Proof{}),
		reflect.TypeOf(largebody.AssessmentRequest{}),
		reflect.TypeOf(largebody.AssessmentResult{}),
		reflect.TypeOf(largebody.AssessmentStamp{}),
		reflect.TypeOf(largebody.WireRequestFacts{}),
		reflect.TypeOf(largebody.WireDomainFacts{}),
		reflect.TypeOf(largebody.RewritePlan{}),
		reflect.TypeOf(largebody.ExecutionResult{}),
		reflect.TypeOf(largebody.ResponseFacts{}),
		reflect.TypeOf(largebody.SessionResponseCarrier{}),
	}
	allowedContentNames := map[string]bool{
		"ContentBytes": true, "TotalContentBytes": true, "ControlCount": true,
		"BodyBytes": true, "BodyMode": true, "bodyBytes": true,
	}
	for _, typ := range structs {
		if strings.Contains(typ.Name(), "Call") {
			t.Fatalf("type %s mirrors lipapi.Call; DTOs must stay narrowly scoped", typ.Name())
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath == "" && field.Type.Kind() == reflect.Map {
				t.Fatalf("type %s field %s is an unbounded map", typ.Name(), field.Name)
			}
			lower := strings.ToLower(field.Name)
			switch {
			// Note: "file" is intentionally absent from this denylist because
			// it false-positives on ProfileID; temp-file smuggling is covered
			// by temp/path/dir plus the no-map rule and the import ban below.
			case strings.Contains(lower, "header"),
				strings.Contains(lower, "prompt"),
				strings.Contains(lower, "temp"),
				strings.Contains(lower, "path"),
				strings.Contains(lower, "dir"):
				t.Fatalf("type %s field %s smuggles forbidden state", typ.Name(), field.Name)
			case strings.Contains(lower, "text") ||
				strings.Contains(lower, "content") ||
				strings.Contains(lower, "body"):
				if !allowedContentNames[field.Name] {
					t.Fatalf("type %s field %s smuggles payload content", typ.Name(), field.Name)
				}
			}
		}
	}
}
