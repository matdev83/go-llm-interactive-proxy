package lipapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func mkDecisionNoulQuestion(id string) lipapi.DecisionQuestion {
	return lipapi.DecisionQuestion{
		ID:           id,
		Kind:         lipapi.DecisionKindNoul,
		Instructions: json.RawMessage(`"decide whether this is acceptable"`),
		TrueCriteria: json.RawMessage(`{"rationale":"acceptable"}`),
	}
}

func mkDecisionChoiceQuestion(id string, options ...string) lipapi.DecisionQuestion {
	q := lipapi.DecisionQuestion{
		ID:           id,
		Kind:         lipapi.DecisionKindChoice,
		Instructions: json.RawMessage(`"pick one region"`),
	}
	for _, opt := range options {
		q.Options = append(q.Options, lipapi.DecisionOption{Name: opt})
	}
	return q
}

func mkDecisionScoreQuestion(id string, levels int) lipapi.DecisionQuestion {
	q := lipapi.DecisionQuestion{
		ID:           id,
		Kind:         lipapi.DecisionKindScore,
		Instructions: json.RawMessage(`"grade the urgency"`),
	}
	for i := range levels {
		q.Levels = append(q.Levels, json.RawMessage(fmt.Sprintf(`"level-%d"`, i)))
	}
	return q
}

func mkDecisionRequest() lipapi.DecisionRequest {
	return lipapi.DecisionRequest{
		Evidence: json.RawMessage(`{"prompt":"ship the release"}`),
		Questions: []lipapi.DecisionQuestion{
			mkDecisionNoulQuestion("is-ok"),
			mkDecisionChoiceQuestion("which-region", "eu", "us"),
			mkDecisionScoreQuestion("how-urgent", 3),
		},
	}
}

func mkDecisionResult() lipapi.DecisionResult {
	return lipapi.DecisionResult{
		Model: "jev-latest",
		Answers: []lipapi.DecisionAnswer{
			{QuestionID: "is-ok", Kind: lipapi.DecisionKindNoul, PTrue: 0.9},
			{QuestionID: "which-region", Kind: lipapi.DecisionKindChoice, Selected: "us", Probabilities: []float64{0.25, 0.75}},
			{QuestionID: "how-urgent", Kind: lipapi.DecisionKindScore, Expected: 1.2, Probabilities: []float64{0.2, 0.5, 0.3}},
		},
	}
}

func assertDecisionValidationError(t *testing.T, err error, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected rejection naming field %q, got nil", wantField)
	}
	ve, ok := errors.AsType[*lipapi.ValidationError](err)
	if !ok {
		t.Fatalf("error type = %T (%v), want *lipapi.ValidationError", err, err)
	}
	if ve.Field != wantField {
		t.Fatalf("ValidationError.Field = %q (message %q), want %q", ve.Field, ve.Message, wantField)
	}
	if ve.Message == "" {
		t.Fatalf("ValidationError for %q carries no message", wantField)
	}
	if !errors.Is(err, lipapi.ErrInvalidCall) {
		t.Fatalf("error %v must wrap ErrInvalidCall", err)
	}
}

func TestDecisionRequestValidate_acceptsThreeKinds(t *testing.T) {
	t.Parallel()

	if err := mkDecisionRequest().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a noul, choice and score question", err)
	}
}

func TestDecisionRequestValidate_acceptsAbsentOptionalFields(t *testing.T) {
	t.Parallel()

	req := lipapi.DecisionRequest{
		Evidence: json.RawMessage(`"plain string evidence"`),
		Questions: []lipapi.DecisionQuestion{
			{ID: "noul", Kind: lipapi.DecisionKindNoul},
			{ID: "choice", Kind: lipapi.DecisionKindChoice, Options: []lipapi.DecisionOption{
				{Name: "only", Description: json.RawMessage(`null`)},
			}},
			{ID: "score", Kind: lipapi.DecisionKindScore, Levels: []json.RawMessage{
				json.RawMessage(`"low"`), json.RawMessage(`{"label":"high"}`),
			}},
		},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil when optional fields are absent", err)
	}
}

func TestDecisionRequestValidate_rejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	manyOptions := make([]lipapi.DecisionOption, lipapi.MaxDecisionChoiceOptions+1)
	for i := range manyOptions {
		manyOptions[i] = lipapi.DecisionOption{Name: fmt.Sprintf("opt-%03d", i)}
	}
	longOptionName := strings.Repeat("n", lipapi.MaxDecisionOptionNameBytes+1)

	cases := []struct {
		name  string
		req   lipapi.DecisionRequest
		field string
	}{
		{
			name:  "missing evidence",
			req:   lipapi.DecisionRequest{Questions: []lipapi.DecisionQuestion{mkDecisionNoulQuestion("q")}},
			field: "Evidence",
		},
		{
			name:  "malformed evidence json",
			req:   lipapi.DecisionRequest{Evidence: json.RawMessage(`{"prompt":`), Questions: []lipapi.DecisionQuestion{mkDecisionNoulQuestion("q")}},
			field: "Evidence",
		},
		{
			name:  "no questions",
			req:   lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`)},
			field: "Questions",
		},
		{
			name: "empty question id",
			req: lipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []lipapi.DecisionQuestion{mkDecisionNoulQuestion("")},
			},
			field: "Questions[0].ID",
		},
		{
			name: "oversized question id",
			req: lipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []lipapi.DecisionQuestion{mkDecisionNoulQuestion(strings.Repeat("q", lipapi.MaxDecisionQuestionIDBytes+1))},
			},
			field: "Questions[0].ID",
		},
		{
			name: "duplicate question id",
			req: lipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []lipapi.DecisionQuestion{mkDecisionNoulQuestion("dup"), mkDecisionNoulQuestion("dup")},
			},
			field: "Questions[1].ID",
		},
		{
			name: "missing kind",
			req: lipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []lipapi.DecisionQuestion{{ID: "q"}},
			},
			field: "Questions[0].Kind",
		},
		{
			name: "unknown kind",
			req: lipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []lipapi.DecisionQuestion{{ID: "q", Kind: lipapi.DecisionKind("boolean")}},
			},
			field: "Questions[0].Kind",
		},
		{
			name: "noul question with options",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				func() lipapi.DecisionQuestion {
					q := mkDecisionNoulQuestion("q")
					q.Options = []lipapi.DecisionOption{{Name: "a"}}
					return q
				}(),
			}},
			field: "Questions[0].Options",
		},
		{
			name: "noul question with levels",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				func() lipapi.DecisionQuestion {
					q := mkDecisionNoulQuestion("q")
					q.Levels = []json.RawMessage{json.RawMessage(`"low"`), json.RawMessage(`"high"`)}
					return q
				}(),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "choice question with levels",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				func() lipapi.DecisionQuestion {
					q := mkDecisionChoiceQuestion("q", "a", "b")
					q.Levels = []json.RawMessage{json.RawMessage(`"low"`), json.RawMessage(`"high"`)}
					return q
				}(),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "choice question with true criteria",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				func() lipapi.DecisionQuestion {
					q := mkDecisionChoiceQuestion("q", "a", "b")
					q.TrueCriteria = json.RawMessage(`"only for noul"`)
					return q
				}(),
			}},
			field: "Questions[0].TrueCriteria",
		},
		{
			name: "score question with false criteria",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				func() lipapi.DecisionQuestion {
					q := mkDecisionScoreQuestion("q", 3)
					q.FalseCriteria = json.RawMessage(`"only for noul"`)
					return q
				}(),
			}},
			field: "Questions[0].FalseCriteria",
		},
		{
			name: "score question with options",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				func() lipapi.DecisionQuestion {
					q := mkDecisionScoreQuestion("q", 3)
					q.Options = []lipapi.DecisionOption{{Name: "a"}}
					return q
				}(),
			}},
			field: "Questions[0].Options",
		},
		{
			name: "choice question without options",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				mkDecisionChoiceQuestion("q"),
			}},
			field: "Questions[0].Options",
		},
		{
			name:  "choice question above the option ceiling",
			req:   lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{{ID: "q", Kind: lipapi.DecisionKindChoice, Options: manyOptions}}},
			field: "Questions[0].Options",
		},
		{
			name: "duplicate option names",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				mkDecisionChoiceQuestion("q", "eu", "eu"),
			}},
			field: "Questions[0].Options[1].Name",
		},
		{
			name: "empty option name",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				mkDecisionChoiceQuestion("q", ""),
			}},
			field: "Questions[0].Options[0].Name",
		},
		{
			name: "option name above its byte bound",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				mkDecisionChoiceQuestion("q", longOptionName, "b"),
			}},
			field: "Questions[0].Options[0].Name",
		},
		{
			name: "malformed option description",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				{ID: "q", Kind: lipapi.DecisionKindChoice, Options: []lipapi.DecisionOption{
					{Name: "a", Description: json.RawMessage(`{"unterminated`)},
				}},
			}},
			field: "Questions[0].Options[0].Description",
		},
		{
			name: "score question without levels",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				mkDecisionScoreQuestion("q", 0),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "score question with one level",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				mkDecisionScoreQuestion("q", 1),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "score question above the level ceiling",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				mkDecisionScoreQuestion("q", lipapi.MaxDecisionScoreLevels+1),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "malformed level description",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				{ID: "q", Kind: lipapi.DecisionKindScore, Levels: []json.RawMessage{
					json.RawMessage(`"low"`), json.RawMessage(`[1,`),
				}},
			}},
			field: "Questions[0].Levels[1]",
		},
		{
			name: "malformed instructions",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				{ID: "q", Kind: lipapi.DecisionKindNoul, Instructions: json.RawMessage(`"unterminated`)},
			}},
			field: "Questions[0].Instructions",
		},
		{
			name: "malformed noul criteria",
			req: lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{
				{ID: "q", Kind: lipapi.DecisionKindNoul, FalseCriteria: json.RawMessage(`[1,`)},
			}},
			field: "Questions[0].FalseCriteria",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertDecisionValidationError(t, tc.req.Validate(), tc.field)
		})
	}
}

func TestDecisionResultValidateFor_acceptsMatchingResult(t *testing.T) {
	t.Parallel()

	res := mkDecisionResult()
	if err := res.ValidateFor(mkDecisionRequest()); err != nil {
		t.Fatalf("ValidateFor() = %v, want nil for a matching three-type result", err)
	}
}

func TestDecisionResultValidateFor_confidenceIsOptionalAndNeverDerived(t *testing.T) {
	t.Parallel()

	req := mkDecisionRequest()
	for _, confidence := range []*float64{nil, new(0.0), new(0.5), new(1.0)} {
		res := mkDecisionResult()
		res.Answers[1].Confidence = confidence
		if err := res.ValidateFor(req); err != nil {
			t.Fatalf("ValidateFor() with confidence %v = %v, want nil", confidence, err)
		}
	}
}

func TestDecisionResultValidateFor_acceptsEitherTiedMaximumOption(t *testing.T) {
	t.Parallel()

	req := mkDecisionRequest()
	for _, selected := range []string{"eu", "us"} {
		res := mkDecisionResult()
		res.Answers[1].Selected = selected
		res.Answers[1].Probabilities = []float64{0.5, 0.5}
		if err := res.ValidateFor(req); err != nil {
			t.Fatalf("ValidateFor() selecting tied option %q = %v, want nil", selected, err)
		}
	}
}

func TestDecisionResultValidateFor_rejectsInvalidResults(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result lipapi.DecisionResult
		field  string
	}{
		{
			name:   "missing model",
			result: lipapi.DecisionResult{Answers: mkDecisionResult().Answers},
			field:  "Model",
		},
		{
			name:   "missing answer",
			result: lipapi.DecisionResult{Model: "jev-latest", Answers: mkDecisionResult().Answers[:2]},
			field:  "Answers",
		},
		{
			name: "extra answer",
			result: lipapi.DecisionResult{Model: "jev-latest", Answers: append(mkDecisionResult().Answers,
				lipapi.DecisionAnswer{QuestionID: "extra", Kind: lipapi.DecisionKindNoul, PTrue: 0.5})},
			field: "Answers",
		},
		{
			name: "answers out of request order",
			result: lipapi.DecisionResult{Model: "jev-latest", Answers: []lipapi.DecisionAnswer{
				mkDecisionResult().Answers[1],
				mkDecisionResult().Answers[0],
				mkDecisionResult().Answers[2],
			}},
			field: "Answers[0].QuestionID",
		},
		{
			name: "duplicate answer for the same question",
			result: lipapi.DecisionResult{Model: "jev-latest", Answers: []lipapi.DecisionAnswer{
				mkDecisionResult().Answers[0],
				mkDecisionResult().Answers[0],
				mkDecisionResult().Answers[2],
			}},
			field: "Answers[1].QuestionID",
		},
		{
			name:   "answer names an unknown question",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[0].QuestionID = "is-fine" }),
			field:  "Answers[0].QuestionID",
		},
		{
			name:   "answer kind disagrees with the question",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[1].Kind = lipapi.DecisionKindNoul }),
			field:  "Answers[1].Kind",
		},
		{
			name:   "nan ptrue",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[0].PTrue = math.NaN() }),
			field:  "Answers[0].PTrue",
		},
		{
			name:   "infinite ptrue",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[0].PTrue = math.Inf(1) }),
			field:  "Answers[0].PTrue",
		},
		{
			name:   "ptrue above one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[0].PTrue = 1.5 }),
			field:  "Answers[0].PTrue",
		},
		{
			name:   "ptrue below zero",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[0].PTrue = -0.5 }),
			field:  "Answers[0].PTrue",
		},
		{
			name:   "choice probability count mismatch",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{1} }),
			field:  "Answers[1].Probabilities",
		},
		{
			name:   "nan choice probability",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{math.NaN(), 1} }),
			field:  "Answers[1].Probabilities[0]",
		},
		{
			name:   "negative choice probability",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{-0.25, 1.25} }),
			field:  "Answers[1].Probabilities[0]",
		},
		{
			name:   "choice probability above one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{1.25, -0.25} }),
			field:  "Answers[1].Probabilities[0]",
		},
		{
			name:   "choice distribution does not sum to one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{0.25, 0.5} }),
			field:  "Answers[1].Probabilities",
		},
		{
			name:   "selected option was never requested",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[1].Selected = "apac" }),
			field:  "Answers[1].Selected",
		},
		{
			name: "selected option is not the maximum",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) {
				r.Answers[1].Selected = "us"
				r.Answers[1].Probabilities = []float64{0.8, 0.2}
			}),
			field: "Answers[1].Selected",
		},
		{
			name:   "score probability count mismatch",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[2].Probabilities = []float64{0.5, 0.5} }),
			field:  "Answers[2].Probabilities",
		},
		{
			name:   "score distribution does not sum to one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[2].Probabilities = []float64{0.2, 0.5, 0.2} }),
			field:  "Answers[2].Probabilities",
		},
		{
			name:   "nan expected score",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[2].Expected = math.NaN() }),
			field:  "Answers[2].Expected",
		},
		{
			name:   "expected score below zero",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[2].Expected = -0.1 }),
			field:  "Answers[2].Expected",
		},
		{
			name:   "expected score above the top level",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) { r.Answers[2].Expected = 2.5 }),
			field:  "Answers[2].Expected",
		},
		{
			name: "nan confidence",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) {
				r.Answers[1].Confidence = new(math.NaN())
			}),
			field: "Answers[1].Confidence",
		},
		{
			name: "confidence below zero",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) {
				r.Answers[1].Confidence = new(-0.1)
			}),
			field: "Answers[1].Confidence",
		},
		{
			name: "confidence above one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *lipapi.DecisionResult) {
				r.Answers[1].Confidence = new(1.1)
			}),
			field: "Answers[1].Confidence",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertDecisionValidationError(t, tc.result.ValidateFor(mkDecisionRequest()), tc.field)
		})
	}
}

func TestIsDecisionReject_detectsClientSafeDecisionRejections(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("systemone decode: %w", &lipapi.DecisionRejectError{
		Field:   "questions[0].options",
		Message: "at most 255 options",
	})
	if !lipapi.IsDecisionReject(wrapped) {
		t.Fatal("IsDecisionReject must recognize a wrapped DecisionRejectError")
	}
	rej, ok := errors.AsType[*lipapi.DecisionRejectError](wrapped)
	if !ok {
		t.Fatalf("error type = %T, want *lipapi.DecisionRejectError", wrapped)
	}
	if rej.Field != "questions[0].options" || rej.Message != "at most 255 options" {
		t.Fatalf("DecisionRejectError = %+v, want the rejected field and bounded message", rej)
	}
	if got, want := rej.Error(), "questions[0].options: at most 255 options"; got != want {
		t.Fatalf("DecisionRejectError.Error() = %q, want %q", got, want)
	}
	if !errors.Is(wrapped, lipapi.ErrInvalidCall) {
		t.Fatal("a decision reject must unwrap to ErrInvalidCall")
	}
	if lipapi.IsReject(wrapped) {
		t.Fatal("a decision reject must not be a capability reject")
	}
	if lipapi.IsDecisionReject(&lipapi.ValidationError{Field: "Messages"}) {
		t.Fatal("IsDecisionReject must not claim canonical validation errors")
	}
	if lipapi.IsDecisionReject(nil) {
		t.Fatal("IsDecisionReject(nil) must be false")
	}
	if lipapi.IsDecisionReject(errors.New("boom")) {
		t.Fatal("IsDecisionReject must be false for an unrelated error")
	}
}

func mutateDecisionResult(res lipapi.DecisionResult, mutate func(*lipapi.DecisionResult)) lipapi.DecisionResult {
	mutate(&res)
	return res
}
