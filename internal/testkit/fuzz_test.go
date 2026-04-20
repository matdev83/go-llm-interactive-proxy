package testkit

import (
	"encoding/json"
	"testing"
)

// FuzzJSONRoundTrip exercises JSON normalize/compare helpers with arbitrary payloads.
func FuzzJSONRoundTrip(f *testing.F) {
	f.Add([]byte(`{"a":1,"b":2}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return
		}
		norm, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		AssertJSONEqual(t, norm, norm)
	})
}
