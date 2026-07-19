package openairesponses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openairesponsesitem"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func canonExactResponsesReasoning(part *lipapi.ReasoningPart) (json.RawMessage, error) {
	if part == nil {
		return nil, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	if lipapi.NormalizeReasoningDialect(part.Dialect) != lipapi.ReasoningDialectOpenAIResponsesItemV1 {
		return nil, errReasoningDialectSkip
	}
	if len(part.Opaque) == 0 {
		return nil, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	canon, err := openairesponsesitem.CanonizeReasoningItemOpaque(part.Opaque)
	if err != nil {
		return nil, err
	}
	return canon, nil
}

var errReasoningDialectSkip = fmt.Errorf("openairesponses: reasoning dialect not representable")

func exactReasoningWireObject(part *lipapi.ReasoningPart) (map[string]any, error) {
	canon, err := canonExactResponsesReasoning(part)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(canon, &obj); err != nil {
		return nil, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	if obj == nil {
		return nil, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	return obj, nil
}

func exactReasoningAddedShell(canon json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(canon, &obj); err != nil {
		return nil, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	id := obj["id"]
	typ := obj["type"]
	if len(id) == 0 || len(typ) == 0 {
		return nil, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	shell := map[string]json.RawMessage{
		"id":      id,
		"type":    typ,
		"summary": json.RawMessage("[]"),
	}
	if status, ok := obj["status"]; ok {
		shell["status"] = status
	}
	raw, err := openairesponsesitem.MarshalEnvelope(shell)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	return json.RawMessage(raw), nil
}

func summaryTextsFromCanon(canon json.RawMessage) ([]string, error) {
	var wire struct {
		Summary []struct {
			Text string `json:"text"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(canon, &wire); err != nil {
		return nil, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	out := make([]string, 0, len(wire.Summary))
	for _, s := range wire.Summary {
		out = append(out, s.Text)
	}
	return out, nil
}

func reasoningIDFromCanon(canon json.RawMessage) (string, error) {
	var wire struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(canon, &wire); err != nil {
		return "", fmt.Errorf("openairesponses: invalid reasoning item")
	}
	id := strings.TrimSpace(wire.ID)
	if id == "" {
		return "", fmt.Errorf("openairesponses: invalid reasoning item")
	}
	return id, nil
}
