// Package systemonecompat adapts the System One wire to canonical decisions.
package systemonecompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const maxResponseBytes = 1 << 20

type wireHTTPError struct {
	status int
	body   []byte
}

func (e *wireHTTPError) Error() string {
	return "System One upstream returned HTTP " + strconv.Itoa(e.status)
}

func evaluateWire(ctx context.Context, client *http.Client, endpoint, key, model string, decision lipapi.DecisionRequest, safeHeaders http.Header) ([]lipapi.Event, error) {
	if err := decision.Validate(); err != nil {
		return nil, err
	}
	body := decisionBody(model, decision)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for name, values := range safeHeaders {
		switch strings.ToLower(name) {
		case "http-referer", "x-title":
			req.Header[name] = append([]string(nil), values...)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, lipapi.RecoverablePreOutputError(errors.New("system one response exceeds its bound or could not be read"))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &wireHTTPError{status: resp.StatusCode, body: data}
	}
	events, err := responseEvents(data, decision)
	if err != nil {
		// Never propagate decoded upstream content or a parser's payload fragment.
		return nil, lipapi.RecoverablePreOutputError(errors.New("invalid System One answer"))
	}
	return events, nil
}

// decisionBody appends validated raw JSON without re-encoding evidence. Objects
// are written in canonical slice order, including the choice criteria keys.
func decisionBody(model string, decision lipapi.DecisionRequest) []byte {
	var out bytes.Buffer
	out.WriteString(`{"model":`)
	writeString(&out, model)
	out.WriteString(`,"state":`)
	out.Write(decision.Evidence)
	out.WriteString(`,"questions":{`)
	for i, q := range decision.Questions {
		if i > 0 {
			out.WriteByte(',')
		}
		writeString(&out, q.ID)
		out.WriteString(`:{"type":`)
		writeString(&out, string(q.Kind))
		if q.Instructions != nil {
			out.WriteString(`,"instructions":`)
			out.Write(q.Instructions)
		}
		switch q.Kind {
		case lipapi.DecisionKindNoul:
			if q.TrueCriteria != nil || q.FalseCriteria != nil {
				out.WriteString(`,"criteria":{`)
				if q.TrueCriteria != nil {
					out.WriteString(`"true":`)
					out.Write(q.TrueCriteria)
				}
				if q.FalseCriteria != nil {
					if q.TrueCriteria != nil {
						out.WriteByte(',')
					}
					out.WriteString(`"false":`)
					out.Write(q.FalseCriteria)
				}
				out.WriteByte('}')
			}
		case lipapi.DecisionKindChoice:
			out.WriteString(`,"criteria":{`)
			for j, option := range q.Options {
				if j > 0 {
					out.WriteByte(',')
				}
				writeString(&out, option.Name)
				out.WriteByte(':')
				if len(option.Description) == 0 {
					out.WriteString("null")
				} else {
					out.Write(option.Description)
				}
			}
			out.WriteByte('}')
		case lipapi.DecisionKindScore:
			out.WriteString(`,"criteria":[`)
			for j, level := range q.Levels {
				if j > 0 {
					out.WriteByte(',')
				}
				out.Write(level)
			}
			out.WriteByte(']')
		}
		out.WriteByte('}')
	}
	out.WriteString(`}}`)
	return out.Bytes()
}

func writeString(out *bytes.Buffer, value string) {
	raw, _ := json.Marshal(value) // string marshaling cannot fail
	out.Write(raw)
}

type wireAnswer struct {
	Type          lipapi.DecisionKind `json:"type"`
	Noul          *float64            `json:"noul"`
	Choice        string              `json:"choice"`
	Score         *float64            `json:"score"`
	Probabilities map[string]*float64 `json:"probabilities"`
	Confidence    *float64            `json:"confidence"`
}

func responseEvents(data []byte, request lipapi.DecisionRequest) ([]lipapi.Event, error) {
	var response struct {
		Model   string                `json:"model"`
		Answers map[string]wireAnswer `json:"answers"`
		Usage   struct {
			Input  *int         `json:"input_tokens"`
			Output *int         `json:"output_tokens"`
			Cost   *json.Number `json:"cost"`
		} `json:"usage"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&response); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing response data")
	}
	if len(response.Answers) != len(request.Questions) {
		return nil, errors.New("answer count mismatch")
	}
	result := lipapi.DecisionResult{Model: response.Model}
	for _, q := range request.Questions {
		wire, ok := response.Answers[q.ID]
		if !ok {
			return nil, errors.New("missing answer")
		}
		answer := lipapi.DecisionAnswer{QuestionID: q.ID, Kind: wire.Type, Selected: wire.Choice, Confidence: wire.Confidence}
		switch q.Kind {
		case lipapi.DecisionKindNoul:
			if wire.Noul == nil {
				return nil, errors.New("missing noul probability")
			}
			answer.PTrue = *wire.Noul
		case lipapi.DecisionKindChoice:
			if len(wire.Probabilities) != len(q.Options) {
				return nil, errors.New("option count mismatch")
			}
			for _, option := range q.Options {
				p := wire.Probabilities[option.Name]
				if p == nil {
					return nil, errors.New("missing option probability")
				}
				answer.Probabilities = append(answer.Probabilities, *p)
			}
		case lipapi.DecisionKindScore:
			if wire.Score == nil || len(wire.Probabilities) != len(q.Levels) {
				return nil, errors.New("missing score or level count mismatch")
			}
			answer.Expected = *wire.Score
			for i := range q.Levels {
				p := wire.Probabilities[strconv.Itoa(i)]
				if p == nil {
					return nil, errors.New("missing level probability")
				}
				answer.Probabilities = append(answer.Probabilities, *p)
			}
		}
		result.Answers = append(result.Answers, answer)
	}
	if err := result.ValidateFor(request); err != nil {
		return nil, err
	}
	usage := lipapi.Event{Kind: lipapi.EventUsageDelta}
	if response.Usage.Input != nil {
		if *response.Usage.Input < 0 {
			return nil, errors.New("negative usage")
		}
		usage.InputTokens, usage.UsagePresence.InputTokens = *response.Usage.Input, true
	}
	if response.Usage.Output != nil {
		if *response.Usage.Output < 0 {
			return nil, errors.New("negative usage")
		}
		usage.OutputTokens, usage.UsagePresence.OutputTokens = *response.Usage.Output, true
	}
	if response.Usage.Cost != nil {
		cost, err := response.Usage.Cost.Float64()
		nano := math.Round(cost * 1e9)
		if err != nil || cost < 0 || math.IsNaN(nano) || math.IsInf(nano, 0) || nano < 0 || nano >= float64(math.MaxInt64) {
			return nil, errors.New("invalid provider cost")
		}
		usage.CostNanoUnits, usage.CostPresent = int64(nano), true
		usage.Currency, usage.CostSource = "USD", "provider_reported"
	}
	return []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventDecisionResult, Decision: &result}, usage, {Kind: lipapi.EventResponseFinished}}, nil
}
