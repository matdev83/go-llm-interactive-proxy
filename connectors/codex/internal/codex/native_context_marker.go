package codex

import (
	"bytes"
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	nativeContinuityMarkerKey   = "lip.internal.openai_codex.reasoning_continuity.v1"
	nativeContinuityMarkerValue = `{"eligible":true,"dialect":"openai.responses.reasoning_item.v1"}`
)

func consumeNativeContinuityMarker(call *lipapi.Call) bool {
	if call == nil || call.Extensions == nil {
		return false
	}
	raw, ok := call.Extensions[nativeContinuityMarkerKey]
	delete(call.Extensions, nativeContinuityMarkerKey)
	if !ok || !bytes.Equal(bytes.TrimSpace(raw), []byte(nativeContinuityMarkerValue)) {
		return false
	}
	var posture struct {
		Eligible bool   `json:"eligible"`
		Dialect  string `json:"dialect"`
	}
	if err := json.Unmarshal(raw, &posture); err != nil {
		return false
	}
	return posture.Eligible && posture.Dialect == string(lipapi.ReasoningDialectOpenAIResponsesItemV1)
}

func peekNativeContinuityMarker(call *lipapi.Call) bool {
	if call == nil || call.Extensions == nil {
		return false
	}
	raw, ok := call.Extensions[nativeContinuityMarkerKey]
	if !ok || !bytes.Equal(bytes.TrimSpace(raw), []byte(nativeContinuityMarkerValue)) {
		return false
	}
	var posture struct {
		Eligible bool   `json:"eligible"`
		Dialect  string `json:"dialect"`
	}
	if json.Unmarshal(raw, &posture) != nil {
		return false
	}
	return posture.Eligible && posture.Dialect == string(lipapi.ReasoningDialectOpenAIResponsesItemV1)
}
