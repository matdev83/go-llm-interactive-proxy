package openairesponses

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzParamsForCall(f *testing.F) {
	rawModel, err := json.Marshal("gpt-4o-mini")
	if err != nil {
		f.Fatal(err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: rawModel}
	base := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		Extensions: ext,
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "openai", Model: "gpt-4o-mini"},
		Key:     "openai:gpt-4o-mini",
	}
	f.Add([]byte(""))
	f.Add([]byte("x"))
	f.Add([]byte(`{"tool":"calls"}`))

	f.Fuzz(func(t *testing.T, suffix []byte) {
		txt := string(suffix)
		if len(txt) > 1<<16 {
			txt = txt[:1<<16]
		}
		if txt == "" {
			txt = "."
		}
		c := *base
		c.Messages = []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(txt)}}}
		_, _ = ParamsForCall(&c, cand)
	})
}
