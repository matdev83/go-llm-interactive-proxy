package jsonshape_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonshape"
)

func FuzzPreflight(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"x":1}`),
		[]byte(`{`),
		[]byte(`[[[[[[[[[[0]]]]]]]]]]`),
		[]byte(`[` + strings.Repeat(`1,`, 128) + `1]`),
		[]byte(`"` + strings.Repeat(`a`, 4096) + `"`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`123456789012345678901234567890`),
		[]byte("\"\xff\""),
		[]byte(`{}{}`),
		[]byte(`null`),
		[]byte(`{"outer":{"x":1,"x":2}}`),
		[]byte(`[1,2,3]`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		result, err := jsonshape.Preflight(data, jsonshape.RequestEnvelopeLimits())
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "\xff") || looksLikePayloadLeak(msg, data) {
				t.Fatalf("error appears to leak payload: %q", msg)
			}
			return
		}
		if !json.Valid(data) {
			t.Fatalf("Preflight succeeded but encoding/json.Valid returned false")
		}
		if result.Bytes != len(data) || result.Tokens <= 0 || result.MaxDepth < 0 {
			t.Fatalf("unexpected result: %+v", result)
		}
	})
}

func looksLikePayloadLeak(msg string, data []byte) bool {
	if len(data) < 12 {
		return false
	}
	sample := string(data)
	if len(sample) > 24 {
		sample = sample[4:24]
	}
	if sample == "" || sample == "{" || sample == "[" {
		return false
	}
	return strings.Contains(msg, sample)
}
