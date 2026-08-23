package reasoningpreservation

import (
	"encoding/json"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// CompressorAuxRequestParams groups control-plane lineage plus sanitized segments.
type CompressorAuxRequestParams struct {
	Route               string
	ParentTraceID       string
	ParentALegID        string
	ParentBLegID        string
	ParentBranchBinding string
	Segments            []CompressorInputSegment
}

// BuildCompressorAuxRequest builds a detached no-tools auxiliary request for compression.
// Control-plane metadata stays in the envelope; model-visible Call.Messages contain only sanitized segments.
func BuildCompressorAuxRequest(p CompressorAuxRequestParams) auxiliary.Request {
	// Build sanitized payload JSON for model input: only local indexes and sanitized text.
	type wireSeg struct {
		Index int    `json:"index"`
		Text  string `json:"text"`
	}
	type wireReq struct {
		SchemaVersion int       `json:"schema_version"`
		Segments      []wireSeg `json:"segments"`
	}
	w := wireReq{SchemaVersion: 1}
	for _, s := range p.Segments {
		w.Segments = append(w.Segments, wireSeg{Index: s.Index, Text: s.Text})
	}
	// Marshal of a struct with only string/int fields cannot fail; fall back to an
	// empty payload rather than propagating an impossible error.
	payload, err := json.Marshal(w)
	textPayload := string(payload)
	if err != nil {
		textPayload = "{}"
	}

	call := &lipapi.Call{
		Messages: []lipapi.Message{
			{
				Role: lipapi.RoleSystem,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: "compress reasoning: semantic preservation"},
				},
			},
			{
				Role: lipapi.RoleUser,
				Parts: []lipapi.Part{
					{Kind: lipapi.PartText, Text: textPayload},
				},
			},
		},
		Route: lipapi.RouteIntent{Selector: p.Route},
		// No Tools: compressor must not have side effects.
	}
	return auxiliary.Request{
		Role:                "reasoning_preservation_compressor",
		Visibility:          "private",
		SessionMode:         auxiliary.SessionModeDetached,
		ParentTraceID:       p.ParentTraceID,
		ParentALegID:        p.ParentALegID,
		ParentBLegID:        p.ParentBLegID,
		ParentBranchBinding: p.ParentBranchBinding,
		DisablePlugins:      []string{ID},
		Call:                call,
	}
}
