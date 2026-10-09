package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func writeResponse(ctx context.Context, w http.ResponseWriter, call *lipapi.Call, stream lipapi.EventStream) error {
	result, usage, err := collectDecision(ctx, call, stream)
	if err != nil {
		return (WireErrors{}).WriteExecuteError(w, classifyExecute(err))
	}
	answers := make([]orderedMember, len(result.Answers))
	for i, a := range result.Answers {
		question := call.Decision.Questions[i]
		value := map[string]any{"type": a.Kind}
		probabilities := make([]orderedMember, len(a.Probabilities))
		for j, probability := range a.Probabilities {
			key := strconv.Itoa(j)
			if a.Kind == lipapi.DecisionKindChoice {
				key = question.Options[j].Name
			}
			raw, err := json.Marshal(probability)
			if err != nil {
				return err
			}
			probabilities[j] = orderedMember{name: key, raw: raw}
		}
		switch a.Kind {
		case lipapi.DecisionKindNoul:
			value["noul"] = a.PTrue
		case lipapi.DecisionKindChoice:
			value["choice"] = a.Selected
			value["probabilities"] = encodeOrderedObject(probabilities)
		case lipapi.DecisionKindScore:
			value["score"] = a.Expected
			value["probabilities"] = encodeOrderedObject(probabilities)
			legend := make([]orderedMember, len(question.Levels))
			for j, level := range question.Levels {
				legend[j] = orderedMember{name: strconv.Itoa(j), raw: level}
			}
			value["legend"] = encodeOrderedObject(legend)
		}
		if a.Kind != lipapi.DecisionKindNoul && a.Confidence != nil {
			value["confidence"] = *a.Confidence
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		answers[i] = orderedMember{name: a.QuestionID, raw: raw}
	}
	return writeJSON(w, http.StatusOK, struct {
		Model   string          `json:"model"`
		Answers json.RawMessage `json:"answers"`
		Usage   map[string]int  `json:"usage"`
	}{Model: result.Model, Answers: encodeOrderedObject(answers), Usage: map[string]int{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens}})
}

func encodeOrderedObject(members []orderedMember) json.RawMessage {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, member := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(member.name)
		out.Write(key)
		out.WriteByte(':')
		out.Write(member.raw)
	}
	out.WriteByte('}')
	return out.Bytes()
}

func collectDecision(ctx context.Context, call *lipapi.Call, stream lipapi.EventStream) (result *lipapi.DecisionResult, usage lipapi.Collected, err error) {
	if stream == nil {
		return nil, usage, lipapi.ErrNilEventStream
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	if ctx == nil {
		return nil, usage, lipapi.ErrNilContext
	}
	if call == nil || call.Decision == nil {
		return nil, usage, errors.New("decision request is missing")
	}
	started := false
	for {
		event, recvErr := stream.Recv(ctx)
		if recvErr != nil {
			return nil, usage, recvErr
		}
		if event.Kind != lipapi.EventResponseStarted && !started {
			return nil, usage, errors.New("decision event before response start")
		}
		switch event.Kind {
		case lipapi.EventResponseStarted:
			if started {
				return nil, usage, errors.New("duplicate response start")
			}
			started = true
		case lipapi.EventDecisionResult:
			if result != nil || event.Decision == nil {
				return nil, usage, errors.New("duplicate or missing decision result")
			}
			if err := event.Decision.ValidateFor(*call.Decision); err != nil {
				return nil, usage, err
			}
			result = event.Decision
		case lipapi.EventUsageDelta:
			usage.AccumulateUsage(event)
		case lipapi.EventWarning:
			// Optional diagnostics do not alter the public decision response.
		case lipapi.EventResponseFinished:
			if result == nil {
				return nil, usage, errors.New("missing decision result")
			}
			return result, usage, nil
		case lipapi.EventError:
			return nil, usage, lipapi.NewStreamError(event.ErrorCode, event.ErrorMessage)
		default:
			return nil, usage, errors.New("unexpected decision stream event")
		}
	}
}
