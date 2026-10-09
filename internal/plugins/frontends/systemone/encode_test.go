package systemone

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestEncode_TypedAnswersLegendConfidenceAndUsage(t *testing.T) {
	t.Parallel()
	call, err := decodeRequest([]byte(`{"model":"jev","state":"x","questions":{"yes":{"type":"noul"},"pick":{"type":"choice","criteria":{"z":null,"a":"option"}},"grade":{"type":"score","criteria":["low","high"]}}}`), 256)
	if err != nil {
		t.Fatal(err)
	}
	result := &lipapi.DecisionResult{Model: "resolved", Answers: []lipapi.DecisionAnswer{
		{QuestionID: "yes", Kind: lipapi.DecisionKindNoul, PTrue: 0.7},
		{QuestionID: "pick", Kind: lipapi.DecisionKindChoice, Selected: "a", Probabilities: []float64{0.2, 0.8}, Confidence: new(0.9)},
		{QuestionID: "grade", Kind: lipapi.DecisionKindScore, Expected: 0.75, Probabilities: []float64{0.25, 0.75}},
	}}
	stream := lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventDecisionResult, Decision: result}, {Kind: lipapi.EventUsageDelta, InputTokens: 10, OutputTokens: 2}, {Kind: lipapi.EventResponseFinished}})
	w := httptest.NewRecorder()
	if err := writeResponse(t.Context(), w, call, stream); err != nil {
		t.Fatal(err)
	}
	want := `{"model":"resolved","answers":{"yes":{"type":"noul","noul":0.7},"pick":{"type":"choice","choice":"a","probabilities":{"z":0.2,"a":0.8},"confidence":0.9},"grade":{"type":"score","score":0.75,"legend":{"0":"low","1":"high"},"probabilities":{"0":0.25,"1":0.75}}},"usage":{"input_tokens":10,"output_tokens":2}}`
	var gotValue, wantValue any
	if json.Unmarshal(w.Body.Bytes(), &gotValue) != nil || json.Unmarshal([]byte(want), &wantValue) != nil || !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("body = %s, want %s", w.Body.String(), want)
	}
	if !strings.Contains(w.Body.String(), `"z":0.2,"a":0.8`) {
		t.Fatal("probability keys lost request order")
	}
}

func TestEncode_MissingOrInvalidResultNeverWritesSuccess(t *testing.T) {
	t.Parallel()
	call, err := decodeRequest([]byte(`{"model":"jev","state":"x","questions":{"q":{"type":"noul"}}}`), 256)
	if err != nil {
		t.Fatal(err)
	}
	for name, events := range map[string][]lipapi.Event{
		"missing":    {{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}},
		"invalid":    {{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventDecisionResult, Decision: &lipapi.DecisionResult{Model: "jev", Answers: []lipapi.DecisionAnswer{{QuestionID: "q", Kind: lipapi.DecisionKindNoul, PTrue: 2}}}}, {Kind: lipapi.EventResponseFinished}},
		"incomplete": {{Kind: lipapi.EventResponseStarted}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			if err := writeResponse(t.Context(), w, call, lipapi.NewFixedEventStream(events)); err != nil {
				t.Fatal(err)
			}
			if w.Code == 200 || strings.Contains(w.Body.String(), `"answers"`) {
				t.Fatalf("invalid result emitted success: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestError_ProtocolEnvelopes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err    error
		status int
		detail any
	}{
		{&lipapi.DecisionRejectError{Field: "body.questions", Message: "invalid questions"}, 422, []any{map[string]any{"loc": []any{"body", "questions"}, "msg": "invalid questions", "type": "value_error"}}},
		{lipapi.NewPolicyDeniedError("secret_guard", "guard", "denied", "policy", "denied", nil), 403, "denied"},
		{&lipapi.RejectError{Reason: "no eligible backend"}, 400, "no eligible backend"},
		{errors.New("private upstream content"), 500, "internal error"},
	} {
		w := httptest.NewRecorder()
		if err := (WireErrors{}).WriteExecuteError(w, classifyExecute(tc.err)); err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != tc.status || !reflect.DeepEqual(body["detail"], tc.detail) {
			t.Fatalf("error envelope = %d %s", w.Code, w.Body.String())
		}
	}
}
